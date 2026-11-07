//go:build windows

package nativetest

// B04（BR07–BR10）的外部观察装置。
//
// 观察源全部是**磁盘上的原始产物**，不是测试进程里的断言变量：
//   - snapshots/*.json      观察结果的原始 JSON（含 truncated/元素数/JSON 字节数）；
//   - actions.jsonl         每次动作一行（kind/delivered/verified/服务端 counter 前后）；
//   - stale-rejections.jsonl 每次陈旧引用被拒绝一行（错误码 + counter 前后）；
//   - limits.json           三条限额的实测值与上限常量；
//   - password-scan.json    密码值在快照/正文/标签/事件日志里的出现次数（全部必须为 0）；
//   - screenshots/*.png + screenshot-sha256.json  截图原始字节与哈希；
//   - screenshot-outcome.json  大页面截图的结论（受控缩小 or 结构化错误）；
//   - unsupported-surfaces.json  cross-origin / closed shadow 的声明与原因；
//   - trusted-only.json     后置条件失败但输入已发出的结论；
//   - counters.json         两个 origin 的服务端 counter 快照（唯一业务 oracle）。
//
// observation.json 里的每个事实都从这些文件**重新读出来算**（见各 finalize），
// 所以"测试里断言过"和"证据里写下的"是同一批数据。

import (
	"encoding/json"
	"strings"
	"testing"

	"my-agent-go/internal/browser"
	browserfixture "my-agent-go/tests/fixtures/browser"
)

// ---------- 写 artifact ----------

// observeB04Snapshot 落盘一份观察结果的原始 JSON。
func observeB04Snapshot(t *testing.T, name string, snap browser.Snapshot) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	wire, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("序列化快照: %v", err)
	}
	var back browser.Snapshot
	if err := json.Unmarshal(wire, &back); err != nil {
		t.Fatalf("快照 JSON 不可往返: %v", err)
	}
	rec := obsFacts{
		"snapshotId":        snap.ID,
		"navigationId":      snap.NavigationID,
		"truncated":         snap.Truncated,
		"elements":          len(snap.Elements),
		"textRunes":         len([]rune(snap.Text)),
		"jsonBytes":         len(wire),
		"unsupportedFrames": len(snap.UnsupportedFrames),
		"wire":              json.RawMessage(wire),
	}
	observationWrite(t, "snapshots/"+name+".json", mustMarshalIndent(t, rec))
}

// observeB04Limits 落盘三条限额的实测值与上限。
func observeB04Limits(t *testing.T, snap browser.Snapshot, jsonBytes int) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	wire, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("序列化快照: %v", err)
	}
	observationWrite(t, "limits.json", mustMarshalIndent(t, obsFacts{
		"maxElements":   browser.MaxSnapshotElements,
		"maxTextChars":  browser.MaxSnapshotTextChars,
		"maxJSONBytes":  browser.MaxSnapshotJSONBytes,
		"elements":      len(snap.Elements),
		"textRunes":     len([]rune(snap.Text)),
		"jsonBytes":     jsonBytes,
		"truncated":     snap.Truncated,
		"jsonValid":     json.Valid(wire),
		"elementsLimit": len(snap.Elements) <= browser.MaxSnapshotElements,
		"textLimit":     len([]rune(snap.Text)) <= browser.MaxSnapshotTextChars,
		"jsonLimit":     jsonBytes <= browser.MaxSnapshotJSONBytes,
	}))
}

// observeB04Action 把一次动作写进 actions.jsonl。
func observeB04Action(t *testing.T, rec obsFacts) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observationAppendJSONL(t, "actions.jsonl", rec)
}

// observeB04FormLast 落盘服务端收到的表单载荷。
func observeB04FormLast(t *testing.T, payload map[string]any) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observationWrite(t, "form-last.json", mustMarshalIndent(t, payload))
}

// observeB04StaleRejection 把一次陈旧引用拒绝写进 stale-rejections.jsonl。
func observeB04StaleRejection(t *testing.T, rec obsFacts) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observationAppendJSONL(t, "stale-rejections.jsonl", rec)
}

// observeB04PasswordScan 落盘密码泄漏扫描结果。
func observeB04PasswordScan(t *testing.T, facts obsFacts) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	total := 0.0
	for _, key := range []string{"snapshotOccurrences", "textOccurrences", "nameOccurrences", "logOccurrences"} {
		if v, ok := facts[key].(int); ok {
			total += float64(v)
			facts[key] = v
		}
	}
	facts["totalOccurrences"] = total
	observationWrite(t, "password-scan.json", mustMarshalIndent(t, facts))
}

// observeB04Screenshot 落盘一张截图与它的 sha256。
func observeB04Screenshot(t *testing.T, name string, img browser.Image, sha string) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observationWrite(t, "screenshots/"+name+".png", img.Bytes)
	observationUpdateSection(t, "screenshot-sha256.json", name, obsFacts{
		"sha256":      sha,
		"bytes":       len(img.Bytes),
		"pixelWidth":  img.PixelWidth,
		"pixelHeight": img.PixelHeight,
		"cssWidth":    img.CSSWidth,
		"cssHeight":   img.CSSHeight,
		"scale":       img.Scale,
		"mimeType":    img.MimeType,
	})
}

// observeB04ScreenshotOutcome 记录大页面截图的结论。
func observeB04ScreenshotOutcome(t *testing.T, facts obsFacts) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	section := facts.text(t, "outcome")
	observationUpdateSection(t, "screenshot-outcome.json", section, facts)
}

// observeB04Unsupported 落盘 Unsupported 表面的声明。
func observeB04Unsupported(t *testing.T, kind string, frames []browser.UnsupportedFrame) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	list := make([]any, 0, len(frames))
	for _, f := range frames {
		list = append(list, obsFacts{"frameId": f.FrameID, "url": f.URL, "reason": f.Reason})
	}
	observationUpdateSection(t, "unsupported-surfaces.json", kind, list)
}

// observeB04TrustedOutcome 记录"后置条件失败但输入已发出"的结论。
func observeB04TrustedOutcome(t *testing.T, facts obsFacts) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observationWrite(t, "trusted-only.json", mustMarshalIndent(t, facts))
}

// observeB04Counters 落盘两个 origin 的服务端计数器快照。
func observeB04Counters(t *testing.T, h *harness, section string) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observationUpdateSection(t, "counters.json", section, obsFacts{
		"primary":   h.fixture.Primary.Counters(),
		"secondary": h.fixture.Secondary.Counters(),
	})
}

// ---------- 读回并合并事实 ----------

// numOr 读一个数字字段，缺失时返回 0。
//
// 服务端计数器只记录"发生过"的名字（fixture 的 Counters() 不会为 0 的项占位），
// 所以"字段不存在"和"计数为 0"在证据里是同一件事。这里把这条口径写清楚，
// 而不是让 finalize 因为缺字段而失败。
func numOr(f obsFacts, key string) float64 {
	v, ok := f[key]
	if !ok {
		return 0
	}
	n, ok := v.(float64)
	if !ok {
		return 0
	}
	return n
}

// counterDeltaSum 按 "*Before"/"*After" 配对累加 counter 变化量。
//
// 记录形状不止一种（SPA 那条同时看 target 与 replaced 两个计数器），
// 所以按前缀配对求和，而不是硬编码字段名。
func counterDeltaSum(rec obsFacts) float64 {
	total := 0.0
	for key := range rec {
		if !strings.HasSuffix(key, "After") {
			continue
		}
		prefix := strings.TrimSuffix(key, "After")
		beforeKey := prefix + "Before"
		if _, ok := rec[beforeKey]; !ok {
			continue
		}
		total += numOr(rec, key) - numOr(rec, beforeKey)
	}
	return total
}

// finalizeB04LimitsObservation 从 limits.json 读回三条限额事实。
func finalizeB04LimitsObservation(t *testing.T) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	limits := map[string]any{}
	observationReadJSON(t, "limits.json", &limits)
	facts := obsFacts(limits)
	observationMergeFacts(t, obsFacts{
		"limitsMaxElements":  facts.num(t, "maxElements"),
		"limitsMaxTextChars": facts.num(t, "maxTextChars"),
		"limitsMaxJSONBytes": facts.num(t, "maxJSONBytes"),
		"limitsElements":     facts.num(t, "elements"),
		"limitsTextRunes":    facts.num(t, "textRunes"),
		"limitsJSONBytes":    facts.num(t, "jsonBytes"),
		"limitsTruncated":    facts.flag(t, "truncated"),
		"limitsJSONValid":    facts.flag(t, "jsonValid"),
		"limitsArtifact":     "limits.json",
		"limitsObservedBy": "limits.json 由 tests/native 在真实 WebView2 上观察巨型页面后写出：" +
			"元素数/文本字符数来自 browser.Snapshot，JSON 字节数来自 json.Marshal(snapshot)，" +
			"上限来自浏览器契约常量；jsonValid 由 json.Valid 在落盘时校验",
	})
}

// finalizeB04ActionObservation 从 actions.jsonl + form-last.json + counters.json 读回事实。
func finalizeB04ActionObservation(t *testing.T, h *harness) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observeB04Counters(t, h, "br07")
	actions := observationReadJSONL(t, "actions.jsonl")
	delivered, fills, clicks := 0, 0, 0
	for _, a := range actions {
		if a.flag(t, "delivered") {
			delivered++
		}
		kind := a.text(t, "test")
		if strings.HasPrefix(kind, "fill") {
			fills++
		}
		if strings.HasPrefix(kind, "click") {
			clicks++
		}
	}
	if delivered != len(actions) {
		t.Fatalf("actions.jsonl 里只有 %d/%d 条动作真的发出去了", delivered, len(actions))
	}
	payload := map[string]any{}
	observationReadJSON(t, "form-last.json", &payload)
	if payload["plan"] != "pro" || payload["confirm"] != true {
		t.Fatalf("服务端载荷不支持「select/checkbox 真的填进去了」: %v", payload)
	}
	counters := obsFacts{}
	observationReadJSON(t, "counters.json", &counters)
	br07 := counters.obj(t, "br07").obj(t, "primary")
	if got := numOr(br07, browserfixture.CounterFormSubmit); got != 1 {
		t.Fatalf("counters.json 里 form-submit=%v，期望恰好 1", got)
	}
	observationMergeFacts(t, obsFacts{
		"actions":             len(actions),
		"actionsDelivered":    delivered,
		"actionsFill":         fills,
		"actionsClick":        clicks,
		"formSubmitCounter":   br07.num(t, browserfixture.CounterFormSubmit),
		"formPayloadName":     payload["name"],
		"formPayloadPlan":     payload["plan"],
		"formPayloadConfirm":  payload["confirm"],
		"actionsArtifact":     "actions.jsonl",
		"countersArtifact":    "counters.json",
		"formPayloadArtifact": "form-last.json",
		"br07ObservedBy": "actions.jsonl 每行的 delivered/verified 来自 ActionResult；" +
			"counter 前后由 HTTP /__counters（fixture 服务端）读出；" +
			"form-last.json 是服务端自己记录的表单载荷（不是页面回显）",
	})
}

// finalizeB04StaleObservation 从 stale-rejections.jsonl + counters.json 读回事实。
func finalizeB04StaleObservation(t *testing.T, h *harness) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observeB04Counters(t, h, "br08")
	rejections := observationReadJSONL(t, "stale-rejections.jsonl")
	withDelta := 0
	for _, r := range rejections {
		code := r.text(t, "errorCode")
		if code != "STALE_SNAPSHOT" {
			t.Fatalf("stale-rejections.jsonl 里出现了非 STALE_SNAPSHOT 的错误码 %q", code)
		}
		if delta := counterDeltaSum(r); delta != 0 {
			withDelta++
		}
	}
	if withDelta != 0 {
		t.Fatalf("stale-rejections.jsonl 里有 %d 条拒绝伴随着 counter 变化（输入被发出去了）", withDelta)
	}
	if len(rejections) < 2 {
		t.Fatalf("stale-rejections.jsonl 只有 %d 条（同 URL 导航与 SPA 断开各需一条）", len(rejections))
	}
	observationMergeFacts(t, obsFacts{
		"staleRejections":    len(rejections),
		"staleCounterDeltas": withDelta,
		"staleArtifact":      "stale-rejections.jsonl",
		"staleObservedBy": "stale-rejections.jsonl 每行是一次旧 ref 动作的结论：" +
			"错误码来自 *contracts.AppError.Code，counter 前后由 HTTP /__counters（服务端）读出",
	})
}

// finalizeB04PasswordObservation 从 password-scan.json 读回泄漏计数。
func finalizeB04PasswordObservation(t *testing.T) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	scan := map[string]any{}
	observationReadJSON(t, "password-scan.json", &scan)
	facts := obsFacts(scan)
	total := facts.num(t, "totalOccurrences")
	if total != 0 {
		t.Fatalf("password-scan.json 记录到 %v 次密码值出现", total)
	}
	if !facts.flag(t, "filled") {
		t.Fatal("password-scan.json 里 filled=false：这次扫描没有证明密码真的被填进去过")
	}
	if facts.text(t, "passwordInputType") != "password" {
		t.Fatalf("密码字段的 inputType 是 %q", facts.text(t, "passwordInputType"))
	}
	observationMergeFacts(t, obsFacts{
		"passwordOccurrences": total,
		"passwordFilled":      facts.flag(t, "filled"),
		"passwordArtifact":    "password-scan.json",
		"passwordObservedBy": "password-scan.json 由 tests/native 统计密码值在快照 JSON、正文、" +
			"元素标签与宿主事件日志里的出现次数；filled 来自 field_value 后置条件的核验结果",
	})
}

// finalizeB04ScreenshotObservation 从 PNG + sha256 + outcome 读回事实。
func finalizeB04ScreenshotObservation(t *testing.T) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	png := readPNGFacts(t, "screenshots/br09-screen.png")
	if png.Bytes > browser.MaxScreenshotBytes {
		t.Fatalf("落盘截图 %d 字节超过 4MiB 上限", png.Bytes)
	}
	if png.Width <= 0 || png.Height <= 0 || png.DistinctColors <= 4 {
		t.Fatalf("落盘截图不像真实页面像素: %+v", png)
	}
	sha := obsFacts{}
	observationReadJSON(t, "screenshot-sha256.json", &sha)
	declared := sha.obj(t, "br09-screen")
	if declared.text(t, "sha256") != png.SHA256 {
		t.Fatalf("screenshot-sha256.json 记录的 sha256 与 PNG 实际 %s 不一致", png.SHA256)
	}
	outcomes := obsFacts{}
	observationReadJSON(t, "screenshot-outcome.json", &outcomes)
	keys := outcomes.keys()
	if len(keys) == 0 {
		t.Fatal("screenshot-outcome.json 里没有任何结论")
	}
	outcome := "unknown"
	for _, k := range keys {
		outcome = k
	}
	facts := obsFacts{
		"screenshotWidth":           png.Width,
		"screenshotHeight":          png.Height,
		"screenshotBytes":           png.Bytes,
		"screenshotSHA256":          png.SHA256,
		"screenshotColors":          png.DistinctColors,
		"screenshotHuge":            outcome,
		"screenshotArtifact":        "screenshots/br09-screen.png",
		"screenshotSHA256Artifact":  "screenshot-sha256.json",
		"screenshotOutcomeArtifact": "screenshot-outcome.json",
		"screenshotObservedBy": "screenshots/br09-screen.png 由 CDP Page.captureScreenshot 的原始字节写出，" +
			"尺寸/颜色数/sha256 从该文件读回；大页面结论来自 screenshot-outcome.json" +
			"（受控缩小后的有界图片，或结构化 UNSUPPORTED 错误）",
	}
	observationMergeFacts(t, facts)
}

// finalizeB04UnsupportedObservation 从 unsupported-surfaces.json + counters.json 读回事实。
func finalizeB04UnsupportedObservation(t *testing.T, h *harness) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observeB04Counters(t, h, "br10")
	surfaces := obsFacts{}
	observationReadJSON(t, "unsupported-surfaces.json", &surfaces)
	cross := surfaces.list(t, "crossOrigin")
	closed := surfaces.list(t, "closedShadow")
	crossOK, closedOK := 0, 0
	for _, v := range cross {
		f := obsFacts(v.(map[string]any))
		if strings.Contains(strings.ToLower(f.text(t, "reason")), "cross-origin") && f.text(t, "url") != "" {
			crossOK++
		}
	}
	for _, v := range closed {
		f := obsFacts(v.(map[string]any))
		if strings.Contains(strings.ToLower(f.text(t, "reason")), "closed shadow") {
			closedOK++
		}
	}
	if crossOK == 0 || closedOK == 0 {
		t.Fatalf("unsupported-surfaces.json 里的声明不成立: crossOrigin=%d closedShadow=%d", crossOK, closedOK)
	}
	counters := obsFacts{}
	observationReadJSON(t, "counters.json", &counters)
	br10 := counters.obj(t, "br10")
	if got := numOr(br10.obj(t, "secondary"), browserfixture.CounterFrameAction); got != 0 {
		t.Fatalf("cross-origin frame 内的计数器 %v，期望 0（没有输入穿过 frame）", got)
	}
	if got := numOr(br10.obj(t, "primary"), browserfixture.CounterShadowClosed); got != 0 {
		t.Fatalf("closed shadow root 内的计数器 %v，期望 0", got)
	}
	observationMergeFacts(t, obsFacts{
		"unsupportedCrossOrigin":  crossOK,
		"unsupportedClosedShadow": closedOK,
		"unsupportedArtifact":     "unsupported-surfaces.json",
		"unsupportedObservedBy": "unsupported-surfaces.json 的两组声明来自浏览器快照的 unsupportedFrames" +
			"（cross-origin 由 contentDocument 访问失败判定，closed shadow 由 CDP DOM.getDocument(pierce) " +
			"看到的 shadowRootType=closed 判定）；两侧计数器为 0 来自 HTTP /__counters",
	})
}

// finalizeB04TrustedObservation 从 trusted-only.json 读回结论。
func finalizeB04TrustedObservation(t *testing.T) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	facts := obsFacts{}
	observationReadJSON(t, "trusted-only.json", &facts)
	if !facts.flag(t, "firstClickVerified") {
		t.Fatal("trusted-only.json 里第一次点击没有核验通过：这条用例失去对照")
	}
	if facts.flag(t, "secondClickVerified") {
		t.Fatal("trusted-only.json 里第二次点击报了 verified=true（Delivered 冒充了成功）")
	}
	if facts.text(t, "secondClickStatus") != "failed" {
		t.Fatalf("trusted-only.json 里第二次点击的状态是 %q，期望 failed", facts.text(t, "secondClickStatus"))
	}
	if !facts.flag(t, "secondClickDelivered") {
		t.Fatal("trusted-only.json 里第二次点击没有如实报告 Delivered=true")
	}
	observationMergeFacts(t, obsFacts{
		"trustedPostConditionFailure": facts.text(t, "secondClickStatus"),
		"trustedDeliveredNotVerified": facts.flag(t, "secondClickDelivered") && !facts.flag(t, "secondClickVerified"),
		"trustedCounter":              facts.num(t, "trustedCounter"),
		"trustedRejectedCounter":      facts.num(t, "rejectedCounter"),
		"trustedArtifact":             "trusted-only.json",
		"trustedObservedBy": "trusted-only.json 由 tests/native 写出：第一次点击用可满足的 element_text " +
			"后置条件（证明核验通道有效），第二次用不成立的条件（证明失败时不会返回 Verified=true），" +
			"服务端计数来自 HTTP /__counters",
	})
}

func mustMarshalIndent(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("序列化观察值失败: %v", err)
	}
	return append(raw, 0x0a)
}
