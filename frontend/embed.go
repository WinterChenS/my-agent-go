// Package frontend 只负责把构建产物嵌入可执行文件。
//
// 为什么放在 frontend 目录：Go 的 //go:embed 不能跨出包目录，而产物天然属于
// 前端；在这个包里做一层薄封装，主程序就不必复制一份 dist。
package frontend

import "embed"

// Dist 是 Vite 的构建输出（index.html + assets/）。
//
//go:embed all:dist
var Dist embed.FS
