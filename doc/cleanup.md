# 代码审查清理清单（Cleanup TODO）

来源：2026-06-04 全工程只读审查（覆盖 `internal/`、`config/`、`client/`、`main.go`、`doc/`、`examples/`）。
构建 / vet 当前是绿的；下列均为**死代码、陈旧注释、设计不自洽**问题，不影响编译。

约定：每完成一项，把该行的复选框打勾并对条目文字加删除线，即
`- [ ] 描述` → `- [x] ~~描述~~`，使已完成项一目了然。

## 高优先级 — 死代码 / 未清理干净的 backfill·ingest 残留

- [x] ~~**#1** 删除空目录 `internal/process/ingest/`（无任何 `.go`，`todo.md:267` 已列删除）。~~
- [x] ~~**#2** 删除无人调用的 `dao/store/writemodel.go:329 UserSnapshotWriteModel`（全工程零引用，含测试）。~~
- [x] ~~**#3** 处置恒为 false 的 `core.WriteOptions.ForceSkipExisting`：整组删除（`WriteOptions` 类型 + `process.New`/各 `NewUploader`/`NewProcessor` 的 `opts` 参数 + `processor.go` 死分支 + `EventWriteModelSkipExisting` 的 `dao` 重导出与 `store` 实现）。~~
- [x] ~~**#4** 清理指向不存在符号的注释：`core/counters.go`（原 `backfill.Stats`）、`core/processor_test.go`（原 `ingest/pipeline/backfill`）；`processor.go`/`writemodel.go` 的 backfill 注释随 #2/#3 一并移除。~~

## 中优先级 — 只写不读的状态 / 无人调用的导出 API

- [x] ~~**#5** `IdentityResolver.distinctBound`（`identity.go:60`）只写不读——删除该 `sync.Map` 及 3 处 `.Store` 写入。~~
- [x] ~~**#6** 删除无人调用的 `IdentityResolver.MappingCollection()`（`identity.go:91`）。~~
- [x] ~~**#7** 重试统计不可见：在 daemon `logFinalStats` 加 `total_retries`（`d.dao.Store.Stats().TotalRetries()`），使该 API 真正被生产消费。~~

## 中优先级 — 设计 / 文档不自洽

- [x] ~~**#8** 对齐"用户亲和顺序"语义：修正 `report.go` 的 `Run` 文档，说明亲和是 best-effort（背压时会溢出到其他 worker），正确性由写模型 `_ts` 条件更新兜底。~~
- [x] ~~**#9** 身份缓存无界增长：在 `identity.go` 缓存字段处补充内存增长权衡说明（随 distinct 用户数线性增长；有界用户群可接受；如需 cap，因 Mongo 为真相源，淘汰安全）。~~

## 低优先级 — 改名后的陈旧注释 / 标识符（"runtime"/"generic" 残留）

- [x] ~~**#10** `dao/mongo/config.go` 注释 "runtime.mongo.* keys" → `dao.mongo.*`。~~
- [x] ~~**#11** `dao/mongo/mongo.go` 错误信息前缀 `"runtime: ..."` → `mongo:`。~~
- [x] ~~**#12** `config/loader.go` 注释举例 `"generic.mongo.uri"` → `dao.mongo.uri`。~~
- [x] ~~**#13** `pipeline/config.go` 注释删去不存在的 `role.gateway.upload.pipeline`，仅保留 `process.pipeline`。~~

## 低优先级 — 其它细节

- [x] ~~**#14** 统一"单行最大字节"默认：`tailer.defaultMaxLineSize` 改为 10 MiB，`tailer.Config.ApplyDefaults` 改用该常量，两路径一致。~~
- [x] ~~**#15** `identity.go` 两处 `err == mongo.ErrNoDocuments` → `errors.Is(...)`（并加 `errors` 导入）。~~
- [x] ~~**#16** `mongo.ConnectMongo` 加 `client.Ping`，Mongo 不可达时在连接处即失败（受 ServerSelectionTimeout 约束），并更新文档措辞。~~
- [x] ~~**#17** `atomicCreateForDistinctID` 注释改为如实描述：胜出者是 `findByDistinctID` 返回的文档，并非 "smaller user_id" 规则。~~
