# tango 重构蒸馏（供其他会话加载）

本文蒸馏近期一轮结构性重构的**决策、重命名映射、约定与迁移注意**，方便后续会话快速建立上下文。
当前架构权威说明见 [arch.md](arch.md)，配置字段见 [config.md](config.md)。

## 1. 核心约定（必须遵守）

1. **根包整合子包**：领域用一个根包对外，子包是实现细节。
   `dao`→{store,mongo}、`parser`→{talog,filter}、`process`→{single,batch,pipeline}、
   `engine`→{daemon,gateway,cli,api}、`source`→{tailer,…}。
2. **每模块自管配置**：配置体下沉到各模块（`mongo.Config`/`store.Config`/`tailer.Config`/
   `pipeline.Config`/`filter.Config`/`log.Config`），顶层 `config.Config`/`ClientConfig`/
   `RuntimeConfig` 用**指针字段**引用。叶子模块**不 import 顶层 `config`**（防环）。
3. **`process` 是三种处理方式唯一对外入口**：`single`/`batch`/`pipeline` 不被外部直接 import；
   engine 与 client SDK 只用 `process.NewIngester*` / `RunPipeline` / `Counters|Snapshot|WriteOptions`。
4. **日志全局化**：统一 `internal/log` 包级函数；不要透传 `*logrus.Logger`；`log.Init(level)` 启动配置一次。
5. **配置归属语义**：连接相关在 `mongo.Config`；bulk-write 重试预算 `MaxElapsedTime` 在 `store.Config`。

## 2. 重命名 / 迁移映射（旧 → 新）

| 旧路径/标识 | 新路径/标识 |
|---|---|
| `internal/source`（talog+filter 整合包，pkg `source`，类型 `Source`） | `internal/parser`（pkg `parser`，类型 `Parser`） |
| `internal/core/tailer` | `internal/source/tailer`（数据来源1） |
| `internal/process/processor`（pkg `processor`） | `internal/process/single`（pkg `single`） |
| `internal/process/ingest`（pkg `ingest`） | `internal/process/batch`（pkg `batch`） |
| `internal/core/dynamicbatch` | 并入 `internal/process/pipeline/dynamicbatch.go`（pkg `pipeline`） |
| `internal/service` | `internal/engine` |
| `internal/service/report`（pkg `report`） | `internal/engine/daemon`（pkg `daemon`，类型仍叫 `Service`） |
| `internal/service/gateway` | `internal/engine/gateway` |
| —（新增） | `internal/engine/cli`、`internal/engine/api`（占位空包） |
| —（新增） | `internal/process/process.go`（pkg `process` 门面） |
| `config.MongoConfig`（含 MaxElapsedTime） | `mongo.Config`（去掉 MaxElapsedTime）+ `store.Config`（持 MaxElapsedTime） |
| `config.FilterConfig` / `PipelineConfig` / `SourceConfig` / `LoggingConfig` | `filter.Config` / `pipeline.Config` / `tailer.Config` / `log.Config` |
| `config.MongoDBFromURI` | `mongo.MongoDBFromURI` |
| `config.Config` 的 `BatchSizeMin/Max/BatchChannelSize` 方法 | `pipeline.Config` 的 `MinBatchSize/MaxBatchSize/ChannelSize` |
| `cli.NewLogger` | 移除；改用 `log.Init` |
| client SDK `WithLogger` 选项 | 移除（client 也走全局 logger） |

## 3. 配置文件 schema 变更（破坏性）

- `runtime.mongo.maxElapsedTime` → **`runtime.store.maxElapsedTime`**。旧配置文件需改键名，
  否则该值被静默忽略（回落默认 10s）。其余键不变。

## 4. 关键设计点 / 易错点

- 顶层 `config.Config` 各段是**指针**；`applyDefaults`/`ClientConfig.applyDefaults` 会分配 nil 段
  并填默认值。**手工构造** `config.Config`（如 client SDK、集成测试）时必须自行分配需要用到的段
  （尤其 `Store`，否则 `store.New` 拿到 nil 配置在 bulkWrite 时取 `MaxElapsedTime` 会 panic）。
- `parser.Parser` 内嵌 `*talog.Parser`，字段名即 `Parser`：访问解析器是 `p.Parser`，过滤器是 `p.Filter()`。
- `process.RunPipeline(ctx, cfg, *dao.Dao, *parser.Parser, lineCh, *Counters, WriteOptions)`：
  内部解包 `dao.Store` / `parser.Parser` / `parser.Filter()` 交给 `pipeline.RunWorkers`。
- `mongo` 包导入名与 mongo driver 冲突时用别名 `daomongo`（见 client、各集成测试）。
- 依赖方向：`config` → 各模块 Config 类型；各模块 ↛ `config`（编译保证无环）。

## 5. 验证

`go build ./...`、`go vet ./...` 全绿；单元 + 本地 MongoDB 集成测试全过。
