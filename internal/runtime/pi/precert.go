package pi

// 本文件实现 F08-C Part 2：**签名预认证清单**——VersionPolicy.PreCertifiedHashes
// 的载体（规格 §8.1 / 计划 F08 Step 5 的 ready_verified 规则）。
//
// 为什么要有它：ready_verified 要求"版本已认证 + 可执行体 SHA-256 已登记 + 契约探测
// 通过"。前两条不能靠运行期观察得到——把本机跑通过的哈希写进白名单就是自证。所以
// 白名单必须**随发行包下发、由发行密钥签名**：清单是 Ed25519 分离签名，签名覆盖
// manifest.json 的**原始字节**；任何一字节改动都会让验签失败。
//
// 纪律：
//   - fail closed：没有清单 / 清单不合法 / 目标不匹配 → 空哈希表，绝不"猜一个"。
//   - 严格解析：未知 JSON 字段、非法 schema、2.x 版本、非法目标、非法哈希一律拒绝，
//     不给"夹带额外信任数据"留缝。
//   - 本仓库当前**不**携带清单（precert/ 里只有 README.md）：真清单由发行流水线用
//     发行私钥生成。测试只用临时密钥对，私钥不入库、不落盘。
//
// 生成方式（发行流水线，不在本仓库执行）：
//
//	go run ./internal/runtime/pi/precertgen -entries entries.json -key release-ed25519.pem -out internal/runtime/pi/precert

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// 清单资产名（相对清单目录）。三者缺一不可。
const (
	PreCertifiedManifestFile  = "manifest.json"
	PreCertifiedSignatureFile = "manifest.json.sig"
	PreCertifiedTrustRootFile = "trust-root.pub"
)

// PreCertifiedManifestSchema 是当前唯一的清单格式版本；其它值一律拒绝。
const PreCertifiedManifestSchema = 1

// 生成器接受的私钥格式：PKCS#8 PEM（默认）或 base64 的 32 字节 seed。
const (
	PreCertifiedKeyFormatPEM  = "pem"
	PreCertifiedKeyFormatSeed = "seed"
)

// ErrNoPreCertifiedManifest 表示发行包里没有随包下发的签名清单。
//
// 这是**正常状态**而不是配置错误：此时未登记的可执行体最高只能 ready_compatible。
// 它与"清单存在但验签失败"必须区分开——后者是错误，绝不能被降级成空表放行。
var ErrNoPreCertifiedManifest = errors.New(
	"pi: 发行包未附带预认证清单（fail closed：未登记的可执行体最高只能 ready_compatible）")

// PreCertifiedManifest 是随发行包下发的预认证清单（签名覆盖其原始字节）。
type PreCertifiedManifest struct {
	Schema      int                 `json:"schema"`      // 必须为 1
	GeneratedAt string              `json:"generatedAt"` // RFC3339，仅供审计，不参与信任判断
	Entries     []PreCertifiedEntry `json:"entries"`
}

// PreCertifiedEntry 是一条"某版本某目标允许的可执行体"登记。
type PreCertifiedEntry struct {
	Version          string `json:"version"`                  // 精确 "1.5.0"（V1 只认证 1.x）
	Target           string `json:"target,omitempty"`         // "win-x64"…"linux-arm64"；"" = 所有目标
	ExecutableSHA256 string `json:"executableSha256"`         // 64 位小写十六进制：Pi 可执行体
	LauncherSHA256   string `json:"launcherSha256,omitempty"` // npm shim 启动器哈希（适用时）
	Notes            string `json:"notes,omitempty"`
}

// precertTargets 是六个发行目标（AGENTS.md §3）。清单里的 target 只能取其中之一或空。
var precertTargets = []string{
	"win-x64", "win-arm64",
	"mac-x64", "mac-arm64",
	"linux-x64", "linux-arm64",
}

// sha256Hex 是 64 位**小写**十六进制的严格判据（大写、短、长都拒绝）。
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

//go:generate go run ./precertgen -entries entries.json -key release-ed25519.pem -out precert

// precertAssets 锚定随二进制嵌入的清单目录。今天里面只有 README.md：
// 真清单由发行流水线生成并签名，本地跑通的哈希绝不手工塞进来。
//
//go:embed precert
var precertAssets embed.FS

// HostTarget 把宿主 GOOS/GOARCH 映射成六个目标之一；未知组合返回 ""。
//
// 返回 "" 时只有 Target=="" 的条目能匹配——这是 fail closed，不是"全都放行"。
func HostTarget() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "win-x64"
	case "windows/arm64":
		return "win-arm64"
	case "darwin/amd64":
		return "mac-x64"
	case "darwin/arm64":
		return "mac-arm64"
	case "linux/amd64":
		return "linux-x64"
	case "linux/arm64":
		return "linux-arm64"
	}
	return ""
}

// VerifyPreCertifiedManifest 验签 + 严格解析清单。
//
// signature 是 manifest.json.sig 的内容（base64 的分离签名；也接受裸 64 字节）。
// 校验顺序：先验签（覆盖 manifest 原始字节），再严格解析内容——任何一项不合格都
// 返回错误，并且**不返回任何条目**（调用方只能得到空哈希表）。
func VerifyPreCertifiedManifest(manifest, signature []byte, pub ed25519.PublicKey) (PreCertifiedManifest, error) {
	var zero PreCertifiedManifest
	if len(pub) != ed25519.PublicKeySize {
		return zero, fmt.Errorf("pi: 预认证清单公钥长度非法: %d（期望 %d）", len(pub), ed25519.PublicKeySize)
	}
	sig := decodeDetachedSignature(signature)
	if len(sig) != ed25519.SignatureSize {
		return zero, fmt.Errorf("pi: 预认证清单签名长度非法: %d（期望 %d）", len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(pub, manifest, sig) {
		return zero, errors.New(
			"pi: 预认证清单签名校验失败（清单字节被改动、签名不匹配，或不是发行密钥签的）；拒绝使用")
	}
	m, err := parsePreCertifiedManifest(manifest)
	if err != nil {
		return zero, err
	}
	return m, nil
}

// HashesFor 把条目转成 VersionPolicy.PreCertifiedHashes 的形态：
// 版本 → 允许的可执行体 sha256 集合，只保留 Target=="" 或 Target==target 的条目。
//
// 条目里的 launcherSha256 也纳入集合：npm 安装的候选，其 ExecutableHash 就是启动器
// 哈希，若不登记就永远无法 ready_verified。两者都是签名覆盖的内容，不存在放宽。
func (m PreCertifiedManifest) HashesFor(target string) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		if e.Target != "" && e.Target != target {
			continue
		}
		if out[e.Version] == nil {
			out[e.Version] = map[string]bool{}
		}
		out[e.Version][e.ExecutableSHA256] = true
		if e.LauncherSHA256 != "" {
			out[e.Version][e.LauncherSHA256] = true
		}
	}
	return out
}

// LoadPreCertifiedManifest 从目录读取 manifest.json / manifest.json.sig / trust-root.pub
// 并验签，返回该目标的哈希表。缺任一文件 → ErrNoPreCertifiedManifest。
func LoadPreCertifiedManifest(dir string, target string) (map[string]map[string]bool, error) {
	return loadPreCertified(os.DirFS(dir), ".", target)
}

// LoadEmbeddedPreCertified 读取随二进制嵌入的清单（precert/），目标取 HostTarget()。
// 缺任一文件 → ErrNoPreCertifiedManifest（今天的默认状态）。
func LoadEmbeddedPreCertified() (map[string]map[string]bool, error) {
	return loadPreCertified(precertAssets, "precert", HostTarget())
}

// ── 生成器（发行流水线用，测试用临时密钥调用同一套函数） ────────────────────

// ParsePreCertifiedEntries 解析生成器输入的 entries JSON（{"entries":[{...}]}）。
// 与清单解析一样拒绝未知字段：输入格式被夹带内容时直接失败。
func ParsePreCertifiedEntries(raw []byte) ([]PreCertifiedEntry, error) {
	var doc struct {
		Entries []PreCertifiedEntry `json:"entries"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("pi: entries 文件不是合法 JSON 或含未知字段: %w", err)
	}
	if err := ensureJSONEnd(dec); err != nil {
		return nil, err
	}
	return doc.Entries, nil
}

// BuildPreCertifiedManifest 校验条目、去重、按确定顺序排序，产出**逐字节稳定**的
// manifest 字节（同样的输入永远得到同样的文件，便于发行日志核对 hash）。
func BuildPreCertifiedManifest(entries []PreCertifiedEntry, generatedAt time.Time) ([]byte, PreCertifiedManifest, error) {
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	m, err := normalizePreCertifiedManifest(PreCertifiedManifest{
		Schema:      PreCertifiedManifestSchema,
		GeneratedAt: generatedAt.UTC().Format(time.RFC3339),
		Entries:     entries,
	})
	if err != nil {
		return nil, PreCertifiedManifest{}, err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, PreCertifiedManifest{}, fmt.Errorf("pi: 序列化预认证清单失败: %w", err)
	}
	raw = append(raw, '\n')
	return raw, m, nil
}

// SignPreCertifiedManifest 对清单**原始字节**做 Ed25519 分离签名。
// 返回 base64 的签名（manifest.json.sig 内容）与 base64 的公钥（trust-root.pub 内容）。
func SignPreCertifiedManifest(manifest []byte, priv ed25519.PrivateKey) (sigB64, pubB64 []byte, err error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, nil, fmt.Errorf("pi: 预认证清单签名私钥长度非法: %d", len(priv))
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, nil, errors.New("pi: 私钥对应的公钥不是 Ed25519 公钥")
	}
	sig := ed25519.Sign(priv, manifest)
	return []byte(base64.StdEncoding.EncodeToString(sig)),
		[]byte(base64.StdEncoding.EncodeToString(pub)), nil
}

// WritePreCertifiedManifest 把三件套写进 dir。
//
// 写出前先自验：拒绝落盘无法验签的清单（防止手滑发出坏清单）。
// 已存在且 force=false 时拒绝覆盖，避免悄悄替换发行清单。
func WritePreCertifiedManifest(dir string, manifest, sigB64, pubB64 []byte, force bool) error {
	pub, err := decodeTrustRoot(pubB64)
	if err != nil {
		return err
	}
	if _, err := VerifyPreCertifiedManifest(manifest, sigB64, pub); err != nil {
		return fmt.Errorf("pi: 拒绝写出无法验签的预认证清单: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("pi: 创建清单目录 %s 失败: %w", dir, err)
	}
	files := []struct {
		name string
		data []byte
	}{
		{PreCertifiedManifestFile, manifest},
		{PreCertifiedSignatureFile, sigB64},
		{PreCertifiedTrustRootFile, pubB64},
	}
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		if !force {
			if _, statErr := os.Stat(p); statErr == nil {
				return fmt.Errorf("pi: %s 已存在，拒绝覆盖（需要 -force）", p)
			} else if !errors.Is(statErr, fs.ErrNotExist) {
				return fmt.Errorf("pi: 检查 %s 失败: %w", p, statErr)
			}
		}
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, 0o644); err != nil {
			return fmt.Errorf("pi: 写 %s 失败: %w", f.name, err)
		}
	}
	return nil
}

// ParsePreCertifiedPrivateKey 解析发行私钥：PKCS#8 PEM，或 base64 的 32 字节 seed。
func ParsePreCertifiedPrivateKey(raw []byte, format string) (ed25519.PrivateKey, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", PreCertifiedKeyFormatPEM:
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, errors.New("pi: 私钥不是 PEM 编码")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("pi: 解析 PKCS#8 私钥失败: %w", err)
		}
		priv, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("pi: 私钥不是 Ed25519 私钥")
		}
		return priv, nil
	case PreCertifiedKeyFormatSeed:
		seed, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(raw)), ""))
		if err != nil {
			return nil, fmt.Errorf("pi: seed 不是合法 base64: %w", err)
		}
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("pi: seed 必须是 %d 字节，实际 %d", ed25519.SeedSize, len(seed))
		}
		return ed25519.NewKeyFromSeed(seed), nil
	default:
		return nil, fmt.Errorf("pi: 未知私钥格式 %q（支持 %s/%s）", format, PreCertifiedKeyFormatPEM, PreCertifiedKeyFormatSeed)
	}
}

// ── 内部实现 ────────────────────────────────────────────────────────────────

// loadPreCertified 是 LoadPreCertifiedManifest / LoadEmbeddedPreCertified 的共用实现。
func loadPreCertified(fsys fs.FS, dir, target string) (map[string]map[string]bool, error) {
	manifest, err := readPreCertifiedAsset(fsys, dir, PreCertifiedManifestFile)
	if err != nil {
		return nil, err
	}
	sig, err := readPreCertifiedAsset(fsys, dir, PreCertifiedSignatureFile)
	if err != nil {
		return nil, err
	}
	pubRaw, err := readPreCertifiedAsset(fsys, dir, PreCertifiedTrustRootFile)
	if err != nil {
		return nil, err
	}
	pub, err := decodeTrustRoot(pubRaw)
	if err != nil {
		return nil, err
	}
	m, err := VerifyPreCertifiedManifest(manifest, sig, pub)
	if err != nil {
		return nil, err
	}
	return m.HashesFor(target), nil
}

// readPreCertifiedAsset 读一个清单资产。缺失或为空都按"没有清单"处理（fail closed）；
// 其它 IO 错误如实报错。
func readPreCertifiedAsset(fsys fs.FS, dir, name string) ([]byte, error) {
	raw, err := fs.ReadFile(fsys, path.Join(dir, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: 缺少 %s", ErrNoPreCertifiedManifest, name)
		}
		return nil, fmt.Errorf("pi: 读取预认证清单资产 %s 失败: %w", name, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%w: %s 为空", ErrNoPreCertifiedManifest, name)
	}
	return raw, nil
}

// parsePreCertifiedManifest 严格解析并规范化清单字节。
func parsePreCertifiedManifest(raw []byte) (PreCertifiedManifest, error) {
	var m PreCertifiedManifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return PreCertifiedManifest{}, fmt.Errorf("pi: 预认证清单不是合法 JSON 或含未知字段: %w", err)
	}
	if err := ensureJSONEnd(dec); err != nil {
		return PreCertifiedManifest{}, err
	}
	return normalizePreCertifiedManifest(m)
}

// ensureJSONEnd 要求 JSON 文档之后没有多余内容（不许"合法对象 + 夹带"）。
func ensureJSONEnd(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("pi: JSON 文档在合法对象之后还有多余内容")
	}
	return nil
}

// normalizePreCertifiedManifest 校验每一条、去重、排序。返回的清单可安全落盘。
func normalizePreCertifiedManifest(m PreCertifiedManifest) (PreCertifiedManifest, error) {
	if m.Schema != PreCertifiedManifestSchema {
		return PreCertifiedManifest{}, fmt.Errorf(
			"pi: 预认证清单 schema 必须是 %d，实际 %d", PreCertifiedManifestSchema, m.Schema)
	}
	if g := strings.TrimSpace(m.GeneratedAt); g != "" {
		if _, err := time.Parse(time.RFC3339, g); err != nil {
			return PreCertifiedManifest{}, fmt.Errorf(
				"pi: 预认证清单 generatedAt %q 不是 RFC3339: %w", m.GeneratedAt, err)
		}
	}

	type entryKey struct{ version, target, executable, launcher string }
	seen := make(map[entryKey]string, len(m.Entries)) // → notes
	hashVersion := make(map[string]string, len(m.Entries))
	out := make([]PreCertifiedEntry, 0, len(m.Entries))
	for i, e := range m.Entries {
		ver, ok := parseSemver(e.Version)
		if !ok {
			return PreCertifiedManifest{}, fmt.Errorf(
				"pi: 预认证清单第 %d 条版本 %q 无法解析（基线只认证精确版本）", i+1, e.Version)
		}
		if ver[0] != 1 {
			return PreCertifiedManifest{}, fmt.Errorf(
				"pi: 预认证清单第 %d 条版本 %q 属 %d.x，V1 只认证 1.x（2.x 需要后续签名清单与契约套件）",
				i+1, e.Version, ver[0])
		}
		if !validPreCertifiedTarget(e.Target) {
			return PreCertifiedManifest{}, fmt.Errorf(
				"pi: 预认证清单第 %d 条 target %q 不是六目标之一（%s），也不为空",
				i+1, e.Target, strings.Join(precertTargets, "/"))
		}
		if err := checkSHA256Field("executableSha256", e.ExecutableSHA256); err != nil {
			return PreCertifiedManifest{}, fmt.Errorf("pi: 预认证清单第 %d 条: %w", i+1, err)
		}
		if e.LauncherSHA256 != "" {
			if err := checkSHA256Field("launcherSha256", e.LauncherSHA256); err != nil {
				return PreCertifiedManifest{}, fmt.Errorf("pi: 预认证清单第 %d 条: %w", i+1, err)
			}
		}

		k := entryKey{e.Version, e.Target, e.ExecutableSHA256, e.LauncherSHA256}
		if prevNotes, dup := seen[k]; dup {
			if prevNotes != e.Notes {
				return PreCertifiedManifest{}, fmt.Errorf(
					"pi: 重复条目（version=%s target=%q executable=%s）的 notes 不一致，清单含义不唯一",
					e.Version, e.Target, e.ExecutableSHA256)
			}
			continue // 完全重复 → 去重
		}
		seen[k] = e.Notes

		// 同一个可执行体哈希不能挂在两个版本下：那等于把"1.5.0 的认证"借给别的版本。
		if prevVersion, conflict := hashVersion[e.ExecutableSHA256]; conflict && prevVersion != e.Version {
			return PreCertifiedManifest{}, fmt.Errorf(
				"pi: 可执行体哈希 %s 同时登记在版本 %s 与 %s 下", e.ExecutableSHA256, prevVersion, e.Version)
		}
		hashVersion[e.ExecutableSHA256] = e.Version
		out = append(out, e)
	}

	sort.SliceStable(out, func(i, j int) bool { return preCertifiedEntryLess(out[i], out[j]) })
	m.Entries = out
	return m, nil
}

// preCertifiedEntryLess 给出确定顺序：版本 → 目标 → 可执行体哈希 → 启动器哈希 → notes。
func preCertifiedEntryLess(a, b PreCertifiedEntry) bool {
	av, aok := parseSemver(a.Version)
	bv, bok := parseSemver(b.Version)
	if aok && bok {
		for i := 0; i < 3; i++ {
			if av[i] != bv[i] {
				return av[i] < bv[i]
			}
		}
	} else if a.Version != b.Version {
		return a.Version < b.Version
	}
	if a.Target != b.Target {
		return a.Target < b.Target
	}
	if a.ExecutableSHA256 != b.ExecutableSHA256 {
		return a.ExecutableSHA256 < b.ExecutableSHA256
	}
	if a.LauncherSHA256 != b.LauncherSHA256 {
		return a.LauncherSHA256 < b.LauncherSHA256
	}
	return a.Notes < b.Notes
}

func validPreCertifiedTarget(target string) bool {
	if target == "" {
		return true
	}
	for _, t := range precertTargets {
		if t == target {
			return true
		}
	}
	return false
}

func checkSHA256Field(name, value string) error {
	if sha256Hex.MatchString(value) {
		return nil
	}
	return fmt.Errorf("%s 必须是 64 位小写十六进制字符串，实际 %q", name, value)
}

// decodeDetachedSignature 解出分离签名：优先按 base64（文件内容形态），
// 否则当作裸 64 字节签名。两种编码都只是同一签名的不同外壳，验签判据不变。
func decodeDetachedSignature(raw []byte) []byte {
	compact := strings.Join(strings.Fields(string(raw)), "")
	if decoded, err := base64.StdEncoding.DecodeString(compact); err == nil &&
		len(decoded) == ed25519.SignatureSize {
		return decoded
	}
	return raw
}

// decodeTrustRoot 解出信任根公钥：base64（允许换行）的 32 字节 Ed25519 公钥。
func decodeTrustRoot(raw []byte) (ed25519.PublicKey, error) {
	compact := strings.Join(strings.Fields(string(raw)), "")
	decoded, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		return nil, fmt.Errorf("pi: 信任根不是合法 base64: %w", err)
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("pi: 信任根必须是 %d 字节 Ed25519 公钥，实际 %d",
			ed25519.PublicKeySize, len(decoded))
	}
	return ed25519.PublicKey(decoded), nil
}
