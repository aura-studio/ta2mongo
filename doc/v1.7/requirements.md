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
数据，回填进既有上报/写入链路，断点可续。按现行架构（cfgtree / source / parser / process / dao / role）
重设，不回滚 v1.0 实现：自 v1.0 tag `8bc899b` 迁回、按 **mongo driver v2 + DocumentDB 安全**重建。

基线前提：go 钉 **1.25.5**；mongo **driver v2**；mongosql 不动；所有写路径 DocumentDB 兼容。

## 2. 范围决策（backfill）

- backfill 是**有界一次性任务**，入口**只在 cli（`function=backfill`）+ api 库（`Engine.RunBackfill`）+
  client SDK（`Client.RunBackfill`）三处**；gateway / daemon **不设** backfill 入口。
- **不恢复**同步执行的 `POST /backfill`（Lambda/ALB 超时模型下不可用，回填动辄分钟到小时级、按页流式落库）。
- **断点续传**：进度落集合 `_backfill_progress`，每 `runID` 一文档、**每页 flush**；同 `runID` 重跑从下一页接力。
  `SQLSignature` 漂移守卫拒绝口径不一致的续跑。
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

新域 `internal/backfill`（自 v1.0 `8bc899b` 迁回、driver v2 + DocumentDB 安全重建）：

- **三端点流程**：`client.go`/`httpclient.go` 串行驱动 TA OpenAPI——`submit-sql`（提交 Presto SQL）→ 轮询
  `sql-task-info`（至完成 / `pollTimeout`）→ 分页拉 `sql-result-page`（NDJSON，`ndjson.go` 流式解码，
  `pageRetries` 重试）。token 走 query 参数；代理支持 http/https/socks5（`golang.org/x/net/proxy`，由间接转直接依赖）。
- **两路写入**（`executor.go`，按 `table`）：
  - **event 表**（`v_event_<pid>`）：每页行经 `rowdecode.EncodeRowAsJSONLine` → **逐页喂 `Engine.Upload`**
    （完整复用 parse → filter → identity → DocumentDB 安全写）。event 路经注入回调复用上报管线，避免 api↔backfill import 环。
  - **user 表**（`v_user_<pid>`）：行绕过 parser，每行 → `dao.UserSnapshotWriteModel(#user_id, doc, forceSkipExisting)`
    （普通 `$set`/`$setOnInsert`，**无聚合管道**）→ `Store.BulkWriteOrdered`。
- **SQL 下推**：`sqlbuilder.go` 的 `buildDaySQL` + `internal/parser/filter/sql.go` 的 `CompileToSQL`
  （expr-lang include/exclude → Presto WHERE）把选择 filter 下推 TA SQL（减少回拉）。
- **checkpoint**：`checkpoint.go`（`_backfill_progress`：Run / DayProgress / SQLSignature / initDays / resume）；
  `FindOne` + `ReplaceOne` upsert，**绝不** pipeline update。
- **配置 `backfill.*`**：apiBaseURL / token / proxy / projectID / table / partDateRange / eventTimeRange /
  pageSize / paginate / pageRetries / pollInterval / pollTimeout / limit / schemaPrefix / runID /
  progressCollection / forceSkipExisting / skipLocalFilter + include/exclude（见 [`config.md`](config.md) §backfill）。
- **入口**：`Engine.RunBackfill` + `api.BackfillConfig` 别名 + cli `function=backfill` + client `RunBackfill`/`WithBackfill*`。

## 4. 约束

- DocumentDB 红线：update 一律普通操作符（无聚合管道）；判错只认数字 code；集成测试连真实 DocumentDB。
- client/ 新面经 `internal/role/api` Engine 中转，import 边界测试卡守（client 不 import `internal/backfill`）。
- 配置键路径 = 包路径（`backfill.*`）；max 示例仅写实际用到的段。
- 幂等：event 路走 `track` 恒按 `#uuid` `$setOnInsert`（**与 `forceSkipExisting` 无关**，重导零新增、不覆写）；
  user 路 `forceSkipExisting`（默认 true）→ `$setOnInsert`（**历史永不覆盖线上**）、`false` → `$set`。同 `runID` 重跑从 checkpoint 续、收敛。

## 5. 验收

- 单元：sqlbuilder / ndjson / rowdecode / checkpoint 状态机 / config。
- 集成：`httptest` mock TA OpenAPI 三端点 + 真实 DocumentDB 临时库；event + user 两路；中断后 resume；SQLSignature 漂移拒绝。
- 文档（arch §10 + 图 D / usage / config + dependency-graph）+ 示例 `examples/config/cli/cli.backfill.{min,max}.{yaml,json}`。
- 门禁全绿（gofmt/vet/全量 test，连真实 DocumentDB）。

## 6. 不在 v1.7 范围

- **taskqueue + worker** 控制面（`role.mode=worker` / `POST /publish/backfill` / cli `publish` /
  `Engine.PublishBackfillTask`）——原 v1.6 三段规划的第三段，暂未纳入。
- 同步执行的 `POST /backfill`。
- gateway / daemon 的 backfill 入口（仅 cli + api + client）。
- `source/taapi` 占位的兑现（backfill 走页驱动，不经 Source 抽象）。
