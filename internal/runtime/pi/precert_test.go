package pi

// F08-C Part 2：签名预认证清单（PreCertifiedHashes 的载体）的测试。
//
// 纪律：
//   - 密钥对在测试内部现生成（ed25519.GenerateKey），不读磁盘、不进仓库；
//   - 清单只写进 t.TempDir()，绝不碰真实发行目录 / ~/.pi / .tools/pi；
//   - 不伪造"认证"：嵌入清单今天必须为空，谁想悄悄放行都得先改这里的断言。

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── fixtures ────────────────────────────────────────────────────────────────

func preCertTestKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成临时密钥对失败: %v", err)
	}
	return pub, priv
}

func preCertFixedTime() time.Time {
	return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
}

// preCertSignRaw 对任意字节签名，用来构造"签名有效但内容非法"的负例。
func preCertSignRaw(t *testing.T, raw string, priv ed25519.PrivateKey) ([]byte, []byte) {
	t.Helper()
	sig, _, err := SignPreCertifiedManifest([]byte(raw), priv)
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	return []byte(raw), sig
}

func preCertHashOf(seed string) string {
	// 64 位小写十六进制占位哈希：只要稳定、格式合法即可，不代表任何真实可执行体。
	sum := sha256.Sum256([]byte("precert-test/" + seed))
	return hex.EncodeToString(sum[:])
}

// preCertWriteFixture 用临时密钥生成并写出三件套，返回目录与公钥。
func preCertWriteFixture(t *testing.T, entries []PreCertifiedEntry) (string, ed25519.PublicKey) {
	t.Helper()
	pub, priv := preCertTestKeyPair(t)
	manifest, _, err := BuildPreCertifiedManifest(entries, preCertFixedTime())
	if err != nil {
		t.Fatalf("BuildPreCertifiedManifest: %v", err)
	}
	sig, pubB64, err := SignPreCertifiedManifest(manifest, priv)
	if err != nil {
		t.Fatalf("SignPreCertifiedManifest: %v", err)
	}
	dir := t.TempDir()
	if err := WritePreCertifiedManifest(dir, manifest, sig, pubB64, false); err != nil {
		t.Fatalf("WritePreCertifiedManifest: %v", err)
	}
	return dir, pub
}

// ── 1) 篡改任意字节必须失败 ────────────────────────────────────────────────

func TestPreCertifiedManifestRejectsTamperedBytes(t *testing.T) {
	pub, priv := preCertTestKeyPair(t)
	entries := []PreCertifiedEntry{{
		Version:          "1.5.0",
		ExecutableSHA256: preCertHashOf("exec-1.5.0"),
	}}
	manifest, _, err := BuildPreCertifiedManifest(entries, preCertFixedTime())
	if err != nil {
		t.Fatalf("BuildPreCertifiedManifest: %v", err)
	}
	sig, _, err := SignPreCertifiedManifest(manifest, priv)
	if err != nil {
		t.Fatalf("SignPreCertifiedManifest: %v", err)
	}
	if _, err := VerifyPreCertifiedManifest(manifest, sig, pub); err != nil {
		t.Fatalf("未篡改的清单必须验签通过: %v", err)
	}

	// 翻掉 entries 区域里的一个字节（版本号），签名必须失效。
	idx := bytes.Index(manifest, []byte("1.5.0"))
	if idx < 0 {
		t.Fatalf("清单里找不到版本号: %s", manifest)
	}
	tampered := append([]byte(nil), manifest...)
	tampered[idx] = '2'

	got, err := VerifyPreCertifiedManifest(tampered, sig, pub)
	if err == nil {
		t.Fatalf("篡改后的清单必须验签失败，实际通过了: %+v", got)
	}
	if !strings.Contains(err.Error(), "签名校验失败") {
		t.Fatalf("错误信息要说清签名校验失败，实际 %v", err)
	}
	if len(got.Entries) != 0 || len(got.HashesFor("")) != 0 {
		t.Fatalf("验签失败时不许返回任何条目: %+v", got)
	}

	// 签名本身被改一个字节同样必须失败。
	badSig := append([]byte(nil), sig...)
	badSig[0] ^= 0x01
	if _, err := VerifyPreCertifiedManifest(manifest, badSig, pub); err == nil {
		t.Fatal("被改动的签名必须验签失败")
	}
}

// ── 2) 用错公钥必须失败 ────────────────────────────────────────────────────

func TestPreCertifiedManifestRejectsWrongKey(t *testing.T) {
	pubA, privA := preCertTestKeyPair(t)
	pubB, _ := preCertTestKeyPair(t)

	manifest, _, err := BuildPreCertifiedManifest([]PreCertifiedEntry{{
		Version:          "1.5.0",
		ExecutableSHA256: preCertHashOf("exec-A"),
	}}, preCertFixedTime())
	if err != nil {
		t.Fatalf("BuildPreCertifiedManifest: %v", err)
	}
	sig, _, err := SignPreCertifiedManifest(manifest, privA)
	if err != nil {
		t.Fatalf("SignPreCertifiedManifest: %v", err)
	}
	if _, err := VerifyPreCertifiedManifest(manifest, sig, pubA); err != nil {
		t.Fatalf("自己的密钥必须验签通过: %v", err)
	}
	if _, err := VerifyPreCertifiedManifest(manifest, sig, pubB); err == nil {
		t.Fatal("用另一把公钥验签必须失败（否则任何人都能自签清单）")
	}
	// 公钥长度非法同样失败，不 panic。
	if _, err := VerifyPreCertifiedManifest(manifest, sig, []byte("short")); err == nil {
		t.Fatal("公钥长度非法时必须报错")
	}
}

// ── 3) 严格解析 ────────────────────────────────────────────────────────────

func TestPreCertifiedManifestRejectsMalformedEntries(t *testing.T) {
	_, priv := preCertTestKeyPair(t)
	good := preCertHashOf("good")

	cases := []struct {
		name    string
		entries []PreCertifiedEntry
		raw     string
		wantErr string
	}{
		{
			name:    "bad-hex-length",
			entries: []PreCertifiedEntry{{Version: "1.5.0", ExecutableSHA256: "abcd"}},
			wantErr: "小写十六进制",
		},
		{
			name:    "uppercase-hex",
			entries: []PreCertifiedEntry{{Version: "1.5.0", ExecutableSHA256: strings.ToUpper(good)}},
			wantErr: "小写十六进制",
		},
		{
			name:    "non-hex",
			entries: []PreCertifiedEntry{{Version: "1.5.0", ExecutableSHA256: strings.Repeat("z", 64)}},
			wantErr: "小写十六进制",
		},
		{
			name:    "bad-launcher-hex",
			entries: []PreCertifiedEntry{{Version: "1.5.0", ExecutableSHA256: good, LauncherSHA256: "xyz"}},
			wantErr: "launcherSha256",
		},
		{
			name:    "schema-zero",
			raw:     `{"schema":0,"generatedAt":"2026-01-02T03:04:05Z","entries":[]}`,
			wantErr: "schema 必须是 1",
		},
		{
			name:    "unknown-top-field",
			raw:     `{"schema":1,"generatedAt":"2026-01-02T03:04:05Z","entries":[],"trusted":true}`,
			wantErr: "未知字段",
		},
		{
			name:    "unknown-entry-field",
			raw:     `{"schema":1,"generatedAt":"2026-01-02T03:04:05Z","entries":[{"version":"1.5.0","executableSha256":"` + good + `","signedBy":"me"}]}`,
			wantErr: "未知字段",
		},
		{
			name:    "unknown-target",
			entries: []PreCertifiedEntry{{Version: "1.5.0", Target: "solaris-x64", ExecutableSHA256: good}},
			wantErr: "六目标",
		},
		{
			name:    "version-2x",
			entries: []PreCertifiedEntry{{Version: "2.0.0", ExecutableSHA256: good}},
			wantErr: "只认证 1.x",
		},
		{
			name:    "version-garbage",
			entries: []PreCertifiedEntry{{Version: "1.5.0-beta.1", ExecutableSHA256: good}},
			wantErr: "无法解析",
		},
		{
			name:    "bad-generated-at",
			raw:     `{"schema":1,"generatedAt":"2026-01-02 03:04:05","entries":[]}`,
			wantErr: "RFC3339",
		},
		{
			name:    "cross-version-hash",
			entries: []PreCertifiedEntry{{Version: "1.5.0", ExecutableSHA256: good}, {Version: "1.6.0", ExecutableSHA256: good}},
			wantErr: "同时登记在版本",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.raw != "" {
				raw, sig := preCertSignRaw(t, tc.raw, priv)
				pub := priv.Public().(ed25519.PublicKey)
				got, err := VerifyPreCertifiedManifest(raw, sig, pub)
				if err == nil {
					t.Fatalf("非法清单必须被拒绝，实际通过: %+v", got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("错误信息缺少 %q，实际 %v", tc.wantErr, err)
				}
				if len(got.Entries) != 0 {
					t.Fatalf("被拒清单不许返回条目: %+v", got)
				}
				return
			}
			_, _, err := BuildPreCertifiedManifest(tc.entries, preCertFixedTime())
			if err == nil {
				t.Fatalf("非法条目必须被拒绝: %+v", tc.entries)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误信息缺少 %q，实际 %v", tc.wantErr, err)
			}
		})
	}

	// parseSemver 接受 1.5（补成 1.5.0）：两段版本号不算非法，但必须是 1.x。
	t.Run("two-part-version-accepted", func(t *testing.T) {
		manifest, m, err := BuildPreCertifiedManifest([]PreCertifiedEntry{{
			Version:          "1.5",
			ExecutableSHA256: good,
		}}, preCertFixedTime())
		if err != nil {
			t.Fatalf("两段版本号 1.5 应被 parseSemver 接受，实际报错: %v", err)
		}
		sig, _, err := SignPreCertifiedManifest(manifest, priv)
		if err != nil {
			t.Fatalf("SignPreCertifiedManifest: %v", err)
		}
		if _, err := VerifyPreCertifiedManifest(manifest, sig, priv.Public().(ed25519.PublicKey)); err != nil {
			t.Fatalf("验签: %v", err)
		}
		if !m.HashesFor("")["1.5"][good] {
			t.Fatalf("HashesFor 应保留原始版本键 1.5，实际 %+v", m.HashesFor(""))
		}
	})
}

// ── 4) 按目标过滤 ──────────────────────────────────────────────────────────

func TestPreCertifiedManifestHashesAreTargetScoped(t *testing.T) {
	winHash := preCertHashOf("win-x64")
	allHash := preCertHashOf("all-targets")
	manifest, m, err := BuildPreCertifiedManifest([]PreCertifiedEntry{
		{Version: "1.5.0", Target: "win-x64", ExecutableSHA256: winHash},
		{Version: "1.5.0", ExecutableSHA256: allHash},
	}, preCertFixedTime())
	if err != nil {
		t.Fatalf("BuildPreCertifiedManifest: %v", err)
	}

	linux := m.HashesFor("linux-x64")
	if linux["1.5.0"][winHash] {
		t.Fatalf("win-x64 专用哈希不许出现在 linux-x64 里: %+v", linux)
	}
	if !linux["1.5.0"][allHash] {
		t.Fatalf("Target=\"\" 的条目应对所有目标生效: %+v", linux)
	}
	win := m.HashesFor("win-x64")
	if !win["1.5.0"][winHash] || !win["1.5.0"][allHash] {
		t.Fatalf("win-x64 应同时拿到专用与通用条目: %+v", win)
	}
	// 未知目标只拿到通用条目（fail closed）。
	if unknown := m.HashesFor("plan9-x64"); unknown["1.5.0"][winHash] || !unknown["1.5.0"][allHash] {
		t.Fatalf("未知目标只应拿到 Target=\"\" 的条目: %+v", unknown)
	}

	// 验签后再取哈希，结果必须与直接取一致。
	pub, priv := preCertTestKeyPair(t)
	sig, _, err := SignPreCertifiedManifest(manifest, priv)
	if err != nil {
		t.Fatalf("SignPreCertifiedManifest: %v", err)
	}
	verified, err := VerifyPreCertifiedManifest(manifest, sig, pub)
	if err != nil {
		t.Fatalf("VerifyPreCertifiedManifest: %v", err)
	}
	if !reflectDeepEqual(verified.HashesFor("win-x64"), win) {
		t.Fatalf("验签后的哈希应与直接取一致: %+v vs %+v", verified.HashesFor("win-x64"), win)
	}
}

// ── 5) ready_verified 必须由签名清单驱动 ───────────────────────────────────

func TestDiscoverReadyVerifiedRequiresSignedRegisteredHash(t *testing.T) {
	root := t.TempDir()
	launcher := writePiStub(t, filepath.Join(root, "pi"), nativePiName(), "stub")
	hash := fileHash(t, launcher)

	dir, _ := preCertWriteFixture(t, []PreCertifiedEntry{{
		Version:          "1.5.0",
		ExecutableSHA256: hash,
	}})
	hashes, err := LoadPreCertifiedManifest(dir, HostTarget())
	if err != nil {
		t.Fatalf("LoadPreCertifiedManifest: %v", err)
	}
	if !hashes["1.5.0"][hash] {
		t.Fatalf("装载后的哈希表里应有 %s: %+v", hash, hashes)
	}
	policy := DefaultPolicy
	policy.PreCertifiedHashes = hashes

	probeOK := func(context.Context, PiCandidate) (bool, []string) { return true, nil }

	// 1) 签名清单登记了该可执行体 + 契约探测通过 → ready_verified。
	res, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
		Policy:       policy,
		ProbeFunc:    probeOK,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := res.Candidates[0].Compatibility; got != StateReadyVerified {
		t.Fatalf("签名登记 + 契约探测通过应为 ready_verified，实际 %q（%v）",
			got, res.Candidates[0].Diagnostics)
	}
	if res.Selected == nil || res.Selected.Compatibility != StateReadyVerified {
		t.Fatalf("ready_verified 候选应被选中: %+v", res.Selected)
	}

	// 2) 之后可执行体内容变了（哈希不再登记）→ 只能 ready_compatible，并说明原因。
	if err := os.WriteFile(launcher, []byte("swapped"), 0o755); err != nil {
		t.Fatalf("改写启动器: %v", err)
	}
	swapped, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
		Policy:       policy,
		ProbeFunc:    probeOK,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	cand := swapped.Candidates[0]
	if cand.Compatibility != StateReadyCompatible {
		t.Fatalf("哈希未登记时最多 ready_compatible，实际 %q", cand.Compatibility)
	}
	if diag := strings.Join(cand.Diagnostics, "\n"); !strings.Contains(diag, "unregistered-executable-hash") {
		t.Fatalf("诊断要说明哈希未登记，实际 %v", cand.Diagnostics)
	}

	// 3) 哈希登记着，但契约探测失败 → incompatible（清单不能替代探测）。
	if err := os.WriteFile(launcher, []byte("stub"), 0o755); err != nil {
		t.Fatalf("恢复启动器: %v", err)
	}
	failed, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
		Policy:       policy,
		ProbeFunc: func(context.Context, PiCandidate) (bool, []string) {
			return false, []string{"kind:\"contract-probe-failed\" settled: timeout"}
		},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := failed.Candidates[0].Compatibility; got != StateIncompatible {
		t.Fatalf("契约探测失败时即使哈希已登记也必须 incompatible，实际 %q", got)
	}
}

// ── 6) 嵌入清单今天必须为空（或将来有效） ──────────────────────────────────

func TestEmbeddedPreCertifiedManifestIsAbsentOrValid(t *testing.T) {
	// 无论清单在不在，README 必须在：它是"本地跑通 ≠ 认证"的现场说明。
	readme, err := precertAssets.ReadFile("precert/README.md")
	if err != nil {
		t.Fatalf("嵌入资产里必须有 precert/README.md: %v", err)
	}
	for _, want := range []string{"本地跑通 ≠ 认证", "manifest.json.sig", "trust-root.pub"} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("precert/README.md 缺少 %q", want)
		}
	}

	hashes, lerr := LoadEmbeddedPreCertified()
	statusHashes, statusErr := EmbeddedPreCertifiedStatus()

	if errors.Is(lerr, ErrNoPreCertifiedManifest) {
		if len(hashes) != 0 {
			t.Fatalf("没有清单时不许返回任何哈希: %+v", hashes)
		}
		if !errors.Is(statusErr, ErrNoPreCertifiedManifest) {
			t.Fatalf("装载错误必须被保留下来供诊断，实际 %v", statusErr)
		}
		if len(statusHashes) != 0 {
			t.Fatalf("状态里的哈希表必须为空: %+v", statusHashes)
		}
		if len(DefaultPolicy.PreCertifiedHashes) != 0 {
			t.Fatalf("DefaultPolicy.PreCertifiedHashes 今天必须为空（未下发签名清单）: %+v",
				DefaultPolicy.PreCertifiedHashes)
		}
		return
	}
	if lerr != nil {
		t.Fatalf("清单存在却读不出来是错误，不是空表: %v", lerr)
	}

	// 将来真下发了清单：同一份断言必须把它验到底，而不是跳过。
	manifest, rerr := precertAssets.ReadFile("precert/" + PreCertifiedManifestFile)
	if rerr != nil {
		t.Fatalf("清单存在时 manifest.json 必须可读: %v", rerr)
	}
	sig, rerr := precertAssets.ReadFile("precert/" + PreCertifiedSignatureFile)
	if rerr != nil {
		t.Fatalf("清单存在时签名必须可读: %v", rerr)
	}
	pubB64, rerr := precertAssets.ReadFile("precert/" + PreCertifiedTrustRootFile)
	if rerr != nil {
		t.Fatalf("清单存在时信任根必须可读: %v", rerr)
	}
	pub, derr := decodeTrustRoot(pubB64)
	if derr != nil {
		t.Fatalf("信任根不是合法 Ed25519 公钥: %v", derr)
	}
	m, verr := VerifyPreCertifiedManifest(manifest, sig, pub)
	if verr != nil {
		t.Fatalf("嵌入清单验签失败: %v", verr)
	}
	if !reflectDeepEqual(m.HashesFor(HostTarget()), hashes) {
		t.Fatalf("嵌入清单哈希与 DefaultPolicy 不一致: %+v vs %+v", m.HashesFor(HostTarget()), hashes)
	}
}

// ── 7) 生成器往返 + 字节稳定 ───────────────────────────────────────────────

func TestPreCertifiedGeneratorRoundTrip(t *testing.T) {
	pub, priv := preCertTestKeyPair(t)
	entries := []PreCertifiedEntry{
		{Version: "1.5.0", Target: "win-x64", ExecutableSHA256: preCertHashOf("win"), Notes: "windows build"},
		{Version: "1.5.0", ExecutableSHA256: preCertHashOf("all")},
		{Version: "1.6.0", Target: "linux-arm64", ExecutableSHA256: preCertHashOf("linux-arm"), LauncherSHA256: preCertHashOf("launcher")},
		// 完全重复的条目要被去重，而不是让清单变长。
		{Version: "1.5.0", ExecutableSHA256: preCertHashOf("all")},
	}

	manifest, m, err := BuildPreCertifiedManifest(entries, preCertFixedTime())
	if err != nil {
		t.Fatalf("BuildPreCertifiedManifest: %v", err)
	}
	if len(m.Entries) != 3 {
		t.Fatalf("完全重复的条目应被去重到 3 条，实际 %d: %+v", len(m.Entries), m.Entries)
	}
	again, _, err := BuildPreCertifiedManifest(entries, preCertFixedTime())
	if err != nil {
		t.Fatalf("BuildPreCertifiedManifest(again): %v", err)
	}
	if !bytes.Equal(manifest, again) {
		t.Fatalf("同样的输入必须产出逐字节相同的清单:\n%s\n---\n%s", manifest, again)
	}

	// entries 输入 JSON（生成器格式）→ 解析 → 构建，字节必须与直接构建一致。
	rawInput, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatalf("序列化输入: %v", err)
	}
	parsed, err := ParsePreCertifiedEntries(rawInput)
	if err != nil {
		t.Fatalf("ParsePreCertifiedEntries: %v", err)
	}
	fromInput, _, err := BuildPreCertifiedManifest(parsed, preCertFixedTime())
	if err != nil {
		t.Fatalf("BuildPreCertifiedManifest(from input): %v", err)
	}
	if !bytes.Equal(manifest, fromInput) {
		t.Fatalf("entries.json 往返后清单字节必须一致:\n%s\n---\n%s", manifest, fromInput)
	}

	// PKCS#8 PEM 私钥 → 解析 → 签名 → 验签。
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	parsedPriv, err := ParsePreCertifiedPrivateKey(pemBytes, PreCertifiedKeyFormatPEM)
	if err != nil {
		t.Fatalf("ParsePreCertifiedPrivateKey(pem): %v", err)
	}
	seedB64 := []byte(base64.StdEncoding.EncodeToString(priv.Seed()))
	seedPriv, err := ParsePreCertifiedPrivateKey(seedB64, PreCertifiedKeyFormatSeed)
	if err != nil {
		t.Fatalf("ParsePreCertifiedPrivateKey(seed): %v", err)
	}
	if !bytes.Equal(seedPriv, parsedPriv) {
		t.Fatal("两种私钥格式解析出的私钥必须一致")
	}

	sig, pubB64, err := SignPreCertifiedManifest(manifest, parsedPriv)
	if err != nil {
		t.Fatalf("SignPreCertifiedManifest: %v", err)
	}
	decodedPub, err := decodeTrustRoot(pubB64)
	if err != nil {
		t.Fatalf("decodeTrustRoot: %v", err)
	}
	if !bytes.Equal(decodedPub, pub) {
		t.Fatal("写出的公钥必须等于签名密钥对应的公钥")
	}
	if _, err := VerifyPreCertifiedManifest(manifest, sig, decodedPub); err != nil {
		t.Fatalf("生成器产物必须能验签: %v", err)
	}

	dir := t.TempDir()
	if err := WritePreCertifiedManifest(dir, manifest, sig, pubB64, false); err != nil {
		t.Fatalf("WritePreCertifiedManifest: %v", err)
	}
	for _, name := range []string{PreCertifiedManifestFile, PreCertifiedSignatureFile, PreCertifiedTrustRootFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("生成器必须写出 %s: %v", name, err)
		}
	}
	hashes, err := LoadPreCertifiedManifest(dir, "win-x64")
	if err != nil {
		t.Fatalf("LoadPreCertifiedManifest: %v", err)
	}
	if !hashes["1.5.0"][preCertHashOf("win")] || !hashes["1.5.0"][preCertHashOf("all")] {
		t.Fatalf("win-x64 应拿到专用与通用条目: %+v", hashes)
	}
	if hashes["1.6.0"][preCertHashOf("linux-arm")] {
		t.Fatalf("linux-arm64 条目不许出现在 win-x64: %+v", hashes)
	}

	// 不覆盖：已存在且没有 -force 时必须拒绝，避免悄悄替换发行清单。
	if err := WritePreCertifiedManifest(dir, manifest, sig, pubB64, false); err == nil {
		t.Fatal("目标目录已有清单时必须拒绝覆盖（除非 -force）")
	}
	if err := WritePreCertifiedManifest(dir, manifest, sig, pubB64, true); err != nil {
		t.Fatalf("force 覆盖应成功: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, PreCertifiedManifestFile))
	if err != nil {
		t.Fatalf("读回 manifest.json: %v", err)
	}
	if !bytes.Equal(after, manifest) {
		t.Fatal("覆盖后清单字节必须与源字节一致")
	}

	// 缺任一文件 → ErrNoPreCertifiedManifest（fail closed，不是空表放行）。
	if err := os.Remove(filepath.Join(dir, PreCertifiedSignatureFile)); err != nil {
		t.Fatalf("删除签名: %v", err)
	}
	if _, err := LoadPreCertifiedManifest(dir, "win-x64"); !errors.Is(err, ErrNoPreCertifiedManifest) {
		t.Fatalf("缺签名文件时必须返回 ErrNoPreCertifiedManifest，实际 %v", err)
	}

	// 清单被改动但签名没跟着换 → 错误，且不能退化成"空表放行"。
	tamperDir, _ := preCertWriteFixture(t, []PreCertifiedEntry{{
		Version:          "1.5.0",
		ExecutableSHA256: preCertHashOf("x"),
	}})
	path := filepath.Join(tamperDir, PreCertifiedManifestFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 manifest: %v", err)
	}
	raw[bytes.Index(raw, []byte("1.5.0"))] = '2'
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("写 manifest: %v", err)
	}
	if _, err := LoadPreCertifiedManifest(tamperDir, "win-x64"); err == nil ||
		errors.Is(err, ErrNoPreCertifiedManifest) {
		t.Fatalf("存在但被篡改的清单必须报错（不是 ErrNoPreCertifiedManifest 空放行），实际 %v", err)
	}
}

// ── 小工具 ─────────────────────────────────────────────────────────────────

func reflectDeepEqual(a, b map[string]map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || len(av) != len(bv) {
			return false
		}
		for h := range av {
			if !bv[h] {
				return false
			}
		}
	}
	return true
}
