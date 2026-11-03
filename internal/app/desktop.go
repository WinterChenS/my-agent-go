// Package app 是桌面宿主与运行时之间的绑定层。
//
// 它只做三件事：把设置变成一次可执行的环境（Pi 安装件、隔离 agentDir、
// models.json、策略服务），把 UI 的调用转成 RunService 的方法调用，
// 把 RunService 的事件转成 Wails 事件。业务判断都不在这里。
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/run"
	"my-agent-go/internal/runtime/pi"
)

// EventName 是推给前端的 Wails 事件名。
const EventName = "myagent:run-event"

// Settings 是桌面端的可编辑设置。
//
// 凭证处理边界：本版把 apiKey 写进 Pi 自己的 models.json（Pi 的原生方式）。
// 后续接入 F07 的 CredentialStore 后，models.json 只留引用、不留明文。
type Settings struct {
	// PiPath 是 Pi 可执行文件；若指向 .js 入口，则用 NodePath 启动。
	PiPath string `json:"piPath"`
	// NodePath 仅在 PiPath 为 .js 时使用。
	NodePath string `json:"nodePath"`
	// Provider 是 models.json 里的 provider 键名。
	Provider string `json:"provider"`
	// BaseURL 是 OpenAI 兼容端点，例如 http://127.0.0.1:8080/v1。
	BaseURL string `json:"baseUrl"`
	// Model 是模型 id。
	Model string `json:"model"`
	// APIKey 交给 Pi 的 models.json。
	APIKey string `json:"apiKey"`
	// Workspace 是 agent 的工作目录（文件读写的边界）。
	Workspace string `json:"workspace"`
	// ShellAllowed 控制是否授予 shell 权限。
	ShellAllowed bool `json:"shellAllowed"`
}

// Status 是给 UI 的环境概览。
type Status struct {
	AppRoot      string `json:"appRoot"`
	AgentDir     string `json:"agentDir"`
	PiPath       string `json:"piPath"`
	PiVersion    string `json:"piVersion"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Workspace    string `json:"workspace"`
	Isolation    string `json:"isolation"`
	PolicyActive bool   `json:"policyActive"`
	Platform     string `json:"platform"`
}

// Desktop 是绑定到前端的服务对象。
type Desktop struct {
	root     string
	settings Settings

	mu      sync.Mutex
	svc     *run.Service
	emitter func(contracts.Event)
	lastErr string
	// policyExt 是解析到的策略扩展绝对路径，供状态展示复用。
	policyExt string
}

// New 构造 Desktop。root 是应用数据根目录。
func New(root string) (*Desktop, error) {
	d := &Desktop{root: root}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("创建应用数据目录失败: %w", err)
	}
	s, err := d.loadSettings()
	if err != nil {
		return nil, err
	}
	d.settings = s
	return d, nil
}

// SetEmitter 注入事件出口（Wails 环境下是 app.Event.Emit）。
func (d *Desktop) SetEmitter(fn func(contracts.Event)) {
	d.mu.Lock()
	d.emitter = fn
	d.mu.Unlock()
}

func (d *Desktop) settingsPath() string { return filepath.Join(d.root, "settings.json") }

func (d *Desktop) loadSettings() (Settings, error) {
	s := defaultSettings(d.root)
	if raw, err := os.ReadFile(d.settingsPath()); err == nil {
		// 用户已保存的设置优先；解析失败不静默丢弃，交由调用方看到错误。
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, fmt.Errorf("解析 settings.json 失败: %w", err)
		}
	}
	applyEnvOverrides(&s)
	return s, nil
}

// defaultSettings 给出可用的默认值：优先本机基线的 Node 与隔离安装的 Pi 1.5.0。
func defaultSettings(root string) Settings {
	s := Settings{
		Provider:  "fixture",
		BaseURL:   "http://127.0.0.1:0/v1",
		Model:     "fixture-model",
		Workspace: filepath.Join(root, "workspace"),
	}
	if runtime.GOOS == "windows" {
		if p := `D:\Program Files\nodejs\node.exe`; fileExists(p) {
			s.NodePath = p
		}
	}
	return s
}

// applyEnvOverrides 允许用环境变量驱动一次运行（自动化验证用，不改变 UI 行为）。
func applyEnvOverrides(s *Settings) {
	set := func(dst *string, keys ...string) {
		for _, k := range keys {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				*dst = v
				return
			}
		}
	}
	set(&s.PiPath, "MYAGENT_PI_PATH")
	set(&s.NodePath, "MYAGENT_NODE_PATH")
	set(&s.Provider, "MYAGENT_PROVIDER")
	set(&s.BaseURL, "MYAGENT_BASE_URL")
	set(&s.Model, "MYAGENT_MODEL")
	set(&s.APIKey, "MYAGENT_API_KEY")
	set(&s.Workspace, "MYAGENT_WORKSPACE")
	if v := strings.TrimSpace(os.Getenv("MYAGENT_SHELL")); v != "" {
		s.ShellAllowed = v == "1" || strings.EqualFold(v, "true")
	}
}

// Settings 返回当前设置（不含任何隐式改写）。
func (d *Desktop) Settings() Settings {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.settings
}

// SaveSettings 持久化设置并丢弃旧的 RunService（下次 Start 用新配置重建）。
func (d *Desktop) SaveSettings(s Settings) error {
	if strings.TrimSpace(s.Workspace) == "" {
		return fmt.Errorf("工作目录不能为空")
	}
	abs, err := filepath.Abs(s.Workspace)
	if err != nil {
		return fmt.Errorf("工作目录无法绝对化: %w", err)
	}
	s.Workspace = abs
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return fmt.Errorf("创建工作目录失败: %w", err)
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(d.settingsPath(), raw, 0o600); err != nil {
		return fmt.Errorf("写 settings.json 失败: %w", err)
	}

	d.mu.Lock()
	old := d.svc
	d.settings = s
	d.svc = nil
	d.mu.Unlock()
	if old != nil {
		_ = old.Close(context.Background())
	}
	return nil
}

// Status 返回环境概览。
func (d *Desktop) Status() Status {
	d.mu.Lock()
	s := d.settings
	ext := d.policyExt
	d.mu.Unlock()

	// 隔离参数按"实际会传给 Pi 的那一份"展示，包括显式 -e <policy ext>。
	// 只展示默认 flag 会让界面看起来比实际更严格或更宽松，两种都是误导。
	spec := pi.IsolationSpec{PolicyExtension: ext}

	st := Status{
		AppRoot:   d.root,
		AgentDir:  d.agentDir(),
		PiPath:    s.PiPath,
		Provider:  s.Provider,
		Model:     s.Model,
		Workspace: s.Workspace,
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		Isolation: strings.Join(spec.Args(), " "),
	}
	st.PolicyActive = true
	return st
}

// agentDir 返回该 workspace 的隔离 agent 目录。
func (d *Desktop) agentDir() string {
	store, err := config.NewStore(filepath.Join(d.root, "config"))
	if err != nil {
		return filepath.Join(d.root, "agents", "default")
	}
	return store.AgentDir("ws-default", "profile-default")
}

// ensureRuntime 按需构建 RunService（首次 Start 时）。
func (d *Desktop) ensureRuntime() (*run.Service, error) {
	d.mu.Lock()
	if d.svc != nil {
		svc := d.svc
		d.mu.Unlock()
		return svc, nil
	}
	s := d.settings
	emitter := d.emitter
	d.mu.Unlock()

	inst, err := d.resolveInstallation(s)
	if err != nil {
		return nil, err
	}
	agentDir := d.agentDir()
	if err := run.EnsureAgentDir(agentDir); err != nil {
		return nil, err
	}
	if err := writeModels(s, agentDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Workspace, 0o700); err != nil {
		return nil, fmt.Errorf("创建工作目录失败: %w", err)
	}

	guard, err := policy.NewPathGuard(s.Workspace)
	if err != nil {
		return nil, err
	}
	pol := policy.New(policy.Options{
		Permissions: config.Permissions{
			Filesystem: "workspace",
			Shell:      s.ShellAllowed,
			Network:    false,
		},
		PathGuard:   guard,
		ApprovalTTL: 60 * time.Second,
	})

	// 策略扩展是仓库内的 pi-extensions/harness-policy.ts；找不到就拒绝启动，
	// 而不是让 Pi 在没有执行前授权的情况下跑工具。
	ext, err := findPolicyExtension()
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.policyExt = ext
	d.mu.Unlock()

	svc := run.New(run.Options{
		WorkspaceID:  "ws-default",
		SessionID:    contracts.SessionID(fmt.Sprintf("sess-%d", time.Now().Unix())),
		WorkspaceDir: s.Workspace,
		AgentDir:     agentDir,
		Installation: inst,
		Isolation:    pi.IsolationSpec{PolicyExtension: ext},
		Provider:     s.Provider,
		Model:        s.Model,
		Policy:       pol,
	})
	if emitter != nil {
		svc.Subscribe(emitter)
	}

	d.mu.Lock()
	if d.svc == nil {
		d.svc = svc
	} else {
		svc = d.svc
	}
	d.mu.Unlock()
	return svc, nil
}

// resolveInstallation 把设置里的 Pi 路径转成可启动的安装件。
func (d *Desktop) resolveInstallation(s Settings) (pi.PiInstallation, error) {
	path := strings.TrimSpace(s.PiPath)
	if path == "" {
		return pi.PiInstallation{}, contracts.NewAppError(contracts.CodeConfigMissing,
			"未配置 Pi 路径（settings.piPath）", false)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return pi.PiInstallation{}, err
	}
	if !fileExists(abs) {
		return pi.PiInstallation{}, contracts.NewAppError(contracts.CodeConfigMissing,
			"Pi 路径不存在: "+abs, false)
	}
	inst := pi.PiInstallation{
		ID:           "local:" + abs,
		Executable:   abs,
		Version:      "1.5.0",
		Origin:       pi.OriginExternal,
		Architecture: runtime.GOARCH,
		Runnable:     true,
	}
	// .js 入口（npm 安装形式）需要 node 前缀。这不是"随便找个 node"：
	// 基线要求固定的 Node 版本，路径由设置显式给出。
	if strings.EqualFold(filepath.Ext(abs), ".js") {
		node := strings.TrimSpace(s.NodePath)
		if node == "" {
			return pi.PiInstallation{}, contracts.NewAppError(contracts.CodeConfigMissing,
				"Pi 是 .js 入口，但未配置 Node 路径", false)
		}
		if !fileExists(node) {
			return pi.PiInstallation{}, contracts.NewAppError(contracts.CodeConfigMissing,
				"Node 路径不存在: "+node, false)
		}
		inst.Executable = node
		inst.Args = []string{abs}
	}
	return inst, nil
}

// writeModels 在隔离 agentDir 里写 models.json，把 Pi 指向配置的 provider。
func writeModels(s Settings, agentDir string) error {
	if strings.TrimSpace(s.BaseURL) == "" || strings.TrimSpace(s.Model) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "缺少 provider 端点或模型 id", false)
	}
	provider := strings.TrimSpace(s.Provider)
	if provider == "" {
		provider = "fixture"
	}
	doc := map[string]any{
		"$schema": "https://pi.dev/schemas/models.schema.json",
		"providers": map[string]any{
			provider: map[string]any{
				"baseUrl": s.BaseURL,
				"api":     "openai-completions",
				"apiKey":  s.APIKey,
				"models": []map[string]any{{
					"id":            s.Model,
					"name":          s.Model,
					"input":         []string{"text"},
					"contextWindow": 128000,
					"maxTokens":     8192,
					"reasoning":     false,
					"cost":          map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
				}},
			},
		},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(agentDir, "models.json"), raw, 0o600)
}

// Start 启动一次 run。
func (d *Desktop) Start(message string) (contracts.RunSnapshot, error) {
	svc, err := d.ensureRuntime()
	if err != nil {
		d.setLastErr(err.Error())
		return contracts.RunSnapshot{}, err
	}
	snap, err := svc.Start(context.Background(), run.StartRunRequest{Message: message})
	if err != nil {
		d.setLastErr(err.Error())
		return snap, err
	}
	return snap, nil
}

// Abort 停止一次 run。
func (d *Desktop) Abort(runID string) error {
	svc, err := d.runtime()
	if err != nil {
		return err
	}
	return svc.Abort(context.Background(), contracts.RunID(runID))
}

// ResolveApproval 提交一次审批结论。
func (d *Desktop) ResolveApproval(runID, approvalID, choice string) error {
	svc, err := d.runtime()
	if err != nil {
		return err
	}
	return svc.ResolveApproval(context.Background(), contracts.RunID(runID), approvalID, choice)
}

// Replay 返回某 run 在 afterSeq 之后的事件（前端重连用）。
func (d *Desktop) Replay(runID string, afterSeq int) ([]contracts.Event, error) {
	svc, err := d.runtime()
	if err != nil {
		return nil, err
	}
	if afterSeq < 0 {
		afterSeq = 0
	}
	return svc.Replay(contracts.RunID(runID), uint64(afterSeq))
}

// LastError 返回最近一次失败原因（便于 UI 展示，不用翻日志）。
func (d *Desktop) LastError() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastErr
}

func (d *Desktop) setLastErr(s string) {
	d.mu.Lock()
	d.lastErr = s
	d.mu.Unlock()
}

func (d *Desktop) runtime() (*run.Service, error) {
	d.mu.Lock()
	svc := d.svc
	d.mu.Unlock()
	if svc == nil {
		return nil, contracts.NewAppError(contracts.CodeNotFound, "尚未启动任何 run", false)
	}
	return svc, nil
}

// Shutdown 关闭运行时。
func (d *Desktop) Shutdown() {
	d.mu.Lock()
	svc := d.svc
	d.svc = nil
	d.mu.Unlock()
	if svc != nil {
		_ = svc.Close(context.Background())
	}
}

// findPolicyExtension 定位仓库内的策略扩展。
//
// 打包后扩展随可执行文件分发；开发态从当前工作目录向上找。
func findPolicyExtension() (string, error) {
	candidates := []string{}
	if v := strings.TrimSpace(os.Getenv("MYAGENT_POLICY_EXT")); v != "" {
		candidates = append(candidates, v)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "pi-extensions", "harness-policy.ts"),
			filepath.Join(dir, "..", "pi-extensions", "harness-policy.ts"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "pi-extensions", "harness-policy.ts"),
			filepath.Join(wd, "..", "pi-extensions", "harness-policy.ts"))
	}
	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil && fileExists(abs) {
			return abs, nil
		}
	}
	return "", contracts.NewAppError(contracts.CodeConfigMissing,
		"找不到策略扩展 pi-extensions/harness-policy.ts；没有它工具调用没有执行前授权", false)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
