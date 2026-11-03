// MyAgent 执行前授权扩展（F04）。
//
// 为什么必须有它：Pi 的内置工具（read/write/edit/bash）、MCP 工具、扩展工具、codemode
// 嵌套调用，全都只在 Pi 进程内执行，不经过本产品 Go 侧的任何代码。要在"执行前"卡住它们，
// 唯一可用的钩子是 Pi 的 tool_call 事件——本扩展就是那个钩子。
//
// 纪律（基线 §6）：
//   - 默认拒绝。端点未配置、不可达、返回非 2xx、返回未知结论，一律 block。
//     不允许出现"策略服务挂了所以放行"的路径。
//   - 不在提示词里要求模型自觉征求批准。模型可以被说服，钩子不能。
//   - 令牌只从环境变量读，绝不打印、不写日志、不进返回值。
//
// 覆盖通道：builtin（read/write/edit/bash）、harness-mcp（mcp__harness__*）、
// external-mcp（mcp__*）、extension（其它工具）、codemode（嵌套调用同样会触发 tool_call）、
// 以及用户手输的 ! 命令（user_bash）。
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const URL_ENV = "MYAGENT_POLICY_URL";
const TOKEN_ENV = "MYAGENT_POLICY_TOKEN";
const SESSION_ENV = "MYAGENT_POLICY_SESSION";

// Pi 内置工具名。这些是最容易造成真实副作用的一组，单独列出便于审计覆盖表。
const BUILTIN_TOOLS = new Set(["read", "write", "edit", "bash", "ls", "grep", "find", "glob", "apply_patch"]);

type Decision = "allow" | "deny" | "ask";

interface AuthorizeResponse {
	decision?: Decision | string;
	reason?: string;
	approvalId?: string;
}

/** 把 Pi 的工具名映射到策略层认识的通道。映射错误会让调用落到"未知通道 → 拒绝"，宁可误拒不可误放。 */
function sourceOf(toolName: string): string {
	if (toolName.startsWith("mcp__harness__") || toolName.startsWith("mcp__harness_")) {
		return "harness-mcp";
	}
	if (toolName.startsWith("mcp__")) {
		return "external-mcp";
	}
	if (toolName === "codemode") {
		return "codemode";
	}
	if (BUILTIN_TOOLS.has(toolName)) {
		return "builtin";
	}
	return "extension";
}

/**
 * 取出工具的目标资源路径。
 *
 * 只认内置工具已知的字段名。取不到就返回空串——策略层对"文件类工具缺资源范围"是直接拒绝，
 * 所以这里的保守取值不会导致漏检，只会导致误拒（可接受方向）。
 */
function scopeOf(toolName: string, input: unknown): string {
	if (!input || typeof input !== "object") return "";
	const rec = input as Record<string, unknown>;
	if (BUILTIN_TOOLS.has(toolName)) {
		if (typeof rec.path === "string") return rec.path;
		if (typeof rec.file_path === "string") return rec.file_path;
	}
	if (typeof rec.path === "string" && !toolName.startsWith("mcp__")) return rec.path;
	return "";
}

interface PolicyChannel {
	url: string;
	token: string;
	session: string;
}

export default function (pi: ExtensionAPI): void {
	const url = (process.env[URL_ENV] ?? "").trim();
	const token = (process.env[TOKEN_ENV] ?? "").trim();
	const session = (process.env[SESSION_ENV] ?? "").trim();
	const channel: PolicyChannel | null = url && token && session ? { url, token, session } : null;

	if (!channel) {
		// 没配置就不加载任何放行逻辑：下面的每个钩子都会 block。
	}

	/** 向策略端点要一个结论。任何异常路径都返回拒绝。 */
	async function authorize(
		toolName: string,
		toolCallId: string,
		input: unknown,
		signal: AbortSignal | undefined,
	): Promise<string | null> {
		if (!channel) {
			return `策略端点未配置（缺少 ${URL_ENV}/${TOKEN_ENV}/${SESSION_ENV}），拒绝执行 ${toolName}`;
		}
		const body = JSON.stringify({
			toolCallId,
			toolName,
			source: sourceOf(toolName),
			arguments: input ?? {},
			resourceScope: scopeOf(toolName, input),
		});

		let resp: Response;
		try {
			resp = await fetch(`${channel.url}/v1/authorize`, {
				method: "POST",
				headers: {
					"Content-Type": "application/json",
					Authorization: `Bearer ${channel.token}`,
					"X-MyAgent-Session": channel.session,
				},
				body,
				signal,
			});
		} catch (err) {
			return `策略端点不可达，拒绝执行 ${toolName}：${err instanceof Error ? err.message : String(err)}`;
		}

		if (!resp.ok) {
			// 认证失败、来源不符、请求非法都在这里；一律不放行。
			return `策略端点拒绝了请求（HTTP ${resp.status}），拒绝执行 ${toolName}`;
		}

		let out: AuthorizeResponse;
		try {
			out = (await resp.json()) as AuthorizeResponse;
		} catch {
			return `策略端点返回了无法解析的响应，拒绝执行 ${toolName}`;
		}

		if (out.decision === "allow") return null;
		const reason = typeof out.reason === "string" && out.reason.length > 0 ? out.reason : "策略未授权";
		return `${toolName} 未获授权：${reason}`;
	}

	// 所有工具调用（含 MCP、扩展工具、codemode 嵌套调用）的唯一强制点。
	pi.on("tool_call", async (event, ctx) => {
		const reason = await authorize(event.toolName, event.toolCallId, event.input, ctx.signal);
		if (reason === null) return;
		return { block: true, reason };
	});

	// 用户手输的 ! / !! 命令是另一条真实执行通道，同样要过策略。
	pi.on("user_bash", async (event, ctx) => {
		const reason = await authorize("shell", "user-bash", { command: event.command }, ctx.signal);
		if (reason === null) return;
		// 返回 result 即"已完结、不再执行"：命令根本没有被跑起来。
		return {
			result: {
				output: reason,
				exitCode: 126, // 126 = 找到但不可执行，语义上最接近"被拒绝"
				cancelled: false,
				truncated: false,
			},
		};
	});
}
