package browser

// B04 的限额与脱敏规则（BR09）。
//
// 这一层是纯函数，不需要 WebView2：真机上"页面很大"这件事只在 tests/native 里
// 复现，而"超过 300 个元素 / 40000 字符 / 256KiB 时到底裁剪什么、怎么标注"
// 在这里被逐条锁死，任何一个数字被顺手放宽都会先红。

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"my-agent-go/internal/contracts"
)

func TestTruncateTextCountsRunesNotBytes(t *testing.T) {
	// 40001 个中日韩字符：按字节算会被提前到 ~13333 字符，那正好是"中文页面
	// 在限额还没到时就被截断"的经典错误。
	long := strings.Repeat("文", MaxSnapshotTextChars+1)
	got, truncated := TruncateText(long, MaxSnapshotTextChars)
	if !truncated {
		t.Fatal("超限文本必须标注被裁剪")
	}
	if n := utf8.RuneCountInString(got); n != MaxSnapshotTextChars {
		t.Fatalf("裁剪后 %d 个字符，期望 %d", n, MaxSnapshotTextChars)
	}
	if !utf8.ValidString(got) {
		t.Fatal("裁剪不能切碎多字节字符")
	}

	short := strings.Repeat("a", 10)
	got, truncated = TruncateText(short, MaxSnapshotTextChars)
	if truncated || got != short {
		t.Fatalf("未超限文本不应被改动: truncated=%t got=%q", truncated, got)
	}
}

func TestScrubSecretsRemovesPasswordValues(t *testing.T) {
	const secret = "fixture-pass"
	in := "用户 fixture-user，密码 fixture-pass，重复 fixture-pass。"
	out, changed := ScrubSecrets(in, []string{secret})
	if !changed {
		t.Fatal("含敏感值的文本必须被标注改动")
	}
	if strings.Contains(out, secret) {
		t.Fatalf("脱敏后仍含敏感值: %q", out)
	}
	if !strings.Contains(out, RedactedPlaceholder) {
		t.Fatalf("脱敏必须留下占位符，而不是静默删掉: %q", out)
	}

	// 太短的"敏感串"不参与替换：否则常见单字会把整段正文抹掉。
	shortOut, shortChanged := ScrubSecrets("a 正常文本 a", []string{"a"})
	if shortChanged || shortOut != "a 正常文本 a" {
		t.Fatalf("短敏感串不应参与脱敏: changed=%t out=%q", shortChanged, shortOut)
	}
}

func TestElementIsSecretInput(t *testing.T) {
	if !(Element{Tag: "input", InputType: "password"}).IsSecretInput() {
		t.Fatal("type=password 必须判为敏感字段")
	}
	if (Element{Tag: "input", InputType: "PASSWORD"}).IsSecretInput() == false {
		t.Fatal("password 判定应忽略大小写")
	}
	if (Element{Tag: "input", InputType: "text"}).IsSecretInput() {
		t.Fatal("type=text 不是敏感字段")
	}
}

// makeHugeSnapshot 造一份"必然超 JSON 上限"的快照：300 个元素 + 4 万中日韩字符。
func makeHugeSnapshot() *Snapshot {
	elems := make([]Element, 0, MaxSnapshotElements)
	for i := 0; i < MaxSnapshotElements; i++ {
		elems = append(elems, Element{
			Ref:    "e" + itoa(i),
			Role:   "button",
			Name:   strings.Repeat("钮", 120),
			Tag:    "button",
			Bounds: Rect{X: float64(i), Y: float64(i), Width: 40, Height: 20},
		})
	}
	return &Snapshot{
		ID:           "snap-huge",
		TabID:        "tab-1",
		NavigationID: "nav-1",
		Viewport:     Viewport{Width: 800, Height: 600, Scale: 1},
		Elements:     elems,
		Text:         strings.Repeat("文", MaxSnapshotTextChars),
	}
}

func TestFitSnapshotJSONKeepsJSONValidAndBounds(t *testing.T) {
	snap := makeHugeSnapshot()
	size, err := snapshotJSONSize(snap)
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	if size <= MaxSnapshotJSONBytes {
		t.Fatalf("构造的快照只有 %d 字节，没有超过上限 %d：这条用例失去意义", size, MaxSnapshotJSONBytes)
	}

	fitted, truncated, err := FitSnapshotJSON(snap, MaxSnapshotJSONBytes)
	if err != nil {
		t.Fatalf("FitSnapshotJSON: %v", err)
	}
	if !truncated {
		t.Fatal("超限快照必须报告 truncated=true")
	}
	if fitted > MaxSnapshotJSONBytes {
		t.Fatalf("裁剪后仍有 %d 字节，超过上限 %d", fitted, MaxSnapshotJSONBytes)
	}
	if len(snap.Elements) == 0 {
		t.Fatal("裁剪不能把元素全部丢光（那样 agent 没有任何可操作目标）")
	}
	if strings.TrimSpace(snap.Text) == "" {
		t.Fatal("裁剪不能把文本丢光（保底 1000 字符）")
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("裁剪后的快照必须仍可序列化: %v", err)
	}
	if len(raw) != fitted {
		t.Fatalf("报告的字节数 %d 与实际 %d 不一致", fitted, len(raw))
	}
	var back Snapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("裁剪后的 JSON 必须完整可解析: %v", err)
	}
	// 裁剪后的快照必须仍然通过契约层的限额校验（它不是"超限但凑合返回"）。
	if err := back.CheckLimits(); err != nil {
		t.Fatalf("裁剪后的快照越界: %v", err)
	}
	if err := back.ValidateRefs(); err != nil {
		t.Fatalf("裁剪后的引用不合法: %v", err)
	}
}

func TestFitSnapshotJSONLeavesSmallSnapshotAlone(t *testing.T) {
	snap := &Snapshot{
		ID: "snap-small", TabID: "tab-1", NavigationID: "nav-1",
		Viewport: Viewport{Width: 800, Height: 600, Scale: 1},
		Elements: []Element{{Ref: "e1", Tag: "button", Name: "提交"}},
		Text:     "小页面",
	}
	before, err := snapshotJSONSize(snap)
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	size, truncated, err := FitSnapshotJSON(snap, MaxSnapshotJSONBytes)
	if err != nil {
		t.Fatalf("FitSnapshotJSON: %v", err)
	}
	if truncated {
		t.Fatal("未超限的快照不应被标成 truncated")
	}
	if size != before {
		t.Fatalf("未超限的快照不应被改动: before=%d after=%d", before, size)
	}
	if len(snap.Elements) != 1 || snap.Text != "小页面" {
		t.Fatalf("未超限的快照内容被改动: %+v", snap)
	}
}

func TestFitSnapshotJSONRefusesImpossibleBudget(t *testing.T) {
	snap := makeHugeSnapshot()
	// 200 字节连一个元素的骨架都放不下：必须明确报错，不能交出一个超限 JSON。
	_, _, err := FitSnapshotJSON(snap, 200)
	if err == nil {
		t.Fatal("不可能满足的上限必须报错")
	}
	if code := appErrCode(t, err); code != contracts.CodeInternal {
		t.Fatalf("错误码 %q，期望 %s", code, contracts.CodeInternal)
	}
}

func TestScreenshotLimitHelpers(t *testing.T) {
	if !ScreenshotFits(MaxScreenshotBytes) {
		t.Fatal("恰好 4MiB 的截图应在上限内（上限是闭区间）")
	}
	if ScreenshotFits(MaxScreenshotBytes + 1) {
		t.Fatal("超过 4MiB 的截图必须判为超限")
	}
	if ScreenshotFits(0) {
		t.Fatal("空截图不是有效截图")
	}

	factors := ScreenshotDownscaleFactors()
	if len(factors) == 0 {
		t.Fatal("受控缩小必须至少有一个档位")
	}
	prev := 1.0
	for _, f := range factors {
		if !(f > 0) || f >= 1 {
			t.Fatalf("缩小因子必须在 (0,1) 之间，实际 %v", f)
		}
		if f >= prev {
			t.Fatalf("缩小因子必须严格递减：%v 之后出现 %v", prev, f)
		}
		prev = f
	}

	// 缩小后 scale 必须以实际像素为准：800/1600 = 0.5，而不是请求的 0.5 恰好相等时
	// 也要能容忍取整误差。
	if got := DerivedScale(800, 1600, 0.499); got != 0.5 {
		t.Fatalf("DerivedScale(800,1600)=%v，期望 0.5", got)
	}
	if got := DerivedScale(0, 1600, 0.75); got != 0.75 {
		t.Fatalf("没有像素宽度时应回退到候选因子，实际 %v", got)
	}
	if got := DerivedScale(0, 0, 0); got != 1 {
		t.Fatalf("完全没有信息时 scale 应回退为 1，实际 %v", got)
	}
}
