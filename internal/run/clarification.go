// C06f：内容澄清（clarification）桥。
//
// 澄清不是审批。审批回答"这次调用允不允许执行"，会留下 approval/许可；澄清回答
// "用户要的内容是什么"，是模型执行中的一个数据依赖。两者混用会带来两个真实故障：
//   - 用户回答一次内容问题，界面上多出一条"已授权"记录，权限台账从此不可读；
//   - 审批的 TTL/断线即拒语义把内容问题也一起杀掉，用户只是没来得及选，
//     整轮却按"拒绝执行"结束。
//
// 所以本文件按计划 C06「内容澄清」把两件事彻底分开：
//   - Pi 的 `extension_ui_request` → 类型化事件 `clarification.requested`；
//   - `AnswerClarification` → 独立的 `extension_ui_response`；
//   - 回答路径**从不**调用 policy：不创建 approval、不写 session grant、
//     不产生任何工具/站点许可（这条由测试钉住）。
//
// 身份而不是名字：只有产品自带、随版本固定的澄清扩展发出的 UI 请求才被当成澄清。
// 判据是两把钥匙——
//  1. UI 请求标题里的 per-run 令牌前缀（Go 生成、只经子进程环境交付）；
//  2. 策略扩展报告的工具体身份（`pi.getAllTools()` 的 sourceInfo.path）与产品扩展
//     路径一致。
//
// 第三方工具哪怕也叫 `anvil_ask_user`，两把钥匙都拿不到：它的 UI 请求会被
// fail-closed 回答成 cancelled（只发一条诊断），它的工具调用照常走 policy。
package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/runtime/pi"
)

// 默认值。
const (
	// defaultClarificationTimeout 是等待用户回答澄清的上限。
	//
	// 澄清是"人此刻在场"的对话，但比审批更轻：超时后按 cancelled 收口，
	// 让模型知道"没拿到答案"，而不是一直挂着等。
	defaultClarificationTimeout = 60 * time.Second
	// clarificationToolName 是产品自带的唯一澄清工具名。它是 catalog 身份的一半，
	// 另一半是扩展路径——只有两者同时对上才算"本地无副作用澄清"。
	clarificationToolName = "anvil_ask_user"
	// clarificationTagPrefix/-Suffix 与 pi-extensions/anvil-clarification.ts 一致。
	clarificationTagPrefix = "[[anvil-clarification:"
	clarificationTagSuffix = "]] "
)

// ClarificationOptions 描述这次 run 的澄清桥配置（由绑定层装配，产品级）。
type ClarificationOptions struct {
	// Enabled 表示产品自带的澄清扩展已随本次 run 加载。
	// false 时：不建立澄清桥，也不把任何工具当成"本地无副作用澄清"。
	Enabled bool
	// ExtensionPath 是澄清扩展的绝对路径（产品自带、随隔离参数 -e 加载）。
	// 它是身份判据，不是"按名字"的判据。
	ExtensionPath string
	// Timeout 是等待回答的上限，默认 60 秒。
	Timeout time.Duration
}

// normalize 填默认值。
func (o ClarificationOptions) normalize() ClarificationOptions {
	if o.Timeout <= 0 {
		o.Timeout = defaultClarificationTimeout
	}
	o.ExtensionPath = strings.TrimSpace(o.ExtensionPath)
	if o.ExtensionPath == "" {
		o.Enabled = false
	}
	return o
}

// MatchesToolCall 报告一次工具调用是不是**产品自带的**澄清工具。
//
// 两个判据缺一不可：工具名必须是固定的那个（Pi 里工具名唯一），并且加载它的
// 扩展路径必须等于产品自带的澄清扩展路径。第三方扩展注册同名工具时，路径不同，
// 于是照常走 policy（默认拒绝/需要审批那条路），不能靠同名绕过。
func (o ClarificationOptions) MatchesToolCall(call policy.ToolCall, identity string) bool {
	if !o.Enabled {
		return false
	}
	if call.Name != clarificationToolName {
		return false
	}
	if call.Source != "" && call.Source != policy.SourceExtension {
		return false
	}
	got := strings.TrimSpace(identity)
	if got == "" || o.ExtensionPath == "" {
		return false
	}
	return samePath(o.ExtensionPath, got)
}

// samePath 按平台语义比较两个路径（Windows 大小写不敏感）。
func samePath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if strings.EqualFold(cleanSlash(a), cleanSlash(b)) {
		return true
	}
	return cleanSlash(a) == cleanSlash(b)
}

func cleanSlash(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return strings.TrimSuffix(p, "/")
}

// ClarificationValue 是 value 分支：用户给出的文本/选项。
type ClarificationValue struct {
	Text string `json:"text"`
}

// ClarificationResponse 是 AnswerClarification 的 tagged union。
//
// 三个分支互斥：
//   - Value：input / select / editor 的回答；
//   - Confirmed：confirm 的是/否；
//   - Cancelled：用户取消（或由超时/run 终止代答）。
//
// 用三个指针而不是"一个字符串 + 一个标志"：分支缺失与分支写错必须能区分，
// 否则"没给答案"会被读成"给了空答案"。
type ClarificationResponse struct {
	Value     *ClarificationValue `json:"value,omitempty"`
	Confirmed *bool               `json:"confirmed,omitempty"`
	Cancelled *bool               `json:"cancelled,omitempty"`
}

// clarificationOutcome 是校验后的分支名。
type clarificationOutcome string

const (
	clarificationOutcomeValue     clarificationOutcome = "value"
	clarificationOutcomeConfirmed clarificationOutcome = "confirmed"
	clarificationOutcomeCancelled clarificationOutcome = "cancelled"
)

// branch 校验"有且只有一个分支"，并返回分支名。
func (r ClarificationResponse) branch() (clarificationOutcome, error) {
	set := 0
	out := clarificationOutcome("")
	if r.Value != nil {
		set++
		out = clarificationOutcomeValue
	}
	if r.Confirmed != nil {
		set++
		out = clarificationOutcomeConfirmed
	}
	if r.Cancelled != nil {
		set++
		out = clarificationOutcomeCancelled
	}
	if set != 1 {
		return "", contracts.NewAppError(contracts.CodeUnsupported,
			"澄清答复必须且只能给出一个分支（value / confirmed / cancelled）", false)
	}
	if r.Cancelled != nil && !*r.Cancelled {
		return "", contracts.NewAppError(contracts.CodeUnsupported,
			"cancelled 只接受 true；要给出内容请用 value 或 confirmed", false)
	}
	return out, nil
}

// isEmpty 报告"这个分支没有内容"的情况（value 文本为空且非取消）。
func (r ClarificationResponse) isEmpty() bool {
	return r.Value != nil && strings.TrimSpace(r.Value.Text) == ""
}

// pendingClarification 是一条等待回答的澄清请求。
type pendingClarification struct {
	id       string
	method   string
	kind     string
	question string
	options  []string
	seq      uint64
}

func (p pendingClarification) payload() map[string]any {
	out := map[string]any{
		"requestId": p.id,
		"kind":      p.kind,
		"question":  p.question,
	}
	if len(p.options) > 0 {
		// 没有选项时不要给一个空数组：界面据此区分"选择题"和"没有选项"。
		out["options"] = p.options
	}
	return out
}

// newClarificationToken 生成本次 run 的澄清身份令牌。
//
// 32 字节来自 crypto/rand：这枚值是对端（澄清扩展）能证明"这次 UI 请求属于
// 产品自带的澄清工具"的东西，可预测就等于没有。
func newClarificationToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "生成澄清令牌失败: "+err.Error(), false)
	}
	return hex.EncodeToString(buf), nil
}

// clarificationTag 是本次 run 的标题前缀。
func clarificationTag(token string) string {
	if token == "" {
		return ""
	}
	return clarificationTagPrefix + token + clarificationTagSuffix
}

// parseClarificationTitle 从 UI 请求标题里取回问题并校验身份令牌。//
// 返回 ok=false 表示"这不是产品自带澄清扩展发出的请求"——调用方必须 fail closed
// （回答 cancelled + 诊断），不得把它当成澄清事件推给前端。
func parseClarificationTitle(title, token string) (question string, ok bool) {
	if token == "" {
		return "", false
	}
	want := clarificationTag(token)
	if !strings.HasPrefix(title, want) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(title, want)), true
}

// dialogMethods 是 Pi 在 RPC 模式下需要答复的扩展 UI 方法。
var dialogMethods = map[string]bool{"select": true, "confirm": true, "input": true, "editor": true}

// handleExtensionUIRequest 处理一条 Pi 的 extension_ui_request。
//
// 三类行为：
//  1. fire-and-forget（notify/setStatus/...）：不需要答复，也不产生澄清事件；
//  2. 产品澄清扩展的 dialog 请求：建 pending + 发 clarification.requested；
//  3. 其它来源的 dialog 请求：fail closed 回答 cancelled + 诊断。
//     不该由产品代答，也不能让它把 Pi 挂住。
func (s *Service) handleExtensionUIRequest(rs *runState, ev pi.RawEvent) {
	var req struct {
		ID      string   `json:"id"`
		Method  string   `json:"method"`
		Title   string   `json:"title"`
		Message string   `json:"message"`
		Options []string `json:"options"`
	}
	if err := json.Unmarshal(ev.Record, &req); err != nil {
		s.emit(rs, EventDiagnostic, map[string]any{
			"kind":    "clarification.malformed_ui_request",
			"message": "无法解析 extension_ui_request: " + err.Error(),
		})
		return
	}
	method := strings.TrimSpace(req.Method)
	if !dialogMethods[method] {
		// notify / setStatus / setWidget / setTitle / set_editor_text：不需要答复。
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		s.emit(rs, EventDiagnostic, map[string]any{
			"kind":    "clarification.missing_request_id",
			"message": "extension_ui_request 缺少 id，无法答复（fail closed：不代答）",
		})
		return
	}

	rs.mu.Lock()
	opts := rs.clarificationOpts
	token := rs.clarificationToken
	rs.mu.Unlock()

	question, ok := parseClarificationTitle(req.Title, token)
	if !ok {
		// 第三方的 dialog 请求：不建澄清、不猜答案，直接取消。
		s.emit(rs, EventDiagnostic, map[string]any{
			"kind": "clarification.foreign_ui_request",
			"message": fmt.Sprintf(
				"扩展 UI 请求（method=%s）不带产品澄清身份令牌，已按 cancelled 收口，不当作澄清", method),
		})
		s.respondExtensionUI(rs, id, map[string]any{"cancelled": true})
		return
	}
	if strings.TrimSpace(question) == "" && strings.TrimSpace(req.Message) != "" {
		// confirm 的正文可能在 message 里；标题只承担身份与副标题。
		question = strings.TrimSpace(req.Message)
	}
	pending := &pendingClarification{
		id:       id,
		method:   method,
		kind:     method,
		question: question,
		options:  append([]string{}, req.Options...),
	}
	rs.mu.Lock()
	if rs.clarifications == nil {
		rs.clarifications = map[string]*pendingClarification{}
	}
	rs.clarifications[id] = pending
	rs.mu.Unlock()

	s.emit(rs, EventClarificationRequested, pending.payload())

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultClarificationTimeout
	}
	time.AfterFunc(timeout, func() { s.expireClarification(rs, id) })
}

// expireClarification 在超时后把一条仍未回答的澄清收口为 cancelled。
func (s *Service) expireClarification(rs *runState, id string) {
	p, ok := s.takePendingClarification(rs, id)
	if !ok {
		return // 已经答复或已随 run 终止清掉
	}
	s.respondExtensionUI(rs, id, map[string]any{"cancelled": true})
	s.emit(rs, EventClarificationResolved, map[string]any{
		"requestId": p.id,
		"outcome":   string(clarificationOutcomeCancelled),
		"reason":    "timeout",
		"kind":      p.kind,
	})
}

// takePendingClarification 原子地取出并删除一条 pending（没有则 ok=false）。
func (s *Service) takePendingClarification(rs *runState, id string) (pendingClarification, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	p, ok := rs.clarifications[id]
	if !ok {
		return pendingClarification{}, false
	}
	delete(rs.clarifications, id)
	return *p, true
}

// cancelPendingClarifications 在 run 终止时把所有未回答的澄清收口为 cancelled。
//
// 必须发一条答复（而不是只清内存）：Pi 侧的 dialog 还在等，不发就会挂住整轮。
// 发失败也照样清掉并留诊断——run 已经结束，不能让 pending 永远指向一个死请求。
func (s *Service) cancelPendingClarifications(rs *runState, reason string) {
	rs.mu.Lock()
	ids := make([]string, 0, len(rs.clarifications))
	for id := range rs.clarifications {
		ids = append(ids, id)
	}
	rs.clarifications = map[string]*pendingClarification{}
	rs.mu.Unlock()

	for _, id := range ids {
		s.respondExtensionUI(rs, id, map[string]any{"cancelled": true})
		s.emit(rs, EventClarificationResolved, map[string]any{
			"requestId": id,
			"outcome":   string(clarificationOutcomeCancelled),
			"reason":    reason,
		})
	}
}

// respondExtensionUI 把一条 extension_ui_response 交给 worker。
//
// 失败只记诊断：调用方（超时/终止路径）已经决定了结论，不能让答复失败
// 反过来改变 run 的终态。
func (s *Service) respondExtensionUI(rs *runState, requestID string, body map[string]any) {
	rs.mu.Lock()
	w := rs.worker
	rs.mu.Unlock()
	if w == nil {
		return
	}
	responder, ok := w.(pi.UIResponder)
	if !ok {
		s.emit(rs, EventDiagnostic, map[string]any{
			"kind":    "clarification.respond_unsupported",
			"message": "当前 worker 不支持 extension UI 答复，澄清只能按 cancelled 收口",
		})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := responder.RespondExtensionUI(ctx, requestID, body); err != nil {
		s.emit(rs, EventDiagnostic, map[string]any{
			"kind":    "clarification.respond_failed",
			"message": fmt.Sprintf("答复澄清请求 %s 失败：%v", requestID, err),
		})
	}
}

// AnswerClarification 提交一次澄清答复。
//
// 按 (sessionID, requestID) 定位：requestID 是 Pi 生成的 UUID，跨 run 唯一；
// 但会话必须一起校验——否则一个会话可以替另一个会话回答内容问题。
//
// 这里**不**调用 policy 的任何入口：不签发审批、不写 session grant、
// 不产生工具/站点许可（C06 的明确要求）。
func (s *Service) AnswerClarification(ctx context.Context, sessionID contracts.SessionID, requestID string, resp ClarificationResponse) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := strings.TrimSpace(requestID)
	if id == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "缺少澄清请求 ID", false)
	}
	outcome, err := resp.branch()
	if err != nil {
		return err
	}
	rs, pending, err := s.findPendingClarification(sessionID, id)
	if err != nil {
		return err
	}
	if outcome != clarificationOutcomeCancelled && resp.isEmpty() {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"澄清答复的 value.text 为空；要表示没有答案请显式使用 cancelled", false)
	}
	if err := responseMatchesKind(outcome, pending.kind); err != nil {
		return err
	}

	body := map[string]any{}
	switch outcome {
	case clarificationOutcomeValue:
		body["value"] = resp.Value.Text
	case clarificationOutcomeConfirmed:
		body["confirmed"] = *resp.Confirmed
	case clarificationOutcomeCancelled:
		body["cancelled"] = true
	}

	// 先发答复再删 pending：发失败时用户还能重试，pending 不会被悄悄丢掉。
	if err := s.respondExtensionUISync(ctx, rs, id, body); err != nil {
		return err
	}
	if _, ok := s.takePendingClarification(rs, id); !ok {
		// 并发：超时路径刚好收口了这条请求。答复已经发出，这里只记录事实。
		return contracts.NewAppError(contracts.CodeConfigConflict,
			"澄清请求已由超时/终止路径收口，本次答复未生效", false)
	}
	payload := map[string]any{
		"requestId": id,
		"outcome":   string(outcome),
		"kind":      pending.kind,
	}
	switch outcome {
	case clarificationOutcomeValue:
		payload["value"] = resp.Value.Text
	case clarificationOutcomeConfirmed:
		payload["confirmed"] = *resp.Confirmed
	}
	s.emit(rs, EventClarificationResolved, payload)
	return nil
}

// respondExtensionUISync 与 respondExtensionUI 相同，但把失败返回给调用方
// （答复路径需要知道"这次答复到底有没有送到 Pi"）。
func (s *Service) respondExtensionUISync(ctx context.Context, rs *runState, requestID string, body map[string]any) error {
	rs.mu.Lock()
	w := rs.worker
	rs.mu.Unlock()
	if w == nil {
		return contracts.NewAppError(contracts.CodeInternal, "该 run 没有可用的 worker，无法答复澄清", false)
	}
	responder, ok := w.(pi.UIResponder)
	if !ok {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"当前 worker 不支持 extension UI 答复（协议能力缺失）", false)
	}
	if err := responder.RespondExtensionUI(ctx, requestID, body); err != nil {
		return contracts.NewAppError(contracts.CodeRpcWriteFailed, "答复澄清失败: "+err.Error(), false)
	}
	return nil
}

// findPendingClarification 在指定会话里找一条 pending 澄清。
func (s *Service) findPendingClarification(sessionID contracts.SessionID, requestID string) (*runState, pendingClarification, error) {
	want := strings.TrimSpace(string(sessionID))
	if want == "" {
		return nil, pendingClarification{}, contracts.NewAppError(contracts.CodeConfigMissing,
			"缺少会话 ID，无法答复澄清", false)
	}
	s.mu.Lock()
	runs := make([]*runState, 0, len(s.runs))
	for _, rs := range s.runs {
		runs = append(runs, rs)
	}
	s.mu.Unlock()

	for _, rs := range runs {
		if string(rs.snapshot().SessionID) != want {
			continue
		}
		rs.mu.Lock()
		p, ok := rs.clarifications[requestID]
		rs.mu.Unlock()
		if ok {
			return rs, *p, nil
		}
	}
	return nil, pendingClarification{}, contracts.NewAppError(contracts.CodeNotFound,
		"这个会话没有待回答的澄清请求: "+requestID+"（可能已超时或已被回答）", false)
}

// responseMatchesKind 拒绝"分支与提问类型不匹配"的答复。
//
// select/input/editor 要文本，confirm 要是/否。放行错配会让模型收到一个它没法
// 解释的答案（例如把 true 当成用户选中的选项），比直接拒绝更糟。
func responseMatchesKind(outcome clarificationOutcome, kind string) error {
	if outcome == clarificationOutcomeCancelled {
		return nil
	}
	switch kind {
	case "confirm":
		if outcome != clarificationOutcomeConfirmed {
			return contracts.NewAppError(contracts.CodeUnsupported,
				"这次澄清是是/否确认，答复必须用 confirmed 分支", false)
		}
	case "select", "input", "editor":
		if outcome != clarificationOutcomeValue {
			return contracts.NewAppError(contracts.CodeUnsupported,
				"这次澄清需要文本/选项答案，答复必须用 value 分支", false)
		}
	default:
		return contracts.NewAppError(contracts.CodeUnsupported, "未知的澄清类型: "+kind, false)
	}
	return nil
}
