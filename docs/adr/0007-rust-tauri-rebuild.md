# ADR 0007 · Anvil 迁移到 Rust + Tauri

- 日期 2026-11-10
- 状态 用户已决定，实施待开始
- 范围 `codex/anvil-rebuild`

产品宿主、构建入口、原生测试运行器和门禁工具迁移到 Rust。桌面壳采用 Tauri v2。现有 React 前端、原型、验收范围与阶段顺序继续沿用。迁移复验完成后删除分支中的 Go/Wails 实现和活动依赖。Tauri 产物使用自己的真实桌面证据。

该决定替换 ADR 0006 及重建计划中的 Go/Wails 技术约定。协调者先暂停旧栈派发并记录当前基线，从迁移计划的 R0-RS01 启动。迁移的文档提交不改变当前实现或历史验收状态。

[技术方案](../superpowers/specs/2026-11-10-anvil-rust-tauri-design.md) · [迁移实施计划](../superpowers/plans/2026-11-10-anvil-rust-tauri-migration-plan.md)
