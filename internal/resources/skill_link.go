// 技能链接导入的目标地址判定与清单解析（RES05）。
//
// 这一步只做"能不能从链接取到一个可解析的清单"，不执行任何东西：
//   - 只接受 HTTPS；不接受 userinfo、非标准端口之外的奇怪写法；
//   - 只接受单个 SKILL.md 或 zip / tar.gz 归档；
//   - 总下载量、解压量、文件数、重定向次数都有上限；
//   - 目标地址必须是"公开目的地"：对**每一个 DNS answer、每一次重定向、
//     连接后的实际 peer** 都做 IANA special-purpose 段判定（防 DNS rebinding）。
//
// 为什么 peer 也要判：只在解析阶段判 host 的话，攻击者可以让第一次解析返回
// 公网地址（通过校验）、第二次解析返回 127.0.0.1（真正连接）。所以解析结果
// 全部校验后再按 IP 直连（不给第二次解析机会），连上后再核对一次对端地址。
//
// 归档内容只写到暂存目录（0600/0700，不带可执行位），不执行脚本、不调用 git。
// 用户确认后才由 ImportSkill 复制进托管目录；暂存目录按时间清扫。
package resources

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- 上限

// LinkLimits 是链接导入的硬上限。
//
// 数字与本地导入保持同一量级（技能是给模型读的说明文本，不是数据集），
// 但"下载"与"解压"分开计数：一个 8 MiB 的压缩包可以解出几个 GiB，
// 用一个数字同时表达这两件事会让其中一头失控。
type LinkLimits struct {
	// MaxDownloadBytes 是响应体的原始字节上限。
	MaxDownloadBytes int64
	// MaxUnpackedBytes 是归档展开后的总字节上限。
	MaxUnpackedBytes int64
	// MaxFiles 是归档里的文件数上限。
	MaxFiles int
	// MaxEntryBytes 是单个文件的展开字节上限。
	MaxEntryBytes int64
	// MaxRedirects 是跟随重定向的次数上限。
	MaxRedirects int
	// Timeout 是整个下载（含重定向）的时间上限。
	Timeout time.Duration
}

// DefaultLinkLimits 是生产默认值。
var DefaultLinkLimits = LinkLimits{
	MaxDownloadBytes: 16 << 20,
	MaxUnpackedBytes: maxSkillTreeBytes,
	MaxFiles:         maxSkillFiles,
	MaxEntryBytes:    4 << 20,
	MaxRedirects:     3,
	Timeout:          60 * time.Second,
}

// ---------------------------------------------------------------- 拒绝规则
//
// 规则 ID 是稳定契约：前端按 ID 取本地化文案，不解析 Go 的 message
//（en-US 下显示中文 message 是明确禁止的）。

const (
	// 目标地址与协议。
	LinkRuleScheme              = "LINK_SCHEME_HTTPS_ONLY"
	LinkRuleUserinfo            = "LINK_USERINFO_REJECTED"
	LinkRuleHostInvalid         = "LINK_HOST_INVALID"
	LinkRulePortInvalid         = "LINK_PORT_INVALID"
	LinkRuleTooManyRedirects    = "LINK_TOO_MANY_REDIRECTS"
	LinkRuleRedirectScheme      = "LINK_REDIRECT_SCHEME"
	LinkRuleSpecialPurpose      = "LINK_SPECIAL_PURPOSE_ADDRESS"
	LinkRulePrivatePeer         = "LINK_PRIVATE_PEER"
	LinkRulePeerUnknown         = "LINK_PEER_UNKNOWN"
	LinkRuleDNSFailed           = "LINK_DNS_FAILED"
	LinkRuleDownloadTooLarge    = "LINK_DOWNLOAD_TOO_LARGE"
	LinkRuleTimeout             = "LINK_TIMEOUT"
	LinkRuleDownloadFailed      = "LINK_DOWNLOAD_FAILED"
	LinkRuleUnsupportedForm     = "LINK_UNSUPPORTED_FORM"
	LinkRuleArchiveTooLarge     = "LINK_ARCHIVE_TOO_LARGE"
	LinkRuleArchiveFileCount    = "LINK_ARCHIVE_FILE_COUNT"
	LinkRuleArchiveEntryLarge   = "LINK_ARCHIVE_ENTRY_TOO_LARGE"
	LinkRuleArchiveTraversal    = "LINK_ARCHIVE_PATH_TRAVERSAL"
	LinkRuleArchiveSymlink      = "LINK_ARCHIVE_SYMLINK"
	LinkRuleArchiveNoSkill      = "LINK_ARCHIVE_NO_SKILL_MD"
	LinkRuleArchiveCorrupt      = "LINK_ARCHIVE_CORRUPT"
	LinkRuleManifestUTF8        = "LINK_MANIFEST_UTF8"
	LinkRuleManifestFrontmatter = "LINK_MANIFEST_FRONTMATTER"
	LinkRuleManifestYAML        = "LINK_MANIFEST_YAML"
	LinkRuleManifestName        = "LINK_MANIFEST_NAME"
	LinkRuleManifestDescription = "LINK_MANIFEST_DESCRIPTION"
	LinkRuleStagingFailed       = "LINK_STAGING_FAILED"
)

// LinkRejection 是一条可逐项展示的拒绝原因。
//
// Rule 是稳定 ID；Params 只放诊断细节（地址、段名、上限数字），
// 文案由前端 catalog 决定。
type LinkRejection struct {
	Rule   string            `json:"rule"`
	Params map[string]string `json:"params,omitempty"`
}

// LinkRejectError 把一条或多条拒绝原因当成错误返回。
//
// 单独一个类型是为了让调用方能把"按规则拒绝"与"内部故障"分开：
// 前者要逐项展示规则，后者才是一个错误码。
type LinkRejectError struct {
	Rejections []LinkRejection
}

func (e *LinkRejectError) Error() string {
	parts := make([]string, 0, len(e.Rejections))
	for _, r := range e.Rejections {
		if r.Params["detail"] != "" {
			parts = append(parts, r.Rule+": "+r.Params["detail"])
			continue
		}
		parts = append(parts, r.Rule)
	}
	return strings.Join(parts, "; ")
}

// linkReject 构造一条规则拒绝。
func linkReject(rule string, params map[string]string) *LinkRejectError {
	if params == nil {
		params = map[string]string{}
	}
	return &LinkRejectError{Rejections: []LinkRejection{{Rule: rule, Params: params}}}
}

// AsLinkRejections 取出规则拒绝；不是规则拒绝时返回 false。
func AsLinkRejections(err error) ([]LinkRejection, bool) {
	var reject *LinkRejectError
	if errors.As(err, &reject) {
		return reject.Rejections, true
	}
	return nil, false
}

// ---------------------------------------------------------------- 公开目的地判定

// SpecialPurposeRange 是 IANA special-purpose 表里的一行。
type SpecialPurposeRange struct {
	CIDR string `json:"cidr"`
	Name string `json:"name"`
}

// specialPurposeRegistrySource / Version 是这张表的来源与版本。
//
// 表必须"有出处、可更新"：零散 hard-code 的 deny list 既说不清为什么拒，
// 也无法在 IANA 更新后核对差异。版本按"取表日期 + 表名"记录。
const (
	SpecialPurposeRegistrySource  = "IANA IPv4/IPv6 Special-Purpose Address Registry"
	SpecialPurposeRegistryVersion = "2026-11-05"
)

// specialPurposeTable 是判定用的完整表。
//
// 只收"绝对不适合作为下载来源"的段：本机、私网、共享地址（CGNAT）、
// 基准测试、文档、保留、组播、未指定、IPv4-mapped、6to4 等。
// 明确标注为 globally reachable 的段（如 192.31.196.0/24）不在这里——
// 把它们一起拒掉会让判定变成"凭感觉的 deny list"。
var specialPurposeTable = []SpecialPurposeRange{
	// ---- IPv4 ----
	{CIDR: "0.0.0.0/8", Name: "This network"},
	{CIDR: "10.0.0.0/8", Name: "Private-Use"},
	{CIDR: "100.64.0.0/10", Name: "Shared Address Space (CGNAT)"},
	{CIDR: "127.0.0.0/8", Name: "Loopback"},
	{CIDR: "169.254.0.0/16", Name: "Link Local"},
	{CIDR: "172.16.0.0/12", Name: "Private-Use"},
	{CIDR: "192.0.0.0/24", Name: "IETF Protocol Assignments"},
	{CIDR: "192.0.2.0/24", Name: "Documentation (TEST-NET-1)"},
	{CIDR: "192.88.99.0/24", Name: "6to4 Relay Anycast (deprecated)"},
	{CIDR: "192.168.0.0/16", Name: "Private-Use"},
	{CIDR: "198.18.0.0/15", Name: "Benchmarking"},
	{CIDR: "198.51.100.0/24", Name: "Documentation (TEST-NET-2)"},
	{CIDR: "203.0.113.0/24", Name: "Documentation (TEST-NET-3)"},
	{CIDR: "224.0.0.0/4", Name: "Multicast"},
	{CIDR: "240.0.0.0/4", Name: "Reserved"},
	{CIDR: "255.255.255.255/32", Name: "Limited Broadcast"},

	// ---- IPv6 ----
	{CIDR: "::/128", Name: "Unspecified"},
	{CIDR: "::1/128", Name: "Loopback"},
	{CIDR: "::ffff:0:0/96", Name: "IPv4-Mapped"},
	{CIDR: "64:ff9b::/96", Name: "IPv4-IPv6 Translation"},
	{CIDR: "64:ff9b:1::/48", Name: "IPv4-IPv6 Translation (local-use)"},
	{CIDR: "100::/64", Name: "Discard-Only"},
	{CIDR: "2001::/23", Name: "IETF Protocol Assignments"},
	{CIDR: "2001:2::/48", Name: "Benchmarking"},
	{CIDR: "2001:db8::/32", Name: "Documentation"},
	{CIDR: "2002::/16", Name: "6to4"},
	{CIDR: "3fff::/20", Name: "Documentation"},
	{CIDR: "5f00::/16", Name: "Segment Routing SIDs"},
	{CIDR: "fc00::/7", Name: "Unique-Local"},
	{CIDR: "fe80::/10", Name: "Link-Local"},
	{CIDR: "ff00::/8", Name: "Multicast"},
}

var specialPurposeNets []struct {
	net  *net.IPNet
	item SpecialPurposeRange
}

func init() {
	for _, item := range specialPurposeTable {
		_, n, err := net.ParseCIDR(item.CIDR)
		if err != nil {
			// 表是包内常量，解析失败属于编码错误，早失败比悄悄少一条判定好。
			panic("resources: 非法的 special-purpose CIDR " + item.CIDR + ": " + err.Error())
		}
		specialPurposeNets = append(specialPurposeNets, struct {
			net  *net.IPNet
			item SpecialPurposeRange
		}{n, item})
	}
}

// SpecialPurposeRanges 返回判定用的完整表（诊断与证据用）。
func SpecialPurposeRanges() []SpecialPurposeRange {
	out := make([]SpecialPurposeRange, len(specialPurposeTable))
	copy(out, specialPurposeTable)
	return out
}

// CheckPublicDestination 判定一个地址能否作为下载来源。
//
// 返回 false 时第二个返回值是命中的段名（用于逐项展示"为什么拒"）。
// IPv4-mapped IPv6 经 To4 归一到 IPv4 再判，避免 "::ffff:127.0.0.1" 绕过。
func CheckPublicDestination(ip net.IP) (bool, string) {
	if ip == nil {
		return false, "Empty Address"
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	bits := 128
	if v4 := ip.To4(); v4 != nil {
		bits = 32
	}
	var (
		hit     bool
		hitName string
		hitOnes int
	)
	for _, entry := range specialPurposeNets {
		ones, total := entry.net.Mask.Size()
		if total != bits {
			continue
		}
		if !entry.net.Contains(ip) {
			continue
		}
		// 最长前缀优先：2001:db8::/32 应报 Documentation，而不是更宽的 2001::/23。
		if !hit || ones > hitOnes {
			hit, hitName, hitOnes = true, entry.item.Name, ones
		}
	}
	if hit {
		return false, hitName
	}
	return true, ""
}

// ---------------------------------------------------------------- 解析器与拨号器

// HostResolver 把主机名解析成 IP 列表。测试可注入，生产用系统解析器。
type HostResolver interface {
	LookupIP(ctx context.Context, host string) ([]net.IP, error)
}

type systemResolver struct{}

func (systemResolver) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// LinkInspector 是链接导入的检查器。
type LinkInspector struct {
	// Root 是应用数据根；暂存目录落在 <Root>/skill-links 下。
	Root string
	// Limits 为空字段时按 DefaultLinkLimits 补齐。
	Limits LinkLimits
	// Resolve 可注入（测试）；nil 用系统解析器。
	Resolve HostResolver
	// Dial 可注入（测试）；生产 nil 表示真的拨号。
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// RootCAs 可注入（测试自签证书）；nil 用系统根。
	RootCAs *x509.CertPool
}

// NewLinkInspector 构造一个使用生产默认值的检查器。
func NewLinkInspector(root string) *LinkInspector {
	return &LinkInspector{Root: root}
}

// StagingRoot 是链接暂存根目录。
func (in *LinkInspector) StagingRoot() string {
	return filepath.Join(in.Root, "skill-links")
}

func (in *LinkInspector) limits() LinkLimits {
	out := in.Limits
	def := DefaultLinkLimits
	if out.MaxDownloadBytes <= 0 {
		out.MaxDownloadBytes = def.MaxDownloadBytes
	}
	if out.MaxUnpackedBytes <= 0 {
		out.MaxUnpackedBytes = def.MaxUnpackedBytes
	}
	if out.MaxFiles <= 0 {
		out.MaxFiles = def.MaxFiles
	}
	if out.MaxEntryBytes <= 0 {
		out.MaxEntryBytes = def.MaxEntryBytes
	}
	if out.MaxRedirects <= 0 {
		out.MaxRedirects = def.MaxRedirects
	}
	if out.Timeout <= 0 {
		out.Timeout = def.Timeout
	}
	return out
}

func (in *LinkInspector) resolve(ctx context.Context, host string) ([]net.IP, error) {
	if in.Resolve != nil {
		return in.Resolve.LookupIP(ctx, host)
	}
	return systemResolver{}.LookupIP(ctx, host)
}

// LinkInspection 是一次检查的结果。
//
// 有 Rejections 时 StagedDir 一定为空：拒绝路径不落暂存，也不碰托管目录。
type LinkInspection struct {
	URL                    string   `json:"url"`
	FinalURL               string   `json:"finalUrl"`
	Form                   string   `json:"form"`
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	Requires               []string `json:"requires"`
	DisableModelInvocation bool     `json:"disableModelInvocation"`
	Hash                   string   `json:"hash"`
	Bytes                  int64    `json:"bytes"`
	Files                  int      `json:"files"`
	// StagedDir 是暂存目录；确认后把 StagedSkillPath 交给 ImportSkill。
	StagedDir       string          `json:"stagedDir"`
	StagedSkillPath string          `json:"stagedSkillPath"`
	Redirects       []string        `json:"redirects"`
	Warnings        []string        `json:"warnings"`
	Rejections      []LinkRejection `json:"rejections"`
}

// ---------------------------------------------------------------- 入口

// Inspect 检查一个链接，成功时把内容暂存下来并返回解析后的清单。
//
// 返回 *LinkRejectError 表示按规则拒绝；其余错误是内部故障。
func (in *LinkInspector) Inspect(ctx context.Context, rawURL string) (LinkInspection, error) {
	limits := in.limits()
	out := LinkInspection{URL: strings.TrimSpace(rawURL), Requires: []string{}, Redirects: []string{}, Warnings: []string{}, Rejections: []LinkRejection{}}

	start, err := validateLinkURL(out.URL)
	if err != nil {
		return withRejection(out, err)
	}

	// 先清扫过期暂存：暂存是"用户还没确认"的临时副本，没有清理策略就会
	// 一直堆在数据目录里。
	in.sweepStagedLinks(30 * time.Minute)

	var (
		mu        sync.Mutex
		redirects []string
	)
	transport := in.transport(limits, &mu, &redirects)
	client := &http.Client{
		Transport: transport,
		Timeout:   limits.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return in.checkRedirect(ctx, req, via, limits, &mu, &redirects)
		},
	}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, start.String(), nil)
	if err != nil {
		return withRejection(out, linkReject(LinkRuleDownloadFailed, map[string]string{"detail": err.Error()}))
	}
	req.Header.Set("Accept", "text/markdown, text/plain, application/zip, application/gzip, application/x-tar, */*")
	resp, err := client.Do(req)
	if err != nil {
		return withRejection(out, downloadError(err))
	}
	defer resp.Body.Close()

	mu.Lock()
	out.Redirects = append([]string{}, redirects...)
	mu.Unlock()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return withRejection(out, linkReject(LinkRuleDownloadFailed, map[string]string{
			"status": strconv.Itoa(resp.StatusCode),
		}))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limits.MaxDownloadBytes+1))
	if err != nil {
		return withRejection(out, downloadError(err))
	}
	if int64(len(body)) > limits.MaxDownloadBytes {
		return withRejection(out, linkReject(LinkRuleDownloadTooLarge, map[string]string{
			"limit": strconv.FormatInt(limits.MaxDownloadBytes, 10),
		}))
	}
	out.FinalURL = resp.Request.URL.String()

	form := detectForm(resp.Request.URL, resp.Header.Get("Content-Type"), body)
	out.Form = form
	if form == "" {
		return withRejection(out, linkReject(LinkRuleUnsupportedForm, map[string]string{
			"url": resp.Request.URL.String(),
		}))
	}

	stagingRoot := in.StagingRoot()
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return out, linkReject(LinkRuleStagingFailed, map[string]string{"detail": err.Error()})
	}
	stagedDir, err := os.MkdirTemp(stagingRoot, "link-")
	if err != nil {
		return out, linkReject(LinkRuleStagingFailed, map[string]string{"detail": err.Error()})
	}
	out.StagedDir = stagedDir
	// 拒绝路径上不保留任何下载内容。
	discard := true
	defer func() {
		if discard {
			_ = os.RemoveAll(stagedDir)
			out.StagedDir = ""
			out.StagedSkillPath = ""
		}
	}()

	var extractErr error
	switch form {
	case "skill-md":
		extractErr = writeStagedFile(stagedDir, "SKILL.md", body, limits)
	case "zip":
		extractErr = extractZip(body, stagedDir, limits, &out)
	case "tar.gz":
		extractErr = extractTarGz(body, stagedDir, limits, &out)
	}
	if extractErr != nil {
		return withRejection(out, extractErr)
	}

	skillDir, err := locateStagedSkillDir(stagedDir)
	if err != nil {
		return withRejection(out, err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	doc, err := readSkillFile(skillPath)
	if err != nil {
		rule, params := classifyManifestError(err)
		return withRejection(out, linkReject(rule, params))
	}
	out.StagedSkillPath = skillPath
	out.Name = doc.Manifest.Name
	out.Description = doc.Manifest.Description
	out.Requires = append([]string{}, doc.Manifest.Requires...)
	out.DisableModelInvocation = doc.Manifest.DisableModelInvocation
	out.Hash = doc.Hash
	out.Bytes = doc.Bytes
	countStagedFiles(stagedDir, &out)
	out.Warnings = append(out.Warnings, describeStagedWarnings(skillDir)...)
	discard = false
	return out, nil
}

// withRejection 把拒绝原因写进结果并返回。
func withRejection(out LinkInspection, err error) (LinkInspection, error) {
	if rejections, ok := AsLinkRejections(err); ok {
		out.Rejections = append(out.Rejections, rejections...)
		out.StagedDir = ""
		out.StagedSkillPath = ""
		return out, err
	}
	return out, err
}

// downloadError 把超时与网络错误分开：超时要用户重试，DNS 失败要用户检查地址。
func downloadError(err error) error {
	if isTimeout(err) {
		return linkReject(LinkRuleTimeout, map[string]string{"detail": err.Error()})
	}
	var reject *LinkRejectError
	if errors.As(err, &reject) {
		return reject
	}
	return linkReject(LinkRuleDownloadFailed, map[string]string{"detail": err.Error()})
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	return false
}

// validateLinkURL 校验初始地址。
func validateLinkURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, linkReject(LinkRuleHostInvalid, map[string]string{"detail": "empty url"})
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, linkReject(LinkRuleHostInvalid, map[string]string{"detail": err.Error()})
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, linkReject(LinkRuleScheme, map[string]string{"scheme": u.Scheme})
	}
	if u.User != nil {
		return nil, linkReject(LinkRuleUserinfo, map[string]string{"host": u.Hostname()})
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return nil, linkReject(LinkRuleHostInvalid, map[string]string{"detail": "missing host"})
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, linkReject(LinkRulePortInvalid, map[string]string{"port": port})
		}
	}
	return u, nil
}

// checkRedirect 对每次重定向做协议与目的地校验。
func (in *LinkInspector) checkRedirect(ctx context.Context, req *http.Request, via []*http.Request, limits LinkLimits, mu *sync.Mutex, redirects *[]string) error {
	if len(via) > limits.MaxRedirects {
		return linkReject(LinkRuleTooManyRedirects, map[string]string{
			"limit": strconv.Itoa(limits.MaxRedirects),
		})
	}
	u := req.URL
	if !strings.EqualFold(u.Scheme, "https") {
		return linkReject(LinkRuleRedirectScheme, map[string]string{"scheme": u.Scheme})
	}
	if u.User != nil {
		return linkReject(LinkRuleUserinfo, map[string]string{"host": u.Hostname()})
	}
	host := u.Hostname()
	if host == "" {
		return linkReject(LinkRuleHostInvalid, map[string]string{"detail": "missing host"})
	}
	if err := in.assertPublicAnswers(ctx, host); err != nil {
		return err
	}
	mu.Lock()
	*redirects = append(*redirects, u.String())
	mu.Unlock()
	return nil
}

// assertPublicAnswers 要求一个主机的**所有** DNS answer 都是公开目的地。
//
// 只要有一条 answer 落在 special-purpose 段就拒绝：攻击者可以让解析器返回
// 一个公网地址和一条 127.0.0.1，客户端挑哪条取决于运气。
func (in *LinkInspector) assertPublicAnswers(ctx context.Context, host string) error {
	ips, err := in.resolve(ctx, host)
	if err != nil {
		return linkReject(LinkRuleDNSFailed, map[string]string{"host": host, "detail": err.Error()})
	}
	if len(ips) == 0 {
		return linkReject(LinkRuleDNSFailed, map[string]string{"host": host, "detail": "no answer"})
	}
	for _, ip := range ips {
		if ok, name := CheckPublicDestination(ip); !ok {
			return linkReject(LinkRuleSpecialPurpose, map[string]string{
				"host": host, "address": ip.String(), "range": name,
			})
		}
	}
	return nil
}

// transport 构造一个不继承代理、按已校验 IP 直连的 transport。
func (in *LinkInspector) transport(limits LinkLimits, mu *sync.Mutex, redirects *[]string) *http.Transport {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if in.RootCAs != nil {
		cfg.RootCAs = in.RootCAs
	}
	return &http.Transport{
		// 明确不使用任何代理：环境里的 HTTP(S)_PROXY 会把"连接实际去了哪里"
		// 变成另一件事，SSRF 校验就无从谈起。
		Proxy:                 nil,
		TLSClientConfig:       cfg,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          2,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return in.dialValidated(ctx, network, addr)
		},
	}
}

// dialValidated 解析 → 全量校验 → 按 IP 直连 → 连接后再核对 peer。
func (in *LinkInspector) dialValidated(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, linkReject(LinkRuleHostInvalid, map[string]string{"detail": err.Error()})
	}
	// 只解析一次：解析结果与校验结果必须是同一份列表。
	// 先 assertPublicAnswers 再单独 resolve 会留下"校验的是第一次答案、
	// 连的是第二次答案"的窗口——那正是 DNS rebinding 的入口。
	ips, err := in.resolve(ctx, host)
	if err != nil {
		return nil, linkReject(LinkRuleDNSFailed, map[string]string{"host": host, "detail": err.Error()})
	}
	if len(ips) == 0 {
		return nil, linkReject(LinkRuleDNSFailed, map[string]string{"host": host, "detail": "no answer"})
	}
	for _, ip := range ips {
		if ok, name := CheckPublicDestination(ip); !ok {
			return nil, linkReject(LinkRuleSpecialPurpose, map[string]string{
				"host": host, "address": ip.String(), "range": name,
			})
		}
	}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, address)
	}
	if in.Dial != nil {
		dial = in.Dial
	}
	var lastErr error
	for _, ip := range ips {
		target := net.JoinHostPort(ip.String(), port)
		conn, dialErr := dial(ctx, network, target)
		if dialErr != nil {
			lastErr = dialErr
			continue
		}
		peer := peerIP(conn)
		if peer == nil {
			_ = conn.Close()
			return nil, linkReject(LinkRulePeerUnknown, map[string]string{"host": host})
		}
		if ok, name := CheckPublicDestination(peer); !ok {
			_ = conn.Close()
			return nil, linkReject(LinkRulePrivatePeer, map[string]string{
				"host": host, "address": peer.String(), "range": name,
			})
		}
		return conn, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no address to dial")
	}
	return nil, linkReject(LinkRuleDownloadFailed, map[string]string{"detail": lastErr.Error()})
}

func peerIP(conn net.Conn) net.IP {
	addr := conn.RemoteAddr()
	if addr == nil {
		return nil
	}
	switch v := addr.(type) {
	case *net.TCPAddr:
		return v.IP
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return net.ParseIP(addr.String())
	}
	return net.ParseIP(host)
}

// ---------------------------------------------------------------- 形式判定与解包

// detectForm 判定这是单个 SKILL.md 还是归档。
//
// 先看 URL 后缀与 Content-Type，再看魔数：有些站点用 application/octet-stream
// 发 zip，也有的把 .md 当 text/plain 发。三者都看不出来才拒绝。
func detectForm(u *url.URL, contentType string, body []byte) string {
	path := strings.ToLower(u.Path)
	ct := strings.ToLower(contentType)
	switch {
	case strings.HasSuffix(path, ".zip"):
		return "zip"
	case strings.HasSuffix(path, ".tar.gz"), strings.HasSuffix(path, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(path, "skill.md"), strings.HasSuffix(path, ".md"):
		return "skill-md"
	}
	if len(body) >= 4 && bytes.Equal(body[:4], []byte{'P', 'K', 0x03, 0x04}) {
		return "zip"
	}
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		return "tar.gz"
	}
	switch {
	case strings.Contains(ct, "markdown"):
		return "skill-md"
	case strings.Contains(ct, "zip"):
		return "zip"
	case strings.Contains(ct, "gzip"), strings.Contains(ct, "x-tar"):
		return "tar.gz"
	}
	return ""
}

// safeEntryPath 把归档里的名字变成暂存目录内的路径，越界一律拒绝。
func safeEntryPath(base, name string) (string, error) {
	raw := strings.TrimSpace(name)
	if raw == "" {
		return "", linkReject(LinkRuleArchiveTraversal, map[string]string{"entry": name})
	}
	slash := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(slash, "/") || filepath.IsAbs(raw) {
		return "", linkReject(LinkRuleArchiveTraversal, map[string]string{"entry": name})
	}
	if len(slash) >= 2 && slash[1] == ':' {
		return "", linkReject(LinkRuleArchiveTraversal, map[string]string{"entry": name})
	}
	for _, part := range strings.Split(slash, "/") {
		if part == ".." {
			return "", linkReject(LinkRuleArchiveTraversal, map[string]string{"entry": name})
		}
	}
	target := filepath.Join(base, filepath.FromSlash(slash))
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", linkReject(LinkRuleArchiveTraversal, map[string]string{"entry": name})
	}
	return target, nil
}

// writeStagedFile 写一个暂存文件：只给读写权限，绝不带可执行位。
func writeStagedFile(dir, name string, data []byte, limits LinkLimits) error {
	if int64(len(data)) > limits.MaxEntryBytes {
		return linkReject(LinkRuleArchiveEntryLarge, map[string]string{
			"entry": name, "size": strconv.Itoa(len(data)), "limit": strconv.FormatInt(limits.MaxEntryBytes, 10),
		})
	}
	target, err := safeEntryPath(dir, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return linkReject(LinkRuleStagingFailed, map[string]string{"detail": err.Error()})
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return linkReject(LinkRuleStagingFailed, map[string]string{"detail": err.Error()})
	}
	return nil
}

// extractZip 解包 zip，并按声明/实际两条线限制体积与文件数。
func extractZip(body []byte, dir string, limits LinkLimits, out *LinkInspection) error {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return linkReject(LinkRuleArchiveCorrupt, map[string]string{"detail": err.Error()})
	}
	if len(zr.File) > limits.MaxFiles {
		return linkReject(LinkRuleArchiveFileCount, map[string]string{
			"count": strconv.Itoa(len(zr.File)), "limit": strconv.Itoa(limits.MaxFiles),
		})
	}
	var (
		files int
		total uint64
	)
	for _, f := range zr.File {
		if f.Mode()&fs.ModeSymlink != 0 {
			return linkReject(LinkRuleArchiveSymlink, map[string]string{"entry": f.Name})
		}
		target, err := safeEntryPath(dir, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return linkReject(LinkRuleStagingFailed, map[string]string{"detail": err.Error()})
			}
			continue
		}
		if !f.Mode().IsRegular() && f.Mode() != 0 {
			return linkReject(LinkRuleArchiveSymlink, map[string]string{"entry": f.Name})
		}
		files++
		if files > limits.MaxFiles {
			return linkReject(LinkRuleArchiveFileCount, map[string]string{
				"count": strconv.Itoa(files), "limit": strconv.Itoa(limits.MaxFiles),
			})
		}
		// 声明大小先判：zip bomb 的典型形态是一个几十 KiB 的包声明解开后几个 GiB。
		declared := f.UncompressedSize64
		if declared > uint64(limits.MaxEntryBytes) {
			return linkReject(LinkRuleArchiveEntryLarge, map[string]string{
				"entry": f.Name, "size": strconv.FormatUint(declared, 10),
				"limit": strconv.FormatInt(limits.MaxEntryBytes, 10),
			})
		}
		if total+declared > uint64(limits.MaxUnpackedBytes) {
			return linkReject(LinkRuleArchiveTooLarge, map[string]string{
				"kind": "declared", "limit": strconv.FormatInt(limits.MaxUnpackedBytes, 10),
			})
		}
		rc, err := f.Open()
		if err != nil {
			return linkReject(LinkRuleArchiveCorrupt, map[string]string{"entry": f.Name, "detail": err.Error()})
		}
		limited := io.LimitReader(rc, int64(limits.MaxEntryBytes)+1)
		data, readErr := io.ReadAll(limited)
		_ = rc.Close()
		if readErr != nil {
			return linkReject(LinkRuleArchiveCorrupt, map[string]string{"entry": f.Name, "detail": readErr.Error()})
		}
		// 实际大小与声明不符（声明小、实际大）是最危险的一种：必须按实际字节拒绝。
		if int64(len(data)) > limits.MaxEntryBytes {
			return linkReject(LinkRuleArchiveEntryLarge, map[string]string{
				"entry": f.Name, "size": strconv.Itoa(len(data)),
				"limit": strconv.FormatInt(limits.MaxEntryBytes, 10),
			})
		}
		if uint64(len(data)) != declared {
			return linkReject(LinkRuleArchiveCorrupt, map[string]string{
				"entry": f.Name, "declared": strconv.FormatUint(declared, 10),
				"actual": strconv.Itoa(len(data)),
			})
		}
		total += uint64(len(data))
		if total > uint64(limits.MaxUnpackedBytes) {
			return linkReject(LinkRuleArchiveTooLarge, map[string]string{
				"kind": "actual", "limit": strconv.FormatInt(limits.MaxUnpackedBytes, 10),
			})
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return linkReject(LinkRuleStagingFailed, map[string]string{"detail": err.Error()})
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return linkReject(LinkRuleStagingFailed, map[string]string{"detail": err.Error()})
		}
	}
	out.Files = files
	return nil
}

// extractTarGz 解包 tar.gz，规则与 zip 相同。
func extractTarGz(body []byte, dir string, limits LinkLimits, out *LinkInspection) error {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return linkReject(LinkRuleArchiveCorrupt, map[string]string{"detail": err.Error()})
	}
	defer gz.Close()
	// 展开流的硬上限：gzip 的压缩比可以极高，仅靠 tar 头里的 Size 不够，
	// 因为头部本身就来自解压流，读完之前无法知道下一个头是什么。
	limited := io.LimitReader(gz, limits.MaxUnpackedBytes+limits.MaxEntryBytes+1024)
	tr := tar.NewReader(limited)
	var (
		files int
		total int64
	)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return linkReject(LinkRuleArchiveCorrupt, map[string]string{"detail": err.Error()})
		}
		switch hdr.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		case tar.TypeDir:
			target, pathErr := safeEntryPath(dir, hdr.Name)
			if pathErr != nil {
				return pathErr
			}
			if err := os.MkdirAll(target, 0o700); err != nil {
				return linkReject(LinkRuleStagingFailed, map[string]string{"detail": err.Error()})
			}
			continue
		case tar.TypeReg:
		default:
			// 符号链接/硬链接/设备/管道一律拒绝：它们能把暂存目录指到任意位置。
			return linkReject(LinkRuleArchiveSymlink, map[string]string{"entry": hdr.Name})
		}
		target, pathErr := safeEntryPath(dir, hdr.Name)
		if pathErr != nil {
			return pathErr
		}
		files++
		if files > limits.MaxFiles {
			return linkReject(LinkRuleArchiveFileCount, map[string]string{
				"count": strconv.Itoa(files), "limit": strconv.Itoa(limits.MaxFiles),
			})
		}
		if hdr.Size > limits.MaxEntryBytes {
			return linkReject(LinkRuleArchiveEntryLarge, map[string]string{
				"entry": hdr.Name, "size": strconv.FormatInt(hdr.Size, 10),
				"limit": strconv.FormatInt(limits.MaxEntryBytes, 10),
			})
		}
		if total+hdr.Size > limits.MaxUnpackedBytes {
			return linkReject(LinkRuleArchiveTooLarge, map[string]string{
				"kind": "declared", "limit": strconv.FormatInt(limits.MaxUnpackedBytes, 10),
			})
		}
		data, readErr := io.ReadAll(io.LimitReader(tr, hdr.Size+1))
		if readErr != nil {
			return linkReject(LinkRuleArchiveCorrupt, map[string]string{"entry": hdr.Name, "detail": readErr.Error()})
		}
		if int64(len(data)) != hdr.Size {
			return linkReject(LinkRuleArchiveCorrupt, map[string]string{
				"entry": hdr.Name, "declared": strconv.FormatInt(hdr.Size, 10),
				"actual": strconv.Itoa(len(data)),
			})
		}
		total += int64(len(data))
		if total > limits.MaxUnpackedBytes {
			return linkReject(LinkRuleArchiveTooLarge, map[string]string{
				"kind": "actual", "limit": strconv.FormatInt(limits.MaxUnpackedBytes, 10),
			})
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return linkReject(LinkRuleStagingFailed, map[string]string{"detail": err.Error()})
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return linkReject(LinkRuleStagingFailed, map[string]string{"detail": err.Error()})
		}
	}
	out.Files = files
	return nil
}

// locateStagedSkillDir 找到暂存里的技能目录（SKILL.md 所在目录）。
//
// 只接受"根部"或"唯一一层子目录"：多个候选说明这不是一个技能包，
// 随便挑一个会让用户导入到一个他没看见的技能。
func locateStagedSkillDir(stagedDir string) (string, error) {
	if _, err := os.Stat(filepath.Join(stagedDir, "SKILL.md")); err == nil {
		return stagedDir, nil
	}
	entries, err := os.ReadDir(stagedDir)
	if err != nil {
		return "", linkReject(LinkRuleArchiveCorrupt, map[string]string{"detail": err.Error()})
	}
	var candidates []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(stagedDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil {
			candidates = append(candidates, dir)
		}
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", linkReject(LinkRuleArchiveNoSkill, nil)
	default:
		return "", linkReject(LinkRuleArchiveNoSkill, map[string]string{"candidates": strconv.Itoa(len(candidates))})
	}
}

// countStagedFiles 统计暂存里的文件数（展示用，判定已经在解包时完成）。
func countStagedFiles(dir string, out *LinkInspection) {
	count := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		count++
		return nil
	})
	out.Files = count
}

// describeStagedWarnings 给出"能导入但要知道"的提示。
func describeStagedWarnings(skillDir string) []string {
	var warnings []string
	_ = filepath.WalkDir(skillDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		switch {
		case strings.HasSuffix(name, ".sh"), strings.HasSuffix(name, ".ps1"),
			strings.HasSuffix(name, ".bat"), strings.HasSuffix(name, ".cmd"),
			strings.HasSuffix(name, ".exe"), strings.HasSuffix(name, ".js"):
			rel, relErr := filepath.Rel(skillDir, p)
			if relErr == nil {
				warnings = append(warnings, "script:"+filepath.ToSlash(rel))
			}
		}
		return nil
	})
	return warnings
}

// classifyManifestError 把清单解析错误映射成稳定规则 ID。
//
// parseSkillContent 的错误消息本来就是给用户看的描述，但消息本身不能直接
// 作为跨语言文案，所以这里只做"归类"，细节放 params.detail 供诊断展示。
func classifyManifestError(err error) (string, map[string]string) {
	detail := messageOf(err)
	switch {
	case strings.Contains(detail, "合法 UTF-8"):
		return LinkRuleManifestUTF8, map[string]string{"detail": detail}
	case strings.Contains(detail, "合法 YAML"):
		return LinkRuleManifestYAML, map[string]string{"detail": detail}
	case strings.Contains(detail, "frontmatter"):
		return LinkRuleManifestFrontmatter, map[string]string{"detail": detail}
	case strings.Contains(detail, "description"):
		return LinkRuleManifestDescription, map[string]string{"detail": detail}
	case strings.Contains(detail, "name"):
		return LinkRuleManifestName, map[string]string{"detail": detail}
	default:
		return LinkRuleManifestFrontmatter, map[string]string{"detail": detail}
	}
}

// sweepStagedLinks 清扫超期的暂存目录。
func (in *LinkInspector) sweepStagedLinks(olderThan time.Duration) {
	root := in.StagingRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-olderThan)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

// SweepStagedLinks 供绑定层在导入成功后清理这一次的暂存副本。
func SweepStagedLinks(root, stagedPath string) error {
	in := NewLinkInspector(root)
	staging := in.StagingRoot()
	clean := filepath.Clean(stagedPath)
	rel, err := filepath.Rel(staging, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	first := strings.Split(rel, string(filepath.Separator))
	if len(first) == 0 || first[0] == "." {
		return nil
	}
	return os.RemoveAll(filepath.Join(staging, first[0]))
}
