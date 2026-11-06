// Anvil 附件读取扩展（C06f）。
//
// 为什么附件走"工具 + 只读端点"而不是"把路径给 Pi"：附件是用户显式选择的内容快照，
// 产品只承诺"经同一个打开句柄校验过 size/sha256 的字节"，不承诺给 Pi 一个可以写、
// 可以拿去做别的事的文件路径。所以：
//
//   模型 → 调用 anvil_read_attachment
//        → 本扩展用环境变量里的 URL/令牌去请求 app-owned 只读端点
//        → Go 侧按 capability 重新校验，通过才把字节交出来
//        → 工具结果文本（内容）回到模型
//
// 纪律：
//   - 端点地址与令牌只从环境变量读，绝不打印、绝不进工具结果；
//   - 不落盘：附件内容只在内存里过一道，工作区里不会出现附件副本；
//   - 失败必须让模型看见（throw），不能返回"空内容"——那会让模型以为附件是空的；
//   - 同一个 run 的清单来自 Go 侧冻结的引用，没有 ID 就没有内容。
import { Type } from "typebox";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

/** 产品注入的只读附件端点与凭据。 */
const URL_ENV = "ANVIL_ATTACHMENT_URL";
const TOKEN_ENV = "ANVIL_ATTACHMENT_TOKEN";
const SESSION_ENV = "ANVIL_ATTACHMENT_SESSION";

/** 单次工具结果里允许内联的最大字节数：超过就明确说明"截断"，不假装读全了。 */
const MAX_INLINE_BYTES = 256 * 1024;

interface AttachmentView {
	id: string;
	displayName?: string;
	size?: number;
	sha256?: string;
	mediaType?: string;
}

interface Channel {
	url: string;
	token: string;
	session: string;
}

function readChannel(): Channel | null {
	const url = (process.env[URL_ENV] ?? "").trim();
	const token = (process.env[TOKEN_ENV] ?? "").trim();
	const session = (process.env[SESSION_ENV] ?? "").trim();
	if (!url || !token || !session) return null;
	return { url, token, session };
}

async function callApi(channel: Channel, pathname: string, signal?: AbortSignal): Promise<Response> {
	const resp = await fetch(`${channel.url}${pathname}`, {
		method: "GET",
		headers: {
			Authorization: `Bearer ${channel.token}`,
			"X-Anvil-Session": channel.session,
		},
		signal,
	});
	return resp;
}

export default function (pi: ExtensionAPI): void {
	pi.registerTool({
		name: "anvil_read_attachment",
		label: "Read Attachment",
		description:
			"读取用户为这次会话选中的附件。不带参数调用时列出可用附件（id/名称/大小/类型）；" +
			"带 attachmentId 时返回该附件的内容（文本内联，二进制给说明）。" +
			"附件没有可写的文件路径——只能通过这个工具读。",
		promptSnippet: "anvil_read_attachment：读取用户选中的附件（先不带参数列清单）",
		promptGuidelines: [
			"Use anvil_read_attachment with no arguments to list the attachments the user selected for this session.",
			"Use anvil_read_attachment with attachmentId to read one attachment's content; do not claim you read it without calling the tool.",
		],
		parameters: Type.Object({
			attachmentId: Type.Optional(
				Type.String({
					description: "附件 id（先不带参数调用一次，从清单里取）。省略表示列出清单。",
				}),
			),
		}),
		async execute(_toolCallId, params, signal) {
			const channel = readChannel();
			if (!channel) {
				throw new Error(
					`附件端点未配置（缺少 ${URL_ENV}/${TOKEN_ENV}/${SESSION_ENV}），本次会话没有可读附件`,
				);
			}
			const id = String(params.attachmentId ?? "").trim();

			if (!id) {
				const resp = await callApi(channel, "/v1/attachments", signal);
				if (!resp.ok) {
					throw new Error(`列出附件失败（HTTP ${resp.status}）`);
				}
				const body = (await resp.json()) as { attachments?: AttachmentView[] };
				const items = body.attachments ?? [];
				if (items.length === 0) {
					return {
						content: [{ type: "text" as const, text: "这次会话没有附件。" }],
						details: { attachments: [] as AttachmentView[] },
					};
				}
				const lines = items.map((it, i) => {
					const size = typeof it.size === "number" ? `${it.size} 字节` : "大小未知";
					const type = it.mediaType ? `，${it.mediaType}` : "";
					return `${i + 1}. id=${it.id} 名称=${it.displayName ?? "（未命名）"}（${size}${type}）`;
				});
				return {
					content: [
						{
							type: "text" as const,
							text: `这次会话有 ${items.length} 个附件：\n${lines.join("\n")}`,
						},
					],
					details: { attachments: items },
				};
			}

			const resp = await callApi(channel, `/v1/attachments/${encodeURIComponent(id)}`, signal);
			if (!resp.ok) {
				let detail = "";
				try {
					const err = (await resp.json()) as { error?: { code?: string; message?: string } };
					detail = err.error?.message ?? err.error?.code ?? "";
				} catch {
					// 非 JSON 错误体：保留状态码即可，不猜内容。
				}
				// fail closed：读不到就说读不到，绝不返回"空附件"。
				throw new Error(`读取附件 ${id} 失败（HTTP ${resp.status}）${detail ? "：" + detail : ""}`);
			}
			const buf = new Uint8Array(await resp.arrayBuffer());
			const truncated = buf.byteLength > MAX_INLINE_BYTES;
			const slice = truncated ? buf.slice(0, MAX_INLINE_BYTES) : buf;
			const decoded = decodeText(slice);

			if (decoded === null) {
				return {
					content: [
						{
							type: "text" as const,
							text:
								`附件 ${id} 是二进制内容（共 ${buf.byteLength} 字节，sha256=` +
								`${resp.headers.get("X-Anvil-SHA256") ?? "见清单"}），无法作为文本内联。` +
								`需要具体字节时请让用户换一个文本附件或先转换格式。`,
						},
					],
					details: { id, bytes: buf.byteLength, binary: true },
				};
			}
			const header = truncated
				? `附件 ${id} 内容（前 ${MAX_INLINE_BYTES} 字节，已截断；共 ${buf.byteLength} 字节）：\n`
				: `附件 ${id} 内容：\n`;
			return {
				content: [{ type: "text" as const, text: header + decoded }],
				details: { id, bytes: buf.byteLength, truncated },
			};
		},
	});
}

/** 把字节按 UTF-8 解成文本；明显不是文本（含 NUL 或非法序列）返回 null。 */
function decodeText(bytes: Uint8Array): string | null {
	for (let i = 0; i < bytes.length; i++) {
		if (bytes[i] === 0) return null;
	}
	try {
		return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
	} catch {
		return null;
	}
}
