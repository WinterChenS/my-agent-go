# precert/ — 随二进制嵌入的签名预认证清单

这个目录是 `internal/runtime/pi` 的 `go:embed` 锚点。**本仓库当前只提交这个 README**：
发行包里应当出现的三件套是

| 文件 | 内容 |
|---|---|
| `manifest.json` | 预认证条目清单（版本 → 允许的 Pi 可执行体 SHA-256） |
| `manifest.json.sig` | 对 `manifest.json` **原始字节**的 Ed25519 分离签名（base64） |
| `trust-root.pub` | 发行信任根公钥（base64 的 32 字节 Ed25519 公钥） |

三者缺一不可。缺任一文件时 `LoadEmbeddedPreCertified()` 返回
`ErrNoPreCertifiedManifest`，`VersionPolicy.PreCertifiedHashes` 保持为空，
候选最多只能到 `ready_compatible`——这是 fail closed，不是错误配置。

## 清单在什么时候生成

清单在**构建/发行时**由发行流水线用发行私钥生成并签名，运行期只做验签：

```
go run ./internal/runtime/pi/precertgen \
  -entries entries.json \
  -key release-ed25519.pem \
  -out internal/runtime/pi/precert
```

`precertgen` 会拒绝覆盖已有文件（除非 `-force`），并打印 `manifest.json` 的
SHA-256，供发行日志留痕。生成器本身不做任何认证判断：它只把 `entries.json`
规范化、排序、签名。

## 本地跑通 ≠ 认证

**在一台开发机上跑通 Pi 并不等于它被认证。** 不能因为"我本机这个二进制能跑"
就把它观测到的 SHA-256 手工加进 `entries.json`，也不能用 `-force` 把测试密钥
签出的清单提交进仓库：

- 认证 = 发行密钥签名 + 契约探测通过 + 版本在预认证清单里，三件事同时成立；
- 本机观测到的哈希只是**观测值**，不是认证依据；只有走完发行签名流程的条目才算；
- 私钥永不入库。测试一律在内存里现生成临时密钥对，签出的清单只写临时目录。

`ready_verified` 因此在本仓库当前状态下不可达，这是设计意图，不是缺陷。
