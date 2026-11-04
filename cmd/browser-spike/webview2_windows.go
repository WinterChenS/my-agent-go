//go:build windows

package main

import (
	"golang.org/x/sys/windows/registry"
)

// webView2ClientGUID 是 Microsoft Edge WebView2 Runtime 在 EdgeUpdate 下的固定
// 客户端 GUID。B01 要记录实际运行时版本，这里先把它查出来写进证据，
// 免得"装了 WebView2"变成一句没有版本号的口头结论。
const webView2ClientGUID = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`

// webView2RuntimeVersion 返回已安装的 WebView2 运行时版本；查不到返回空串。
//
// 48 位进程下 32/64 位注册表都可能被重定向，所以两条路径都试，用户级安装
// （HKCU）也试：per-user 安装在这台机器上是常见形态。
func webView2RuntimeVersion() string {
	paths := []string{
		`SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + webView2ClientGUID,
		`SOFTWARE\Microsoft\EdgeUpdate\Clients\` + webView2ClientGUID,
	}
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, path := range paths {
			key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			version, _, err := key.GetStringValue("pv")
			_ = key.Close()
			if err == nil && version != "" {
				return version
			}
		}
	}
	return ""
}
