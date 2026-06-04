# tango 架构说明

## 1. 目标

`tango` 将 ThinkingData 日志 JSON 行采集并写入 MongoDB 的 `user` / `event` /
`dead_letter` 集合。它是单一二进制，按运行角色组织，只保留**上报日志**能力。
所有上报角色共享同一个引擎（`internal/role/api`），区别只在数据**来源**与**入口形态**：

| 角色 | 命令 | 来源 | 职责 |
|---|---|---|---|
| **Daemon** | `tango daemon` | tailer（文件） | 常驻：文件追尾、解析、filter、identity、流水线批量写 MongoDB |
| **Gateway** | `tango gateway` | httpbody（HTTP 请求体） | 常驻 HTTP：单个 `/upload`，按 mode 选 single/batch/pipeline |
| **CLI** | `tango cli` | stdin（控制台） | 一次性：从 stdin 读日志数组，按 `--mode` 上报 |
| **API** | （无 cmd，库） | httpbody（调用方传入） | 作为 Go 库被业务代码 import：`api.New(...).Upload(mode, lines)` |

gateway / cli / api 三者都提供 single/batch/pipeline 三种上传方式（都内嵌 api 引擎）；
daemon 是长驻的 pipeline 流水线。api 只作为库使用，没有对应的 `cmd/`。

## 2. 设计约定（其他会话务必遵守）

这些约定是项目的核心结构原则，新增/修改代码时必须保持：

1. **根包整合子包（root-fronts-subpackages）**：每个领域有一个根包，对外只暴露根包，
   子包是其实现细节。已建立的根包：
   - `internal/dao`（`Dao` 显式持有 `Mongo` + `Store`，`dao.Config` 聚合 mongo/store 配置）整合 `dao/store` + `dao/mongo`
   - `internal/parser`（`Parser` 内嵌 `*talog.Parser`，持有 filter）整合 `parser/talog` + `parser/filter`
   - `internal/process`（`process.go`）整合 `process/core`（共享 Processor/stats）+ `process/single` + `process/batch` + `process/pipeline`
   - `internal/role` 是运行模式集合：`daemon` / `gateway` / `cli` / `api`
   - `internal/source` 是数据来源集合：`source.Source` 契约（`Run(ctx) <-chan string`）+ `source/httpbody`（HTTP 请求体，gateway/api 用）/ `source/tailer`（文件追尾，daemon 用）/ `source/stdin`（控制台，cli 用）/ `source/taapi`（占位）
   - 6 个根包统一文件形态：`<包名>.go`（主类型/逻辑/包文档）+ `config.go`（该领域的 `Config` 聚合）。
     即 `logging/logging.go`、`dao/dao.go`、`parser/parser.go`、`source/source.go`、`process/process.go`、`role/role.go`，各配 `config.go`。
2. **配置键路径 = 包路径，config 只做覆盖**：配置结构体下沉到各自模块并由领域根包聚合
   （`dao.Config` 聚合 `mongo`/`store`，`parser.Config` 聚合 `filter`，`process.Config` 聚合 `pipeline`，
   `source.Config` 聚合 `tailer`，`role.Config` 聚合 `gateway`）。统一 schema `config.Config` 用
   **指针字段**引用这些根包配置，使**每个文件键路径都等于消费它的包路径**（`internal/` 下）：
   `logging.level`、`dao.mongo.uri`、`dao.store.maxElapsedTime`、`parser.filter.*`、
   `source.tailer.*`、`process.pipeline.*`、`role.gateway.*`。最外层 `config` 包**不定义任何具体字段**，
   只负责加载/覆盖机制（`Load` = 文件 < `TANGO_*` env < flag，外加 `setDefaults`/`applyDefaults`/`RegisterFlags`）。
   **三个途径一致**：`RegisterFlags` 为每个键注册同名 `--<键>` flag，故 文件/env/flag 可互换；
   角色由子命令指定（不在配置里），`--config`（文件路径）与 `--mode`（cli 运行参数）是仅有的非配置键 flag。
   叶子模块**不得 import 顶层 `config` 包**。依赖方向：`config` → 各模块；各模块 ↛ `config`。
3. **`process` 是三种上传方式的唯一对外入口**：`single`（逐行即时写）/`batch`（同步批量）/
   `pipeline`（异步流水线）不被外部直接 import，三者实现同一 `process.Uploader` 接口
   （`Run(ctx, source.Source) error` + `Stop()`，可启动可停止，都消费一个日志源）；
   role 与 client SDK 只用 `process` 的 `New(mode, …)` / `Mode` / `ParseMode` /
   `Source` / `Counters` / `Snapshot` / `WriteOptions`。
4. **日志是全局底层**：统一用 `internal/logging` 的包级函数（`logging.WithError`、`logging.Info`…），
   不要把 `*logrus.Logger` 当对象到处透传。`logging.Init(level)` 在启动时配置一次。
5. **MaxElapsedTime（bulk-write 重试预算）属于 store**，不属于 mongo 连接配置；
   配置文件键为 `dao.store.maxElapsedTime`。
6. **配置结构 = internal 包层级；角色不重复 host 模块配置**：模块配置都在各自包路径的顶层
   （`logging.*`/`dao.*`/`parser.filter.*`/`source.tailer.*`/`process.*`），由需要的角色**共享复用**；
   `role.<name>.*` 只放该角色**专属**的字段（如 `role.gateway.addr`/`role.gateway.defaultMode`）。
   例如 gateway 的上传处理直接用顶层 `process.*` 与 `parser.filter.*`，不在 `role.gateway` 下再开
   `process`/`filter`。`role.daemon` 暂为空（daemon 完全由顶层模块驱动）。
7. **cmd 层独立调用**：`cmd/` 下各入口文件不引用共享胶水包（如 `cmdshared`）；
   每个 cmd 入口内联自己的配置解析、client 构建、服务启动逻辑。
8. **cmdShared 做内联**：`internal/cmdshared/` 的逻辑已内联到 `cmd/daemon/` 和 `cmd/gateway/`，
   不再保留为独立模块。

## 3. 目录结构

```text
.
├── main.go
├── cmd/         # 控制台入口（api 是库，无 cmd）
│   ├── daemon/  # tango daemon（内联配置解析 + 服务启动）
│   ├── gateway/ # tango gateway（内联配置解析 + HTTP 启动）
│   └── cli/     # tango cli（内联配置解析 + 读 stdin 上报）
├── config/      # 单一包路径映射 schema + Load/override；只聚合各模块 Config，不定义字段
├── doc/ examples/
└── internal/
    ├── logging/    # 全局 logger + logging.Config
    ├── parser/     # parser.go 整合 talog + filter（日志解析层）
    │   ├── config.go # parser.Config：聚合 parser 子模块配置
    │   ├── talog/    # TA JSON 行解析 -> Record
    │   └── filter/   # expr-lang include/exclude 上报过滤器 + filter.Config
    ├── source/     # 数据来源集合（source.Source 契约 + source.Config 聚合 tailer）
    │   ├── httpbody/ # HTTP 请求体来源（gateway/api 用）
    │   ├── tailer/  # 文件追尾来源 + tailer.Config / TailMode 常量（daemon 用）
    │   ├── stdin/   # 控制台 stdin 来源（cli 用）
    │   └── taapi/   # 占位（未来 TA API 来源）
    ├── dao/        # dao.go 整合 store + mongo
    │   ├── config.go # dao.Config：聚合 mongo.Config + store.Config
    │   ├── store/    # MongoDB 持久化 + store.Config
    │   └── mongo/    # 连接装配 + mongo.Config + MongoDBFromURI
    ├── process/    # process.go 统一管理三种上传方式（唯一对外入口；Uploader 接口 + New）
    │   ├── core/    # 共享 Processor（parse→filter→identity→写模型）+ stats（Counters/StatsCollector）
    │   ├── single/  # 逐行即时写 Uploader
    │   ├── batch/   # 同步批量 Uploader（累积 + bulk flush）
    │   └── pipeline/# 异步 N-worker 流水线 Uploader + pipeline.Config + dynamicbatch
    └── role/       # 运行角色（role.Config 聚合 daemon/gateway）
        ├── api/     # 可复用引擎库：api.Engine（New/Upload/Run/EnsureIndexes/Close）
        ├── daemon/  # daemon 常驻服务（report.Service，pipeline + tailer）
        ├── gateway/ # HTTP gateway：内嵌 api.Engine + /upload；gateway.Config（role.gateway.*）
        └── cli/     # 命令行：内嵌 api.Engine + stdin 源
```

依赖方向：

```text
cmd      -> config + role(api/cli/daemon/gateway) + logging
role/api -> process + parser + dao + source/httpbody + logging   (引擎库，被 gateway/cli 内嵌)
gateway  -> api + dao + process + logging          (HTTP 面)
cli      -> api + dao + process + source/stdin      (stdin 面)
daemon   -> process + parser + dao + source/tailer + logging
process  -> single/batch/pipeline + source + dao + parser
parser   -> talog + filter
dao      -> store + mongo
config   -> 各模块 Config 类型（logging/dao/parser/source/process/role(→gateway)）
各叶子模块 ↛ config
```

### 3.1 文件功能清单

#### 命令层 `cmd/`（薄封装：参数 + 配置加载 + 启动）

| 文件 | 职责 |
|---|---|
| `main.go` | 根 cobra 命令，挂载 daemon / gateway / cli（api 无 cmd） |
| `cmd/daemon/daemon.go` | `tango daemon`；内联 configFlag、resolveConfigPath、runDaemonService、runReport、maskURI |
| `cmd/gateway/gateway.go` | `tango gateway`；内联 configFlag、resolveConfigPath + `config.Load` → `gateway.New` → HTTP |
| `cmd/cli/cli.go` | `tango cli`；`--mode` + `config.Load` → 读 stdin → `cli.Run`，打印统计 JSON |

#### 配置层 `config/`（单一包路径映射 schema；只聚合，不定义字段）

| 文件 | 职责 |
|---|---|
| `config/config.go` | 统一 `Config`（指针引用各模块 Config，键=包路径）+ `Validate` |
| `config/load.go` | `Load`（文件<env<flag）+ `setDefaults`（env 绑定）+ `RegisterFlags`（每键注册同名 flag） |
| `config/defaults.go` | `applyDefaults`：分配 nil 指针段并委托子模块默认值 |
| `config/loader.go` | viper 装配 helper（env 前缀、decode hook、flag 绑定） |

#### 全局基础 `internal/logging`

| 文件 | 职责 |
|---|---|
| `logging/logging.go` | 进程级 logger：`Init`、`L`、`WithError`/`WithField`/`WithFields`、`Info`/`Warn`/...、`Fields` 别名 |
| `logging/config.go` | `logging.Config`（level/format） |

#### 解析层 `internal/parser`

| 文件 | 职责 |
|---|---|
| `parser/parser.go` | `Parser`：内嵌 `*talog.Parser` + 持有 `*filter.Holder`（`Filter()`）；`New(flt)` |
| `parser/talog/parser.go` | `Parser.ParseLine`：TA JSON → `Record` |
| `parser/talog/record.go` | `Record` + `Category`/`IsUserType`/`IsEventType` |
| `parser/filter/filter.go` | `Filter`：expr-lang 编译与 `Keep` |
| `parser/filter/holder.go` | `Holder`：原子可热替换的 filter 持有者 |
| `parser/filter/config.go` | `filter.Config`（include/exclude）+ `Build()` |

#### 来源层 `internal/source`

| 文件 | 职责 |
|---|---|
| `source/source.go` | `source.Source` 契约：`Run(ctx) <-chan string` |
| `source/config.go` | `source.Config`：聚合 `tailer.Config`（键 source.tailer.*） |
| `source/httpbody/httpbody.go` | `httpbody.Source`：把预解析的行数组（单条/批量）包成 line channel（gateway/api 用） |
| `source/stdin/stdin.go` | `stdin.Source`：从 io.Reader/os.Stdin 逐行扫描成 channel（cli 用） |
| `source/tailer/tailer.go` | `Tailer`：glob 发现文件、追尾、rescan，输出 line channel（hybrid/poll/event） |
| `source/tailer/config.go` | `tailer.Config` + `TailModeHybrid`/`Poll`/`Event` 常量 |

#### 数据访问 `internal/dao`

| 文件 | 职责 |
|---|---|
| `dao/dao.go` | `Dao`：显式持有 `Mongo *mongo.MongoResource` + `Store *store.Store`；`New(res, cfg)` 装配 store |
| `dao/config.go` | `dao.Config`：聚合 `mongo.Config` + `store.Config` |
| `dao/store/store.go` | `Store` + `store.Config`(MaxElapsedTime) + `WriteStats` + `BulkWrite(Ordered)` + 集合访问器 |
| `dao/store/identity.go` | `IdentityResolver`：`#account_id`/`#distinct_id` → `#user_id` |
| `dao/store/indexes.go` | `EnsureIndexes`：user/event/dead_letter/id_mapping 索引 |
| `dao/store/writemodel.go` | 构建写模型与 dead-letter 模型（`_ts` 防回退） |
| `dao/mongo/mongo.go` | `MongoResource` + `ConnectMongo`/`Borrow`/`DatabaseFromClient`/`Close` |
| `dao/mongo/config.go` | `mongo.Config`（URI + 连接超时）+ `MongoDBFromURI` |

#### 处理层 `internal/process`（`process.go` 唯一对外）

| 文件 | 职责 |
|---|---|
| `process/process.go` | 对外门面：`Uploader` 接口 + `Mode`/`ParseMode`/`Source` + `New(mode,…)`；`Counters`/`Snapshot`/`WriteOptions` 别名 |
| `process/core/processor.go` | `core.Processor.Process`：parse→filter→identity→写模型分类（`Kind`/`Result`）；逐行 panic recover |
| `process/core/stats.go` | `StatsCollector` 接口 + `NoopStats` |
| `process/core/counters.go` | `Counters`（并发计数器）+ `Snapshot` |
| `process/single/uploader.go` | `single.Uploader`：逐行即时写（`Run`/`Stop`） |
| `process/batch/uploader.go` | `batch.Uploader`：drain 源 → 累积写模型 → bulk flush（`Run`/`Stop`） |
| `process/pipeline/uploader.go` | `pipeline.Uploader`：包装 `RunWorkers`（`Run`/`Stop`） |
| `process/pipeline/worker.go` | `RunWorkers`/`worker`：N 并发 + 批累积 + 动态刷新 |
| `process/pipeline/config.go` | `pipeline.Config` + `MinBatchSize`/`MaxBatchSize`/`ChannelSize` |
| `process/pipeline/dynamicbatch.go` | `ComputeFlushThreshold`：按 backlog 自适应刷新阈值 |
| `process/pipeline/batch.go` | `Batch`：写模型批容器 |
| `process/pipeline/dispatch.go` | `Dispatch`：按亲和键路由到各 worker channel |
| `process/pipeline/routing.go` | `ExtractRoutingKey`/`RouteIndex`：用户亲和性 hash 路由 |

#### 运行模式 `internal/role`

| 文件 | 职责 |
|---|---|
| `role/api/api.go` | 可复用引擎库 `api.Engine`：`New(ctx,dao,proc,filter)`/`Upload(mode,lines)`/`Run(mode,src)`/`EnsureIndexes`/`Close` + `Result` |
| `role/daemon/report.go` | `daemon.Service`：tailer 源 → `process.New(ModePipeline).Run` → MongoDB；周期/最终统计日志 |
| `role/role.go` | 角色集合包文档 + 角色名常量（`API`/`CLI`/`Daemon`/`Gateway`） |
| `role/config.go` | `role.Config`：聚合 `daemon`/`gateway` 角色配置 + `ApplyDefaults`/`Validate`/`RegisterDefaults` |
| `role/daemon/config.go` | `daemon.Config`（`role.daemon.*`，暂空，schema 对称用） |
| `role/gateway/config.go` | `gateway.Config`（仅 `role.gateway.addr` + `defaultMode`）+ `ApplyDefaults`/`Validate`/`RegisterDefaults` |
| `role/gateway/server.go` | gateway `Server`：内嵌 `*api.Engine` + HTTP 面；`New(ctx,dao,process,filter,cfg)`/`Upload`/`EnsureIndexes`/`Close`/`Run`；`/healthz` + 单个 `/upload`（按 mode 选策略） |
| `role/cli/cli.go` | `cli.Run(ctx,dao,proc,filter,mode,in)`：内嵌 `api.Engine` + `stdin.Source`，一次性上报 |

## 4. Daemon Service（daemon 模式）

```text
Tailer -> Dispatcher(按用户亲和性路由) -> Worker[i](Parse -> Filter -> Identity -> Batch) -> MongoDB BulkWrite
```

`role/daemon` 用 `process.New(process.ModePipeline, …)` 构造 pipeline `Uploader`，再
`up.Run(ctx, tailerSource)` 驱动流水线（阻塞至 ctx 取消）；命令层 `cmd/daemon` 内联配置解析与 daemon 服务启动逻辑。

## 5. HTTP Gateway Service（gateway 模式）

```text
GET  /healthz
POST /upload   # body: {mode?: single|batch|pipeline, line?, lines?[]}；mode 省略走配置默认（batch）
```

`/upload` 把请求体的日志数组包成 `httpbody.Source`，按 `mode` 选上传策略（single/batch/pipeline）
运行，返回本次统计（行数/写入数/死信等）。gateway **只接 httpbody 源**。`Server` 内嵌 `api.Engine`
引擎；`cmd/gateway` 用 `config.Load` 取共享 `dao` + `process` + `parser.filter` + `role.gateway` 段，
构造 `gateway.New(ctx, dao, process, filter, cfg)`（处理/过滤配置复用顶层共享模块）。

## 5.1 API 库 / CLI（api / cli 角色）

`role/api` 是可复用引擎 `api.Engine`：`New` 连接 MongoDB，`Upload(mode, lines)` / `Run(mode, src)`
对任意 `source.Source` 跑三种策略之一。它被 gateway（httpbody 面）与 cli（stdin 面）内嵌，因此三者
提供**完全相同**的 single/batch/pipeline 上传能力。`tango cli` 从 stdin 读日志数组，`--mode` 选策略；
api 无 `cmd/`，作为库 import。

## 6. 上报 filter

上报 filter 作用于所有上报路径（daemon、gateway/cli/api upload），维度为
`#type` / `#event_name` / 属性，用 include / exclude（expr-lang）表达，
经 `parser.Config.Build()`（→ `filter.Config.Build()` / `filter.New`）编译。
