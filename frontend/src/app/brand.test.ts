// 品牌守卫（BRAND01–03 的前端收尾项，U07 concern 3）。
//
// 窗口标题来自 Go 的 `app.BrandName`（Anvil），但**文档标题**来自 index.html：
// 它会成为页面与 WebView 的可访问名称（AX 树里的 RootWebArea name），
// 所以它必须跟品牌一致，而且不能退回旧名字。
//
// 这条用例读的是 index.html 的原文而不是渲染结果：它不经过 React，
// 渲染测试永远看不到它；而漏改的后果（读屏软件念出旧产品名）只有在真机上
// 才会被发现。
import { describe, expect, it } from "vitest";
import indexHtml from "../../index.html?raw";
import zhCN from "../i18n/locales/zh-CN.json";
import enUS from "../i18n/locales/en-US.json";

describe("brand", () => {
  it("文档标题是 Anvil", () => {
    expect(indexHtml).toContain("<title>Anvil</title>");
  });

  it("index.html 里不残留旧产品名", () => {
    expect(indexHtml).not.toContain("MyAgent");
    expect(indexHtml).not.toContain("myagent");
  });

  it("界面上的产品名与 app.name 一致（两种语言都是 Anvil）", () => {
    expect((zhCN as { app: { name: string } }).app.name).toBe("Anvil");
    expect((enUS as { app: { name: string } }).app.name).toBe("Anvil");
  });
});
