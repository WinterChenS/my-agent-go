// Package browser 定义"同一主窗口右侧真实网页"（基线 R06）的契约层。
//
// 本包只有类型、常量、校验和明确的"未实现"占位实现。真正的原生宿主放在
// internal/browser/native（B01 Windows WebView2、B02 macOS WKWebView、
// B03 Linux GTK/WebKitGTK）。B00 阶段一个原生实现都没有：调用方拿到的是
// NewUnimplementedBackend / NewUnimplementedSurface，每个方法都返回
// NOT_IMPLEMENTED。这是刻意的——"右侧还没有原生视图"必须是一段可复现的失败，
// 不能被零值结果或 iframe 冒充成功。
//
// 边界：
//   - 远程网页拿不到 Wails bridge，也不认识本包的任何类型；
//   - NativeParent 是唯一持有原生句柄的类型，它不能序列化（见 surface.go）；
//   - 元素引用（Element.Ref）只在同一个 Snapshot 内有效，导航即失效。
package browser

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"my-agent-go/internal/contracts"
)

// TabID 复用 contracts 的领域标识，避免同一个 tab 在两处出现两套类型。
type TabID = contracts.TabID

// 基线 §7 的浏览器固定值。
//
// 这些是产品契约，不是调参旋钮：改任何一个数字都等于改验收口径，需要先走 ADR。
// TestBaselineLimits 逐条锁定，schema 里的上限也由同一批常量交叉校验。
const (
	// MaxSnapshotElements 是单次 snapshot 返回的元素上限。
	MaxSnapshotElements = 300
	// MaxSnapshotTextChars 是 snapshot 文本上限，按 Unicode 字符（rune）计，
	// 不是字节数——中文页面按字节计会提前一半截断。
	MaxSnapshotTextChars = 40000
	// MaxSnapshotJSONBytes 是 snapshot 序列化后的 JSON 字节上限。
	MaxSnapshotJSONBytes = 256 * 1024
	// MaxScreenshotBytes 是单张截图的字节上限。
	MaxScreenshotBytes = 4 * 1024 * 1024
	// MaxTabsPerSession 是单个 session 的活跃 tab 上限。
	MaxTabsPerSession = 4
	// MaxTabsTotal 是全部 session 合计的活跃 tab 上限。
	MaxTabsTotal = 12
	// ActionTimeout 是单次浏览器动作（click/fill/key/scroll）的超时。
	// 超时后不得自动重放（基线 §7 的 OutcomeUnknown 语义）。
	ActionTimeout = 15 * time.Second
)

// 浏览器专用错误码。
//
// contracts 已登记 UNSUPPORTED / STALE_SNAPSHOT / VERIFICATION_FAILED /
// NEEDS_HUMAN / OUTCOME_UNKNOWN / CONTROL_UNCERTAIN，本包直接用那些；
// 下面两个 contracts 尚未登记，先在本包定义，B05 若要收进 contracts 必须保持
// 字符串值不变（前端按 Code 分支，改值等于改协议）。
const (
	// CodeNotImplemented 表示该能力在当前版本没有任何实现。
	// 与 UNSUPPORTED 的区别：UNSUPPORTED 是"这个平台/站点不支持"，
	// NOT_IMPLEMENTED 是"我们还没写"——两者都不能被当成成功。
	CodeNotImplemented = "NOT_IMPLEMENTED"
	// CodeTabLimitExceeded 表示触及 4/session 或 12/total 的 tab 限额。
	CodeTabLimitExceeded = "TAB_LIMIT_EXCEEDED"
)

// 允许的期望条件种类。
//
// 只允许有界检查。任意 JS 求值（evaluate/script/expression）刻意不在列表里：
// 那等于把"验证"变成执行面，BR10 的"无隐式绕过"就无从检查。
const (
	ConditionURLEquals      ConditionKind = "url_equals"
	ConditionElementText    ConditionKind = "element_text"
	ConditionFieldValue     ConditionKind = "field_value"
	ConditionElementPresent ConditionKind = "element_present"
)

// 动作种类，与计划里的工具一一对应（click / fill / key / scroll）。
const (
	ActionClick  ActionKind = "click"
	ActionFill   ActionKind = "fill"
	ActionKey    ActionKind = "key"
	ActionScroll ActionKind = "scroll"
)

// 后置条件核验结果。
//
// 关键语义：unknown 是默认值。没有后置条件、条件还没核验、或核验通道不支持时，
// 只能是 unknown，不能因为"没有报错"就写成 verified（BR10）。
const (
	// VerificationUnknown：没有后置条件，或结果未知。动作可能已经发生。
	VerificationUnknown VerificationStatus = "unknown"
	// VerificationVerified：后置条件被真实观测通过。
	VerificationVerified VerificationStatus = "verified"
	// VerificationFailed：后置条件被观测为不成立。
	VerificationFailed VerificationStatus = "failed"
	// VerificationUnsupported：该页面/平台无法核验（例如跨域 frame）。
	VerificationUnsupported VerificationStatus = "unsupported"
	// VerificationNeedsHuman：需要人工确认，不做自动判定。
	VerificationNeedsHuman VerificationStatus = "needs_human"
)

// AllActionKinds 是 ActionKind 的完整集合，校验与 schema 一致性测试共用。
var AllActionKinds = []ActionKind{ActionClick, ActionFill, ActionKey, ActionScroll}

// AllConditionKinds 是 ConditionKind 的完整集合（即允许的全部后置条件）。
var AllConditionKinds = []ConditionKind{
	ConditionURLEquals, ConditionElementText, ConditionFieldValue, ConditionElementPresent,
}

// AllVerificationStatuses 是 VerificationStatus 的完整集合。
var AllVerificationStatuses = []VerificationStatus{
	VerificationUnknown, VerificationVerified, VerificationFailed,
	VerificationUnsupported, VerificationNeedsHuman,
}

// ActionKind 是动作种类。
type ActionKind string

// ConditionKind 是有界后置条件的种类。
type ConditionKind string

// VerificationStatus 是后置条件的核验状态。
type VerificationStatus string

// Rect 是元素在页面视口坐标系里的矩形，单位是 CSS px。
//
// 不用物理像素：缩放和 DPI 换算在 native 适配层做，越界到契约就要靠猜。
type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Viewport 是快照时刻的页面视口。
//
// Width/Height 是 CSS px，Scale 是设备像素/CSS 像素比（100% 缩放时为 1，
// 150% 为 1.5）。截图与点击坐标都要能回到同一个坐标系，否则 BR04 的
// ±2 CSS px 无从核对。
type Viewport struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Scale  float64 `json:"scale"`
}

// Element 是 snapshot 里可被引用的一个元素。
//
// Ref 只在产生它的 Snapshot 内有效；FrameID 为空表示顶层文档，非空表示该元素
// 来自受支持的子 frame。未覆盖的 frame 不会出现在 Elements 里，而是进
// Snapshot.UnsupportedFrames（BR10：明确 Unsupported，不是假装整页都能点）。
type Element struct {
	Ref       string `json:"ref"`
	FrameID   string `json:"frameId"`
	Role      string `json:"role"`
	Name      string `json:"name"`
	Tag       string `json:"tag"`
	InputType string `json:"inputType,omitempty"`
	Disabled  bool   `json:"disabled"`
	Bounds    Rect   `json:"bounds"`
}

// UnsupportedFrame 描述一个观察不到/操作不了的 frame。
//
// 页面里有它，不代表我们能看到它。Reason 必须是给用户看的具体理由
// （cross-origin / closed-shadow / detached），不能是空串。
type UnsupportedFrame struct {
	FrameID string `json:"frameId"`
	URL     string `json:"url,omitempty"`
	Reason  string `json:"reason"`
}

// Snapshot 是一次观察的结果，也是后续动作的引用基准。
//
// NavigationID 在每次文档替换后变化（包括同 URL 重新导航和 SPA 里重建文档），
// ControlGeneration 是产生快照时刻的控制权代数；两者与 Snapshot.ID 一起构成
// 引用有效性判据：任何一项不匹配都必须返回 STALE_SNAPSHOT，而不是猜一个新位置。
type Snapshot struct {
	ID                string             `json:"id"`
	TabID             TabID              `json:"tabId"`
	NavigationID      string             `json:"navigationId"`
	ControlGeneration uint64             `json:"controlGeneration"`
	Viewport          Viewport           `json:"viewport"`
	Elements          []Element          `json:"elements"`
	Text              string             `json:"text"`
	UnsupportedFrames []UnsupportedFrame `json:"unsupportedFrames"`

	// Truncated 表示本次观察因为基线 §7 的限额（元素/文本/JSON）被裁剪过。
	//
	// 它刻意**不进 JSON**：`schemas/browser.schema.json` 是串行维护的跨边界
	// 契约，B04 不改它（新增线上字段要同步改 schema 与 DTO 一致性测试，属于
	// 另一次受控变更）。裁剪事实因此通过两条通道表达：这个 Go 字段（MCP 层
	// B05 可以直接读给模型看）与证据 artifact（limits.json，逐项写明哪条上限
	// 被触发）。JSON 本身仍然是合法且**完整**的——只是内容被如实裁剪过，
	// 不会出现半个 base64 或坏掉的 JSON。
	Truncated bool `json:"-"`
}

// ExpectedCondition 是动作执行后的有界检查。
//
// 零值表示"没有后置条件"：这时 ActionResult.Verified 必须为 false，
// Verification.Status 必须是 unknown，调用方要重新 observe。
// 不允许任意 JS——见 ConditionKind 的注释。
type ExpectedCondition struct {
	Kind ConditionKind `json:"kind"`
	// URL 只在 url_equals 时使用，按规范化后的完整 URL 比较。
	URL string `json:"url,omitempty"`
	// Ref 只在 element_text / field_value / element_present 时使用，
	// 必须来自同一个 SnapshotID。
	Ref string `json:"ref,omitempty"`
	// Value 是期望的文本或字段值；element_present 忽略它。
	Value string `json:"value,omitempty"`
	// TimeoutMS 是该条件的等待上限；0 表示使用后端默认值，
	// 且永远不得超过 ActionTimeout。
	TimeoutMS int `json:"timeoutMs,omitempty"`
}

// Action 是一次原子操作请求。
//
// SnapshotID 是授权基准：ref 必须来自该快照，且执行前要再核验一次
// 文档/元素是否还在（B04/B08）。Text 是 fill 的值，Key 是 key 的键名，
// DeltaX/DeltaY 是 scroll 的位移（CSS px）。
type Action struct {
	Kind              ActionKind        `json:"kind"`
	SnapshotID        string            `json:"snapshotId"`
	Ref               string            `json:"ref,omitempty"`
	Text              string            `json:"text,omitempty"`
	Key               string            `json:"key,omitempty"`
	DeltaX            float64           `json:"deltaX,omitempty"`
	DeltaY            float64           `json:"deltaY,omitempty"`
	ExpectedCondition ExpectedCondition `json:"expectedCondition,omitzero"`
}

// Verification 记录后置条件的核验过程。
//
// Condition 为空表示本次没有后置条件（Status 必须是 unknown）。
// Detail 是给人和 agent 看的具体观测值，不能只写"失败"。
type Verification struct {
	Condition ConditionKind      `json:"condition,omitempty"`
	Status    VerificationStatus `json:"status"`
	Detail    string             `json:"detail,omitempty"`
}

// ActionResult 是一次操作的结论。
//
// Delivered 只表示输入已经交给 native 引擎，不等于业务成功：页面可能忽略它、
// 事件可能不被信任、后续请求可能失败。没有 Verified 就必须重新观察，
// 不能把 Delivered 当成功报给用户或模型（基线 §7、BR10）。
type ActionResult struct {
	Delivered    bool         `json:"delivered"`
	Verified     bool         `json:"verified"`
	NavigationID string       `json:"navigationId"`
	Verification Verification `json:"verification"`
	// Error 是给人和模型看的失败原因；机器分支请用 Verification.Status。
	Error string `json:"error,omitempty"`
}

// Image 是可见区域截图及其坐标元数据。
//
// Bytes 在 JSON 里是 base64（encoding/json 对 []byte 的默认编码）。
// 只带 PNG/JPEG/WebP；超过 4MiB 必须返回明确错误或受控缩小并更新 Scale，
// 不允许截断 base64（那样得到的图片是坏的，却看起来"有截图"）。
type Image struct {
	Bytes        []byte  `json:"bytes"`
	MimeType     string  `json:"mimeType"`
	PixelWidth   int     `json:"pixelWidth"`
	PixelHeight  int     `json:"pixelHeight"`
	CSSWidth     float64 `json:"cssWidth"`
	CSSHeight    float64 `json:"cssHeight"`
	Scale        float64 `json:"scale"`
	NavigationID string  `json:"navigationId"`
}

// SlotGeometry 是 React 上报的右侧槽位几何（B07 的 UpdateBrowserSlot 载荷）。
//
// 全部是应用 WebView 内的 CSS px。Revision 单调递增，接收方必须丢弃旧 revision
// 的迟到消息，否则拖动分栏时原生视图会被旧坐标拽回去。
type SlotGeometry struct {
	SlotID   string  `json:"slotId"`
	Revision uint64  `json:"revision"`
	Left     float64 `json:"left"`
	Top      float64 `json:"top"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	Visible  bool    `json:"visible"`
	Zoom     float64 `json:"zoom"`
}

// UnsupportedCapability 是缺失能力及原因，供 UI/agent 显示"需要人工"。
type UnsupportedCapability struct {
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
}

// CapabilitySet 是 backend 的实测能力（基线 §7：不能按 GOOS 写死）。
//
// 未具备的能力必须是 false 并在 Unsupported 里给出原因；上层据此不注册对应
// 工具，而不是先注册再在运行时报错。
type CapabilitySet struct {
	Platform string `json:"platform"`
	Engine   string `json:"engine"`
	// 基线 R06 的必备项。
	Navigate   bool `json:"navigate"`
	Observe    bool `json:"observe"`
	Act        bool `json:"act"`
	Screenshot bool `json:"screenshot"`
	// 首版不保证的部分（基线 §4）。
	TrustedInput      bool                    `json:"trustedInput"`
	CrossOriginFrames bool                    `json:"crossOriginFrames"`
	ClosedShadowRoots bool                    `json:"closedShadowRoots"`
	Downloads         bool                    `json:"downloads"`
	Popups            bool                    `json:"popups"`
	Unsupported       []UnsupportedCapability `json:"unsupported"`
}

// appErr 构造一个不可重试的 AppError，全部错误都带机器可解析的 Code。
func appErr(code, message string) *contracts.AppError {
	return contracts.NewAppError(code, message, false)
}

// Validate 检查后置条件是否在允许集合内，以及字段组合是否自相矛盾。
//
// 零值（没有后置条件）是合法的：它表示调用方不要求核验。
func (c ExpectedCondition) Validate() error {
	if c.TimeoutMS < 0 {
		return appErr(contracts.CodeUnsupported, "后置条件 timeoutMs 不能为负")
	}
	if c.TimeoutMS > 0 && time.Duration(c.TimeoutMS)*time.Millisecond > ActionTimeout {
		return appErr(contracts.CodeUnsupported, fmt.Sprintf(
			"后置条件 timeoutMs=%d 超过动作超时 %s", c.TimeoutMS, ActionTimeout))
	}
	switch c.Kind {
	case "":
		if c.URL != "" || c.Ref != "" || c.Value != "" {
			return appErr(contracts.CodeUnsupported,
				"没有后置条件种类时不允许携带 url/ref/value，避免看起来像核验过")
		}
		return nil
	case ConditionURLEquals:
		if strings.TrimSpace(c.URL) == "" {
			return appErr(contracts.CodeUnsupported, "url_equals 需要 url")
		}
	case ConditionElementText, ConditionFieldValue, ConditionElementPresent:
		if c.Ref == "" {
			return appErr(contracts.CodeUnsupported, string(c.Kind)+" 需要 ref")
		}
	case ConditionKind("evaluate"), ConditionKind("script"), ConditionKind("js"):
		// 显式拒绝，让"想跑一段 JS 来验证"的调用方拿到明确错误而不是静默降级。
		return appErr(contracts.CodeUnsupported,
			"不允许任意 JS 后置条件；只支持 "+joinConditionKinds())
	default:
		return appErr(contracts.CodeUnsupported,
			fmt.Sprintf("未知后置条件 %q；只支持 %s", c.Kind, joinConditionKinds()))
	}
	return nil
}

// Validate 检查动作本身是否可执行（不含服务端授权，授权在 B05）。
func (a Action) Validate() error {
	if err := a.ExpectedCondition.Validate(); err != nil {
		return err
	}
	if a.SnapshotID == "" {
		return appErr(contracts.CodeStaleSnapshot, "动作缺少 snapshotId：必须先 observe")
	}
	switch a.Kind {
	case ActionClick:
		if a.Ref == "" {
			return appErr(contracts.CodeStaleSnapshot, "click 需要 snapshot 里的 ref")
		}
	case ActionFill:
		if a.Ref == "" {
			return appErr(contracts.CodeStaleSnapshot, "fill 需要 snapshot 里的 ref")
		}
	case ActionKey:
		if a.Key == "" {
			return appErr(contracts.CodeUnsupported, "key 需要 key 名")
		}
	case ActionScroll:
		if a.DeltaX == 0 && a.DeltaY == 0 {
			return appErr(contracts.CodeUnsupported, "scroll 需要非零 deltaX/deltaY")
		}
	case "":
		return appErr(contracts.CodeUnsupported, "动作缺少 kind")
	default:
		return appErr(contracts.CodeUnsupported,
			fmt.Sprintf("未知动作 %q；只支持 %s", a.Kind, joinActionKinds()))
	}
	return nil
}

// Validate 强制"Delivered 不等于 Verified"这条不变量。
//
// 规则：Verified=true 时必须有后置条件且状态为 verified；没有后置条件时状态
// 只能是 unknown。Violation 是可执行的契约检查，不是注释。
func (r ActionResult) Validate() error {
	if r.Verified && r.Verification.Condition == "" {
		return appErr(contracts.CodeVerificationFailed,
			"Verified=true 但没有任何后置条件：Delivered 不能冒充业务成功")
	}
	if r.Verified && r.Verification.Status != VerificationVerified {
		return appErr(contracts.CodeVerificationFailed, fmt.Sprintf(
			"Verified=true 但核验状态是 %q，必须同时是 %q", r.Verification.Status, VerificationVerified))
	}
	if r.Verification.Condition == "" && r.Verification.Status != VerificationUnknown {
		return appErr(contracts.CodeVerificationFailed, fmt.Sprintf(
			"没有后置条件时状态只能是 %q，实际 %q", VerificationUnknown, r.Verification.Status))
	}
	if r.Verification.Condition != "" && r.Verification.Status == VerificationUnknown {
		// 允许：条件已下发但还没等到结果（例如导航打断）。
		return nil
	}
	if !isKnownVerificationStatus(r.Verification.Status) {
		return appErr(contracts.CodeVerificationFailed,
			fmt.Sprintf("未知核验状态 %q", r.Verification.Status))
	}
	return nil
}

// DeliveredResult 构造"输入已发出、业务结果未知"的结果。
//
// 这是没有后置条件时的唯一正确结论，避免各 backend 各自写一份相似但口径不同的
// 返回值。
func DeliveredResult(navigationID string) ActionResult {
	return ActionResult{
		Delivered:    true,
		Verified:     false,
		NavigationID: navigationID,
		Verification: Verification{
			Status: VerificationUnknown,
			Detail: "未提供后置条件：输入已交给 native 引擎，业务结果未知，必须重新 observe",
		},
	}
}

// CheckLimits 校验 snapshot 是否越过基线 §7 的三条上限。
//
// 返回错误意味着 backend 违反了契约：它可以自己截断并如实标注，但不能把超限
// 数据当成正常结果交出去。
func (s Snapshot) CheckLimits() error {
	if len(s.Elements) > MaxSnapshotElements {
		return appErr(contracts.CodeInternal, fmt.Sprintf(
			"snapshot 元素数 %d 超过上限 %d", len(s.Elements), MaxSnapshotElements))
	}
	if n := utf8.RuneCountInString(s.Text); n > MaxSnapshotTextChars {
		return appErr(contracts.CodeInternal, fmt.Sprintf(
			"snapshot 文本 %d 字符超过上限 %d", n, MaxSnapshotTextChars))
	}
	b, err := json.Marshal(s)
	if err != nil {
		return appErr(contracts.CodeInternal, "snapshot 无法序列化: "+err.Error())
	}
	if len(b) > MaxSnapshotJSONBytes {
		return appErr(contracts.CodeInternal, fmt.Sprintf(
			"snapshot JSON %d 字节超过上限 %d", len(b), MaxSnapshotJSONBytes))
	}
	return nil
}

// ValidateRefs 检查元素引用可用：非空、不重复、且受元素上限约束。
//
// 重复 ref 会让动作指向不确定的元素，宁可返回错误也不要猜。
func (s Snapshot) ValidateRefs() error {
	if len(s.Elements) > MaxSnapshotElements {
		return appErr(contracts.CodeInternal, fmt.Sprintf(
			"snapshot 元素数 %d 超过上限 %d", len(s.Elements), MaxSnapshotElements))
	}
	seen := make(map[string]struct{}, len(s.Elements))
	for i, el := range s.Elements {
		if el.Ref == "" {
			return appErr(contracts.CodeInternal, fmt.Sprintf("第 %d 个元素缺少 ref", i))
		}
		if _, dup := seen[el.Ref]; dup {
			return appErr(contracts.CodeInternal, "重复的元素引用 "+el.Ref)
		}
		seen[el.Ref] = struct{}{}
	}
	return nil
}

// Validate 检查截图元数据自洽并守住 4MiB 上限。
func (i Image) Validate() error {
	if len(i.Bytes) == 0 {
		return appErr(contracts.CodeInternal, "截图没有内容")
	}
	if len(i.Bytes) > MaxScreenshotBytes {
		return appErr(contracts.CodeInternal, fmt.Sprintf(
			"截图 %d 字节超过上限 %d", len(i.Bytes), MaxScreenshotBytes))
	}
	switch i.MimeType {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return appErr(contracts.CodeUnsupported, "不支持的截图类型 "+i.MimeType)
	}
	if i.PixelWidth <= 0 || i.PixelHeight <= 0 {
		return appErr(contracts.CodeInternal, "截图缺少像素尺寸")
	}
	if i.CSSWidth <= 0 || i.CSSHeight <= 0 {
		return appErr(contracts.CodeInternal, "截图缺少 CSS viewport")
	}
	if !(i.Scale > 0) || math.IsInf(i.Scale, 0) {
		return appErr(contracts.CodeInternal, "截图缺少有效的 scale")
	}
	if i.NavigationID == "" {
		return appErr(contracts.CodeInternal, "截图缺少 navigationId：坐标无法回溯到某一次文档")
	}
	return nil
}

// Validate 检查槽位几何可用。
func (g SlotGeometry) Validate() error {
	if strings.TrimSpace(g.SlotID) == "" {
		return appErr(contracts.CodeUnsupported, "槽位几何缺少 slotId")
	}
	if g.Width < 0 || g.Height < 0 {
		return appErr(contracts.CodeUnsupported, "槽位宽高不能为负")
	}
	if g.Zoom <= 0 {
		return appErr(contracts.CodeUnsupported, "槽位 zoom 必须为正")
	}
	return nil
}

// CheckTabLimit 校验 tab 限额，返回 nil 表示还能创建。
//
// activeForSession 是该 session 当前活跃 tab 数，activeTotal 是全部 session 合计。
// 调用方必须在创建 native 实例之前调用：B05 的用例要求超限时不留下新实例。
func CheckTabLimit(activeForSession, activeTotal int) error {
	if activeForSession >= MaxTabsPerSession {
		return appErr(CodeTabLimitExceeded, fmt.Sprintf(
			"该 session 已有 %d 个 tab，上限 %d", activeForSession, MaxTabsPerSession))
	}
	if activeTotal >= MaxTabsTotal {
		return appErr(CodeTabLimitExceeded, fmt.Sprintf(
			"已有 %d 个 tab，总计上限 %d", activeTotal, MaxTabsTotal))
	}
	return nil
}

func isKnownVerificationStatus(s VerificationStatus) bool {
	for _, v := range AllVerificationStatuses {
		if v == s {
			return true
		}
	}
	return false
}

func joinActionKinds() string {
	parts := make([]string, 0, len(AllActionKinds))
	for _, k := range AllActionKinds {
		parts = append(parts, string(k))
	}
	return strings.Join(parts, "/")
}

func joinConditionKinds() string {
	parts := make([]string, 0, len(AllConditionKinds))
	for _, k := range AllConditionKinds {
		parts = append(parts, string(k))
	}
	return strings.Join(parts, "/")
}
