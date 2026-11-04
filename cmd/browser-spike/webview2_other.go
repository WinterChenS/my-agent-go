//go:build !windows

package main

// webView2RuntimeVersion 在不支持 WebView2 的平台上返回空串。
//
// B00 的 spike 只在 Windows 上实跑；保留这个桩是为了让整个仓库在 macOS/Linux
// 上仍然 `go build ./...`，而不是让一个试验命令挡住别人的构建。
func webView2RuntimeVersion() string { return "" }
