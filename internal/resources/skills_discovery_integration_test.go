package resources

// 真实 Pi 的技能发现边界（C03 / SEC08 / RES02）。
//
// 2026-11-04 用户指令：不抑制本地 Pi 的 skill，直接同步过来 → `-ns` 从隔离参数移除。
// 于是技能发现分成三层，本文件用真实 Pi（fixture provider 只替代模型端点，Pi 是
// 真实进程）分别钉住：
//
//	用户级  <home>/.agents/skills/<name>/SKILL.md   → 必须进系统提示（"同步"真的成立）
//	项目级  <workspace>/.pi/skills/<name>/SKILL.md  → -na 下不得进；-a 阳性对照必须进
//	祖先级  <parent>/.agents/skills/<name>/SKILL.md → 同上
//	同名    托管 --skill 与用户级副本同名            → 自动发现副本胜出（已知限制）
//
// 为什么必须有阳性对照：只看"-na 下没出现"无法区分"被切断"与"marker 本身写错 /
// 根本没被发现"。同一份文件在 -a 下出现，才能把结论锁在信任参数上。
//
// 为什么必须真起 Pi：我们验证的是"Pi 拿这套参数后到底加载了什么"，不是我们自己
// 拼的参数字符串。用假 worker 测这件事等于自证。
//
// 需要 MYAGENT_PI_WORKER_INTEGRATION=1；默认 skip。设置
// MYAGENT_C03_EVIDENCE_OUT=<path> 时把本次原始测量写成 JSON（供证据归档）。

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/runtime/pi"
)

const (
	// 每条测量用互不相同的 sentinel：否则无法区分"到底是谁被加载了"。
	sentinelUserLevel  = "SENTINEL-C03-USER-9f21c"
	sentinelProject    = "SENTINEL-C03-PROJ-4a7be"
	sentinelAncestor   = "SENTINEL-C03-ANC-c1d38"
	sentinelManaged    = "SENTINEL-C03-MANAGED-5e0b2"
	sentinelUserCopy   = "SENTINEL-C03-USERCOPY-8b3d7"
	discoveryHelpLines = `  --skill <path>                 Load a skill file or directory (can be used multiple times)
  --no-skills, -ns               Disable skills discovery and loading
  --approve, -a                  Trust project-local files for this run
  --no-approve, -na              Ignore project-local files for this run`
)

// discoveredSkill 是 Pi 系统提示 <available_skills> 里的一条。
type discoveredSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Location    string `json:"location"`
}

type discoveryRecord struct {
	Scenario            string            `json:"scenario"`
	Requirement         string            `json:"requirement"`
	TrustArg            string            `json:"trustArg"`
	ManagedSkills       []string          `json:"managedSkills,omitempty"`
	PiArgv              []string          `json:"piArgv"`
	PositiveControlArgv []string          `json:"positiveControlPiArgv,omitempty"`
	WorkingDir          string            `json:"workingDir"`
	Home                string            `json:"home"`
	Sentinels           map[string]string `json:"sentinels"`
	Expected            string            `json:"expected"`
	Observed            map[string]bool   `json:"observed"`
	AvailableSkillCount int               `json:"availableSkillCount"`
	AvailableSkills     []discoveredSkill `json:"availableSkills"`
	RawAvailableSkills  string            `json:"rawAvailableSkills"`
}

type discoveryEvidence struct {
	Command      string            `json:"command,omitempty"`
	MeasuredAt   string            `json:"measuredAt"`
	HostOS       string            `json:"hostOs"`
	HostArch     string            `json:"hostArch"`
	PiVersion    string            `json:"piVersion"`
	PiExecutable string            `json:"piExecutable"`
	PiCLI        string            `json:"piCli"`
	HelpExcerpt  string            `json:"piHelpSkillsAndTrustFlags"`
	Note         string            `json:"note"`
	Records      []discoveryRecord `json:"records"`
}

// realPiInstallationForDiscovery 找本仓库隔离安装件（.tools/pi）与基线 Node。
// 与 probeInstallation 的区别：不按 GOOS 跳过，Linux/macOS 上只要装了 node 也能跑。
func realPiInstallationForDiscovery(t *testing.T) pi.PiInstallation {
	t.Helper()
	node := strings.TrimSpace(os.Getenv("MYAGENT_NODE_PATH"))
	if node == "" {
		found, err := exec.LookPath("node")
		if err != nil {
			t.Skip("找不到 node，跳过真实 Pi 技能发现测试")
		}
		node = found
	}
	root := repoRootForTest(t)
	matches, _ := filepath.Glob(filepath.Join(root, ".tools", "pi", "*",
		"node_modules", "@earendil-works", "pi-coding-agent", "dist", "bundle", "cli.js"))
	if len(matches) == 0 {
		t.Skip("本机没有 .tools/pi 安装件，跳过真实 Pi 技能发现测试")
	}
	return pi.PiInstallation{
		ID:           "probe:" + matches[0],
		Executable:   node,
		Args:         []string{matches[0]},
		Version:      "1.5.0",
		Origin:       pi.OriginExternal,
		Architecture: runtime.GOARCH,
		Runnable:     true,
	}
}

func writeDiscoverySkill(t *testing.T, baseDir, name, description string) string {
	t.Helper()
	p := filepath.Join(baseDir, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("建技能目录: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n正文。\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("写技能: %v", err)
	}
	return p
}

// parseAvailableSkills 从系统提示里拆出 <available_skills> 段。
// 返回解析结果与原始片段（原始片段进证据，避免只留二手结论）。
func parseAvailableSkills(t *testing.T, prompt string) ([]discoveredSkill, string) {
	t.Helper()
	start := strings.Index(prompt, "<available_skills>")
	end := strings.Index(prompt, "</available_skills>")
	if start < 0 || end < start {
		return nil, ""
	}
	raw := prompt[start : end+len("</available_skills>")]
	var out []discoveredSkill
	for _, chunk := range strings.Split(raw, "<skill>")[1:] {
		out = append(out, discoveredSkill{
			Name:        xmlTagText(chunk, "name"),
			Description: xmlTagText(chunk, "description"),
			Location:    xmlTagText(chunk, "location"),
		})
	}
	return out, raw
}

func xmlTagText(chunk, tag string) string {
	open, closeTag := "<"+tag+">", "</"+tag+">"
	i := strings.Index(chunk, open)
	if i < 0 {
		return ""
	}
	rest := chunk[i+len(open):]
	j := strings.Index(rest, closeTag)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func skillsNamed(skills []discoveredSkill, name string) []discoveredSkill {
	var out []discoveredSkill
	for _, s := range skills {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

// evidenceView 只保留与 C03 测量有关的条目（带 SENTINEL-C03- 的），
// 并明确标注总数与被省略的无关条目。真实运行环境里用户可能装了几十个技能，
// 把它们的描述全录进仓库既冗长也无助于复核这条测量。
func evidenceView(raw string, skills []discoveredSkill) ([]discoveredSkill, string) {
	var marked []discoveredSkill
	for _, s := range skills {
		if strings.Contains(s.Description, "SENTINEL-C03-") {
			marked = append(marked, s)
		}
	}
	if raw == "" {
		return marked, ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "(系统提示共 %d 条技能，其中 %d 条带 SENTINEL-C03 marker；无关条目已省略)\n", len(skills), len(marked))
	b.WriteString("<available_skills>")
	for _, s := range marked {
		fmt.Fprintf(&b, "\n  <skill>\n    <name>%s</name>\n    <description>%s</description>\n    <location>%s</location>\n  </skill>", s.Name, s.Description, s.Location)
	}
	b.WriteString("\n</available_skills>")
	return marked, b.String()
}

// sameFile 比较 Pi 报的 location 与我们在临时目录里造的路径。
// 用 os.SameFile 而不是字符串相等：Windows 上同一路径可能有 8.3 短名/大小写差异。
func sameFile(t *testing.T, got, want string) bool {
	t.Helper()
	gi, err := os.Stat(got)
	if err != nil {
		return false
	}
	wi, err := os.Stat(want)
	if err != nil {
		return false
	}
	return os.SameFile(gi, wi)
}

// trustArgOf 从真实隔离参数里读回 -na/-a，写进证据。
func trustArgOf(args []string) string {
	for _, a := range args {
		if a == "-na" || a == "-a" {
			return a
		}
	}
	return ""
}

// withTrustOverride 把产品参数里的 -na 换成 -a，作为"marker 可被发现"的阳性对照。
// 只改这一处：其余隔离参数保持不变，结论才只归因于信任开关。
func withTrustOverride(t *testing.T, args []string) []string {
	t.Helper()
	out := make([]string, 0, len(args))
	replaced := false
	for _, a := range args {
		if a == "-na" {
			out = append(out, "-a")
			replaced = true
			continue
		}
		out = append(out, a)
	}
	if !replaced {
		t.Fatalf("产品隔离参数里没有 -na，无法做阳性对照: %v", args)
	}
	return out
}

func TestSkillDiscoveryLayersAgainstRealPi(t *testing.T) {
	if os.Getenv("MYAGENT_PI_WORKER_INTEGRATION") != "1" {
		t.Skip("设置 MYAGENT_PI_WORKER_INTEGRATION=1 才跑真实 Pi 的技能发现测试")
	}
	inst := realPiInstallationForDiscovery(t)

	ev := &discoveryEvidence{
		Command:      os.Getenv("MYAGENT_C03_EVIDENCE_COMMAND"),
		MeasuredAt:   time.Now().Format(time.RFC3339),
		HostOS:       runtime.GOOS,
		HostArch:     runtime.GOARCH,
		PiVersion:    inst.Version,
		PiExecutable: inst.Executable,
		PiCLI:        inst.Args[0],
		HelpExcerpt:  discoveryHelpLines,
		Note: "--help 里没有只关项目/祖先技能发现而保留用户级的参数：" +
			"只有 --no-skills（连用户级一起关）与 --no-approve（项目级信任开关）。" +
			" fixture provider 只替代模型端点，Pi 是真实进程。",
	}
	t.Cleanup(func() {
		out := strings.TrimSpace(os.Getenv("MYAGENT_C03_EVIDENCE_OUT"))
		if out == "" {
			return
		}
		b, err := json.MarshalIndent(ev, "", "  ")
		if err != nil {
			t.Logf("序列化证据失败: %v", err)
			return
		}
		b = append(b, '\n')
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Logf("写证据失败: %v", err)
			return
		}
		t.Logf("证据已写入 %s", out)
	})

	// (a) 用户级 ~/.agents/skills：按设计同步，必须进系统提示。
	t.Run("UserLevelAgentsSkillsSync", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		work := filepath.Join(root, "ws")
		if err := os.MkdirAll(work, 0o700); err != nil {
			t.Fatalf("建工作区: %v", err)
		}
		userSkill := writeDiscoverySkill(t, filepath.Join(home, ".agents", "skills"), "user-marker", sentinelUserLevel)

		isoArgs := pi.IsolationSpec{}.Args() // 产品真实参数（含 -na）
		run := skillPromptWithArgs(t, inst, isoArgs, work, home)
		prompt := promptText(t, run.Prompt)
		skills, raw := parseAvailableSkills(t, prompt)
		found := skillsNamed(skills, "user-marker")
		marked, excerpt := evidenceView(raw, skills)

		ev.Records = append(ev.Records, discoveryRecord{
			Scenario:            "用户级 .agents/skills",
			Requirement:         "T1(a) / 本地技能同步",
			TrustArg:            trustArgOf(isoArgs),
			PiArgv:              run.Argv,
			WorkingDir:          work,
			Home:                home,
			Sentinels:           map[string]string{"user": sentinelUserLevel},
			Expected:            "用户级技能出现在系统提示，location 指向 " + userSkill,
			Observed:            map[string]bool{"user_marker_present": len(found) > 0},
			AvailableSkillCount: len(skills),
			AvailableSkills:     marked,
			RawAvailableSkills:  excerpt,
		})

		if len(found) != 1 {
			t.Fatalf("用户级技能应被 Pi 自动发现（本地技能同步），实际匹配 %d 条。原文: %s",
				len(found), clip(raw, 800))
		}
		if !strings.Contains(found[0].Description, sentinelUserLevel) {
			t.Fatalf("用户级技能的 description 不是我们写的那份: %q", found[0].Description)
		}
		if !sameFile(t, found[0].Location, userSkill) {
			t.Fatalf("location 应指向用户级副本 %s，实际 %s", userSkill, found[0].Location)
		}
	})

	// (b) 项目级 <workspace>/.pi/skills：-na 下不得进；-a 阳性对照必须进。
	t.Run("ProjectPiSkillsBlockedByNoApprove", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		work := filepath.Join(root, "ws", "proj")
		if err := os.MkdirAll(work, 0o700); err != nil {
			t.Fatalf("建工作区: %v", err)
		}
		projectSkill := writeDiscoverySkill(t, filepath.Join(work, ".pi", "skills"), "proj-marker", sentinelProject)

		isoArgs := pi.IsolationSpec{}.Args()
		run := skillPromptWithArgs(t, inst, isoArgs, work, home)
		untrustedPrompt := promptText(t, run.Prompt)
		untrusted, rawUntrusted := parseAvailableSkills(t, untrustedPrompt)
		untrustedHits := skillsNamed(untrusted, "proj-marker")

		trustedArgs := withTrustOverride(t, isoArgs)
		runTrusted := skillPromptWithArgs(t, inst, trustedArgs, work, home)
		trustedPrompt := promptText(t, runTrusted.Prompt)
		trusted, rawTrusted := parseAvailableSkills(t, trustedPrompt)
		trustedHits := skillsNamed(trusted, "proj-marker")
		marked, excerpt := evidenceView(rawTrusted, trusted)

		ev.Records = append(ev.Records, discoveryRecord{
			Scenario:            "项目级 .pi/skills（-na 与 -a 对照）",
			Requirement:         "T1(b) / SEC08",
			TrustArg:            "-na vs -a",
			PiArgv:              run.Argv,
			PositiveControlArgv: runTrusted.Argv,
			WorkingDir:          work,
			Home:                home,
			Sentinels:           map[string]string{"project": sentinelProject},
			Expected:            "-na 下不出现；-a 下出现（否则 marker 本身不可被发现）",
			Observed: map[string]bool{
				"present_under_no_approve":  len(untrustedHits) > 0,
				"present_under_approve":     len(trustedHits) > 0,
				"sentinel_in_untrusted_raw": strings.Contains(untrustedPrompt, sentinelProject),
			},
			AvailableSkillCount: len(trusted),
			AvailableSkills:     marked,
			RawAvailableSkills:  "== -na ==\n" + rawUntrusted + "\n== -a ==\n" + excerpt,
		})

		if len(untrustedHits) > 0 || strings.Contains(untrustedPrompt, sentinelProject) {
			t.Fatalf("SEC08：-na 下项目级 .pi/skills 仍进了系统提示（marker %s，命中 %d 条）: %s",
				sentinelProject, len(untrustedHits), clip(rawUntrusted, 800))
		}
		if len(trustedHits) != 1 {
			t.Fatalf("阳性对照失败：-a 下项目级 marker 也没出现（命中 %d 条）。"+
				"这说明测试 fixture 本身不可被发现，不能据此断言 -na 切断了它: %s",
				len(trustedHits), clip(rawTrusted, 800))
		}
		if !sameFile(t, trustedHits[0].Location, projectSkill) {
			t.Fatalf("location 应指向项目级副本 %s，实际 %s", projectSkill, trustedHits[0].Location)
		}
	})

	// (c) 祖先目录 .agents/skills：-na 下不得进；-a 阳性对照必须进。
	t.Run("AncestorAgentsSkillsBlockedByNoApprove", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		parent := filepath.Join(root, "ws")
		work := filepath.Join(parent, "proj")
		if err := os.MkdirAll(work, 0o700); err != nil {
			t.Fatalf("建工作区: %v", err)
		}
		ancestorSkill := writeDiscoverySkill(t, filepath.Join(parent, ".agents", "skills"), "anc-marker", sentinelAncestor)

		isoArgs := pi.IsolationSpec{}.Args()
		run := skillPromptWithArgs(t, inst, isoArgs, work, home)
		untrustedPrompt := promptText(t, run.Prompt)
		untrusted, rawUntrusted := parseAvailableSkills(t, untrustedPrompt)
		untrustedHits := skillsNamed(untrusted, "anc-marker")

		trustedArgs := withTrustOverride(t, isoArgs)
		runTrusted := skillPromptWithArgs(t, inst, trustedArgs, work, home)
		trustedPrompt := promptText(t, runTrusted.Prompt)
		trusted, rawTrusted := parseAvailableSkills(t, trustedPrompt)
		trustedHits := skillsNamed(trusted, "anc-marker")
		marked, excerpt := evidenceView(rawTrusted, trusted)

		ev.Records = append(ev.Records, discoveryRecord{
			Scenario:            "祖先目录 .agents/skills（-na 与 -a 对照）",
			Requirement:         "T1(c) / SEC08",
			TrustArg:            "-na vs -a",
			PiArgv:              run.Argv,
			PositiveControlArgv: runTrusted.Argv,
			WorkingDir:          work,
			Home:                home,
			Sentinels:           map[string]string{"ancestor": sentinelAncestor},
			Expected:            "-na 下不出现；-a 下出现（否则 marker 本身不可被发现）",
			Observed: map[string]bool{
				"present_under_no_approve":  len(untrustedHits) > 0,
				"present_under_approve":     len(trustedHits) > 0,
				"sentinel_in_untrusted_raw": strings.Contains(untrustedPrompt, sentinelAncestor),
			},
			AvailableSkillCount: len(trusted),
			AvailableSkills:     marked,
			RawAvailableSkills:  "== -na ==\n" + rawUntrusted + "\n== -a ==\n" + excerpt,
		})

		if len(untrustedHits) > 0 || strings.Contains(untrustedPrompt, sentinelAncestor) {
			t.Fatalf("SEC08：-na 下祖先 .agents/skills 仍进了系统提示（marker %s，命中 %d 条）: %s",
				sentinelAncestor, len(untrustedHits), clip(rawUntrusted, 800))
		}
		if len(trustedHits) != 1 {
			t.Fatalf("阳性对照失败：-a 下祖先 marker 也没出现（命中 %d 条）。"+
				"这说明测试 fixture 本身不可被发现，不能据此断言 -na 切断了它: %s",
				len(trustedHits), clip(rawTrusted, 800))
		}
		if !sameFile(t, trustedHits[0].Location, ancestorSkill) {
			t.Fatalf("location 应指向祖先副本 %s，实际 %s", ancestorSkill, trustedHits[0].Location)
		}
	})

	// (d) 同名冲突：托管 --skill 与用户级副本同名。实测自动发现副本胜出
	// （Pi 先加载发现路径、再加载 --skill，同名保留先出现的那条）。
	// 这条把已知限制钉成测试：禁用托管技能不保证副本消失，启用也不保证覆盖副本。
	t.Run("SameNameAutoCopyWinsOverManagedSkill", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		work := filepath.Join(root, "ws")
		if err := os.MkdirAll(work, 0o700); err != nil {
			t.Fatalf("建工作区: %v", err)
		}
		managed := writeDiscoverySkill(t, filepath.Join(root, "managed"), "dup-skill", sentinelManaged)
		userCopy := writeDiscoverySkill(t, filepath.Join(home, ".agents", "skills"), "dup-skill", sentinelUserCopy)

		// 启用托管技能：--skill 传入 managed；用户级同名副本仍在。
		enabledArgs := pi.IsolationSpec{Skills: []string{managed}}.Args()
		runEnabled := skillPromptWithArgs(t, inst, enabledArgs, work, home)
		enabledPrompt := promptText(t, runEnabled.Prompt)
		enabledSkills, rawEnabled := parseAvailableSkills(t, enabledPrompt)
		enabledHits := skillsNamed(enabledSkills, "dup-skill")

		// 禁用托管技能：不传 --skill；用户级副本仍在。
		disabledArgs := pi.IsolationSpec{}.Args()
		runDisabled := skillPromptWithArgs(t, inst, disabledArgs, work, home)
		disabledPrompt := promptText(t, runDisabled.Prompt)
		disabledSkills, rawDisabled := parseAvailableSkills(t, disabledPrompt)
		disabledHits := skillsNamed(disabledSkills, "dup-skill")
		enabledMarked, enabledExcerpt := evidenceView(rawEnabled, enabledSkills)
		_, disabledExcerpt := evidenceView(rawDisabled, disabledSkills)

		ev.Records = append(ev.Records, discoveryRecord{
			Scenario:            "同名冲突：托管 --skill + 用户级副本",
			Requirement:         "T1(d) / RES02 已知限制",
			TrustArg:            "managed enabled(-na) vs managed disabled(-na)",
			ManagedSkills:       []string{managed},
			PiArgv:              runEnabled.Argv,
			PositiveControlArgv: runDisabled.Argv,
			WorkingDir:          work,
			Home:                home,
			Sentinels: map[string]string{
				"managed":  sentinelManaged,
				"userCopy": sentinelUserCopy,
			},
			Expected: "两种情况下都只出现 1 条 dup-skill，且 location 指向用户级副本；" +
				"托管那份被同名副本压过（已知限制）",
			Observed: map[string]bool{
				"enabled_entry_count_is_1":            len(enabledHits) == 1,
				"enabled_managed_sentinel_in_prompt":  strings.Contains(enabledPrompt, sentinelManaged),
				"enabled_usercopy_sentinel_in_prompt": strings.Contains(enabledPrompt, sentinelUserCopy),
				"disabled_entry_count_is_1":           len(disabledHits) == 1,
			},
			AvailableSkillCount: len(enabledSkills),
			AvailableSkills:     enabledMarked,
			RawAvailableSkills:  "== managed enabled ==\n" + enabledExcerpt + "\n== managed disabled ==\n" + disabledExcerpt,
		})

		if len(enabledHits) != 1 {
			t.Fatalf("同名时 Pi 只保留一条 dup-skill，实际 %d 条。原文: %s", len(enabledHits), clip(rawEnabled, 800))
		}
		if !sameFile(t, enabledHits[0].Location, userCopy) {
			t.Fatalf("已知限制失效（可能 Pi 升级改了加载顺序）：托管启用时 location 应指向用户级副本 %s，"+
				"实际 %s。请复核 ADR 0003 与 RES02 口径。", userCopy, enabledHits[0].Location)
		}
		if !strings.Contains(enabledHits[0].Description, sentinelUserCopy) {
			t.Fatalf("同名时生效的应是用户级副本（%s），实际 description=%q",
				sentinelUserCopy, enabledHits[0].Description)
		}
		if strings.Contains(enabledPrompt, sentinelManaged) {
			t.Fatalf("托管技能的 description 不应出现（被同名副本压过），实际出现在系统提示里: %s",
				clip(rawEnabled, 800))
		}

		if len(disabledHits) != 1 {
			t.Fatalf("托管禁用后用户级副本仍应出现且只有 1 条，实际 %d 条。原文: %s",
				len(disabledHits), clip(rawDisabled, 800))
		}
		if !sameFile(t, disabledHits[0].Location, userCopy) {
			t.Fatalf("托管禁用后 location 应指向用户级副本 %s，实际 %s", userCopy, disabledHits[0].Location)
		}
	})
}
