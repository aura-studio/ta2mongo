# tango v1.7 需求文档（backfill TA OpenAPI 历史回填）

> 状态：v1.7 = **v1.6（file 存量文件一次性导入）+ backfill**。本文只描述 v1.7 相对 v1.6 的**增量**
> ——`backfill` TA OpenAPI 历史回填领域；v1.6 的 file 需求见 [`../v1.6/requirements.md`](../v1.6/requirements.md)，
> 架构与图见 [`arch.md`](arch.md) §10 与图 D，模块依赖见 [`dependency-graph.md`](dependency-graph.md)。
> **不含** taskqueue + worker 控制面（原 v1.6 三段规划的第三段，暂未纳入）。
> v1.0 源码参照：`git show 8bc899b:<path>`（tag v1.0.2）。

## 1. 背景与目标

v1.0→v1.1 的大收敛删除了 TA-OpenAPI backfill（连同 worker/taskqueue/公开 SDK 等）。其余能力已在
v1.3–v1.6 按现行架构回归（SQL Data API / cfgsync / client SDK / file）。

**v1.7 目标：加回 backfill**——从 ThinkingData（TA）OpenAPI 按日期范围（事件表）或整表（用户表）拉取历史
数据，**编码成 TA 日志行后喂进既有上报/写入链路**（经内存中转源 `internal/source/mem` + 强制 pipeline 模式）。
按现行架构（cfgtree / source / process / dao / role）重设、复用既有上报管线：**不改 parser、不改 dao**
（parser 与 dao 与 v1.6 完全一致），不引入自定义写模型、不做 checkpoint、不做 SQL filter 下推。
自 v1.0 tag `8bc899b` 迁回灵感、按 **mongo driver v2 + DocumentDB 安全**重建。

基线前提：go 钉 **1.25.5**；mongo **driver v2**；mongosql 不动；所有写路径 DocumentDB 兼容。

## 2. 范围决策（backfill）

- backfill 是**有界一次性任务**，入口**只在 cli（`function=backfill`）+ api 库（`Engine.RunBackfill`）+
  client SDK（`Client.RunBackfill`）三处**；gateway / daemon **不设** backfill 入口。
- **不恢复**同步执行的 `POST /backfill`（Lambda/ALB 超时模型下不可用，回填动辄分钟到小时级、按页流式上报）。
- **不做 checkpoint / 断点续传**：fetcher 仅 fetch→encode→push，无进度集合、无 `runID`、无 `SQLSignature` 守卫；
  重跑就是重新拉取，**幂等靠写模型**（event 按 `#uuid` `$setOnInsert`、user 按解析出的 `#user_id` `user_setOnce`）。
- 公开 `client/` SDK 新增 `RunBackfill` 面，一律经 Engine 中转，守 import 边界（client 不 import `internal/backfill`，
  经 `api.BackfillConfig` 别名）。
- **不纳入** taskqueue + worker 控制面（任务发布/异步消费、`role.mode=worker`、`POST /publish/backfill`）。

### 能力矩阵（v1.7 新增入口）

| 面 | 新增 |
| --- | --- |
| Engine（`internal/role/api`） | `RunBackfill(ctx, *BackfillConfig)` |
| client/ 公开 SDK | `RunBackfill(ctx)`（`WithBackfill*` options；经 Engine 中转，守 import 边界） |
| cli | `role.cli.function=backfill`（读 `backfill.*`，不读 stdin） |
| gateway / daemon | 不动（无 backfill 入口） |

## 3. 设计

新域 `internal/backfill`（driver v2 + DocumentDB 安全重建）。核心理念：**backfill 只负责「拉取 + 编码」，
落库完全复用既有上报管线**——fetcher 把每行编码成 TA 日志行 push 进内存中转源，正常 pipeline 把它当普通上报消费。

- **三端点流程**（`client.go`/`httpclient.go` 串行驱动 TA OpenAPI）：`submit-sql`（提交 Presto SQL，`pageSize`
  在 submit 时定）→ 轮询 `sql-task-info`（至 FINISHED / `pollTimeout`）→ 按 `pageCount` 分页拉 `sql-result-page`
  （NDJSON，`ndjson.go` 流式解码，`pageRetries` 重试；task 过期则重提一次、从 page 0 重拉，靠写模型去重）。
  token 走 query 参数；代理支持 http/https/socks5（`golang.org/x/net/proxy`）。
- **编码成日志行**（`rowdecode.EncodeRowAsJSONLine`）：把 header 与一行数据拼成与 file-tail 同形的 TA JSON 行
  （`#`/`_`/`$` 前缀字段提顶层、其余进 `properties`、nil 丢弃）；行缺 `#type` 时注入默认类型——event 表注 `track`，
  user 表注 `user_setOnce`（默认，永不覆盖）或 `user_set`（`forceSkipExisting=false` 时）。
- **内存中转源**（**新包** `internal/source/mem`，门面 `source.NewMem`）：channel 背书的 `source.Source`
  （`New(buf)` / `Push(ctx,line)` / `Close()` / `Run(ctx) <-chan string`，**单生产者**；Push 满则阻塞背压）。
  它是 `source/file` 的内存版——file 从磁盘读行、mem 从同进程生产者收行。
- **强制 pipeline 消费**（`Engine.RunBackfill` + `runPipeline`）：起一个后台 pipeline uploader 漏取中转源，同时 fetcher
  作为生产者 fetch→encode→push；`runPipeline` **强制 `process.mode=pipeline`**（无视 engine 配置的 mode），让生产/消费并发。
  借派生 ctx：pipeline 写失败时 cancel，解开 fetcher 在满缓冲上阻塞的 Push（避免死锁）。
  于是回填行走的是与实时上报**完全相同**的 parse → filter → identity → DocumentDB 安全写路径，**无自定义写模型、
  无选择 filter、无 checkpoint**。
- **两表 SQL**（`config.BuildSQL`，**无下推**）：
  - event：`SELECT * FROM [schema.]v_event_<pid> WHERE "$part_date"='<day>'`
    `[AND "#event_time">='..'][AND "#event_time"<='..'][AND "#event_name" IN ('a','b')] [LIMIT n]`。
  - user：`SELECT * FROM [schema.]v_user_<pid> [LIMIT n]`（整表、不分区）。
  - event-name 之外的选择性 = engine 的上报 filter（`parser.filter.*`），故本包**不依赖 parser/filter**。
- **配置 `backfill.*`**：apiBaseURL / token / proxy / projectID / table（event|user）/ events（`[]string` → `#event_name IN`）/
  schemaPrefix / partDateRange.{start,end} / eventTimeRange.{start,end} / limit / pageSize / paginate（`*bool` 默认 true）/
  pageRetries / pollInterval / pollTimeout / forceSkipExisting（`*bool` 默认 true——仅决定 user 表 `#type`：true→`user_setOnce`、
  false→`user_set`；**不影响 event**）（见 [`config.md`](config.md) §backfill）。
- **入口**：`Engine.RunBackfill` + `api.BackfillConfig` 别名 + cli `function=backfill` + client `RunBackfill`/`WithBackfill*`。
- **依赖**：`internal/backfill` 现**只 import** `internal/logging` + `internal/cfgtree`（近叶子；无 dao、无 parser/filter、
  无 process、无 source）；旧「backfill → parser/filter」跨域进子包的例外**已消除**，依赖图中不再有此类例外。

## 4. 约束

- **不改 parser / 不改 dao**：回填复用既有写模型（event 走 `UserWriteModel` 之外的 track upsert、user 复用既有
  `UserWriteModel` 经 `user_setOnce`），与 v1.6 一致；不新增 `UserSnapshotWriteModel`、不新增 `CompileToSQL`。
- DocumentDB 红线：update 一律普通操作符（无聚合管道）；判错只认数字 code；集成测试连真实 DocumentDB。
- client/ 新面经 `internal/role/api` Engine 中转，import 边界测试卡守（client 不 import `internal/backfill`）。
- 配置键路径 = 包路径（`backfill.*`）；max 示例仅写实际用到的段。
- **user 表语义变更**：user 行**也走 pipeline**，身份由 `#account_id`/`#distinct_id` 在 identity 阶段解析，user 文档以
  **tango 解析出的 `#user_id`** 为键（与 event 口径一致），**而非**源表 `#user_id`。**这要求 `v_user` 携带身份列**。
- 幂等（无 checkpoint，靠写模型）：event 走 `track` 恒按 `#uuid` `$setOnInsert`（**与 `forceSkipExisting` 无关**，
  重导零新增、不覆写）；user 由 `forceSkipExisting`（默认 true）→ `user_setOnce`（`$setOnInsert`，**历史永不覆盖线上**）、
  `false` → `user_set`（`$set`）。重跑直接重新拉取即收敛。

## 5. 验收

- 单元：`config`（BuildSQL / 默认值 / 校验 / Days）/ `rowdecode`（`#type` 注入、`properties` 分组、nil 丢弃）/
  client（NDJSON 流解码、task 过期重提）/ `source/mem`（Push 背压、Close、单生产者、ctx 取消）。
- 集成：`httptest` mock TA OpenAPI 三端点 + 真实 DocumentDB 临时库；event + user 两路均经 pipeline 落库；
  **重跑幂等**（event `#uuid` 零新增、user `user_setOnce` 不覆写）；pipeline 写失败时 fetcher 不死锁（派生 ctx 解阻塞）。
- import 边界：`internal/backfill` 仅依赖 logging + cfgtree；client 不 import `internal/backfill`。
- 文档（arch §10 + 图 D / usage / config + dependency-graph）+ 示例 `examples/config/cli/cli.backfill.{min,max}.{yaml,json}`。
- 门禁全绿（gofmt/vet/全量 test，连真实 DocumentDB）。

## 6. 不在 v1.7 范围

- **taskqueue + worker** 控制面（`role.mode=worker` / `POST /publish/backfill` / cli `publish` /
  `Engine.PublishBackfillTask`）——原 v1.6 三段规划的第三段，暂未纳入。
- 同步执行的 `POST /backfill`。
- gateway / daemon 的 backfill 入口（仅 cli + api + client）。
- `source/taapi` 占位的兑现（backfill 经 `internal/source/mem` 中转源接入 Source 抽象，不再需要独立的 taapi Source）。
