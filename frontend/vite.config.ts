/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  // 缓存固定在工作区内：默认会落到系统临时目录，本机的文件钩子会拒绝
  // 在那里写入，表现为一串 EPERM 未处理错误（测试仍然通过，但退出状态不可信）。
  cacheDir: "./node_modules/.vite-cache",
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  test: {
    // 组件测试需要真实 DOM。纯归约测试在 jsdom 下也能跑，
    // 所以统一一个环境，避免同一份断言在不同环境行为不同。
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test-setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    // 文件串行执行。
    //
    // 并发时多个 worker 会同时往 vitest 自己的临时模块缓存目录
    // （os.tmpdir()/<id>/web/<hash>，不可配置）写文件，本机实测会随机
    // 撞上 EPERM：报错是"未处理错误"，但更糟的是那一轮里部分测试文件
    // 根本没被执行——同一份代码几次跑出来 38 / 54 / 60 条结果。
    // 这种"少跑了几条还说不清"的结果比慢几秒危险得多；总共十几秒，串行无所谓。
    fileParallelism: false,
  },
});
