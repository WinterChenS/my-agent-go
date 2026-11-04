package pi

// 测试证据的落盘规则。
//
// 背景（2026-11-04，C03 复核时发现）：F02/F04 的集成测试曾经直接写仓库里的
// docs/evidence/raw/*.json。这带来两个真实后果：
//
//  1. 一次 `verify --suite=integration` 就会把**已提交的历史运行记录**改写成
//     当前参数。交接文档里 `-ns` 从 f04-policy-integration.json 里消失、以及
//     "历史证据正在被回写"的告警，根因就是这里，不是某个 agent 手滑。
//  2. 工作区因此变脏。门禁现在按 `worktreeDirty` 拒绝这种证据——测试自己
//     制造了"证据不合法"，然后门禁判死，看起来像门禁有问题。
//
// 规则：历史证据不可变，新运行写新文件。默认只在测试日志里留结论；需要归档时
// 显式给一个绝对目录：
//
//	MYAGENT_EVIDENCE_DIR=D:\tmp\evidence go test ./internal/runtime/pi -run TestRealPiPolicy -count=1 -v

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestEvidence 按显式配置写证据；未配置时不落盘（只由调用方 t.Logf）。
func writeTestEvidence(t *testing.T, name string, body []byte) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("MYAGENT_EVIDENCE_DIR"))
	if dir == "" {
		// 默认不写：跑测试不能改变仓库内容，更不能改写已提交的证据。
		return
	}
	if !filepath.IsAbs(dir) {
		t.Fatalf("MYAGENT_EVIDENCE_DIR 必须是绝对路径（相对路径会落到包目录）: %s", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建证据目录 %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("写证据 %s: %v", path, err)
	}
	t.Logf("证据已写入 %s（%d bytes）", path, len(body))
}
