# tango v1.6 需求文档

> 状态：需求已收敛（2026-06-12 拍板）；**2026-06-24 更正**：原计划的第三块
> **taskqueue + worker（v1.6.2）已回退、移出 v1.6 范围**（见 §6）。v1.6 现仅含
> uploadfile + backfill 两块，分支顶端为 `v1.6.1`。
> 本文档自洽，不依赖已删除的 `diff.md` / `todo.md`；历史细节见 git 历史。
> v1.0 源码参照：`git show 8bc899b:<path>`（tag v1.0.2）。

## 1. 背景与目标

v1.0→v1.1 的大收敛把 tango 从"全功能数据接入/控制平台"砍成纯上报引擎，删除了：
公开 SDK、operator 命令树、taskqueue+worker、TA-OpenAPI backfill、临时 SQL 导入、
remote config、文件单次上传+断点续传（`UploadFiles` / `_tango_fileupload`）。

其中多数能力已在前序版本按新架构回归：

| 被删能力 | 回归形态 | 版本 |
| --- | --- | --- |
| 临时 SQL | `dao/sql` 只读 SQL Data API（依赖 mongosql） | v1.3 / v1.4 |
| remote config | `cfgsync`（发布/拉取/热替换 filter） | v1.5 |
| 公开 Go SDK | `client/`（只依赖 `internal/role/api`，import 边界测试卡守） | v1.5 |
| operator 命令树 | cli `role.cli.function=*` | v1.1 起渐进 |

**v1.6 目标：加回两块**，全部按现行架构（cfgtree / source / parser / process /
dao / role）重设，不回滚 v1.0 实现：

1. **uploadfile**（v1.6.0）——本地存量文件一次性导入（原"文件上传"的简化形态）。
2. **backfill**（v1.6.1）——TA OpenAPI 历史数据回填（新域 `internal/backfill`）。

> 原列入 v1.6 的第三块 **taskqueue + worker** 已回退（见 §6 不在本轮范围）：backfill 的
> 异步控制面方向改由独立线探索（v1.7），不再绑定 v1.6 的常驻 worker 形态。

基线前提：go 钉 **1.25.5**；mongo **driver v2**；mongosql v1.0.1 不动；
所有写路径 DocumentDB 兼容。

## 2. 已拍板的范围决策

- 阶段顺序与 tag：**uploadfile=v1.6.0 → backfill=v1.6.1**。
- **不恢复**同步执行的 `POST /backfill`（Lambda/ALB 超时模型下不可用）；本轮也不提供异步
  发布/消费控制面（taskqueue+worker 已移出，见 §6）。
- uploadfile **无 checkpoint / 无断点续传**：仿照 daemon 对文件的消费，纯有限 Source；
  重跑全量重导，幂等由写模型保证（event 按 uuid upsert、user 按 `_ts` 守卫）；
  不建 `_tango_fileupload` 集合。
- backfill **有 checkpoint**（`_backfill_progress`，逐页 flush，同 runID 续跑）。
- 公开 `client/` SDK 新增两面，一律经 Engine 中转：`UploadFile` / `RunBackfill`。

### 能力矩阵（v1.6 全部新增入口）

| 面 | 新增 |
| --- | --- |
| Engine（`internal/role/api`） | `UploadFile` / `RunBackfill` |
| client/ 公开 SDK | 同上两面（经 Engine 中转，守 import 边界） |
| cli | `function=uploadfile` / `function=backfill` |
| daemon / gateway | 不动 |

## 3. v1.6.0 — uploadfile（本地存量文件一次性导入）

**需求**：把一批已落盘的存量日志文件（glob 匹配）一次性灌入上报链路，读完即止。
与既有能力的边界：tailer=常驻追新增；cli `upload`=stdin；uploadfile=存量文件、有限。

**设计**：

- `internal/source/uploadfile` 实现 `source.Source`：glob 匹配文件 → 逐文件从头读到
  EOF → 发完关 channel；ctx 取消即提前关闭。行读取语义对齐 tailer（maxLineBytes）。
  无 dao 依赖（source 层保持干净），无新集合。
- 配置 `source.uploadfile.*`：`logPattern` / `maxLineBytes`（键=包路径惯例）。
- source 门面新增 `NewUploadFile(cfg)`，对齐 `NewLines` / `NewReader` / `NewTailer`。
- `Engine.UploadFile(ctx)` = 构造 source → 复用 `Engine.Run`（薄封装）。
- cli `role.cli.function=uploadfile`（config `Validate` 同步增加）+ client `UploadFile` 面。

**验收**：

- source 单测：glob / 行边界 / maxLineBytes / ctx 取消 / 空匹配。
- 集成：喂引擎写真实 DocumentDB；**重复导入幂等断言**（重跑结果一致）。
- 文档（arch/usage/config）+ 示例 `examples/config/cli/cli.uploadfile.{min,max}.{yaml,json}`。
- 门禁全绿（gofmt/vet/全量 test，连真实 DocumentDB）→ 合入 v1.6 → tag `v1.6.0`。

## 4. v1.6.1 — backfill（TA OpenAPI 历史回填，新域 `internal/backfill`）

**需求**：从 ThinkingData OpenAPI 按日期范围或显式 SQL 拉取历史数据
（submit→poll→paginate），事件表与用户表两路写入 MongoDB/DocumentDB，断点可续。

**设计**：

- 事件表行 → `rowdecode.EncodeRowAsJSONLine`（TA 列式行→TA JSON 行）→ **每页喂
  `Engine.Upload`**（解析/过滤/identity/DocumentDB 安全写全复用）。页驱动使 checkpoint
  语义干净，故**不兑现** `source/taapi` 占位（`Source` 接口无分页回报通道）。
- 用户表行 → `internal/dao/store` 新增 `UserSnapshotWriteModel(userID, doc, skipExisting)`
  （普通 `$set`/`$setOnInsert` upsert，**无聚合管道**）直走 `BulkWrite`，dao 门面重导出。
- 移植 v1.0 参照源并按 driver v2 重写：`client.go`+`httpclient.go`（APIError /
  ErrTaskExpired / proxy）、`ndjson.go`、`rowdecode.go`、`sqlbuilder`、
  `checkpoint.go`（`_backfill_progress`：Run / DayProgress / SQLSignature / initDays / resume）。
- `runner.go`/`executor.go` 接线：`daomongo.ConnectMongo` + `dao.Store`/`Identity`；
  弃用 v1.0 的 x/term ProgressBar，改日志周期进度；stats 内嵌 `process.Counters`。
- 配置 `backfill.*`：apiBaseURL / token / proxy / projectID / table / partDateRange /
  eventTimeRange / sql / pageSize / pollInterval / pollTimeout / pageRetries / limit /
  schemaPrefix / runID / progressCollection / forceSkip / skipLocalFilter +
  filter(include/exclude) + `buildDaySQL` / `backfillWhere` / `effectivePageSize`。
- 依赖：stdlib net/http + `golang.org/x/net/proxy`（由间接转直接），不引入重依赖。
- 入口：`Engine.RunBackfill(ctx)` + cli `function=backfill`（day-range 或显式 SQL）+
  client `RunBackfill` 面。**同步、跑到完成、进程内**（无 worker、无队列）。

**验收**：

- 单元：sqlbuilder / ndjson / rowdecode / checkpoint 状态机。
- 集成：`httptest` mock TA OpenAPI 三端点 + 真实 DocumentDB 临时库；事件+用户两路；
  中断后 resume。
- 文档 + 示例 `examples/config/cli/cli.backfill.{min,max}.{yaml,json}`。
- 门禁全绿 → 合入 v1.6 → tag `v1.6.1`。

## 5. 跨阶段约束

- 恢复集合：`_backfill_progress`（`_tango_config` 已由 cfgsync 在用；`_tango_fileupload`
  **不恢复**）。本轮**不**引入 `_tango_tasks` / `_tango_instances`（随 taskqueue+worker 回退）。
- DocumentDB 三红线：update 一律普通操作符（无聚合管道）；判错只认数字 code；
  每阶段集成测试连真实 DocumentDB（`TANGO_TEST_MONGO_URI` 机制现成）。
- client/ 新面一律经 `internal/role/api` Engine 中转，import 边界测试卡守。
- 每阶段交付：配置（键=包路径）+ 真实 DocumentDB 测试 + 文档（arch/usage/config）+
  示例（max 仅写实际用到的段）；分支 → 合 v1.6 → tag。

## 6. 不在本轮范围

- **taskqueue + worker 控制面（原 v1.6.2，已回退）**：backfill 的「发布任务 → 独立 worker
  异步消费」控制面（`internal/dao/taskqueue` + `role.mode=worker` + gateway
  `POST /publish/backfill` + cli `function=publish` + `Engine.PublishBackfillTask` +
  `_tango_tasks` / `_tango_instances`）已从 v1.6 移除。异步化方向改由 v1.7 独立探索。
- 同步执行的 `POST /backfill`。
- gateway / daemon 的 uploadfile 入口（仅 cli + api）。
- uploadfile 的 checkpoint / 断点续传与 `_tango_fileupload` 集合。
- `/ingest` 接口（被 `/upload` 取代）。
- `source/taapi` 占位的兑现（backfill 走页驱动，不经 Source 抽象）。
