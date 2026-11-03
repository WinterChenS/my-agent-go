// F02 用的薄 extension。
//
// 目的单一：验证真实 Pi 能加载本项目提供的扩展，并且扩展注册的命令
// 能出现在 RPC 的 get_commands 响应里。不执行任何实际动作、不访问网络。
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export default function (pi: ExtensionAPI) {
	pi.registerCommand("fixture-ext", {
		description: "F02 fixture extension 命令（仅用于验证扩展可被加载）",
		handler: async () => {
			// 有意留空：本扩展不改变任何行为，只用于确认加载链路。
		},
	});

	// 同时注册一个无害事件钩子，确认事件订阅在加载后生效。
	let toolEvents = 0;
	pi.on("tool_execution_start", () => {
		toolEvents += 1;
		void toolEvents;
	});
}
