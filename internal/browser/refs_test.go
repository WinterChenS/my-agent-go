package browser

// B04 的引用作用域规则：ref 只在产生它的那份快照里有效，永远不会被重新编号。
//
// 这些用例不需要 WebView2：它们锁的是"不许猜"这条规则本身。真机上的
// SPA/同 URL 导航场景在 tests/native 里跑，并核对服务端 counter 保持 0。

import (
	"testing"

	"my-agent-go/internal/contracts"
)

func TestRefRegistryResolvesOnlyInsideOneSnapshot(t *testing.T) {
	reg := NewRefRegistry()
	table := reg.Begin("snap-1", "nav-1", 0, nil)
	table.Bind(Element{Ref: "e1", Tag: "button", Name: "提交"}, "nonce-e1")
	table.Bind(Element{Ref: "e2", Tag: "input", InputType: "text", Name: "姓名"}, "nonce-e2")

	got, identity, err := table.Resolve("e2")
	if err != nil {
		t.Fatalf("Resolve(e2): %v", err)
	}
	if got.Tag != "input" || identity != "nonce-e2" {
		t.Fatalf("Resolve(e2) 返回 %+v identity=%q", got, identity)
	}

	// 不在该快照里的 ref 必须结构化拒绝，而不是"返回最接近的一个"。
	if _, _, err := table.Resolve("e3"); err == nil {
		t.Fatal("未知 ref 必须被拒绝")
	} else if code := appErrCode(t, err); code != contracts.CodeStaleSnapshot {
		t.Fatalf("未知 ref 的错误码 %q，期望 %s", code, contracts.CodeStaleSnapshot)
	}
	if _, _, err := table.Resolve(""); err == nil {
		t.Fatal("空 ref 必须被拒绝")
	}
}

func TestRefRegistryRejectsDifferentDocument(t *testing.T) {
	reg := NewRefRegistry()
	reg.Begin("snap-1", "nav-1", 0, nil)

	if _, err := reg.Lookup("snap-1", "nav-1"); err != nil {
		t.Fatalf("同一文档应能取回快照: %v", err)
	}
	// 同 URL 重新导航后文档 id 变了：旧快照必须失效。
	_, err := reg.Lookup("snap-1", "nav-2")
	if err == nil {
		t.Fatal("导航后的旧快照必须被拒绝")
	}
	if code := appErrCode(t, err); code != contracts.CodeStaleSnapshot {
		t.Fatalf("错误码 %q，期望 %s", code, contracts.CodeStaleSnapshot)
	}
	if _, err := reg.Lookup("snap-404", "nav-1"); err == nil {
		t.Fatal("不存在的快照必须被拒绝")
	}
}

func TestRefRegistryNeverRenumbersAcrossSnapshots(t *testing.T) {
	reg := NewRefRegistry()
	first := reg.Begin("snap-1", "nav-1", 0, nil)
	first.Bind(Element{Ref: "e1", Tag: "input", InputType: "text", Name: "姓名"}, "nonce-u1")

	// 第二份快照里同名的 ref 指向**另一个**元素（SPA 换节点后重建的 DOM）。
	second := reg.Begin("snap-2", "nav-1", 0, nil)
	second.Bind(Element{Ref: "e1", Tag: "button", Name: "替换后的按钮"}, "nonce-u9")

	// 旧快照里的 e1 仍然是那个 input，不会被第二份快照"重新编号"成按钮。
	el, identity, err := first.Resolve("e1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if el.Tag != "input" || identity != "nonce-u1" {
		t.Fatalf("旧快照的 e1 被改写成了 %+v identity=%q", el, identity)
	}

	// backend 现在按旧 ref 找到的元素身份是 nonce-u9 → 必须拒绝，而不是接着点。
	if err := first.CheckIdentity("e1", "nonce-u9"); err == nil {
		t.Fatal("元素身份不一致必须被拒绝")
	} else if code := appErrCode(t, err); code != contracts.CodeStaleSnapshot {
		t.Fatalf("身份不一致的错误码 %q，期望 %s", code, contracts.CodeStaleSnapshot)
	}
	if err := first.CheckIdentity("e1", "nonce-u1"); err != nil {
		t.Fatalf("身份一致时应通过: %v", err)
	}
}

func TestRefRegistryEvictsOldestWithoutAliasing(t *testing.T) {
	reg := NewRefRegistry()
	for i := 0; i < MaxRetainedSnapshots+3; i++ {
		tbl := reg.Begin(snapshotIDForTest(i), "nav-1", 0, nil)
		tbl.Bind(Element{Ref: "e1", Tag: "button"}, "uid-"+snapshotIDForTest(i))
	}
	if got := reg.Len(); got != MaxRetainedSnapshots {
		t.Fatalf("保留的快照数 %d，期望 %d", got, MaxRetainedSnapshots)
	}
	// 最旧的三份被淘汰：必须返回 STALE_SNAPSHOT，而不是悄悄换成最新的那份。
	for i := 0; i < 3; i++ {
		if _, err := reg.Lookup(snapshotIDForTest(i), "nav-1"); err == nil {
			t.Fatalf("被淘汰的快照 %s 仍可查找", snapshotIDForTest(i))
		} else if code := appErrCode(t, err); code != contracts.CodeStaleSnapshot {
			t.Fatalf("淘汰快照的错误码 %q", code)
		}
	}
	if _, err := reg.Lookup(snapshotIDForTest(MaxRetainedSnapshots+2), "nav-1"); err != nil {
		t.Fatalf("最新快照应可用: %v", err)
	}

	reg.Drop(snapshotIDForTest(MaxRetainedSnapshots + 2))
	if _, err := reg.Lookup(snapshotIDForTest(MaxRetainedSnapshots+2), "nav-1"); err == nil {
		t.Fatal("Drop 之后仍能查找")
	}
	reg.Reset()
	if reg.Len() != 0 {
		t.Fatalf("Reset 之后还剩 %d 份快照", reg.Len())
	}
}

// TestRefTableIsSecretOnlyForPasswordFields 锁住"哪些字段的核验不能回显值"。
//
// 核验密码字段的 field_value 时，比较必须在页面侧完成；一旦 IsSecret 判错，
// 密码值就会顺着 Verification.Detail 进日志（BR09 的泄漏路径之一）。
func TestRefTableIsSecretOnlyForPasswordFields(t *testing.T) {
	reg := NewRefRegistry()
	table := reg.Begin("snap-1", "nav-1", 0, nil)
	table.Bind(Element{Ref: "e1", Tag: "input", InputType: "password"}, "uid-1")
	table.Bind(Element{Ref: "e2", Tag: "input", InputType: "text"}, "uid-2")
	table.Bind(Element{Ref: "e3", Tag: "textarea"}, "uid-3")

	if !table.IsSecret("e1") {
		t.Fatal("password 字段必须判为敏感")
	}
	if table.IsSecret("e2") || table.IsSecret("e3") {
		t.Fatal("非密码字段不应判为敏感（否则正常核验也会被降级成布尔比较）")
	}
	if table.IsSecret("e9") {
		t.Fatal("不存在的 ref 不应判为敏感")
	}
}

func snapshotIDForTest(i int) string { return "snap-" + itoa(i) }
