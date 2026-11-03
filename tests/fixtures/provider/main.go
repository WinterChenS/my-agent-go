// Command provider 是 Pi 的受控 fixture provider 命令行壳（F02 专用）。
//
// 起一个本地 OpenAI Chat Completions 兼容端点，Pi 通过 models.json 指向它。
// 场景逻辑在 internal/fixtures/openaisse，与 Go 测试共用同一实现。
//
// 用法：
//
//	go run ./tests/fixtures/provider -addr-out addr.txt -record-dir ./rec
//
// 边界：这不是真实云模型，由它产生的协议记录必须标注为 fixture provider，
// 不得冒充真实模型通过验收（v1-gates.md PI03）。
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"my-agent-go/internal/fixtures/openaisse"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "监听地址，默认由系统分配端口")
	addrOut := flag.String("addr-out", "", "把实际监听地址写入该文件，便于脚本读取")
	recordDir := flag.String("record-dir", "", "把收到的请求体记录到该目录")
	flag.Parse()

	if *recordDir != "" {
		if err := os.MkdirAll(*recordDir, 0o755); err != nil {
			log.Fatalf("创建记录目录失败: %v", err)
		}
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("监听失败: %v", err)
	}
	actual := ln.Addr().String()
	if *addrOut != "" {
		if err := os.WriteFile(*addrOut, []byte(actual), 0o644); err != nil {
			log.Fatalf("写入地址文件失败: %v", err)
		}
	}
	fmt.Printf("fixture provider listening on http://%s\n", actual)

	srv := &http.Server{Handler: openaisse.NewHandler(*recordDir), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}
