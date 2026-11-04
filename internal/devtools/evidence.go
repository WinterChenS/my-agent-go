package devtools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"my-agent-go/internal/contracts"
)

// ComputeEvidenceHash 计算证据目录的规范化哈希，覆盖命令、输出与结果文件。
// 门禁据此重新计算并比对 result.json.reportHash，用于发现报告被篡改（GOV02）。
// 任一文件缺失也参与哈希，保证缺失本身可被检测。
//
// 关键点：result.json 自身包含 reportHash 字段，直接哈希原始字节会形成自引用
// （写入哈希后文件就变了，哈希永远对不上）。因此对 result.json 采用"清空
// reportHash 后的规范 JSON"参与哈希；其余文件按原始字节参与。
func ComputeEvidenceHash(dir string) string {
	h := sha256.New()
	raw, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		h.Write([]byte("result.json MISSING "))
	} else {
		var ev contracts.GateEvidence
		if err := json.Unmarshal(raw, &ev); err != nil {
			// 无法解析时退回原始字节，至少保持可检测性。
			h.Write(raw)
		} else {
			ev.ReportHash = ""
			canon, _ := json.Marshal(ev)
			h.Write(canon)
		}
	}
	for _, name := range []string{"command.txt", "stdout.log", "stderr.log"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			h.Write([]byte(name + " MISSING "))
			continue
		}
		h.Write(b)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// GetSourceRevision 返回当前仓库的短 revision。
// 若 git 不可用，返回空串；此时门禁跳过 revision 一致性检查（其他检查仍生效）。
func GetSourceRevision() string {
	rev, _ := SourceIdentity()
	return rev
}

// SourceIdentity 同时返回短 revision 与「工作区是否不干净」。
//
// 只取短 HEAD 是不够的：dirty 工作区的代码不属于那个提交，证据若贴上该
// revision 就是假归属（门禁看到的是「rev1 通过」，实际测的是 rev1+改动）。
// 因此证据必须记录 dirty 标记，门禁对 dirty 证据判死；要用正式证据就把
// 改动提交成干净工作区再跑（或把证据目录放在 gitignore 的 artifacts/ 下）。
func SourceIdentity() (string, bool) {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "", false
	}
	rev := strings.TrimSpace(string(out))
	// --untracked-files=normal：新增源码文件也算脏，但不把 .gitignore 里的
	// 证据目录（artifacts/、docs/evidence/runs/）算成脏。
	st, stErr := exec.Command("git", "status", "--porcelain").Output()
	dirty := stErr == nil && strings.TrimSpace(string(st)) != ""
	return rev, dirty
}

// ComputeVersionsLockHash 返回 versions.lock 文件的 sha256（带前缀）。
func ComputeVersionsLockHash(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// GateBaseline 是门禁判定所需的当前基线（源 revision 与锁文件哈希）。
type GateBaseline struct {
	SourceRevision   string
	VersionsLockHash string
	VersionsLockPath string
	// WorktreeDirty 是「下结论时」的工作区状态。它不影响已绑定 revision
	// 的证据能否通过（证据自带 WorktreeDirty 且必须为 false），只在报告里
	// 如实标注，避免把脏工作区的分析说成干净基线。
	WorktreeDirty bool
}

// BuildBaseline 依据当前仓库构建门禁基线。
func BuildBaseline() (GateBaseline, error) {
	rev, dirty := SourceIdentity()
	const lockPath = "build/versions.lock.json"
	h, err := ComputeVersionsLockHash(lockPath)
	if err != nil {
		return GateBaseline{}, err
	}
	return GateBaseline{
		SourceRevision:   rev,
		VersionsLockHash: h,
		VersionsLockPath: lockPath,
		WorktreeDirty:    dirty,
	}, nil
}
