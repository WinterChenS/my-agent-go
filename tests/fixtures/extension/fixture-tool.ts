// F04/SEC03 用的"扩展工具通道"探针。
//
// 为什么要这个 fixture：Pi 的内置工具（read/write/edit/bash）、MCP 工具、codemode 嵌套
// 各有各的参数结构与执行路径。策略扩展声称"所有工具调用统一在 tool_call 钩子上拦"，
// 但这句话必须被实测证明——否则"扩展工具也被拦住了"只是一句注释，不是事实。
//
// 这个工具的唯一作用是在**执行时**往磁盘写一个 marker 文件。于是"策略说拒绝之后工具
// 到底跑没跑"可以落在文件系统上判定，而不是看界面文案或日志里出现的字符串
// （工具参数里本身就含 marker 名，"被拦"和"跑了"在日志里长得一样）。
//
// 参数名特意用 marker 而不是 path：policy 层对 filesystemTools（read/write/edit/...）
// 才做资源范围绑定，用别的字段名能让本用例只考察"扩展通道是否过策略"这一件事。
import { writeFileSync } from "node:fs";
import path from "node:path";
import { Type } from "typebox";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export default function (pi: ExtensionAPI): void {
	pi.registerTool({
		name: "fixture_marker",
		label: "Fixture Marker",
		description:
			"写一个 marker 文件。这是受控 fixture 工具，只用于验证扩展工具通道在执行前会经过策略层，不做任何别的事。",
		parameters: Type.Object({
			marker: Type.String({ description: "marker 文件名（相对 Pi 的工作目录）" }),
		}),
		async execute(_toolCallId, params) {
			// 相对路径按 Pi 进程的 cwd 解析：工具的真实落地位置与内置工具一致。
			const target = path.isAbsolute(params.marker)
				? params.marker
				: path.resolve(process.cwd(), params.marker);
			writeFileSync(target, "extension-tool-ran");
			return {
				content: [{ type: "text" as const, text: `wrote ${target}` }],
				details: {},
			};
		},
	});
}
