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

## SQL：由拷贝改为依赖 mongosql（v1.4；master 与 v1.4 同步）

> mongosql 已在 master(`adb703b`) 加 `driver.New(client,db)` 注入构造器 + Close 所有权（支持注入连接与 URI 两种）。
> tango 删除 `internal/dao/sql` 的拷贝（translator 全树 + schema/codec），改为薄封装依赖 mongosql；vitess 退为间接依赖。

- [x] ~~mongosql：`driver.New(client,db)` 注入构造器 + Close 所有权；合入 master(`adb703b`) 并推送。~~
- [x] ~~tango `go get github.com/aura-studio/mongosql@adb703b`（pseudo v0.0.0-…-adb703bff614）+ `go mod tidy`（vitess 转间接）。~~
- [x] ~~删除拷贝：`internal/dao/sql/{schema.go,codec.go,translator/**,.DS_Store}`；`sql.go` 改薄封装（`New(res)`→`mongosql.New(client,db)`，`Exec`）；新增 `result.go`（`Result` 镜像 + `MarshalEJSON` + `fromMongosql`）。dao 门面/gateway/cli 不变。~~
- [x] ~~重写 `internal/dao/sql/sql_test.go`（New(nil) + 集成走薄封装，throwaway db 隔离）。~~
- [x] ~~EC2 全绿（go build/vet/全量 test，连真实 DocumentDB）。~~
- [ ] master 同步到 v1.4（保持一致）。

## Phase 1 — Backfill / TA-OpenAPI SQL 导入（新 domain `internal/backfill`）

> 设计：事件表行 → `rowdecode.EncodeRowAsJSONLine`（TA 列式行→TA JSON 行）→ 复用 `api.Engine.Run(source)`
> （解析/过滤/identity/DocumentDB 安全写）；用户表行 → 新增 DocumentDB 安全的 `UserSnapshotWriteModel` 直写
> （v1.3 无此模型）。`checkpoint` 由 driver v1 转 v2。弃用 v1.0 的 x/term `ProgressBar`，改用日志周期进度。

- [x] ~~移植 TA OpenAPI client `client.go`+`httpclient.go`（submit→poll→paginate；APIError/ErrTaskExpired/proxy）。~~
- [x] ~~移植 `ndjson.go`（NDJSON 流解码）+ `rowdecode.go`（`EncodeRowAsJSONLine`）。~~
- [x] ~~加 `golang.org/x/net`(proxy) 依赖；`go build ./internal/backfill/...` 通过（EC2）。~~
- [ ] `internal/backfill/config.go`：`backfill.*`（apiBaseURL/token/proxy/projectID/table/partDateRange/eventTimeRange/sql/pageSize/pollInterval/pollTimeout/pageRetries/limit/schemaPrefix/runID/progressCollection/forceSkip/skipLocalFilter）+ filter(include/exclude) + `buildDaySQL`/`backfillWhere`/`effectivePageSize` + FromTree/defaults/validate。
- [ ] `internal/dao/store` 新增 `UserSnapshotWriteModel(userID, doc, skipExisting)`（DocumentDB 安全：普通 `$set`/`$setOnInsert` upsert，无 pipeline）+ `dao` 门面重导出。
- [ ] 移植 `checkpoint.go` 到 driver v2（`_backfill_progress`：Run/DayProgress/SQLSignature/initDays/resume）。
- [ ] `runner.go`/`executor.go` 重写接线：`daomongo.ConnectMongo` + `dao.Store`/`Identity`；事件表页→喂 `api.Engine.Run`；用户表页→`UserSnapshotWriteModel`+`BulkWrite`；日志周期进度（替代 ProgressBar）；stats 内嵌 `process.Counters`。
- [ ] 入口：`role.cli.function=backfill`（`cli.RunBackfill`：按 day-range 或显式 SQL 跑）+ `role.go` 派发 + cli config `Validate` 增加 `backfill`。
- [ ] 测试：单元（sqlbuilder/ndjson/rowdecode/checkpoint 状态机）+ 集成（`httptest` mock TA OpenAPI 三端点，写真实 DocumentDB 临时库；事件+用户两路）。
- [ ] 文档/示例（arch §5.4 + usage + config + `examples/config/cli/cli.backfill.{min,max}.{yaml,json}`）。
- [ ] EC2 全绿（gofmt/vet/全量 test）→ 合入 v1.4 → tag `v1.4.0`。

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
