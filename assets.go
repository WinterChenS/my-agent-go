// Package anvil 收纳随二进制分发的产品资源。
//
// 目前只有一样：产品自带的 Pi 扩展（策略/内容澄清/附件投递）。它们必须跟着
// 二进制走，而不是跟着仓库布局走——打包后的可执行文件旁边没有 pi-extensions/，
// 而策略扩展是工具调用执行前授权的唯一强制点，缺了它每一次 run 都起不来。
//
// 为什么放在模块根：go:embed 不能引用 ..，而 pi-extensions/ 就在模块根。
// 这里只导出**只读**读取，"要不要重写盘上那份"由 app 层的物化逻辑判断——
// 嵌入内容本身没有任何可写入口。
package anvil

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// productExtensionDir 是产品扩展在仓库里的目录，也是嵌入路径的前缀。
const productExtensionDir = "pi-extensions"

//go:embed pi-extensions/*.ts
var embeddedExtensions embed.FS

// ProductExtension 返回嵌入的产品扩展内容（按文件名，例如 "harness-policy.ts"）。
//
// 只接受单层文件名：调用方给的是产品自己的名字，不该（也不能）用它读任意路径。
// 内容为空/不存在都算缺失——空文件的扩展交给 Pi 只会得到一条更难懂的启动错误。
func ProductExtension(name string) ([]byte, bool) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || strings.Contains(name, ":") {
		return nil, false
	}
	body, err := embeddedExtensions.ReadFile(path.Join(productExtensionDir, name))
	if err != nil || len(body) == 0 {
		return nil, false
	}
	return body, true
}

// ProductExtensionNames 返回嵌入的产品扩展文件名（已排序，便于稳定断言与物化）。
func ProductExtensionNames() []string {
	entries, err := fs.ReadDir(embeddedExtensions, productExtensionDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}
