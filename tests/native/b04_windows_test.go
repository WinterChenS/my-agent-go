//go:build windows

package nativetest

// B04：真实 Windows WebView2 上的 observe / refs / actions（BR07–BR10）。
//
// 与 B01a/B01b 的分工：
//   - B01 证明"右侧有真实原生视图、坐标与缩放对得上"；
//   - 这里证明**工具语义**：快照里的 ref 只在它那一帧里有效、动作真的落到服务端、
//     密码值不会被观察到、看不见的表面明确 Unsupported、截图完整且有界。
//
// 所有"动作有没有发生"的判断都走 fixture 的服务端 counter（唯一 oracle），
// 不读页面文案、不读内存变量。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/browser/native"
	"my-agent-go/internal/contracts"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

// eventSink 收集宿主事件（B04 用它验证"日志里没有密码值"）。
type eventSink struct {
	mu     sync.Mutex
	events []native.BrowserEvent
}

func (s *eventSink) add(ev native.BrowserEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

// JSON 返回事件日志的 JSON 表示（只用于扫描泄漏，不用于断言业务结果）。
func (s *eventSink) JSON(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(s.events)
	if err != nil {
		t.Fatalf("序列化事件日志失败: %v", err)
	}
	return string(raw)
}

// newB04Harness 建带事件日志的宿主（newHarness 的选项变体）。
func newB04Harness(t *testing.T) (*harness, *eventSink) {
	t.Helper()
	sink := &eventSink{}
	h := newHarnessWith(t, func(o *native.HostOptions) { o.OnEvent = sink.add })
	return h, sink
}

// findElement 按谓词找快照里的元素；找不到直接失败（比空 ref 更有诊断价值）。
func findElement(t *testing.T, snap browser.Snapshot, what string, pred func(browser.Element) bool) browser.Element {
	t.Helper()
	for _, el := range snap.Elements {
		if pred(el) {
			return el
		}
	}
	names := make([]string, 0, 16)
	for i, el := range snap.Elements {
		if i >= 16 {
			break
		}
		names = append(names, el.Tag+":"+el.Name)
	}
	t.Fatalf("快照里找不到%s（前 16 个元素: %s）", what, strings.Join(names, " | "))
	return browser.Element{}
}

// findByName 按标签与名字片段找元素。
func findByName(t *testing.T, snap browser.Snapshot, tag, namePart string) browser.Element {
	t.Helper()
	return findElement(t, snap, fmt.Sprintf("%s 含 %q", tag, namePart), func(el browser.Element) bool {
		return el.Tag == tag && strings.Contains(el.Name, namePart)
	})
}

// findByInput 按标签与 input type 找表单控件。
func findByInput(t *testing.T, snap browser.Snapshot, tag, inputType string) browser.Element {
	t.Helper()
	return findElement(t, snap, fmt.Sprintf("%s[type=%s]", tag, inputType), func(el browser.Element) bool {
		return el.Tag == tag && strings.EqualFold(el.InputType, inputType)
	})
}

// lastForm 用 HTTP 去 fixture 读服务端收到的最后一次表单载荷。
//
// 断言一律读这里：页面把某个变量回显一遍不能证明"服务端看到的值是对的"。
func lastForm(t *testing.T, originURL string) map[string]any {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get(originURL + "/__form/last")
	if err != nil {
		t.Fatalf("HTTP 读取服务端表单载荷失败: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("服务端还没有收到表单（状态 %d）: %s", res.StatusCode, truncate(string(body), 200))
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("解析服务端表单载荷失败: %v", err)
	}
	return out
}

// ---------- BR07 / BR09：稳定引用与限额 ----------

// TestObserveReturnsStableRefsAndHonoursLimits 覆盖两件事：
//
//  1. 同一份 DOM 观察两次，ref 一致且都解析到**同一个元素**（"稳定引用"）；
//  2. 巨型页面撞上限时如实裁剪并标注 Truncated，JSON 仍然合法且不超 256KiB。
//
// 不覆盖：跨导航的引用失效（见 TestStaleRefAfterNavigationIsRejected）。
func TestObserveReturnsStableRefsAndHonoursLimits(t *testing.T) {
	h, _ := newB04Harness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	navigateTo(t, ctx, h, "/form", "普通表单")

	first, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("第一次 Observe: %v", err)
	}
	second, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("第二次 Observe: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("两次观察的快照 id 相同（%s）：快照必须是不可变的一帧", first.ID)
	}
	if first.NavigationID != second.NavigationID {
		t.Fatalf("同一文档的 navigationId 变了: %s != %s", first.NavigationID, second.NavigationID)
	}
	if first.Truncated || second.Truncated {
		t.Fatalf("普通表单页不该被判为裁剪: first=%t second=%t", first.Truncated, second.Truncated)
	}
	if len(first.Elements) == 0 || len(first.Elements) != len(second.Elements) {
		t.Fatalf("两次观察的元素数不一致: %d vs %d", len(first.Elements), len(second.Elements))
	}
	// 同一份 DOM：ref 与元素描述必须逐条目一致。
	for i := range first.Elements {
		a, b := first.Elements[i], second.Elements[i]
		if a.Ref != b.Ref {
			t.Fatalf("第 %d 个元素的 ref 变了: %q != %q（ref 必须稳定到元素本体）", i, a.Ref, b.Ref)
		}
		if a.Tag != b.Tag || a.Name != b.Name || a.InputType != b.InputType {
			t.Fatalf("第 %d 个元素的描述变了: %+v vs %+v", i, a, b)
		}
	}

	// 两份快照里的同名 ref 都必须真的落到那个 input 上（用 field_value 核验）。
	nameField := findByInput(t, second, "input", "text")
	for _, snap := range []browser.Snapshot{first, second} {
		label := "名" + snap.ID
		res, err := h.host.Act(ctx, h.tab, browser.Action{
			Kind:       browser.ActionFill,
			SnapshotID: snap.ID,
			Ref:        nameField.Ref,
			Text:       label,
			ExpectedCondition: browser.ExpectedCondition{
				Kind: browser.ConditionFieldValue, Ref: nameField.Ref, Value: label, TimeoutMS: 5000,
			},
		})
		if err != nil {
			t.Fatalf("用快照 %s 的 ref 填写: %v", snap.ID, err)
		}
		if !res.Verified {
			t.Fatalf("快照 %s 的 ref 没有落到同一个 input 上：delivered=%t status=%s detail=%s",
				snap.ID, res.Delivered, res.Verification.Status, res.Verification.Detail)
		}
	}
	observeB04Snapshot(t, "br07-form-stable-refs", second)

	// ---- 限额 1：元素上限 ----
	navigateTo(t, ctx, h, "/huge?nodes=400&labelLen=6&textChars=200", "巨型页面")
	elems, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(元素超限页): %v", err)
	}
	if got := len(elems.Elements); got != browser.MaxSnapshotElements {
		t.Fatalf("元素上限应为 %d，实际返回 %d 个", browser.MaxSnapshotElements, got)
	}
	if !elems.Truncated {
		t.Fatal("元素被裁剪时必须标注 Truncated")
	}
	if err := elems.CheckLimits(); err != nil {
		t.Fatalf("裁剪后的快照越界: %v", err)
	}

	// ---- 限额 2：文本上限（按 Unicode 字符计）----
	navigateTo(t, ctx, h, "/huge?nodes=10&labelLen=6&textChars=50000", "巨型页面")
	textSnap, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(文本超限页): %v", err)
	}
	if got := utf8.RuneCountInString(textSnap.Text); got != browser.MaxSnapshotTextChars {
		t.Fatalf("文本上限应为 %d 个字符，实际 %d", browser.MaxSnapshotTextChars, got)
	}
	if !textSnap.Truncated {
		t.Fatal("文本被裁剪时必须标注 Truncated")
	}

	// ---- 限额 3：JSON 上限 ----
	navigateTo(t, ctx, h, "/huge?nodes=400&labelLen=120&textChars=50000", "巨型页面")
	jsonSnap, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(JSON 超限页): %v", err)
	}
	raw, err := json.Marshal(jsonSnap)
	if err != nil {
		t.Fatalf("序列化快照: %v", err)
	}
	if len(raw) > browser.MaxSnapshotJSONBytes {
		t.Fatalf("快照 JSON %d 字节，超过上限 %d", len(raw), browser.MaxSnapshotJSONBytes)
	}
	if !json.Valid(raw) {
		t.Fatal("裁剪后的快照必须是完整 JSON")
	}
	if len(jsonSnap.Elements) == 0 {
		t.Fatal("裁剪不能把元素全部丢光")
	}
	if !jsonSnap.Truncated {
		t.Fatal("撞上任何一条限额都必须标注 Truncated")
	}
	if err := jsonSnap.CheckLimits(); err != nil {
		t.Fatalf("裁剪后的快照越界: %v", err)
	}
	observeB04Limits(t, jsonSnap, len(raw))
	finalizeB04LimitsObservation(t)
}

// ---------- BR08：陈旧引用 ----------

// TestStaleRefAfterNavigationIsRejected 是 BR08 的核心：
//   - 同 URL 重新导航后，旧快照的 ref 必须 STALE_SNAPSHOT；
//   - SPA 断开旧节点后，旧 ref 必须被拒绝，**不能**被重新编号到替换后的新节点上；
//   - 两次拒绝期间服务端 counter 必须保持 0。
func TestStaleRefAfterNavigationIsRejected(t *testing.T) {
	h, _ := newB04Harness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	primary := h.fixture.Primary

	// ---- (a) 同 URL 导航 ----
	navigateTo(t, ctx, h, "/same-url", "同 URL 导航")
	snap, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(/same-url): %v", err)
	}
	target := findByName(t, snap, "button", "点我计数")
	reload := findByName(t, snap, "a", "再导航一次")
	before := fixtureCounter(t, primary.URL, browserfixture.CounterSameURL)

	res, err := h.host.Act(ctx, h.tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: snap.ID, Ref: reload.Ref,
		ExpectedCondition: browser.ExpectedCondition{
			Kind: browser.ConditionURLEquals, URL: primary.URL + "/same-url", TimeoutMS: 10000,
		},
	})
	if err != nil {
		t.Fatalf("点同 URL 导航链接: %v", err)
	}
	if !res.Verified {
		t.Fatalf("同 URL 导航没有核验通过: status=%s detail=%s", res.Verification.Status, res.Verification.Detail)
	}

	// 旧快照属于上一份文档：ref 必须被拒绝，且不发出任何输入。
	_, err = h.host.Act(ctx, h.tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: snap.ID, Ref: target.Ref,
	})
	if err == nil {
		t.Fatal("导航后使用旧 ref 必须被拒绝")
	}
	if code := appErrCode(t, err); code != contracts.CodeStaleSnapshot {
		t.Fatalf("同 URL 导航后旧 ref 的错误码 %q，期望 %s", code, contracts.CodeStaleSnapshot)
	}
	after := fixtureCounter(t, primary.URL, browserfixture.CounterSameURL)
	if after != before {
		t.Fatalf("旧 ref 被拒绝时服务端 counter 仍从 %d 变成 %d：说明输入被发出去了", before, after)
	}
	observeB04StaleRejection(t, obsFacts{
		"test":          "same-url-navigation",
		"errorCode":     contracts.CodeStaleSnapshot,
		"counterBefore": before,
		"counterAfter":  after,
	})

	// ---- (b) SPA 断开节点 ----
	navigateTo(t, ctx, h, "/spa", "SPA 节点替换")
	snapA, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(/spa): %v", err)
	}
	oldTarget := findByName(t, snapA, "button", "点我计数")
	replace := findByName(t, snapA, "button", "替换节点")
	if _, err := h.host.Act(ctx, h.tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: snapA.ID, Ref: replace.Ref,
	}); err != nil {
		t.Fatalf("点替换节点: %v", err)
	}

	// 重新观察：替换后的节点是一个**新元素**，会拿到新的 ref。
	snapB, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(替换后): %v", err)
	}
	if snapA.NavigationID != snapB.NavigationID {
		t.Fatalf("SPA 替换不该换文档: %s != %s", snapA.NavigationID, snapB.NavigationID)
	}
	newTarget := findByName(t, snapB, "button", "替换后的按钮")
	if newTarget.Ref == oldTarget.Ref {
		t.Fatalf("替换后的新节点复用了旧 ref %q：这正是「重新编号后误点」的入口", newTarget.Ref)
	}

	beforeTarget := fixtureCounter(t, primary.URL, browserfixture.CounterSPATarget)
	beforeReplaced := fixtureCounter(t, primary.URL, browserfixture.CounterSPAReplaced)
	if _, err := h.host.Act(ctx, h.tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: snapA.ID, Ref: oldTarget.Ref,
	}); err == nil {
		t.Fatal("SPA 断开节点后旧 ref 必须被拒绝")
	} else if code := appErrCode(t, err); code != contracts.CodeStaleSnapshot {
		t.Fatalf("SPA 旧 ref 的错误码 %q，期望 %s", code, contracts.CodeStaleSnapshot)
	}
	if got := fixtureCounter(t, primary.URL, browserfixture.CounterSPATarget); got != beforeTarget {
		t.Fatalf("旧 ref 被拒绝时 spa-target 从 %d 变成 %d", beforeTarget, got)
	}
	if got := fixtureCounter(t, primary.URL, browserfixture.CounterSPAReplaced); got != beforeReplaced {
		t.Fatalf("旧 ref 被拒绝时 spa-replaced 从 %d 变成 %d：输入被重新编号到新节点上了", beforeReplaced, got)
	}
	observeB04StaleRejection(t, obsFacts{
		"test":                 "spa-node-teardown",
		"errorCode":            contracts.CodeStaleSnapshot,
		"counterTargetBefore":  beforeTarget,
		"counterTargetAfter":   fixtureCounter(t, primary.URL, browserfixture.CounterSPATarget),
		"counterReplaceBefore": beforeReplaced,
		"counterReplaceAfter":  fixtureCounter(t, primary.URL, browserfixture.CounterSPAReplaced),
	})

	// 新 ref 必须指向新元素并且真的能用：证明"拒绝旧的"不是因为坐标全废了。
	res, err = h.host.Act(ctx, h.tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: snapB.ID, Ref: newTarget.Ref,
	})
	if err != nil {
		t.Fatalf("用新快照的 ref 点替换后的按钮: %v", err)
	}
	if !res.Delivered {
		t.Fatal("新 ref 的点击没有发出")
	}
	if got := fixtureCounter(t, primary.URL, browserfixture.CounterSPAReplaced); got != beforeReplaced+1 {
		t.Fatalf("新 ref 的点击应让 spa-replaced 变成 %d，实际 %d", beforeReplaced+1, got)
	}
	if got := fixtureCounter(t, primary.URL, browserfixture.CounterSPATarget); got != beforeTarget {
		t.Fatalf("spa-target 被误增到 %d", got)
	}
	observeB04Snapshot(t, "br08-spa-after-replace", snapB)
	finalizeB04StaleObservation(t, h)
}

// ---------- BR07：fill / click / select / checkbox / textarea ----------

// TestFillAndClickReachTheFixtureServer 覆盖标准表单的完整工具语义：
// 文本 input、select、textarea、checkbox 都能填，按钮能点，服务端看到的值对，
// 提交 counter 恰好 +1。
func TestFillAndClickReachTheFixtureServer(t *testing.T) {
	h, _ := newB04Harness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	primary := h.fixture.Primary
	navigateTo(t, ctx, h, "/form", "普通表单")
	if got := fixtureCounter(t, primary.URL, browserfixture.CounterFormSubmit); got != 0 {
		t.Fatalf("提交前 counter=%d，期望 0", got)
	}
	snap, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(/form): %v", err)
	}
	observeB04Snapshot(t, "br07-form", snap)

	const (
		name  = "张三"
		note  = "备注：中文与 emoji 🙂"
		plan  = "pro"
		state = "true"
	)
	fills := []struct {
		label string
		ref   string
		text  string
	}{
		{"姓名 input", findByInput(t, snap, "input", "text").Ref, name},
		{"套餐 select", findByInput(t, snap, "select", "").Ref, plan},
		{"备注 textarea", findByInput(t, snap, "textarea", "").Ref, note},
		{"确认 checkbox", findByInput(t, snap, "input", "checkbox").Ref, state},
	}
	for _, f := range fills {
		res, err := h.host.Act(ctx, h.tab, browser.Action{
			Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: f.ref, Text: f.text,
		})
		if err != nil {
			t.Fatalf("fill(%s): %v", f.label, err)
		}
		if !res.Delivered {
			t.Fatalf("fill(%s) 没有发出输入", f.label)
		}
		// 没有后置条件时只能是 unknown：不能因为"没报错"就写成 verified。
		if res.Verified || res.Verification.Status != browser.VerificationUnknown {
			t.Fatalf("fill(%s) 无后置条件却报 verified=%t status=%s", f.label, res.Verified, res.Verification.Status)
		}
		observeB04Action(t, obsFacts{
			"test": "fill", "control": f.label, "delivered": res.Delivered,
			"verified": res.Verified, "status": string(res.Verification.Status),
			"counterBefore": fixtureCounter(t, primary.URL, browserfixture.CounterFormSubmit),
			"counterAfter":  fixtureCounter(t, primary.URL, browserfixture.CounterFormSubmit),
		})
	}

	submit := findByName(t, snap, "button", "提交")
	// #submit-count 是"服务端提交次数"的页面回显（初始为 0）；用 element_text 核验它，
	// 这样"点到了按钮"和"页面真的收到了服务端结果"是两件分开的事。
	submitCount := findElement(t, snap, "#submit-count（初始值 0）", func(el browser.Element) bool {
		return el.Tag == "strong" && strings.TrimSpace(el.Name) == "0"
	})
	res, err := h.host.Act(ctx, h.tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: snap.ID, Ref: submit.Ref,
		ExpectedCondition: browser.ExpectedCondition{
			Kind: browser.ConditionElementText, Ref: submitCount.Ref, Value: "1", TimeoutMS: 10000,
		},
	})
	if err != nil {
		t.Fatalf("点提交: %v", err)
	}
	if !res.Delivered || !res.Verified {
		t.Fatalf("提交没有核验通过: delivered=%t verified=%t status=%s detail=%s",
			res.Delivered, res.Verified, res.Verification.Status, res.Verification.Detail)
	}
	after := fixtureCounter(t, primary.URL, browserfixture.CounterFormSubmit)
	if after != 1 {
		t.Fatalf("提交一次后服务端 counter=%d，期望恰好 1", after)
	}
	payload := lastForm(t, primary.URL)
	checks := map[string]any{"name": name, "plan": plan, "note": note, "confirm": true}
	for k, want := range checks {
		if got := payload[k]; got != want {
			t.Fatalf("服务端收到的 %s=%v，期望 %v（完整载荷 %v）", k, got, want, payload)
		}
	}
	observeB04FormLast(t, payload)
	observeB04Action(t, obsFacts{
		"test": "click-submit", "control": "submit", "delivered": res.Delivered,
		"verified": res.Verified, "status": string(res.Verification.Status),
		"counterBefore": 0, "counterAfter": after,
	})
	finalizeB04ActionObservation(t, h)
}

// ---------- BR09：密码与截图 ----------

// TestPasswordNeverLeaks 检查密码字段的值既不在快照 JSON 里，也不在正文/标签/日志里。
//
// 只断言"没出现"是不够的：还要先证明那个密码字段真的被填进去过（field_value 核验），
// 否则"没有泄漏"可能只是因为什么都没发生。
func TestPasswordNeverLeaks(t *testing.T) {
	h, sink := newB04Harness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	const sentinel = "S3cr3t-值-42"
	const fixtureSecret = browserfixture.FixturePass

	navigateTo(t, ctx, h, "/login", "登录 cookie")
	snap, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(/login): %v", err)
	}
	pass := findByInput(t, snap, "input", "password")
	if strings.Contains(pass.Name, fixtureSecret) {
		t.Fatalf("密码字段的标签里出现了密码值: %q", pass.Name)
	}
	initial, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("序列化快照: %v", err)
	}
	if strings.Contains(string(initial), fixtureSecret) {
		t.Fatal("快照 JSON 里出现了页面预置的密码值")
	}
	observeB04Snapshot(t, "br09-login", snap)

	res, err := h.host.Act(ctx, h.tab, browser.Action{
		Kind: browser.ActionFill, SnapshotID: snap.ID, Ref: pass.Ref, Text: sentinel,
		ExpectedCondition: browser.ExpectedCondition{
			Kind: browser.ConditionFieldValue, Ref: pass.Ref, Value: sentinel, TimeoutMS: 5000,
		},
	})
	if err != nil {
		t.Fatalf("填写密码字段: %v", err)
	}
	if !res.Verified {
		t.Fatalf("密码没有真的写进去（后续的「没泄漏」就没有意义）: status=%s detail=%s",
			res.Verification.Status, res.Verification.Detail)
	}

	after, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("填写后 Observe: %v", err)
	}
	payload, err := json.Marshal(after)
	if err != nil {
		t.Fatalf("序列化快照: %v", err)
	}
	if strings.Contains(string(payload), sentinel) {
		t.Fatal("快照 JSON 里出现了刚填进去的密码值")
	}
	if strings.Contains(after.Text, sentinel) {
		t.Fatal("快照正文里出现了刚填进去的密码值")
	}
	passAfter := findByInput(t, after, "input", "password")
	if strings.Contains(passAfter.Name, sentinel) {
		t.Fatalf("密码字段的标签里出现了刚填进去的值: %q", passAfter.Name)
	}
	// 核验结论同样不能回显密码值（field_value 核验在页面侧比较，只回布尔）。
	if strings.Contains(res.Verification.Detail, sentinel) {
		t.Fatalf("核验结论里出现了密码值: %q", res.Verification.Detail)
	}
	// 事件日志同样不能泄漏（fill 的文本刻意不进事件载荷）。
	logJSON := sink.JSON(t)
	if strings.Contains(logJSON, sentinel) {
		t.Fatal("宿主事件日志里出现了密码值")
	}
	if strings.Contains(logJSON, fixtureSecret) {
		t.Fatal("宿主事件日志里出现了页面预置的密码值")
	}
	observeB04PasswordScan(t, obsFacts{
		"snapshotOccurrences": strings.Count(string(payload), sentinel) + strings.Count(string(payload), fixtureSecret),
		"textOccurrences":     strings.Count(after.Text, sentinel) + strings.Count(after.Text, fixtureSecret),
		"nameOccurrences":     strings.Count(passAfter.Name, sentinel) + strings.Count(passAfter.Name, fixtureSecret),
		"logOccurrences":      strings.Count(logJSON, sentinel) + strings.Count(logJSON, fixtureSecret),
		"passwordInputType":   passAfter.InputType,
		"filled":              res.Verified,
	})
	finalizeB04PasswordObservation(t)
}

// TestScreenshotIsCompleteAndBounded 检查截图的两半：
//   - 普通页面：像素尺寸 = CSS 尺寸 × scale，PNG 可解码，字节在上限内；
//   - 刻意做大的页面（2M×3M 随机像素）：要么给出**受控缩小**后的有界图片（scale 更新），
//     要么给出结构化错误；绝不返回被截断的坏图。
func TestScreenshotIsCompleteAndBounded(t *testing.T) {
	h, _ := newB04Harness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	navigateTo(t, ctx, h, "/form", "普通表单")
	snap, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(/form): %v", err)
	}
	img, err := h.host.Screenshot(ctx, h.tab)
	if err != nil {
		t.Fatalf("Screenshot(/form): %v", err)
	}
	if err := img.Validate(); err != nil {
		t.Fatalf("截图元数据不自洽: %v", err)
	}
	if len(img.Bytes) > browser.MaxScreenshotBytes {
		t.Fatalf("截图 %d 字节超过上限 %d", len(img.Bytes), browser.MaxScreenshotBytes)
	}
	if img.NavigationID != snap.NavigationID {
		t.Fatalf("截图的 navigationId=%q，快照是 %q：坐标无法回溯到同一次文档", img.NavigationID, snap.NavigationID)
	}
	if absFloat(img.CSSWidth-snap.Viewport.Width) > 1 {
		t.Fatalf("截图的 CSS 宽度 %.1f 与快照视口 %.1f 不一致", img.CSSWidth, snap.Viewport.Width)
	}
	px := pngStats(t, img.Bytes)
	if px.width != img.PixelWidth || px.height != img.PixelHeight {
		t.Fatalf("PNG 实际尺寸 %dx%d 与元数据 %dx%d 不一致", px.width, px.height, img.PixelWidth, img.PixelHeight)
	}
	if want := int(img.CSSWidth * img.Scale); absInt(px.width-want) > 1 {
		t.Fatalf("像素宽度 %d 与 CSS %.1f×scale %.3f=%d 不一致", px.width, img.CSSWidth, img.Scale, want)
	}
	if px.uniqueColors <= 4 {
		t.Fatalf("截图是纯色（uniqueColors=%d），不是真实页面像素", px.uniqueColors)
	}
	sum := sha256.Sum256(img.Bytes)
	sha := hex.EncodeToString(sum[:])
	observeB04Screenshot(t, "br09-screen", img, sha)
	t.Logf("截图: %dx%d css=%.0fx%.0f scale=%.3f bytes=%d sha256=%s",
		px.width, px.height, img.CSSWidth, img.CSSHeight, img.Scale, len(img.Bytes), sha)

	// ---- 刻意的大页面：噪声 + 大视口 ----
	//
	// 顺序很重要：噪声画布在文档加载时按当时视口铺满，所以必须先放大视口、
	// **再重新加载**页面，否则画布还是小尺寸，"大截图"根本不会发生。
	navigateTo(t, ctx, h, "/noise", "噪声页面")
	t.Cleanup(func() { clearDeviceMetrics(t, h) })
	setDeviceMetrics(t, h, 1600, 2400, 1)
	if err := h.host.Navigate(ctx, h.tab, h.fixture.Primary.URL+"/noise"); err != nil {
		t.Fatalf("在大视口下重新加载噪声页面: %v", err)
	}
	navigateTo(t, ctx, h, "/noise", "噪声页面")

	huge, err := h.host.Screenshot(ctx, h.tab)
	if err != nil {
		// 允许的第二种结果：结构化错误（明确说超限、没有返回坏图）。
		if code := appErrCode(t, err); code != contracts.CodeUnsupported {
			t.Fatalf("超限截图的错误码 %q，期望 %s: %v", code, contracts.CodeUnsupported, err)
		}
		if !strings.Contains(err.Error(), "4") && !strings.Contains(err.Error(), "字节") {
			t.Fatalf("超限截图的错误消息没有说明原因: %v", err)
		}
		observeB04ScreenshotOutcome(t, obsFacts{"outcome": "structured-error", "message": truncate(err.Error(), 200)})
		finalizeB04ScreenshotObservation(t)
		return
	}
	if err := huge.Validate(); err != nil {
		t.Fatalf("受控缩小后的截图元数据不自洽: %v", err)
	}
	if len(huge.Bytes) > browser.MaxScreenshotBytes {
		t.Fatalf("大页面截图 %d 字节超过上限 %d（既没有缩小也没有报错）", len(huge.Bytes), browser.MaxScreenshotBytes)
	}
	bigStats := pngStats(t, huge.Bytes)
	if bigStats.width != huge.PixelWidth || bigStats.height != huge.PixelHeight {
		t.Fatalf("大页面 PNG 尺寸 %dx%d 与元数据 %dx%d 不一致", bigStats.width, bigStats.height, huge.PixelWidth, huge.PixelHeight)
	}
	if want := int(huge.CSSWidth * huge.Scale); absInt(bigStats.width-want) > 1 {
		t.Fatalf("大页面像素宽度 %d 与 CSS %.1f×scale %.3f=%d 不一致：缩小后 scale 必须更新",
			bigStats.width, huge.CSSWidth, huge.Scale, want)
	}
	sum = sha256.Sum256(huge.Bytes)
	sha = hex.EncodeToString(sum[:])
	observeB04Screenshot(t, "br09-screen-big", huge, sha)
	observeB04ScreenshotOutcome(t, obsFacts{
		"outcome":     "bounded-image",
		"bytes":       len(huge.Bytes),
		"scale":       huge.Scale,
		"cssWidth":    huge.CSSWidth,
		"cssHeight":   huge.CSSHeight,
		"pixelWidth":  huge.PixelWidth,
		"pixelHeight": huge.PixelHeight,
		"sha256":      sha,
	})
	t.Logf("大页面截图: %dx%d css=%.0fx%.0f scale=%.3f bytes=%d",
		bigStats.width, bigStats.height, huge.CSSWidth, huge.CSSHeight, huge.Scale, len(huge.Bytes))
	finalizeB04ScreenshotObservation(t)
}

// ---------- BR10：Unsupported 表面与后置条件失败 ----------

// TestClosedShadowAndCrossOriginAreUnsupported 检查"看不见的区域必须说出来"：
// cross-origin iframe 与 closed shadow root 都要以 Unsupported 出现在快照里并带原因，
// 而且它们内部的按钮确实点不到（counter 保持 0，没有按坐标绕过）。
func TestClosedShadowAndCrossOriginAreUnsupported(t *testing.T) {
	h, _ := newB04Harness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	primary, secondary := h.fixture.Primary, h.fixture.Secondary

	// ---- cross-origin iframe ----
	navigateTo(t, ctx, h, "/cross-origin", "cross-origin iframe")
	snap, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(/cross-origin): %v", err)
	}
	crossOrigin := ""
	for _, f := range snap.UnsupportedFrames {
		if strings.Contains(f.URL, secondary.URL) {
			crossOrigin = f.Reason
			if !strings.Contains(strings.ToLower(f.Reason), "cross-origin") {
				t.Fatalf("cross-origin frame 的原因必须是 cross-origin，实际 %q", f.Reason)
			}
			if strings.TrimSpace(f.FrameID) == "" {
				t.Fatal("UnsupportedFrame 缺少 frameId")
			}
		}
	}
	if crossOrigin == "" {
		t.Fatalf("快照没有声明 cross-origin frame（unsupportedFrames=%v）", snap.UnsupportedFrames)
	}
	for _, el := range snap.Elements {
		if el.Tag == "button" && strings.Contains(el.Name, "frame 内计数") {
			t.Fatal("cross-origin frame 里的元素出现在快照里：那是读不到的 DOM")
		}
	}
	outer := findByName(t, snap, "button", "外层计数一次")
	if _, err := h.host.Act(ctx, h.tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: snap.ID, Ref: outer.Ref,
	}); err != nil {
		t.Fatalf("点外层按钮: %v", err)
	}
	if got := fixtureCounter(t, primary.URL, browserfixture.CounterFrameAction); got != 1 {
		t.Fatalf("外层按钮的服务端计数 %d，期望 1", got)
	}
	if got := fixtureCounter(t, secondary.URL, browserfixture.CounterFrameAction); got != 0 {
		t.Fatalf("frame 内的按钮被点到了（secondary counter=%d）：不应该有任何输入穿过 frame", got)
	}
	// 用不存在的 ref 也点不到 frame 里的东西：结构化拒绝，不按坐标猜。
	if _, err := h.host.Act(ctx, h.tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: snap.ID, Ref: "frame-target",
	}); err == nil {
		t.Fatal("指向 frame 内元素的 ref 必须被拒绝")
	} else if code := appErrCode(t, err); code != contracts.CodeStaleSnapshot {
		t.Fatalf("未知 ref 的错误码 %q，期望 %s", code, contracts.CodeStaleSnapshot)
	}
	observeB04Snapshot(t, "br10-cross-origin", snap)
	observeB04Unsupported(t, "crossOrigin", snap.UnsupportedFrames)

	// ---- closed shadow root ----
	navigateTo(t, ctx, h, "/closed-shadow", "closed shadow root")
	shadowSnap, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(/closed-shadow): %v", err)
	}
	closedReason := ""
	for _, f := range shadowSnap.UnsupportedFrames {
		if strings.Contains(strings.ToLower(f.Reason), "closed shadow") {
			closedReason = f.Reason
		}
	}
	if closedReason == "" {
		t.Fatalf("快照没有声明 closed shadow root（unsupportedFrames=%v）", shadowSnap.UnsupportedFrames)
	}
	for _, el := range shadowSnap.Elements {
		if strings.Contains(el.Name, "shadow 内按钮") {
			t.Fatal("closed shadow root 里的按钮出现在快照里：宿主脚本读不到它")
		}
	}
	light := findByName(t, shadowSnap, "button", "光 DOM 计数")
	if _, err := h.host.Act(ctx, h.tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: shadowSnap.ID, Ref: light.Ref,
	}); err != nil {
		t.Fatalf("点光 DOM 按钮: %v", err)
	}
	if got := fixtureCounter(t, primary.URL, browserfixture.CounterShadowLight); got != 1 {
		t.Fatalf("光 DOM 按钮计数 %d，期望 1", got)
	}
	if got := fixtureCounter(t, primary.URL, browserfixture.CounterShadowClosed); got != 0 {
		t.Fatalf("closed shadow root 里的按钮被点到了（counter=%d）", got)
	}
	observeB04Snapshot(t, "br10-closed-shadow", shadowSnap)
	observeB04Unsupported(t, "closedShadow", shadowSnap.UnsupportedFrames)
	finalizeB04UnsupportedObservation(t, h)
}

// TestTrustedOnlyPostConditionFailure 覆盖"Delivered 不能冒充 Verified"：
// 后置条件不成立时必须返回 VerificationFailed，且仍然如实报告输入已发出；
// 同一个按钮换成可满足的条件则必须 verified（证明不是"永远失败"）。
func TestTrustedOnlyPostConditionFailure(t *testing.T) {
	h, _ := newB04Harness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	primary := h.fixture.Primary
	navigateTo(t, ctx, h, "/trusted-only", "trusted-only")
	snap, err := h.host.Observe(ctx, h.tab, browser.ObserveOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("Observe(/trusted-only): %v", err)
	}
	target := findByName(t, snap, "button", "只认真实点击")
	last := findElement(t, snap, "事件回显 #trusted-last", func(el browser.Element) bool {
		return el.Tag == "code" && el.Name == "none"
	})

	// (1) 可满足的后置条件：页面把 isTrusted 记为 "trusted"。
	res, err := h.host.Act(ctx, h.tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: snap.ID, Ref: target.Ref,
		ExpectedCondition: browser.ExpectedCondition{
			Kind: browser.ConditionElementText, Ref: last.Ref, Value: "trusted", TimeoutMS: 10000,
		},
	})
	if err != nil {
		t.Fatalf("点 trusted-only 按钮: %v", err)
	}
	if !res.Delivered || !res.Verified {
		t.Fatalf("CDP 点击是可信事件，应当核验通过: delivered=%t verified=%t status=%s detail=%s",
			res.Delivered, res.Verified, res.Verification.Status, res.Verification.Detail)
	}
	if got := fixtureCounter(t, primary.URL, browserfixture.CounterTrustedOnly); got != 1 {
		t.Fatalf("服务端 trusted-only 计数 %d，期望 1", got)
	}

	// (2) 不可能成立的后置条件：必须 VerifiedFailed，不能因为"输入发出去了"就算成功。
	bad, err := h.host.Act(ctx, h.tab, browser.Action{
		Kind: browser.ActionClick, SnapshotID: snap.ID, Ref: target.Ref,
		ExpectedCondition: browser.ExpectedCondition{
			Kind: browser.ConditionElementText, Ref: last.Ref,
			Value: "untrusted（已忽略，未发请求）", TimeoutMS: 3000,
		},
	})
	if err != nil {
		t.Fatalf("第二次点击: %v", err)
	}
	if !bad.Delivered {
		t.Fatal("第二次点击应当如实报告输入已发出（Delivered=true）")
	}
	if bad.Verified {
		t.Fatal("后置条件不成立却报 Verified=true：Delivered 冒充了业务成功")
	}
	if bad.Verification.Status != browser.VerificationFailed {
		t.Fatalf("后置条件不成立时状态应为 failed，实际 %q（detail=%s）",
			bad.Verification.Status, bad.Verification.Detail)
	}
	if strings.TrimSpace(bad.Error) == "" {
		t.Fatal("失败的核验必须带可读原因")
	}
	if err := bad.Validate(); err != nil {
		t.Fatalf("失败结果本身必须自洽: %v", err)
	}
	if got := fixtureCounter(t, primary.URL, browserfixture.CounterTrustedOnly); got != 2 {
		t.Fatalf("第二次可信点击也应计入服务端（%d，期望 2）", got)
	}
	if got := fixtureCounter(t, primary.URL, browserfixture.CounterTrustedRejected); got != 0 {
		t.Fatalf("没有非 trusted 请求，rejected 应为 0，实际 %d", got)
	}
	observeB04Snapshot(t, "br10-trusted-only", snap)
	observeB04TrustedOutcome(t, obsFacts{
		"firstClickVerified":   res.Verified,
		"secondClickVerified":  bad.Verified,
		"secondClickStatus":    string(bad.Verification.Status),
		"secondClickDelivered": bad.Delivered,
		"trustedCounter":       fixtureCounter(t, primary.URL, browserfixture.CounterTrustedOnly),
		"rejectedCounter":      fixtureCounter(t, primary.URL, browserfixture.CounterTrustedRejected),
		"failureDetail":        truncate(bad.Verification.Detail, 200),
	})
	finalizeB04TrustedObservation(t)
}

// ---------- 小工具 ----------

// appErrCode 取机器可读的错误码（前端按 Code 分支，测试也按 Code 断言）。
func appErrCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("期望一个错误，实际 nil")
	}
	var ae *contracts.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("错误必须是 *contracts.AppError（实际 %T: %v）", err, err)
	}
	return ae.Code
}

func setDeviceMetrics(t *testing.T, h *harness, width, height int, dpr float64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	params := fmt.Sprintf(`{"width":%d,"height":%d,"deviceScaleFactor":%.2f,"mobile":false}`,
		width, height, dpr)
	if _, err := h.host.CallCDP(ctx, h.tab, "Emulation.setDeviceMetricsOverride", params, 15*time.Second); err != nil {
		t.Fatalf("Emulation.setDeviceMetricsOverride: %v", err)
	}
}

func clearDeviceMetrics(t *testing.T, h *harness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := h.host.CallCDP(ctx, h.tab, "Emulation.clearDeviceMetricsOverride", `{}`, 15*time.Second); err != nil {
		t.Logf("警告: 清除 device metrics 覆盖失败: %v", err)
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
