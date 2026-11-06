package piinstall

// 本文件实现 F06 的**受信清单**（规格 §8.2 / 计划 F06 Step 3）。
//
// 信任根：清单用 go:embed 编进二进制，运行期不再从网络取任何哈希（不拉 SHA256SUMS）。
// 清单本身由签名的 Anvil 安装包（X05）保护；上游 Pi v1.5.0 没有独立签名，所以
// signature 一律为空，只有在非空时才校验（见 download.go 的 fail-closed 规则）。
//
// 严格解析（照抄 precert.go 的纪律）：
//   - 未知字段一律拒绝（夹带额外信任数据没有缝）；
//   - schema 必须等于 1；
//   - targetId 只能是六个目标之一；
//   - sha256 必须是 64 位小写十六进制；
//   - url 必须 https、主机在白名单里、不带凭证、不是 IP 字面量；
//   - assetName 必须与 URL 的最后一段一致（否则"清单说 A、实际下 B"）；
//   - 同一个 (version,target) 不能出现两条。
//
// sizeBytes 的纪律（PI_INS01 / 报告口径）：
//   - 解析时拒绝负值（明显是错的）；
//   - **取条目时拒绝 sizeBytes <= 0**：离线测不出精确大小的目标（本清单里的五个）
//     在发行流程补齐之前显式不可用，绝不编造一个"看起来合理"的数字。
//     拒绝时返回 UNSUPPORTED，并在错误里点名 sizeBytes。

import (
	"bytes"
	"crypto/ed25519"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

// ManifestSchema 是当前唯一的清单格式版本。
const ManifestSchema = 1

// TargetIDs 是六个发行目标（AGENTS.md §3），顺序即展示顺序。
var TargetIDs = []string{"win-x64", "win-arm64", "mac-x64", "mac-arm64", "linux-x64", "linux-arm64"}

// DefaultAllowedHosts 是生产环境的下载主机白名单。
//
// 只认这三个名字：GitHub 发布页会 302 到 objects.githubusercontent.com /
// release-assets.githubusercontent.com。IP 字面量不在白名单里，因此默认被拒绝。
var DefaultAllowedHosts = []string{
	"github.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
}

var (
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

// ManifestEntry 是一个"某版本某目标的官方安装包"。
type ManifestEntry struct {
	Version string `json:"version"`
	// TargetID 是六目标之一。
	TargetID string `json:"targetId"`
	// AssetName 是发布资产名，必须与 URL 最后一段一致。
	AssetName string `json:"assetName"`
	// URL 必须是官方 HTTPS 地址。
	URL string `json:"url"`
	// SizeBytes 是精确字节数；<=0 表示"未测量"，取条目时会被拒绝。
	SizeBytes int64 `json:"sizeBytes"`
	// SHA256 是包的 64 位小写十六进制哈希。
	SHA256 string `json:"sha256"`
	// Signature 是上游提供的分离签名（base64 Ed25519）；上游没有就是空串。
	Signature string `json:"signature,omitempty"`
}

// Manifest 是随二进制下发的受信清单。
type Manifest struct {
	Schema      int             `json:"schema"`
	GeneratedAt string          `json:"generatedAt"`
	Source      string          `json:"source"`
	Notes       string          `json:"notes,omitempty"`
	Entries     []ManifestEntry `json:"entries"`
}

//go:embed manifest.json
var embeddedManifest []byte

// LoadEmbeddedManifest 解析随二进制嵌入的清单。
func LoadEmbeddedManifest() (Manifest, error) {
	return ParseManifest(embeddedManifest)
}

// ParseManifest 严格解析并规范化清单字节。
func ParseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("piinstall: 清单不是合法 JSON 或含未知字段: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("piinstall: 清单在合法对象之后还有多余内容")
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Validate 校验并规范化清单（去重排序，保证输出稳定）。
func (m *Manifest) Validate() error {
	if m.Schema != ManifestSchema {
		return fmt.Errorf("piinstall: 清单 schema 必须是 %d，实际 %d", ManifestSchema, m.Schema)
	}
	if g := strings.TrimSpace(m.GeneratedAt); g != "" {
		if _, err := time.Parse(time.RFC3339, g); err != nil {
			return fmt.Errorf("piinstall: 清单 generatedAt %q 不是 RFC3339: %w", m.GeneratedAt, err)
		}
	}
	if err := ValidateURL(m.Source, DefaultAllowedHosts); err != nil {
		return fmt.Errorf("piinstall: 清单 source 不合法: %w", err)
	}
	seen := map[string]bool{}
	for i := range m.Entries {
		if err := m.Entries[i].Validate(); err != nil {
			return fmt.Errorf("piinstall: 清单第 %d 条: %w", i+1, err)
		}
		// 随二进制下发的清单只允许指向官方主机：这是"信任根不指向任意地址"的静态保证。
		if err := ValidateURL(m.Entries[i].URL, DefaultAllowedHosts); err != nil {
			return fmt.Errorf("piinstall: 清单第 %d 条 url 不在官方主机白名单里: %w", i+1, err)
		}
		key := m.Entries[i].Version + "/" + m.Entries[i].TargetID
		if seen[key] {
			return fmt.Errorf("piinstall: 清单里 %s 出现了两次（同一目标两份哈希含义不唯一）", key)
		}
		seen[key] = true
	}
	sort.SliceStable(m.Entries, func(i, j int) bool {
		if m.Entries[i].Version != m.Entries[j].Version {
			return m.Entries[i].Version < m.Entries[j].Version
		}
		return targetRank(m.Entries[i].TargetID) < targetRank(m.Entries[j].TargetID)
	})
	return nil
}

// Validate 校验单条清单条目（不含 sizeBytes<=0 的可用性判断，那是 Entry 的事）。
func (e ManifestEntry) Validate() error {
	if !versionPattern.MatchString(e.Version) {
		return fmt.Errorf("version %q 不是三段式版本号（只认证精确版本）", e.Version)
	}
	if !validTarget(e.TargetID) {
		return fmt.Errorf("targetId %q 不是六目标之一（%s）", e.TargetID, strings.Join(TargetIDs, "/"))
	}
	if e.AssetName == "" || strings.ContainsAny(e.AssetName, `/\`) || e.AssetName == "." || e.AssetName == ".." {
		return fmt.Errorf("assetName %q 必须是不含路径分隔符的资产名", e.AssetName)
	}
	// 这里只校验 URL 形态（https/无凭证/有主机）。主机白名单是**下载时**的门
	// （Fetch 用注入的 allowlist），清单整体校验（Manifest.Validate）才要求官方主机。
	if err := validateURLShape(e.URL); err != nil {
		return fmt.Errorf("url 不合法: %w", err)
	}
	u, err := url.Parse(e.URL)
	if err != nil {
		return fmt.Errorf("url 无法解析: %w", err)
	}
	if path.Base(u.Path) != e.AssetName {
		return fmt.Errorf("assetName %q 与 url 最后一段 %q 不一致", e.AssetName, path.Base(u.Path))
	}
	if !sha256Pattern.MatchString(e.SHA256) {
		return fmt.Errorf("sha256 必须是 64 位小写十六进制，实际 %q", e.SHA256)
	}
	if e.SizeBytes < 0 {
		return fmt.Errorf("sizeBytes 不能为负（实际 %d）", e.SizeBytes)
	}
	if e.Signature != "" {
		compact := strings.Join(strings.Fields(e.Signature), "")
		raw, err := base64.StdEncoding.DecodeString(compact)
		if err != nil {
			return fmt.Errorf("signature 不是合法 base64: %w", err)
		}
		if len(raw) != ed25519.SignatureSize {
			return fmt.Errorf("signature 必须是 %d 字节 Ed25519 签名，实际 %d", ed25519.SignatureSize, len(raw))
		}
	}
	return nil
}

// Entry 取出某版本某目标的条目。
//
// 这里就是"loader 拒绝 sizeBytes<=0"的落点：清单可以列出目标（URL/hash 是官方事实），
// 但没测出精确大小时不允许安装——否则下载器就没有可核对的上界。
func (m Manifest) Entry(version, targetID string) (ManifestEntry, error) {
	version = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(version), "v"))
	targetID = strings.TrimSpace(targetID)
	if !validTarget(targetID) {
		return ManifestEntry{}, contracts.NewAppError(contracts.CodeUnknownTarget,
			fmt.Sprintf("未知目标 %q（只支持 %s）", targetID, strings.Join(TargetIDs, "/")), false)
	}
	for _, e := range m.Entries {
		if e.Version != version || e.TargetID != targetID {
			continue
		}
		if e.SizeBytes <= 0 {
			return ManifestEntry{}, contracts.NewAppError(contracts.CodeUnsupported,
				fmt.Sprintf("清单里 %s/%s 的 sizeBytes=%d：发行流程还没有填入精确大小，该目标在补齐前显式不可安装（不编造大小）",
					version, targetID, e.SizeBytes), false)
		}
		return e, nil
	}
	return ManifestEntry{}, contracts.NewAppError(contracts.CodeUnknownTarget,
		fmt.Sprintf("清单里没有 %s/%s（V1 只信任随二进制下发的清单）", version, targetID), false)
}

// Targets 返回清单里出现过的目标（按展示顺序）。
func (m Manifest) Targets(version string) []string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	have := map[string]bool{}
	for _, e := range m.Entries {
		if e.Version == version {
			have[e.TargetID] = true
		}
	}
	out := make([]string, 0, len(have))
	for _, t := range TargetIDs {
		if have[t] {
			out = append(out, t)
		}
	}
	return out
}

// UnavailableTargets 返回清单里有条目但 sizeBytes<=0 的目标（即 BLOCKED 目标）。
func (m Manifest) UnavailableTargets(version string) []string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	out := []string{}
	for _, t := range TargetIDs {
		for _, e := range m.Entries {
			if e.Version == version && e.TargetID == t && e.SizeBytes <= 0 {
				out = append(out, t)
			}
		}
	}
	return out
}

// Versions 返回清单里出现过的版本（升序）。
func (m Manifest) Versions() []string {
	have := map[string]bool{}
	for _, e := range m.Entries {
		have[e.Version] = true
	}
	out := make([]string, 0, len(have))
	for v := range have {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// ValidateURL 校验下载地址：https + 主机白名单 + 无凭证 + 非 IP 字面量。
//
// allowedHosts 是唯一的主机信任源。生产用 DefaultAllowedHosts（不含任何 IP），
// 测试注入本地 TLS 服务器的主机名/端口——注入本身不会放宽生产默认。
func ValidateURL(raw string, allowedHosts []string) error {
	if err := validateURLShape(raw); err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("URL 无法解析: %w", err)
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil {
		// IP 字面量默认拒绝；只有白名单显式列了它（测试注入）才放行。
		if !hostAllowed(u.Host, host, allowedHosts) {
			return fmt.Errorf("不接受 IP 字面量主机 %q（白名单里没有它）", host)
		}
		return nil
	}
	if !hostAllowed(u.Host, host, allowedHosts) {
		return fmt.Errorf("主机 %q 不在下载白名单里（%s）", host, strings.Join(allowedHosts, "/"))
	}
	return nil
}

// validateURLShape 只校验地址形态：https、无凭证、有主机名。主机信任交给白名单。
func validateURLShape(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("URL 为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("URL 无法解析: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("只接受 HTTPS，实际 scheme=%q", u.Scheme)
	}
	if u.User != nil {
		return errors.New("URL 不允许携带凭证")
	}
	if u.Hostname() == "" {
		return errors.New("URL 缺少主机名")
	}
	return nil
}

// hostAllowed 判断 host:port / host 是否命中白名单条目。
// 白名单条目可以带端口（测试注入），也可以只是主机名（生产）。
func hostAllowed(hostPort, host string, allowedHosts []string) bool {
	if len(allowedHosts) == 0 {
		allowedHosts = DefaultAllowedHosts
	}
	for _, a := range allowedHosts {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.Contains(a, ":") {
			if strings.EqualFold(a, hostPort) {
				return true
			}
			continue
		}
		if strings.EqualFold(a, host) {
			return true
		}
	}
	return false
}

func validTarget(target string) bool {
	for _, t := range TargetIDs {
		if t == target {
			return true
		}
	}
	return false
}

func targetRank(target string) int {
	for i, t := range TargetIDs {
		if t == target {
			return i
		}
	}
	return len(TargetIDs)
}

// hostTargetOrDefault 返回请求里的目标，空则取宿主目标。
func hostTargetOrDefault(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target != "" {
		return target, nil
	}
	if h := pi.HostTarget(); h != "" {
		return h, nil
	}
	return "", contracts.NewAppError(contracts.CodeUnknownTarget,
		"请求未指定 targetId，且宿主不在六个发行目标内", false)
}
