// 用 node scripts/check-ui-contract.mjs 运行（不写 shebang：Vite 的 transform 会把它当非法 token）
// UI 契约检查（U00）：把两类"看起来能用、实际会烂掉"的约定变成可执行门禁。
//
// 1) 用户可见文案必须来自 i18n catalog。
//    扫描 frontend/src 下的 .ts/.tsx（跳过测试与 locale JSON），找出：
//      - 含中日韩字符的字符串字面量/模板字符串；
//      - JSX 文本节点（>文字<）；
//      - aria-label / title / placeholder / alt 等面向用户的属性值。
//    注释里的中文**不算**（注释不是文案），因此扫描器先做词法级屏蔽。
//
//    历史页面（U01–U07 会逐页重写）还没迁完，所以用 scripts/ui-copy-baseline.json
//    记录每个文件的**既有条数**。规则是只许减不许增：
//      - 没登记的文件出现文案 → 失败（新代码必须走 catalog）；
//      - 已登记文件条数变多 → 失败（不许在旧页面里继续堆字面量）；
//      - 条数变少或文件消失 → 失败，要求同步下调/删除 baseline
//        （否则这份清单会变成一张永久豁免书）。
//
// 2) 每个 Go 错误码都要有两种语言的文案。
//    错误码从 internal/contracts/errors.go 解析，不手抄——手抄的清单一定会过期。
//
// 用法：
//   node scripts/check-ui-contract.mjs           # 人类可读，失败退出 1
//   node scripts/check-ui-contract.mjs --json    # 机器可读（证据里留档用）

import { readdirSync, readFileSync, statSync, existsSync } from "node:fs";
import { dirname, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(HERE, "..");
const FRONTEND_SRC = join(REPO_ROOT, "frontend", "src");
const LOCALES = ["zh-CN", "en-US"];
const BASELINE_PATH = join(HERE, "ui-copy-baseline.json");
const ERRORS_GO = join(REPO_ROOT, "internal", "contracts", "errors.go");

const CJK = /[\u3400-\u9fff\uf900-\ufaff]/;

// 开发者日志文案的豁免标记：只对**同一行里确实在写日志/抛错**的语句生效。
//
// 日志不是用户文案（用户在界面上看不到，诊断包里也只有开发者读），
// 但豁免必须窄：要求同行出现 console.*/log.*，否则这行标记就成了
// "任何文案都能绕过 catalog"的后门。
const DEV_ONLY_MARKER = "ui-copy: dev-only";
const DEV_LOG_CALL = /\b(console\.(warn|error|log|info|debug)|log\.(Print|Printf|Println|Fatalf|Errorf))\b/;

// ---------------------------------------------------------------- 词法扫描

/**
 * 屏蔽注释与字符串内容，返回：
 *  - masked：与源码等长，注释与字符串体被替换成空格（行号不变）；
 *  - strings：字符串字面量列表（含行号与值），用于 CJK 判定。
 *
 * 单遍状态机而不是正则链：`"http://x"` 里的 `//` 不是注释，
 * 用正则先删注释会把 URL 截断，进而出现莫名其妙的"语法错误"式误报。
 */
export function tokenize(src) {
  const out = src.split("");
  const strings = [];
  let i = 0;
  let line = 1;
  const n = src.length;
  const blank = (from, to) => {
    for (let k = from; k < to; k++) if (out[k] !== "\n") out[k] = " ";
  };
  while (i < n) {
    const c = src[i];
    if (c === "/" && src[i + 1] === "/") {
      const start = i;
      while (i < n && src[i] !== "\n") i++;
      blank(start, i);
      continue;
    }
    if (c === "/" && src[i + 1] === "*") {
      const start = i;
      const startLine = line;
      i += 2;
      while (i < n && !(src[i] === "*" && src[i + 1] === "/")) {
        if (src[i] === "\n") line++;
        i++;
      }
      i = Math.min(i + 2, n);
      blank(start, i);
      continue;
    }
    if (c === '"' || c === "'") {
      const start = i;
      const startLine = line;
      i++;
      let value = "";
      while (i < n && src[i] !== c) {
        if (src[i] === "\\") {
          value += src[i] ?? "";
          i++;
        }
        if (i < n) {
          value += src[i] ?? "";
          if (src[i] === "\n") line++;
          i++;
        }
      }
      i++;
      strings.push({ value, start, line: startLine, quote: c });
      blank(start, i);
      continue;
    }
    if (c === "`") {
      const start = i;
      const startLine = line;
      i++;
      let value = "";
      while (i < n && src[i] !== "`") {
        if (src[i] === "\\") {
          value += src[i] ?? "";
          i++;
        }
        if (i < n) {
          value += src[i] ?? "";
          if (src[i] === "\n") line++;
          i++;
        }
      }
      i++;
      strings.push({ value, start, line: startLine, quote: "`" });
      blank(start, i);
      continue;
    }
    if (c === "\n") line++;
    i++;
  }
  return { masked: out.join(""), strings };
}

function lineOf(masked, index) {
  let count = 1;
  for (let k = 0; k < index && k < masked.length; k++) if (masked[k] === "\n") count++;
  return count;
}

// JSX 文本节点：`>` 与 `<` 之间、不含表达式分隔符的一段。
// 收紧条件是刻意的：宁可漏（由 baseline 兜住）也不要对 `a < b && c > d` 误报。
const JSX_TEXT = />([^<>{}=(){};|&!?:*+%$#@\\[\]]*)</g;

// 面向用户的属性；技术属性（role/tabIndex/className/...）不在其中。
const USER_ATTR =
  /\b(aria-label|aria-description|aria-placeholder|title|placeholder|alt|label|description|heading|caption|summary)\s*=\s*(?:"([^"]*)"|'([^']*)'|\{\s*`([^`]*)`\s*\}|\{\s*"([^"]*)"\s*\})/g;

/** 扫描一段源码，返回该文件里的用户可见文案位置。 */
export function scanSource(relPath, src) {
  const problems = [];
  const { masked, strings } = tokenize(src);
  const rawLines = src.split("\n");
  const maskedLines = masked.split("\n");
  const isDevOnly = (line) => {
    const raw = rawLines[line - 1] ?? "";
    if (!raw.includes(DEV_ONLY_MARKER)) return false;
    return DEV_LOG_CALL.test(maskedLines[line - 1] ?? "");
  };

  for (const s of strings) {
    if (CJK.test(s.value) && !isDevOnly(s.line)) {
      problems.push({
        file: relPath,
        line: s.line,
        rule: "string-literal",
        text: s.value.trim().slice(0, 120),
      });
    }
  }

  JSX_TEXT.lastIndex = 0;
  let m;
  while ((m = JSX_TEXT.exec(masked)) !== null) {
    const text = m[1].trim();
    if (text.length < 2) continue;
    if (!/[A-Za-z\u3400-\u9fff]/.test(text)) continue;
    // 纯小写短标识（div/span 之类被误当成文本时）不算文案。
    if (/^[a-z-]{1,3}$/.test(text)) continue;
    const line = lineOf(masked, m.index);
    if (isDevOnly(line)) continue;
    problems.push({
      file: relPath,
      line,
      rule: "jsx-text",
      text,
    });
  }

  USER_ATTR.lastIndex = 0;
  while ((m = USER_ATTR.exec(src)) !== null) {
    const value = (m[2] ?? m[3] ?? m[4] ?? m[5] ?? "").trim();
    if (!/[A-Za-z\u3400-\u9fff]{2,}/.test(value)) continue;
    if (CJK.test(value) === false && /^[a-z0-9-]+$/.test(value)) continue;
    const line = lineOf(masked, m.index);
    if (isDevOnly(line)) continue;
    problems.push({
      file: relPath,
      line,
      rule: `attr:${m[1]}`,
      text: value.slice(0, 120),
    });
  }

  return problems;
}

// ---------------------------------------------------------------- 文件遍历

function walk(dir, acc = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      // i18n/locales 是文案的合法归属地；node_modules 不该出现但排除掉更稳。
      if (name === "node_modules" || name === "dist") continue;
      walk(p, acc);
      continue;
    }
    if (!/\.(ts|tsx)$/.test(p)) continue;
    if (/\.test\.(ts|tsx)$/.test(p)) continue;
    if (/\.d\.ts$/.test(p)) continue;
    if (p.endsWith(join("src", "test-setup.ts"))) continue;
    acc.push(p);
  }
  return acc;
}

function relFromSrc(file) {
  return relative(FRONTEND_SRC, file).split(sep).join("/");
}

/**
 * 扫描 frontend/src，返回全部文案违规（未考虑 baseline）。
 *
 * root 可覆盖（测试用）；默认是仓库里的 frontend/src。
 */
export function scanUserCopy(root = FRONTEND_SRC) {
  if (!existsSync(root)) return [];
  const out = [];
  for (const file of walk(root)) {
    const problems = scanSource(relFromSrc(file), readFileSync(file, "utf8"));
    out.push(...problems);
  }
  out.sort((a, b) => (a.file === b.file ? a.line - b.line : a.file < b.file ? -1 : 1));
  return out;
}

/** 按文件统计条数（baseline 的形状）。 */
export function countByFile(problems) {
  const counts = {};
  for (const p of problems) counts[p.file] = (counts[p.file] ?? 0) + 1;
  return counts;
}

export function loadBaseline(path = BASELINE_PATH) {
  if (!existsSync(path)) return { files: {} };
  return JSON.parse(readFileSync(path, "utf8"));
}

/**
 * 对比 baseline：任何"新增文案"、"条数增加"、"baseline 过期"都算失败。
 *
 * baseline 只允许减：迁移完一个页面就该把它的条数改小或删掉条目，
 * 否则这份文件会慢慢变成"当初就是这样"的豁免清单。
 */
export function checkBaseline(problems, baseline = loadBaseline()) {
  const issues = [];
  const counts = countByFile(problems);
  const allowed = baseline.files ?? {};

  for (const [file, count] of Object.entries(counts)) {
    const limit = allowed[file];
    if (limit === undefined) {
      issues.push({
        kind: "unlisted-copy",
        file,
        detail: `出现 ${count} 处用户可见文案，但 catalog 之外不允许新文案`,
      });
      continue;
    }
    if (count > limit) {
      issues.push({
        kind: "copy-increased",
        file,
        detail: `文案条数从 ${limit} 增加到 ${count}；新文案必须进 i18n catalog`,
      });
    } else if (count < limit) {
      issues.push({
        kind: "baseline-stale",
        file,
        detail: `文案条数从 ${limit} 降到 ${count}；请同步下调 scripts/ui-copy-baseline.json`,
      });
    }
  }

  for (const file of Object.keys(allowed)) {
    if (!(file in counts)) {
      issues.push({
        kind: "baseline-stale",
        file,
        detail: "该文件已经没有字面量文案（或已被删除）；请从 baseline 里移除条目",
      });
    }
  }

  return issues;
}

// ---------------------------------------------------------------- Go 错误码

/**
 * 解析 internal/contracts/errors.go 里的错误码常量。
 *
 * 只认 `CodeXxx = "VALUE"` 形状（值是大写常量）：注释掉的、别处定义的
 * 字符串字面量都不算，避免把文档里的示例当成真实错误码。
 */
export function goErrorCodes(goSource = readFileSync(ERRORS_GO, "utf8")) {
  const codes = new Set();
  const re = /^\s*Code[A-Za-z0-9]+\s*=\s*"([A-Z][A-Z0-9_]*)"/gm;
  let m;
  while ((m = re.exec(goSource)) !== null) codes.add(m[1]);
  return [...codes].sort();
}

function flatten(catalog, prefix = "") {
  const out = [];
  for (const [key, value] of Object.entries(catalog)) {
    const path = prefix ? `${prefix}.${key}` : key;
    if (typeof value === "string") out.push(path);
    else out.push(...flatten(value, path));
  }
  return out;
}

/** 检查两种语言是否都覆盖了每个 Go 错误码。 */
export function checkErrorMessages(codes = goErrorCodes()) {
  const issues = [];
  for (const locale of LOCALES) {
    const path = join(FRONTEND_SRC, "i18n", "locales", `${locale}.json`);
    const keys = new Set(flatten(JSON.parse(readFileSync(path, "utf8"))));
    for (const code of codes) {
      if (!keys.has(`errors.${code}`)) {
        issues.push({ kind: "missing-error-message", locale, file: `errors.${code}` });
      }
    }
  }
  return issues;
}

/** 检查两种语言的 key 集合完全一致。 */
export function checkLocaleParity() {
  const sets = LOCALES.map((locale) => {
    const path = join(FRONTEND_SRC, "i18n", "locales", `${locale}.json`);
    return { locale, keys: new Set(flatten(JSON.parse(readFileSync(path, "utf8")))) };
  });
  const issues = [];
  const [first, ...rest] = sets;
  for (const other of rest) {
    for (const key of first.keys) {
      if (!other.keys.has(key)) {
        issues.push({ kind: "locale-parity", file: key, detail: `${first.locale} 有、${other.locale} 没有` });
      }
    }
    for (const key of other.keys) {
      if (!first.keys.has(key)) {
        issues.push({ kind: "locale-parity", file: key, detail: `${other.locale} 有、${first.locale} 没有` });
      }
    }
  }
  return issues;
}

// ---------------------------------------------------------------- 入口

/** 跑全部检查，返回结构化结果（测试与 CLI 共用同一份判定）。 */
export function checkAll() {
  const problems = scanUserCopy();
  const baselineIssues = checkBaseline(problems);
  const errorIssues = checkErrorMessages();
  const parityIssues = checkLocaleParity();
  return {
    ok: baselineIssues.length === 0 && errorIssues.length === 0 && parityIssues.length === 0,
    copyViolations: problems,
    baselineIssues,
    errorIssues,
    parityIssues,
    counts: countByFile(problems),
    errorCodes: goErrorCodes(),
  };
}

function main(argv) {
  const result = checkAll();
  if (argv.includes("--json")) {
    console.log(JSON.stringify(result, null, 2));
    process.exit(result.ok ? 0 : 1);
  }

  const files = Object.keys(result.counts).length;
  console.log(
    `[ui-contract] 扫描 ${files} 个含字面量文案的文件（baseline 内 ${Object.keys(loadBaseline().files ?? {}).length} 个历史页面）`
  );
  console.log(`[ui-contract] Go 错误码 ${result.errorCodes.length} 个，覆盖检查通过`);
  for (const issue of result.baselineIssues) {
    console.error(`[FAIL] ${issue.kind}: ${issue.file} — ${issue.detail}`);
  }
  for (const issue of result.errorIssues) {
    console.error(`[FAIL] ${issue.kind}: ${issue.locale} 缺少 ${issue.file}`);
  }
  for (const issue of result.parityIssues) {
    console.error(`[FAIL] ${issue.kind}: ${issue.file} — ${issue.detail}`);
  }
  if (result.ok) {
    console.log("[PASS] UI 契约：catalog 之外的文案增量为 0，两种语言覆盖全部错误码");
    process.exit(0);
  }
  process.exit(1);
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  main(process.argv.slice(2));
}
