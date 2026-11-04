package harnessmcp

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// 本文件与 internal/policybridge/auth.go 是两份独立实现，不是复制粘贴的疏忽。
//
// 两个端点的信任域不同：策略端点接受的是"扩展问能不能执行"，自有 MCP 接受的是
// "Pi 直接调用工具"。令牌、监听端口、会话绑定都必须各自独立——共用一份实现会让
// 将来改一处认证逻辑时同时改掉两条边界。认证是安全边界，宁可重复也不共享。

// newToken 生成 256 位随机令牌。
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 MCP 令牌失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// tokenMatches 用常量时间比较令牌，避免按字节提前返回造成时序侧信道。
func tokenMatches(got, want string) bool {
	if got == "" || want == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// bearerToken 从 Authorization 头取出 bearer 令牌。
//
// 只接受 `Bearer <token>`，不接受 query 参数：query 会进访问日志、浏览器历史与
// 反代记录，令牌一旦落进这些地方就等于泄露。
func bearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(v) <= len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(v[len(prefix):])
}

// hostAllowed 校验 Host 头指向本端点（防 DNS rebinding）。
//
// 攻击者可以把任意域名解析到 127.0.0.1，浏览器随后就会带着那个域名当 Host
// 打到本地端口。只认回环字面量与端口，域名一律不认。
func hostAllowed(hostport string, port int) bool {
	host, p, err := net.SplitHostPort(hostport)
	if err != nil || p != fmt.Sprint(port) {
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
//   - 无 Origin：非浏览器调用（Pi、CLI），放行但仍受令牌约束；
//   - 有 Origin 且是本端点：放行；
//   - 其它：拒绝，网页无法借用户浏览器打到本地 MCP。
func originAllowed(origin string, port int) bool {
	if origin == "" {
		return true
	}
	for _, scheme := range []string{"http://", "https://"} {
		if rest := strings.TrimPrefix(origin, scheme); rest != origin && hostAllowed(rest, port) {
			return true
		}
	}
	return false
}
