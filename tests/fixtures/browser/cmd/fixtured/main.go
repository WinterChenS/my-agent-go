// Command fixtured 单独启动 B00 的双 origin fixture。
//
// 自动化测试直接 import browserfixture.New()；这个命令只是给人和外部脚本一个
// 可 go run 的入口（例如在没有浏览器自动化时手工点页面，或在 B01 里把原生视图
// 指向一个固定端口的站点）。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	browserfixture "my-agent-go/tests/fixtures/browser"
)

func main() {
	f, err := browserfixture.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "启动 fixture 失败: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = f.Close() }()

	info := map[string]string{
		"primary":   f.Primary.URL,
		"secondary": f.Secondary.URL,
	}
	raw, err := json.Marshal(info)
	if err != nil {
		fmt.Fprintf(os.Stderr, "编码 origin 失败: %v\n", err)
		os.Exit(1)
	}
	// 一行 JSON，便于证据脚本原样采集。
	fmt.Println(string(raw))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Fprintln(os.Stderr, "收到中断，关闭 fixture")
}
