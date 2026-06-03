# Tango internal 重构方案

## 1. 分析范围

本方案聚焦 `internal` 目录的实现边界、职责拆分和后续可落地重构顺序，不重复 `.omo/plans/tango-startup-architecture-refactor.md` 已经覆盖的启动命令体系重构。

已检查的主要路径：

- `internal/core/*`：基础能力，包括 `talog`、`filter`、`store`、`tailer`、`taskqueue`、`remoteconfig`、`dynamicbatch`、`cli`。
- `internal/process/*`：处理流程，包括异步 pipeline 与同步 ingest。
- `internal/service/*`：运行服务，包括 report、worker、gateway、backfill。
- `config/role.go`、`cmd/*`：用于理解角色化配置和命令入口如何投影到 internal runtime。

当前 `go test ./internal/...` 通过，可作为重构前的基础回归门槛。

## 2. 当前结构判断

当前目录分层大体合理：

```text
cmd/                  CLI 入口和角色命令
config/               配置 schema、默认值、校验、角色投影
client/               外部 SDK / operator / gateway 复用的客户端能力
internal/core/        原子基础设施和领域基础组件
internal/process/     可复用处理流程
internal/service/     长生命周期服务或复杂业务运行器
```

现有依赖方向总体是：

```text
service -> process -> core
service -> core
process -> core
core -> config
cmd -> service / client / config
```

这个方向没有明显反向依赖或循环依赖，说明不需要推倒重来。真正的问题是局部模块职责过重、同类逻辑重复、运行时资源装配散落在多个服务中。

## 3. 主要问题

### 3.1 MongoDB 连接和 Store 装配重复

多处重复执行 `mongo.Connect`、`config.MongoDBFromURI`、`store.New`：

- `internal/service/report/report.go:89`
- `internal/service/worker/worker.go:40`
- `internal/service/backfill/runner.go:115`
- `internal/process/ingest/ingest.go:47`
- `internal/core/remoteconfig/startup.go:26`

影响：

- 错误处理、超时、日志、连接归属语义不统一。
- 单进程组合多个角色时容易创建重复连接。
- 单测和集成测试需要绕过真实连接时成本偏高。

### 3.2 ingest 与 pipeline 存在处理语义重复

异步 pipeline 和同步 ingest 都包含类似步骤：解析、filter、identity resolve、路由 user/event/dead_letter、写入 MongoDB。

相关路径：

- `internal/process/pipeline/worker.go:103`
- `internal/process/ingest/ingest.go:121`
- `internal/process/ingest/ingest.go:183`
- `internal/service/backfill/runner.go:463`
- `internal/service/backfill/runner.go:500`

影响：

- 过滤错误、identity 错误、dead letter、user/event 写入语义可能逐渐分叉。
- 新增一种 ingestion 入口时需要复制业务规则。
- backfill event 复用 pipeline，但 user backfill 走手写流式写入，边界不够清楚。

### 3.3 backfill Runner 职责过大

`internal/service/backfill/runner.go` 约 647 行，当前同时负责：

- MongoDB / Store / HTTP client 装配。
- checkpoint 初始化和 day/chunk 调度。
- TA SQL 构造。
- submit / poll / retry / expired resubmit。
- result page streaming。
- event page 转 NDJSON 后复用 pipeline。
- user page 直接构建 Mongo write model。
- progress bar 和统计日志。

影响：

- SQL 生成、分页恢复、用户表写入、进度展示难以独立测试。
- Runner 改动很容易引发隐性回归。
- `ExecuteSQL` 与 `Run` 共享了一部分行为，但入口语义不同，容易继续膨胀。

### 3.4 worker Service 混合了任务循环和任务执行器

`internal/service/worker/worker.go` 约 437 行，当前同时负责：

- instance heartbeat / deregister。
- task claim / lease renewal / reap。
- task result report。
- `report-sync` 远程配置写入。
- backfill / sql payload decode 与 config overlay。
- backfill Runner 生命周期管理。

影响：

- task queue worker 的通用生命周期和具体 task handler 耦合。
- 新增 task type 会继续扩张 `execute` switch。
- payload schema 解析逻辑和任务执行逻辑混在服务层。

### 3.5 Stats 模型重复且分散

已有 `pipeline.StatsCollector`，但 report 和 backfill 各自实现计数结构：

- `internal/process/pipeline/worker.go:20`
- `internal/service/report/report.go:30`
- `internal/service/backfill/runner.go:25`

影响：

- 指标字段命名和统计时机难以统一。
- report 的周期日志、backfill 的 summary、worker task result 之间没有共同快照模型。
- 后续接入 Prometheus / OpenTelemetry 会重复改多处。

### 3.6 pipeline 写入错误只记录不返回

`internal/process/pipeline/worker.go:226` 和 `internal/process/pipeline/worker.go:241` 中的 `flushBatch` / `flushBatchOrdered` 只记录错误并 reset batch，`RunWorkers` 本身不返回 error。

影响：

- report 作为常驻服务可以容忍局部写入失败，但 backfill / sql 这类一次性导入需要更明确的失败语义。
- 当前 backfill 通过 stats 记录写入错误，但调用方拿到的 `Run` / `ExecuteSQL` 结果不一定能反映写入失败。

这部分需要谨慎处理，不能简单把所有写入错误都变成 fatal，否则可能改变 report 的容错行为。

### 3.7 gateway 边界清晰但 HTTP 基础能力偏薄

`internal/service/gateway/server.go` 是较薄的 SDK wrapper，职责本身清楚。短板主要是工程性能力：

- request body size limit 缺失。
- 缺少统一 request timeout / request id / access log。
- handler request struct 分散在函数内部，不利于生成 API 文档或复用校验。

这不是第一优先级，除非 gateway 要暴露给不可信网络。

### 3.8 tailer / store / taskqueue 的单文件职责偏宽

除服务层外，部分 core 文件也偏大：

- `internal/core/tailer/tailer.go`：同时包含 Windows/Linux 路径归一、glob/base-dir 推导、poll/event/hybrid 三种 tail 策略。
- `internal/core/store/writemodel.go`：同时处理 user/event/dead-letter 多类 write model，`user_unset` 分支还通过运行时类型断言回读刚构建的 pipeline 结构。
- `internal/core/store/identity.go`：identity resolve、cache、查询和原子写入策略都在一个文件中。
- `internal/core/taskqueue/queue.go`：publish、claim、renew、complete、fail、reap、get 聚合在一个文件，`Reap` 同时承载多个清理策略。

这些不一定都是第一优先级，但在后续维护中会持续增加阅读成本。

### 3.9 服务层测试缺口明显

当前测试覆盖集中在 `core` 和部分 `backfill` 集成测试，服务层缺口较明显：

- `internal/service/worker` 没有测试，但它承载 task dispatch、lease renew、payload decode 和三类 task handler。
- `internal/service/gateway` 没有测试，但 HTTP method、body decode、默认值 overlay、错误映射都很容易用 `httptest` 覆盖。
- `internal/service/report/dispatch_test.go` 是占位文件，真实 `report.go` 的 remote-config sync、stats reporter、final summary 没有有效测试。
- `internal/process/pipeline/worker.go` 是核心 per-line loop，但没有独立单测。

### 3.10 配置层职责继续膨胀

`config/config.go` 同时承担 schema、defaults、validation、build helper 四类职责。`config/role.go` 又把 `RoleConfig` 投影和角色默认值混在同一组方法里：

- `config/config.go`：包含配置结构、`applyDefaults`、`Validate`、`BuildFilter`、`BuildBackfillFilter`、`BackfillWhere`、batch/page size helper。
- `config/role.go`：`ReportRuntime`、`WorkerRuntime`、`Client` 看起来像纯投影，但 loader 随后立即做 role-specific default / enablement。

影响：

- 配置字段增长后，单文件继续膨胀。
- 调用方难以区分“字段投影”和“默认值/运行模式修正”。
- 后续 role schema 演进时容易引入默认值顺序回归。

### 3.11 backfill client 混合 HTTP、API 与 NDJSON 解析

`internal/service/backfill/client.go` 约 489 行，当前同时负责：

- HTTP transport / proxy / socks5 client 构造。
- TA OpenAPI submit / task-info / result-page / cancel API。
- envelope 错误映射与 task expired 判断。
- NDJSON streaming 解析及 remainder helper。

影响：

- 通用 JSON/NDJSON helper 被锁在 backfill 业务包内。
- HTTP transport 测试、API envelope 测试和 stream parser 测试耦合在一起。

### 3.12 checkpoint 写入粒度可能成为规模瓶颈

`internal/service/backfill/checkpoint.go` 的 `SetDay` / `flush` 当前倾向重写整个 run document。小规模回填没问题，但当 chunk/page 数量变大时，每页 checkpoint 都可能携带完整 days map。

这不是立即重构项，应先加指标确认文档大小和写入频率，再决定是否改成 `$set: {"days.<key>": progress}` 或 run/day 分文档。

## 4. 重构目标

### 4.1 保持现有三层方向

保留 `core / process / service` 的主结构，不把 `process` 下沉到 `core`，也不让 `core` 反向依赖 `process` / `service`。`core` 可以继续细分，但应按能力边界细分，而不是再套一层抽象层级。

建议把 `core` 的内部认知划分为四类：

- `core/pure`：纯领域语义和纯函数能力，例如 `talog`、`filter`、`dynamicbatch`。
- `core/storage`：MongoDB 持久化与控制面存储，例如 `store`、`taskqueue`、`remoteconfig`。
- `core/runtime`：运行时资源装配，例如 Mongo connect、database resolve、owned/borrowed client lifecycle、少量 CLI/runtime helper。
- `core/source`：外部输入源 adapter，例如 `tailer`。

落地时不建议一次性大搬目录。优先新增 `internal/core/runtime` 解决重复装配；其余能力分组先作为架构约束和后续拆包方向。

### 4.2 统一运行时资源装配

把 MongoDB client、database、store、task queue、remote config collection 的装配集中到 `internal/core/runtime`，减少重复连接代码，并显式表达资源 ownership。

### 4.3 建立共享 ingestion 语义

把“解析 -> filter -> identity -> write model / dead letter”的核心规则沉淀为 process 层可复用组件，让 report、operator ingest、gateway ingest、backfill event 路径共享同一语义。

### 4.4 拆分 backfill 编排与执行细节

Runner 只保留“调度一个 backfill run”的职责，把 SQL 构造、TA task polling、page ingestion、user snapshot writer、progress/stats 分离出来。

### 4.5 拆分 worker 生命周期与 task handlers

worker service 只负责 queue lifecycle。具体 task type 通过 handler registry 执行，避免 `execute` switch 无限制增长。

## 5. 建议目标结构

保持 `core / process / service` 外层不大动，`core` 内部按能力边界逐步细分。第一阶段只建议真实新增 `internal/core/runtime`；`pure/storage/source` 可以先作为分组方向，不必一次性移动目录。

```text
internal/core/pure/          # 规划分组：纯领域语义，不依赖 Mongo / runtime
  talog/                     # 可先保持在 internal/core/talog
  filter/                    # 可先保持在 internal/core/filter
  dynamicbatch/              # 可先保持在 internal/core/dynamicbatch

internal/core/storage/       # 规划分组：Mongo-backed storage / control plane
  store/                     # 可先保持在 internal/core/store
  taskqueue/                 # 可先保持在 internal/core/taskqueue
  remoteconfig/              # 可先保持在 internal/core/remoteconfig

internal/core/runtime/
  mongo.go              # Mongo connect、database resolve、owned client lifecycle
  store.go              # Store / task queue / registry 装配 helper

internal/core/source/        # 规划分组：外部输入源 adapter
  tailer/                    # 可先保持在 internal/core/tailer

internal/process/ingestion/
  processor.go          # Parse/filter/identity/route 的共享处理器
  writer.go             # user/event/dead-letter write abstraction
  stats.go              # 统一 StatsCollector / Snapshot

internal/process/pipeline/
  worker.go             # 仅保留并发、batch、flush 调度
  errors.go             # 可选：写入错误策略

internal/core/source/tailer/ # 中长期可选移动；短期只拆文件，不改 import path
  path_windows.go       # Windows/Linux 路径归一和转换
  path_unix.go          # 非 Windows 路径适配
  glob.go               # glob/base-dir 解析
  tailer.go             # tailing state machine

internal/core/storage/store/ # 中长期可选移动；短期只拆文件，不改 import path
  usermodel.go          # user write models
  eventmodel.go         # event write models
  deadletter.go         # dead-letter write model
  identity_cache.go     # identity cache 细节

internal/service/backfill/
  runner.go             # run-level orchestration
  client.go             # TA OpenAPI high-level API only
  httpclient.go         # HTTP/proxy/socks5 transport
  ndjson.go             # result-page NDJSON streaming parser
  sqlbuilder.go         # buildDaySQL / SQL signature 相关
  executor.go           # submit/poll/page orchestration, ExecuteSQL shared flow
  event_ingester.go     # event page -> ingestion pipeline
  user_writer.go        # user table stream -> snapshot upsert
  stats.go              # backfill-specific counters wrapping process stats

config/
  config.go             # schema + Validate only
  defaults.go           # applyDefaults/apply*Defaults
  filter.go             # BuildFilter/BuildBackfillFilter/BackfillWhere
  pipeline.go           # batch sizing helpers
  backfill.go           # page size / paginate / force-skip helpers
  role.go               # role schema + pure projections

internal/service/worker/
  worker.go             # queue loop / heartbeat / lease lifecycle
  handlers.go           # handler interface + registry
  payload.go            # payload decode / overlay
  report_sync.go        # report-sync handler
  backfill_handler.go   # backfill + sql handlers
```

## 6. 分阶段实施计划

### Phase 0：建立重构保护网

目标：先锁定行为，再移动代码。

动作：

1. 保留并持续运行 `go test ./internal/...`。
2. 为当前无测试的服务层补最小单测：
   - `internal/service/worker`：payload decode、`reportSyncFilters`、`overlayBackfillFilter`、unknown task type。
   - `internal/service/gateway`：method check、bad JSON、默认参数 overlay、SDK error 到 HTTP error。
   - `internal/service/report`：remote-config filter changed / unchanged 的可测试逻辑可先抽函数再测。
3. 对 backfill 增加 SQL builder table-driven tests，覆盖 event/user、schemaPrefix、date range、eventTimeRange、limit、filterWhere。
4. 删除或改造 `internal/service/report/dispatch_test.go` 这个占位测试文件。

验收：

- `go test ./internal/...` 通过。
- 新测试在不连接真实外部 TA API 的情况下覆盖 payload、SQL、handler 行为。

### Phase 1：抽取 runtime 装配层

目标：消除 MongoDB 装配重复，但不改变业务流程。

建议新增：

```go
// internal/core/runtime
type MongoResource struct {
    Client *mongo.Client
    DB     *mongo.Database
    Owns   bool
}

func ConnectMongo(ctx context.Context, cfg config.MongoConfig) (*MongoResource, error)
func DatabaseFromClient(client *mongo.Client, uri string) (*mongo.Database, error)
func NewStore(db *mongo.Database, cfg config.Config, logger *logrus.Logger) *store.Store
```

迁移顺序：

1. 先迁移 `internal/service/report/report.go:89`。
2. 再迁移 `internal/process/ingest/ingest.go:47` 和 `internal/process/ingest/ingest.go:79`。
3. 再迁移 `internal/service/worker/worker.go:40`。
4. 最后迁移 `internal/service/backfill/runner.go:115`、`internal/core/remoteconfig/startup.go:26`。

风险控制：

- 不改变 `Shutdown` / `Close` 的对外语义。
- `NewFromClient` 仍不拥有 client，避免误断开外部管理的连接。

### Phase 2：统一 ingestion processor

目标：让同步 ingest、异步 pipeline、backfill event ingestion 共享核心规则。

建议抽象：

```go
type Processor struct {
    Parser   *talog.Parser
    Filter   *filter.Holder
    Store    *store.Store
    Stats    StatsCollector
    Options  Options
}

func (p *Processor) ProcessLine(ctx context.Context, line string) (Result, error)
func (p *Processor) BuildModels(ctx context.Context, line string) (Models, error)
```

迁移顺序：

1. 先让 `internal/process/ingest.Ingest` 使用 processor 生成单条 write model，再沿用现有 immediate write。
2. 让 `internal/process/ingest.IngestBatch` 使用 processor 收集 models。
3. 让 `internal/process/pipeline.worker` 使用 processor 处理 line，只保留 batch 和 flush。
4. 让 `internal/service/backfill.fetchAndIngestEventPage` 继续通过 pipeline，但底层语义已统一。

注意：

- `pipeline` 的 affinity dispatch 不能动，它保证同一用户顺序。
- user writes 仍需 ordered bulk write。
- report 过滤丢弃不写 dead letter 的语义要保持。
- parse / identity 错误写 dead letter 的语义要保持。

### Phase 3：拆分 backfill Runner

目标：把 647 行 Runner 拆成可测试的子组件。

建议拆分顺序：

1. `sqlbuilder.go`：移动 `buildDaySQL`，补 table-driven tests。
2. `poller.go` 或 `executor.go`：移动 `awaitFinished`、`ingestPageWithRetry`、`resubmitDay` 的 TA task lifecycle。
3. `event_ingester.go`：移动 `fetchAndIngestEventPage`。
4. `user_writer.go`：移动 `streamUserPage`、`userFlushBatch`、`indexOf`。
5. `stats.go`：保留 backfill-specific counters，同时嵌入或适配 process stats snapshot。

拆完后 `Runner` 只负责：

- 初始化 checkpoint。
- 遍历 pending days。
- 调用 executor 处理 chunk。
- 记录 chunk 成功/失败。
- 输出 summary。

风险控制：

- 不改变 checkpoint 文档结构。
- 不改变 task expired 后 resubmit 的行为。
- 不改变 user table 使用 `#user_id` upsert 和 event table 使用 `#uuid` 去重的策略。

### Phase 4：拆分 worker task handlers

目标：让 worker queue 生命周期和任务执行逻辑解耦。

建议接口：

```go
type Handler interface {
    Type() taskqueue.TaskType
    Execute(ctx context.Context, task *taskqueue.Task) (map[string]any, error)
}
```

拆分：

- `report_sync.go`：`TaskReportSync`，负责 filter 编译和 remote config upsert。
- `backfill_handler.go`：`TaskBackfill`，负责 payload -> backfill config -> Runner。
- `sql_handler.go`：`TaskSQL`，负责 sql payload -> Executor。
- `payload.go`：`decodePayload`、`overlayBackfillFilter`、`toStringSlice`。

`worker.Service.execute` 变成：

```go
handler, ok := a.handlers[task.Type]
if !ok { return nil, unknownTaskError }
return handler.Execute(ctx, task)
```

风险控制：

- claim / lease / renew / report success-failure 不和 handler 混在一起。
- handler 不能直接 complete/fail task，只返回 result/error。

### Phase 5：统一 Stats / Snapshot / Error policy

目标：为 report、backfill、gateway/operator 可观测性打基础。

建议：

- 将 `pipeline.StatsCollector` 扩展为 `process/ingestion.Stats`。
- 提供 `Snapshot()`，包含 line、parse、identity、write、filter、dead_letter。
- backfill stats 组合 ingestion stats，并追加 pages、http_errors、days_completed、days_failed。
- report periodic log 和 final log 使用统一 snapshot。

写入错误策略需要单独设计：

- report：默认继续运行，写入错误进入 stats 和日志。
- backfill/sql：建议可配置为 `failOnWriteError=true`，默认先保持现状，再通过新配置开启严格模式。

### Phase 6：拆分 core 大文件，并逐步建立 core 能力分组

目标：在服务层和 process 层边界稳定后，降低 core 文件阅读成本，并把 `core` 的内部边界从“一个大底层”细化为 `pure / storage / runtime / source` 四类能力。

分组原则：

- `pure`：不能依赖 Mongo、runtime、process、service，只承载领域语义和纯算法。
- `storage`：可以依赖 Mongo driver 和 `config`，但不能依赖 `process` / `service`。
- `runtime`：负责资源装配和 ownership，不承载业务处理规则。
- `source`：承载外部输入源 adapter，例如文件 tailer；不承载写入、identity、task queue 策略。

建议顺序：

1. 先新增 `internal/core/runtime`，承接 Mongo connect、database resolve、resource ownership；这是唯一建议立即真实新增的 core 子层。
2. `internal/core/tailer/tailer.go`：先拆路径转换和 glob/base-dir 逻辑，再保留 tail 状态机；短期不改 import path，长期可归入 `core/source/tailer`。
3. `internal/core/store/writemodel.go`：按 user/event/dead-letter 拆文件；重写 `user_unset` 分支，避免 `pipeline[0].(bson.M)` 这类运行时断言；长期可归入 `core/storage/store`。
4. `internal/core/taskqueue/queue.go`：把 `Reap` 策略拆出到独立文件，保持 queue public API 不变；长期可归入 `core/storage/taskqueue`。
5. `internal/core/store/identity.go`：最后拆 identity cache 和 query helper，因为该模块数据一致性风险更高。

风险控制：

- 只做文件拆分和内部 helper 提取，不改 public API。
- 不为追求目录美观一次性迁移 `talog/filter/store/taskqueue/tailer` 的 import path。
- `store` 与 `taskqueue` 的集成测试必须保持通过。
- `writemodel` 拆分前先补 `user_unset` 回归测试。

### Phase 7：拆分配置层与 backfill client

目标：降低非 internal 但强影响 internal 的配置和 API 客户端复杂度。

动作：

1. `config/config.go`：只保留 schema 和 `Validate`；把 defaults、filter builder、pipeline/backfill helper 拆到独立文件，保持方法签名不变。
2. `config/role.go`：把 `ReportRuntime` / `WorkerRuntime` / `Client` 改造成纯投影，role-specific default / enablement 放回 `LoadReport` / `LoadWorker` / `LoadGateway` / `LoadOperator` 的显式步骤。
3. `internal/service/backfill/client.go`：拆出 `httpclient.go` 和 `ndjson.go`，保留 `client.go` 作为 TA OpenAPI facade。
4. `internal/core/remoteconfig`、`internal/service/worker`、`config/loader.go`：抽统一 mapstructure decoder helper，避免 hook 配置分叉。

风险控制：

- 配置 public method 名称先不变，仅移动实现。
- role loader 的默认值顺序必须用现有 config tests 锁住。
- backfill client 拆分只移动私有 helper，不改 HTTP request/response 语义。

### Phase 8：checkpoint 写入优化（先量测，后改造）

目标：只在真实大规模回填遇到 checkpoint 写入成本时优化。

建议：

1. 先在 checkpoint flush 周边加可选 debug metric，记录 marshaled document size、days count、flush frequency。
2. 如果确认是瓶颈，再把 day progress 改成局部 `$set` 或 run/day 分文档。
3. 保持 `SQLSignature` 计算方式和现有 checkpoint 文档兼容，必要时做 schema migration。

风险控制：

- 不在没有规模证据时调整 checkpoint schema。
- `SQLSignature` 是跨重启恢复契约，不能随意改输入字段或 hash 格式。

## 7. 不建议现在做的事

- 不建议大规模改包名或移动所有文件。当前 `core/process/service` 分层可保留。
- 不建议一次性把 `internal/core/{talog,filter,store,taskqueue,tailer}` 全部搬到 `pure/storage/source` 子目录；先用能力分组指导新增代码和局部拆文件。
- 不建议把 `process/pipeline` 下沉到 `core`；如果 `core` 反复想用 `process` 能力，应拆 `core/pure` / `core/storage` 或让 `process` 通过 adapter 注入，而不是放宽反向依赖。
- 不建议马上把 `config.Config` 从 `core/store` 中完全剥离，收益低且牵连大。
- 不建议先改 CLI 命令体系；已有 `.omo/plans/tango-startup-architecture-refactor.md` 已覆盖这条线，internal 重构应避免与命令迁移交叉。
- 不建议在 Phase 1-4 中改变 Mongo collection 名称、checkpoint schema、task queue schema。
- 不建议把 backfill user path 强行塞进 event pipeline；user table 没有 event envelope，直接 writer 是合理的，只需要把 writer 独立出来。
- 不建议在未补测试前重写 `tailer` 的 tailing 策略；可以先拆路径/glob helper，保留状态机行为。
- 不建议改 `internal/core/store/identity.go` 的原子 resolve 分支；这类并发一致性代码收益低、风险高。
- 不建议改 `internal/core/taskqueue/queue.go` 的 `Claim` 原子 update pipeline；它是 worker 并发 claim 正确性的核心。
- 不建议改 `internal/process/pipeline/dispatch.go` 的 affinity routing/hash 语义；这关系到同一用户事件顺序。
- 不建议改 `internal/core/filter/filter.go` 中 `#field` 表达式 rewrite 逻辑，除非 expr-lang 本身提供原生支持。
- 不建议改 backfill checkpoint `SQLSignature` 格式；它是跨重启恢复和 signature mismatch 检测契约。

## 8. 推荐落地顺序

优先级从高到低：

1. **Phase 0 测试保护网**：成本低，能降低后续移动代码风险。
2. **Phase 1 runtime 装配层**：重复最明显，改动机械，收益稳定。
3. **Phase 4 worker handlers**：边界清楚，能快速降低 worker 文件复杂度。
4. **Phase 3 backfill Runner 拆分**：收益高但风险更高，应在 SQL builder tests 后进行。
5. **Phase 2 ingestion processor**：长期价值高，但会触碰 report、ingest、pipeline 多条主链路，建议等前面完成后再做。
6. **Phase 5 stats/error policy**：依赖 processor 边界稳定后再统一。
7. **Phase 6 core 大文件拆分与能力分组**：先新增 `core/runtime`，再按 `pure/storage/source` 的方向拆大文件；真实目录迁移放后。
8. **Phase 7 config/backfill client 拆分**：影响面跨 internal/config/client，建议等服务层边界稳定后做。
9. **Phase 8 checkpoint 写入优化**：必须先量测，只有确认规模瓶颈再改 schema 或局部更新。

## 9. 每阶段验收标准

通用验收：

```bash
go test ./internal/...
```

阶段专项验收：

- Phase 1：所有 Mongo connect helper 单测通过；服务 `Shutdown`/`Close` 语义不变。
- Phase 2：`internal/process/ingest`、`internal/process/pipeline` 现有测试不需要重写业务断言。
- Phase 3：backfill integration tests 仍通过；SQL builder 单测覆盖 user/event 分支。
- Phase 4：worker handler 单测可在无 Mongo 连接情况下验证 payload 和 dispatch。
- Phase 5：report/backfill stats 字段含义一致，backfill 写入错误策略有显式测试。
- Phase 6：`tailer`、`store`、`taskqueue` public API 不变；`core` 仍不得 import `process` / `service`；现有测试无需改断言即可通过。
- Phase 7：配置 tests 全部通过；role loader 投影前后生成的 runtime config 保持一致。
- Phase 8：旧 checkpoint 文档仍能读取；signature mismatch 行为不变。

## 10. 最小首个 PR 建议

首个 PR 不建议直接拆 backfill。建议只做：

1. 新增 `internal/core/runtime` 的 Mongo connect helper，作为 `core` 能力分组中唯一首批真实新增子层。
2. 迁移 `report.New` 和 `ingest.New` 到 helper。
3. 增加 helper 单测和 `ingest.NewFromClient` ownership 回归测试。
4. 保持 public command、config、task schema、checkpoint schema 全部不变。

这样首个 PR 风险小、收益明确，并为后续 service/backfill/worker 拆分铺路。
