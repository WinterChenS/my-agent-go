package browser

// 动作语义中与平台无关的那一半：控件分类、按键校验、命中/核验结论的升级规则。
//
// native 只负责"把决定好的动作发出去"，决定本身放在这里，好处是没有 WebView2
// 的机器也能单元测试这些规则，且三平台（B01–B03）不会各自解释一遍。

import (
	"fmt"
	"strings"

	"my-agent-go/internal/contracts"
)

// CodeTargetObscured 表示目标元素存在，但点击/输入的落点被别的元素占用。
//
// 与 STALE_SNAPSHOT 的区别很重要：快照没过期，元素也还在，只是"点了会点到
// 别人身上"。用 UNSUPPORTED 表达会误导成"平台不支持"，用 VERIFICATION_FAILED
// 又会让人以为输入已经发出去了。所以记一个独立码；
// B05 若要收进 contracts，必须保持字符串值不变（前端按 Code 分支）。
const CodeTargetObscured = "TARGET_OBSCURED"

// ControlKind 是 fill 需要区分的控件族。
//
// fill 的语义是"把这个字段的值设成 Text"：文本类走可信输入，select/checkbox
// 这类没有键盘等价路径的控件走 DOM 赋值 + 事件派发，并在证据里分开标注。
// 把它显式分类，是为了让"我们用哪种方式写入"在代码里可审，而不是散在 JS 里。
type ControlKind string

// 控件族。ControlNone 表示 fill 不适用于该元素（例如 button/img）。
const (
	ControlText            ControlKind = "text"
	ControlTextArea        ControlKind = "textarea"
	ControlSelect          ControlKind = "select"
	ControlCheckbox        ControlKind = "checkbox"
	ControlRadio           ControlKind = "radio"
	ControlContentEditable ControlKind = "contenteditable"
	ControlNone            ControlKind = "none"
)

// AllControlKinds 是 ControlKind 的完整集合。
var AllControlKinds = []ControlKind{
	ControlText, ControlTextArea, ControlSelect, ControlCheckbox,
	ControlRadio, ControlContentEditable, ControlNone,
}

// ClassifyControl 按标签名与 input type 判定 fill 的处理方式。
func ClassifyControl(tag, inputType string) ControlKind {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "textarea":
		return ControlTextArea
	case "select":
		return ControlSelect
	case "input":
		switch strings.ToLower(strings.TrimSpace(inputType)) {
		case "checkbox":
			return ControlCheckbox
		case "radio":
			return ControlRadio
		case "password", "text", "email", "tel", "url", "search", "number", "date", "time", "color", "range", "":
			return ControlText
		default:
			return ControlNone
		}
	}
	return ControlNone
}

// Fillable 报告该控件是否支持 fill。
func (k ControlKind) Fillable() bool {
	return k != ControlNone && k != ""
}

// WantsTrustedInput 报告该控件是否可以用可信输入（CDP Input.insertText）写入。
//
// select/checkbox/radio 没有键盘等价路径：把它们的值改掉必然走 DOM + 事件，
// 所以"可信输入"这件事必须如实标注，而不是笼统写成"已用可信输入填写"。
func (k ControlKind) WantsTrustedInput() bool {
	return k == ControlText || k == ControlTextArea || k == ControlContentEditable
}

// InterpretBool 把 fill 的文本解释成 checkbox/radio 的目标勾选状态。
//
// 只接受明确的真/假词：把 "maybe" 当成 false 会让调用方以为设置成功了。
func InterpretBool(text string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "true", "1", "yes", "on", "checked":
		return true, nil
	case "false", "0", "no", "off", "unchecked":
		return false, nil
	}
	return false, appErr(contracts.CodeUnsupported, fmt.Sprintf(
		"checkbox/radio 的 fill 只接受 true/false 之类的取值，收到 %q", text))
}

// 允许的具名按键。
//
// 只放"有明确语义、且 CDP 能可靠表达"的键：任意字符串会被当成单字符处理，
// 更复杂的组合键留给后续任务，不在这里假装支持。
var namedKeys = map[string]struct{}{
	"Enter": {}, "Tab": {}, "Escape": {}, "Backspace": {}, "Delete": {},
	"ArrowUp": {}, "ArrowDown": {}, "ArrowLeft": {}, "ArrowRight": {},
	"Home": {}, "End": {}, "PageUp": {}, "PageDown": {}, "Space": {},
}

// ValidateKeyName 校验 key 动作的键名。
//
// 返回的 isText 表示"这是一个可打印字符，要带 text 一起派发"。
func ValidateKeyName(key string) (isText bool, err error) {
	if key == "" {
		return false, appErr(contracts.CodeUnsupported, "key 动作需要键名")
	}
	if _, ok := namedKeys[key]; ok {
		return false, nil
	}
	if utf8Len := len([]rune(key)); utf8Len == 1 {
		return true, nil
	}
	return false, appErr(contracts.CodeUnsupported, fmt.Sprintf(
		"不支持的键名 %q：只接受单字符或 Enter/Tab/Escape/Backspace/Delete/方向键/Home/End/PageUp/PageDown/Space", key))
}

// EscalateUnverifiable 把"输入已发出、但核验通道不可用"升级成 needs_human。
//
// BR10 的口径：Delivered 不能冒充 Verified；同时"核验不了"也不等于"不支持这个
// 站点"——输入可能已经产生了副作用，必须有人看一眼。所以 delivered 为真时，
// unsupported 的核验结论升级成 needs_human 并保留原因。
func EscalateUnverifiable(v Verification, delivered bool) Verification {
	if !delivered {
		return v
	}
	if v.Status != VerificationUnsupported {
		return v
	}
	v.Status = VerificationNeedsHuman
	if v.Detail == "" {
		v.Detail = "输入已发出但无法核验，需要人工确认结果"
	} else {
		v.Detail = "输入已发出但无法核验，需要人工确认结果：" + v.Detail
	}
	return v
}

// ObscuredError 构造"落点被别的元素占用"的结构化错误。
func ObscuredError(ref, hit string) error {
	if hit == "" {
		hit = "未知元素"
	}
	return appErr(CodeTargetObscured, fmt.Sprintf(
		"ref %q 的落点被 %s 占用：点击会命中的不是目标元素，因此没有发出输入（不按坐标绕过）", ref, hit))
}
