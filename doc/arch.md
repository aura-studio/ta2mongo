# tango 架构说明

## 1. 目标

`tango` 将 ThinkingData 日志 JSON 行采集并写入 MongoDB 的 `user` / `event` / `dead_letter` 集合，同时提供任务队列、历史回填、SQL 导入和 HTTP 接入能力。

tango 仍是单一二进制，但启动体系按运行角色组织：

| 角色 | 命令 | 生命周期 | 职责 |
|---|---|---|---|
| **Report Service** | `tango report run` | 常驻 | 文件追尾、解析、report filter、identity、批量写 MongoDB |
| **Task Worker Service** | `tango worker run` | 常驻 | 注册心跳，claim/renew/execute task queue 任务 |
| **HTTP Gateway Service** | `tango gateway serve` | 常驻 | 暴露 REST API，把 HTTP 请求转为 SDK 操作或任务发布 |
| **Operator CLI** | `tango operator ...` | 一次性 | ingest/upload/backfill/sql/publish 等人工或脚本操作 |

tango 只有这四个角色命令，没有 legacy 兼容入口。

## 2. 启动模式：从部署 profile 到运行角色

早期 tango 把 `standalone` / `agent` 当作启动模式，混淆了部署形态与功能职责：
`standalone` 只做 report，`agent` 同时做 report + remote config sync + task worker，
而 HTTP 接入又藏在 `client serve` 下。现在按**运行角色**彻底拆开：

```text
report   — 常驻采集上报
worker   — 常驻任务消费
gateway  — 常驻 HTTP 接入
operator — 一次性操作
```

角色之间不再有「单进程组合模式」：要同时跑采集与任务消费，就分别启动 `tango report run`
与 `tango worker run` 两个进程。旧的 daemon / client / profile 命令与其配置 schema 已移除。

## 3. 目录结构

```text
.
├── main.go
├── cmd/
│   ├── report/      # tango report run
│   ├── worker/      # tango worker run
│   ├── gateway/     # tango gateway serve
│   ├── operator/    # tango operator ...
│   └── shared/      # cmd glue: config resolution, client building, service runners
├── config/          # RoleConfig (unified file schema) + role loaders; ClientConfig (runtime projection); shared runtime Config
├── client/          # 对外 Go SDK
├── doc/ examples/
└── internal/
    ├── core/        # cli remoteconfig filter store talog tailer dynamicbatch taskqueue
    ├── process/     # ingest pipeline
    └── service/
        ├── report/  # report service runtime (report.Service)
        ├── worker/  # task worker service runtime (worker.Service)
        ├── gateway/ # HTTP gateway runtime
        └── backfill/
```

依赖方向保持：

```text
cmd -> config + service/client SDK
service -> process + core
process -> core
core -> external libs only
```

## 4. Report Service

命令：

```bash
tango report run
```

数据流：

```text
Tailer -> Dispatcher(按用户亲和性路由) -> Worker[i](Parse -> Filter -> Identity -> Batch) -> MongoDB BulkWrite
```

职责：

- 读取 `report.source.logPattern`。
- 追尾文件并输出 line channel。
- 解析 TA JSON。
- 应用 report filter。
- 根据 `#account_id` / `#distinct_id` 做用户亲和性路由。
- 批量写入 MongoDB。
- 可选启用 remote config hot reload。

report service 不启动 task worker，也不持有 worker lifecycle。

## 5. Task Worker Service

命令：

```bash
tango worker run --instanceID worker-1
```

职责：

- 注册 `_tango_instances` 心跳。
- 从 `_tango_tasks` claim 任务。
- 执行任务期间续约 lease。
- 完成或失败任务。
- 定期 reap orphaned / stuck tasks。

任务类型：

| 任务类型 | 说明 |
|---|---|
| `report-sync` | 写入 remote config 文档；独立 report service 通过自己的 sync loop 应用 |
| `backfill` | 执行历史回填 |
| `sql` | 执行 SQL 并导入结果 |

`worker run` 不要求 `report.source.logPattern`，也不持有 report 的 `filter.Holder`。worker 与 report 完全解耦：执行 `report-sync` 只写 remote config 文档，由各 report service 通过自己的 remote config sync loop 收敛。

## 6. HTTP Gateway Service

命令：

```bash
tango gateway serve
```

gateway 是常驻服务，使用现有 ClientConfig 和 Go SDK，暴露：

```text
GET  /healthz
POST /ingest
POST /upload
POST /backfill
POST /sql
POST /publish/report-sync
POST /publish/backfill
POST /publish/sql
```

HTTP 运行时位于 `internal/service/gateway`；命令层 `cmd/gateway` 只做参数与配置加载。

## 7. Operator CLI 与 SDK

命令：

```bash
tango operator ingest
tango operator upload
tango operator backfill
tango operator sql
tango operator publish report-sync
tango operator publish backfill
tango operator publish sql
```

operator 是一次性操作入口，复用 `client/` Go SDK。SDK 公共 API 保持稳定。

## 8. 两种 filter

| | 上报 filter | backfill filter |
|---|---|---|
| 使用方 | report service、string/file upload | backfill、sql |
| 维度 | `#type` / `#event_name` / 属性 | 表名(event/user) + 事件/属性，不含 `#type` |
| 表达式 | include / exclude | include / exclude + events 语法糖 |

`config.BackfillFilterConfig.IncludeExprs()` 把 `events` 折叠进 include，再复用 `filter.New` / `filter.CompileToSQL`。

## 9. Taskqueue 可靠性边界

taskqueue 是可靠性敏感模块，重构启动体系时不改变其核心语义：

- `Claim` 原子领取。
- 长任务续租。
- `Complete` / `Fail` 校验 lease。
- `Fail` 设置退避 `notBefore`。
- `Reap` 清理 orphaned / stuck tasks。
- 实例 heartbeat + TTL。

这些逻辑属于 worker service 的核心可靠性边界，不能随命令行重构顺手重写。

## 10. Report-sync 语义

角色路径：

```text
operator/gateway publish report-sync
worker claim task and write remote config document
report service poll remote config and apply filter.Holder
```

`worker.executeReportSync` 只校验表达式能编译，然后写入 remote config 文档。因此 worker 完成 report-sync 表示 **remote config 写入成功**，而非所有 report service 已经应用——各 report service 通过自己的 remote config sync loop 收敛到该过滤器。若后续需要全局确认语义，可引入 config version + ack collection。
