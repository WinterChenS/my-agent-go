package devtools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"my-agent-go/internal/contracts"
)

type processOracle struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Command       string `json:"command"`
	ExitCode      int    `json:"exitCode"`
	StdoutSHA256  string `json:"stdoutSha256"`
	StderrSHA256  string `json:"stderrSha256"`
}

func bytesSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writeProcessOracle(dir, command string, exitCode int, stdout, stderr []byte) error {
	oracle := processOracle{
		SchemaVersion: 1,
		Kind:          "test-process",
		Command:       command,
		ExitCode:      exitCode,
		StdoutSHA256:  bytesSHA256(stdout),
		StderrSHA256:  bytesSHA256(stderr),
	}
	data, err := json.MarshalIndent(oracle, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "oracle.json"), data, 0o644)
}

func validateProcessOracle(dir, oraclePath, command string, exitCode int) error {
	raw, err := os.ReadFile(filepath.Join(dir, oraclePath))
	if err != nil {
		return err
	}
	var oracle processOracle
	if err := json.Unmarshal(raw, &oracle); err != nil {
		return fmt.Errorf("oracle 不是合法 JSON: %w", err)
	}
	if oracle.SchemaVersion != 1 || oracle.Kind != "test-process" {
		return fmt.Errorf("oracle schema/kind 不受支持: %d/%q", oracle.SchemaVersion, oracle.Kind)
	}
	if oracle.Command != command || oracle.ExitCode != exitCode {
		return fmt.Errorf("oracle 命令或退出码与 result 不一致")
	}
	stdout, err := os.ReadFile(filepath.Join(dir, "stdout.log"))
	if err != nil {
		return fmt.Errorf("读取 stdout: %w", err)
	}
	stderr, err := os.ReadFile(filepath.Join(dir, "stderr.log"))
	if err != nil {
		return fmt.Errorf("读取 stderr: %w", err)
	}
	if oracle.StdoutSHA256 != bytesSHA256(stdout) || oracle.StderrSHA256 != bytesSHA256(stderr) {
		return fmt.Errorf("oracle 的子进程输出哈希不匹配")
	}
	return nil
}

// ComputeEvidenceHash 计算证据目录的规范化哈希，覆盖 result.json 与目录中全部
// 其它常规文件（命令、输出、环境、oracle、截图、录屏和附件）。门禁据此重新计算
// 并比对 result.json.reportHash，用于发现报告或外部判据被篡改（GOV02）。
//
// 关键点：result.json 自身包含 reportHash 字段，直接哈希原始字节会形成自引用。
// 因此 result.json 清空 reportHash 后按规范 JSON 参与；其它文件按相对路径排序，
// 同时哈希路径和原始字节，避免文件重命名或拼接碰撞逃过检查。
func ComputeEvidenceHash(dir string) string {
	h := sha256.New()
	resultPath := filepath.Join(dir, "result.json")
	raw, err := os.ReadFile(resultPath)
	h.Write([]byte("result.json\x00"))
	if err != nil {
		h.Write([]byte("MISSING\x00"))
	} else {
		var ev contracts.GateEvidence
		if err := json.Unmarshal(raw, &ev); err != nil {
			h.Write(raw)
		} else {
			ev.ReportHash = ""
			canon, _ := json.Marshal(ev)
			h.Write(canon)
		}
	}

	var files []string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || path == resultPath {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil && info.Mode().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool {
		ri, _ := filepath.Rel(dir, files[i])
		rj, _ := filepath.Rel(dir, files[j])
		return filepath.ToSlash(ri) < filepath.ToSlash(rj)
	})
	for _, path := range files {
		rel, _ := filepath.Rel(dir, path)
		h.Write([]byte(filepath.ToSlash(rel) + "\x00"))
		if b, readErr := os.ReadFile(path); readErr == nil {
			h.Write(b)
		} else {
			h.Write([]byte("UNREADABLE\x00"))
		}
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
