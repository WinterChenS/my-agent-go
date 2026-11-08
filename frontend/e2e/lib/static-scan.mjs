// 静态扫描（U07 预检）。
//
// 四类"看起来能用、实际是假的"写法必须在**源码**层面就被拦住，因为它们在
// 运行时看起来完全正常：
//   1) mock-import        生产代码引用 mock/demo/stub/fixture 模块 → 界面数字不是真的
//   2) hardcoded-metric   源码里写死产品指标（1,284 runs / 98%）→ 演示数据冒充真实数据
//   3) direct-color       组件直接写颜色值/调色板类名 → 主题切换必然失控（THEME01）
//   4) message-key-drift  t("key") 用了 catalog 里不存在的 key → 界面上出现 key 本身
//
// 每一条都必须先在 frontend/e2e/fixtures/violations/ 的故意违规夹具上失败，
// 再对生产源码运行（见 fixtures.spec.ts）。检查在夹具上不红 = 这条检查没有意义。
//
// 口径纪律：扫描器只读源码文本，不执行它；发现项带 file:line，便于直接改。

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative, sep } from "node:path";

const SKIP_DIRS = new Set(["node_modules", ".git", "dist", "coverage"]);
const TEST_FILE = /(\.test\.|\.spec\.)[cm]?[jt]sx?$/;

function walk(root, out = []) {
  for (const name of readdirSync(root)) {
    if (SKIP_DIRS.has(name)) continue;
    const p = join(root, name);
    const st = statSync(p);
    if (st.isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

function lineOf(src, index) {
  let line = 1;
  for (let i = 0; i < index && i < src.length; i++) if (src[i] === "\n") line++;
  return line;
}

function relPath(root, file) {
  return relative(root, file).split(sep).join("/");
}

// 词法级屏蔽注释与字符串内容，行号不变（复用 check-ui-contract 的思路：
// 正则先删注释会在 "https://" 上出错）。
export function maskComments(src) {
  const out = src.split("");
  let i = 0;
  const n = src.length;
  while (i < n) {
    const c = src[i];
    if (c === "/" && src[i + 1] === "/") {
      while (i < n && src[i] !== "\n") {
        out[i] = " ";
        i++;
      }
      continue;
    }
    if (c === "/" && src[i + 1] === "*") {
      i += 2;
      while (i < n && !(src[i] === "*" && src[i + 1] === "/")) {
        if (src[i] !== "\n") out[i] = " ";
        i++;
      }
      i += 2;
      continue;
    }
    i++;
  }
  return out.join("");
}

// ---------------------------------------------------------------- 1) mock 引用

const MOCK_PATH = /(^|[/.])(mock|mocks|demo|demos|stub|stubs|fixtures?|faker|sample-data)([/.]|$)/i;
const MOCK_IDENT = /\b(demo[A-Z][A-Za-z0-9_]*|mock[A-Z][A-Za-z0-9_]*|MOCK_[A-Z0-9_]+|fake[A-Z][A-Za-z0-9_]*)\b/;
const IMPORT_RE = /\b(?:import|export)\s[^;]*?from\s*["']([^"']+)["']|\brequire\s*\(\s*["']([^"']+)["']\s*\)/g;

export function scanMockImports(root, files) {
  const findings = [];
  for (const file of files) {
    const src = readFileSync(file, "utf8");
    const masked = maskComments(src);
    IMPORT_RE.lastIndex = 0;
    let m;
    while ((m = IMPORT_RE.exec(masked)) !== null) {
      const spec = m[1] || m[2] || "";
      // 测试与 e2e 夹具允许引用夹具模块；生产源码不允许。
      if (MOCK_PATH.test(spec)) {
        findings.push({
          rule: "mock-import",
          file: relPath(root, file),
          line: lineOf(masked, m.index),
          detail: `引用了 mock/demo/stub 模块: ${spec}`,
        });
      }
    }
    // 直接具名 import 演示数据（路径看不出来，但标识符是自曝的）。
    const masked2 = maskComments(src);
    const ident = MOCK_IDENT.exec(masked2);
    if (ident) {
      findings.push({
        rule: "mock-import",
        file: relPath(root, file),
        line: lineOf(masked2, ident.index),
        detail: `出现演示数据标识符: ${ident[1]}`,
      });
    }
  }
  return findings;
}

// ---------------------------------------------------------------- 2) 硬编码指标

// 只认"产品指标"的形态，避免把 CSS 数字、字号、版本号当成指标：
//   - 千分位数字（1,284）
//   - 带百分号的数字（98%）
//   - 数字 + 业务单位（runs / sessions / tokens / ms / 秒 / 次 / 条 / 个）
const METRIC_PATTERNS = [
  { re: /\b\d{1,3}(?:,\d{3})+\b/, why: "千分位统计数字" },
  { re: /\b\d{1,3}(?:\.\d+)?\s*%/, why: "百分比统计数字" },
  {
    re: /\b\d{1,6}\s*(?:runs?|sessions?|messages?|tokens?|requests?|calls?|分钟|秒|次|条|个会话|会话)\b/i,
    why: "数字 + 业务单位",
  },
];

export function scanHardcodedMetrics(root, files) {
  const findings = [];
  for (const file of files) {
    const src = readFileSync(file, "utf8");
    const masked = maskComments(src);
    for (const { re, why } of METRIC_PATTERNS) {
      const g = new RegExp(re.source, re.flags.includes("g") ? re.flags : re.flags + "g");
      let m;
      while ((m = g.exec(masked)) !== null) {
        // Tailwind 的任意值宽度（max-w-[85%]）不是"产品指标"：它写在类名里，
        // 前后被方括号包着。把它算成硬编码统计会让这条检查变成噪音。
        const before = masked[m.index - 1] ?? "";
        const after = masked[m.index + m[0].length] ?? "";
        if (before === "[" || before === "(" || after === "]" || after === ")") continue;
        findings.push({
          rule: "hardcoded-metric",
          file: relPath(root, file),
          line: lineOf(masked, m.index),
          detail: `${why}: ${JSON.stringify(m[0])}`,
        });
      }
    }
  }
  return findings;
}

// ---------------------------------------------------------------- 3) 直接颜色值

// 允许的例外：index.css 是 token 定义处（颜色值本来就该写在那里）。
const COLOR_ALLOW = [/index\.css$/, /tokens\.css$/];
const HEX_COLOR = /#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})\b/g;
const FUNC_COLOR = /\b(?:rgb|rgba|hsl|hsla)\s*\(/g;
// Tailwind 的调色板类名（text-slate-200 / bg-zinc-900 / border-white/10）绕过了 token。
const PALETTE_CLASS = /\b(?:bg|text|border|ring|from|to|via|placeholder|divide|outline|shadow)-(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|white|black)(?:-[0-9]{2,3})?(?:\/[0-9]{1,3})?\b/g;

export function scanDirectColors(root, files) {
  const findings = [];
  for (const file of files) {
    if (COLOR_ALLOW.some((re) => re.test(file))) continue;
    const src = readFileSync(file, "utf8");
    const masked = maskComments(src);
    for (const re of [HEX_COLOR, FUNC_COLOR, PALETTE_CLASS]) {
      const g = new RegExp(re.source, "g");
      let m;
      while ((m = g.exec(masked)) !== null) {
        findings.push({
          rule: "direct-color",
          file: relPath(root, file),
          line: lineOf(masked, m.index),
          detail: `直接颜色/调色板类名: ${JSON.stringify(m[0])}`,
        });
      }
    }
  }
  return findings;
}

// ---------------------------------------------------------------- 4) 消息 key 漂移

function flatten(obj, prefix = "", out = new Set()) {
  for (const [k, v] of Object.entries(obj)) {
    const p = prefix ? `${prefix}.${k}` : k;
    if (typeof v === "string") out.add(p);
    else if (v && typeof v === "object") flatten(v, p, out);
  }
  return out;
}

// 模板 key（t(`nav.${feature}`)）无法静态判定，只检查字面量 key。
const T_CALL = /\bt\(\s*["']([A-Za-z0-9_.]+)["']/g;

export function scanMessageKeys(root, files, catalogs) {
  const findings = [];
  const known = new Set();
  for (const cat of catalogs) for (const k of flatten(cat)) known.add(k);
  for (const file of files) {
    const src = readFileSync(file, "utf8");
    const masked = maskComments(src);
    T_CALL.lastIndex = 0;
    let m;
    while ((m = T_CALL.exec(masked)) !== null) {
      const key = m[1];
      if (!known.has(key)) {
        findings.push({
          rule: "message-key-drift",
          file: relPath(root, file),
          line: lineOf(masked, m.index),
          detail: `catalog 里没有的 key: ${key}`,
        });
      }
    }
  }
  return findings;
}

// ---------------------------------------------------------------- 入口

export function sourceFiles(root) {
  return walk(root).filter((f) => /\.(ts|tsx)$/.test(f) && !TEST_FILE.test(f));
}

export function localeFiles(frontendRoot) {
  return ["zh-CN", "en-US"].map((l) => join(frontendRoot, "src", "i18n", "locales", `${l}.json`));
}

export function scanAll({ sourceRoot, catalogs }) {
  const files = sourceFiles(sourceRoot);
  return [
    ...scanMockImports(sourceRoot, files),
    ...scanHardcodedMetrics(sourceRoot, files),
    ...scanDirectColors(sourceRoot, files),
    ...(catalogs ? scanMessageKeys(sourceRoot, files, catalogs) : []),
  ];
}
