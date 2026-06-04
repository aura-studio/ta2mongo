# tango v1.0 / v1.1 详细 feature 对比

本文记录 `tango` 项目 `v1.0` 与 `v1.1` 的功能、接口、配置和代码结构差异。

对比方式：不 checkout，不改工程状态，只用 Git 历史引用读取与比较。

- 对比基线：`git diff v1.0..v1.1`
- `v1.0`：`8bc899b`，`git describe` 为 `v1.0.2`
- `v1.1`：`4836c52`，`git describe` 为 `v1.0.2-31-g4836c52`
- `origin/v1.1`：`3757c8e`。本记录按本地 `v1.1` 引用对比，不按远端分支。
- 总体 diff：`171 files changed, 4154 insertions(+), 12237 deletions(-)`
- 文件数量：`v1.0` 约 138 个文件，`v1.1` 约 100 个文件
- 文件变更：新增 41 个、删除 79 个、重命名 33 个
- 工作区未提交内容不纳入本记录。

## 一句话结论

`v1.0` 是一个全功能数据接入/控制平台：日志上报、HTTP gateway、operator、worker、任务队列、远程配置、历史回填、临时 SQL、公开 Go SDK 都在一个仓库/二进制体系里。

`v1.1` 是一次大幅收敛：删除控制面、任务系统、回填/SQL 和公开 SDK，聚焦为 ThinkingData 日志写 MongoDB 的上报引擎。保留的核心是 daemon/gateway/cli/api 四种入口形态，共享 `single` / `batch` / `pipeline` 三种上传策略。

## 角色与命令

| 能力 | v1.0 | v1.1 | 差异 |
|---|---|---|---|
| 文件追尾常驻上报 | `tango report run` | `tango daemon` | 保留能力，命令改名，配置段从 `report.*` 迁到 `source.*` / `process.*` / `parser.*` |
| 任务 worker | `tango worker run` | 无 | 删除 task queue 消费、心跳、lease、reap、任务执行 |
| HTTP 服务 | `tango gateway serve` | `tango gateway` | 保留 HTTP 服务，但接口从多功能控制面收窄到 `/upload` |
| 一次性操作 | `tango operator ...` | `tango cli upload` | operator 删除；cli 子命令对齐 gateway `/upload`，从 stdin 读日志并上报 |
| Go 库入口 | 公开 `github.com/aura-studio/tango/client` | 内部 `internal/role/api` | v1.1 只允许仓库内部 import，对外是 breaking change |
| 根命令 | 挂载 report/worker/gateway/operator | 挂载 daemon/gateway/cli | 角色集合整体变更 |

### v1.0 命令集合

```bash
tango report run
tango worker run
tango gateway serve
tango operator ingest
tango operator upload
tango operator backfill
tango operator sql
tango operator publish report-sync
tango operator publish backfill
tango operator publish sql
```

### v1.1 命令集合

```bash
tango daemon
tango gateway
tango cli upload --process.mode single
tango cli upload --process.mode batch
tango cli upload --process.mode pipeline
```

`v1.1` 的角色由子命令决定，不再通过配置字段表达。CLI 操作子命令与 gateway path 对齐，例如 `tango cli upload` 对齐 `POST /upload`。`--config` 仍是配置文件路径，但不是配置键。上传策略统一使用配置键 `process.mode`，而不是请求字段或独立运行参数。

## HTTP API

| HTTP 接口 | v1.0 body / 功能 | v1.1 状态 | 说明 |
|---|---|---|---|
| `GET /healthz` | 健康检查 | 保留 | 语义基本不变 |
| `POST /ingest` | `{"line":...}` 或 `{"lines":[...]}`，字符串上报 | 删除 | 可改用 `POST /upload`，传 `line` / `lines` |
| `POST /upload` | `{"patterns":[...],"batchSize":N}`，按文件模式上传，带断点续传 | 保留但语义改变 | v1.1 变成日志数组上传：`{"line":...,"lines":[...]}`，策略由 `process.mode` 决定 |
| `POST /backfill` | 直接执行历史回填 | 删除 | 回填模块整体删除 |
| `POST /sql` | 执行临时 SQL 并导入 | 删除 | SQL 导入模块整体删除 |
| `POST /publish/report-sync` | 发布 report-sync 任务 | 删除 | taskqueue 和 remote config 删除 |
| `POST /publish/backfill` | 发布 backfill 任务 | 删除 | taskqueue 和 backfill 删除 |
| `POST /publish/sql` | 发布 SQL 任务 | 删除 | taskqueue 和 SQL 删除 |

### v1.1 `/upload` 语义

```json
{
  "line": "{\"#type\":\"track\"}",
  "lines": [
    "{\"#type\":\"track\"}",
    "{\"#type\":\"user_set\"}"
  ]
}
```

- 上传策略来自 `process.mode`：`single` / `batch` / `pipeline`
- `line` 与 `lines` 会合并成同一个 httpbody source
- 返回本次统计：行数、user 写入、event 写入、dead letter、filtered 等

## 上传与处理链路

| 处理面 | v1.0 | v1.1 | 结论 |
|---|---|---|---|
| 常驻文件追尾 | report service 使用 tailer + pipeline worker | daemon 使用 `source/tailer` + `process.ModePipeline` | 保留并重命名 |
| 字符串上报 | SDK `Ingest` / `IngestBatch`，gateway `/ingest`，operator `ingest` | gateway `/upload`、cli stdin、api `Upload` | 入口变少，但底层策略统一 |
| 文件单次上传 | SDK/operator/gateway 的 `UploadFiles`，有 checkpoint | 删除 | v1.1 不再提供按文件模式的一次性断点续传 |
| 批量策略 | report pipeline 与 SDK batch 分散实现 | `process.mode` + `process.New(cfg, ...)` 统一构造 single/batch/pipeline | v1.1 新增统一上传引擎门面 |
| 数据来源 | tailer、HTTP、SDK、backfill 混在不同服务/SDK 中 | `source.Source` 统一抽象：tailer/httpbody/stdin/taapi | v1.1 源抽象更清晰 |
| 过滤 | report filter、upload filter、backfill filter 分开 | 顶层 `parser.filter.*` 作用于上报路径 | v1.1 只保留上报 filter |

### v1.1 三种上传模式

| mode | 说明 | 典型入口 |
|---|---|---|
| `single` | 逐行即时写，适合少量日志或低延迟写入 | gateway / cli / api |
| `batch` | 同步累积写模型后 bulk write，适合一次性批量输入 | gateway / cli / api |
| `pipeline` | N worker 异步流水线，按用户亲和性路由并动态刷新批次 | daemon，gateway / cli / api 也可选 |

## 配置 schema

`v1.0` 是角色型 schema：`runtime`、`report`、`remoteConfig`、`tasks`、`gateway`、`upload`、`backfill`、`backfillFilter`、`sql`。

`v1.1` 是包路径 schema：配置键路径等于消费它的 internal 包路径，例如 `dao.mongo.uri`、`source.tailer.logPattern`、`process.pipeline.batchSize`。

### 配置来源与优先级

| 项 | v1.0 | v1.1 |
|---|---|---|
| 基础优先级 | 默认值 < 文件 < 环境变量 < flag | 默认值 < 文件 < 环境变量 < flag |
| 远程配置 | report 可启用 `remoteConfig.enabled`，只热更新 filter | 删除 |
| flag 覆盖 | 只有部分常用键显式注册 flag | 每个配置键都有同名 `--<键>` flag |
| 角色指定 | 角色命令 + 各自 loader | 子命令指定，配置里不写角色 |
| 默认配置文件 | report/worker/gateway/operator | daemon/gateway/cli |

### 常用配置迁移映射

| v1.0 key | v1.1 key | 说明 |
|---|---|---|
| `runtime.logging.level` | `logging.level` | 日志级别 |
| `runtime.logging.format` | `logging.format` | 日志格式 |
| `runtime.mongo.uri` | `dao.mongo.uri` | MongoDB 连接串 |
| `runtime.mongo.connectTimeout` | `dao.mongo.connectTimeout` | MongoDB 连接超时 |
| `runtime.mongo.serverSelectionTimeout` | `dao.mongo.serverSelectionTimeout` | server selection 超时 |
| `runtime.mongo.maxElapsedTime` | `dao.store.maxElapsedTime` | bulk-write 重试预算从 mongo 归属迁到 store |
| `report.source.logPattern` | `source.tailer.logPattern` | daemon 追尾文件模式 |
| `report.source.tailMode` | `source.tailer.tailMode` | tail 模式 |
| `report.source.rescanInterval` | `source.tailer.rescanInterval` | 重新扫描间隔 |
| `report.source.pollInterval` | `source.tailer.pollInterval` | poll 间隔 |
| `report.source.maxLineBytes` | `source.tailer.maxLineBytes` | 单行最大字节数 |
| `report.pipeline.batchSize` | `process.pipeline.batchSize` | pipeline 批大小 |
| `report.pipeline.batchSizeMin` | `process.pipeline.batchSizeMin` | 自适应批下限 |
| `report.pipeline.batchSizeMax` | `process.pipeline.batchSizeMax` | 自适应批上限 |
| `report.pipeline.batchWorkers` | `process.pipeline.batchWorkers` | pipeline worker 数 |
| `report.pipeline.flushInterval` | `process.pipeline.flushInterval` | flush 间隔 |
| `report.pipeline.channelBuffer` | `process.pipeline.channelBuffer` | worker channel buffer |
| `report.pipeline.deadLetterCap` | `process.pipeline.deadLetterCap` | dead letter 批容量 |
| `report.filter.include` | `parser.filter.include` | 上报 include filter |
| `report.filter.exclude` | `parser.filter.exclude` | 上报 exclude filter |
| `upload.string.batchSize` | `process.batchSize` | single/batch 策略批大小 |
| `upload.string.filter.*` | `parser.filter.*` | v1.1 顶层共享上报 filter |
| `upload.file.filter.*` | `parser.filter.*` | 文件上传独立 filter 消失，统一到上报 filter |
| `gateway.addr` | `role.gateway.addr` | gateway 监听地址 |
| 无 | `process.mode` | v1.1 新增，统一控制 gateway / cli / api 的上传策略 |

### 已删除配置段

- `remoteConfig.*`
- `tasks.*`
- `upload.file.logPattern`
- `upload.file.checkpointCollection`
- `backfill.*`
- `backfillFilter.*`
- `sql.*`

### 环境变量迁移示例

| v1.0 env | v1.1 env |
|---|---|
| `TANGO_RUNTIME_MONGO_URI` | `TANGO_DAO_MONGO_URI` |
| `TANGO_RUNTIME_LOGGING_LEVEL` | `TANGO_LOGGING_LEVEL` |
| `TANGO_REPORT_SOURCE_TAILMODE` | `TANGO_SOURCE_TAILER_TAILMODE` |
| `TANGO_GATEWAY_ADDR` | `TANGO_ROLE_GATEWAY_ADDR` |
| `TANGO_REMOTECONFIG_ENABLED` | 删除 |
| `TANGO_TASKS_INSTANCEID` | 删除 |

## Go API / SDK

### v1.0 公开 SDK

v1.0 提供公开包：

```go
import "github.com/aura-studio/tango/client"
```

主要能力：

- `New`
- `Close`
- `EnsureIndexes`
- `Ingest`
- `IngestBatch`
- `Ping`
- `Stats`
- `PublishConfig`
- `PublishFilter`
- `GetPublishedConfig`
- `UploadFiles`
- `RunBackfill`
- `ExecuteSQL`
- `PublishTask`
- `PublishBackfillTask`
- `PublishSQLTask`
- `PublishReportSync`
- `GetTask`
- `ListInstances`

### v1.1 内部 API

v1.1 删除公开 `client/` 目录，新增仓库内部包：

```go
import "github.com/aura-studio/tango/internal/role/api"
```

主要能力：

- `New`
- `Close`
- `EnsureIndexes`
- `Run(ctx, source)`
- `Upload(ctx, lines)`

影响：

- 仓库外代码不能 import `internal/role/api`
- 原来依赖 `client.Ingest` / `UploadFiles` / `RunBackfill` / `ExecuteSQL` / `PublishTask` 的调用方必须重写
- 如果仍需要外部 SDK，需要重新设计一个公开 package 包装 `internal/role/api`

## 数据库集合与运行时状态

| 集合/状态 | v1.0 | v1.1 | 说明 |
|---|---|---|---|
| `user` | 使用 | 使用 | 核心数据集合保留 |
| `event` | 使用 | 使用 | 核心数据集合保留 |
| `dead_letter` | 使用 | 使用 | 解析/写入异常记录保留 |
| `id_mapping` | 使用 | 使用 | identity resolve 保留 |
| `_tango_config` | 使用 | 删除 | remote config 删除 |
| `_tango_tasks` | 使用 | 删除 | taskqueue 删除 |
| `_tango_instances` | 使用 | 删除 | worker heartbeat 删除 |
| `_tango_fileupload` | 使用 | 删除 | 文件断点续传删除 |
| `_backfill_progress` | 使用 | 删除 | backfill checkpoint 删除 |

## 代码结构变化

| v1.0 路径 | v1.1 路径/状态 | 说明 |
|---|---|---|
| `client/` | 删除 | 公开 SDK 移除 |
| `cmd/report/` | `cmd/daemon/` | report service 改名 daemon |
| `cmd/worker/` | 删除 | worker 命令移除 |
| `cmd/operator/` | 删除 | operator 命令移除 |
| `cmd/shared/` | 删除 | cmd glue 内联到各命令 |
| `cmd/gateway/` | 保留但重写 | 从 `gateway serve` 变为 `gateway` |
| 无 | `cmd/cli/` | 新增 stdin 上报入口；子命令按 gateway path 对齐 |
| `config/role.go` | 删除 | 不再使用角色投影 schema |
| `config/client.go` | 删除 | client runtime projection 删除 |
| `config/backfill.go` | 删除 | backfill 配置删除 |
| `config/filter.go` | 删除 | backfill SQL/filter helper 删除 |
| `config/pipeline.go` | 删除/下沉 | pipeline 配置下沉到 `internal/process/pipeline` |
| `internal/core/store/` | `internal/dao/store/` | 数据持久化移动到 dao 领域 |
| `internal/core/runtime/mongo.go` | `internal/dao/mongo/mongo.go` | Mongo resource 移到 dao/mongo |
| `internal/core/filter/` | `internal/parser/filter/` | 上报 filter 移到 parser 领域；SQL 编译能力删除 |
| `internal/core/talog/` | `internal/parser/talog/` | TA log parser 移到 parser 领域 |
| `internal/core/tailer/` | `internal/source/tailer/` | tailer 归入 source 领域 |
| `internal/core/dynamicbatch/` | `internal/process/pipeline/` | 动态批刷新归入 pipeline |
| `internal/core/taskqueue/` | 删除 | task queue 删除 |
| `internal/core/remoteconfig/` | 删除 | remote config 删除 |
| `internal/process/ingest/` | 删除/拆分 | 同步 ingest 能力重组为 single/batch uploader |
| `internal/process/ingestion/` | `internal/process/single/` | 共享 processor/counter/stat 归入 single 体系 |
| `internal/service/report/` | `internal/role/daemon/` | report service 改为 daemon role |
| `internal/service/gateway/` | `internal/role/gateway/` | gateway runtime 移入 role |
| `internal/service/worker/` | 删除 | worker runtime 删除 |
| `internal/service/backfill/` | 删除 | backfill runtime 删除 |
| 无 | `internal/logging/` | 新增全局日志包 |
| 无 | `internal/source/httpbody/` | 新增 HTTP body 数据源 |
| 无 | `internal/source/stdin/` | 新增 stdin 数据源 |
| 无 | `internal/source/taapi/` | 新增预留 TA API 数据源 |
| 无 | `internal/role/api/` | 新增内部可复用上报引擎 |

## v1.1 保留或强化的能力

- 追尾日志文件上报到 MongoDB。
- TA JSON line 解析。
- `#account_id` / `#distinct_id` identity resolve。
- 写入 `user` / `event` / `dead_letter`。
- 上报 filter：include/exclude，作用于 `#type`、`#event_name`、`properties.*`。
- pipeline worker、用户亲和性路由、动态批刷新。
- MongoDB index 初始化。
- bulk write 重试预算。
- HTTP `/upload`。
- CLI 从 stdin 上报。
- `single` / `batch` / `pipeline` 三种策略统一抽象。
- `source.Source` 数据源接口。
- 全局日志 `internal/logging`。
- goroutine panic recovery：daemon stats reporter 与 gateway serve goroutine 等处增强健壮性。
- 文件/env/flag 三种配置途径完全同名、同语义。

## v1.1 删除的能力

- 公开 `client/` SDK。
- `tango operator ...` 命令树。
- `tango worker run`。
- MongoDB task queue：publish、claim、lease、heartbeat、reap、target。
- report-sync。
- remote config filter 热更新。
- TA OpenAPI 历史 backfill。
- backfill checkpoint / progress / retry / pagination。
- 临时 SQL 执行与导入。
- backfill filter 与 SQL 下推。
- Gateway 控制面接口：`/ingest`、`/backfill`、`/sql`、`/publish/*`。
- 文件单次上传和断点续传：`UploadFiles` / `_tango_fileupload`。
- operator/gateway 作为任务发布端。
- worker 作为任务消费端。
- report/worker/operator 示例配置。

## 兼容性风险

| 风险 | 影响 |
|---|---|
| 命令不兼容 | `report run`、`worker run`、`gateway serve`、`operator` 在 v1.1 不存在 |
| 配置不兼容 | v1.0 配置文件不能直接用于 v1.1，需要迁移 key |
| 环境变量不兼容 | `TANGO_RUNTIME_*`、`TANGO_REPORT_*`、`TANGO_TASKS_*` 等大量变量失效 |
| HTTP API 不兼容 | `/upload` 的语义改变；其他控制面接口删除 |
| Go import 不兼容 | `github.com/aura-studio/tango/client` 删除，外部调用方无法直接用 `internal/role/api` |
| 数据库辅助集合不兼容 | task/remote/backfill/fileupload 相关集合不再读写 |
| 运行模型不兼容 | v1.1 不再支持“采集 + worker 控制面”组合部署 |
| 功能范围不兼容 | backfill、SQL、任务发布、远程热更新若仍需要，必须重新引入或另建服务 |

## v1.0 到 v1.1 迁移清单

1. 将 `tango report run` 改为 `tango daemon`。
2. 删除 `tango worker run` 部署；确认是否仍需要 taskqueue/backfill/sql/report-sync。
3. 将 `tango gateway serve` 改为 `tango gateway`。
4. 将 gateway 客户端调用从 `/ingest` 改为 `/upload`。
5. 注意 `/upload` body 从文件模式 `patterns/batchSize` 改成日志数组 `line/lines`，上传策略改由 `process.mode` 配置。
6. 将 `operator ingest` 替换为 `tango cli upload --process.mode <mode>` + stdin。
7. 删除或替代 `operator upload/backfill/sql/publish` 流程。
8. 按配置迁移映射改写配置文件。
9. 按环境变量迁移映射改写部署变量。
10. 将 `runtime.mongo.maxElapsedTime` 移到 `dao.store.maxElapsedTime`。
11. 将 `report.filter.*` / `upload.*.filter.*` 合并为 `parser.filter.*`。
12. 将 `gateway.addr` 改为 `role.gateway.addr`；如需调整上传策略，设置 `process.mode`。
13. 外部 Go 代码若 import `client/`，需要新增公开适配层或改成同仓库内部调用。
14. 如果仍需要历史回填或 SQL 导入，不能只靠配置迁移，需要恢复/重建被删除的 backfill 与 SQL 模块。

## 重要提交脉络

| 提交 | 主题 | 影响 |
|---|---|---|
| `48d6e38` | Refactor ingestion pipeline and DAO/package structure | 开始重组 ingest、DAO、包结构 |
| `8a64233` | Rename core store packages to dao and refresh imports | store 迁到 dao |
| `b415b83` | restructure internal layout | 形成 source/parser/process/engine 分层雏形 |
| `41fe303` | unify upload engine across roles | 统一上传引擎和配置键路径 |
| `be56c4b` | daemon and gateway commands into role-specific packages | 命令按角色包重组 |
| `1151c96` | gateway client into role package and NewServer | gateway runtime 迁入 role |
| `3757c8e` | Validate pure delegation | 配置校验下沉模块 |
| `a9e1ef2` | module own default-key registration | 每个模块注册自己的默认 key |
| `7220866` | logging + panic recovery | 增加全局 logging 与 goroutine recover |
| `acaacbc` | recover daemon stats reporter and gateway serve goroutines | 健壮性补强 |
| `4836c52` | full file/env/flag parity for every key | 每个配置键都有同名 flag，角色由子命令指定 |

## 后续待决策

- 是否需要重新提供公开 Go SDK。若需要，建议新增薄封装包，不直接暴露 `internal/role/api`。
- 是否需要恢复文件断点续传。如果需要，应基于 `source/tailer` 或新 `source/filebatch` 重新设计，不直接回滚 v1.0 `client.UploadFiles`。
- 是否仍需要 backfill / SQL。如果需要，建议作为独立 service 或独立 role 重新引入，避免重新污染上报引擎。
- 是否仍需要 remote config。如果需要，需要定义 filter 版本、ack、回滚和安全边界，而不是只恢复旧 `_tango_config`。
- 是否仍需要 taskqueue。如果需要，需要明确它是平台控制面的一部分，还是 tango 上报引擎之外的独立组件。
