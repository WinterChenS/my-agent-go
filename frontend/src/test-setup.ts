// 测试环境初始化。
//
// 只做两件事：补 jest-dom 的匹配器，以及在每个用例后清理 DOM。
// 不做全局 mock——把 api 层 mock 掉会让组件测试变成"测试我自己的假数据"，
// 真正需要 mock 的地方在用例里显式写，读代码的人才知道它依赖了什么。
import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

afterEach(() => {
  cleanup();
});
