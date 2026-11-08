package app

// B2：带 revision 的设置保存（CAS）。
//
// 守四件事：
//  1. 过期 revision → CONFIG_CONFLICT，并且 settings.json 的**字节**不变；
//  2. 冲突时内存里的生效值不变（不能"报错但已经改了"）；
//  3. 旧 SaveSettings 仍然可用，且它也让 CAS 凭据前进（否则旧入口就成了绕过 CAS 的后门）；
//  4. 冲突结果带回磁盘上的当前 revision，调用方可以直接重读后重试。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
)

func settingsFileHash(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "settings.json"))
	if err != nil {
		t.Fatalf("读 settings.json: %v", err)
	}
	return sha256Hex(raw)
}

func TestSaveSettingsWithRevisionRejectsStaleRevision(t *testing.T) {
	d, root := newTestDesktop(t)

	first := d.SettingsWithRevision()
	if first.Error != nil {
		t.Fatalf("读设置: %+v", first.Error)
	}
	if first.Revision != 0 {
		t.Fatalf("从未保存过时 revision = %d，期望 0", first.Revision)
	}

	want := first.Settings
	want.ShellAllowed = true
	saved := d.SaveSettingsWithRevision(first.Revision, want)
	if saved.Error != nil {
		t.Fatalf("首次保存: %+v", saved.Error)
	}
	if saved.Revision == 0 {
		t.Fatal("保存后必须给出非 0 的 CAS 凭据")
	}
	// 凭据是内容派生的：重新读一遍必须得到同一个值。
	if again := d.SettingsWithRevision(); again.Revision != saved.Revision {
		t.Fatalf("重读凭据 = %d，保存返回 %d（必须是同一个文件的同一份凭据）", again.Revision, saved.Revision)
	}
	before := settingsFileHash(t, root)

	// 文件形状必须仍是"一份 Settings"：不因 CAS 多出任何键（对外 DTO 形状不变）。
	raw := mustRead(t, filepath.Join(root, "settings.json"))
	dtoKeys := map[string]json.RawMessage{}
	dto, err := json.Marshal(Settings{})
	if err != nil {
		t.Fatalf("序列化 Settings DTO: %v", err)
	}
	if err := json.Unmarshal(dto, &dtoKeys); err != nil {
		t.Fatalf("解析 Settings DTO 键: %v", err)
	}
	fileKeys := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(raw), &fileKeys); err != nil {
		t.Fatalf("解析落盘的 settings.json: %v", err)
	}
	for k := range fileKeys {
		if _, ok := dtoKeys[k]; !ok {
			t.Fatalf("settings.json 多出了 Settings 之外的键 %q（DTO 形状被改变）: %s", k, raw)
		}
	}
	var roundTrip Settings
	if err := json.Unmarshal([]byte(raw), &roundTrip); err != nil {
		t.Fatalf("落盘内容不是一份可解析的 Settings: %v", err)
	}
	if roundTrip != saved.Settings {
		t.Fatalf("落盘值与返回的设置不一致: file=%+v res=%+v", roundTrip, saved.Settings)
	}

	// 另一个窗口拿着更旧的凭据来写：必须冲突，且磁盘一个字节都不变。
	stale := want
	stale.ShellAllowed = false
	conflict := d.SaveSettingsWithRevision(first.Revision, stale)
	if conflict.Error == nil || conflict.Error.Code != contracts.CodeConfigConflict {
		t.Fatalf("过期 revision 应返回 CONFIG_CONFLICT，实际 %+v", conflict.Error)
	}
	if got := settingsFileHash(t, root); got != before {
		t.Fatalf("冲突时改写了 settings.json: before=%s after=%s", before, got)
	}
	if conflict.Revision != saved.Revision {
		t.Fatalf("冲突结果应带回磁盘上的当前凭据=%d，实际 %d", saved.Revision, conflict.Revision)
	}
	if conflict.Error.Params["expected"] == "" || conflict.Error.Params["actual"] == "" {
		t.Fatalf("冲突错误要带出期望/实际凭据（便于排查）: %+v", conflict.Error)
	}
	// 内存里的生效值不能因为一次失败而改变。
	if !d.Settings().ShellAllowed {
		t.Fatal("冲突时内存设置被改成了失败请求里的值")
	}

	// 用冲突结果里带回的凭据重试：成功，并且新凭据与磁盘对得上。
	retry := d.SaveSettingsWithRevision(conflict.Revision, stale)
	if retry.Error != nil {
		t.Fatalf("按当前凭据重试: %+v", retry.Error)
	}
	if retry.Revision == saved.Revision {
		t.Fatalf("内容变了但凭据没变（%d），下次写者会误以为还是同一份设置", retry.Revision)
	}
	if fresh := d.SettingsWithRevision().Revision; fresh != retry.Revision {
		t.Fatalf("重读凭据 = %d，重试返回 %d", fresh, retry.Revision)
	}
	if d.Settings().ShellAllowed {
		t.Fatal("重试成功后内存设置应更新为 false")
	}
	// 重启后读回磁盘上的值（CAS 不是内存假象）。
	d2, err := New(root)
	if err != nil {
		t.Fatalf("重启构造 Desktop: %v", err)
	}
	t.Cleanup(d2.Shutdown)
	if d2.Settings().ShellAllowed {
		t.Fatal("重启后 shellAllowed 应为 false")
	}
	if got := d2.SettingsWithRevision().Revision; got != retry.Revision {
		t.Fatalf("重启后 revision = %d，期望 %d", got, retry.Revision)
	}
}

func TestSaveSettingsStillWorksAndKeepsCASHonest(t *testing.T) {
	d, root := newTestDesktop(t)

	// 旧入口：不返回 revision，但必须照常落盘。
	s := d.Settings()
	s.ShellAllowed = true
	if err := d.SaveSettings(s); err != nil {
		t.Fatalf("旧 SaveSettings: %v", err)
	}
	cur := d.SettingsWithRevision()
	if cur.Revision == 0 {
		t.Fatal("旧 SaveSettings 之后文件应当有非 0 的凭据")
	}

	// 旧入口也要让 CAS 保持诚实：拿"没有文件"（0）的凭据再写必须冲突。
	stale := s
	stale.ShellAllowed = false
	if res := d.SaveSettingsWithRevision(0, stale); res.Error == nil ||
		res.Error.Code != contracts.CodeConfigConflict {
		t.Fatalf("旧入口写过后，凭据 0 的写必须冲突，实际 %+v", res.Error)
	}
	if got := settingsFileHash(t, root); got == "" {
		t.Fatal("settings.json 必须存在")
	}
	if !d.Settings().ShellAllowed {
		t.Fatal("冲突后内存设置被改写")
	}

	// 空工作目录是入参问题，不是冲突：给 INVALID_INPUT，且不动文件。
	before := settingsFileHash(t, root)
	curRev := d.SettingsWithRevision()
	in := curRev.Settings
	in.Workspace = "   "
	invalid := d.SaveSettingsWithRevision(curRev.Revision, in)
	if invalid.Error == nil || invalid.Error.Code != contracts.CodeInvalidInput {
		t.Fatalf("空工作目录应返回 INVALID_INPUT，实际 %+v", invalid.Error)
	}
	if got := settingsFileHash(t, root); got != before {
		t.Fatal("非法输入时改写了 settings.json")
	}
}

func TestSettingsRevisionSurvivesOtherSettingsWriters(t *testing.T) {
	// persistPiSelection 也会写 settings.json（选 Pi）：它必须同样换掉 CAS 凭据，
	// 否则"选完 Pi 再保存设置"会用一份过期凭据覆盖掉这次选择。
	d, root := newTestDesktop(t)
	if err := d.persistSettings(d.Settings()); err != nil {
		t.Fatalf("落盘初始设置: %v", err)
	}
	rev := d.SettingsWithRevision().Revision
	if rev == 0 {
		t.Fatal("落盘初始设置后凭据不能为 0")
	}

	if err := d.persistPiSelection(filepath.Join(root, "some-pi")); err != nil {
		t.Fatalf("persistPiSelection: %v", err)
	}
	after := d.SettingsWithRevision()
	if after.Revision == rev {
		t.Fatal("另一个写者改完 settings.json 后凭据没变（CAS 会被绕过）")
	}
	if after.Settings.PiSavedPath == "" {
		t.Fatal("选 Pi 的路径没有落盘")
	}

	s := after.Settings
	s.ShellAllowed = true
	if res := d.SaveSettingsWithRevision(rev, s); res.Error == nil ||
		res.Error.Code != contracts.CodeConfigConflict {
		t.Fatalf("用选 Pi 之前的凭据保存必须冲突，实际 %+v", res.Error)
	}
	raw := mustRead(t, filepath.Join(root, "settings.json"))
	if !strings.Contains(raw, "some-pi") {
		t.Fatalf("冲突时把上一次选定的 Pi 抹掉了: %s", raw)
	}
	if res := d.SaveSettingsWithRevision(after.Revision, s); res.Error != nil {
		t.Fatalf("用最新凭据保存: %+v", res.Error)
	}
	if !strings.Contains(mustRead(t, filepath.Join(root, "settings.json")), "some-pi") {
		t.Fatal("保存设置时丢了选定的 Pi 路径")
	}
}
