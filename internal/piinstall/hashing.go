package piinstall

// 本文件放安装流程里反复用到的哈希工具。
// 生产代码（激活/回滚/下载复核）与测试共用同一实现：证据里的哈希就是产品算的哈希。

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// hashFileContent 返回文件内容的 sha256（小写十六进制）。
func hashFileContent(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashString 返回字符串的 sha256（小写十六进制）。
func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// fmtErrs 把 probe 的失败诊断拼成一行（证据里要能读，不要 JSON 嵌套）。
func fmtErrs(lines []string) string {
	if len(lines) == 0 {
		return "（probe 没有给出诊断）"
	}
	out := lines[0]
	for _, l := range lines[1:] {
		out += " | " + l
	}
	return out
}
