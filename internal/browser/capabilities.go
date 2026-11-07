package browser

// 能力声明的集中口径。
//
// BR10 要求"未覆盖的表面明确 Unsupported 并给原因"，不接受空原因、也不接受
// 把"看不见"当成"页面是空的"。原因文案放在这里而不是散在 native 里，是为了
// 让 UI/agent 看到的是同一句话，且新增平台不会各写一份。

// 未覆盖表面的固定原因。
const (
	// ReasonCrossOriginFrame：浏览器同源策略决定宿主脚本读不到它的 DOM。
	// 这一条不是"我们还没写"，而是"在当前架构下无法读"——所以是 Unsupported。
	ReasonCrossOriginFrame = "cross-origin frame：同源策略禁止读取其 DOM，无法观察或点击内部元素（BR10 Unsupported）"
	// ReasonSameOriginFrameNotWalked：同源 frame 理论上可读，但 B04 只覆盖顶层文档。
	// 区分这两种原因很重要：一个是能力边界，一个是本轮范围。
	ReasonSameOriginFrameNotWalked = "same-origin frame：B04 只观察顶层文档，子 frame 内容未纳入快照（不是空页面）"
	// ReasonClosedShadowRoot：closed 模式的 shadow root 对宿主脚本不可读。
	ReasonClosedShadowRoot = "closed shadow root：attachShadow({mode:'closed'}) 后宿主脚本读不到内部 DOM，无法观察或点击（BR10 Unsupported）"
	// ReasonShadowProbeUnavailable 是探测通道本身不可用时的诚实声明。
	ReasonShadowProbeUnavailable = "无法确认 closed shadow root：DOM.getDocument(pierce) 探测通道不可用"
)

// UncoveredSurfaces 返回"当前实现明确不覆盖"的表面清单。
//
// CapabilitySet.Unsupported 直接用它，避免同一个能力在 native、UI、工具注册
// 三处各写一套理由。
func UncoveredSurfaces() []UnsupportedCapability {
	return []UnsupportedCapability{
		{Capability: "crossOriginFrames", Reason: ReasonCrossOriginFrame},
		{Capability: "closedShadowRoots", Reason: ReasonClosedShadowRoot},
	}
}

// UnsupportedSurface 构造一条"这个 frame/shadow 我们看不到"的声明。
//
// FrameID 允许不是真正的 frame（closed shadow root 没有 frame id）：这里复用的
// 是"页面里有一块我们没纳入快照的区域"这个语义，Reason 必须具体到为什么。
func UnsupportedSurface(id, url, reason string) UnsupportedFrame {
	return UnsupportedFrame{FrameID: id, URL: url, Reason: reason}
}
