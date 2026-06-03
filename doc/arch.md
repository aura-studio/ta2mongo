# tango 架构说明

## 1. 目标

`tango` 将 ThinkingData 日志 JSON 行采集并写入 MongoDB 的 `user` / `event` /
`dead_letter` 集合。它是单一二进制，按运行角色组织为两种模式，只保留**上报日志**能力：

| 角色 | 命令 | 生命周期 | 职责 |
|---|---|---|---|
| **Standalone Service** | `tango standalone` | 常驻 | 文件追尾、解析、上报 filter、identity、批量写 MongoDB |
| **HTTP Gateway Service** | `tango gateway` | 常驻 | 暴露 `/ingest` `/upload` REST 接口，把 HTTP 请求转为 SDK 上报操作 |

两种模式都直接运行（无二级动作子命令）。

## 2. 设计约定（其他会话务必遵守）

这些约定是项目的核心结构原则，新增/修改代码时必须保持：

1. **根包整合子包（root-fronts-subpackages）**：每个领域有一个根包，对外只暴露根包，
   子包是其实现细节。已建立的根包：
   - `internal/dao`（`Dao` 显式持有 `Mongo` + `Store`，`dao.Config` 聚合 mongo/store 配置）整合 `dao/store` + `dao/mongo`
   - `internal/parser`（`Parser` 内嵌 `*talog.Parser`，持有 filter）整合 `parser/talog` + `parser/filter`
   - `internal/process`（`process.go`）整合 `process/single` + `process/batch` + `process/pipeline`
   - `internal/engine` 是运行模式集合：`daemon` / `gateway` / `cli` / `api`
   - `internal/source` 是数据来源集合：目前 `source/tailer`（未来 sql、console）
2. **每个模块自管配置**：配置结构体下沉到各自模块并由领域根包聚合（`dao.Config` 聚合 `mongo.Config` + `store.Config`，`parser.Config` 聚合 filter 配置，另有 `tailer.Config`、`pipeline.Config`、`log.Config`），顶层
   `config.Config` 用**指针字段**引用领域根包配置。叶子模块**不得 import 顶层 `config` 包**
   （否则成环）。依赖方向：`config` → 各模块；各模块 ↛ `config`。
3. **`process` 是三种处理方式的唯一对外入口**：`single`（单条）/`batch`（同步批量）/
   `pipeline`（异步流水线）不被外部直接 import；engine 与 client SDK 只用 `process` 的
   `NewIngester*` / `RunPipeline` / `Counters`/`Snapshot`/`WriteOptions`。
4. **日志是全局底层**：统一用 `internal/log` 的包级函数（`log.WithError`、`log.Info`…），
   不要把 `*logrus.Logger` 当对象到处透传。`log.Init(level)` 在启动时配置一次。
5. **MaxElapsedTime（bulk-write 重试预算）属于 store**，不属于 mongo 连接配置；
   配置文件键为 `runtime.store.maxElapsedTime`。

## 3. 目录结构

```text
.
├── main.go
├── cmd/
│   ├── standalone/  # tango standalone
│   ├── gateway/     # tango gateway
│   └── shared/      # cmd glue: 配置解析、client 构建、service runner
├── config/          # 统一 RoleConfig 文件 schema + 角色加载器；顶层 Config / ClientConfig（指针引用各模块 Config）
├── client/          # 对外 Go SDK
├── doc/ examples/
└── internal/
    ├── log/         # 全局 logger + log.Config
    ├── core/        # cli（仅配置路径解析 helper）
    ├── parser/      # parser.go 整合 talog + filter（日志解析层，原 source 包改名而来）
    │   ├── config.go # parser.Config：聚合 parser 子模块配置
    │   ├── talog/    # TA JSON 行解析 -> Record
    │   └── filter/   # expr-lang include/exclude 上报过滤器 + filter.Config
    ├── source/      # 数据来源集合
    │   └── tailer/  # 文件追尾来源（来源1）+ tailer.Config / TailMode 常量
    ├── dao/         # dao.go 整合 store + mongo
    │   ├── config.go # dao.Config：聚合 mongo.Config + store.Config
    │   ├── store/    # MongoDB 持久化 + store.Config
    │   └── mongo/    # 连接装配 + mongo.Config + MongoDBFromURI
    ├── process/     # process.go 统一管理三种处理方式（唯一对外入口）
    │   ├── single/  # 单条 parse→filter→identity→写模型（原 processor 包）
    │   ├── batch/   # 同步单条/批量 Ingester（原 ingest 包）
    │   └── pipeline/# 异步 N-worker 流水线 + pipeline.Config + dynamicbatch
    └── engine/      # 运行模式
        ├── daemon/  # standalone 常驻服务（report.Service，原 service/report）
        ├── gateway/ # HTTP gateway 运行时
        ├── cli/     # 命令行模式（占位，空）
        └── api/     # API 模式（占位，空）
```

依赖方向：

```text
cmd     -> config + engine + client SDK
engine  -> process + parser + dao + source + config + log
process -> single/batch/pipeline + dao + parser + config
parser  -> talog + filter
dao     -> store + mongo
config  -> 各领域根包/模块的 Config 类型（dao/parser/tailer/pipeline/log）
各叶子模块 ↛ config
```

### 3.1 文件功能清单

#### 命令层 `cmd/`（薄封装：参数 + 配置加载 + 启动）

| 文件 | 职责 |
|---|---|
| `main.go` | 根 cobra 命令，挂载 standalone / gateway |
| `cmd/standalone/standalone.go` | `tango standalone`；解析 `standalone.yaml`，委托 `shared.RunStandaloneService` |
| `cmd/gateway/gateway.go` | `tango gateway`；解析 `gateway.yaml`，启动 HTTP gateway |
| `cmd/shared/client.go` | `ConfigFlag`、`GatewayConfig` 加载器、`BuildClient`/`ConnectClient`、`ClientLoader` |
| `cmd/shared/service.go` | `RunStandaloneService`/`runReport`、`MaskURI`；`log.Init` |

#### 配置层 `config/`（顶层用指针引用各模块 Config）

| 文件 | 职责 |
|---|---|
| `config/config.go` | 顶层 `Config`（指针字段）+ `ModeReport` + `Validate` |
| `config/role.go` | 统一 `RoleConfig` schema + `LoadStandalone/LoadGateway` + 投影 + `setRoleDefaults` |
| `config/client.go` | `ClientConfig`（gateway/SDK 投影）+ `applyDefaults` |
| `config/defaults.go` | `applyDefaults`：分配 nil 指针段并填默认值 |
| `config/filter.go` | `BuildParser` → 委托 `parser.Config.Build()` |
| `config/loader.go` | viper 装配 helper |

#### 全局基础 `internal/log` `internal/core`

| 文件 | 职责 |
|---|---|
| `log/log.go` | 进程级 logger：`Init`、`L`、`WithError/WithField/WithFields`、`Info/Warn/...`、`Fields` 别名 |
| `log/config.go` | `log.Config`（level/format） |
| `core/cli/cli.go` | `ResolveConfigPath`（二进制同级默认配置） |

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
| `source/tailer/tailer.go` | `Tailer`：glob 发现文件、追尾、rescan，输出 line channel（hybrid/poll/event） |
| `source/tailer/config.go` | `tailer.Config` + `TailModeHybrid/Poll/Event` 常量 |

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
| `process/process.go` | 对外门面：`Ingester`/`Counters`/`Snapshot`/`WriteOptions` 别名；`NewIngester`/`NewIngesterFromClient`/`RunPipeline` |
| `process/single/processor.go` | `Processor.Process`：parse→filter→identity→写模型分类（`Kind`/`Result`） |
| `process/single/stats.go` | `StatsCollector` 接口 + `NoopStats` |
| `process/single/counters.go` | `Counters`（并发计数器）+ `Snapshot` |
| `process/batch/ingest.go` | `Ingester`：同步单条/批量 ingest（`New`/`NewFromClient`/`Close`） |
| `process/pipeline/worker.go` | `RunWorkers`/`worker`：N 并发 + 批累积 + 动态刷新 |
| `process/pipeline/config.go` | `pipeline.Config` + `MinBatchSize`/`MaxBatchSize`/`ChannelSize` |
| `process/pipeline/dynamicbatch.go` | `ComputeFlushThreshold`：按 backlog 自适应刷新阈值 |
| `process/pipeline/batch.go` | `Batch`：写模型批容器 |
| `process/pipeline/dispatch.go` | `Dispatch`：按亲和键路由到各 worker channel |
| `process/pipeline/routing.go` | `ExtractRoutingKey`/`RouteIndex`：用户亲和性 hash 路由 |

#### 运行模式 `internal/engine`

| 文件 | 职责 |
|---|---|
| `engine/daemon/report.go` | `daemon.Service`：tailer → `process.RunPipeline` → MongoDB；周期/最终统计日志 |
| `engine/gateway/server.go` | gateway HTTP `Server`：`/healthz` `/ingest` `/upload`（转 SDK 调用） |
| `engine/cli/cli.go` | 命令行模式占位（空包） |
| `engine/api/api.go` | API 模式占位（空包） |

#### 对外 SDK `client/`

| 文件 | 职责 |
|---|---|
| `client/client.go` | SDK 入口：`Client`、`Options`/`Option`、`New`/`Close`/`EnsureIndexes`、`Ingest`/`IngestBatch`、`Ping`（经 `process.NewIngesterFromClient`） |
| `client/upload.go` | `UploadFiles` + `UploadRequest`/`UploadResult`（文件上传，断点续传） |

> `examples/` 下是独立演示程序，不属于二进制。

## 4. Standalone Service（daemon 模式）

```text
Tailer -> Dispatcher(按用户亲和性路由) -> Worker[i](Parse -> Filter -> Identity -> Batch) -> MongoDB BulkWrite
```

`engine/daemon` 用 `process.RunPipeline(ctx, cfg, dao, parser, lineCh, stats, opts)` 驱动流水线；
命令层 `cmd/standalone` 只做参数与配置加载。

## 5. HTTP Gateway Service（gateway 模式）

```text
GET  /healthz
POST /ingest
POST /upload
```

`engine/gateway` 使用 ClientConfig 和 Go SDK；`cmd/gateway` 只做参数与配置加载。

## 6. 上报 filter

上报 filter 作用于 standalone 服务与 string/file upload，维度为
`#type` / `#event_name` / 属性，用 include / exclude（expr-lang）表达。
`config.Config.BuildParser()` 委托 `parser.Config.Build()`（→ `filter.Config.Build()` / `filter.New`）。
