# Draft: Tango Startup Architecture Refactor

## Requirements (confirmed)
- 用户认为当前启动模式分类有问题。
- 用户确认问题核心包括：`daemon agent` 同时做采集和任务执行，职责混杂。
- 用户要求：1. 改命令行参数；2. 改整个体系；生成一个重构架构。

## Technical Decisions
- 将本次工作按 Architecture intent 处理，需要产出完整重构架构计划，而不是直接改代码。
- 当前命令树事实：`tango daemon standalone`、`tango daemon agent`、`tango client <subcmd>`、`tango client serve`。
- 当前 `daemon agent` 由 `runReport(..., agentOn=true)` 同进程启动 report pipeline 和 task agent，且通过 shared `filter.Holder` 让 report-sync 热替换上报 filter。
- 当前 `client serve` 位于 `cmd/client/serve.go`，实际是常驻 HTTP gateway，不是一次性 client 操作。

## Research Findings
- `main.go`: root command 同时挂载 `daemoncmd.NewCommand()` 和 `clientcmd.NewCommand()`，但顶部注释仍写 v1.0.0 client 未接线，已过期。
- `cmd/daemon/daemon.go`: `agent` 模式同时执行 remote-config startup apply、report pipeline、task agent。
- `cmd/client/client.go`: client 同时承载 one-shot operator 命令与 `serve` 常驻服务。
- `cmd/client/serve.go`: `/ingest`、`/upload`、`/backfill`、`/sql`、`/publish/...` 是 HTTP API gateway。
- `doc/arch.md`: 文档把角色定义为 Daemon standalone、Daemon agent、Client，与用户指出的问题一致，需要重写。

## Open Questions
- 是否要求新 CLI 保持旧命令兼容别名/弃用提示，还是允许 breaking change？
- 新体系是否要把 report service 与 task worker service 拆成独立进程命令，还是保留一个组合命令作为 convenience profile？
- `report-sync` 目前依赖 task agent 与 report pipeline 共享 `filter.Holder`；拆进程后需要选择新的热更新机制。

## Scope Boundaries
- INCLUDE: 命令行层级重新设计、运行角色重新划分、配置体系重构、服务生命周期边界、迁移兼容策略、文档/测试计划。
- EXCLUDE: 直接修改 Go 源码；直接执行重构。
