# tango 架构说明

## 1. 目标

`tango` 将 ThinkingData 日志 JSON 行采集并写入 MongoDB 的 `user` / `event` /
`dead_letter` 集合。它是单一二进制，按运行角色组织为两种模式，只保留**上报日志**能力：

| 角色 | 命令 | 生命周期 | 职责 |
|---|---|---|---|
| **Standalone Service** | `tango standalone` | 常驻 | 文件追尾、解析、上报 filter、identity、批量写 MongoDB |
| **HTTP Gateway Service** | `tango gateway` | 常驻 | 暴露 `/ingest` `/upload` REST 接口，把 HTTP 请求转为 SDK 上报操作 |

两种模式都直接运行（无二级动作子命令）。

## 2. 目录结构

```text
.
├── main.go
├── cmd/
│   ├── standalone/  # tango standalone
│   ├── gateway/     # tango gateway
│   └── shared/      # cmd glue: config resolution, client building, service runner
├── config/          # RoleConfig (unified file schema) + role loaders; ClientConfig (runtime projection); shared runtime Config
├── client/          # 对外 Go SDK
├── doc/ examples/
└── internal/
    ├── core/        # cli filter store talog tailer dynamicbatch runtime
    ├── process/     # ingest ingestion pipeline
    └── service/
        ├── report/  # standalone service runtime (report.Service)
        └── gateway/ # HTTP gateway runtime
```

依赖方向保持：

```text
cmd -> config + service/client SDK
service -> process + core
process -> core
core -> external libs only
```

### 2.1 文件功能清单

#### 命令层 `cmd/`（薄封装：参数 + 配置加载 + 启动）

| 文件 | 职责 |
|---|---|
| `main.go` | 根 cobra 命令，挂载 standalone / gateway 两个角色命令 |
| `cmd/standalone/standalone.go` | `tango standalone` 命令；解析 `standalone.yaml`，委托 `shared.RunStandaloneService` |
| `cmd/gateway/gateway.go` | `tango gateway` 命令；解析 `gateway.yaml`，启动 HTTP gateway |
| `cmd/shared/client.go` | 命令层共享：`ConfigFlag`、`GatewayConfig` 加载器、`BuildClient`/`ConnectClient`、`ClientLoader` |
| `cmd/shared/service.go` | 服务运行器：`RunStandaloneService`/`runReport`、`MaskURI` |

#### 配置层 `config/`

| 文件 | 职责 |
|---|---|
| `config/config.go` | 运行时 `Config` 及各嵌套结构、常量、`Validate`、`MongoDBFromURI` |
| `config/role.go` | 统一 `RoleConfig` 文件 schema + 角色加载器 `LoadStandalone/LoadGateway` + 投影 + `setRoleDefaults` |
| `config/client.go` | `ClientConfig`（gateway/SDK 的运行时投影） |
| `config/defaults.go` | `applyDefaults` 默认值填充 |
| `config/pipeline.go` | 批大小 helper：`BatchSizeMin`/`BatchSizeMax`/`BatchChannelSize` |
| `config/filter.go` | `BuildFilter`（编译上报过滤器） |
| `config/loader.go` | viper 装配 helper：`newViper`、`readConfigFile`、`bindFlagsTo`、`durationDecodeHook`、`weaklyTyped` |

#### 对外 SDK `client/`

| 文件 | 职责 |
|---|---|
| `client/client.go` | SDK 入口：`Client`、`Options`/`Option`（`WithURI`…）、`New`/`Close`/`EnsureIndexes`、`Ingest`/`IngestBatch`、`Ping` |
| `client/upload.go` | `UploadFiles` + `UploadRequest`/`UploadResult`（文件上传，断点续传） |

#### 基础设施 `internal/core/`

| 文件 | 职责 |
|---|---|
| `core/cli/cli.go` | `ResolveConfigPath`（二进制同级默认配置）、`NewLogger` |
| `core/dynamicbatch/flush_threshold.go` | `ComputeFlushThreshold`：按 backlog 自适应批刷新阈值 |
| `core/filter/filter.go` | `Filter`：expr-lang include/exclude 编译与求值（`Keep`） |
| `core/filter/holder.go` | `Holder`：原子可热替换的 `Filter` 持有者 |
| `core/runtime/mongo.go` | `MongoResource`（Client/DB/Owns）+ `ConnectMongo`/`Borrow`/`DatabaseFromClient`/`Close` |
| `core/runtime/store.go` | `NewStore`：在 DB 上装配 `Store` 的薄 helper |
| `core/store/store.go` | `Store`：MongoDB 持久化入口、`WriteStats`、`BulkWrite`/`BulkWriteOrdered`、集合访问器 |
| `core/store/identity.go` | `IdentityResolver`：`#account_id`/`#distinct_id` → `#user_id` 解析与缓存、`id_mapping` 原子写 |
| `core/store/indexes.go` | `Store.EnsureIndexes`：创建 user/event/dead_letter/id_mapping 索引 |
| `core/store/writemodel.go` | 构建 `user_*`/`track_*` 写模型与 dead-letter 模型（`_ts` 防回退语义） |
| `core/talog/parser.go` | `Parser.ParseLine`：解析 TA JSON 行为 `Record` |
| `core/talog/record.go` | `Record` 及 `Category`/`IsUserType`/`IsEventType`（user vs event 分类） |
| `core/tailer/tailer.go` | `Tailer`：按 glob 发现文件、追尾、定期 rescan，输出 line channel（hybrid/poll/event） |

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
| `service/report/report.go` | `report.Service`：追尾 → pipeline → MongoDB；周期/最终统计日志 |
| `service/gateway/server.go` | gateway HTTP `Server`：`/healthz` `/ingest` `/upload`（转 SDK 上报调用） |

> `examples/` 下是独立的演示程序，不属于二进制：`examples/client/ingest`、
> `examples/client/ingestbatch` 演示 SDK 单条/批量上报，`examples/logpattern` 演示
> tailer 的 glob 匹配；`examples/config/` 是各角色的样例配置（见 [config.md](config.md)）。

## 3. Standalone Service

命令：

```bash
tango standalone
```

数据流：

```text
Tailer -> Dispatcher(按用户亲和性路由) -> Worker[i](Parse -> Filter -> Identity -> Batch) -> MongoDB BulkWrite
```

职责：

- 读取 `report.source.logPattern`。
- 追尾文件并输出 line channel。
- 解析 TA JSON。
- 应用上报 filter。
- 根据 `#account_id` / `#distinct_id` 做用户亲和性路由。
- 批量写入 MongoDB。

## 4. HTTP Gateway Service

命令：

```bash
tango gateway
```

gateway 是常驻服务，使用 ClientConfig 和 Go SDK，只暴露上报日志接口：

```text
GET  /healthz
POST /ingest
POST /upload
```

HTTP 运行时位于 `internal/service/gateway`；命令层 `cmd/gateway` 只做参数与配置加载。

## 5. 上报 filter

上报 filter 作用于 standalone 服务与 string/file upload，维度为
`#type` / `#event_name` / 属性，用 include / exclude（expr-lang）表达。
`config.Config.BuildFilter()` 复用 `filter.New` 编译。
