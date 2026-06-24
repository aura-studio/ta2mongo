# tango v1.7 需求文档

> 状态：**2026-06-24 更正**。v1.7 现含的 feature = v1.6.1 的两块（uploadfile + backfill）
> **加上**合入的 v1.5.11 daemon 日志可靠性修复。原计划的 **B 方案（Step Functions + Lambda
> 分片 backfill，v1.7.1）已回退、移出 v1.7 范围**（见 §4）。分支顶端为 `v1.7.0`。

## 1. 背景与目标

v1.7 自 **v1.6.1**（`283655b`，uploadfile + backfill；不含已回退的 v1.6.2 taskqueue+worker）
起分支，目标是在该 feature 基线上**合入 v1.5 线的 daemon 日志可靠性修复**，得到一个
"v1.6 功能 + v1.5 可靠性"的收敛基线。

> 异步化 backfill（让回填能在 Lambda/Step Functions 上跑）曾作为 v1.7.1 的 B 方案落地，
> 现已回退（见 §4）。本文只描述 v1.7 当前实际包含的 feature。

基线前提：go 钉 **1.25.5**；mongo **driver v2**；mongosql v1.0.1 不动；
所有写路径 DocumentDB 兼容。

## 2. v1.7 包含的 feature

### 2.1 自 v1.6.1 继承（uploadfile + backfill）

- **uploadfile**（v1.6.0）——本地存量文件一次性导入：`internal/source/uploadfile`
  实现有限 `source.Source`，glob 发现 → 逐文件读到 EOF；无 checkpoint、无新集合；
  入口 cli `function=uploadfile` / `Engine.UploadFile` / client `UploadFile`。
- **backfill**（v1.6.1）——TA OpenAPI 历史回填：`internal/backfill` 域，submit→poll→
  paginate，事件表经上报链路、用户表快照 upsert，`_backfill_progress` 逐页 checkpoint
  续跑；**同步、跑到完成、进程内**（无 worker、无队列）。入口 cli `function=backfill` /
  `Engine.RunBackfill` / client `RunBackfill`。

详见 [v1.6 需求](../v1.6/requirements.md) §3 / §4。

### 2.2 合入 v1.5.11 daemon 日志可靠性修复

v1.7.0 是一次 `merge(v1.7)`：把 v1.5 线（tag `v1.5.11`，`6271c8d`）的 daemon log-loss
remediation 合进 v1.7 基线，三处关键修复：

- **B5 — pipeline 背压重试**：bulk-write 失败时带背压重试，不再丢整批
  （`internal/process/pipeline/worker.go`）。
- **B6 — filter fail-open**：filter 求值出错时放行该记录，而非静默丢弃
  （`internal/parser/filter/filter.go`）。
- **tailer 从头读新文件**：hybrid 模式下新发现的文件从头读、而非 EOF，避免漏采
  （`internal/source/tailer/tailer.go`）。
- 附带 `test/logloss/*` 日志丢失/重启恢复集成测试脚手架。

## 3. 验收

- 合并保持干净（v1.5 侧改动文件与 v1.6 侧零重叠：filter / pipeline-worker / tailer
  vs uploadfile / backfill）。
- 门禁全绿（gofmt / vet / 全量 test，连真实 DocumentDB）。
- 分支 → tag `v1.7.0`。

## 4. 不在本轮范围

- **B 方案：Step Functions + Lambda 分片 backfill（原 v1.7.1，已回退）**。该方案曾实现：
  backfill 执行核心加 deadline 协作让出（`RunBackfillSlice`）、`internal/sfnpublish`
  发布器（`StartExecution`，`sfn.*` 配置）、`Engine.PublishBackfill` / client
  `PublishBackfill` / cli `function=publish`、`cmd/lambda` handler、Step Functions ASL
  与部署文档。整套已从 v1.7 回退、移出范围；若日后重启，从 v1.7.0 基线另起。
- v1.6.2 的 taskqueue + worker 常驻控制面（已在 v1.6 回退，见 v1.6 需求 §6）。
- 同步执行的 `POST /backfill`。
