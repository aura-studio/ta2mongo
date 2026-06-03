# tango 重构摘要

本文记录当前这一轮结构重构后的约定，方便后续会话快速恢复上下文。当前架构以 [arch.md](arch.md) 和 [config.md](config.md) 为准。

## 核心约定

1. 领域包由根包对外聚合，子包承载实现细节：
   `dao` 聚合 `mongo`/`store`，`parser` 聚合 `filter`，`process` 聚合 `pipeline`，`role` 聚合 `api`/`cli`/`daemon`/`gateway`。
2. 各模块的默认值由各自 `Config.ApplyDefaults` 承担，顶层 `config` 只负责装配、投影和校验，不集中维护子模块默认值。
3. `process` 是处理层对外入口，`pipeline` 配置作为 `process.Config` 的成员使用。
4. 日志包统一为 `internal/logging`，配置层级为 `runtime.logging`，字段和实际代码结构保持一致。
5. `client` 逻辑已经下放到 `internal/role/gateway/client`，作为 gateway 角色的紧密逻辑使用。

## 当前命名

- 角色层目录：`internal/role`
- daemon 角色：`internal/role/daemon`
- gateway 角色：`internal/role/gateway`
- gateway 配置：`config/gateway.go` / `GatewayRuntimeConfig`
- 日志包：`internal/logging`

## 命令和角色

命令以 role 为标准对齐，保留四个角色入口：

- `api`
- `cli`
- `daemon`
- `gateway`

其中 `daemon` 和 `gateway` 已有运行逻辑；`api` 和 `cli` 当前作为预留入口。

## 配置约定

- `runtime.mongo.maxElapsedTime` 已迁移为 `runtime.store.maxElapsedTime`。
- `runtime.logging` 对应 `internal/logging.Config`。
- `report.pipeline` 投影为 `process.Config.Pipeline`。
- `role.mode` 由 role 配置承载，daemon 运行时使用 `daemon`。

## 验证

本轮结构调整后应至少运行：

```powershell
go test ./...
```
