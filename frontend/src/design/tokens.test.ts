// 设计令牌与外壳版式的守卫测试（D1）。
//
// 这一组断言解决的是"抄错了但看起来还行"：原型是视觉契约，令牌值抄错一个数字，
// 界面只是"不太像"，没有任何测试会自己发现。所以这里**解析两边的源码文本**：
//
//   docs/ui/Agent-客户端产品原型/assets/studio.css  ← 契约（只读）
//   frontend/src/design/tokens.ts                  ← 契约的逐字副本
//   frontend/src/index.css                         ← 契约的 sRGB 投影（组件真正消费的变量）
//
// 任何一侧改动而另一侧没跟上，这组测试就红。深色主题没有契约来源（原型只有一份
// :root），所以深色走锚点推导，测试锁的是"色相彩度不变 + 明度锚点 + 明度序 + AA"。

import { describe, expect, it } from "vitest";
import { FRONTEND_ROOT, readContract, shellSourceFiles } from "./tokens.files.mjs";
import {
  COLOR_ALIASES,
  CONTRAST_PAIRS,
  DARK_LIGHTNESS,
  DERIVED_COLORS,
  FONT_STACKS,
  GEOMETRY,
  PROTO_LIGHT,
  contrastRatio,
  oklchOf,
  parseOklch,
  srgbOf,
  type ProtoColorName,
} from "./tokens";

const { studioCss, indexCss } = readContract();

/** 取出指定选择器的所有 {...} 块（按大括号配平，能穿过嵌套）。 */
function blocksOf(css: string, selectorPattern: RegExp): string[] {
  const out: string[] = [];
  const re = new RegExp(selectorPattern.source, "g");
  let match: RegExpExecArray | null;
  while ((match = re.exec(css)) !== null) {
    const open = css.indexOf("{", match.index);
    if (open < 0) continue;
    let depth = 0;
    for (let i = open; i < css.length; i++) {
      if (css[i] === "{") depth++;
      else if (css[i] === "}") {
        depth--;
        if (depth === 0) {
          out.push(css.slice(open + 1, i));
          re.lastIndex = i;
          break;
        }
      }
    }
  }
  return out;
}

/** 把若干块里的 `--name: value;` 收进一张表（后面的覆盖前面的）。 */
function declarationsOf(blocks: string[]): Map<string, string> {
  const out = new Map<string, string>();
  for (const block of blocks) {
    const re = /--([a-z0-9-]+)\s*:\s*([^;]+);/g;
    let m: RegExpExecArray | null;
    while ((m = re.exec(block)) !== null) {
      out.set(m[1], m[2].replace(/\s+/g, " ").trim());
    }
  }
  return out;
}

const studioRoot = declarationsOf(blocksOf(studioCss, /:root\s*\{/));
const indexRoot = declarationsOf(blocksOf(indexCss, /:root\s*\{/));
const indexDark = declarationsOf(blocksOf(indexCss, /\[data-theme="dark"\]\s*\{/));

/** 顺着 `var(--x)` 引用取值（只跟一层就够了：颜色别名只指向基础名）。 */
function resolveVar(table: Map<string, string>, name: string): string | undefined {
  const value = table.get(name) ?? indexRoot.get(name);
  if (value === undefined) return undefined;
  const ref = /^var\(--([a-z0-9-]+)\)$/.exec(value);
  if (!ref) return value;
  return table.get(ref[1]) ?? indexRoot.get(ref[1]);
}

/** index.css 里的投影写法：`rgb(r g b)`（uiaudit.js 的 parseColor 只认这种）。 */
function formatSrgb(rgb: { r: number; g: number; b: number }): string {
  return ["rgb", "(", String(rgb.r), " ", String(rgb.g), " ", String(rgb.b), ")"].join("");
}

const PROTO_NAMES = Object.keys(PROTO_LIGHT) as ProtoColorName[];

describe("原型令牌（studio.css）是唯一来源", () => {
  it("PROTO_LIGHT 与 studio.css 逐字一致", () => {
    for (const name of PROTO_NAMES) {
      expect(studioRoot.get(name), `studio.css 里没有 --${name}`).toBeDefined();
      // 逐字比较：多一个空格都算偏差，因为契约就是那串字符。
      expect(PROTO_LIGHT[name], `--${name} 与原型不一致`).toBe(studioRoot.get(name)!.replace(/\s+/g, " ").trim());
    }
  });

  it("原型没有深色块（出现了就必须把深色锚点换成契约值）", () => {
    expect(studioCss).not.toMatch(/prefers-color-scheme|data-theme|\[data-theme/);
  });

  it("每个原型颜色令牌都被映射进 --anvil-*", () => {
    const mapped = new Set(Object.values(COLOR_ALIASES));
    for (const name of PROTO_NAMES) {
      expect(mapped.has(name), `原型 --${name} 没有任何 --anvil-* 消费它`).toBe(true);
    }
  });
});

describe("index.css 是原型的投影", () => {
  it("浅色：每个别名等于原型 token 的 sRGB 投影", () => {
    for (const [ourName, protoName] of Object.entries(COLOR_ALIASES)) {
      const declared = resolveVar(indexRoot, ourName);
      expect(declared, `index.css 缺少 --${ourName}`).toBeDefined();
      expect(declared, `--${ourName} 与原型 --${protoName} 不同色`).toBe(
        formatSrgb(srgbOf(protoName, "light"))
      );
    }
  });

  it("深色：每个别名等于深色锚点的 sRGB 投影", () => {
    for (const [ourName, protoName] of Object.entries(COLOR_ALIASES)) {
      const declared = resolveVar(indexDark, ourName);
      expect(declared, `[data-theme=dark] 缺少 --${ourName}`).toBeDefined();
      expect(declared, `深色 --${ourName} 与锚点推导值不同`).toBe(
        formatSrgb(srgbOf(protoName, "dark"))
      );
    }
  });

  it("派生色只用原型公式（color-mix 引用语义 token）", () => {
    for (const [ourName, spec] of Object.entries(DERIVED_COLORS)) {
      for (const table of [indexRoot, indexDark]) {
        const declared = table.get(ourName);
        expect(declared, `缺少派生色 --${ourName}`).toBeDefined();
        expect(declared).toContain("color-mix(");
        expect(declared, `派生色 --${ourName} 必须引用 --${spec.from}`).toContain(`var(--${spec.from})`);
        expect(declared, `派生色 --${ourName} 的比例应为 ${spec.opacity}`).toContain(`${spec.opacity}%`);
      }
    }
  });

  it("没有残留的十六进制颜色（原型没定义的值必须删掉，而不是换成另一个猜的值）", () => {
    expect(indexCss).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
  });

  it("所有 rgb() 值都登记在令牌表里（不许有第二条调色板）", () => {
    const registered = new Set([...Object.keys(COLOR_ALIASES), ...Object.keys(DERIVED_COLORS)]);
    for (const [name, value] of [...indexRoot, ...indexDark]) {
      if (!value.startsWith("rgb(")) continue;
      expect(registered.has(name), `--${name} 是未登记的硬编码颜色`).toBe(true);
    }
  });

  it("几何、字体、动效与原型一致", () => {
    // 原型的几何值：--r-* / --sidebar-w 直接从 studio.css 读，避免两边一起抄错。
    for (const key of ["r-xs", "r-sm", "r-md", "r-lg", "sidebar-w"] as const) {
      expect(studioRoot.get(key), `studio.css 里没有 --${key}`).toBe(GEOMETRY[key]);
    }
    expect(indexRoot.get("anvil-sidebar-w")).toBe(GEOMETRY["sidebar-w"]);
    expect(indexRoot.get("anvil-r-md")).toBe(GEOMETRY["r-md"]);
    expect(indexRoot.get("anvil-r-lg")).toBe(GEOMETRY["r-lg"]);
    expect(indexRoot.get("anvil-radius")).toBe("var(--anvil-r-md)");
    // 顶栏 52px / 内容留白 26px 28px 56px：取自 studio.css 的 .topbar / .page-inner。
    expect(studioCss).toContain(`height: ${GEOMETRY["topbar-h"]};`);
    expect(studioCss).toContain(
      `padding: ${GEOMETRY["page-pad-y"]} ${GEOMETRY["page-pad-x"]} ${GEOMETRY["page-pad-b"]};`
    );
    expect(indexRoot.get("anvil-topbar-h")).toBe(GEOMETRY["topbar-h"]);
    expect(indexRoot.get("anvil-page-max")).toBe(GEOMETRY["page-max"]);
    expect(indexRoot.get("anvil-page-pad-y")).toBe(GEOMETRY["page-pad-y"]);
    expect(indexRoot.get("anvil-page-pad-x")).toBe(GEOMETRY["page-pad-x"]);
    expect(indexRoot.get("anvil-page-pad-b")).toBe(GEOMETRY["page-pad-b"]);

    // 字体栈逐字。
    expect(studioRoot.get("font-display")!.replace(/\s+/g, " ")).toBe(FONT_STACKS.display);
    expect(studioRoot.get("font-body")!.replace(/\s+/g, " ")).toBe(FONT_STACKS.body);
    expect(studioRoot.get("font-mono")!.replace(/\s+/g, " ")).toBe(FONT_STACKS.mono);
    expect(indexRoot.get("anvil-font-display")!.replace(/\s+/g, " ")).toBe(FONT_STACKS.display);
    expect(indexRoot.get("anvil-font-body")!.replace(/\s+/g, " ")).toBe(FONT_STACKS.body);
    expect(indexRoot.get("anvil-font-mono")!.replace(/\s+/g, " ")).toBe(FONT_STACKS.mono);

    // 阴影跟着语义色走（原型用 color-mix 引用 --fg）。
    expect(indexRoot.get("anvil-shadow-sm")).toContain("color-mix(");
    expect(indexRoot.get("anvil-shadow-md")).toContain("var(--anvil-foreground)");
    expect(indexRoot.get("anvil-shadow")).toBe("var(--anvil-shadow-md)");
  });
});

describe("深色主题（原型没有深色块，锚点必须自证）", () => {
  it("色相与彩度沿用原型，只有明度不同", () => {
    for (const name of PROTO_NAMES) {
      const light = parseOklch(oklchOf(name, "light"));
      const dark = parseOklch(oklchOf(name, "dark"));
      expect(dark.c, `--${name} 深色彩度应沿用原型`).toBe(light.c);
      expect(dark.h, `--${name} 深色色相应沿用原型`).toBe(light.h);
      expect(dark.l, `--${name} 深色明度应为锚点值`).toBe(DARK_LIGHTNESS[name]);
    }
  });

  it("深色是浅色的明度反转：前景变亮、面变暗，卡片始终比页面底色亮", () => {
    // 浅色："越靠前越亮"不是全局成立的（卡片比页面底色还亮），所以分别锁两件事。
    const lightOrder: ProtoColorName[] = [
      "fg",
      "fg-2",
      "muted",
      "border-strong",
      "border",
      "surface-3",
      "surface-2",
      "bg",
      "surface",
    ];
    const darkOrder: ProtoColorName[] = [
      "bg",
      "surface-2",
      "surface",
      "surface-3",
      "border",
      "border-strong",
      "muted",
      "fg-2",
      "fg",
    ];
    for (const [theme, order] of [
      ["light", lightOrder],
      ["dark", darkOrder],
    ] as const) {
      const ls = order.map((n) => parseOklch(oklchOf(n, theme)).l);
      for (let i = 1; i < order.length; i++) {
        expect(ls[i], `${theme}: ${order[i - 1]}(${ls[i - 1]}) 应暗于 ${order[i]}(${ls[i]})`).toBeGreaterThan(
          ls[i - 1]
        );
      }
    }

    // 方向性：前景族在深色下变亮，面族在深色下变暗。
    for (const name of ["fg", "fg-2", "muted"] as ProtoColorName[]) {
      expect(parseOklch(oklchOf(name, "dark")).l, `--${name} 深色应更亮`).toBeGreaterThan(
        parseOklch(oklchOf(name, "light")).l
      );
    }
    for (const name of ["bg", "surface", "surface-2", "surface-3", "border", "border-strong"] as ProtoColorName[]) {
      expect(parseOklch(oklchOf(name, "dark")).l, `--${name} 深色应更暗`).toBeLessThan(
        parseOklch(oklchOf(name, "light")).l
      );
    }
  });

  it("两套主题的语义色对都达到门禁阈值（AA）", () => {
    for (const theme of ["light", "dark"] as const) {
      for (const pair of CONTRAST_PAIRS) {
        const ratio = contrastRatio(srgbOf(pair.fg, theme), srgbOf(pair.bg, theme));
        expect(
          ratio,
          `${theme}: ${pair.fg}/${pair.bg} = ${ratio.toFixed(2)} < ${pair.min}`
        ).toBeGreaterThanOrEqual(pair.min);
      }
    }
  });
});

describe("外壳文件只用语义令牌（THEME01 在 app/** 上的落点）", () => {
  const PALETTE = /\b(?:bg|text|border|ring|outline|divide|placeholder)-(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|white|black)(?:-[0-9]{2,3})?\b/;
  const HEX = /#[0-9a-fA-F]{3,8}\b/;

  it("app/** 与 components/** 不出现调色板类名或直接颜色值", () => {
    const files = shellSourceFiles();
    expect(files.length).toBeGreaterThan(5);
    const findings: string[] = [];
    for (const file of files) {
      for (const [lineNo, line] of file.text.split("\n").entries()) {
        if (PALETTE.test(line) || HEX.test(line)) {
          findings.push(
            `${file.path.replace(FRONTEND_ROOT, "frontend")}:${lineNo + 1} ${line.trim().slice(0, 80)}`
          );
        }
      }
    }
    expect(findings, "壳层文件必须只用语义 token").toEqual([]);
  });
});
