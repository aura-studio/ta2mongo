# tango 架构说明

## 1. 目标

`tango` 将 ThinkingData 日志 JSON 行采集并写入 MongoDB 的 `user` / `event` /
`dead_letter` 集合。它是单一二进制，按运行角色组织为两种模式，只保留**上报日志**能力：

| 角色 | 命令 | 生命周期 | 职责 |
|---|---|---|---|
| **Daemon Service** | `tango daemon` | 常驻 | 文件追尾、解析、上报 filter、identity、批量写 MongoDB |
| **HTTP Gateway Service** | `tango gateway` | 常驻 | 暴露 `/ingest` `/upload` REST 接口，把 HTTP 请求转为 SDK 上报操作 |

两种模式都直接运行（无二级动作子命令）。

## 2. 设计约定（其他会话务必遵守）

这些约定是项目的核心结构原则，新增/修改代码时必须保持：

1. **根包整合子包（root-fronts-subpackages）**：每个领域有一个根包，对外只暴露根包，
   子包是其实现细节。已建立的根包：
   - `internal/dao`（`Dao` 显式持有 `Mongo` + `Store`，`dao.Config` 聚合 mongo/store 配置）整合 `dao/store` + `dao/mongo`
   - `internal/parser`（`Parser` 内嵌 `*talog.Parser`，持有 filter）整合 `parser/talog` + `parser/filter`
   - `internal/process`（`process.go`）整合 `process/single` + `process/batch` + `process/pipeline`
   - `internal/role` 是运行模式集合：`daemon` / `gateway` / `cli` / `api`
   - `internal/source` 是数据来源集合：目前 `source/tailer`（未来 sql、console）
2. **每个模块自管配置**：配置结构体下沉到各自模块并由领域根包聚合（`dao.Config` 聚合 `mongo.Config` + `store.Config`，`parser.Config` 聚合 filter 配置，另有 `tailer.Config`、`pipeline.Config`、`logging.Config`），顶层
   `config.Config` 用**指针字段**引用领域根包配置。叶子模块**不得 import 顶层 `config` 包**
   （否则成环）。依赖方向：`config` → 各模块；各模块 ↛ `config`。
3. **`process` 是三种处理方式的唯一对外入口**：`single`（单条）/`batch`（同步批量）/
   `pipeline`（异步流水线）不被外部直接 import；engine 与 client SDK 只用 `process` 的
   `NewIngester*` / `RunPipeline` / `Counters`/`Snapshot`/`WriteOptions`。
4. **日志是全局底层**：统一用 `internal/logging` 的包级函数（`logging.WithError`、`logging.Info`…），
   不要把 `*logrus.Logger` 当对象到处透传。`logging.Init(level)` 在启动时配置一次。
5. **MaxElapsedTime（bulk-write 重试预算）属于 store**，不属于 mongo 连接配置；
   配置文件键为 `runtime.store.maxElapsedTime`。
6. **配置与逻辑层级对齐**：网关相关配置（`GatewayRuntimeConfig` 及子类型）归属于 `internal/role/gateway/`，
   不放在顶层 `config/` 中。`config/gateway.go` 文件不应存在。
7. **cmd 层独立调用**：`cmd/` 下各入口文件不引用共享胶水包（如 `cmdshared`）；
   每个 cmd 入口内联自己的配置解析、client 构建、服务启动逻辑。
8. **cmdShared 做内联**：`internal/cmdshared/` 的逻辑已内联到 `cmd/daemon/` 和 `cmd/gateway/`，
   不再保留为独立模块。

## 3. 目录结构

```text
.
├── main.go
├── cmd/
│   ├── daemon/  # tango daemon（内联配置解析 + 服务启动）
│   └── gateway/ # tango gateway（内联配置解析 + client 构建 + HTTP 启动）
├── config/      # 统一 RoleConfig 文件 schema + 角色加载器；顶层 Config（指针引用各模块 Config）
├── doc/ examples/
└── internal/
    ├── logging/    # 全局 logger + logging.Config
    ├── parser/     # parser.go 整合 talog + filter（日志解析层）
    │   ├── config.go # parser.Config：聚合 parser 子模块配置
    │   ├── talog/    # TA JSON 行解析 -> Record
    │   └── filter/   # expr-lang include/exclude 上报过滤器 + filter.Config
    ├── source/     # 数据来源集合
    │   └── tailer/  # 文件追尾来源 + tailer.Config / TailMode 常量
    ├── dao/        # dao.go 整合 store + mongo
    │   ├── config.go # dao.Config：聚合 mongo.Config + store.Config
    │   ├── store/    # MongoDB 持久化 + store.Config
    │   └── mongo/    # 连接装配 + mongo.Config + MongoDBFromURI
    ├── process/    # process.go 统一管理三种处理方式（唯一对外入口）
    │   ├── single/  # 单条 parse→filter→identity→写模型
    │   ├── batch/   # 同步单条/批量 Ingester
    │   └── pipeline/# 异步 N-worker 流水线 + pipeline.Config + dynamicbatch
    └── role/       # 运行角色
        ├── daemon/  # daemon 常驻服务（report.Service）
        ├── gateway/ # HTTP gateway 运行时 + GatewayRuntimeConfig + client SDK
        ├── cli/     # 命令行模式（占位，空）
        └── api/     # API 模式（占位，空）
```

依赖方向：

```text
cmd     -> config + role + gateway + logging
role    -> process + parser + dao + source + config + logging
process -> single/batch/pipeline + dao + parser + config
parser  -> talog + filter
dao     -> store + mongo
config  -> 各领域根包/模块的 Config 类型（dao/parser/tailer/pipeline/logging）+ gateway（GatewayRuntimeConfig）
gateway -> dao + logging + parser + pipeline + tailer
各叶子模块 ↛ config
```

### 3.1 文件功能清单

#### 命令层 `cmd/`（薄封装：参数 + 配置加载 + 启动）

| 文件 | 职责 |
|---|---|
| `main.go` | 根 cobra 命令，挂载 daemon / gateway |
| `cmd/daemon/daemon.go` | `tango daemon`；内联 configFlag、resolveConfigPath、runDaemonService、runReport、maskURI |
| `cmd/gateway/gateway.go` | `tango gateway`；内联 configFlag、resolveConfigPath、gatewayConfig、buildClient、connectClient |

#### 配置层 `config/`（顶层用指针引用各模块 Config）

| 文件 | 职责 |
|---|---|
| `config/config.go` | 顶层 `Config`（指针字段）+ `ModeDaemon` + `Validate` |
| `config/role.go` | 统一 `RoleConfig` schema + `LoadDaemon`/`LoadGateway` + 投影 + `setRoleDefaults` + `parserConfigFromFilter` |
| `config/defaults.go` | `applyDefaults`：分配 nil 指针段并委托子模块默认值 |
| `config/loader.go` | viper 装配 helper |

#### 全局基础 `internal/logging`

| 文件 | 职责 |
|---|---|
| `logging/log.go` | 进程级 logger：`Init`、`L`、`WithError`/`WithField`/`WithFields`、`Info`/`Warn`/...、`Fields` 别名 |
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

#### 运行模式 `internal/role`

| 文件 | 职责 |
|---|---|
| `role/daemon/report.go` | `daemon.Service`：tailer → `process.RunPipeline` → MongoDB；周期/最终统计日志 |
| `role/gateway/config.go` | `GatewayRuntimeConfig`（gateway 运行时投影）+ `StringUploadConfig`/`FileUploadConfig`/`GatewayServerConfig` + `ApplyDefaults` |
| `role/gateway/server.go` | gateway HTTP `Server`：`/healthz` `/ingest` `/upload`（转 SDK 调用）；`NewServer` 构造函数 |
| `role/gateway/client.go` | Go SDK client：连接池管理、`New`/`Ingest`/`IngestBatch`/`EnsureIndexes`/`Ping`/`Stats` |
| `role/gateway/upload.go` | 文件上传（断点续传）+ `DefaultFileUploadCheckpointCollection` |
| `role/cli/cli.go` | 命令行模式占位（空包） |
| `role/api/api.go` | API 模式占位（空包） |

## 4. Daemon Service（daemon 模式）

```text
Tailer -> Dispatcher(按用户亲和性路由) -> Worker[i](Parse -> Filter -> Identity -> Batch) -> MongoDB BulkWrite
```

`role/daemon` 用 `process.RunPipeline(ctx, cfg, dao, parser, lineCh, stats, opts)` 驱动流水线；
命令层 `cmd/daemon` 内联配置解析与 daemon 服务启动逻辑。

## 5. HTTP Gateway Service（gateway 模式）

```text
GET  /healthz
POST /ingest
POST /upload
```

`role/gateway` 使用自有 `GatewayRuntimeConfig` 和 Go SDK；`cmd/gateway` 内联配置解析与 client 构建逻辑。

## 6. 上报 filter

上报 filter 作用于 daemon 服务与 string/file upload，维度为
`#type` / `#event_name` / 属性，用 include / exclude（expr-lang）表达。
`config.Config.BuildParser()` 委托 `parser.Config.Build()`（→ `filter.Config.Build()` / `filter.New`）。
