// MCP 服务配置（C04）。
//
// 定位：Pi 1.5.0 负责**外部** MCP 连接（stdio 与 Streamable HTTP）。本文件不做
// MCP 客户端，只做三件事：
//
//  1. 配置校验与 CAS 保存。配置里只有 command/args/url 与**凭证引用**，
//     没有明文令牌；command 与 args 必须分开，禁止把整条 shell 命令塞进
//     command（那等于把一个不受审计的 shell 通道接进 Pi）。
//  2. 把启用的服务编译成 Pi 的 mcp.json（写进隔离 agentDir，绝不写项目 .pi/）。
//     秘密以 ${ENV} 引用出现，真实值在启动时经子进程环境变量注入，不落盘。
//  3. 让"禁用"真的影响运行时：禁用的服务不会出现在 mcp.json 里（不是只隐藏菜单），
//     旧 run 继续用启动时的旧 mcp.json，新集合在下一个安全点（ensureRuntime 重建）
//     生效，并如实报 pending。
//
// 诊断（DiagnoseMCP）走 `pi mcp list --json`：连接的拥有者仍然是 Pi，Go 侧不建
// 第二个 MCP client；诊断会启动一个短命的 Pi 进程，这一点在 UI 与证据里明说。
package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// MCP transport 允许值。
const (
	MCPTransportStdio = "stdio"
	MCPTransportHTTP  = "http"
)

// MCP exposure 允许值（Pi 的 codemode 默认值刻意不提供：本产品只暴露这三种）。
const (
	MCPExposureDirect   = "direct"
	MCPExposureDeferred = "deferred"
	MCPExposureHidden   = "hidden"
)

// mcpConfigField 是用户配置文档里的字段名。
const mcpConfigField = "mcp"

// mcpManagedListFile 记录"上一次由产品写进 mcp.json 的外部服务名"。
//
// 有了它才能把已禁用/已删除的服务从 mcp.json 里**移除**，而不是留着
// `enabled:false` 的残条——后者一旦被别的进程改成 true 就绕过了禁用。
const mcpManagedListFile = "mcp-managed.json"

// MCPDefinition 是一条外部 MCP 服务配置。
//
// 只允许引用凭证（EnvRefs/HeaderRefs 的值是凭证库里的 ref），不允许明文秘密。
// Args 与 Command 分开：Command 只能是一个可执行文件。
type MCPDefinition struct {
	ID        string   `json:"id"`
	Transport string   `json:"transport"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	// EnvRefs 是"子进程环境变量名 → 凭证 ref"。值在启动时注入，不落盘。
	EnvRefs    map[string]string `json:"envRefs,omitempty"`
	WorkingDir string            `json:"workingDir,omitempty"`
	URL        string            `json:"url,omitempty"`
	// HeaderRefs 是"HTTP 头名 → 凭证 ref"。凭证内容是**完整的头值**
	// （例如 `Bearer abc`），因为是否带前缀属于服务端契约，产品不猜。
	HeaderRefs map[string]string `json:"headerRefs,omitempty"`
	Enabled    bool              `json:"enabled"`
	Exposure   string            `json:"exposure"`
}

// MCPSet 是一次列举的结果。
type MCPSet struct {
	Items    []MCPDefinition `json:"items"`
	Revision uint64          `json:"revision"`
}

// MCPSkip 说明某条已启用的服务为什么没有参与运行。
type MCPSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// MCPServerConfig 是一条编译后的 mcp.json 条目（不含明文秘密）。
type MCPServerConfig struct {
	Name       string   `json:"name"`
	Transport  string   `json:"transport"`
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	WorkingDir string   `json:"workingDir,omitempty"`
	URL        string   `json:"url,omitempty"`
	// Env/Headers 的值是 ${VAR} 形式的引用，不是秘密本体。
	Env      map[string]string `json:"env,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Exposure string            `json:"exposure"`
}

// MCPPlan 是一次运行要交给 Pi 的 MCP 集合。
type MCPPlan struct {
	Servers []MCPServerConfig
	// Env 是"VAR=secret"，只经子进程环境交付。
	Env     []string
	Digest  string
	IDs     []string
	Skipped []MCPSkip
}

// PiConfig 把一条编译后的服务转成 mcp.json 的条目形状。
func (c MCPServerConfig) PiConfig() map[string]any {
	out := map[string]any{}
	if c.Transport == MCPTransportStdio {
		out["command"] = c.Command
		if len(c.Args) > 0 {
			out["args"] = append([]string{}, c.Args...)
		}
		if c.WorkingDir != "" {
			out["cwd"] = c.WorkingDir
		}
		if len(c.Env) > 0 {
			env := map[string]any{}
			for k, v := range c.Env {
				env[k] = v
			}
			out["env"] = env
		}
	} else {
		out["url"] = c.URL
		if len(c.Headers) > 0 {
			headers := map[string]any{}
			for k, v := range c.Headers {
				headers[k] = v
			}
			out["headers"] = headers
		}
	}
	if c.Exposure != "" {
		out["exposure"] = c.Exposure
	}
	return out
}

// mcpServerNamePattern 与 Pi 的服务器名规则一致：字母、数字、_、-。
//
// 这个名字会成为 mcp.json 的键与工具名 mcp__<server>__<tool> 的一部分，
// 放宽到任意字符会让"哪个服务被调用"在日志里无法辨认。
var mcpServerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

var mcpEnvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var mcpHeaderNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,127}$`)

// shellWrapperFlags 列出"会把参数当命令字符串执行"的解释器。
//
// 这不是黑名单美学：`cmd /c ...`、`bash -lc ...` 会把整串命令交给 shell，
// 之后 Pi 的进程树、策略层的资源范围与副作用审计都只能看见外层解释器。
var shellWrapperFlags = map[string][]string{
	"cmd":        {"/c", "/k", "-c"},
	"bash":       {"-c", "-lc", "-ic", "-ec", "-xc"},
	"sh":         {"-c", "-lc", "-ic", "-ec", "-xc"},
	"zsh":        {"-c", "-lc", "-ic", "-ec", "-xc"},
	"dash":       {"-c", "-lc", "-ic", "-ec", "-xc"},
	"powershell": {"-c", "-command", "-encodedcommand", "-ec"},
	"pwsh":       {"-c", "-command", "-encodedcommand", "-ec"},
}

// mcpSecretEnvName 由凭证 ref 派生注入用的环境变量名。
func mcpSecretEnvName(ref string) string { return CredentialEnvName(ref) }

// SetWorkspaceRoot 指定 MCP workingDir 允许的第二根（第一根是 app home）。
func (s *Service) SetWorkspaceRoot(p string) {
	p = strings.TrimSpace(p)
	if p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			p = filepath.Clean(abs)
		}
	}
	s.mcpMu.Lock()
	s.workspaceRoot = p
	s.mcpMu.Unlock()
}

// SaveMCPCredential 把 MCP 使用的凭证写入 OS 凭证库。配置里只存 ref。
//
// 与模型密钥同一条纪律：保护存储不可用时拒绝保存，不回退明文文件。
func (s *Service) SaveMCPCredential(ctx context.Context, ref, secret string) error {
	ref = strings.TrimSpace(ref)
	if !credentialRefPattern.MatchString(ref) {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"凭证引用名非法: "+ref+"（只允许小写字母、数字、点、下划线、连字符，且以字母或数字开头）", false)
	}
	if strings.TrimSpace(secret) == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "凭证内容为空", false)
	}
	if s.creds == nil {
		return contracts.NewAppError(contracts.CodeCredentialStoreUnavailable,
			"没有可用的 OS 凭证库，拒绝保存 MCP 凭证（不回退明文）", false)
	}
	return s.creds.Put(ctx, ref, []byte(secret))
}

// ListMCP 返回全部 MCP 配置。
func (s *Service) ListMCP(ctx context.Context) (MCPSet, error) {
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return MCPSet{}, err
	}
	items, err := decodeMCPDefinitions(doc.Body)
	if err != nil {
		return MCPSet{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return MCPSet{Items: items, Revision: doc.Revision}, nil
}

// GetMCP 读取单条配置。
func (s *Service) GetMCP(ctx context.Context, id string) (MCPDefinition, error) {
	set, err := s.ListMCP(ctx)
	if err != nil {
		return MCPDefinition{}, err
	}
	for _, it := range set.Items {
		if it.ID == strings.TrimSpace(id) {
			return it, nil
		}
	}
	return MCPDefinition{}, contracts.NewAppError(contracts.CodeNotFound, "MCP 服务不存在: "+id, false)
}

// SaveMCP 保存一条 MCP 配置（CAS）。
//
// 保存过程只有校验与写配置：不启动任何进程、不执行 command、不访问 url。
// 隐藏测试就是靠这一点：命令指向会写 marker 的脚本，保存后 marker 不能出现。
func (s *Service) SaveMCP(ctx context.Context, expectedRevision uint64, def MCPDefinition) (contracts.ConfigDocument, error) {
	normalized, err := s.normalizeMCP(ctx, def)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return contracts.ConfigDocument{}, contracts.NewAppError(contracts.CodeInternal,
			"序列化 MCP 配置失败: "+err.Error(), false)
	}
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	body, err := upsertEncoded(doc.Body, mcpConfigField, normalized.ID, encoded)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	return s.store.CompareAndSwap(ctx, config.KeyUser, expectedRevision, body)
}

// SetMCPEnabled 启用/禁用一条服务（CAS）。
func (s *Service) SetMCPEnabled(ctx context.Context, id string, expectedRevision uint64, enabled bool) (contracts.ConfigDocument, error) {
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	items, err := decodeMCPDefinitions(doc.Body)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	idx := -1
	for i := range items {
		if items[i].ID == strings.TrimSpace(id) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return contracts.ConfigDocument{}, contracts.NewAppError(contracts.CodeNotFound, "MCP 服务不存在: "+id, false)
	}
	items[idx].Enabled = enabled
	encoded, err := json.Marshal(items[idx])
	if err != nil {
		return contracts.ConfigDocument{}, contracts.NewAppError(contracts.CodeInternal,
			"序列化 MCP 配置失败: "+err.Error(), false)
	}
	body, err := upsertEncoded(doc.Body, mcpConfigField, items[idx].ID, encoded)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	return s.store.CompareAndSwap(ctx, config.KeyUser, expectedRevision, body)
}

// DeleteMCP 删除一条配置（CAS）。
func (s *Service) DeleteMCP(ctx context.Context, expectedRevision uint64, id string) (contracts.ConfigDocument, error) {
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	body, found, err := removeEncoded(doc.Body, mcpConfigField, strings.TrimSpace(id))
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	if !found {
		return contracts.ConfigDocument{}, contracts.NewAppError(contracts.CodeNotFound, "MCP 服务不存在: "+id, false)
	}
	return s.store.CompareAndSwap(ctx, config.KeyUser, expectedRevision, body)
}

// CompileMCP 计算本次运行要写进 mcp.json 的服务集合。
//
// 只包含"启用 + 凭证可解析"的服务；已启用但凭证缺失的进 Skipped，
// 由运行/诊断如实说明原因，而不是悄悄少一条。
func (s *Service) CompileMCP(ctx context.Context, workspaceID string) (MCPPlan, error) {
	_ = workspaceID
	set, err := s.ListMCP(ctx)
	if err != nil {
		return MCPPlan{}, err
	}
	plan := MCPPlan{}
	envSeen := map[string]string{}
	for _, def := range set.Items {
		if !def.Enabled {
			continue
		}
		cfg, env, err := s.compileMCP(ctx, def)
		if err != nil {
			plan.Skipped = append(plan.Skipped, MCPSkip{ID: def.ID, Reason: messageOf(err)})
			continue
		}
		conflict := false
		for _, kv := range env {
			name, value, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			if prev, seen := envSeen[name]; seen && prev != value {
				plan.Skipped = append(plan.Skipped, MCPSkip{ID: def.ID,
					Reason: "环境变量 " + name + " 被多条配置引用到不同凭证；请改用不同的 ref"})
				conflict = true
				break
			}
			envSeen[name] = value
		}
		if conflict {
			continue
		}
		plan.Servers = append(plan.Servers, cfg)
		plan.IDs = append(plan.IDs, def.ID)
		plan.Env = append(plan.Env, env...)
	}
	sort.Slice(plan.Servers, func(i, j int) bool { return plan.Servers[i].Name < plan.Servers[j].Name })
	sort.Strings(plan.IDs)
	// 凭证轮换也必须触发重建：摘要里只放值的哈希，不放明文。
	sort.Strings(plan.Env)
	plan.Digest = mcpPlanDigest(plan.Servers, plan.Env)
	return plan, nil
}

// compileMCP 把一条定义变成 mcp.json 条目，并把凭证解析成环境变量。
func (s *Service) compileMCP(ctx context.Context, def MCPDefinition) (MCPServerConfig, []string, error) {
	cfg := MCPServerConfig{
		Name:       def.ID,
		Transport:  def.Transport,
		Command:    def.Command,
		Args:       append([]string{}, def.Args...),
		WorkingDir: def.WorkingDir,
		URL:        def.URL,
		Exposure:   def.Exposure,
	}
	var env []string
	resolve := func(refs map[string]string, namePattern *regexp.Regexp, kind string) (map[string]string, error) {
		if len(refs) == 0 {
			return nil, nil
		}
		out := map[string]string{}
		names := make([]string, 0, len(refs))
		for n := range refs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			if !namePattern.MatchString(name) {
				return nil, contracts.NewAppError(contracts.CodeUnsupported,
					"非法的"+kind+"名: "+name, false)
			}
			ref := strings.TrimSpace(refs[name])
			if s.creds == nil {
				return nil, contracts.NewAppError(contracts.CodeCredentialStoreUnavailable,
					"没有可用的 OS 凭证库，无法解析 "+kind+" "+name+" 的凭证引用", false)
			}
			secret, err := s.creds.Get(ctx, ref)
			if err != nil {
				return nil, contracts.NewAppError(contracts.CodeDependencyMissing,
					kind+" "+name+" 引用的凭证 "+ref+" 不可用: "+messageOf(err), false)
			}
			varName := mcpSecretEnvName(ref)
			out[name] = "${" + varName + "}"
			env = append(env, varName+"="+string(secret))
		}
		return out, nil
	}
	envCfg, err := resolve(def.EnvRefs, mcpEnvNamePattern, "环境变量")
	if err != nil {
		return MCPServerConfig{}, nil, err
	}
	headerCfg, err := resolve(def.HeaderRefs, mcpHeaderNamePattern, "HTTP 头")
	if err != nil {
		return MCPServerConfig{}, nil, err
	}
	cfg.Env = envCfg
	cfg.Headers = headerCfg
	return cfg, env, nil
}

// mcpPlanDigest 是"这一轮实际交给 Pi 的 MCP 集合"的摘要。
// 只含非秘密字段（命令、参数、地址、引用名、暴露策略）。
func mcpPlanDigest(servers []MCPServerConfig, env []string) string {
	h := sha256.New()
	for _, s := range servers {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%v\x00%v\x00",
			s.Name, s.Transport, s.Command, s.URL, s.WorkingDir, s.Exposure, sortedPairs(s.Env), sortedPairs(s.Headers))
		for _, a := range s.Args {
			fmt.Fprintf(h, "%s\x00", a)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func sortedPairs(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// ApplyMCPPlan 把编译结果写进 agentDir/mcp.json。
//
// 三条纪律：
//   - 只写隔离 agentDir，不碰项目 .pi/mcp.json；
//   - 上一次由产品写入、这一次不在集合里的服务必须被**移除**（禁用真的生效）；
//   - 文件里只有 ${ENV} 引用，秘密经 plan.Env 在启动时注入。
//
// harness 等由别的写入者维护的条目原样保留：这里只动自己记过账的名字。
func ApplyMCPPlan(agentDir string, plan MCPPlan) error {
	if strings.TrimSpace(agentDir) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "写入 mcp.json 失败: agentDir 为空", false)
	}
	path := filepath.Join(agentDir, "mcp.json")
	root, err := readMCPJSON(path)
	if err != nil {
		return err
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		if root["mcpServers"] != nil {
			return contracts.NewAppError(contracts.CodeConfigConflict,
				"已有 mcp.json 的 mcpServers 不是对象，拒绝覆盖", false)
		}
		servers = map[string]any{}
	}
	prev, err := readManagedMCPNames(agentDir)
	if err != nil {
		return err
	}
	for _, name := range prev {
		delete(servers, name)
	}
	names := make([]string, 0, len(plan.Servers))
	for _, cfg := range plan.Servers {
		servers[cfg.Name] = cfg.PiConfig()
		names = append(names, cfg.Name)
	}
	root["mcpServers"] = servers
	if err := writeJSONAtomic(path, root); err != nil {
		return err
	}
	return writeManagedMCPNames(agentDir, names)
}

// readMCPJSON 读取 mcp.json；不存在时返回空对象。
// 损坏的文件直接报错，不"修好再写"——那会静默丢掉别人的配置。
func readMCPJSON(path string) (map[string]any, error) {
	root := map[string]any{}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return root, nil
		}
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取 mcp.json 失败: "+err.Error(), false)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return root, nil
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, contracts.NewAppError(contracts.CodeConfigConflict,
			"已有 mcp.json 不是合法 JSON，拒绝覆盖以免丢失原配置: "+err.Error(), false)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

func readManagedMCPNames(agentDir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(agentDir, mcpManagedListFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取 MCP 管理清单失败: "+err.Error(), false)
	}
	var doc struct {
		Managed []string `json:"managed"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, contracts.NewAppError(contracts.CodeConfigConflict,
			"MCP 管理清单不是合法 JSON，拒绝继续（避免留下无法清理的残留服务）: "+err.Error(), false)
	}
	return doc.Managed, nil
}

func writeManagedMCPNames(agentDir string, names []string) error {
	if names == nil {
		names = []string{}
	}
	data, err := json.MarshalIndent(map[string]any{"managed": names}, "", "  ")
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "序列化管理清单失败: "+err.Error(), false)
	}
	data = append(data, '\n')
	return writeJSONAtomic(filepath.Join(agentDir, mcpManagedListFile), json.RawMessage(data))
}

// writeJSONAtomic 写临时文件再 rename：Pi 不会读到写了一半的 mcp.json。
func writeJSONAtomic(path string, v any) error {
	var data []byte
	switch x := v.(type) {
	case json.RawMessage:
		data = []byte(x)
	default:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return contracts.NewAppError(contracts.CodeInternal, "序列化 "+filepath.Base(path)+" 失败: "+err.Error(), false)
		}
		data = append(b, '\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "创建目录失败: "+err.Error(), false)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "写入临时文件失败: "+err.Error(), false)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return contracts.NewAppError(contracts.CodeInternal, "提交 "+filepath.Base(path)+" 失败: "+err.Error(), false)
	}
	return nil
}

func decodeMCPDefinitions(body json.RawMessage) ([]MCPDefinition, error) {
	raws, err := rawField(body, mcpConfigField)
	if err != nil {
		return nil, err
	}
	out := make([]MCPDefinition, 0, len(raws))
	for _, raw := range raws {
		var def MCPDefinition
		if err := json.Unmarshal(raw, &def); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "MCP 配置解析失败: "+err.Error(), false)
		}
		if strings.TrimSpace(def.ID) == "" {
			return nil, contracts.NewAppError(contracts.CodeInternal, "MCP 配置缺少 id", false)
		}
		out = append(out, def)
	}
	return out, nil
}

// normalizeMCP 校验并归一化一条配置。
//
// 所有拒绝都带可读错误码：错误码决定界面走哪条分支（改配置 / 换凭证 / 看文档），
// 把"command 里带了 shell"与"URL 不是 http"混成一句"配置无效"等于让用户猜。
func (s *Service) normalizeMCP(ctx context.Context, def MCPDefinition) (MCPDefinition, error) {
	if err := ctx.Err(); err != nil {
		return MCPDefinition{}, err
	}
	def.ID = strings.TrimSpace(def.ID)
	def.Transport = strings.ToLower(strings.TrimSpace(def.Transport))
	def.Command = strings.TrimSpace(def.Command)
	def.URL = strings.TrimSpace(def.URL)
	def.WorkingDir = strings.TrimSpace(def.WorkingDir)
	def.Exposure = strings.ToLower(strings.TrimSpace(def.Exposure))

	if !mcpServerNamePattern.MatchString(def.ID) {
		return MCPDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"MCP 服务 ID 非法: "+def.ID+"（只允许字母、数字、下划线、连字符，且以字母或数字开头）", false)
	}
	if def.Command != "" && def.URL != "" {
		return MCPDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"不能同时给出 command 与 url：stdio 用 command/args，HTTP 用 url", false)
	}
	switch def.Transport {
	case "":
		switch {
		case def.Command != "":
			def.Transport = MCPTransportStdio
		case def.URL != "":
			def.Transport = MCPTransportHTTP
		default:
			return MCPDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
				"缺少 transport：stdio 需要 command，http 需要 url", false)
		}
	case "sse":
		return MCPDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"旧的 SSE transport 已废弃，Pi 1.5.0 不再接受；请改用 http（Streamable HTTP，通常路径是 /mcp）", false)
	case MCPTransportStdio, MCPTransportHTTP:
	default:
		return MCPDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"未知 transport: "+def.Transport+"（只允许 stdio / http）", false)
	}

	// 暴露策略。空值按 direct 处理：本产品不提供 Pi 的默认 codemode，
	// 显式写 direct 才能让模型直接看到工具（与自有 MCP 的做法一致）。
	switch def.Exposure {
	case "":
		def.Exposure = MCPExposureDirect
	case MCPExposureDirect, MCPExposureDeferred, MCPExposureHidden:
	default:
		return MCPDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"未知 exposure: "+def.Exposure+"（只允许 direct / deferred / hidden）", false)
	}

	args, err := normalizeMCPArgs(def.Args)
	if err != nil {
		return MCPDefinition{}, err
	}
	def.Args = args

	switch def.Transport {
	case MCPTransportStdio:
		if def.Command == "" {
			return MCPDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
				"stdio 服务必须给出 command（可执行文件）；参数放进 args，不要拼成一整条命令", false)
		}
		if err := validateMCPCommand(def.Command, def.Args); err != nil {
			return MCPDefinition{}, err
		}
		if len(def.HeaderRefs) > 0 {
			return MCPDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
				"stdio 服务不接受 headerRefs；HTTP 头只属于 http 传输（stdio 用 envRefs）", false)
		}
		if def.URL != "" {
			return MCPDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
				"stdio 服务不能同时给出 url", false)
		}
	case MCPTransportHTTP:
		if def.URL == "" {
			return MCPDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
				"http 服务必须给出 url", false)
		}
		if err := validateMCPURL(def.URL); err != nil {
			return MCPDefinition{}, err
		}
		if def.Command != "" || len(def.Args) > 0 || def.WorkingDir != "" || len(def.EnvRefs) > 0 {
			return MCPDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
				"http 服务不接受 command/args/workingDir/envRefs；这些只属于 stdio（HTTP 用 headerRefs）", false)
		}
	}

	if def.Transport == MCPTransportStdio {
		dir, err := s.resolveMCPWorkingDir(def.WorkingDir)
		if err != nil {
			return MCPDefinition{}, err
		}
		def.WorkingDir = dir
	}

	envRefs, err := validateMCPRefs("环境变量", def.EnvRefs, mcpEnvNamePattern)
	if err != nil {
		return MCPDefinition{}, err
	}
	def.EnvRefs = envRefs
	headerRefs, err := validateMCPRefs("HTTP 头", def.HeaderRefs, mcpHeaderNamePattern)
	if err != nil {
		return MCPDefinition{}, err
	}
	def.HeaderRefs = headerRefs

	// 引用必须能在凭证库里解析，否则保存成功也跑不起来。
	// 凭证库不可用时**拒绝**带引用的配置：假装保存成功等于把失败推迟到运行时。
	if (len(envRefs) > 0 || len(headerRefs) > 0) && s.creds == nil {
		return MCPDefinition{}, contracts.NewAppError(contracts.CodeCredentialStoreUnavailable,
			"没有可用的 OS 凭证库，无法保存带凭证引用的 MCP 配置（不回退明文）", false)
	}
	if s.creds != nil {
		for _, ref := range append(refValues(envRefs), refValues(headerRefs)...) {
			if _, err := s.creds.Get(ctx, ref); err != nil {
				return MCPDefinition{}, contracts.NewAppError(contracts.CodeDependencyMissing,
					"凭证引用 "+ref+" 不存在；请先保存该凭证再保存这条 MCP 配置（配置里不允许写明文）", false)
			}
		}
	}
	return def, nil
}

func refValues(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func normalizeMCPArgs(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.ContainsAny(a, "\x00\r\n") {
			return nil, contracts.NewAppError(contracts.CodeUnsupported,
				"args 里不能包含换行或 NUL", false)
		}
		out = append(out, a)
	}
	return out, nil
}

// validateMCPCommand 拦截"把整条命令塞进 command"与交给 shell 解释的写法。
func validateMCPCommand(command string, args []string) error {
	if strings.ContainsAny(command, "\x00\r\n") {
		return contracts.NewAppError(contracts.CodeUnsupported, "command 不能包含换行或 NUL", false)
	}
	if strings.ContainsAny(command, "|&;<>`") || strings.Contains(command, "$(") {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"command 里出现了 shell 语义字符（| & ; < > ` $(）：只能填一个可执行文件，参数放进 args", false)
	}
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(command), filepath.Ext(command)))
	if flags, isShell := shellWrapperFlags[base]; isShell {
		for _, a := range args {
			lower := strings.ToLower(strings.TrimSpace(a))
			for _, f := range flags {
				if lower == f || (strings.HasPrefix(base, "bash") && strings.HasPrefix(lower, "-") &&
					strings.Contains(lower, "c") && len(lower) <= 4) {
					return contracts.NewAppError(contracts.CodeUnsupported,
						"不要把整条命令交给 shell（"+base+" "+a+"）：command 只填可执行文件，命令与参数分开写进 args", false)
				}
			}
		}
	}
	// 空格只允许出现在"确实存在的绝对路径"里（Windows 的 Program Files）。
	// 其它带空格的值按整串命令行处理——那正是 `"npx -y foo"` 的症状。
	if strings.ContainsAny(command, " \t") {
		if !(filepath.IsAbs(command) && pathIsFile(command)) {
			return contracts.NewAppError(contracts.CodeUnsupported,
				"command 只能是单个可执行文件，不能是整条命令: "+command+"；请拆成 command 与 args", false)
		}
	}
	return nil
}

func pathIsFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// validateMCPURL 只接受 http/https，且不接受内嵌凭证或查询参数里的秘密。
func validateMCPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "url 无法解析: "+err.Error(), false)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"url 只支持 http/https（只允许 Streamable HTTP）: "+raw, false)
	}
	if u.Host == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "url 缺少主机名: "+raw, false)
	}
	if u.User != nil {
		return contracts.NewAppError(contracts.CodeUnauthorized,
			"url 不能内嵌用户名或密码；请改用 headerRefs 引用凭证", false)
	}
	for key := range u.Query() {
		if secretKeyPattern.MatchString(key) {
			return contracts.NewAppError(contracts.CodeUnauthorized,
				"url 的查询参数 "+key+" 看起来是密钥；请改用 headerRefs 引用凭证", false)
		}
	}
	return nil
}

// validateMCPRefs 校验"名字 → 凭证引用"：
//   - 名字合法；
//   - 值必须是凭证引用名，不能是明文秘密（Bearer ...、sk-...、JWT 等）。
func validateMCPRefs(kind string, refs map[string]string, namePattern *regexp.Regexp) (map[string]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(refs))
	for name, ref := range refs {
		name = strings.TrimSpace(name)
		ref = strings.TrimSpace(ref)
		if !namePattern.MatchString(name) {
			return nil, contracts.NewAppError(contracts.CodeUnsupported,
				"非法的"+kind+"名: "+name, false)
		}
		if looksLikePlaintextSecret(ref) {
			return nil, contracts.NewAppError(contracts.CodeUnauthorized,
				kind+" "+name+" 的值看起来是明文凭证；这里只接受凭证引用名（原文请先保存进凭证库）", false)
		}
		if !credentialRefPattern.MatchString(ref) {
			return nil, contracts.NewAppError(contracts.CodeUnsupported,
				"凭证引用名非法: "+ref+"（只允许小写字母、数字、点、下划线、连字符，且以字母或数字开头）", false)
		}
		out[name] = ref
	}
	return out, nil
}

// looksLikePlaintextSecret 识别常见的"把令牌直接写进配置"的形态。
//
// 这不是完整的秘密扫描器，而是把最容易被误填的几种形态挡住：
// 带 Bearer 前缀、常见 token 前缀、JWT、PEM、带空白/换行、以及超过 ref 长度的串。
func looksLikePlaintextSecret(v string) bool {
	if v == "" {
		return false
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return true
	}
	lower := strings.ToLower(v)
	for _, prefix := range []string{"bearer", "token", "sk-", "pk-", "rk-", "ghp_", "gho_", "ghu_",
		"github_pat_", "xoxb-", "xoxp-", "xapp-", "akia", "aiza", "ya29.", "eyj", "-----begin"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return len(v) > 64
}

// resolveMCPWorkingDir 把 workingDir 钉在"工作区或托管目录"之内。
func (s *Service) resolveMCPWorkingDir(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if !filepath.IsAbs(raw) {
		return "", contracts.NewAppError(contracts.CodeUnsupported,
			"workingDir 必须是绝对路径: "+raw, false)
	}
	clean := filepath.Clean(raw)
	s.mcpMu.RLock()
	root := s.layout.Root()
	workspace := s.workspaceRoot
	s.mcpMu.RUnlock()
	roots := []string{}
	if root != "" {
		roots = append(roots, root)
	}
	if workspace != "" {
		roots = append(roots, workspace)
	}
	if len(roots) == 0 {
		return "", contracts.NewAppError(contracts.CodeConfigMissing,
			"尚未配置工作区根，无法校验 workingDir: "+raw, false)
	}
	if !insideAnyRoot(clean, roots) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"workingDir 必须在工作区或产品托管目录内: "+clean, false)
	}
	// 路径存在时用真实路径再判一次：junction / 符号链接可以把 Clean 后的
	// 字符串留在根内、真实目录却在根外。
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		if !insideAnyRoot(real, roots) {
			return "", contracts.NewAppError(contracts.CodeUnauthorized,
				"workingDir 经链接后落在允许范围之外: "+real, false)
		}
		if info, statErr := os.Stat(real); statErr == nil && !info.IsDir() {
			return "", contracts.NewAppError(contracts.CodeUnsupported,
				"workingDir 必须是目录: "+clean, false)
		}
	}
	return clean, nil
}

func insideAnyRoot(p string, roots []string) bool {
	p = filepath.Clean(p)
	for _, root := range roots {
		root = filepath.Clean(root)
		if root == "" {
			continue
		}
		if strings.EqualFold(p, root) {
			return true
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		return true
	}
	return false
}

// mcpNow 便于测试固定时间戳文案（目前只用于诊断时间）。
func mcpNow() time.Time { return time.Now() }
