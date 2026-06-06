# TODO — v1.4 开发线（加回 v1.0 被删特性，按 v1.3 架构重设）

本文件只保留尚未完成的任务。完成后改成 `- [x] ~~任务内容~~`，下次整理时删除。
（v1.3 已完成项——Driver v2 / Mongo Data API(ejson) / SQL Data API(dao/sql) / cli 配置——见 git 历史。）

> 目标：把 `doc/diff.md` 记录的 v1.0→v1.1 删除的控制面特性（backfill/TA-OpenAPI SQL 导入、
> taskqueue+worker、remote config、operator/控制面接口、文件断点续传）**按 v1.3 架构重新引入**到
> **v1.4 开发线**。不回滚 v1.0；复用现有 dao.Store 写模型（DocumentDB 安全）、source/process/api 引擎、
> parser.Filter 原子热替换。公开 Go SDK 本轮不做。源码参照 `git show v1.0:<path>`（tag 8bc899b）。

## Phase 0 — v1.4 线建立

- [x] ~~从 master(`221166d`) 建 `v1.4` 分支。~~
- [x] ~~用本清单替换 `doc/todo.md` 并提交（之后逐行划掉）。~~

## Phase 1 — Backfill / TA-OpenAPI SQL 导入（新 domain `internal/backfill`）

- [ ] 移植 TA OpenAPI client：`client.go`+`httpclient.go`（submit-SQL→awaitFinished 轮询→分页；APIError/ErrTaskExpired/proxy）。
- [ ] 移植 `sqlbuilder.go`（buildDaySQL）、`ndjson.go`+`rowdecode.go`（TA 列式行→文档）。
- [ ] 移植 `checkpoint.go`+`progress.go`（`_backfill_progress`，day/run 断点续传）与 `runner.go`/`executor.go` 编排。
- [ ] 写层重设：用当前 `dao.EventWriteModel`/`UserWriteModel`+`BulkWrite`（DocumentDB 安全 `_ts` 守卫）替换 v1.0 的 event_ingester/user_writer 直写；identity 用 `dao.Store.Identity()`。
- [ ] `internal/backfill/config.go`：`backfill.*`（apiBaseURL/token/proxy/sql/时间范围/progressCollection/pageSize/并发）+ FromTree/defaults/validate。
- [ ] 入口（一次性）：`role.cli.function=backfill`（`cli.RunBackfill`）→ `backfill.Runner`。
- [ ] 测试：单元（sqlbuilder/ndjson/rowdecode/checkpoint 状态机）+ 集成（httptest mock TA OpenAPI，写真实 DocumentDB 临时库）。
- [ ] 文档/示例（arch/usage/config + `examples/config/backfill/*`）+ EC2 全绿。
- [ ] 合入 v1.4，tag `v1.4.0`。

## Phase 2 — TaskQueue + Worker 控制面

- [ ] `internal/dao/taskqueue`（dao 子包，dao 门面中转）：`Task`/`TaskType`/`TaskStatus` + publish/claim(lease)/heartbeat/reap/complete，集合 `_tango_tasks`(+`_tango_instances`)。
- [ ] `internal/role/worker`（新 `role.mode=worker`）：claim 循环 + 按 TaskType 分发 handler（backfill / report-sync / sql）；`role.worker.*` 配置；接入 `role.Get`/`role.Config` + 信号处理/panic recover。
- [ ] publish 入口：gateway `POST /publish/{backfill,sql,report-sync}` + cli `function=publish`。
- [ ] 测试：taskqueue claim/lease/reap 并发（真实 DocumentDB）；worker 跑通已发布 backfill 任务；`/publish/*` httptest。
- [ ] 文档/示例 + EC2 全绿；合入 v1.4，tag `v1.4.1`。

## Phase 3 — Remote config + 文件断点续传 + operator/控制面接口

- [ ] `internal/remoteconfig`（移植 Fetch/Merge/FilterChanged，集合 `_tango_config`）：轮询热替换 `parser.Filter()` holder；接入 daemon+gateway；`remoteconfig.*`（enabled/documentID/pollInterval）。
- [ ] `internal/source/filebatch`（新 `source.Source`）：glob 文件 + `_tango_fileupload` 断点续传 → 喂 process 流水线；入口 cli `function=fileupload`（和/或 gateway 文件模式）；`source.filebatch.*`。
- [ ] 补齐 operator cli functions（upload|ejson|sql|backfill|publish|fileupload）与 gateway 控制面路由（`/publish/*`、`/backfill`）；确认 `/ingest` 仍不提供。
- [ ] 测试：remoteconfig 运行中热替换 filter；filebatch 中断后续传；控制面接口 httptest —— 全连真实 DocumentDB。
- [ ] 文档/示例 + EC2 全绿；合入 v1.4，tag `v1.4.2`。

## 不在本轮范围

- 公开 Go SDK（`client/`）：延后；将来用薄封装包装 `internal/role/api` + 新入口，不暴露 internal。
- `/ingest` 接口（被 `/upload` 取代）。

## 跨阶段约定

- 恢复集合：`_backfill_progress`、`_tango_tasks`、`_tango_instances`、`_tango_config`、`_tango_fileupload`。
- 每阶段交付：配置（键=包路径）+ 真实 DocumentDB 测试 + 文档（arch/usage/config）+ 示例（max 仅写实际用到的段）+ todo 划掉；分支→合 v1.4→tag。
- 依赖：backfill 仅用 stdlib net/http（+ 现有 logrus），预期不引入重依赖；保持 go 1.26.2。
