// 对话页几何对比（D2b 证据生成器，不是断言门禁）。
//
// 做什么：
//   1. 把**构建产物**（frontend/dist）用静态服务器起在 127.0.0.1:4190，SPA 回退到 index.html；
//   2. 把原型导出 `docs/ui/Agent-客户端产品原型/` 起在 127.0.0.1:4191；
//   3. 在 app 里把 Wails runtime 的 HTTP 传输（POST /wails/runtime，见
//      node_modules/@wailsio/runtime/dist/runtime.js）挡成本脚本内的**只读 fixture**，
//      再用 `window._wails.dispatchWailsEvent` 推一条真实的 run 事件序列——
//      于是量到的是"产品自己的组件 + 自己的 CSS + 后端形状的数据"，
//      而不是另写一份 HTML 冒充产品；
//   4. 1366×768 / 1440×900 各截一张 app 与原型图，并量同一批盒子：
//      thread-inner 的 max-width/padding/gap、消息间距、composer 宽度、
//      右栏宽度与 head 高度、气泡圆角/内边距/字号、页边距；
//   5. 写 chat-diff.json 并对每一行给出 delta（误差 >1px 时脚本退出码非零）。
//
// 口径与限制：
//   - 这是**网页侧**证据（同一份 React 构建产物 + Chromium），不含 Wails 宿主 /
//     WebView2 / DPI 缩放；真机几何仍由 tests/native 产生。
//   - fixture 只喂"页面渲染需要的最小后端事实"（会话、工作区、模型、Pi 就绪）；
//     所有数字都来自产品组件，不来自本脚本。
//
// 用法（Node 24；先用 npm run build 出 dist）：
//   node frontend/src/features/chat/chat-diff.mjs [--out=<dir>]
// 浏览器：PLAYWRIGHT_BROWSERS_PATH 默认指向 <repo>/.tools/pw-browsers。

import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, extname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const HERE = dirname(fileURLToPath(import.meta.url));
const FRONTEND = resolve(HERE, "..", "..", "..");
const REPO = resolve(FRONTEND, "..");
const PROTO = join(REPO, "docs", "ui", "Agent-客户端产品原型");
const DIST = join(FRONTEND, "dist");
// 允许指向 dev server（排查用：源码位置可读）；默认仍是构建产物。
const APP_ORIGIN = process.env.CHAT_DIFF_APP_ORIGIN || "http://127.0.0.1:4190";

const DEBUG = process.argv.includes("--debug");
const outArg = process.argv.find((a) => a.startsWith("--out="));
const OUT = outArg ? resolve(outArg.slice("--out=".length)) : join(REPO, ".tmp", "d2b-evidence");
// D2c：给出"改前"那次实测的目录时，额外写 chat-head-diff.json（改前/改后/原型 的 .thread top）。
const headDiffArg = process.argv.find((a) => a.startsWith("--head-diff-before="));
const headDiffBefore = headDiffArg
  ? resolve(headDiffArg.slice("--head-diff-before=".length))
  : null;

// 浏览器位置：优先环境变量，其次工程内的 .tools/pw-browsers（与 D1 的证据脚本同一约定）。
// 用 executablePath 显式指定而不是只设环境变量：ESM 的 import 在模块体之前求值，
// 这里再改 process.env 已经晚了（Playwright 在 import 时读的默认路径）。
const BROWSERS =
  process.env.PLAYWRIGHT_BROWSERS_PATH || join(REPO, ".tools", "pw-browsers");

/** 在 browsers 目录下找一个可用的 Chromium（优先完整 chrome，其次 headless shell）。 */
function findChromium() {
  if (!existsSync(BROWSERS)) return undefined;
  for (const dir of readdirSync(BROWSERS)) {
    for (const rel of [["chrome-win64", "chrome.exe"], ["chrome-win", "chrome.exe"], ["chrome-linux", "chrome"], ["chrome-mac", "Chromium.app/Contents/MacOS/Chromium"]]) {
      const candidate = join(BROWSERS, dir, ...rel);
      if (existsSync(candidate)) return candidate;
    }
    const headless = join(BROWSERS, dir, "chrome-headless-shell-win64", "chrome-headless-shell.exe");
    if (existsSync(headless)) return headless;
  }
  return undefined;
}

const VIEWPORTS = [
  { width: 1366, height: 768 },
  { width: 1440, height: 900 },
];

/** 契约容差：1px（规格里的数字是整数，子像素取整只允许 ±1）。 */
const TOLERANCE_PX = 1;

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".woff2": "font/woff2",
};

/** 静态服务器：dist 走 SPA 回退（/chat/sess-1 也要给 index.html）。 */
function serve(root, port, { spaFallback = false } = {}) {
  const server = createServer((req, res) => {
    const url = new URL(req.url ?? "/", `http://127.0.0.1:${port}`);
    let file = join(root, decodeURIComponent(url.pathname));
    if (!existsSync(file) || url.pathname.endsWith("/")) {
      file = join(root, "index.html");
      if (!spaFallback && !existsSync(file)) {
        res.writeHead(404).end("not found");
        return;
      }
    }
    try {
      const body = readFileSync(file);
      res.writeHead(200, { "Content-Type": MIME[extname(file)] ?? "application/octet-stream" }).end(body);
    } catch {
      if (spaFallback) {
        res.writeHead(200, { "Content-Type": MIME[".html"] }).end(readFileSync(join(root, "index.html")));
        return;
      }
      res.writeHead(404).end("not found");
    }
  });
  return new Promise((ok) => server.listen(port, "127.0.0.1", () => ok(server)));
}

// ---------------------------------------------------------------- fixture（后端形状）

const SESSION = {
  id: "sess-1",
  workspaceId: "ws-a",
  title: "API 迁移评估：v1 → v2 兼容性",
  pinned: false,
  archived: false,
  kind: "chat",
  createdAt: "2026-11-06T00:00:00Z",
  updatedAt: "2026-11-06T00:00:00Z",
  revision: 3,
};

const WORKSPACE = {
  id: "ws-a",
  name: "my-agent-go-ui",
  canonicalPath: "~/work/my-agent-go-ui",
  trusted: true,
  createdAt: "2026-11-06T00:00:00Z",
};

const SETTINGS = {
  piPath: "D:/pi/pi.exe",
  nodePath: "D:/node/node.exe",
  provider: "anthropic",
  baseUrl: "https://api.example.invalid",
  model: "claude-sonnet-4.5",
  apiKey: "",
  modelRef: "anthropic/claude-sonnet-4.5",
  workspace: WORKSPACE.canonicalPath,
  shellAllowed: true,
};

const STATUS = {
  appRoot: "D:/app",
  agentDir: "D:/app/agent",
  piPath: SETTINGS.piPath,
  piVersion: "1.5.0",
  provider: SETTINGS.provider,
  model: SETTINGS.model,
  modelRef: SETTINGS.modelRef,
  workspace: SETTINGS.workspace,
  isolation: "agentDir",
  policyActive: true,
  platform: "windows/amd64",
  runtimeProvider: SETTINGS.provider,
  runtimeModel: SETTINGS.model,
  runtimePending: false,
  runtimeSkills: [],
  skillAutoDiscovery: "enabled",
  skillsPending: false,
  runtimeMCP: [],
  mcpPending: false,
};

const CURRENT_MODEL = {
  source: "resources",
  ref: SETTINGS.modelRef,
  provider: SETTINGS.provider,
  model: SETTINGS.model,
  item: {
    id: "claude-sonnet-4.5",
    api: "anthropic",
    name: "claude-sonnet-4.5",
    model: "claude-sonnet-4.5",
    baseUrl: SETTINGS.baseUrl,
    contextWindow: 200000,
    thinkingLevels: ["off", "low", "medium", "high"],
    inputKinds: ["text"],
  },
  revision: 1,
};

const PI_CANDIDATE = {
  installation: {
    id: "managed:1.5.0",
    executable: "D:/pi/pi.exe",
    origin: "managed",
    architecture: "x86_64",
    runnable: true,
    version: "1.5.0",
  },
  origin: "managed",
  distribution: "standalone",
  executableHash: "a".repeat(64),
  launcherPath: "D:/pi/pi.exe",
  runtimeExecutable: "D:/pi/pi.exe",
  runtimeHash: "b".repeat(64),
  compatibility: "ready_verified",
  diagnostics: [],
};

const RUN_SNAPSHOT = {
  runId: "run-1",
  sessionId: SESSION.id,
  workspaceId: WORKSPACE.id,
  state: "running",
  lastSeq: 0,
  configRevision: 1,
  piVersion: "1.5.0",
  provider: CURRENT_MODEL.provider,
  model: CURRENT_MODEL.model,
  managedSkills: [],
  skillAutoDiscovery: "enabled",
};

const ARTIFACT = {
  id: "art-1",
  workspaceId: WORKSPACE.id,
  runId: "run-1",
  sessionId: SESSION.id,
  displayName: "callers-inventory.csv",
  canonicalPath: "~/work/my-agent-go-ui/callers-inventory.csv",
  sha256: "c".repeat(64),
  size: 4096,
  mediaType: "text/csv",
  createdAt: "2026-11-06T00:00:00Z",
  updatedAt: "2026-11-06T00:00:00Z",
};

const RUN_DETAILS = {
  runId: "run-1",
  record: {
    runId: "run-1",
    state: "waiting_approval",
    provider: CURRENT_MODEL.provider,
    model: CURRENT_MODEL.model,
    configRevision: 1,
    lastSeq: 8,
    startedAt: "2026-11-06T00:00:00Z",
    usage: { availability: "unavailable" },
  },
  usage: {
    availability: "reported",
    runs: 1,
    reportedRuns: 1,
    unreportedRuns: 0,
    tokens: { availability: "reported", totalTokens: 2180, reportedRuns: 1, unreportedRuns: 0 },
    cost: { availability: "unavailable", knownRuns: 0, unknownRuns: 1 },
  },
  artifacts: [ARTIFACT],
  tools: [
    {
      seq: 3,
      eventId: "e3",
      type: "tool.start",
      occurredAt: "2026-11-06T00:00:00Z",
      toolCallId: "c1",
      toolName: "search_files",
    },
  ],
  approvals: [],
  compactions: [],
  diagnostics: [],
  errors: [],
  eventCount: 8,
  unavailable: [],
};

/** run 事件序列：用户消息由 StartSessionRun 之后的 UI 自己写入，这里只推后端事件。 */
const EVENTS = [
  ["message.delta", { text: "这个仓库有 3 处会真正断掉：鉴权头、分页契约、错误结构。" }],
  ["tool.start", { toolCallId: "c1", toolName: "search_files", args: { pattern: "api/v1/**" } }],
  [
    "tool.end",
    { toolCallId: "c1", toolName: "search_files", result: "src/api/v1/handlers/auth.ts", isError: false },
  ],
  [
    "tool.start",
    { toolCallId: "c2", toolName: "read_file", args: { path: "web/src/lib/api-client.ts" } },
  ],
  [
    "tool.end",
    { toolCallId: "c2", toolName: "read_file", text: "export async function listOrders(page, size)", isError: false },
  ],
  [
    "approval.request",
    {
      approvalId: "apr-1",
      toolName: "write_file",
      toolCallId: "c3",
      arguments: { path: "docs/migration/v1-to-v2-plan.md" },
      expiresAtUnix: 1_900_000_000,
    },
  ],
];

const FQN = "my-agent-go/internal/app.Desktop";

/** 方法名 → 只读返回值。未登记的方法返回结构化错误并记录（脚本会报出来）。 */
function respond(methodName, args) {
  const name = methodName.startsWith(`${FQN}.`) ? methodName.slice(FQN.length + 1) : methodName;
  switch (name) {
    case "Status":
      return STATUS;
    case "Settings":
      return SETTINGS;
    case "ListWorkspaces":
      return { items: [WORKSPACE] };
    case "CurrentWorkspace":
      return { item: WORKSPACE };
    case "ListProfiles":
      return { items: [] };
    case "ListModels":
      return { items: [CURRENT_MODEL.item], revision: 1, options: { apis: [], inputKinds: [], thinkingLevels: [] } };
    case "CurrentModel":
      return CURRENT_MODEL;
    case "ListSkills":
      return { items: [], revision: 0, pending: false, activeRun: false };
    case "ListExtensions":
      return { items: [], revision: 0 };
    case "GetPreferences":
      return { preferences: { locale: "zh-CN", theme: "light" }, revision: 1 };
    case "WorkbenchSummary":
      return {
        generatedAt: "2026-11-06T00:00:00Z",
        currentRuns: { availability: "available", count: 1 },
        pendingApprovals: { availability: "available", count: 1 },
        recentSessions: { availability: "available", count: 1 },
        recentArtifacts: { availability: "available", count: 1 },
        schedules: { availability: "unavailable", reasonCode: "SCHEDULES_UNSUPPORTED" },
        health: { availability: "available" },
        runs: [],
        approvals: [],
        sessions: [],
        artifacts: [],
        schedule: [],
        healthInfo: {
          activeRuns: 1,
          completed24h: 0,
          failed24h: 0,
          lastRunId: "run-1",
          lastState: "running",
          lastUpdatedAt: "2026-11-06T00:00:00Z",
        },
      };
    case "ListSessions":
      return { groups: [{ workspaceId: WORKSPACE.id, sessions: [SESSION] }], hasMore: false };
    case "DiscoverPiFast":
      return {
        state: "ready_verified",
        selected: PI_CANDIDATE,
        candidates: [PI_CANDIDATE],
        diagnostics: [],
        verification: "version_only",
      };
    case "BrowserState":
      // 浏览器分区需要原生宿主；网页侧没有原生子窗口，所以 available=false 并给出
      // 结构化的缺口原因（与真实"没有宿主"的报告形状一致）。
      return {
        available: false,
        slot: { x: 0, y: 0, width: 0, height: 0, revision: 0 },
        hasSlot: false,
        chromeReservedTop: 0,
        activeTab: "",
        tabs: [],
        control: {
          mode: "human",
          generation: 0,
          navigationId: "",
          queued: 0,
          inFlight: 0,
          canTakeOver: false,
          canResume: false,
        },
        visible: false,
        requestedVisible: false,
        overlays: [],
        history: [],
        capabilities: { surface: false, tabs: false, control: false },
        unsupported: [{ capability: "surface", reason: "BROWSER_API_MISSING" }],
        counters: {
          slotPresented: 0,
          slotDropped: 0,
          slotDeduped: 0,
          visibilityChanges: 0,
          overlayTransitions: 0,
          tabSelectDeduped: 0,
          tabsClosed: 0,
        },
        occlusion: [],
      };
    case "ListRunArtifacts":
      return { items: [ARTIFACT] };
    case "RunDetails":
      return { runId: "run-1", details: RUN_DETAILS };
    case "GetArtifact":
      return {
        item: {
          id: ARTIFACT.id,
          sha256: ARTIFACT.sha256,
          verification: { status: "verified", observedSha256: ARTIFACT.sha256, checkedAt: "2026-11-06T00:00:00Z" },
        },
      };
    case "StartSessionRun":
      return RUN_SNAPSHOT;
    case "PendingBrowserApprovals":
      // 没有真实的浏览器写动作在等批准（网页侧没有原生宿主）。
      return [];
    case "SetBrowserOverlay":
    case "UpdateBrowserSlot":
      return null;
    default:
      return { __unhandled: name, args };
  }
}

// ---------------------------------------------------------------- 测量

/**
 * 在页面里量一批盒子。
 *
 * 每个 region 给 {selector}；取计算样式与 rect。数字保留一位小数，
 * 因为 Chromium 的 rect 可能是 0.5 的倍数。
 */
const MEASURE = `(spec) => {
  const round = (v) => Math.round(v * 10) / 10;
  const px = (v) => {
    const n = parseFloat(String(v));
    return Number.isFinite(n) ? round(n) : null;
  };
  const out = {};
  const el = (sel) => (sel ? document.querySelector(sel) : null);
  const style = (sel) => (el(sel) ? getComputedStyle(el(sel)) : null);

  const inner = el(spec.threadInner);
  const innerS = style(spec.threadInner);
  out.threadInnerMaxWidth = innerS ? px(innerS.maxWidth) : null;
  out.threadInnerPaddingLeft = innerS ? px(innerS.paddingLeft) : null;
  out.threadInnerPaddingRight = innerS ? px(innerS.paddingRight) : null;
  out.threadInnerGap = innerS ? px(innerS.rowGap) : null;
  out.threadInnerWidth = inner ? round(inner.getBoundingClientRect().width) : null;

  const threadS = style(spec.thread);
  out.threadPaddingTop = threadS ? px(threadS.paddingTop) : null;

  // D2c：页头只允许有一条 52px 的带子。量线程的顶边（= 顶栏总高），以及
  // 文档里有几个 <header>、几个页头钩子。原型里这里是 52：.topbar 之后就是 .thread。
  out.threadTop = el(spec.thread) ? round(el(spec.thread).getBoundingClientRect().top) : null;
  out.topbarHeight = el(spec.topbar) ? round(el(spec.topbar).getBoundingClientRect().height) : null;
  out.headerCount = document.querySelectorAll("header").length;
  out.headerClasses = Array.from(document.querySelectorAll("header")).map((h) => h.className);
  out.headHookCount = spec.head ? document.querySelectorAll(spec.head).length : null;
  out.threadPaddingBottom = threadS ? px(threadS.paddingBottom) : null;
  out.threadPaddingLeft = threadS ? px(threadS.paddingLeft) : null;

  const layout = el(spec.layout);
  out.layoutColumns = layout ? getComputedStyle(layout).gridTemplateColumns : null;
  out.railWidth = layout && style(spec.rail)
    ? round(el(spec.rail).getBoundingClientRect().width)
    : null;
  const railHeadS = style(spec.railHead);
  out.railHeadHeight = railHeadS ? round(el(spec.railHead).getBoundingClientRect().height) : null;

  const composerS = style(spec.composer);
  out.composerMaxWidth = composerS ? px(composerS.maxWidth) : null;
  out.composerWidth = el(spec.composer) ? round(el(spec.composer).getBoundingClientRect().width) : null;
  const wrapS = style(spec.composerWrap);
  out.composerWrapPaddingTop = wrapS ? px(wrapS.paddingTop) : null;
  out.composerWrapPaddingLeft = wrapS ? px(wrapS.paddingLeft) : null;
  out.composerWrapPaddingBottom = wrapS ? px(wrapS.paddingBottom) : null;

  const msg = el(spec.msg);
  const msgS = msg ? getComputedStyle(msg) : null;
  out.msgGap = msgS ? px(msgS.rowGap) : null;
  out.msgCount = document.querySelectorAll(spec.msg).length;
  // 相邻两条消息之间的真实距离（第一条的底 → 第二条的顶）
  const msgs = Array.from(document.querySelectorAll(spec.msg));
  out.msgGapMeasured =
    msgs.length >= 2
      ? round(msgs[1].getBoundingClientRect().top - msgs[0].getBoundingClientRect().bottom)
      : null;

  const bubble = el(spec.bubble);
  const bubbleS = bubble ? getComputedStyle(bubble) : null;
  out.bubbleRadius = bubbleS ? px(bubbleS.borderTopLeftRadius) : null;
  out.bubblePaddingTop = bubbleS ? px(bubbleS.paddingTop) : null;
  out.bubblePaddingLeft = bubbleS ? px(bubbleS.paddingLeft) : null;
  out.bubbleFontSize = bubbleS ? round(parseFloat(bubbleS.fontSize)) : null;
  // 用户气泡靠右：气泡右边与内容区右边的距离（= padding-right）
  if (bubble && inner) {
    const b = bubble.getBoundingClientRect();
    const i = inner.getBoundingClientRect();
    const iS = getComputedStyle(inner);
    out.bubbleRightOffset = round(i.right - parseFloat(iS.paddingRight) - b.right);
  } else {
    out.bubbleRightOffset = null;
  }

  out.pageWidth = round(document.documentElement.clientWidth);
  return out;
}`;

async function measure(page, spec) {
  return page.evaluate(new Function("spec", `return (${MEASURE})(spec)`), spec);
}

const APP_SPEC = {
  topbar: '[data-shell-region="topbar"]',
  head: "[data-chat-head]",
  layout: "[data-chat-layout]",
  thread: "[data-chat-thread]",
  threadInner: "[data-thread-inner]",
  msg: '.achat-msg[data-role]',
  bubble: ".achat-bubble",
  composer: ".achat-composer",
  composerWrap: "[data-composer]",
  rail: "[data-context-panel]",
  railHead: ".achat-rail-head",
};

const PROTO_SPEC = {
  topbar: ".topbar",
  head: ".chat-head-main",
  layout: ".chat-layout",
  thread: ".thread",
  threadInner: ".thread-inner",
  msg: ".msg",
  bubble: ".msg-user .bubble",
  composer: ".composer",
  composerWrap: ".composer-wrap",
  rail: ".rail",
  railHead: ".rail-head",
};

/** 逐项对比：原型是基准，app 是实测。 */
const REGIONS = [
  "threadTop",
  "topbarHeight",
  "threadInnerMaxWidth",
  "threadInnerPaddingLeft",
  "threadInnerPaddingRight",
  "threadInnerGap",
  "threadPaddingTop",
  "threadPaddingBottom",
  "msgGap",
  "msgGapMeasured",
  "composerMaxWidth",
  "composerWrapPaddingTop",
  "composerWrapPaddingLeft",
  "composerWrapPaddingBottom",
  "railWidth",
  "railHeadHeight",
  "bubbleRadius",
  "bubblePaddingTop",
  "bubblePaddingLeft",
  "bubbleFontSize",
];

/**
 * 结构判据（D2c）：chat 路由只允许有一个 header，它就是这个顶栏；页头钩子只出现一次，
 * 而且线程紧贴在顶栏下面（threadTop == topbarHeight）。这三项与像素无关，所以不放进
 * REGIONS（那是原型 vs app 的数字对比），单独判并让脚本同样非零退出。
 */
function checkStructure(app) {
  const sameTop =
    typeof app.threadTop === "number" &&
    typeof app.topbarHeight === "number" &&
    Math.abs(app.threadTop - app.topbarHeight) <= TOLERANCE_PX;
  return {
    headerCount: app.headerCount,
    headerClasses: app.headerClasses,
    headHookCount: app.headHookCount,
    threadTop: app.threadTop,
    topbarHeight: app.topbarHeight,
    checks: {
      oneHeader: app.headerCount === 1,
      oneHeadHook: app.headHookCount === 1,
      threadRightBelowTopbar: sameTop,
    },
    pass: app.headerCount === 1 && app.headHookCount === 1 && sameTop,
  };
}

function compare(proto, app) {
  const rows = [];
  for (const region of REGIONS) {
    const p = proto[region];
    const a = app[region];
    const delta = p === null || a === null ? null : Math.round((a - p) * 10) / 10;
    rows.push({
      region,
      unit: region === "bubbleFontSize" ? "px" : "px",
      prototype: p,
      app: a,
      delta,
      tolerance: TOLERANCE_PX,
      pass: delta !== null && Math.abs(delta) <= TOLERANCE_PX,
    });
  }
  return rows;
}

// ---------------------------------------------------------------- 主流程

async function main() {
  if (!existsSync(join(DIST, "index.html"))) {
    console.error(`[chat-diff] 没有构建产物：先运行 npm run build（缺 ${join(DIST, "index.html")}）`);
    process.exit(2);
  }
  mkdirSync(OUT, { recursive: true });

  const appServer = await serve(DIST, 4190, { spaFallback: true });
  const protoServer = await serve(PROTO, 4191);

  const executablePath = findChromium();
  const browser = await chromium.launch(executablePath ? { executablePath } : {});
  const report = {
    task: "D2b",
    kind: "web-build-chat-diff",
    generatedAt: new Date().toISOString(),
    environment: {
      browser: "",
      appUrl: `${APP_ORIGIN}/ + 侧栏历史点进会话 sess-1`,
      prototypeUrl: "http://127.0.0.1:4191/chat.html",
      backend: "Wails runtime HTTP 传输被本脚本的只读 fixture 接管（POST /wails/runtime）",
      theme: "light",
      limits: [
        "网页侧证据：同一份 React 构建产物 + Chromium，不含 Wails 宿主/WebView2/DPI 缩放",
        "run 事件用 window._wails.dispatchWailsEvent 推入：形状与 Go contracts.Event 相同，内容是本脚本的 fixture",
        "浏览器分区没有原生宿主，如实显示 unavailable（不是几何差异）",
      ],
    },
    tolerance: { px: TOLERANCE_PX },
    rows: [],
  };

  const unhandled = new Set();

  for (const vp of VIEWPORTS) {
    const tag = `${vp.width}x${vp.height}`;
    const context = await browser.newContext({ viewport: vp, deviceScaleFactor: 1 });
    const page = await context.newPage();
    if (DEBUG) {
      page.on("console", (m) => console.log(`[app:${m.type()}]`, m.text().slice(0, 300)));
      page.on("pageerror", (e) => console.log("[app:pageerror]", String(e).slice(0, 400)));
    }
    report.environment.browser ||= await page.evaluate(() => navigator.userAgent);

    await page.route("**/wails/runtime", async (route) => {
      const body = JSON.parse(route.request().postData() ?? "{}");
      const payload = body?.args ?? {};
      const methodName = payload.methodName ?? "";
      const result = respond(methodName, payload.args ?? []);
      if (result && typeof result === "object" && "__unhandled" in result) {
        unhandled.add(result.__unhandled);
      }
      // Events 对象（object 3）的调用在网页侧没有后端：返回 null 即可。
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(body?.object === 3 ? null : result),
      });
    });

    await page.goto(`${APP_ORIGIN}/`, { waitUntil: "domcontentloaded" });
    // 路由是应用内状态（不是 URL）：像用户那样从侧栏历史点进这个会话。
    const historyEntry = page
      .locator('[data-shell-region="history"] button', { hasText: SESSION.title })
      .first();
    await historyEntry.waitFor({ state: "visible", timeout: 30_000 });
    await historyEntry.click();
    try {
      await page.waitForSelector("[data-thread-inner]", { timeout: 30_000 });
    } catch (e) {
      // 失败时留下现场：截图 + 页面文本（否则只有一句 "timeout"）。
      await page.screenshot({ path: join(OUT, `app-chat-${tag}-failed.png`) });
      const text = await page.evaluate(() => document.body.innerText);
      writeFileSync(join(OUT, `app-chat-${tag}-failed.txt`), text);
      console.error(`[chat-diff] 等待 [data-thread-inner] 超时；页面文本：
${text.slice(0, 1200)}`);
      throw e;
    }

    // 发送一条消息：走产品自己的 StartSessionRun 路径（fixture 返回快照）。
    await page.fill("#composer-input", "读一下 api/v1/handlers 下的所有入口，评估升级到 v2 会破坏哪些调用点。");
    await page.press("#composer-input", "Enter");
    await page.waitForTimeout(300);

    // 推后端事件（形状 = contracts.Event），让消息/工具卡/审批卡按真实顺序出现。
    await page.evaluate((events) => {
      const now = Date.now();
      events.forEach(([type, payload], i) => {
        window._wails.dispatchWailsEvent({
          name: "myagent:run-event",
          data: {
            schemaVersion: 1,
            eventId: `ev-${i}`,
            workspaceId: "ws-a",
            sessionId: "sess-1",
            runId: "run-1",
            seq: i + 1,
            type,
            occurredAt: new Date(now + i).toISOString(),
            payload,
          },
        });
      });
    }, EVENTS);
    // 事件到达之后要等三条都渲染出来再量：少一条就会出现 null（一次假失败，
    // 但假失败和真失败在证据里分不出来，所以这里必须等确定的东西）。
    await page.waitForSelector(".achat-bubble", { timeout: 15_000 });
    await page.waitForSelector("[data-tool-card]", { timeout: 15_000 });
    await page.waitForSelector("[data-approval-card]", { timeout: 15_000 });
    await page.waitForTimeout(200);

    // 截图与量测都把线程滚到顶部：原型截图也在顶部，否则比的是两张不同的画面。
    await page.evaluate(() => {
      const thread = document.querySelector("[data-chat-thread]");
      if (thread) thread.scrollTop = 0;
    });
    await page.waitForTimeout(150);

    // 量右栏（默认开栏）——先确保开栏状态确实是 open。
    const railState = await page.getAttribute("[data-chat-layout]", "data-rail");
    const appValues = await measure(page, APP_SPEC);
    await page.screenshot({ path: join(OUT, `app-chat-${tag}.png`), fullPage: false });

    // 原型：同一视口
    const protoPage = await context.newPage();
    await protoPage.goto("http://127.0.0.1:4191/chat.html", { waitUntil: "domcontentloaded" });
    await protoPage.waitForSelector(".thread-inner", { timeout: 30_000 });
    await protoPage.waitForTimeout(400);
    const protoValues = await measure(protoPage, PROTO_SPEC);
    await protoPage.screenshot({ path: join(OUT, `prototype-chat-${tag}.png`), fullPage: false });

    report.rows.push({
      viewport: vp,
      screenshots: {
        app: `app-chat-${tag}.png`,
        prototype: `prototype-chat-${tag}.png`,
      },
      app: { railState, ...appValues },
      prototype: protoValues,
      structure: checkStructure(appValues),
      regions: compare(protoValues, appValues),
    });

    await context.close();
  }

  await browser.close();
  appServer.close();
  protoServer.close();

  report.unhandledMethods = Array.from(unhandled);
  const failed = report.rows.flatMap((row) => [
    ...row.regions
      .filter((r) => !r.pass)
      .map((r) => `${row.viewport.width}x${row.viewport.height} ${r.region}`),
    ...Object.entries(row.structure.checks)
      .filter(([, ok]) => !ok)
      .map(([name]) => `${row.viewport.width}x${row.viewport.height} structure:${name}`),
  ]);
  report.failedRegions = failed;

  writeFileSync(join(OUT, "chat-diff.json"), JSON.stringify(report, null, 2) + "\n");

  for (const row of report.rows) {
    const bad = row.regions.filter((r) => !r.pass);
    console.log(
      `[chat-diff] ${row.viewport.width}x${row.viewport.height}: ${row.regions.length - bad.length}/${row.regions.length} 项在 ${TOLERANCE_PX}px 内`
    );
    for (const r of bad) console.log(`  Δ ${r.region}: 原型 ${r.prototype} / app ${r.app}（${r.delta}）`);
    const s = row.structure;
    console.log(
      `[chat-diff] 结构: header=${s.headerCount}（${s.headerClasses.map((c) => c || "(无类名)").join(" + ")}） head 钩子=${s.headHookCount} thread.top=${s.threadTop} 顶栏=${s.topbarHeight} → ${s.pass ? "PASS" : "FAIL"}`
    );
  }

  // D2c：与"改前"那一次实测（另一个 --out 目录）合成本任务的专门证据。
  if (headDiffBefore) {
    writeHeadDiff(report, headDiffBefore);
  }

  if (report.unhandledMethods.length) {
    console.log(`[chat-diff] 未登记的绑定方法（fixture 缺口）：${report.unhandledMethods.join(", ")}`);
  }
  console.log(`[chat-diff] 证据写在 ${OUT}`);
  process.exit(failed.length === 0 ? 0 : 1);
}

/**
 * `chat-head-diff.json`（D2c 证据）：同一视口下，改前 / 改后 / 原型的 thread.top。
 *
 * 为什么要求一个 --head-diff-before 目录：
 * "改前"的数字必须是真跑出来的（当时的 build + 当时的组件），不能手写；
 * 这里只负责把两次实测摆到一起算差，而不是让报告里出现推演数字。
 */
function writeHeadDiff(after, beforeDir) {
  const beforePath = join(beforeDir, "chat-diff.json");
  if (!existsSync(beforePath)) {
    console.error(`[chat-diff] --head-diff-before 目录里没有 chat-diff.json：${beforePath}`);
    process.exitCode = 2;
    return;
  }
  const before = JSON.parse(readFileSync(beforePath, "utf8"));
  const pick = (row) => ({
    threadTop: row.app.threadTop,
    topbarHeight: row.app.topbarHeight,
    headerCount: row.app.headerCount,
    headHookCount: row.app.headHookCount,
  });
  const viewports = after.rows.map((row) => {
    const tag = `${row.viewport.width}x${row.viewport.height}`;
    const pre = before.rows.find((r) => `${r.viewport.width}x${r.viewport.height}` === tag);
    const preValues = pre ? pick(pre) : null;
    const postValues = pick(row);
    return {
      viewport: row.viewport,
      before: preValues,
      after: postValues,
      prototype: { threadTop: row.prototype.threadTop, topbarHeight: row.prototype.topbarHeight },
      delta: {
        threadTopBeforeMinusPrototype:
          preValues && row.prototype.threadTop !== null
            ? Math.round((preValues.threadTop - row.prototype.threadTop) * 10) / 10
            : null,
        threadTopAfterMinusPrototype:
          postValues.threadTop !== null && row.prototype.threadTop !== null
            ? Math.round((postValues.threadTop - row.prototype.threadTop) * 10) / 10
            : null,
      },
      pass:
        preValues !== null &&
        postValues.headerCount === 1 &&
        postValues.headHookCount === 1 &&
        row.prototype.threadTop !== null &&
        Math.abs(postValues.threadTop - row.prototype.threadTop) <= TOLERANCE_PX,
    };
  });
  const out = {
    task: "D2c",
    kind: "chat-head-diff",
    generatedAt: new Date().toISOString(),
    question: "对话页的页头是不是就长在壳层 52px 顶栏里：.thread 的 top 是否等于原型渲染值 52，文档里是否只剩一个 header",
    beforeRun: { dir: beforeDir, generatedAt: before.generatedAt },
    afterRun: { dir: OUT, generatedAt: after.generatedAt },
    tolerance: { px: TOLERANCE_PX },
    viewports,
    pass: viewports.every((v) => v.pass),
  };
  writeFileSync(join(OUT, "chat-head-diff.json"), JSON.stringify(out, null, 2) + "\n");
  console.log(
    `[chat-head-diff] ${out.pass ? "PASS" : "FAIL"}：${viewports
      .map((v) => `${v.viewport.width}x${v.viewport.height} thread.top ${v.before?.threadTop} → ${v.after.threadTop}（原型 ${v.prototype.threadTop}）`)
      .join("；")}`
  );
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
