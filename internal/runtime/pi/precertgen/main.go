// Command precertgen 是发行流水线的预认证清单生成器（F08-C Part 2）。
//
// 它只做三件事：把 entries.json 规范化/排序、用发行私钥签名、写出三件套。
// 它**不**做任何认证判断：什么版本、什么哈希可以进清单是发行流程的决定，
// 不是工具的决定。本地跑通 ≠ 认证，别把本机观测到的哈希直接写进 entries。
//
// 用法：
//
//	go run ./internal/runtime/pi/precertgen \
//	  -entries entries.json \
//	  -key release-ed25519.pem \
//	  -out internal/runtime/pi/precert
//
// entries.json 形如：
//
//	{"entries":[{"version":"1.5.0","target":"win-x64","executableSha256":"<64 hex>","notes":"..."}]}
//
// 私钥格式（-key-format）：
//   - pem（默认）：PKCS#8 PEM，即 `openssl genpkey -algorithm ed25519 -out release-ed25519.pem`；
//   - seed：base64 的 32 字节 Ed25519 seed（用于把私钥只放在构建机 KMS/secret 里的场景）。
//
// 私钥永不入库。生成器打印 manifest.json 的 SHA-256，供发行日志留痕。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"my-agent-go/internal/runtime/pi"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "precertgen: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		entriesPath = flag.String("entries", "", "entries.json 路径（{\"entries\":[...]}）")
		keyPath     = flag.String("key", "", "发行私钥路径（PKCS#8 PEM 或 base64 seed）")
		keyFormat   = flag.String("key-format", pi.PreCertifiedKeyFormatPEM, "私钥格式：pem|seed")
		outDir      = flag.String("out", "", "输出目录（写 manifest.json / manifest.json.sig / trust-root.pub）")
		force       = flag.Bool("force", false, "允许覆盖已存在的清单（默认拒绝）")
		generatedAt = flag.String("generated-at", "", "生成时间（RFC3339，默认当前时间；只为复现构建用）")
	)
	flag.Parse()

	if *entriesPath == "" || *keyPath == "" || *outDir == "" {
		return fmt.Errorf("-entries、-key、-out 都是必填")
	}

	entriesRaw, err := os.ReadFile(*entriesPath)
	if err != nil {
		return fmt.Errorf("读 entries: %w", err)
	}
	entries, err := pi.ParsePreCertifiedEntries(entriesRaw)
	if err != nil {
		return err
	}

	at := time.Now().UTC()
	if s := *generatedAt; s != "" {
		parsed, perr := time.Parse(time.RFC3339, s)
		if perr != nil {
			return fmt.Errorf("-generated-at 不是 RFC3339: %w", perr)
		}
		at = parsed
	}

	manifest, normalized, err := pi.BuildPreCertifiedManifest(entries, at)
	if err != nil {
		return err
	}

	keyRaw, err := os.ReadFile(*keyPath)
	if err != nil {
		return fmt.Errorf("读私钥: %w", err)
	}
	priv, err := pi.ParsePreCertifiedPrivateKey(keyRaw, *keyFormat)
	if err != nil {
		return err
	}
	sig, pub, err := pi.SignPreCertifiedManifest(manifest, priv)
	if err != nil {
		return err
	}
	if err := pi.WritePreCertifiedManifest(*outDir, manifest, sig, pub, *force); err != nil {
		return err
	}

	sum := sha256.Sum256(manifest)
	fmt.Printf("precertgen: 写出 %d 条条目到 %s\n", len(normalized.Entries), filepath.Clean(*outDir))
	fmt.Printf("precertgen: manifest.json sha256=%s\n", hex.EncodeToString(sum[:]))
	return nil
}
