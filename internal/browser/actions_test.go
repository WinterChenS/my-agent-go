package browser

// B04 的动作语义：fill 的控件分类、按键白名单、以及"输入已发出但核验不了"
// 该升级成 needs_human 而不是冒充成功（BR07/BR10）。

import (
	"errors"
	"testing"

	"my-agent-go/internal/contracts"
)

func TestClassifyControl(t *testing.T) {
	cases := []struct {
		tag      string
		input    string
		want     ControlKind
		fillable bool
		trusted  bool
	}{
		{"input", "text", ControlText, true, true},
		{"input", "", ControlText, true, true},
		{"input", "password", ControlText, true, true},
		{"input", "email", ControlText, true, true},
		{"input", "checkbox", ControlCheckbox, true, false},
		{"input", "radio", ControlRadio, true, false},
		{"input", "file", ControlNone, false, false},
		{"input", "submit", ControlNone, false, false},
		{"textarea", "", ControlTextArea, true, true},
		{"select", "", ControlSelect, true, false},
		{"div", "", ControlNone, false, false},
		{"button", "", ControlNone, false, false},
	}
	for _, c := range cases {
		got := ClassifyControl(c.tag, c.input)
		if got != c.want {
			t.Errorf("ClassifyControl(%q,%q)=%q，期望 %q", c.tag, c.input, got, c.want)
		}
		if got.Fillable() != c.fillable {
			t.Errorf("%q.Fillable()=%t，期望 %t", got, got.Fillable(), c.fillable)
		}
		if got.WantsTrustedInput() != c.trusted {
			t.Errorf("%q.WantsTrustedInput()=%t，期望 %t（select/checkbox 没有键盘等价路径）",
				got, got.WantsTrustedInput(), c.trusted)
		}
	}
}

func TestValidateKeyName(t *testing.T) {
	for _, name := range []string{"Enter", "Tab", "Escape", "Backspace", "ArrowDown", "Space"} {
		isText, err := ValidateKeyName(name)
		if err != nil {
			t.Errorf("具名键 %q 应被接受: %v", name, err)
		}
		if isText {
			t.Errorf("具名键 %q 不应被当成可打印文本", name)
		}
	}
	isText, err := ValidateKeyName("a")
	if err != nil || !isText {
		t.Fatalf("单字符键应作为文本派发: isText=%t err=%v", isText, err)
	}
	for _, bad := range []string{"", "F1", "Ctrl+A", "Enter\n"} {
		if _, err := ValidateKeyName(bad); err == nil {
			t.Errorf("键名 %q 应被拒绝（不假装支持任意组合键）", bad)
		}
	}
}

func TestInterpretBool(t *testing.T) {
	yes := []string{"true", "TRUE", "1", "yes", "on", "checked", " true "}
	for _, v := range yes {
		got, err := InterpretBool(v)
		if err != nil || !got {
			t.Errorf("InterpretBool(%q)=%t,%v 期望 true", v, got, err)
		}
	}
	no := []string{"false", "0", "no", "off", "unchecked"}
	for _, v := range no {
		got, err := InterpretBool(v)
		if err != nil || got {
			t.Errorf("InterpretBool(%q)=%t,%v 期望 false", v, got, err)
		}
	}
	// "maybe" 必须报错：当成 false 会让调用方以为设置成功了。
	if _, err := InterpretBool("maybe"); err == nil {
		t.Fatal("模糊取值必须被拒绝，而不是当成 false")
	}
}

func TestEscalateUnverifiableOnlyAfterDelivery(t *testing.T) {
	// 输入已经发出、但核验通道不可用 → 必须升级成 needs_human。
	delivered := EscalateUnverifiable(Verification{
		Condition: ConditionURLEquals,
		Status:    VerificationUnsupported,
		Detail:    "旧执行上下文已销毁",
	}, true)
	if delivered.Status != VerificationNeedsHuman {
		t.Fatalf("已发出的输入 + 无法核验应升级成 needs_human，实际 %q", delivered.Status)
	}
	if delivered.Detail == "" {
		t.Fatal("升级后必须保留原因")
	}

	// 没有送出输入时，"不支持核验"就是"不支持核验"，不能谎称需要人工。
	notDelivered := EscalateUnverifiable(Verification{Status: VerificationUnsupported}, false)
	if notDelivered.Status != VerificationUnsupported {
		t.Fatalf("没有输入时不应升级，实际 %q", notDelivered.Status)
	}

	// 已经观测到"不满足"的结果不能被升级掩盖成"需要人工"。
	failed := EscalateUnverifiable(Verification{Status: VerificationFailed}, true)
	if failed.Status != VerificationFailed {
		t.Fatalf("确定的失败不得被改写，实际 %q", failed.Status)
	}
}

func TestObscuredErrorIsStructuredAndSpecific(t *testing.T) {
	err := ObscuredError("nonce-e3", "div#overlay")
	var ae *contracts.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("必须是 *contracts.AppError，实际 %T", err)
	}
	if ae.Code != CodeTargetObscured {
		t.Fatalf("错误码 %q，期望 %q", ae.Code, CodeTargetObscured)
	}
	if ae.Retryable {
		t.Fatal("落点被占用不是可重试错误：重试会点到别人身上")
	}
	for _, want := range []string{"nonce-e3", "div#overlay"} {
		if !contains(ae.Message, want) {
			t.Fatalf("错误消息 %q 缺少关键信息 %q", ae.Message, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
