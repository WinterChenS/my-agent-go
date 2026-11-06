package resources

// 技能加载的真实性验证（2026-11-04 方向调整后）。
//
// 方向：**不再抑制** Pi 的用户级技能自动发现（不再用 -ns）。`~/.agents/skills`
// （以及 `<agentDir>/skills`、用户配置里列出的技能）由 Pi 自己发现并加载——
// “本地技能直接同步过来”。项目级 `<workspace>/.pi/skills` 与祖先目录
// `.agents/skills` 不在此列：它们仍然被 `-na`（--no-approve）切断（2026-11-04
// 真实 Pi 1.5.0 正反两向测量，见 skills_discovery_integration_test.go）。
// 托管目录里启用的技能通过 `--skill` 额外显式加载（托管目录不在发现路径上）。
//
// 三条不变量分别验证：
//   - 启用的托管技能：通过 --skill 显式加载，系统提示里能看到它的 description 与托管路径。
//     （TestEnabledSkillAppearsInRealPiPrompt）
//   - 禁用的托管技能：不进 --skill；托管目录不在 Pi 的发现路径上，所以下一轮看不到它。
//     （TestDisabledManagedSkillAbsentInNextRun）
//   - 用户级自动发现副本：即使同名托管技能被禁用也仍会出现（按设计同步）；
//     且同名时自动发现副本会压过 --skill（Pi 保留先发现的同名技能）。
//     （skills_discovery_integration_test.go / TestSameNameAutoCopyWinsOverManagedSkill）
//
// 为什么必须真起 Pi：我们做的所有事情（--skill、跳过禁用项）最终都表现为
// "Pi 的系统提示里有没有这一条"。用假 worker 测不了这一点——那测的是我们自己
// 拼的参数字符串，而不是 Pi 拿到参数后真的加载了什么。
//
// fixture provider 只替代模型端点：Pi 仍然是真实进程，走真实的资源解析。
// 需要 MYAGENT_PI_WORKER_INTEGRATION=1。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/runtime/pi"
)

// skillPromptBody 起一次真实 Pi 会话，返回 fixture provider 收到的第一份请求体。
//
// 只看第一份：它就是 system prompt 被组装好、第一次发给模型的那一次。
func skillPromptBody(t *testing.T, svc *Service, skills []string, work, home string) string {
	t.Helper()
	return skillPromptRun(t, svc, skills, work, home).Prompt
}

// skillPromptRun 与 skillPromptBody 相同，但把整份测量产物（请求体 + argv）交给调用方。
// RES02 要落盘 provider 收到的原文与真实 argv，只拿到 Prompt 字符串做不到。
func skillPromptRun(t *testing.T, svc *Service, skills []string, work, home string) promptRun {
	t.Helper()
	if os.Getenv("MYAGENT_PI_WORKER_INTEGRATION") != "1" {
		t.Skip("设置 MYAGENT_PI_WORKER_INTEGRATION=1 才跑真实 Pi 的技能加载测试")
	}

	spec := pi.IsolationSpec{Skills: skills}
	return skillPromptWithArgs(t, probeInstallation(t), spec.Args(), work, home)
}

// promptRun 是一次真实 Pi 启动的原始产物：fixture provider 收到的首份请求体
// （含完整 system prompt）与真实交给 Pi 的 argv（供证据记录，不靠猜）。
type promptRun struct {
	Prompt string
	Argv   []string
}

// skillPromptWithArgs 是真正的测量装置：isoArgs 是交给 Pi 的隔离/资源参数，
// 其余（fixture provider、agentDir、工具集、prompt 流程）保持与产品一致。
//
// 单独抽出来是为了能测"换掉某个参数后 Pi 的行为如何变化"（例如把 -na 换成 -a
// 做阳性对照，证明 sentinel 本身是可被发现的），而不必复制整段启动流程。
func skillPromptWithArgs(t *testing.T, inst pi.PiInstallation, isoArgs []string, work, home string) promptRun {
	t.Helper()

	sim := &providerSim{mode: "ok"}
	srv := httptest.NewServer(sim)
	t.Cleanup(srv.Close)

	agentDir := filepath.Join(work, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatalf("建 agentDir: %v", err)
	}
	pm, err := CompileLegacy(ModelDefinition{
		ID:            "m-skill",
		ProviderID:    "probe",
		ModelID:       "probe-model",
		API:           "openai-completions",
		BaseURL:       srv.URL + "/v1",
		InputKinds:    []string{"text"},
		ContextWindow: 8192,
		MaxOutput:     1024,
	}, "")
	if err != nil {
		t.Fatalf("编译 models.json: %v", err)
	}
	if err := WritePiModels(agentDir, pm); err != nil {
		t.Fatalf("写 models.json: %v", err)
	}

	args := append([]string{"--mode", "rpc"}, isoArgs...)
	// 必须给一个 read/bash 工具：Pi 只在存在"能读取技能文件的工具"时才把
	// <available_skills> 注入系统提示（skillFileReadTool 判空）。用 --no-tools
	// 测技能是否加载，会得到一个永远为"没加载"的假结论。
	args = append(args, "--no-session", "--tools", "read",
		"--provider", "probe", "--model", "probe-model")
	// argv 走产品自己的 BuildLaunch，而不是在测试里再拼一次：
	// .cmd/.ps1 启动器会被包成 cmd.exe /c 或 powershell -File，手拼的 argv
	// 会和真正 exec 的那条不一致，证据就成了"测试以为的参数"。
	launchExe, launchArgs, err := pi.BuildLaunch(inst, args...)
	if err != nil {
		t.Fatalf("构造 Pi 启动参数: %v", err)
	}
	argv := append([]string{launchExe}, launchArgs...)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	env := append([]string{}, pm.Env...)
	// 把 home 指到临时目录，才有机会验证"用户级 ~/.agents/skills 里的副本"。
	// 这条通道按设计开放（不再用 -ns 抑制），副本应当被 Pi 自动发现并加载。
	env = append(env, "HOME="+home, "USERPROFILE="+home)

	w, err := pi.StartWorker(ctx, pi.WorkerSpec{
		Installation: inst,
		WorkingDir:   work,
		AgentDir:     agentDir,
		Env:          env,
		Args:         args,
	})
	if err != nil {
		t.Fatalf("启动 Pi: %v", err)
	}
	defer func() {
		closeCtx, cc := context.WithTimeout(context.Background(), 15*time.Second)
		defer cc()
		_ = w.Close(closeCtx)
	}()

	if _, err := w.Call(ctx, json.RawMessage(`{"type":"set_auto_retry","enabled":false}`)); err != nil {
		t.Fatalf("关自动重试: %v", err)
	}
	msg, _ := json.Marshal(map[string]string{"type": "prompt", "message": "回一句 ok"})
	if _, err := w.Call(ctx, msg); err != nil {
		t.Fatalf("发 prompt: %v", err)
	}

	deadline := time.After(45 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("等不到模型响应")
		case ev, ok := <-w.Events():
			if !ok {
				tail, _ := w.StderrTail()
				code, _ := w.ExitCode()
				t.Fatalf("Pi 提前退出（code=%d）: %s", code, clip(tail, 400))
			}
			if ev.Type == "message_end" {
				reqs := sim.requests()
				if len(reqs) == 0 {
					continue
				}
				return promptRun{Prompt: reqs[0], Argv: argv}
			}
		}
	}
}

// promptText 把所有字符串字段拼起来，便于在"JSON 转义过的请求体"里断言。
//
// 直接对原始 body 做 Contains 会漏判 Windows 路径：JSON 里反斜杠是 \\，
// 于是"路径确实在提示里"也会被判成不含。这类假失败比漏判更浪费人。
func promptText(t *testing.T, body string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("请求体不是 JSON: %v", err)
	}
	var b strings.Builder
	var walk func(any)
	walk = func(node any) {
		switch x := node.(type) {
		case string:
			b.WriteString(x)
			b.WriteString("\n")
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			for _, item := range x {
				walk(item)
			}
		}
	}
	walk(v)
	return b.String()
}

// newFixtureService 造一个带托管目录的资源服务。
func newFixtureService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	store, err := config.NewStore(filepath.Join(root, "config"))
	if err != nil {
		t.Fatalf("创建配置存储: %v", err)
	}
	return New(store, credentials.NewMemStore(), WithLayout(NewLayout(root))), root
}

// TestEnabledSkillAppearsInRealPiPrompt 证明"启用"这件事真的传到了 Pi。
func TestEnabledSkillAppearsInRealPiPrompt(t *testing.T) {
	svc, _ := newFixtureService(t)
	ctx := context.Background()
	const sentinel = "SENTINEL-C03-MANAGED-ENABLED-7f3a"

	src := writeSourceSkill(t, "loadme", skillDocForTest("loadme-skill", sentinel), nil)
	if _, err := svc.ImportSkill(ctx, src, ScopeUser, ""); err != nil {
		t.Fatalf("导入: %v", messageOf(err))
	}
	plan, err := svc.CompileSkills(ctx, "ws-default")
	if err != nil {
		t.Fatalf("编译: %v", messageOf(err))
	}
	if len(plan.Paths) != 1 {
		t.Fatalf("应有 1 个技能参与运行，实际 %v", plan.IDs)
	}

	work := t.TempDir()
	home := t.TempDir()
	prompt := promptText(t, skillPromptBody(t, svc, plan.Paths, work, home))

	if !strings.Contains(prompt, sentinel) {
		t.Fatalf("系统提示里没有该技能的 description，Pi 没有加载它。片段: %s", clip(prompt, 800))
	}
	if !strings.Contains(prompt, "loadme-skill") {
		t.Fatalf("系统提示里没有技能名。片段: %s", clip(prompt, 800))
	}
	// 名字与描述必须是我们解析出来的那一份——两套解析器一旦分叉，
	// 界面说的和 Pi 加载的就不是同一个技能。
	if !strings.Contains(prompt, plan.Paths[0]) {
		t.Fatalf("系统提示里的 location 应是托管路径 %s。片段: %s", plan.Paths[0], clip(prompt, 800))
	}
}

// TestDisabledManagedSkillAbsentInNextRun 钉住 2026-11-04 方向调整后的核心不变量：
// 禁用一条"纯托管"技能后，下一轮真实 Pi 的系统提示里看不到它。
//
// 为什么对"纯托管"技能成立：禁用 → 不进 --skill；托管目录又不 Pi 的自动发现路径上，
// Pi 不会自己发现它。两条一起决定它不出现。
//
// 为什么不再断言"同名自动发现副本也消失"：方向调整后 Pi 的用户级技能自动发现
// 保持打开（不再用 -ns 抑制），同名副本**会被 Pi 加载**——这是"本地技能同步过来"的
// 预期，不再算绕过。项目级 .pi/skills 与祖先 .agents/skills 仍由 -na 切断，
// 这三层行为由 skills_discovery_integration_test.go 用真实 Pi 分别钉住。
func TestDisabledManagedSkillAbsentInNextRun(t *testing.T) {
	svc, root := newFixtureService(t)
	ctx := context.Background()
	const managed = "SENTINEL-C03-MANAGED-DISABLED-11bb"

	// 托管的那一份：先导入、再禁用（模拟"用户点了关闭"）。
	src := writeSourceSkill(t, "shut", skillDocForTest("shut-skill", managed), nil)
	if _, err := svc.ImportSkill(ctx, src, ScopeUser, ""); err != nil {
		t.Fatalf("导入: %v", messageOf(err))
	}

	work := t.TempDir()
	home := t.TempDir()

	set, err := svc.ListSkills(ctx)
	if err != nil {
		t.Fatalf("列举: %v", messageOf(err))
	}
	if _, err := svc.SetSkillEnabled(ctx, "shut-skill", set.Revision, false); err != nil {
		t.Fatalf("禁用: %v", messageOf(err))
	}
	plan, err := svc.CompileSkills(ctx, "ws-default")
	if err != nil {
		t.Fatalf("编译: %v", messageOf(err))
	}
	if len(plan.Paths) != 0 {
		t.Fatalf("禁用后不应有托管技能参与运行，实际 %v", plan.IDs)
	}
	// 目录还在（禁用不是删除）：文件保留、配置说不加载、参数里没有它。
	if _, statErr := os.Stat(filepath.Join(root, "skills", "shut-skill", "SKILL.md")); statErr != nil {
		t.Fatalf("禁用不应删除文件: %v", statErr)
	}

	run := skillPromptRun(t, svc, plan.Paths, work, home)
	prompt := promptText(t, run.Prompt)

	// 托管的那一份（已禁用）：不在 --skill 里，且托管目录不被 Pi 自动发现 → 不出现。
	if strings.Contains(prompt, managed) {
		t.Fatalf("已禁用的托管技能仍出现在系统提示里——禁用没有生效: %s", clip(prompt, 800))
	}

	// RES02 的外部观察（source: http-server）：把 fixture provider **自己收到的**
	// 首份请求体原样落盘，事实再从这份文件读回来（不是拿上面的 prompt 变量自证）。
	if resourcesObservationDir() == "" {
		return
	}
	writeResourcesArtifact(t, "provider-request.json", map[string]any{
		"capturedBy":  "internal/resources providerSim（httptest server 收到的第一份请求体）",
		"promptBytes": len(run.Prompt),
		"argv":        run.Argv,
		"body":        run.Prompt,
	})
	artifact := readResourcesArtifact(t, "provider-request.json")
	body, _ := artifact["body"].(string)
	argv, _ := artifact["argv"].([]any)
	argvStrings := make([]string, 0, len(argv))
	for _, a := range argv {
		if s, ok := a.(string); ok {
			argvStrings = append(argvStrings, s)
		}
	}
	_, statErr := os.Stat(filepath.Join(root, "skills", "shut-skill", "SKILL.md"))
	writeResourcesObservation(t, map[string]any{
		"promptBytes":         len(body),
		"sentinelPresent":     strings.Contains(body, managed),
		"argv":                argvStrings,
		"argvLen":             len(argvStrings),
		"providerRequestFile": "provider-request.json",
		"managedSkillInArgv":  strings.Contains(strings.Join(argvStrings, " "), "shut-skill"),
		"managedDirOnDisk":    statErr == nil,
	}, nil)
}

// skillDocForTest 造一份 SKILL.md 内容。
func skillDocForTest(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n正文。\n"
}
