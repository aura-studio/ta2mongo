# tango 架构说明

## 1. 目标

`tango` 将 ThinkingData 日志 JSON 行采集并写入 MongoDB 的 `user` / `event` /
`dead_letter` 集合。它是单一二进制，按运行角色组织，只保留**上报日志**能力。
所有上报角色共享同一个引擎（`internal/role/api`），区别只在数据**来源**与**入口形态**：

| 角色 | `role.mode` | 来源 | 职责 |
|---|---|---|---|
| **Daemon** | `daemon` | tailer（文件） | 常驻：文件追尾、解析、filter、identity、流水线批量写 MongoDB |
| **Gateway** | `gateway` | httpbody（HTTP 请求体） | 常驻 HTTP：`/upload`（按 `process.mode` 选 single/batch/pipeline）+ 独立的 `/ejson`（Mongo Data API）+ `/sql`（SQL Data API） |
| **CLI** | `cli` | stdin（控制台） | 一次性：`role.cli.function=upload` 对齐 `/upload`；`=ejson` 对齐 `/ejson`；`=sql` 对齐 `/sql` |
| **API** | （库，不可派发） | httpbody（调用方传入） | 作为 Go 库被业务代码 import：`api.New(...).Upload(lines)` / `.EJSON(req)` / `.SQL(query)` |

除上报外，三端还共享两个 Data API：**Mongo Data API**（`internal/dao/ejson`）与 **SQL Data API**
（`internal/dao/sql`，代码自 `aura-studio/mongosql` 拷贝），均由 `dao` 根包经 `dao.go` 中转：
gateway `POST /ejson`+`/sql`、cli `function=ejson`/`sql`、库 `engine.EJSON(req)`/`engine.SQL(query)`，
功能核心完全一致、只是入口形态不同
（与上报的 `api.Engine` 复用模式一致）。见 §5.2。

运行角色由配置键 `role.mode` 选定（不是子命令），`role.Get(mode)` 取对应 `Role` 对象执行。
gateway / cli / api 三者都通过同一个 `process.mode` 配置选择 single/batch/pipeline（都内嵌 api 引擎）；
daemon 是长驻的 pipeline 流水线。api 只作为库使用，不由 `role.mode` 派发。

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
   - **该约定现已端到端强制：领域之间只经根包接口互相引用，任何包都不再 import 兄弟领域的子包**
     （即不存在 `process/* → dao/store`、`process/* → parser/talog|filter`、`role/* → source/httpbody|stdin|tailer`、
     `role/* → parser/filter` 这类跨领域子包引用）。为此每个根包把子包里被跨领域复用的符号在 `<包名>.go` 里**重导出成门面**：
     - `dao.go`：`type Store = store.Store`（别名）+ `UserWriteModel`/`EventWriteModel`/`EventWriteModelSkipExisting`/`DeadLetterModel`（薄包装，返回值 `mongo.WriteModel` 是驱动类型，按设计不再隐藏）。
     - `parser.go`：`type Record = talog.Record`、`type RecordCategory = talog.RecordCategory`、`CategoryUser`/`CategoryEvent`、`EnvelopeKeys`（重导出）；过滤器经 `Parser.Filter()` 取 `*filter.Holder`，故消费方无需 import `parser/filter`。
     - `source.go`：`NewLines`（httpbody）/`NewReader`（stdin）/`NewTailer`（tailer）三个构造器门面，role 经它们建源。
     唯一被允许的"跨界"是 `client → role/api`（公共门面包装引擎，见 §7）；领域**自身的**子包之间（如 `process/single → process/core`、`role/cli → role/api`）不受此限。
2. **配置键路径 = 包路径；config 只产出一棵 Tree**：配置结构体下沉到各自模块并由领域根包聚合
   （`dao.Config` 聚合 `mongo`/`store`，`parser.Config` 聚合 `filter`，`process.Config` 聚合 `pipeline`，
   `source.Config` 聚合 `tailer`，`role.Config` 聚合 `gateway`），使**每个文件键路径都等于消费它的包路径**（`internal/` 下）：
   `logging.level`、`dao.mongo.uri`、`dao.store.maxElapsedTime`、`parser.filter.*`、
   `source.tailer.*`、`process.pipeline.*`、`role.gateway.*`。**没有顶层 typed 聚合结构体**：
   `config.Load` 把 文件 < `TANGO_*` env < flag 解析后，用 `viper.AllSettings` 物化成一棵**依赖中立**的
   `cfgtree.Tree`（只依赖 `mapstructure`，不依赖 viper；viper 只困在 `config` 包内）。每个模块提供
   `FromTree(t) = t.Sub("<前缀>").Into(&cfg) + ApplyDefaults + Validate`，自取并校验**自己那棵子树**
   （模块拥有"前缀 + 解码 + 默认 + 校验"）。
   **三个途径一致**：`RegisterFlags` 为每个键注册同名 `--<键>` flag，故 文件/env/flag 可互换；
   运行角色由配置键 `role.mode` 选定（不是子命令），上传策略由配置键 `process.mode` 选定；`--config` 是文件路径、非配置键。
   叶子模块**不得 import 顶层 `config` 包**，只依赖叶子载体 `cfgtree`。依赖方向：`config` → `cfgtree` + 各模块；各模块 ↛ `config`。
3. **`process` 是三种上传方式的唯一对外入口**：`single`（逐行即时写）/`batch`（同步批量）/
   `pipeline`（异步流水线）不被外部直接 import，三者实现同一 `process.Uploader` 接口
   （`Run(ctx, source.Source) error` + `Stop()`，可启动可停止，都消费一个日志源）；
   role 与 client SDK 只用 `process` 的 `New(cfg, …)` / `Mode` / `ParseMode` /
   `Source` / `Counters` / `Snapshot` / `WriteOptions`。
4. **日志是全局底层**：统一用 `internal/logging` 的包级函数（`logging.WithError`、`logging.Info`…），
   不要把 `*logrus.Logger` 当对象到处透传。`logging.Init(cfg)` 在启动时配置一次
   （接收完整的 `*logging.Config`，应用 level 与 format）。
5. **MaxElapsedTime（bulk-write 重试预算）属于 store**，不属于 mongo 连接配置；
   配置文件键为 `dao.store.maxElapsedTime`。
6. **配置结构 = internal 包层级；角色不重复 host 模块配置**：模块配置都在各自包路径的顶层
   （`logging.*`/`dao.*`/`parser.filter.*`/`source.tailer.*`/`process.*`），由需要的角色**共享复用**；
   `role.<name>.*` 只放该角色**专属**的字段（如 `role.gateway.addr`）。
   例如 gateway 的上传处理直接用顶层 `process.*` 与 `parser.filter.*`，不在 `role.gateway` 下再开
   `process`/`filter`。`role.daemon` 暂为空（daemon 完全由顶层模块驱动）。
   角色拿到整棵 `cfgtree.Tree`，通过各模块 `FromTree` **按枝叶裁剪**自己需要的子树
   （如 gateway 取 `dao`/`process`/`parser.filter` + `role.gateway`），而不是接收一个预先拆好的聚合结构。
7. **角色统一为 `Role` 接口，外层按 `role.mode` 派发**：`internal/role` 定义
   `Role`（`Run(ctx, cfgtree.Tree) error`）与 `Get(mode) (Role, error)`；`daemon`/`gateway`/`cli` 各实现 `Role`，
   并把启动编排（daemon 的信号处理/启动日志、cli 的 stdin→JSON stdout）折入各自的角色实现。
   `main.go` 只做 `Load→Tree → logging.FromTree+Init → role.FromTree 取 mode → role.Get(mode).Run(ctx, tree)`，
   **不再有 `cmd/` 包**。`api` 仍是被 gateway/cli 内嵌的引擎库，不是可派发角色。

## 3. 目录结构

```text
.
├── main.go      # Load→Tree → logging.Init → role.Get(role.mode).Run(ctx, tree)（无 cmd/ 子命令）
├── config/      # 构建并物化 cfgtree.Tree（Load/RegisterFlags）；不定义任何配置字段，viper 只在此
├── doc/ examples/
└── internal/
    ├── cfgtree/    # 依赖中立的配置载体 cfgtree.Tree（Sub/Into；只依赖 mapstructure）
    ├── logging/    # 全局 logger + logging.Config（+ FromTree）
    ├── parser/     # parser.go 整合 talog + filter（日志解析层）+ talog 门面（Record/categories/EnvelopeKeys 重导出）
    │   ├── config.go # parser.Config：聚合 parser 子模块配置
    │   ├── talog/    # TA JSON 行解析 -> Record
    │   └── filter/   # expr-lang include/exclude 上报过滤器 + filter.Config
    ├── source/     # 数据来源集合（source.Source 契约 + source.Config 聚合 tailer）+ New{Lines,Reader,Tailer} 构造器门面
    │   ├── httpbody/ # HTTP 请求体来源（NewLines；gateway/api 用）
    │   ├── tailer/  # 文件追尾来源 + tailer.Config / TailMode 常量（NewTailer；daemon 用）
    │   ├── stdin/   # 控制台 stdin 来源（NewReader；cli 用）
    │   └── taapi/   # 占位（未来 TA API 来源）
    ├── dao/        # dao.go 整合 store + mongo + ejson + sql 门面（Store 别名 / 写模型构造器 / Mongo&SQL Data API 重导出）
    │   ├── config.go # dao.Config：聚合 mongo.Config + store.Config
    │   ├── store/    # MongoDB 持久化 + store.Config
    │   ├── mongo/    # 连接装配 + mongo.Config + MongoDBFromURI
    │   ├── ejson/    # 通用 Mongo Data API 共享核心：Request/Response/Execute + EJSON 编解码（经 dao.go 中转）
    │   └── sql/      # SQL Data API 共享核心（拷贝自 mongosql）：vitess 解析 + translator + Driver.Exec（依赖 dao/mongo + vitess）
    ├── process/    # process.go 统一管理三种上传方式（唯一对外入口；Uploader 接口 + New）
    │   ├── core/    # 共享 Processor（parse→filter→identity→写模型；经 dao/parser 根包门面）+ stats（Counters/StatsCollector）
    │   ├── single/  # 逐行即时写 Uploader
    │   ├── batch/   # 同步批量 Uploader（累积 + bulk flush）
    │   └── pipeline/# 异步 N-worker 流水线 Uploader + pipeline.Config + dynamicbatch
    └── role/       # 运行角色：Role 接口 + Get(mode) 派发；role.Config 聚合 daemon/gateway
        ├── api/     # 可复用引擎库：api.Engine（New/Upload/Run/EnsureIndexes/Close）——非可派发角色
        ├── daemon/  # daemon.Role + daemon.Service（pipeline + tailer；含信号处理/启动日志）
        ├── gateway/ # gateway.Role + HTTP Server：内嵌 api.Engine + /upload + /ejson + /sql；gateway.Config（role.gateway.*）
        └── cli/     # cli.Role：内嵌 api.Engine + stdin 源；cli.Config（role.cli.function=upload|ejson|sql）
```

依赖方向：

```text
main             -> config + role + logging
client           -> role/api                                       (公共门面，redis-go 风格，包装引擎)
role             -> cfgtree + role/(daemon/gateway/cli/api)         (Role 接口 + Get(mode) 派发)
role/api         -> process + parser + dao + source + logging       (引擎库，被 gateway/cli 内嵌；Mongo Data API 经 dao 中转)
role/gateway     -> api + dao + process + parser + cfgtree + logging (gateway.Role + HTTP 面)
role/cli         -> api + dao + process + parser + source + cfgtree   (cli.Role + stdin/数据 面)
role/daemon      -> process + parser + dao + source + cfgtree + logging (daemon.Role + Service)
process          -> process/(core/single/batch/pipeline) + source + dao + parser + cfgtree
process/<子包>   -> dao + parser + source + (core)                  (经 dao/parser 根包门面，不碰 dao/store、parser/talog|filter)
parser           -> talog + filter + cfgtree                        (根包整合，对外重导出 Record/categories/EnvelopeKeys)
dao              -> store + mongo + ejson + sql + cfgtree            (根包整合，对外重导出 Store/写模型构造器 + Mongo&SQL Data API)
dao/ejson        -> dao/mongo（+ bson/mongo/options 驱动）           (dao 子包；用 MongoResource 取 client 与默认库)
dao/sql          -> dao/mongo + vitess sqlparser（+ 驱动）           (dao 子包；拷贝自 mongosql，SQL→MongoDB)
source           -> httpbody + stdin + tailer + cfgtree             (根包整合，对外暴露 Source + New{Lines,Reader,Tailer})
cfgtree          -> mapstructure   (叶子载体，不依赖 viper)
config           -> cfgtree + 各模块（仅用其 RegisterDefaults 注册键）；viper 只在 config
各模块通过 cfgtree.Tree 解码（FromTree）；各叶子模块 ↛ config；领域之间只经根包，不跨领域 import 兄弟子包
```

### 3.1 文件功能清单

#### 入口层 `main.go`（无 cmd/ 子命令）

| 文件 | 职责 |
|---|---|
| `main.go` | 根 cobra 命令（`NoArgs`）；`config.RegisterFlags` 注册全键 flag；`run` = `config.Load`→Tree → `logging.FromTree`+`Init` → `role.FromTree` 取 mode → `role.Get(mode).Run(ctx, tree)`；含 configFlag/resolveConfigPath |

#### 配置载体 `internal/cfgtree`（依赖中立，只依赖 mapstructure）

| 文件 | 职责 |
|---|---|
| `cfgtree/cfgtree.go` | `Tree`（已物化设置 map + 当前路径）；`New(settings)` / `Sub(key)`（累加路径，不新建子 viper）/ `Into(dst)`（沿路径取子树 + 时长/切片 hook + 弱类型，纯 mapstructure 解码） |

#### 配置层 `config/`（构建 Tree；不定义任何字段，viper 只在此）

| 文件 | 职责 |
|---|---|
| `config/config.go` | `registerAll`：枚举各模块 `RegisterDefaults`（唯一知道顶层段列表的地方），无 typed 聚合结构 |
| `config/load.go` | `Load`（文件<env<flag → `viper.AllSettings` 物化 → `cfgtree.New`）+ `RegisterFlags`（每键注册同名 flag） |
| `config/loader.go` | viper 装配 helper（`newViper` env 前缀、`readConfigFile`、`bindFlagsTo`） |

#### 全局基础 `internal/logging`

| 文件 | 职责 |
|---|---|
| `logging/logging.go` | 进程级 logger：`Init`、`L`、`WithError`/`WithField`/`WithFields`、`Info`/`Warn`/...、`Fields` 别名 |
| `logging/config.go` | `logging.Config`（level/format） |

#### 解析层 `internal/parser`

| 文件 | 职责 |
|---|---|
| `parser/parser.go` | `Parser`：内嵌 `*talog.Parser` + 持有 `*filter.Holder`（`Filter()`）；`New(flt)`；**talog 门面**：`type Record`/`RecordCategory` 别名 + `CategoryUser`/`CategoryEvent` + `EnvelopeKeys` 重导出 |
| `parser/talog/parser.go` | `Parser.ParseLine`：TA JSON → `Record` |
| `parser/talog/record.go` | `Record` + `Category`/`IsUserType`/`IsEventType` |
| `parser/filter/filter.go` | `Filter`：expr-lang 编译与 `Keep` |
| `parser/filter/holder.go` | `Holder`：原子可热替换的 filter 持有者 |
| `parser/filter/config.go` | `filter.Config`（include/exclude）+ `Build()` |

#### 来源层 `internal/source`

| 文件 | 职责 |
|---|---|
| `source/source.go` | `source.Source` 契约：`Run(ctx) <-chan string`；**子包门面**：`NewLines`(httpbody)/`NewReader`(stdin)/`NewTailer`(tailer) 构造器 |
| `source/config.go` | `source.Config`：聚合 `tailer.Config`（键 source.tailer.*） |
| `source/httpbody/httpbody.go` | `httpbody.Source`：把预解析的行数组（单条/批量）包成 line channel（gateway/api 用） |
| `source/stdin/stdin.go` | `stdin.Source`：从 io.Reader/os.Stdin 逐行扫描成 channel（cli 用） |
| `source/tailer/tailer.go` | `Tailer`：glob 发现文件、追尾、rescan，输出 line channel（hybrid/poll/event） |
| `source/tailer/config.go` | `tailer.Config` + `TailModeHybrid`/`Poll`/`Event` 常量 |

#### 数据访问 `internal/dao`

| 文件 | 职责 |
|---|---|
| `dao/dao.go` | `Dao`：显式持有 `Mongo *mongo.MongoResource` + `Store *store.Store`；`New(res, cfg)` 装配 store；**store 门面**：`type Store = store.Store` 别名 + `UserWriteModel`/`EventWriteModel`/`EventWriteModelSkipExisting`/`DeadLetterModel` 重导出；**ejson 门面**：`type EJSONRequest/EJSONResponse = ejson.Request/Response` 别名 + `EJSONAction*` 常量 + `DecodeEJSONRequest` + `(*Dao).EJSON` 中转；**sql 门面**：`type SQLResult = sql.Result` 别名 + `(*Dao).SQL`（惰性 `sync.Once` 建 `*sql.Driver`）中转 |
| `dao/config.go` | `dao.Config`：聚合 `mongo.Config` + `store.Config` |
| `dao/store/store.go` | `Store` + `store.Config`(MaxElapsedTime) + `WriteStats` + `BulkWrite(Ordered)` + 集合访问器 |
| `dao/store/identity.go` | `IdentityResolver`：`#account_id`/`#distinct_id` → `#user_id` |
| `dao/store/indexes.go` | `EnsureIndexes`：user/event/dead_letter/id_mapping 索引 |
| `dao/store/writemodel.go` | 构建写模型与 dead-letter 模型（`_ts` 防回退） |
| `dao/mongo/mongo.go` | `MongoResource` + `ConnectMongo`/`Borrow`/`DatabaseFromClient`/`Close` |
| `dao/mongo/config.go` | `mongo.Config`（URI + 连接超时）+ `MongoDBFromURI` |

#### Mongo Data API `internal/dao/ejson`（dao 子包，经 dao.go 中转）

| 文件 | 职责 |
|---|---|
| `dao/ejson/ejson.go` | `Request`/`Response` 类型 + action 常量 + `Execute(ctx, *mongo.MongoResource, *Request)`：从 resource 取 client 与默认库、解析 db/collection、按 action 分发 findOne/find/insertOne/updateOne/deleteOne/aggregate，选项（sort/projection/limit/skip/upsert）原样转发；无白名单/上限 |
| `dao/ejson/codec.go` | `DecodeRequest`（`bson.UnmarshalExtJSON`，relaxed，兼容 JSON）+ `(*Response).MarshalEJSON`（`bson.MarshalExtJSON` relaxed） |

#### SQL Data API `internal/dao/sql`（dao 子包，拷贝自 mongosql，经 dao.go 中转）

| 文件 | 职责 |
|---|---|
| `dao/sql/sql.go` | `Driver`（持有 client/db/translator/SchemaStore）+ `New(*mongo.MongoResource)`（注入连接，不自拨号/不 Close）+ `Exec(ctx, sql)` 按语句类型分发 `execFind/Aggregate/Insert/Update/Delete/InsertSelect`；`Result` 类型 |
| `dao/sql/codec.go` | `(*Result).MarshalEJSON`（SELECT 行含 BSON 类型，relaxed EJSON 编码） |
| `dao/sql/schema.go` | `SchemaStore`（表 schema 缓存）+ `ApplyDefaults`/`ApplyOnUpdate`/`NextAutoIncrement`（DDL 未引入时 schema 为空，自动跳过） |
| `dao/sql/translator/**` | vitess 解析 + SQL→`stmt.Statement` 翻译（translator/stmt/plan/internal{expr,sel,write}），原样拷贝自 mongosql |

#### 处理层 `internal/process`（`process.go` 唯一对外）

| 文件 | 职责 |
|---|---|
| `process/process.go` | 对外门面：`Uploader` 接口 + `Mode`/`ParseMode`/`Source` + `New(cfg,…)`；`Counters`/`Snapshot`/`WriteOptions` 别名 |
| `process/core/processor.go` | `core.Processor.Process`：parse→filter→identity→写模型分类（`Kind`/`Result`）；逐行 panic recover；`NewProcessor(*parser.Parser, *dao.Store, …)` 只依赖 dao/parser 根包门面 |
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

#### 运行时动态配置同步 `internal/cfgsync`（顶层领域，非角色；被 daemon/gateway 内嵌）

| 文件 | 职责 |
|---|---|
| `cfgsync/cfgsync.go` | 包文档 + `Watcher`（读侧）：持有 `*dao.Dao`/`*Config`/注入的 `onChange func(bson.M) error` + 单调 `lastVersion`；`New(d, cfg, onChange)` / `(*Watcher).Run(ctx)`（选 backend → 启动拉取 → backend 循环）；`observe` = nil 文档 no-op + 缺版本忽略 + **单调版本守卫**（`<= lastVersion` 丢弃）+ 调 `onChange`（失败记 warn 保留 last-good，不停循环） |
| `cfgsync/backend.go` | `Backend` 接口（`Run(ctx, observe)`）+ `selectBackend(d,cfg)`（poll/changestream/未知报错） |
| `cfgsync/config.go` | `cfgsync.Config`（`enabled`/`backend`/`documentID`/`pollInterval`/`reconcileInterval`/`collection`）+ `FromTree`/`ApplyDefaults`/`Validate`/`RegisterDefaults` + backend/默认常量 |
| `cfgsync/poll.go` | `pollBackend`：启动拉取一次 → 每 `pollInterval` 经 `fetchDoc` 读 → `observe`；读错误记 warn 不中断（自愈），任意拓扑可用 |
| `cfgsync/changestream.go` | `changeStreamBackend`：**先订阅后快照**（消除 read↔subscribe TOCTOU）→ stream 事件 + `reconcileInterval` 兜底全量读；断流→`resubscribe`（退避）+ 全量读 fallback，不硬崩；不支持拓扑→清晰报错指向 `backend=poll` |
| `cfgsync/fetch.go` | `fetchDoc`（经 `dao.EJSON` findOne，缺文档=nil no-op）+ `docVersion`（int/int32/int64/float64 归一为 int64） |
| `cfgsync/registry.go` | **applier 注册表 / allowlist**：`Registry`（`map[subtree]ApplyFunc`）+ `Register`/`Allows`/`Apply`（按子树路由；保留字 `_id`/`version` 跳过；不在 allowlist→拒绝记 warn；applier 失败保留 last-good）；是 `onChange` 的泛化 |
| `cfgsync/filter.go` | `RegisterFilter(reg, *parser.Parser)`：把 `filter:{include,exclude}` 子树映射到 `parser.SwapFilter`（编译失败保留 last-good）；唯一默认 applier；`toStringSlice` 容错 |
| `cfgsync/publish.go` | **写侧** `Publish(ctx, d, cfg, doc) (version, err)`：`validatePublishDoc`（剥 `_id`/`version` + 经同一 Registry 干跑校验 allowlist+编译 filter）→ `dao.EJSON` updateOne（`$set`+`$inc:{version:1}`，upsert，DocumentDB 安全）→ 读回新 version |

读写同核：`Watcher`（读）与 `Publish`（写）同属 cfgsync 根包，共享 `_tango_config` 文档 schema 与单调 version；
三面发布（gateway `POST /config`、cli `function=config`、`api.PublishConfig`）共用 `cfgsync.Publish`。
详见 §5.4 与 [`cfgsync.TODO.md`](cfgsync.TODO.md)。

#### 运行模式 `internal/role`

| 文件 | 职责 |
|---|---|
| `role/role.go` | `Role` 接口（`Run(ctx, cfgtree.Tree) error`）+ `Get(mode) (Role,error)` 派发 + 角色名常量（`API`/`CLI`/`Daemon`/`Gateway`） |
| `role/config.go` | `role.Config`：聚合 `daemon`/`gateway`/`cli` + `ApplyDefaults`/`Validate`/`RegisterDefaults` + `FromTree` |
| `role/api/api.go` | 可复用引擎库 `api.Engine`：`New(ctx, *dao.Config, *process.Config, *parser.Config)`/`Upload(lines)`（经 `source.NewLines`）/`Run(src)`/`EJSON(req)`（Mongo Data API，经 `dao.EJSON`）/`SQL(query)`（SQL Data API，经 `dao.SQL`）/`EnsureIndexes`/`Close` + `Result`（非可派发角色） |
| `role/daemon/role.go` | `daemon.Role.Run`：从 Tree 取 dao/parser/source/process → 信号处理 + 启动日志 + `New` → `Run`；含 `maskURI` |
| `role/daemon/report.go` | `daemon.Service`：持有 `*source.Config`，经 `source.NewTailer(srcCfg.Tailer)` 建源 → 强制 pipeline `process.New(cfg).Run` → MongoDB；周期/最终统计日志 |
| `role/daemon/config.go` | `daemon.Config`（`role.daemon.*`，暂空，schema 对称用） |
| `role/gateway/role.go` | `gateway.Role.Run`：从 Tree 取 dao/process/parser + `role.gateway` → `New` → `EnsureIndexes` → `Run(addr)` |
| `role/gateway/server.go` | gateway `Server`：内嵌 `*api.Engine` + HTTP 面；`New(...)`/`Upload`/`EJSON`/`SQL`/`EnsureIndexes`/`Close`/`Handler`/`Run`；路由 `/healthz` + `/upload`（按 `process.mode` 选策略）+ `/ejson`（Mongo Data API）+ `/sql`（SQL Data API）；`writeEJSON` 经 `ejsonMarshaler` 接口同时服务 EJSON/SQL 响应 |
| `role/gateway/config.go` | `gateway.Config`（仅 `role.gateway.addr`）+ `ApplyDefaults`/`Validate`/`RegisterDefaults` |
| `role/cli/role.go` | `cli.Role.Run`：取 dao + `role.cli`；`function=upload` 走 stdin 上报（统计 JSON → stdout），`=ejson` 走 `RunEJSON`，`=sql` 走 `RunSQL` |
| `role/cli/cli.go` | `cli.RunUpload(...)`：内嵌 `api.Engine` + `source.NewReader(in)` 一次性上报；`cli.RunEJSON(...)`：stdin EJSON 请求 → `engine.EJSON` → out EJSON；`cli.RunSQL(...)`：stdin SQL → `engine.SQL` → out EJSON |
| `role/cli/config.go` | `cli.Config`（`role.cli.function`=upload\|ejson\|sql）+ `ApplyDefaults`/`Validate`/`RegisterDefaults` |

## 4. Daemon Service（daemon 模式）

```text
Tailer -> Dispatcher(按用户亲和性路由) -> Worker[i](Parse -> Filter -> Identity -> Batch) -> MongoDB BulkWrite
```

`role/daemon` 将自己的 process 配置拷贝为 `ModePipeline` 后用 `process.New(cfg, …)` 构造 pipeline `Uploader`，再
`up.Run(ctx, tailerSource)` 驱动流水线（阻塞至 ctx 取消）；`daemon.Role.Run` 从 `cfgtree.Tree` 取各模块配置、
安装信号处理（SIGINT/SIGTERM）并启动该服务。

## 5. HTTP Gateway Service（gateway 模式）

```text
GET  /healthz
POST /upload   # body: {line?, lines?[]}；上传策略来自 process.mode
POST /ejson    # body: EJSON {action, collection, ...}；通用 Mongo Data API（见 §5.2）
POST /sql      # body: JSON {"sql":"..."}；SQL Data API（见 §5.3）
```

`/upload` 把请求体的日志数组经 `source.NewLines` 包成 Source，按 `process.mode` 选上传策略（single/batch/pipeline）
运行，返回本次统计（行数/写入数/死信等）。gateway **只接 httpbody 源**。`Server` 内嵌 `api.Engine`
引擎；`gateway.Role.Run` 从 `cfgtree.Tree` 裁剪共享的 `dao` + `process` + `parser` 段与角色专属的
`role.gateway` 段，构造 `gateway.New(ctx, daoCfg, procCfg, parserCfg, cfg)`（处理/过滤配置复用顶层共享模块；
过滤器随 `parser.Config` 一同传入，而非单独的 `filter.Config`）。

## 5.1 API 库 / CLI（api / cli 角色）

`role/api` 是可复用引擎 `api.Engine`：`New(ctx, daoCfg, procCfg, parserCfg)` 连接 MongoDB，`Upload(lines)` / `Run(src)`
对任意 `source.Source` 跑 `process.mode` 指定的策略。它被 gateway（`source.NewLines` 面）与 cli（`source.NewReader` 面）内嵌，
因此三者提供**完全相同**的 single/batch/pipeline 上传能力。`cli` 角色（`role.mode=cli`）从 stdin 读日志数组、`process.mode` 选策略，
统计 JSON 写 stdout；`api` 不由 `role.mode` 派发，作为库 import。公共 SDK `client`（`client.New(...).Upload(...)`）
也内嵌同一 `api.Engine`，见 §7。

## 5.2 Mongo Data API（`/ejson` · cli `ejson` · `api.EJSON`）

`internal/dao/ejson` 是通用 MongoDB 读写的**共享功能核心**，与上报链路（process/parser）完全解耦——
只依赖 `dao/mongo` 的 `MongoResource`。它是 dao 子包，由 `dao` 根包经 `dao.go` 中转（`(*Dao).EJSON`），
其它领域只经 `dao` 门面调用、不直接 import `dao/ejson`。三端经同一个 `ejson.Execute(ctx, res, req)` 落地，入口不同：

- **api（库）**：`(*api.Engine).EJSON(ctx, *dao.EJSONRequest)` → `c.dao.EJSON(...)`，用引擎已有的 `dao.Mongo`（`MongoResource`，含 client 与默认库）。
- **gateway（HTTP）**：`POST /ejson`，`handleEJSON` 读 body → `dao.DecodeEJSONRequest` → `engine.EJSON` → relaxed EJSON 响应（`writeEJSON`）。
- **cli（控制台）**：`role.cli.function=ejson` 时 `cli.RunEJSON` 从 stdin 读一个请求 → `engine.EJSON` → stdout 写响应。

请求/响应体为 **Extended JSON v2**，用官方驱动 `bson.UnmarshalExtJSON` / `bson.MarshalExtJSON`，
故 `ObjectId`/`Date`/`Decimal128` 等 BSON 类型可无损往返；请求 `Content-Type` 推荐 `application/ejson`，
也接受 `application/json`（EJSON 的子集，同一解析路径）。

action：`findOne`/`find`/`insertOne`/`updateOne`/`deleteOne`/`aggregate`；请求外壳字段：
`action`、`collection`（必填）、`database`（缺省取连接 URI 的库）、`filter`、`projection`、`sort`、
`limit`、`skip`、`document`、`update`、`pipeline`、`upsert`。

**设计上完全放开**（按需求"最大化功能、忽略安全限制"）：不做 database/collection 白名单、不做
operator/stage 黑白名单、不设 limit/返回条数/body/超时上限，请求原样转发给驱动。任意受控/鉴权由调用方负责。
注意 DocumentDB 不支持 aggregation-pipeline 形式的 `update`；此类请求会把引擎错误透传给调用方（普通 `$set` 文档更新正常）。

## 5.3 SQL Data API（`/sql` · cli `sql` · `api.SQL`）

`internal/dao/sql` 用 SQL 读写 MongoDB，是 dao 子包，由 `dao` 根包经 `dao.go` 中转（`(*Dao).SQL`，
首次调用惰性 `sync.Once` 构造 `*sql.Driver`）。代码**拷贝自 `github.com/aura-studio/mongosql`**
（`driver/` + `translator/`，跳过其 MySQL 协议层），并适配为 `New(*mongo.MongoResource)` 注入连接、
新增 `Result.MarshalEJSON`。三端入口与 ejson 同形：

- **api（库）**：`(*api.Engine).SQL(ctx, query)` → `c.dao.SQL(...)`。
- **gateway（HTTP）**：`POST /sql`，`handleSQL` 解 JSON `{"sql":...}` → `engine.SQL` → relaxed EJSON 响应。
- **cli（控制台）**：`role.cli.function=sql` 时 `cli.RunSQL` 从 stdin 读一条 SQL → `engine.SQL` → stdout 写 EJSON。

SQL 经 **vitess sqlparser**（MySQL 方言）解析，translator 翻译为 `stmt.Statement`，再由 `Driver.Exec`
执行：`SELECT`→find/aggregate/distinct、`INSERT`/`UPDATE`/`DELETE`→对应写操作、`INSERT ... SELECT`→聚合后写入。
**表名即集合名**，库取自连接 URI；响应 `Result` 经 `MarshalEJSON` 输出 relaxed EJSON（SELECT 行含 BSON 类型）。
这是 tango 唯一引入 `vitess.io/vitess` 的地方（`go get` 时自动把 `go` 指令上调到 1.26.2）。

限制：未拷贝 DDL（CREATE/ALTER TABLE 在 mongosql 的 MySQL 层），故 schema 表为空时 AUTO_INCREMENT/DEFAULT/
ON UPDATE 自动跳过；含表达式的 `UPDATE`（如 `SET n = n + 1`）翻译为 pipeline 形式，DocumentDB 不支持（常量 `SET n = 10` 正常）。

## 5.4 cfgsync（运行时动态配置同步）

`internal/cfgsync` 是 `cfgtree` 的**动态对偶**：`cfgtree` 启动时一次性加载静态配置，`cfgsync` 让其中
**一小撮显式 allowlist 的子树**在运行中持续对齐中心文档（集合 `_tango_config`）。它**不是角色**，而是像
`api.Engine` 一样的可嵌入 Watcher/Service，被**长驻且持有 live filter** 的角色内嵌：`daemon`（tailer→pipeline）
与 `gateway`（/upload）；一次性的 `cli` 与库 `api` 不内嵌读侧，`worker`/taskqueue 与之无关。

**读侧 Watcher**（embed 进 daemon/gateway）：选 backend → 启动拉取一次 → 运行中持续 `observe(doc)`；
经单调版本守卫后调注入的 `onChange`（= `Registry.Apply`）把变更子树路由到 applier，金标准是
`filter` → `parser.SwapFilter`（原子 Holder 热替换）。**写侧 Publish**（被 gateway/cli/api 调用）：先按
allowlist 校验 + 编译 filter，再 `dao.EJSON` updateOne（`$set` + `$inc:{version}`，upsert）原子写入。

**安全模型**——目标不是"瞬时全集群一致"（分布式物理做不到），而是**有界陈旧 + 自愈 + 不回退 + 坏配置打不挂**，
由以下叠加保证：

| 机制 | 作用 |
|---|---|
| 启动拉取 | 进程启动先全量读+应用 → 收敛停机期间错过的更新 |
| change stream（可选 backend） | 运行中亚秒级推送 |
| 定时拉取（poll / changestream 的 reconcile） | 兜底：断流/丢事件/超保留窗口 → 最终收敛，最坏陈旧 = 一个周期 |
| 单调版本守卫 | 只接受 `version` 更大的文档，丢弃更旧/重放 → 防回退 |
| 校验后再换 + 保留上一版 | 新 filter 编译失败→不替换、保留 last-good、记 warn → 坏配置打不挂 |
| 先订阅后快照 | changestream 先订阅再全量读 → 消除 read↔subscribe 的 TOCTOU 缝 |
| 消费者边界 | 仅 daemon/gateway 订阅（逐行过滤可中途换）；有界任务（backfill）不订阅，用一致快照 |

**覆盖范围（动态面积故意压到最小）**：能否运行时生效取决于目标有没有「live-reconfigure applier」，判据是
（a）经原子 indirection 在数据路径上被读；（b）不改资源身份/生命周期。据此默认 allowlist **只放
`parser.filter`**（顶多再加 `logging.level`）；结构性/收益低的（`process.*`、`source.tailer.*`）默认不放；
资源身份/生命周期的（`dao.mongo.*`、`role.mode`、`role.gateway.addr`、`cfgsync.*` 自身）绝不覆盖。
**publish 与 apply 两端都按同一 allowlist 校验**——新增动态键 = 往注册表加一个「校验 + 原子 apply」函数。

三面发布（同核 `cfgsync.Publish`，与 upload/ejson/sql 的"同核多面"一致）：

- **gateway（HTTP）**：`POST /config`，body = 配置文档（如 `{"filter":{"include":[...],"exclude":[...]}}`）→ `engine.PublishConfig` → `{version}`；`/ingest` 仍不提供。
- **cli（控制台）**：`role.cli.function=config`，stdin 读文档 → `cli.RunConfig` → stdout 写 `{version}`。
- **api（库）**：`(*api.Engine).PublishConfig(ctx, doc) (int64, error)`。

依赖边：`cfgsync → dao`（`EJSON`/`Watch` 门面）`+ parser`（`SwapFilter` 门面，由内嵌角色注入）`+ cfgtree`（`FromTree`）`+ logging`；
**不碰** `dao/ejson`、`parser/filter` 子包。DocumentDB 安全：仅 findOne / watch / updateOne(`$set`+`$inc`) upsert，无 pipeline update。
文档 schema：`{_id: <documentID>, version: <int 单调>, filter: {include: [...], exclude: [...]}}`。

```text
                 ┌──────────── 写侧（任一面）─────────────┐
 gateway POST /config ┐                                    │
 cli function=config  ├─→ cfgsync.Publish ──校验(allowlist+编译filter)──→ dao.EJSON updateOne($set+$inc) ─→ _tango_config
 api.PublishConfig    ┘                                    │ (DocumentDB 安全)
                                                           ▼
 ┌──────────── 读侧 Watcher（embed daemon/gateway）───────────────────────────────────────┐
 │  启动拉取 ─┐                                                                            │
 │  poll tick ├─→ fetchDoc(findOne) ─┐                                                     │
 │  reconcile ┘                      ├─→ observe(doc) ─→ [单调版本守卫: version>last?] ─否→ 丢弃(不回退) │
 │  changestream 事件 ───────────────┘                          │是                        │
 │                                                              ▼                         │
 │                                  Registry.Apply ─按子树路由→ filter applier ─→ parser.SwapFilter │
 │                                  (allowlist 外→拒绝记warn)     (编译失败→保留 last-good，不打挂)    │
 └────────────────────────────────────────────────────────────────────────────────────────┘
```

环境前置与降级：`backend=changestream` 需副本集（普通 MongoDB）或 `modifyChangeStreams` 开启（DocumentDB，
Elastic Cluster 不支持）；standalone mongod 无 change stream → 启动时**清晰报错并提示改用 `backend=poll`**（不静默吞）。
`backend=poll` 任意拓扑可用，是默认。

## 6. 上报 filter

上报 filter 作用于所有上报路径（daemon、gateway/cli/api upload），维度为
`#type` / `#event_name` / 属性，用 include / exclude（expr-lang）表达，
经 `parser.Config.Build()`（→ `filter.Config.Build()` / `filter.New`）编译。

## 7. 子模块间的函数依赖关系

领域之间**只经根包门面互相调用**（见 §2 约定 1）。下表按"消费方 → 提供方根包"列出实际用到的函数/类型，
括号内是兄弟领域子包里被门面转出的真正实现，消费方并不直接 import 它。

### 7.1 跨领域函数依赖一览

| 消费方 | 提供方（根包） | 用到的函数 / 类型 | 用途 |
|---|---|---|---|
| `process/{core,single,batch,pipeline}` | **dao** | `dao.Store`（=`store.Store` 别名）；`(*Store).Identity().Resolve(ctx, accountID, distinctID)`；`(*Store).BulkWrite`/`BulkWriteOrdered`；`(*Store).UserCollection`/`EventCollection`/`DeadLetterCollection`；`dao.UserWriteModel`/`EventWriteModel`/`EventWriteModelSkipExisting`/`DeadLetterModel` | 身份解析、写模型构造、bulk 写（实现在 `dao/store`） |
| `process/{core,single,batch,pipeline}` | **parser** | `*parser.Parser`；`(*Parser).ParseLine(line) → *parser.Record`；`(*Parser).Filter() → *filter.Holder`（再 `.Empty()`/`.Keep(doc)`）；`parser.Record` 字段（`Type`/`UUID`/`AccountID`/`DistinctID`/`Doc`）；`(*Record).Category()` + `parser.CategoryUser`/`CategoryEvent`；`parser.EnvelopeKeys` | 解析、过滤、分类、亲和路由键抽取（实现在 `parser/talog`+`parser/filter`） |
| `process`（root）、`role/{api,daemon}` | **source** | `source.Source`（`Run(ctx) <-chan string` 接口）；`source.NewLines`/`NewReader`/`NewTailer` | 消费日志源 / 构造具体源（实现在 `source/httpbody`+`stdin`+`tailer`） |
| `role/api` | **dao** | `dao.New(ctx, *dao.Config) → *dao.Dao`；`(*Dao).Store`；`(*Dao).Mongo.Close()`；`(*Store).EnsureIndexes(ctx)` | 连接 MongoDB、建索引、收尾 |
| `role/api` | **parser** | `(*parser.Config).Build() → *parser.Parser` | 装配解析器（含 filter） |
| `role/api` | **process** | `process.New(cfg, *dao.Dao, *parser.Parser, *Counters, WriteOptions) → Uploader`；`(Uploader).Run(ctx, Source)`；`process.Counters`/`Snapshot`/`WriteOptions`；`(*process.Config).ModeValue()` | 选策略并驱动上传 |
| `role/{gateway,cli}` | **role/api** | `api.New(ctx, *dao.Config, *process.Config, *parser.Config) → *Engine`；`(*Engine).Upload`/`Run`/`Data`/`EnsureIndexes`/`Close`；`api.Result` | 内嵌同一引擎（上报 + Mongo Data API） |
| `role/api` | **dao** | `(*Dao).EJSON(ctx, *EJSONRequest) → *EJSONResponse`；`dao.EJSONRequest`/`EJSONResponse` | Mongo Data API 中转（实现在 dao/ejson，经 dao 门面；dao/ejson 内部依赖 dao/mongo） |
| `role/{gateway,cli}` | **dao** | `dao.DecodeEJSONRequest(body) → *EJSONRequest`；`(*EJSONResponse).MarshalEJSON()` | HTTP / stdin 的 EJSON 请求解析与响应编码 |
| `role/api` | **dao** | `(*Dao).SQL(ctx, query) → *SQLResult`；`dao.SQLResult` | SQL Data API 中转（实现在 dao/sql，经 dao 门面；dao/sql 内部依赖 dao/mongo + vitess） |
| `role/{gateway,cli}` | **dao** | `(*SQLResult).MarshalEJSON()` | HTTP / stdin 的 SQL 结果 EJSON 编码 |
| `role/daemon` | **dao/parser/source/process** | `dao.New`；`(*parser.Config).Build`；`source.NewTailer(srcCfg.Tailer)`；`process.New(pipelineCfg, …).Run(ctx, tailerSrc)` | 编排长驻流水线 |
| `client`（公共 SDK） | **role/api** | `api.New(o.ctx, &o.dao, &o.proc, &o.parser)`；`(*Engine).Upload`/`EnsureIndexes`/`Close` | redis-go 风格门面，复用真实 config 结构体 |
| `cfgsync` | **dao** | `(*Dao).EJSON(ctx, *EJSONRequest)`（findOne 读 / updateOne `$set`+`$inc` upsert 写）；`(*Dao).Watch(ctx, coll, pipeline, opts) → *mongo.ChangeStream` | 读中心文档 + 订阅 change stream + 原子发布（实现在 dao/ejson + dao/mongo，经 dao 门面） |
| `cfgsync` | **parser** | `(*Parser).SwapFilter(include, exclude)` | 编译后再原子热替换 live filter（实现在 parser/filter，经 parser 门面；由内嵌角色注入回调调用） |
| `role/{daemon,gateway}` | **cfgsync** | `cfgsync.FromTree(t)`；`cfgsync.New(d, cfg, reg.Apply)`；`(*Watcher).Run(ctx)`；`cfgsync.NewRegistry`/`RegisterFilter` | 内嵌读侧 Watcher（goroutine，panic recover），热替换自身 live filter |
| `role/{api,gateway,cli}` | **cfgsync** | `cfgsync.Publish(ctx, d, cfg, doc) → version` | 三面配置发布（同核），`api.PublishConfig` 中转，gateway/cli 经 api |
| `config` | **dao/parser/source/process/role/cfgsync/logging** | 各 `(*Config).RegisterDefaults(set, prefix)`；各模块 `FromTree(t)` | 注册全键 + 按子树解码 |

### 7.2 单行上报的函数调用链（运行时数据流）

三种 `Uploader` 的差异只在批量/并发编排，**逐行处理规则共享 `core.Processor.Process`**，
其内部按下序调用各根包门面（不碰任何兄弟子包）：

```text
(Uploader).Run(ctx, src)                                   [process/{single,batch,pipeline}]
 ├─ src.Run(ctx) <-chan string                             [source.Source：NewLines/NewReader/NewTailer 之一]
 └─ core.Processor.Process(ctx, line):                     [process/core]
      ├─ (*parser.Parser).ParseLine(line) → *parser.Record       → 失败：dao.DeadLetterModel(line, err)
      ├─ (*parser.Parser).Filter().Empty()/.Keep(rec.Doc)        → 命中 exclude/不命中 include：丢弃
      ├─ (*dao.Store).Identity().Resolve(ctx, AccountID, DistinctID) → userID
      │                                                           → 失败：dao.DeadLetterModel(line, err)
      └─ switch rec.Category():
           parser.CategoryUser  → dao.UserWriteModel(rec.Type, userID, rec.Doc)
           parser.CategoryEvent → dao.EventWriteModel / EventWriteModelSkipExisting(rec.UUID, rec.Doc)
            （均返回 mongo.WriteModel —— MongoDB 驱动类型，门面不隐藏）
 └─ (*dao.Store).BulkWrite / BulkWriteOrdered(ctx, coll, models)   [按 user/event/dead_letter 分集合刷写]
```

pipeline 模式额外用 `parser.EnvelopeKeys` + `ExtractRoutingKey`/`RouteIndex` 做用户亲和性分发，
保证同一用户的写操作落到同一 worker 顺序执行。

### 7.3 配置装配的函数链

```text
main → config.Load(文件 < TANGO_* env < flag) → viper.AllSettings 物化 → cfgtree.Tree
 角色侧（各取自己那棵子树）：
   dao.FromTree(t)     = t.Sub("dao").Into(&c)     + ApplyDefaults + Validate
   parser.FromTree(t)  = t.Sub("parser").Into(&c)  + ApplyDefaults + Validate
   source.FromTree(t)  = t.Sub("source").Into(&c)  + ApplyDefaults + Validate
   process.FromTree(t) = t.Sub("process").Into(&c) + ApplyDefaults + Validate
 再交给 role/api（或 daemon）的 New(...)：dao.New / parser.Config.Build / process.New。
```

`client` 不走 `cfgtree`：`With*` 选项直接写入它内嵌的真实 `dao.Config`/`parser.Config`/`process.Config`，
`client.New` 把三者地址原样交给 `api.New`（与上面角色侧最终调用的 `api.New` 同一入口）。
