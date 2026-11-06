// Anvil 内容澄清扩展（C06f）。
//
// 存在意义：模型需要问用户一个必须由人回答的问题（选一个选项、确认一次、
// 补一段文字）。产品侧不能从助手文本里"猜"这个意图（U02 明确禁止），也不能把它
// 混进审批（审批是权限决定，澄清是内容决定，两者不能共用一条记录）。所以这里
// 注册**唯一**一个工具，由它把问题变成一次真实的 Pi extension UI 请求：
//
//   Go ← extension_ui_request ← Pi ← ctx.ui.input/select/confirm/editor ← 本扩展
//   Go → extension_ui_response → Pi → ctx.ui 返回值 → 工具结果 → 模型
//
// 纪律（与计划 C06「内容澄清」、handoff §4.3 一致）：
//   - 只注册一个工具 `anvil_ask_user`，固定 schema（question/kind/options），
//     不注册命令、不订阅事件、不碰文件、不联网；
//   - 本扩展是产品自带、不可关闭的系统扩展：由产品隔离参数以绝对路径 -e 加载，
//     不从用户配置目录发现；环境里拿不到产品身份令牌就直接失败（不是"跳过"）；
//   - 每个 UI 请求的标题带一枚 per-run 令牌前缀。Go 侧据此确认"这个请求来自
//     产品自带的澄清工具"，而不是靠工具名猜——第三方同名工具的 UI 请求没有这枚
//     令牌，Go 会拒绝把它当成澄清（见 internal/run/clarification.go）；
//   - 不设置 timeout：超时由 Go 统一裁决（发送 cancelled），这样"超时"与"用户
//     取消"在同一侧收敛，不会出现 Pi 自己按默认值悄悄回答的情况。
import { Type } from "typebox";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

/** 产品注入的 per-run 澄清令牌环境变量。 */
const TOKEN_ENV = "ANVIL_CLARIFICATION_TOKEN";
/** 产品注入的扩展自身路径，供策略扩展核对工具身份（不是靠工具名）。 */
const EXT_ENV = "ANVIL_CLARIFICATION_EXT";

/** 标题里的身份前缀格式；Go 侧按同一格式解析（见 internal/run/clarification.go）。 */
const TAG_PREFIX = "[[anvil-clarification:";
const TAG_SUFFIX = "]] ";

/** 固定 schema：只有这三个字段，kind 只接受四个值。 */
const schema = Type.Object({
	question: Type.String({
		description:
			"要问用户的问题，用完整的一句话写清楚（必填）。不要在这里复述已经做过的操作。",
	}),
	kind: Type.Union(
		[
			Type.Literal("text"),
			Type.Literal("select"),
			Type.Literal("confirm"),
			Type.Literal("editor"),
		],
		{
			description:
				"澄清类型：text=一行文本；editor=多行文本；select=从 options 里选一个；confirm=是/否。",
		},
	),
	options: Type.Optional(
		Type.Array(Type.String(), {
			description: "kind=select 时的候选项（至少一项）；其它 kind 忽略此字段。",
		}),
	),
});

export default function (pi: ExtensionAPI): void {
	const token = (process.env[TOKEN_ENV] ?? "").trim();
	// 身份必须由产品注入：拿不到就不是"产品自带的澄清工具"，
	// 此时工具仍然注册（模型看得见），但每次执行都失败——绝不静默按默认值回答。
	const tagged = (question: string): string =>
		token ? `${TAG_PREFIX}${token}${TAG_SUFFIX}${question}` : question;

	pi.registerTool({
		name: "anvil_ask_user",
		label: "Ask User",
		description:
			"向用户提出一个必须由人回答的澄清问题（不带任何副作用）。" +
			"kind=text 收一行文本；kind=editor 收多行文本；kind=select 让用户从 options 里选一个；" +
			"kind=confirm 收一个是/否。用户取消时返回取消说明，不要把它当成已确认。",
		promptSnippet:
			"anvil_ask_user：需要人回答的问题（选项/确认/文本）用它问，不要自己编造用户的选择",
		promptGuidelines: [
			"Use anvil_ask_user whenever the answer must come from the user (a choice, a confirmation, or free text) instead of guessing.",
			"anvil_ask_user with kind=select must pass at least one entry in options.",
		],
		parameters: schema,
		async execute(_toolCallId, params, signal, _onUpdate, ctx) {
			if (!token) {
				// 失败必须让模型看见：悄悄返回一个"默认答案"等于伪造用户输入。
				throw new Error(
					`澄清工具缺少产品身份令牌（${TOKEN_ENV} 未设置），本次澄清无法发给用户`,
				);
			}
			const question = String(params.question ?? "").trim();
			if (!question) {
				throw new Error("anvil_ask_user 缺少 question");
			}
			const title = tagged(question);

			switch (params.kind) {
				case "text": {
					const value = await ctx.ui.input(title, "输入你的回答", { signal });
					return answerText(value, "用户回答");
				}
				case "editor": {
					// editor 没有 opts 形参（见 ExtensionUIContext.editor），这里只传 prefill。
					const value = await ctx.ui.editor(title, "");
					return answerText(value, "用户回答（多行）");
				}
				case "select": {
					const options = (params.options ?? []).map((v) => String(v));
					if (options.length === 0) {
						throw new Error("anvil_ask_user 的 kind=select 必须给出至少一个 option");
					}
					const value = await ctx.ui.select(title, options, { signal });
					return answerText(value, "用户选择");
				}
				case "confirm": {
					const ok = await ctx.ui.confirm(title, question, { signal });
					return {
						content: [
							{
								type: "text" as const,
								text: ok ? "用户确认：是" : "用户确认：否（或已取消）",
							},
						],
						details: { kind: "confirm", confirmed: ok },
					};
				}
				default:
					throw new Error(`未知的澄清类型: ${String(params.kind)}`);
			}
		},
	});
}

/** 把 dialog 的返回值变成模型可见的结果文本；undefined 表示用户取消。 */
function answerText(value: string | undefined, label: string) {
	if (value === undefined || value === null) {
		return {
			content: [
				{
					type: "text" as const,
					text: "用户取消了这次澄清（没有提供答案）；不要把它当成已确认或按默认值继续。",
				},
			],
			details: { kind: "cancelled" },
		};
	}
	return {
		content: [{ type: "text" as const, text: `${label}：${value}` }],
		details: { kind: "value", value },
	};
}
