package app

// C03 的"运行记录到底在说什么"这条口径的绑定层测试。
//
// 2026-11-04 起 Pi 的本地技能发现保持打开（隔离参数里没有 -ns），Pi 会自己加载
// $HOME/.agents/skills、项目/祖先目录里它发现的技能。产品只知道自己显式
// --skill 传了哪些托管技能，没有 Pi 自动发现清单的可信来源。
//
// 所以快照必须分成两段事实：managedSkills（可为空数组）与 skillAutoDiscovery
// （开关状态）。旧实现只有一个 skills 列表，空数组被界面读成"未加载任何技能"——
// 自动发现开着时这是谎话。这两条测试就是钉住那个谎话。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

// trustedDesktopWithStubWorker 让 Desktop 走真实的运行时构建路径
// （隔离参数、agentDir、MCP、策略扩展都真的建出来），只把 worker 换成进程内替身。
//
// 不另起一条轻量路径：ensureRuntime 里传给 RunService 的字段正是这次要断言的东西，
// 绕过它就测不到“界面会看到什么”。
func trustedDesktopWithStubWorker(t *testing.T) *Desktop {
	t.Helper()
	d := prepareRuntimeDesktop(t)

	d.mu.Lock()
	d.workerStarter = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return newStubWorker(), nil
	}
	work := d.settings.Workspace
	d.mu.Unlock()

	ws := d.CreateWorkspace(work)
	if ws.Error != nil {
		t.Fatalf("登记工作区失败: %s: %s", ws.Error.Code, ws.Error.Message)
	}
	if res := d.SetWorkspaceTrust(string(ws.Item.ID), true); res.Error != nil {
		t.Fatalf("信任工作区失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	return d
}

// TestRunSnapshotDoesNotClaimNothingLoadedWhenAutoDiscoveryEnabled 守这条口径：
// 托管技能为空 + 自动发现开启时，快照不能说"这一轮没有加载任何技能"。
func TestRunSnapshotDoesNotClaimNothingLoadedWhenAutoDiscoveryEnabled(t *testing.T) {
	d := trustedDesktopWithStubWorker(t)

	// 没有任何导入的托管技能：这一轮不会有 --skill。
	snap, err := d.Start("你好")
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}

	if len(snap.ManagedSkills) != 0 {
		t.Fatalf("没有托管技能时 managedSkills 必须为空，实际 %v", snap.ManagedSkills)
	}
	// 空 managedSkills 与"自动发现开着"必须同时出现，且不能被合成一句"没有技能"。
	if snap.SkillAutoDiscovery != contracts.SkillDiscoveryEnabled {
		t.Fatalf("Pi 自动发现此时是开的（隔离参数里没有 -ns），快照却写 %q", snap.SkillAutoDiscovery)
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("快照不能序列化: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("快照不是合法 JSON 对象: %v\n%s", err, raw)
	}
	// 两项都必须在 JSON 里看得见：空数组是"确实没显式加载"，不是"没记录"。
	if got, ok := doc["managedSkills"]; !ok || string(got) != "[]" {
		t.Fatalf("managedSkills 应显式序列化成 []，实际 %s（存在=%v）：\n%s", got, ok, raw)
	}
	if got := string(doc["skillAutoDiscovery"]); got != `"enabled"` {
		t.Fatalf("skillAutoDiscovery 应在 JSON 里可见且为 enabled，实际 %s：\n%s", got, raw)
	}
	if got, ok := doc["skills"]; ok {
		t.Fatalf("旧的 skills 字段不应再出现（会被读成实际加载清单），实际 %s", got)
	}

	// Status 也要说同样的话：托管清单为空 + 自动发现开着。
	st := d.Status()
	if len(st.RuntimeSkills) != 0 {
		t.Fatalf("没有托管技能时 runtimeSkills 必须为空，实际 %v", st.RuntimeSkills)
	}
	if st.SkillAutoDiscovery != contracts.SkillDiscoveryEnabled {
		t.Fatalf("Status 必须暴露自动发现开关，实际 %q", st.SkillAutoDiscovery)
	}
	if strings.Contains(st.Isolation, "-ns") || strings.Contains(st.Isolation, "--no-skills") {
		t.Fatalf("状态里的隔离参数不应再抑制技能自动发现: %s", st.Isolation)
	}

	// CFG03.resources 的外部观察：落库的 run 行 + 隔离 agentDir 的磁盘树。
	observeRunIndex(t, d.root, map[string]any{"autoDiscoveryEnabled": true})
	observeTree(t, d.agentDir(), "agent-tree.json", map[string]any{"managedSkillCount": len(snap.ManagedSkills)})
}

// TestRunSnapshotRecordsManagedSkills 确认显式加载的托管技能照实进快照，
// 而不是被自动发现那一段盖过去。
func TestRunSnapshotRecordsManagedSkills(t *testing.T) {
	d := trustedDesktopWithStubWorker(t)

	if res := d.ImportSkill(fixtureSkill(t, "snap-skill", "快照口径用"), "user", ""); res.Error != nil {
		t.Fatalf("导入失败: %s: %s", res.Error.Code, res.Error.Message)
	}

	snap, err := d.Start("你好")
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if len(snap.ManagedSkills) != 1 || snap.ManagedSkills[0] != "snap-skill" {
		t.Fatalf("托管技能 ID 未进快照: %v", snap.ManagedSkills)
	}
	if snap.SkillAutoDiscovery != contracts.SkillDiscoveryEnabled {
		t.Fatalf("自动发现状态丢了: %q", snap.SkillAutoDiscovery)
	}

	st := d.Status()
	if len(st.RuntimeSkills) != 1 || st.RuntimeSkills[0] != "snap-skill" {
		t.Fatalf("Status.RuntimeSkills 应是托管且启用的清单，实际 %v", st.RuntimeSkills)
	}

	// CFG03.resources：新一次 run 的落库事实与磁盘上的托管技能文件。
	observeRunIndex(t, d.root, map[string]any{"managedSkillsRecorded": snap.ManagedSkills})
	observeTree(t, d.agentDir(), "agent-tree.json", map[string]any{"managedSkillsRecorded": snap.ManagedSkills})
}
