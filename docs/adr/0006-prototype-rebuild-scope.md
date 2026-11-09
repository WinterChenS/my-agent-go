# ADR 0006 · 原型重建分支的运行时与验收顺序

- 日期 2026-11-09
- 状态 已采用的重建分支约束
- 范围 `codex/anvil-rebuild`

用户要求基于原型从零重建，按界面、多运行时发现和同步、完整会话的顺序交付，并为每一步设置门禁。运行时从 Pi 单一支持改为宿主多 CLI 适配，桌面技术栈沿用现有约定。平台范围为 Windows、macOS 和 Linux，Windows 10+ 优先，首轮以 Windows 10 22H2 x64（build 19045）实机取证。其他系统与架构按实测记录。

- 界面、目录同步和会话分别设 G0、G1、G2，各阶段独立取证。
- Pi 和 Codex 是首批可运行验收基准。Claude Code 和 OpenCode 首批必须覆盖发现与注册，认证及每项能力在矩阵中如实记录。
- 旧规格及命令记录供查证。本分支的实现范围、命令及验收以重建设计和计划为准。原型及旧提交保留为设计与历史资料。

[重建设计](../superpowers/specs/2026-11-09-anvil-prototype-rebuild-design.md) · [实施计划](../superpowers/plans/2026-11-09-anvil-prototype-rebuild-plan.md)
