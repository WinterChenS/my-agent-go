// 生产源码的静态扫描（U07 Step 2）。
//
// 这是"必须在夹具上先红"的四条规则的生产侧用法：违规夹具在 fixtures.spec.ts
// 里已经各自失败过一次，这里要求生产源码**零发现**。
//
// 为什么放在预检而不是 Go 门禁：它是纯文本判定，跑一次不到 100ms，能在改完
// 样式/文案的下一秒就告诉人哪里错了。门禁侧对应的是 GOV01–04 的静态扫描，
// 两者口径一致（同一份规则集语义），但证据只从 native/static 套件产生。
import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { REPO_ROOT } from "./lib/audit";
import {
  scanDirectColors,
  scanHardcodedMetrics,
  scanMessageKeys,
  scanMockImports,
  sourceFiles,
} from "./lib/static-scan.mjs";

const SRC = resolve(REPO_ROOT, "frontend/src");

function catalogs(): object[] {
  return ["zh-CN", "en-US"].map(
    (l) => JSON.parse(readFileSync(resolve(REPO_ROOT, "frontend/src/i18n/locales", `${l}.json`), "utf8")) as object
  );
}

function describeFindings(findings: Array<{ file: string; line: number; detail: string }>): string {
  return findings
    .slice(0, 25)
    .map((f) => `${f.file}:${f.line} ${f.detail}`)
    .join("\n  ");
}

test.describe("生产源码不得有假数据/直接颜色/漂移 key", () => {
  test("没有 mock/demo/stub 引用", () => {
    const findings = scanMockImports(SRC, sourceFiles(SRC));
    expect(findings.length, `生产源码引用了演示数据:\n  ${describeFindings(findings)}`).toBe(0);
  });

  test("没有写死的产品指标", () => {
    const findings = scanHardcodedMetrics(SRC, sourceFiles(SRC));
    expect(findings.length, `生产源码写死了产品指标:\n  ${describeFindings(findings)}`).toBe(0);
  });

  test("没有直接颜色值或 Tailwind 调色板类名", () => {
    const findings = scanDirectColors(SRC, sourceFiles(SRC));
    expect(findings.length, `生产组件必须只用语义 token（THEME01）:\n  ${describeFindings(findings)}`).toBe(0);
  });

  test("所有 t() 字面量 key 都在 catalog 里", () => {
    const findings = scanMessageKeys(SRC, sourceFiles(SRC), catalogs());
    expect(findings.length, `消息 key 漂移:\n  ${describeFindings(findings)}`).toBe(0);
  });
});
