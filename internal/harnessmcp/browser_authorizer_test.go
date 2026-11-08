package harnessmcp

// T1（BR07.tool）：把浏览器写动作的授权真正接上 —— MCP 工具 → 策略服务 Authorize
// → 派发 → 核验，并且**任何情况下都不默认放行**。
//
// 这一组用例与 BR13 的分工：
//   - BR13 证明"浏览器通道经 MCP + 策略服务"（拒绝/过期/换站点后 counter 为 0）；
//   - 这里证明**接线本身**：生产构造函数（run binding 身份 + 真实策略服务）装配起来之后
//     允许恰好一次、拒绝/过期/换站点为 0，并且缺授权器/装配错误时仍然默认拒绝。
//
// 为什么这些断言落在 fixture 服务端计数上：错误文案可以被改得"看起来对"，服务端
// counter 不会。允许路径 counter 必须**恰好 1**（多一次就是重复派发）；拒绝路径必须
// **恰好 0**（哪怕过去一次，页面已经被改过了）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/policy"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

// browserCallResult 是异步 tools/call 的结论。
//
// 等待人工答复的动作会阻塞在 tools/call 里，必须在 goroutine 里发；这里刻意不用
// t.Fatalf 的 browserCall（在非测试 goroutine 里 FailNow 会让结论丢失）。
type browserCallResult struct {
	text  string
	isErr bool
}

func browserCallAsync(h *policyHarness, name string, args map[string]any) <-chan browserCallResult {
	ch := make(chan browserCallResult, 1)
	go func() {
		code, resp := h.mcp.rpc(11, "tools/call", map[string]any{"name": name, "arguments": args})
		if code != http.StatusOK || resp.Error != nil {
			ch <- browserCallResult{
				text:  fmt.Sprintf("rpc code=%d err=%+v", code, resp.Error),
				isErr: true,
			}
			return
		}
		result, _ := resp.Result.(map[string]any)
		content, _ := result["content"].([]any)
		text := ""
		for _, c := range content {
			if m, ok := c.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					text += s
				}
			}
		}
		isErr, _ := result["isError"].(bool)
		ch <- browserCallResult{text: text, isErr: isErr}
	}()
	return ch
}

// decodeApprovalArgs 把审批里的参数指纹解出来。
func decodeApprovalArgs(t *testing.T, p policy.ApprovalRequest) browserCallArguments {
	t.Helper()
	var args browserCallArguments
	if err := json.Unmarshal(p.Arguments, &args); err != nil {
		t.Fatalf("审批参数不是 browser_act 的指纹: %v（%s）", err, string(p.Arguments))
	}
	return args
}

// TestBrowserToolAuthorizedWriteHitsFixtureExactlyOnce 是 BR07.tool 的正向路径。
//
// 装配走生产构造函数 NewBrowserPolicyAuthorizer：执行身份来自 run binding，
// 授权器只认这一份身份。允许之后服务端 counter 必须**恰好** 1。
func TestBrowserToolAuthorizedWriteHitsFixtureExactlyOnce(t *testing.T) {
	h := newPolicyHarness(t, policyHarnessOptions{Network: true, PinnedBinding: true})
	nameRef := h.backend.RefOf(h.tab, "name")
	submitRef := h.backend.RefOf(h.tab, "submit")

	// fill：问一次 → 用户点"允许一次" → 派发。
	if text, isErr := browserCall(t, h, BrowserToolAct, map[string]any{
		"tabId": string(h.tab), "kind": "fill", "snapshotId": h.snap.ID,
		"ref": nameRef, "text": "王小明",
	}); isErr {
		t.Fatalf("fill 失败: %s", text)
	}
	if got := h.backend.Field(h.tab, "name"); got != "王小明" {
		t.Fatalf("fill 没有真的写入字段（读到 %q）", got)
	}
	if n := h.backend.Dispatched(); n != 1 {
		t.Fatalf("fill 之后派发计数 = %d，期望 1", n)
	}
	// fill 不提交表单：counter 必须仍是 0（别把"输入过"当成"提交过"）。
	if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("fill 之后 form-submit = %d，期望 0", n)
	}

	// click：再问一次 → 允许一次 → 服务端真实提交恰好一次。
	clickText, isErr := browserCall(t, h, BrowserToolAct, map[string]any{
		"tabId": string(h.tab), "kind": "click", "snapshotId": h.snap.ID, "ref": submitRef,
	})
	if isErr {
		t.Fatalf("click 失败: %s", clickText)
	}
	if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 1 {
		t.Fatalf("允许路径的 form-submit = %d，期望恰好 1（多一次就是重复派发）", n)
	}
	if n := h.backend.Dispatched(); n != 2 {
		t.Fatalf("允许路径的派发计数 = %d，期望 2", n)
	}
	if !strings.Contains(clickText, "Delivered") {
		t.Fatalf("click 结论应说明 Delivered 不等于业务成功: %s", clickText)
	}

	// 审批指纹必须含 origin 与 navigationId：站点/文档变了，同一条审批不再成立。
	approvals := h.waitApprovals(t, 2)
	first := decodeApprovalArgs(t, approvals[0])
	if first.Origin != h.fixture.Primary.URL {
		t.Fatalf("审批指纹的 origin = %q，期望 %q", first.Origin, h.fixture.Primary.URL)
	}
	if first.NavigationID == "" || first.NavigationID != h.snap.NavigationID {
		t.Fatalf("审批指纹的 navigationId = %q，期望 %q", first.NavigationID, h.snap.NavigationID)
	}
	if first.Kind != "fill" || first.TabID != string(h.tab) {
		t.Fatalf("审批指纹的动作/tab 不对: %+v", first)
	}
	// 身份来自 run binding：审批上记的就是这次 run 的会话与 run id。
	if approvals[0].SessionID != h.exec.SessionID || approvals[0].RunID != h.exec.RunID {
		t.Fatalf("审批身份 = (%s,%s)，期望 run binding (%s,%s)",
			approvals[0].SessionID, approvals[0].RunID, h.exec.SessionID, h.exec.RunID)
	}

	observeBrowserFlow(t, h, approvals)
}

// TestBrowserToolPendingApprovalDispatchesNothingAndDenialLeavesCounterZero
// 证明"等人工答复期间页面一个字节都没变"，以及用户拒绝后 counter 恒为 0。
func TestBrowserToolPendingApprovalDispatchesNothingAndDenialLeavesCounterZero(t *testing.T) {
	h := newPolicyHarness(t, policyHarnessOptions{Network: true, Manual: true})

	done := browserCallAsync(h, BrowserToolAct, map[string]any{
		"tabId": string(h.tab), "kind": "click", "snapshotId": h.snap.ID,
		"ref": h.backend.RefOf(h.tab, "submit"),
	})
	approvals := h.waitApprovals(t, 1)
	// 还在等人工：没有派发、没有提交。
	if n := h.backend.Dispatched(); n != 0 {
		t.Fatalf("等待审批期间派发了 %d 次输入", n)
	}
	if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("等待审批期间 form-submit = %d，期望 0", n)
	}

	if err := h.resolveApproval(t, approvals[0].ID, policy.ApprovalDeny); err != nil {
		t.Fatalf("答复拒绝失败: %v", err)
	}
	res := <-done
	if !res.isErr {
		t.Fatalf("用户拒绝后工具必须返回失败: %s", res.text)
	}
	if n := h.backend.Dispatched(); n != 0 {
		t.Fatalf("用户拒绝后仍派发了 %d 次", n)
	}
	if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("用户拒绝后 form-submit = %d，期望 0", n)
	}
	observeBrowserDenials(t, h, "denied")
}

// TestBrowserToolExpiredApprovalLeavesCounterZero：审批过期后即使有人补批也进不到派发。
func TestBrowserToolExpiredApprovalLeavesCounterZero(t *testing.T) {
	// 审批有效期用真实流逝时间（120ms）：这里不拨假时钟。
	h := newPolicyHarness(t, policyHarnessOptions{
		Network: true, Manual: true, ApprovalTTL: 120 * time.Millisecond,
	})
	done := browserCallAsync(h, BrowserToolAct, map[string]any{
		"tabId": string(h.tab), "kind": "click", "snapshotId": h.snap.ID,
		"ref": h.backend.RefOf(h.tab, "submit"),
	})
	approvals := h.waitApprovals(t, 1)

	time.Sleep(300 * time.Millisecond)
	res := <-done
	if !res.isErr {
		t.Fatalf("审批过期的动作必须失败: %s", res.text)
	}
	if !strings.Contains(res.text, "过期") && !strings.Contains(res.text, "复查") {
		t.Fatalf("失败结论应说明审批过期/复查未通过: %s", res.text)
	}
	// 过期之后补批必须失败：过期的许可不能复活。
	if err := h.resolveApproval(t, approvals[0].ID, policy.ApprovalOnce); err == nil {
		t.Fatal("过期审批仍然被答复成功：过期的许可可以复活")
	}
	if n := h.backend.Dispatched(); n != 0 {
		t.Fatalf("审批过期后仍派发了 %d 次", n)
	}
	if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("审批过期后 form-submit = %d，期望 0", n)
	}
	observeBrowserDenials(t, h, "expired")
}

// TestBrowserToolOriginChangeRequiresFreshApproval：换 origin 之后旧的一次性许可
// 不能复用，必须重新审批；在拿到新审批之前 counter 保持 0。
func TestBrowserToolOriginChangeRequiresFreshApproval(t *testing.T) {
	h := newPolicyHarness(t, policyHarnessOptions{Network: true, Manual: true})
	ctx := context.Background()

	// A 站：一次 fill（允许一次）。
	doneA := browserCallAsync(h, BrowserToolAct, map[string]any{
		"tabId": string(h.tab), "kind": "fill", "snapshotId": h.snap.ID,
		"ref": h.backend.RefOf(h.tab, "name"), "text": "A 站",
	})
	approvals := h.waitApprovals(t, 1)
	firstID := approvals[0].ID
	if got := decodeApprovalArgs(t, approvals[0]).Origin; got != h.fixture.Primary.URL {
		t.Fatalf("第一次审批的 origin = %q，期望 %q", got, h.fixture.Primary.URL)
	}
	if err := h.resolveApproval(t, firstID, policy.ApprovalOnce); err != nil {
		t.Fatalf("答复第一次审批失败: %v", err)
	}
	if res := <-doneA; res.isErr {
		t.Fatalf("A 站 fill 失败: %s", res.text)
	}
	// 一次性许可用过一次就不能再用（否则"允许一次"等于"一直允许"）。
	if err := h.resolveApproval(t, firstID, policy.ApprovalOnce); err == nil {
		t.Fatal("同一条一次性许可被答复了两次")
	}

	// 真实导航到另一个 origin：文档身份与站点都变了。
	if err := h.browser.Navigate(ctx, h.exec, h.tab, h.fixture.Secondary.URL+"/form"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	snapB, err := h.browser.Observe(ctx, h.exec, h.tab, browser.ObserveOptions{})
	if err != nil {
		t.Fatalf("Observe(换站点后): %v", err)
	}
	if snapB.NavigationID == h.snap.NavigationID {
		t.Fatalf("换站点后 navigationId 没变（%q）：动作可能绑在同一个文档上", snapB.NavigationID)
	}

	doneB := browserCallAsync(h, BrowserToolAct, map[string]any{
		"tabId": string(h.tab), "kind": "fill", "snapshotId": snapB.ID,
		"ref": h.backend.RefOf(h.tab, "name"), "text": "B 站",
	})
	approvals = h.waitApprovals(t, 2)
	secondID := approvals[1].ID
	if secondID == firstID {
		t.Fatalf("换站点后复用了同一条审批 %s：旧许可跨站点生效了", firstID)
	}
	second := decodeApprovalArgs(t, approvals[1])
	if second.Origin != h.fixture.Secondary.URL {
		t.Fatalf("换站点后审批的 origin = %q，期望 %q", second.Origin, h.fixture.Secondary.URL)
	}
	if second.NavigationID != snapB.NavigationID {
		t.Fatalf("换站点后审批的 navigationId = %q，期望 %q", second.NavigationID, snapB.NavigationID)
	}
	// 新审批还没答复：B 站 counter 必须是 0，且没有任何新派发（A 站那一次不算）。
	if n := h.fixture.Secondary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("新审批未答复时 B 站 form-submit = %d，期望 0", n)
	}
	if n := h.backend.Dispatched(); n != 1 {
		t.Fatalf("新审批未答复时派发计数 = %d，期望仍为 1（只有 A 站那一次）", n)
	}

	if err := h.resolveApproval(t, secondID, policy.ApprovalOnce); err != nil {
		t.Fatalf("答复第二次审批失败: %v", err)
	}
	if res := <-doneB; res.isErr {
		t.Fatalf("B 站 fill 失败: %s", res.text)
	}

	// B 站提交一次：又要一次新审批；允许后 counter 恰好 1。
	doneB2 := browserCallAsync(h, BrowserToolAct, map[string]any{
		"tabId": string(h.tab), "kind": "click", "snapshotId": snapB.ID,
		"ref": h.backend.RefOf(h.tab, "submit"),
	})
	approvals = h.waitApprovals(t, 3)
	if n := h.fixture.Secondary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("提交审批未答复时 B 站 form-submit = %d，期望 0", n)
	}
	if err := h.resolveApproval(t, approvals[2].ID, policy.ApprovalOnce); err != nil {
		t.Fatalf("答复第三次审批失败: %v", err)
	}
	if res := <-doneB2; res.isErr {
		t.Fatalf("B 站 click 失败: %s", res.text)
	}
	if n := h.fixture.Secondary.Counter(browserfixture.CounterFormSubmit); n != 1 {
		t.Fatalf("B 站 form-submit = %d，期望恰好 1", n)
	}
	if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("换站点动作写到了 A 站（primary form-submit = %d）", n)
	}

	observeBrowserDenials(t, h, "origin-change-fresh-approval")
}

// TestBrowserToolFailsClosedWithoutAuthorizer 是"默认放行"回归的捕手。
//
// 三种装配错误都必须让写动作在**派发之前**失败，fixture counter 保持 0：
//   - 完全没有授权器（服务层的 nil 分支）；
//   - 授权器存在但配置错误（Gate 为空）；
//   - 生产构造函数拒绝这种装配错误（不返回一个"看起来能用"的授权器）。
//
// 如果谁把默认分支改成 allow（或让 nil Gate 直接放行），这里会立刻红：
// counter 会从 0 变成 1。
func TestBrowserToolFailsClosedWithoutAuthorizer(t *testing.T) {
	t.Run("没有装配授权器", func(t *testing.T) {
		h := newPolicyHarness(t, policyHarnessOptions{Network: true, NoAuthorizer: true})
		text, isErr := browserCall(t, h, BrowserToolAct, map[string]any{
			"tabId": string(h.tab), "kind": "click", "snapshotId": h.snap.ID,
			"ref": h.backend.RefOf(h.tab, "submit"),
		})
		if !isErr {
			t.Fatalf("没有授权器时写动作必须失败: %s", text)
		}
		if !strings.Contains(text, "默认拒绝") && !strings.Contains(text, "授权") {
			t.Fatalf("失败结论应说明缺少授权/默认拒绝: %s", text)
		}
		if n := h.backend.Dispatched(); n != 0 {
			t.Fatalf("没有授权器时仍派发了 %d 次", n)
		}
		if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
			t.Fatalf("没有授权器时 form-submit = %d，期望 0", n)
		}
		observeBrowserDenials(t, h, "no-authorizer")
	})

	t.Run("授权器配置错误（没有策略服务）", func(t *testing.T) {
		h := newPolicyHarness(t, policyHarnessOptions{Network: true, BrokenAuthorizer: true})
		text, isErr := browserCall(t, h, BrowserToolAct, map[string]any{
			"tabId": string(h.tab), "kind": "click", "snapshotId": h.snap.ID,
			"ref": h.backend.RefOf(h.tab, "submit"),
		})
		if !isErr {
			t.Fatalf("授权器缺少策略服务时写动作必须失败（不能默认放行）: %s", text)
		}
		if !strings.Contains(text, "默认拒绝") {
			t.Fatalf("失败结论必须写明默认拒绝: %s", text)
		}
		if n := h.backend.Dispatched(); n != 0 {
			t.Fatalf("授权器配置错误时仍派发了 %d 次", n)
		}
		if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
			t.Fatalf("授权器配置错误时 form-submit = %d，期望 0", n)
		}
		observeBrowserDenials(t, h, "broken-authorizer")
	})

	t.Run("生产构造函数拒绝缺策略服务的装配", func(t *testing.T) {
		if _, err := NewBrowserPolicyAuthorizer(BrowserAuthorizerOptions{}); err == nil {
			t.Fatal("NewBrowserPolicyAuthorizer 接受了没有策略服务的装配：默认放行的入口被打开了")
		}
		if _, err := NewBrowserPolicyAuthorizer(BrowserAuthorizerOptions{
			Gate:         policy.New(policy.Options{}),
			Binding:      &BrowserAuthorizationBinding{},
			ApprovalWait: time.Second,
		}); err == nil {
			t.Fatal("NewBrowserPolicyAuthorizer 接受了空会话的 run binding：身份缺失时不能装配授权器")
		}
	})
}

// TestBrowserAuthorizerRefusesForeignExecutionBinding：授权器只认 run binding 里的
// 执行身份；换一个 run/session 的 exec 必须被拒——工具参数、调用方都不能另造身份。
func TestBrowserAuthorizerRefusesForeignExecutionBinding(t *testing.T) {
	h := newPolicyHarness(t, policyHarnessOptions{Network: true, PinnedBinding: true})
	if h.authorizer == nil {
		t.Fatal("生产构造函数没有返回授权器")
	}
	req := browser.AuthorizationRequest{
		TabID: h.tab, SessionID: h.exec.SessionID,
		Origin: h.fixture.Primary.URL, URL: h.fixture.Primary.URL + "/form",
		NavigationID: h.snap.NavigationID, Generation: 1,
		Action: browser.Action{
			Kind: browser.ActionClick, SnapshotID: h.snap.ID,
			Ref: h.backend.RefOf(h.tab, "submit"),
		},
	}

	foreign := h.exec
	foreign.SessionID = "sess-other"
	foreign.RunID = "run-other"
	if _, err := h.authorizer.Authorize(context.Background(), foreign, req); err == nil {
		t.Fatal("换了 run/session 的执行身份仍然通过授权：身份可以被调用方自报")
	}
	// 缺会话身份时同样拒绝（不能“没身份就当自己人”）。
	noSession := h.exec
	noSession.SessionID = ""
	if _, err := h.authorizer.Authorize(context.Background(), noSession, req); err == nil {
		t.Fatal("没有会话身份仍然通过了授权")
	}
	if err := h.authorizer.Recheck(context.Background(), foreign,
		req, h.authorizer.Guard.Issue(foreign.SessionID, req, "", "伪造")); err == nil {
		t.Fatal("换了 run/session 的执行身份仍然通过派发前复查")
	}

	// 原绑定仍然能走通（不是把所有调用都拒了）。
	token, err := h.authorizer.Authorize(context.Background(), h.exec, req)
	if err != nil {
		t.Fatalf("原绑定的授权被拒绝: %v", err)
	}
	if err := h.authorizer.Recheck(context.Background(), h.exec, req, token); err != nil {
		t.Fatalf("原绑定的派发前复查被拒绝: %v", err)
	}
	if n := h.fixture.Primary.Counter(browserfixture.CounterFormSubmit); n != 0 {
		t.Fatalf("授权器单元路径不应产生服务端副作用，form-submit = %d", n)
	}
	observeBrowserDenials(t, h, "foreign-binding")
}
