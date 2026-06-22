# Tango v1.5 daemon 漏日志待修清单（log-loss TODO）

> 来源：2026-06-22 对 `v1.5` 分支 daemon 上传链路的全链路漏日志审计
> （多智能体 finder + 对抗式验证：41 候选 → 29 确认真丢失 → 去重为下列根因）。
> 与 [`../fix_todo.md`](../fix_todo.md)（fd 泄漏专项）互补：那张单是 fd 占用，这张是“事件丢失”。
> 用法同其它任务单：做完一条划掉（`~~...~~` + `[x]`），根因彻底消除后删行。
>
> **设计前提（用户 2026-06-22 拍板）**：tailer 读取位点只存内存、**不落地 checkpoint**；
> 进程重启从头重读**可容忍**（事件按 `#uuid` 去重；`user_add`/`user_append` 过计也可容忍）。
> 因此“加断点续传”不在选项内——下列修复都不依赖持久化。

## 判定基线（“真丢失” vs “重复/有意丢弃”）

- 事件 `track` 按 `#uuid` `$setOnInsert` 去重（解析器强制 `#uuid` 非空）→ 重读安全。
- `user_set` / `user_setOnce` / `user_unset` / `user_uniq_append` / `user_del` 幂等。
- `user_add`($inc) / `user_append`($push) 重读会**过计**（可容忍，非丢失）。
- **真丢失** = 读到/缓冲过的行最终没入库，且不会被重读（文件已轮转/删除）。

---

## Critical

- [x] ~~**B1　默认 hybrid 从 EOF 起读**~~ — 已修并推 `v1.5`（`9a1534f`）。
      `tailFileHybrid` 首开 `lastSize=fi.Size()` → 跳过新文件已有内容，并把所有在途丢弃钉成永久丢失。
      改为从头读（`offset 0`），与 poll/event 对齐；新增 `TestLifecycle_C0_PreexistingContentReadFromHead`
      三模式断言。`internal/source/tailer/tailer.go` `tailFileHybrid`。

- [ ] **B2　worker 停机不排空自身 channel 缓冲** — `internal/process/pipeline/worker.go:146`
      `ctx.Done()` 分支只 `flush` 内存 batch 就 `return`，每 worker 的 `workerCh`（深 `BatchSize*2`，默认 2000）
      里未读的行随 goroutine 退出丢弃。**修**：优雅停机改为“排空 channel 直到 close 再退”（走 `!ok` 分支），
      不让 `ctx.Done()` 抄近路；或先停 tailer→drain `lineCh`→drain 各 `workerCh`→flush 的有序收口。

- [ ] **B5　写失败丢整批** — `internal/process/pipeline/worker.go:156` `flushBatch` 在
      `store.BulkWrite` 重试超 `MaxElapsedTime`（`internal/dao/store/store.go:124`，默认 10s）返回 err 后，
      只 `OnWriteError()`+`b.Reset()`（worker.go:167），**整批静默丢弃**，不 dead-letter、不停机、不延迟重试。
      高延迟后端/停机末次 flush 时尤甚（H4 写侧根因）。**修**：失败批转 `dead_letter` 或阻塞重试到成功/停机，
      绝不 `Reset` 丢弃；并考虑写错持续时主动告警。

## High

- [ ] **B3　dispatch 停机丢手里那行 + 弃 lineCh** — `internal/process/pipeline/dispatch.go:45-46`
      阻塞回退 `select { workerChs[idx]<-line ; <-ctx.Done(): return }`，`ctx` 取消时丢掉正持有的 `line`，
      并放弃 `for range lineCh` 剩余项。**修**：与 B2 一起做有序收口；停机时先把 `lineCh`/手里行排空再退。

- [ ] **B4　tailer 停机丢 in-hand 行 + 2000 深 out 缓冲无人排空** —
      `internal/source/tailer/tailer.go:275` `out` 缓冲 2000；各 per-file goroutine 在
      `out<-line` 处遇 `ctx.Done()` 丢手里行（poll `:450`、event `:583`、hybrid `:722`、drainByPoll `:775`）。
      停机时 `out` 缓冲整段无人下游消费。**修**：随 B2/B3 的有序收口；停机让下游把 `out` 排空后再关。

- [ ] **B6　filter 求值出错→合法行当 filtered 静默丢**（确定性、与模式无关，重读也再触发）—
      `internal/process/core/processor.go:96-101`：`flt.Keep` 对 include 规则求值出错时按“未匹配”返回 `false`
      （`internal/parser/filter/filter.go` `Keep`），processor 据此 `OnFiltered` 丢弃。**修**：求值错误的行应保留
      或转 `dead_letter`，不可当成被过滤静默丢。

- [ ] **B7　轮转/删除走的文件不回读（无 checkpoint）** — daemon 宕机/重启期间被改名移走（不再匹配 glob）
      或被删的文件永不回读。**注**：按拍板**不做 checkpoint**，故“断点续传”不修；**残留真风险见下方滚动实测**——
      消费跟不上删除型滚动源时，文件在被读到前就被删 = 永久丢失。**修方向**：背压感知 + 消费滞后/删除-未读告警
      （而非持久化）。

- [ ] **B8　fd 看门狗自重启继承全部停机丢失** — `internal/role/daemon/report.go:284` `triggerRestart()`
      取消 `runCtx`，走的就是 B2/B3/B4 的停机丢数据路径，且**自动、无人值守**。**修**：依赖 B2/B3/B4 收口；
      自重启前确保 drain 完成。

## Medium

- [ ] **B9　poll 轮转丢旧 inode 未读尾** — `internal/source/tailer/tailer.go` `readFollowFile` 检出
      `os.SameFile` 变化即 `return` 并从新文件 0 重开，旧 inode 末次 EOF 后追加的尾巴未读即丢。

- [ ] **B10　超 `maxLineBytes`（默认 10MB）的行卡死整文件** — poll 的 `readFollowFile`
      `scanner.Err()`（`bufio.ErrTooLong`）返回 → `tailFile` 重试又从 0 重开 → 撞同一超长行 → 无限循环，
      该文件后续内容永不摄入。**修**：跳过超长行（转 `dead_letter`）并推进 offset，不要原地死循环。

- [ ] **B11　rescanInterval（默认 30s）内的瞬时文件从不被发现** —
      `internal/source/tailer/tailer.go` `run`/`scanAndTail` 只在 ticker 触发时扫描；
      在两次扫描间“创建+轮转/删除”的短命文件从未被 tail。**修**：缩短 rescan 或事件驱动发现。

- [ ] **B12　poll truncate 后越过旧 offset 重写跳过字节** — 仅靠 `fi.Size()<pos` 判 truncate；
      truncate 后立即重写到超过旧 offset，两次轮询间 size≥pos → 不回绕 → 跳过重写的字节。

- [ ] **B13　纯 event 模式 stall + stopTail 丢缓冲（仅 `tailMode=event`）** —
      `tailFileEvent`（`:540`）无 stall 检测，`sendOnlyIfEmpty` 通知丢失下可停滞；`stopTail`（`:525`）
      排空并**丢弃** `tt.Lines`，而纯 event 模式**无 drainByPoll 回补** → 停机/换 inode 时已缓冲未转发的行丢。
      **修**：给 event 模式加 stall 检测 + 缺口回补，或文档明确仅 `poll`/`hybrid` 用于生产。

## 附：条件触发

- [ ] **B14　cfgsync 网关致命错→永久 fail-closed 不摄入**（仅 `cfgsync.enabled=true`）—
      `internal/role/daemon/report.go` 的 pull-before-ingest gate + `startCfgsync`：watcher 致命错误
      （如不支持的拓扑跑 changestream）时 `Ready()` 永不触发 → daemon 永远等待不摄入，而文件在底下轮转走。
      **修**：watcher 致命错时要么 fail-open（带告警）要么让 Pod 退出由编排器重建，别静默空转。

---

## 滚动入库实测结论（2026-06-22）+ 运维项

真实 daemon 进程 + Docker mongo:6，按生产形态滚动（`log.<ts>`、固定大小、最多留 5 删最旧）限速写
唯一-uuid 行，核对入库去重数。harness 在 [`../../test/logloss/`](../../test/logloss/)。

- **丢失 100% 在读侧**（每次 `total_lines==入库数`、写错误/重试均 0）：文件在被 tailer 读到前就被滚动删除。
  **三模式表现一致**（瓶颈在 tailer 下游的 mongo 写吞吐，与 tail 模式无关）。
- 是**吞吐竞争**：`消费吞吐 < 持续写速率` 且持续到 churn 完 `KEEP×文件大小` 缓冲就开始丢。
  实测窗口：10MB×keep5，写 8MB/s 丢 49% / 2MB/s 丢 32% / 1MB/s 丢 13% / 0.4MB/s（低于消费上限）零丢失。
- 用户真实 100MB×5：缓冲 500MB，可吸收 `500MB÷写速率` 时长的消费停顿；只要 ingest 平均 ≥ 写速率即零丢失。

运维 / 增强（非 v1.5 代码 bug，但属同一风险面）：

- [ ] 保证 Mongo/DocumentDB 写吞吐 > 峰值日志写速率（可调 `pipeline.batchSize`/`batchWorkers`）。
- [ ] 监控消费滞后：daemon 周期 stats 的 `lines/s` vs 写速率；或最旧 `log.*` 文件年龄 vs 删除节奏。
- [ ] 缓冲按最坏 ingest 停顿时长来定（加大每文件大小或 `keep` 数）。
- [ ] （增强）daemon 暴露“消费滞后 / 删除-未读”告警指标——tango 当前对“消费跟不上删除型滚动源”
      **无背压、无告警**，文件删后数据不可恢复。
