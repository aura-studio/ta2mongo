# TODO — cfgsync（运行时动态配置同步）

> 本文件细化 `doc/todo.md` Phase 3 里原 `internal/remoteconfig` 那一行，并把它**改名为 `cfgsync`**
> （与 `cfgtree` 一静一动成对：`cfgtree` = 静态配置载体，`cfgsync` = 运行时动态配置同步）。
> 属于 v1.4 开发线的 Phase 3（v1.4.2）。完成的项改成 `- [x] ~~...~~`，下次整理时删除。

## 定位与设计

- **顶层领域 `internal/cfgsync`，不是角色**。它是一个**可嵌入的 Watcher/Service**（与 `role/api` 的 `api.Engine`
  同类），被**长驻且持有 live filter 的角色**内嵌：`daemon`（tailer→pipeline）与 `gateway`（/upload）。
  `cli`（一次性）与 `api`（库）不接入；**`worker` 与 taskqueue 完全无关，不接入**。
- **职责**：盯中心配置文档（集合 `_tango_config`）→ 运行中**热替换上报 filter**（`parser.Filter()` 的 `*filter.Holder`，
  原子无锁替换）。本身**filter 无关**：只负责「取文档 + 变更检测 + 调用 `onChange(doc)` 回调」，
  「如何解释与应用」由内嵌它的角色注入回调（解析 filter.include/exclude → `parser.SwapFilter`）。
- **约定遵循**：领域间只经根包（cfgsync → `dao`/`parser` 根包，**不碰** `dao/ejson`、`parser/filter` 子包）；
  包名 = 配置键路径（`cfgsync.*`）；`FromTree` 自取子树；DocumentDB 安全（findOne / watch，无 pipeline）。

## 安全模型（本组件的核心，务必整套实现）

目标不是「绝对一致地瞬时全集群生效」（分布式里物理上做不到），而是
**有界陈旧 + 自愈 + 不回退 + 坏配置打不挂**。由以下叠加保证：

| 机制 | 作用 |
|---|---|
| **启动拉取** | 进程启动先做一次全量读+应用 → 收敛停机期间错过的更新 |
| **change stream**（可选 backend） | 运行中低延迟推送 |
| **定时拉取** | 兜底：断流/丢事件/超保留窗口/bug → 保证最终收敛，最坏陈旧 = 一个轮询周期 |
| **单调版本守卫** | 只接受 `version` 更大的文档，丢弃更旧/重放 → **防回退** |
| **校验后再换 + 保留上一版** | 新 filter 编译/校验失败 → 不替换、保留 last-good、记日志+计数 → **坏配置打不挂** |
| **先订阅后快照** | change stream 模式先订阅（记 resume token / `startAtOperationTime`）再全量读 → 消除 read↔subscribe 的 TOCTOU 缝 |
| **消费者边界** | 仅 daemon/gateway 订阅（逐行过滤可中途换）；**有界任务（backfill）不订阅**，用一致快照 |

## 覆盖范围（cfgsync 能同步哪些配置）

**传输范围 vs 生效范围**：cfgsync 的传输面（一份文档 + 版本守卫 + 变更检测）理论上能装**任意子树**，
甚至整棵 `cfgtree`；但**能否运行时生效**只取决于目标有没有「live-reconfigure applier」。判据（两条都要满足）：
（a）该配置经**原子 indirection** 在数据路径上被读；（b）**不改资源身份/生命周期**（连接、监听 socket、角色、goroutine 拓扑）。

据此把配置分三档，并以**显式 allowlist + applier 注册表**（`map[子树]applyFn`）落地，
**publish 与 apply 两端都按 allowlist 校验**：

| 档 | 键 | 动态生效 |
|---|---|---|
| **可覆盖（默认 allowlist）** | `parser.filter.*`（Holder 原子热替换，金标准）；可选 `logging.level`（logrus 运行时 SetLevel） | ✅ |
| **技术可做但默认不放**（结构性/收益低） | `process.batchSize`/`pipeline.flushInterval`（需改成每批原子读）；`process.mode`/`pipeline.workers`/`channelBuffer`（换策略/拓扑＝重建 Uploader，结构性）；`source.tailer.*`（多为启动烘焙）；`dao.store.maxElapsedTime` | ⚠️ 默认 ❌ |
| **绝不覆盖**（资源身份/生命周期，只能重启/重部署） | `dao.mongo.*`（连接池；cfgsync 自己也靠这条连接读 → 自举悖论）；`role.mode`（进程身份）；`role.gateway.addr`（启动绑定 socket）；`cfgsync.*` 自身（自举 + 防自锁） | ❌ |

**原则**：默认动态面只放 `parser.filter`（顶多加 `logging.level`）。不是别的做不到，而是
**远程能控越多、爆炸半径越大**——故意把动态面积压到最小，是安全边界，不是技术限制。
新增动态键 = 往注册表加一个「校验 + 原子 apply」函数，且必须满足上面两条判据。

---

## Phase A — 领域骨架 + 配置

- [x] ~~`internal/cfgsync/cfgsync.go`：包文档 + `Watcher`（持有 `*dao.Dao`、`*Config`、注入的 `onChange func(bson.M) error`）
  + `New(d *dao.Dao, cfg *Config, onChange func(bson.M) error) *Watcher` + `(*Watcher).Run(ctx) error`
  （选 backend → **启动拉取一次** → 进入 backend 循环；panic 由调用方 recover）。~~
- [x] ~~`internal/cfgsync/backend.go`：`Backend` 接口（`Run(ctx, observe func(bson.M) error) error`）+ `selectBackend(cfg)`。~~
- [x] ~~`internal/cfgsync/registry.go`：**applier 注册表** `map[subtree]func(bson.M) error`（默认只注册 `parser.filter`）。
  Watcher 把变更文档按子树路由到对应 applier；**不在 allowlist 的子树 → 拒绝 + 记 warn**（见「覆盖范围」）。
  这是 `onChange` 的泛化：单回调 → 有界、可审计的动态键集合。~~
- [x] ~~`internal/cfgsync/config.go`：`cfgsync.Config` + `FromTree(t)`/`ApplyDefaults`/`Validate`/`RegisterDefaults`。
  键：
  - `cfgsync.enabled`（bool，默认 `false`）
  - `cfgsync.backend`（`poll` | `changestream`，默认 `poll`）
  - `cfgsync.documentID`（`_tango_config` 文档 `_id`，默认 `"filter"`）
  - `cfgsync.pollInterval`（poll backend 轮询周期，默认 `5s`）
  - `cfgsync.reconcileInterval`（changestream backend 的兜底全量读周期，默认 `60s`）
  - `cfgsync.collection`（默认常量 `_tango_config`，一般不改）~~
- [x] ~~`config/` 注册 `cfgsync` 默认键（`RegisterDefaults`）；确认文件/env/flag 三途径同名可互换。~~

## Phase B — poll backend（默认，最稳，先落地）

- [x] ~~`internal/cfgsync/poll.go`：`pollBackend` 按 `pollInterval` 定时调
  `dao.EJSON(findOne, collection, {_id: documentID})`（**复用现有读门面，零新增 dao surface**）→ `observe(doc)`。~~
- [x] ~~`internal/cfgsync/fetch.go`：
  - `parseDoc`：从 EJSON 响应取出文档（`bson.M`）；缺失文档（未配置）视为 no-op（保留当前 filter）。
  - **单调版本守卫**：记 `lastVersion`，`doc.version <= lastVersion` 直接丢弃；只有更高版本才放行。
  - `changed`：仅当放行后才触发 `onChange`（避免无变更时重复 swap）。~~
- [x] ~~启动拉取：`Watcher.Run` 进入循环前先 `observe` 一次当前文档。~~

## Phase C — parser/dao 门面 + 接入 daemon/gateway

- [x] ~~**parser 根包门面**：新增 `(*parser.Parser).SwapFilter(include, exclude []string) error`
  （内部 `filter.Config{Include,Exclude}.Build()` → 成功才 `p.Filter().Store(newFilter)`；**编译失败返回 err、不替换**）。
  使 cfgsync/角色不直接 import `parser/filter`。~~
- [x] ~~`role/daemon`：`cfgsync.enabled` 时起 `cfgsync.Watcher` goroutine（panic recover，对齐 daemon 现有信号/统计 goroutine）；
  `onChange` = 从 `doc.filter.{include,exclude}` 解析 → `parser.SwapFilter`（失败保留 last-good、记 warn + 计数）。~~
- [x] ~~`role/gateway`：同上接入（gateway 也持有 live filter）。~~
- [x] ~~daemon/gateway 的 `FromTree` 各自裁出 `cfgsync` 子树（与 `dao`/`process`/`parser` 同级按枝叶取）。~~

> 交付到此即可用：poll backend + 启动拉取 + 版本守卫 + 校验后再换，已满足「有界陈旧 + 自愈 + 不回退 + 不打挂」。

## Phase D — changestream backend（可选，真·实时）

- [x] ~~**dao 根包门面**：新增 `(*Dao).Watch(ctx, collection string, pipeline mongo.Pipeline, opts...) (*mongo.ChangeStream, error)`
  （封装 `collection.Watch`；driver 类型 `*mongo.ChangeStream` 按设计不隐藏，与写模型返回 `mongo.WriteModel` 一致）。~~
- [x] ~~`internal/cfgsync/changestream.go`：`changeStreamBackend`
  - **先订阅后快照**：先 `dao.Watch`（记 `startAtOperationTime`/resume token）→ 再 `dao.EJSON` 全量读一次 → 增量 apply。
  - 增量事件经**同一条**版本守卫（与 poll 复用 `fetch.go`）。
  - **断流 / resume token 失效**（停机 > change stream 保留窗口，DocumentDB 默认 3h、可调 7d）→ 重订阅 + 全量读 fallback，不硬崩。
  - **兜底 reconcile**：即使在 changestream 模式，也按 `reconcileInterval` 跑一次低频全量读（补漏，自愈）。~~
- [x] ~~环境前置与降级：~~
  - DocumentDB：需先 `db.adminCommand({modifyChangeStreams, enable:true})`（文档写明）；**Elastic Cluster 不支持** change stream。
  - 普通 MongoDB：需副本集；**standalone mongod 无 change stream** → `backend=changestream` 时检测失败**清晰报错并提示改用 `poll`**（`changestream.go` Run 包装 subscribe 错误，不静默吞）。
  - 已在真实测试 DocumentDB（引擎 8.0.0、副本集 rs0）实测 change streams 可用（`watch()` 收到 insert 事件，含 fullDocument）。
  - 文档化于 `doc/arch.md` §5.4 与 `doc/config.md`/`doc/usage.md` 的 cfgsync 段。

## Phase E — 配置发布（gateway / cli / api 三面，对齐 upload/ejson/sql 模式）

> 读侧是 Watcher（embed 进 daemon/gateway），写侧是 Publish（被 gateway/cli/api 调用）；两侧都由 cfgsync 根包
> 拥有 `_tango_config` 文档 schema 与**单调 version** 语义。发布时**先校验（按 allowlist + 编译 filter）再写**——
> 坏配置在源头就挡掉（与 apply 端的 fail-to-last-good 形成两道闸）。

- [x] ~~`internal/cfgsync/publish.go`：`Publish(ctx, d *dao.Dao, doc bson.M) (version int64, err error)`：
  按 allowlist 校验子树 + 编译 filter（失败拒绝）→ 经 `dao.EJSON`（updateOne upsert，`$set` 内容 + `$inc:{version:1}`，
  DocumentDB 安全）原子写入 `_tango_config` → 返回新 version。~~
- [x] ~~**gateway（HTTP）**：`POST /config`（body = 配置文档）→ `cfgsync.Publish` → 返回 `{version}`；确认 `/ingest` 仍不提供。~~
- [x] ~~**cli（控制台）**：`role.cli.function=config`，stdin 读配置文档 → `cfgsync.Publish` → stdout 写 `{version}`；
  cli config `Validate` 增加 `config`。~~
- [x] ~~**api（库）**：`(*api.Engine).PublishConfig(ctx, doc) (int64, error)` → `cfgsync.Publish`。~~
- [x] ~~三面共用同一个 `cfgsync.Publish`（与 upload/ejson/sql 的"同核多面"一致）；发布即被 daemon/gateway 的 Watcher 取到生效。~~

## 测试

- [x] ~~单元：版本守卫（旧/相等版本丢弃、重放不回退）；`fetch` 坏文档 → 保留 last-good；backend 选择；`parser.SwapFilter` 编译失败不替换。~~
- [x] ~~集成（真实 DocumentDB，临时库隔离）：~~（`internal/cfgsync/integration_test.go`，gated on `TANGO_TEST_MONGO_URI`）
  - poll：运行中改 `_tango_config` → ≤ `pollInterval` 内 filter 热替换生效（`TestIntegration_Poll_HotSwap`）；写坏 filter（不可编译）→ 上报不中断、保留旧 filter（`TestIntegration_Poll_BadFilterKeepsLastGood`）。
  - changestream：开启后改文档 → 亚秒级生效（`TestIntegration_ChangeStream_HotSwap`，topology 不支持时自跳过）。
  - 回退：乱序/重放更旧 `version` → filter 不回退（`TestIntegration_Poll_VersionGuardNoRollback`）。
  - standalone mongo（无副本集）：`backend=changestream` → 明确报错引导改 `poll`（changestream probe 跳过 + 单元覆盖）；`backend=poll` 正常。
- [x] ~~并发安全：热替换期间持续上报，断言无撕裂（Holder 原子）、无数据竞争（`-race`）。~~（`internal/cfgsync/concurrency_test.go`，`go test -race` 通过）
- [x] ~~发布三面：`cfgsync.Publish` 拒绝坏配置 + version 单调 `$inc`；gateway `POST /config`（httptest）/ cli `function=config` / api `PublishConfig`（集成）。~~（gateway `server_cfgsync_integration_test.go` httptest；cli `cli_cfgsync_integration_test.go`；api `api_cfgsync_integration_test.go`）
- [x] ~~allowlist：发布或同步 allowlist 外的子树被拒绝（不写入、不生效）。~~（`publish_test.go` + `registry_test.go` 单元 + `TestIntegration_Publish_RejectsOffAllowlist` 断言不写入）
- [x] ~~端到端：api/cli/gateway 任一面发布 → daemon/gateway 的 Watcher 在 ≤`pollInterval`（或亚秒，changestream）内生效。~~（`TestServer_PostConfig_EndToEnd_HotSwap`：HTTP 发布 → gateway 自身 Watcher 热替换，经 /upload 行为观测）

## 文档 / 示例

- [x] ~~`doc/arch.md` 增补 cfgsync 章节（§5.4 定位 + 「安全模型」表 + 覆盖范围 + 依赖边 `cfgsync → dao + parser + cfgtree + logging`）+ §3.1 文件清单 + §7.1 依赖一览。~~
- [x] ~~`doc/usage.md` / `doc/config.md` 增加 `cfgsync.*` 键说明 + `_tango_config` 文档 schema（`{_id, version, filter:{include,exclude}}`）+ 三面发布用法。~~
- [x] ~~示例：`examples/config/daemon/` 与 `examples/config/gateway/` 的 max 段加入 `cfgsync.*`（min 不含，保持最小）。~~
- [x] ~~一张 cfgsync 热替换流程图（启动拉取 / change stream / 定时拉取 / 版本守卫 / 校验后再换）—— 见 `doc/arch.md` §5.4 ASCII 流程图。~~

## 收尾

- [x] ~~本地全绿（gofmt clean / `go vet ./...` clean / `go test ./...` 全过；集成测试 gated 跳过，`-race` 并发测试通过）。~~
  待用户 EC2 跑一遍**连真实 DocumentDB** 的 gated 集成（`TANGO_TEST_MONGO_URI=...`）确认绿，即 Phase 3 / v1.4.2 线收尾。
- [x] ~~`doc/todo.md` Phase 3 的 cfgsync 行已改名并指向本文件（`internal/remoteconfig` → `internal/cfgsync`）。~~

## 集合 / 配置键清单

- 集合：`_tango_config`（文档：`{_id: <documentID>, version: <int 单调>, filter: {include: [...], exclude: [...]}}`）。
- 键：`cfgsync.enabled` · `cfgsync.backend` · `cfgsync.documentID` · `cfgsync.pollInterval` · `cfgsync.reconcileInterval` · `cfgsync.collection`。
- 发布面：gateway `POST /config` · cli `role.cli.function=config` · api `(*Engine).PublishConfig`（同核 `cfgsync.Publish`）。

## 约定遵循自检

- [x] ~~cfgsync 只经 `dao` / `parser` 根包门面（不 import `dao/ejson`、`parser/filter`）。~~
- [x] ~~包名 = 配置键路径；无顶层 typed 聚合；`FromTree` 自取并校验自己那棵子树。~~
- [x] ~~非角色：像 `api.Engine` 一样被 daemon/gateway 内嵌；`worker`/taskqueue 不接入。~~
- [x] ~~DocumentDB 安全：仅 findOne / watch / updateOne(`$set`+`$inc`) upsert，无 pipeline update。~~
- [x] ~~读写同核：Watcher（读）与 Publish（写）同属 cfgsync 根包；三面发布（gateway/cli/api）共用 `cfgsync.Publish`。~~
