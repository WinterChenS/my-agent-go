package piinstall

// 本文件实现 F06 的下载通道（计划 F06 Step 3 的后半段）。
//
// 纪律：
//   - 只接受 HTTPS；主机白名单；**每一跳重定向都重新校验**（上限 5 跳）；
//   - 流式落盘到托管根里的 staging 文件，绝不写系统临时目录之外的地方；
//   - 边写边算 SHA-256，长度/哈希/（有签名时的）签名全部通过才允许下一步；
//   - 取消/失败不留半成品（.part 一定被删）；
//   - 声明大小就是硬上界：响应比声明大就失败（不信 Content-Length，也不无限读）。

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"my-agent-go/internal/contracts"
)

// maxRedirects 是允许的重定向跳数上限。
const maxRedirects = 5

// Downloader 是下载 + 校验的唯一入口。
//
// 生产路径：Client 为 nil（用默认 http.Client），AllowedHosts 为 nil（用
// DefaultAllowedHosts），PublicKey 为编译进去的包签名公钥（当前为空 = 上游没有签名）。
type Downloader struct {
	// Client 是 HTTP 客户端（测试注入 TLS fixture 客户端）。
	Client *http.Client
	// AllowedHosts 是主机白名单（nil → DefaultAllowedHosts）。
	AllowedHosts []string
	// PublicKey 是包签名公钥；为空时"有签名就失败"（fail closed）。
	PublicKey ed25519.PublicKey
	// MaxRedirects 覆盖默认跳数上限（测试用）。
	MaxRedirects int
}

// StagedFile 是落到 staging 目录里的包。
type StagedFile struct {
	Path              string `json:"path"`
	Size              int64  `json:"size"`
	SHA256            string `json:"sha256"`
	SignatureVerified bool   `json:"signatureVerified"`
}

// Fetch 下载一个清单条目到 dir（必须是托管根内的 staging 目录）。
func (d *Downloader) Fetch(ctx context.Context, entry ManifestEntry, dir string) (StagedFile, error) {
	var zero StagedFile
	if ctx == nil {
		ctx = context.Background()
	}
	allowed := d.AllowedHosts
	if len(allowed) == 0 {
		allowed = DefaultAllowedHosts
	}
	if err := entry.Validate(); err != nil {
		return zero, verificationError("清单条目不合法: " + err.Error())
	}
	if entry.SizeBytes <= 0 {
		return zero, contracts.NewAppError(contracts.CodeUnsupported,
			fmt.Sprintf("%s/%s 的 sizeBytes=%d：没有精确大小就不下载（不做无上界的下载）",
				entry.Version, entry.TargetID, entry.SizeBytes), false)
	}
	if err := ValidateURL(entry.URL, allowed); err != nil {
		return zero, verificationError("下载地址不可信: " + err.Error())
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return zero, fmt.Errorf("piinstall: 创建 staging 目录失败: %w", err)
	}

	maxRedirectsAllowed := d.MaxRedirects
	if maxRedirectsAllowed <= 0 {
		maxRedirectsAllowed = maxRedirects
	}
	client := d.httpClient(allowed, maxRedirectsAllowed)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.URL, nil)
	if err != nil {
		return zero, fmt.Errorf("piinstall: 构造下载请求失败: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		// CheckRedirect 返回的错误会被 net/http 包成 *url.Error：
		// 先把它里面的结构化错误原样抛出来，别降级成"网络不可达"。
		var appErr *contracts.AppError
		if errors.As(err, &appErr) {
			return zero, appErr
		}
		if ctx.Err() != nil {
			return zero, contracts.NewAppError(contracts.CodeRpcCancelled,
				"下载已取消: "+ctx.Err().Error(), true)
		}
		return zero, contracts.NewAppError(contracts.CodeProviderUnreachable,
			"下载失败: "+err.Error(), true)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return zero, contracts.NewAppError(contracts.CodeProviderUnreachable,
			fmt.Sprintf("下载返回 HTTP %d（期望 200）", resp.StatusCode), true)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != entry.SizeBytes {
		return zero, verificationError(fmt.Sprintf(
			"响应 Content-Length=%d 与清单 sizeBytes=%d 不一致", resp.ContentLength, entry.SizeBytes))
	}

	// 先写 .part：任何失败都把它删掉，绝不留下"看起来像包"的东西。
	final := filepath.Join(dir, filepath.Base(entry.AssetName))
	part := final + ".part"
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return zero, fmt.Errorf("piinstall: 创建 staging 目录失败: %w", err)
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return zero, fmt.Errorf("piinstall: 创建 staging 文件失败: %w", err)
	}
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(part)
	}

	h := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, entry.SizeBytes+1))
	if copyErr != nil {
		cleanup()
		if ctx.Err() != nil {
			return zero, contracts.NewAppError(contracts.CodeRpcCancelled,
				"下载已取消: "+ctx.Err().Error(), true)
		}
		return zero, contracts.NewAppError(contracts.CodeProviderUnreachable,
			"读取响应失败: "+copyErr.Error(), true)
	}
	if written > entry.SizeBytes {
		cleanup()
		return zero, verificationError(fmt.Sprintf(
			"响应体超过清单声明的大小（已读 %d > %d）", written, entry.SizeBytes))
	}
	if written != entry.SizeBytes {
		cleanup()
		return zero, verificationError(fmt.Sprintf(
			"下载长度 %d 与清单 sizeBytes=%d 不一致", written, entry.SizeBytes))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return zero, fmt.Errorf("piinstall: 关闭 staging 文件失败: %w", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(sum, entry.SHA256) {
		_ = os.Remove(part)
		return zero, verificationError(fmt.Sprintf("sha256 不符：下载得到 %s，清单要求 %s", sum, entry.SHA256))
	}
	verified, err := d.verifySignature(entry, part)
	if err != nil {
		_ = os.Remove(part)
		return zero, err
	}
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part)
		return zero, fmt.Errorf("piinstall: 落盘 staging 文件失败: %w", err)
	}
	return StagedFile{Path: final, Size: written, SHA256: sum, SignatureVerified: verified}, nil
}

// VerifyStagedFile 在 verify 阶段从磁盘重新读一遍：证明**盘上**的字节才是清单里的字节
// （下载时的哈希只证明流过的字节，不证明落盘之后没被换掉）。
func VerifyStagedFile(entry ManifestEntry, path string, pub ed25519.PublicKey) (StagedFile, error) {
	var zero StagedFile
	st, err := os.Stat(path)
	if err != nil {
		return zero, verificationError("staging 文件不可读: " + err.Error())
	}
	if st.Size() != entry.SizeBytes {
		return zero, verificationError(fmt.Sprintf("staging 文件大小 %d 与清单 %d 不一致", st.Size(), entry.SizeBytes))
	}
	sum, err := HashFileContent(path)
	if err != nil {
		return zero, verificationError("计算 staging 文件哈希失败: " + err.Error())
	}
	if !strings.EqualFold(sum, entry.SHA256) {
		return zero, verificationError(fmt.Sprintf("staging 文件 sha256=%s 与清单 %s 不一致", sum, entry.SHA256))
	}
	d := &Downloader{PublicKey: pub}
	verified, err := d.verifySignature(entry, path)
	if err != nil {
		return zero, err
	}
	return StagedFile{Path: path, Size: st.Size(), SHA256: sum, SignatureVerified: verified}, nil
}

// httpClient 返回带"每跳复核"的客户端。
//
// 注入的客户端只提供 Transport/Timeout；CheckRedirect 一律换成我们的实现，
// 免得测试客户端的宽松策略被当成生产策略。
func (d *Downloader) httpClient(allowed []string, maxRedirectsAllowed int) *http.Client {
	base := d.Client
	if base == nil {
		base = &http.Client{}
	}
	clone := *base
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirectsAllowed {
			return verificationError(fmt.Sprintf("重定向超过 %d 跳", maxRedirectsAllowed))
		}
		if err := ValidateURL(req.URL.String(), allowed); err != nil {
			return verificationError("重定向目标不可信: " + err.Error())
		}
		return nil
	}
	return &clone
}

// verifySignature 在条目带签名时校验分离签名；不带签名时跳过（上游 v1.5.0 就是这种）。
//
// fail closed：有签名但没有信任根 → 失败；签名不匹配 → 失败。
func (d *Downloader) verifySignature(entry ManifestEntry, path string) (bool, error) {
	if strings.TrimSpace(entry.Signature) == "" {
		return false, nil
	}
	if len(d.PublicKey) != ed25519.PublicKeySize {
		return false, verificationError(
			"包带签名但本二进制没有编译进对应的信任根（fail closed，拒绝安装）")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(entry.Signature), ""))
	if err != nil {
		return false, verificationError("签名不是合法 base64: " + err.Error())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return false, verificationError("读取待验签文件失败: " + err.Error())
	}
	if !ed25519.Verify(d.PublicKey, body, raw) {
		return false, verificationError("包签名校验失败（字节被改动或不是发行密钥签的）")
	}
	return true, nil
}

// verificationError 统一把校验类失败包装成 VERIFICATION_FAILED。
func verificationError(msg string) error {
	return contracts.NewAppError(contracts.CodeVerificationFailed, msg, false)
}
