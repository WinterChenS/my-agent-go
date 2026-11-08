// 令牌测试要读的源码文本（D1）。
//
// 为什么单独一个 .mjs：`frontend/tsconfig.json` 没有装 @types/node（package.json
// 不在 D1 的改动范围），.ts 里 `import "node:fs"` 会直接把 typecheck 打红。
// 仓库里已有同样的做法：`scripts/check-ui-contract.mjs` + `.d.mts` 声明。
// 判定逻辑留在测试里，这里只负责"把文本取出来"。

import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));

/** frontend/ 目录。 */
export const FRONTEND_ROOT = resolve(HERE, "..", "..");
/** 仓库根目录（原型导出在 docs/ 下）。 */
export const REPO_ROOT = resolve(FRONTEND_ROOT, "..");

/** 原型的 studio.css 与我们投影后的 index.css。 */
export function readContract() {
  return {
    studioCss: readFileSync(
      join(REPO_ROOT, "docs", "ui", "Agent-客户端产品原型", "assets", "studio.css"),
      "utf8"
    ),
    indexCss: readFileSync(join(FRONTEND_ROOT, "src", "index.css"), "utf8"),
  };
}

function walk(dir, out) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) walk(path, out);
    else if (/\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name)) {
      out.push({ path, text: readFileSync(path, "utf8") });
    }
  }
  return out;
}

/** 外壳拥有的源码文件（app/** 与 components/**，跳过测试）。 */
export function shellSourceFiles() {
  return [
    ...walk(join(FRONTEND_ROOT, "src", "app"), []),
    ...walk(join(FRONTEND_ROOT, "src", "components"), []),
  ];
}
