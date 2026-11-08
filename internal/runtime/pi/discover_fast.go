package pi

// 本文件是 FIX1-B 的**快速发现**路径。
//
// 背景（2026-11-08 用户实测）：应用每次启动都卡在“Pi 连接”上。原因是挂载时的
// Discover 对每个可用候选都跑完整的六项契约探测（spawn 真实 Pi、起 stub provider，
// 本机实测单个候选 10.7s）。用户要的是“本机已有 Pi 就直接连，没有就提示安装”。
//
// 所以把发现拆成两档，结论都写清楚：
//   - 快速发现（本文件）：只做 --version 版本探测 + 路径/指纹/来源解析，**不 spawn**
//     任何契约探测子进程；秒级以内返回。它承诺“能不能启动、版本是什么”，
//     **不**承诺“这个二进制会说我们的协议”。
//   - 完整发现（Discover 的默认行为，未改语义）：逐候选跑六项契约探测，
//     结论是 ready_verified / incompatible 级别的证据。
//
// 完整探测仍然保留在需要它的两个地方：设置页显式的“验证契约”动作，以及
// 真正要跑一次 run 之前（见报告里对 run-start 缺口的说明）。
//
// 结果形状与 Discover 一致（同一个 DiscoveryResult），额外用 Verification 标注
// 这次发现是 contract_probed 还是 version_only，候选上也有 ContractVerified。
// 界面因此能同时回答“能用吗”和“验证过吗”，不会把后者说成前者。

import "context"

// DiscoverFast 做一次**不跑契约探测**的发现。
//
// 与 Discover 的关系：同一套来源/去重/状态判定，只是强制跳过 F08-B 的契约探测。
// 它不改变 Discover 的行为——只在本函数自己的请求副本上置 SkipContractProbe，
// 并且忽略调用方传进来的 ProbeFunc（快速路径不允许被注入的探针拖慢或改判）。
func DiscoverFast(ctx context.Context, req DiscoveryRequest) (DiscoveryResult, error) {
	fast := req
	fast.SkipContractProbe = true
	// 调用方即使传了 ProbeFunc 也不执行：快速路径的承诺是“不 spawn 契约探测”，
	// 这条承诺不能因为一个注入点而失效。
	fast.ProbeFunc = nil

	res, err := Discover(ctx, fast)
	if err != nil {
		return res, err
	}
	// Discover 在没有装上 probe 时已经不会给出 contract_probed；这里再显式钉一次，
	// 防止将来有人把默认探针塞回这条路。
	res.Verification = VerificationVersionOnly
	res.Diagnostics = append(res.Diagnostics,
		"kind:\"fast-discovery\" 快速发现只探测版本与指纹，未执行六项契约探测；"+
			"结论只说明能启动，不说明契约已验证（verification=version_only）")
	return res, nil
}
