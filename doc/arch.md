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

### 3.1 文件功能清单

#### 命令层 `cmd/`（薄封装：参数 + 配置加载 + 启动）

| 文件 | 职责 |
|---|---|
| `main.go` | 根 cobra 命令，挂载 report/worker/gateway/operator 四个角色命令 |
| `cmd/report/report.go` | `tango report` 命令 + `run` 子命令；解析 `report.yaml`，委托 `shared.RunReportService` |
| `cmd/worker/worker.go` | `tango worker` 命令 + `run`；解析 `worker.yaml`，提供 `--instanceID` 别名 |
| `cmd/gateway/gateway.go` | `tango gateway` 命令 + `serve`；解析 `gateway.yaml`，启动 HTTP gateway |
| `cmd/operator/operator.go` | `tango operator` 命令树：ingest / upload / backfill / sql / publish 一次性子命令 |
| `cmd/shared/client.go` | 命令层共享：`ConfigFlag`、`OperatorConfig`/`GatewayConfig` 加载器、`BuildClient`/`ConnectClient`、`ClientLoader`、`PrintJSON` |
| `cmd/shared/service.go` | 服务运行器：`RunReportService`/`RunWorkerService`、`runReport`/`runWorker`、`MaskURI` |

#### 配置层 `config/`

| 文件 | 职责 |
|---|---|
| `config/config.go` | 运行时 `Config` 及各嵌套结构、常量、`Validate`、`MongoDBFromURI`、`IncludeExprs`/`TimeRange` |
| `config/role.go` | 统一 `RoleConfig` 文件 schema + 角色加载器 `LoadReport/Worker/Gateway/Operator` + 投影 + `setRoleDefaults` |
| `config/client.go` | `ClientConfig`（gateway/operator/SDK 的运行时投影）+ `BackfillRuntime`/`SQLRuntime` |
| `config/defaults.go` | `applyDefaults` 及各 `apply*Defaults` 默认值填充 |
| `config/pipeline.go` | 批大小 helper：`BatchSizeMin`/`BatchSizeMax`/`BatchChannelSize` |
| `config/filter.go` | `BuildFilter`/`BuildBackfillFilter`/`BackfillWhere`（编译过滤器与 SQL 下推） |
| `config/backfill.go` | backfill helper：`ForceSkip`/`ShouldPaginate`/`EffectivePageSize` + `BackfillConfig.validate` |
| `config/loader.go` | viper 装配 helper：`newViper`、`readConfigFile`、`bindFlagsTo`、`durationDecodeHook`、`weaklyTyped` |

#### 对外 SDK `client/`

| 文件 | 职责 |
|---|---|
| `client/client.go` | SDK 入口：`Client`、`Options`/`Option`（`WithURI`…）、`New`/`Close`/`EnsureIndexes`、`Ingest`/`IngestBatch`、`Ping` |
| `client/config.go` | remote config 操作：`PublishConfig`/`PublishFilter`/`GetPublishedConfig` |
| `client/run.go` | 直接执行：`RunBackfill`/`ExecuteSQL`，`BackfillResult` |
| `client/task.go` | 任务发布：`PublishTask`/`PublishBackfillTask`/`PublishSQLTask`/`PublishReportSync` + `GetTask`/`ListInstances`，`TaskSpec` |
| `client/upload.go` | `UploadFiles` + `UploadRequest`/`UploadResult`（文件上传，断点续传） |

#### 基础设施 `internal/core/`

| 文件 | 职责 |
|---|---|
| `core/cli/cli.go` | `ResolveConfigPath`（二进制同级默认配置）、`NewLogger` |
| `core/dynamicbatch/flush_threshold.go` | `ComputeFlushThreshold`：按 backlog 自适应批刷新阈值 |
| `core/filter/filter.go` | `Filter`：expr-lang include/exclude 编译与求值（`Keep`） |
| `core/filter/holder.go` | `Holder`：原子可热替换的 `Filter` 持有者（report 热更新用） |
| `core/filter/sql.go` | `CompileToSQL`：把 include/exclude 编译为 Presto WHERE 下推 |
| `core/remoteconfig/remoteconfig.go` | remote config 文档 `Fetch`/`Merge`/`FilterChanged`（控制面覆盖） |
| `core/remoteconfig/startup.go` | `ApplyAtStartup`：启动时一次性拉取并合并 remote config |
| `core/runtime/mongo.go` | `MongoResource`（Client/DB/Owns）+ `ConnectMongo`/`Borrow`/`DatabaseFromClient`/`Close`（连接与归属） |
| `core/runtime/store.go` | `NewStore`：在 DB 上装配 `Store` 的薄 helper |
| `core/store/store.go` | `Store`：MongoDB 持久化入口、`WriteStats`、`BulkWrite`/`BulkWriteOrdered`、集合访问器 |
| `core/store/identity.go` | `IdentityResolver`：`#account_id`/`#distinct_id` → `#user_id` 解析与缓存、`id_mapping` 原子写 |
| `core/store/indexes.go` | `Store.EnsureIndexes`：创建 user/event/dead_letter/id_mapping 索引 |
| `core/store/writemodel.go` | 构建 `user_*`/`track_*` 写模型与 dead-letter 模型（`_ts` 防回退语义） |
| `core/talog/parser.go` | `Parser.ParseLine`：解析 TA JSON 行为 `Record` |
| `core/talog/record.go` | `Record` 及 `Category`/`IsUserType`/`IsEventType`（user vs event 分类） |
| `core/tailer/tailer.go` | `Tailer`：按 glob 发现文件、追尾、定期 rescan，输出 line channel（hybrid/poll/event） |
| `core/taskqueue/task.go` | `Task`/`TaskType`/`TaskStatus` 模型定义 |
| `core/taskqueue/queue.go` | `Queue`：`Publish`/`Claim`/`RenewLease`/`Complete`/`Fail`/`Get`（原子 claim、lease、退避） |
| `core/taskqueue/reap.go` | `Queue.Reap`：清理 lease 过期耗尽 + 目标离线的孤儿任务 |
| `core/taskqueue/instance.go` | `Registry`：实例心跳注册 / `IsAlive` / TTL（targeting fail-fast） |

#### 处理流程 `internal/process/`

| 文件 | 职责 |
|---|---|
| `process/ingestion/processor.go` | `Processor.Process`：共享 parse→filter→identity→写模型分类（`Kind`/`Result`/`WriteOptions`） |
| `process/ingestion/stats.go` | `StatsCollector` 接口 + `NoopStats` |
| `process/ingestion/counters.go` | `Counters`（并发计数器，实现 `StatsCollector`）+ `Snapshot` |
| `process/ingest/ingest.go` | `Ingester`：同步单条/批量 ingest（`New`/`NewFromClient`/`Close`），经 `Processor` 即时写入 |
| `process/pipeline/worker.go` | `RunWorkers`/`worker`：N 并发 + 批累积 + 动态刷新（每行交给 `Processor`） |
| `process/pipeline/batch.go` | `Batch`：写模型批容器 |
| `process/pipeline/dispatch.go` | `Dispatch`：按亲和键把行路由到各 worker channel |
| `process/pipeline/routing.go` | `ExtractRoutingKey`/`RouteIndex`：用户亲和性 hash 路由 |

#### 运行服务 `internal/service/`

| 文件 | 职责 |
|---|---|
| `service/report/report.go` | `report.Service`：追尾 → pipeline → MongoDB；remote config 同步循环、周期/最终统计日志 |
| `service/worker/worker.go` | `worker.Service`：心跳 / claim / lease / reap / result 生命周期 + handler registry |
| `service/worker/handlers.go` | `Handler` 接口 + `buildHandlers` 类型索引注册表 |
| `service/worker/report_sync.go` | `reportSyncHandler`：编译过滤器并写 remote config 文档 |
| `service/worker/backfill_handler.go` | `backfillHandler` + `sqlHandler`：payload → config → Runner / Executor |
| `service/worker/payload.go` | payload 解码 helper：`reportSyncFilters`/`decodePayload`/`overlayBackfillFilter`/`toStringSlice` |
| `service/gateway/server.go` | gateway HTTP `Server`：`/healthz` `/ingest` `/upload` `/backfill` `/sql` `/publish/*`（转 SDK 调用或任务发布） |
| `service/backfill/runner.go` | `Runner`：run 级编排（init checkpoint、遍历 days、调 executor、summary） |
| `service/backfill/executor.go` | TA 任务生命周期：`ExecuteSQL`/`awaitFinished`/`ingestPageWithRetry`/`ingestPage`/`resubmitDay` |
| `service/backfill/event_ingester.go` | `fetchAndIngestEventPage`：event 结果页流式入 pipeline |
| `service/backfill/user_writer.go` | `streamUserPage`：user 表行流式快照 upsert |
| `service/backfill/sqlbuilder.go` | `buildDaySQL`：构造按日 Presto SQL |
| `service/backfill/client.go` | TA OpenAPI facade：submit / task-info / result-page / cancel + envelope/retry |
| `service/backfill/httpclient.go` | `NewHTTPClient`：HTTP / proxy / socks5 transport 构造 |
| `service/backfill/ndjson.go` | result-page NDJSON 流式解析 helper |
| `service/backfill/checkpoint.go` | `Checkpoint`：按 `RunID` 的 Mongo 进度存储 + `SQLSignature` |
| `service/backfill/progress.go` | `ProgressBar`：进度条 / 周期统计输出 |
| `service/backfill/rowdecode.go` | `EncodeRowAsJSONLine`：TA 结果行 → TA JSON line |
| `service/backfill/stats.go` | `Stats`：嵌入 `ingestion.Counters` + `Pages`/`HTTPErrors`/`DaysCompleted`/`DaysFailed` |

> `examples/` 下是独立的演示程序，不属于二进制：`examples/client/ingest`、`examples/client/ingestbatch` 演示 SDK 单条/批量上报，`examples/logpattern` 演示 tailer 的 glob 匹配；`examples/config/` 是各角色的样例配置（见 [config.md](config.md)）。

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
