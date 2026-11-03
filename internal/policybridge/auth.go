package policybridge

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// newToken 生成每 worker 独立的随机令牌。
//
// 32 字节来自 crypto/rand：这个值是对端（Pi 进程内的扩展）唯一能证明
// "我属于这个会话"的东西，可预测就等于没有。
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成策略令牌失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// tokenMatches 用常量时间比较令牌，避免按字节提前返回造成时序侧信道。
func tokenMatches(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// bearerToken 从 Authorization 头取出 bearer 令牌。
// 只接受 `Bearer <token>`，不接受 query 参数——query 会进日志与历史。
func bearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(v) <= len(prefix) {
		return ""
	}
	if !strings.EqualFold(v[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(v[len(prefix):])
}

// hostAllowed 校验 Host 头指向本端点。
//
// 这是防 DNS rebinding 的一环：攻击者可以把恶意域名解析到 127.0.0.1，
// 浏览器随后会把 Host 带成恶意域名。只认回环字面量，域名一律不认。
func hostAllowed(hostport string, port int) bool {
	host, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return false
	}
	if p != fmt.Sprint(port) {
		return false
	}
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// originAllowed 校验 Origin 头。
//
// 浏览器对跨源 POST 一定会带 Origin 且页面无法伪造它，所以：
//   - 没有 Origin：非浏览器调用（Pi 扩展、CLI），放行（仍受令牌约束）；
//   - 有 Origin 但不是本端点：一律拒，网页无法借用户浏览器打到本地策略服务。
func originAllowed(origin string, port int) bool {
	if origin == "" {
		return true
	}
	for _, scheme := range []string{"http://", "https://"} {
		rest := strings.TrimPrefix(origin, scheme)
		if rest != origin && hostAllowed(rest, port) {
			return true
		}
	}
	return false
}
