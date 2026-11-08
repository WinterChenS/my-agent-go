package resources

// 端点安全分类（MP1）。
//
// 产品要连的是三种端点：
//   - 本机（Ollama / LM Studio / vLLM / llama.cpp）：回环，无密钥或本地占位；
//   - 局域网网关（用户真实场景：http://192.168.3.10:3425/v1 + Bearer）：
//     明文 HTTP 是常见部署方式，一刀切拒绝会让这个场景用不了；
//   - 公网供应商：必须 HTTPS，或用户显式确认。
//
// 所以规则不是"只许 HTTPS"，而是"是否会把凭据暴露在本机网络之外"：
//   - https：任何地址都行；
//   - http + 回环：本机通信，不算 insecure；
//   - http + 私有网段：允许，但标 insecure，界面必须警告；
//   - http + 公网/无法判定为私有的主机名：默认拒绝，只有显式确认才放行（仍标 insecure）。
//
// 判定只看 URL 里的主机，不做 DNS 解析：解析结果可能随环境变化，而"用户输入了
// 一个公网域名却解析到内网"这种情形，按公网处理更安全。

import (
	"net"
	"net/url"
	"strings"

	"my-agent-go/internal/contracts"
)

type endpointClass int

const (
	endpointLoopback endpointClass = iota
	endpointPrivate
	endpointPublic
)

// isLoopbackHost 判断主机名是否指本机。
//
// 只认 "localhost" 与 IP 回环段；不做 DNS。"127.0.0.1.evil.com" 这种
// 看起来像回环的名字会落到 public，从而要求 HTTPS 或显式确认。
func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(strings.ToLower(strings.Trim(host, "[]")))
	if h == "" {
		return false
	}
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// isPrivateHost 判断是否是 RFC1918 / 100.64/10 / IPv6 ULA。
//
// 明确不把链路本地（169.254/16、fe80::/10）算作私有：它们通常是
// 自动配置地址，把它当"局域网"放行等于放宽了一档没有依据的例外。
func isPrivateHost(host string) bool {
	h := strings.TrimSpace(strings.Trim(host, "[]"))
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127:
			return true
		}
		return false
	}
	return len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc
}

// classifyEndpoint 返回主机属于哪一档，以及解析出的主机名与端口。
func classifyEndpoint(hostport string) (endpointClass, string, string, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		// 没有端口是合法的；url.URL.Host 允许省略。
		host = hostport
		port = ""
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return endpointPublic, "", "", contracts.NewAppError(contracts.CodeUnsupported,
			"端点缺少主机名: "+hostport, false)
	}
	switch {
	case isLoopbackHost(host):
		return endpointLoopback, host, port, nil
	case isPrivateHost(host):
		return endpointPrivate, host, port, nil
	}
	return endpointPublic, host, port, nil
}

// parseEndpoint 解析并校验端点 URL，返回 (是否明文 HTTP 到非回环, 错误)。
//
// allowInsecure 是用户的显式确认：只有它能让"公网 + 明文 HTTP"通过。
// 私网与回环的明文 HTTP 不需要确认——那正是局域网网关的常态。
func parseEndpoint(raw string, allowInsecure bool) (insecure bool, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false, contracts.NewAppError(contracts.CodeUnsupported, "端点无法解析: "+err.Error(), false)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false, contracts.NewAppError(contracts.CodeUnsupported,
			"端点只支持 http/https: "+raw, false)
	}
	if u.User != nil {
		return false, contracts.NewAppError(contracts.CodeUnauthorized,
			"端点不能内嵌用户名或密码；请改用凭证引用", false)
	}
	if u.Host == "" {
		return false, contracts.NewAppError(contracts.CodeUnsupported, "端点缺少主机名: "+raw, false)
	}
	if u.Scheme == "https" {
		return false, nil
	}
	class, host, _, err := classifyEndpoint(u.Host)
	if err != nil {
		return false, err
	}
	switch class {
	case endpointLoopback:
		return false, nil
	case endpointPrivate:
		return true, nil
	}
	if !allowInsecure {
		return false, contracts.NewAppError(contracts.CodeUnsupported,
			host+" 不是回环或私有网段地址，明文 HTTP 会把凭据暴露在公网上；"+
				"请改用 HTTPS，或显式确认使用明文 HTTP", false)
	}
	return true, nil
}
