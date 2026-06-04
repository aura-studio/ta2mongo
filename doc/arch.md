# tango 架构说明

## 1. 目标

`tango` 将 ThinkingData 日志 JSON 行采集并写入 MongoDB 的 `user` / `event` /
`dead_letter` 集合。它是单一二进制，按运行角色组织，只保留**上报日志**能力。
所有上报角色共享同一个引擎（`internal/role/api`），区别只在数据**来源**与**入口形态**：

| 角色 | `role.mode` | 来源 | 职责 |
|---|---|---|---|
| **Daemon** | `daemon` | tailer（文件） | 常驻：文件追尾、解析、filter、identity、流水线批量写 MongoDB |
| **Gateway** | `gateway` | httpbody（HTTP 请求体） | 常驻 HTTP：单个 `/upload`，按 `process.mode` 选 single/batch/pipeline |
| **CLI** | `cli` | stdin（控制台） | 一次性：对齐 gateway `/upload`，从 stdin 读日志数组，按 `process.mode` 上报 |
| **API** | （库，不可派发） | httpbody（调用方传入） | 作为 Go 库被业务代码 import：`api.New(...).Upload(lines)` |

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
    └── role/       # 运行角色：Role 接口 + Get(mode) 派发；role.Config 聚合 daemon/gateway
        ├── api/     # 可复用引擎库：api.Engine（New/Upload/Run/EnsureIndexes/Close）——非可派发角色
        ├── daemon/  # daemon.Role + daemon.Service（pipeline + tailer；含信号处理/启动日志）
        ├── gateway/ # gateway.Role + HTTP Server：内嵌 api.Engine + /upload；gateway.Config（role.gateway.*）
        └── cli/     # cli.Role：内嵌 api.Engine + stdin 源，统计 JSON → stdout
```

依赖方向：

```text
main     -> config + role + logging
role     -> cfgtree + role/(daemon/gateway/cli/api)   (Role 接口 + Get(mode) 派发)
role/api -> process + parser + dao + source/httpbody + logging   (引擎库，被 gateway/cli 内嵌)
gateway  -> api + dao + process + parser + cfgtree + logging   (gateway.Role + HTTP 面)
cli      -> api + dao + process + parser + cfgtree              (cli.Role + stdin 面)
daemon   -> process + parser + dao + source + cfgtree + logging (daemon.Role + Service)
process  -> single/batch/pipeline + source + dao + parser + cfgtree
parser   -> talog + filter + cfgtree
dao      -> store + mongo + cfgtree
cfgtree  -> mapstructure   (叶子载体，不依赖 viper)
config   -> cfgtree + 各模块（仅用其 RegisterDefaults 注册键）；viper 只在 config
各模块通过 cfgtree.Tree 解码（FromTree）；各叶子模块 ↛ config
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
| `process/process.go` | 对外门面：`Uploader` 接口 + `Mode`/`ParseMode`/`Source` + `New(cfg,…)`；`Counters`/`Snapshot`/`WriteOptions` 别名 |
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
| `role/role.go` | `Role` 接口（`Run(ctx, cfgtree.Tree) error`）+ `Get(mode) (Role,error)` 派发 + 角色名常量（`API`/`CLI`/`Daemon`/`Gateway`） |
| `role/config.go` | `role.Config`：聚合 `daemon`/`gateway` + `ApplyDefaults`/`Validate`/`RegisterDefaults` + `FromTree` |
| `role/api/api.go` | 可复用引擎库 `api.Engine`：`New(ctx,dao,proc,filter)`/`Upload(lines)`/`Run(src)`/`EnsureIndexes`/`Close` + `Result`（非可派发角色） |
| `role/daemon/role.go` | `daemon.Role.Run`：从 Tree 取 dao/parser/source/process → 信号处理 + 启动日志 + `New` → `Run`；含 `maskURI` |
| `role/daemon/report.go` | `daemon.Service`：tailer 源 → 强制 pipeline `process.New(cfg).Run` → MongoDB；周期/最终统计日志 |
| `role/daemon/config.go` | `daemon.Config`（`role.daemon.*`，暂空，schema 对称用） |
| `role/gateway/role.go` | `gateway.Role.Run`：从 Tree 取 dao/process/parser.filter + `role.gateway` → `New` → `EnsureIndexes` → `Run(addr)` |
| `role/gateway/server.go` | gateway `Server`：内嵌 `*api.Engine` + HTTP 面；`New(ctx,dao,process,filter,cfg)`/`Upload`/`EnsureIndexes`/`Close`/`Run`；`/healthz` + 单个 `/upload`（按 `process.mode` 选策略） |
| `role/gateway/config.go` | `gateway.Config`（仅 `role.gateway.addr`）+ `ApplyDefaults`/`Validate`/`RegisterDefaults` |
| `role/cli/role.go` | `cli.Role.Run`：从 Tree 取 dao/process/parser.filter → `Run(…, os.Stdin)` → 统计 JSON 写 `os.Stdout` |
| `role/cli/cli.go` | `cli.Run(ctx,dao,proc,filter,in)`：内嵌 `api.Engine` + `stdin.Source`，一次性上报（核心，被 `cli.Role` 包装） |

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
```

`/upload` 把请求体的日志数组包成 `httpbody.Source`，按 `process.mode` 选上传策略（single/batch/pipeline）
运行，返回本次统计（行数/写入数/死信等）。gateway **只接 httpbody 源**。`Server` 内嵌 `api.Engine`
引擎；`gateway.Role.Run` 从 `cfgtree.Tree` 裁剪共享的 `dao` + `process` + `parser.filter` 段与角色专属的
`role.gateway` 段，构造 `gateway.New(ctx, dao, process, filter, cfg)`（处理/过滤配置复用顶层共享模块）。

## 5.1 API 库 / CLI（api / cli 角色）

`role/api` 是可复用引擎 `api.Engine`：`New` 连接 MongoDB，`Upload(lines)` / `Run(src)`
对任意 `source.Source` 跑 `process.mode` 指定的策略。它被 gateway（httpbody 面）与 cli（stdin 面）内嵌，因此三者
提供**完全相同**的 single/batch/pipeline 上传能力。`cli` 角色（`role.mode=cli`）从 stdin 读日志数组、`process.mode` 选策略，
统计 JSON 写 stdout；`api` 不由 `role.mode` 派发，作为库 import。

## 6. 上报 filter

上报 filter 作用于所有上报路径（daemon、gateway/cli/api upload），维度为
`#type` / `#event_name` / 属性，用 include / exclude（expr-lang）表达，
经 `parser.Config.Build()`（→ `filter.Config.Build()` / `filter.New`）编译。
