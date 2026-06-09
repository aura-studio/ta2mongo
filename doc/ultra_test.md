# Tango v1.5 超详细测试需求（软件工程级全特性覆盖）

> 本文是**测试需求规格（test requirements specification）**，不是任务勾选单。它**完全从 v1.5
> 源码反推**（不依赖既有 `doc/test.md` / `test2.md`，那两份只覆盖 tailer fd 增量，已落后于全量需求），
> 目标是对 tango v1.5 的**每一个模块、每一条对外契约、每一个分支与失败路径**给出可执行、可判定的测试需求。
>
> 每条需求带：**编号**、**类型**（U=单元 / I=集成-需 Mongo / E=端到端 / P=性能压测）、
> **环境**（任意 / **Linux-only**）、**优先级**（P0 阻断发布 / P1 重要 / P2 增强）、**现状**
> （✅已覆盖 / 🟡部分 / ❌缺口，对照 `*_test.go` 套件估算）。
>
> 判定原则：**每条需求必须有可观测的断言**（返回值 / Mongo 文档状态 / 日志字段 / 计数器 / fd 计数 /
> 进程退出码），不接受"跑起来不崩"这类弱断言。

---

## 0. 测试总纲

### 0.1 被测系统（SUT）边界

tango v1.5 是**单一二进制 + 一个公共 SDK 库（`client`）**。运行角色由配置键 `role.mode` 选定
（`daemon` / `gateway` / `cli`；`api` 是被内嵌的引擎库，不可由 `role.mode` 派发）。能力面：

1. **上报链路**（parse → filter → identity → write-model → MongoDB bulk）——三种上传策略
   `single` / `batch` / `pipeline`，由 `process.mode` 选定；daemon 强制 pipeline。
2. **Mongo Data API**（`/ejson` · cli `ejson` · `api.EJSON`）——EJSON 驱动的完全放开 CRUD/aggregate。
3. **SQL Data API**（`/sql` · cli `sql` · `api.SQL`）——经外部依赖 `aura-studio/mongosql` 注入式
   SQL→MongoDB。
4. **运行时动态配置同步 cfgsync**（读侧 Watcher + 写侧 Publish；`/config` · cli `config` ·
   `api.PublishConfig`）。
5. **数据来源**：tailer（文件追尾，daemon）/ httpbody（请求体，gateway/api）/ stdin（cli）/ taapi（占位）。

### 0.2 铁律：Linux Docker 是 fd/inode 类用例的唯一可信环境

凡涉及 **deleted-but-open / unlink / inode 复用 / `/proc/<pid>/fd` / `open_fds` 计数 /
`IN_DELETE_SELF` 自锁** 的用例（§4.3 D/E/F、§8.2 fd 看门狗），**必须在 Linux/amd64 容器内跑**，
Windows/macOS 复现不出来会"假绿"。复用：

```bash
# Go 1.23 + procps + lsof（快速单元/集成）
docker compose -f test/docker-compose.yml run --rm tango-test go test -race ./...
# Ubuntu 24.04 + Go 1.26.2 + gcc（go.mod 要求 1.26.2；-race 需 cgo）
docker compose -f test/docker-compose.ubuntu.yml run --rm tango-test go test -race ./...
```

两套 compose 均注入 `TANGO_TEST_MONGO_URI=mongodb://mongo:27017`（mongo:6，副本集**未开**，
故 changestream 类用例默认 `t.Skip`，需单独起带 `--replSet` 的 mongo 才跑，见 §7.5）。
非 fd 类的纯逻辑单元用例可在宿主机跑，但**回归门禁一律以容器结果为准**。

### 0.3 测试分层与命名

| 层 | 标记 | 依赖 | 触发 |
|---|---|---|---|
| 单元 U | `*_test.go` 纯逻辑 | 无 | `go test ./...` |
| 集成 I | `*_integration_test.go` | `TANGO_TEST_MONGO_URI` | 容器内 |
| 端到端 E | `tests/`、`role/*` httptest | Mongo + HTTP/stdin | 容器内 |
| 压测 P | 长稳/吞吐/泄漏 | Mongo + lumberjack | 容器内、可长跑 |

约定：未设 `TANGO_TEST_MONGO_URI` 时 I/E 用例 `t.Skip`（不许静默假绿）；所有 I/E 用例**自建独立
database/collection 前缀并在结束清理**，互不串库。

### 0.4 全局 Release Gate（全绿才发 v1.5）

- G-A 容器内 `go build ./...`、`go vet ./...` 无错无告警（含测试文件编译）。
- G-B 容器内 `go test -race ./...` 全绿（纯单元 + 有 Mongo 的集成）。
- G-C §4 tailer 三模式生命周期 + fd/goroutine 不泄漏（D 组、E2、G1）。
- G-D §8.2 fd 看门狗超阈优雅重启、在途数据不丢、进程干净退出。
- G-E §5/§6/§7 上报三策略、Data API、cfgsync 同核多面 功能正确。
- G-F §9 `NewFromTree` 三处等价、daemon fail-fast、typed `New` 回归全绿。

---

## 0.5 覆盖进度（2026-06-09 实测更新，Ubuntu 24.04 容器 `-race`）

> 下表是对各 ID 现状列的**权威实测覆盖说明**（行内现状列可能滞后，以此为准）。
> 全量 `go vet ./...` + `go test -race ./...` 0 FAIL / 0 DATA RACE。

### Release Gate 实测结论

| 门禁 | 结果 | 证据 |
|---|---|---|
| G-A build + vet | ✅ | 容器内 `go build ./...` / `go vet ./...` 无告警 |
| G-B `go test -race ./...` | ✅ | 19 个含测试包全绿；`-race` 期间还抓出并修了 4 个真实 tailer 并发 bug |
| G-C tailer 三模式 + fd/goroutine 不泄漏 | ✅(G1 跑动中) | C1–C4/D1–D5（`lifecycle_test.go`）、event ticker C2/inode C4/200 轮转 C5（`reaping_test.go`）；E2 soak PASS；G1 4h 进行中曲线全平 |
| G-D fd 看门狗超阈优雅重启 | ✅ | `TestWatchdog_E2/E5/E1E3E4`、`TestProcStats_D2`、`TestRuntimeStats_D1_D3`（`watchdog_test.go`） |
| G-E 上报三策略/Data API/cfgsync | 🟡 | H1/H3/H4（`test/h_release_gate_test.go`）、identity（`dao/store`）、roles×modes（`tests`）已绿；EJ-*/SQL/cfgsync changestream 细分仍部分 |
| G-F `NewFromTree` 等价 + typed 回归 | ✅ | `TestNewFromTree_G1/G2/G3/G4`、全量 role/client/test `-race` 绿 |

### 本轮新增测试 → 覆盖的 ID

| 测试文件 | 覆盖 ID | 结果 |
|---|---|---|
| `internal/source/tailer/lifecycle_test.go` | TAIL C1–C4 / D1–D5、TAIL-15/16 | ✅ `-race -count=5` |
| `internal/source/tailer/reaping_test.go` | TAIL-10（event ticker）、in-place inode、200 轮转 | ✅ |
| `internal/source/tailer/backpressure_test.go` | TAIL-17/18（背压 fd 释放、不死锁、不丢） | ✅ 三模式 |
| `internal/role/daemon/watchdog_test.go` | DMN-2/4/5/6/7/8/9/11、E2/E5、D1–D4 | ✅ |
| `internal/role/daemon/newfromtree_test.go` | G1/G2（daemon 等价 + fail-fast 连库前） | ✅ |
| `internal/role/{api,gateway}/newfromtree_test.go` | G3/G4 | ✅ |
| `test/h_release_gate_test.go` | E2E-1/3（H1/H3）、X-1（H4 优雅退出 fd=0） | ✅ |
| `config/ultra_config_test.go` | CFG-2/4/5/7/11/13 | ✅ 14 用例 |
| `internal/logging/ultra_logging_test.go` | LOG-1/2/4/5 | ✅ 11 用例 |
| `internal/parser/talog/ultra_talog_test.go` | PARSE-10/11 | ✅ |
| `internal/parser/filter/ultra_filter_test.go` | FILT-7 | ✅ |
| `internal/process/core/ultra_core_test.go` | CORE-6、STAT-3 | ✅ |
| `internal/process/ultra_process_test.go` | PROC-1、BATCH-4 | ✅ |
| `internal/source/ultra_source_test.go` | SRC-1/2/3/4 | ✅ 11 用例 |
| `main_test.go` | MAIN-1/4 | ✅ |
| `internal/role/daemon/ultra_maskuri_test.go` | DMN-13 / X-6（maskURI 脱敏） | ✅ |

> 备注：以上单元用例**未发现生产 bug**——代码与契约一致（如 talog `toString` 丢弃非字符串字段为 `""`、
> `maskURI` 把 `user:pass`→`***:***`、`Process` 把逐行 panic 收成 dead_letter、`BatchSize<=0`→默认 1000）。

### 仍未覆盖（需 Mongo 集成 / DocumentDB / 长跑，留作后续）

- §5.6 Data API 各 action 细分断言（EJ-2~7/9/10）、§5.7 SQL（SQL-2/4/5/6/7）——需 Mongo，多为 `🟡`。
- §7 cfgsync changestream（CS-14~18/35）——需带 `--replSet` 的 Mongo 才跑。
- §10.2 X-4（DocumentDB 兼容矩阵）——`dao/store` 存储层已在真 DocumentDB 跑过 23 用例，其余 Data API 面未单独跑。
- G1 4h 长稳——进行中，跑满归档 VERDICT 后 G-C 完全闭环。
- DocumentDB daemon 上报能力压测——见 `doc/` 下单独报告（计划中）。

---

## 1. 配置层：`config` + `internal/cfgtree`

> 设计：`config` 拥有 viper（文件 < `TANGO_*` env < flag），物化成依赖中立的 `cfgtree.Tree`；
> 每个模块用 `FromTree` 自取子树 + `ApplyDefaults` + `Validate`；键路径 = 包路径。

### 1.1 三途径优先级与互换（`config/load.go`、`loader.go`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CFG-1 | 优先级 默认 < 文件 < `TANGO_*` env < flag：四源同设一键，断言最终值取最高优先级源 | U / 任意 / P0 / ✅(`loader_test.TestFlagOverridesEnvAndFile`,`TestLoad_TypedEnvOverrides`) |
| CFG-2 | 仅"用户显式设置"的 flag 才覆盖（`flags.Visit` 语义）：未传的 flag 不得把文件/env 值打回零值 | U / 任意 / P0 / 🟡 |
| CFG-3 | env 映射：`TANGO_` 前缀 + `.`→`_` 转大写（`dao.mongo.uri`↔`TANGO_DAO_MONGO_URI`、`role.mode`↔`TANGO_ROLE_MODE`、`source.tailer.tailMode`↔`TANGO_SOURCE_TAILER_TAILMODE`） | U / 任意 / P0 / ✅(`TestEnvOverridesYAML`) |
| CFG-4 | `--config` 是文件路径**不是配置键**：`bindFlagsTo` 跳过 `config`；不得被注册成 `--config` 配置键 | U / 任意 / P1 / 🟡 |
| CFG-5 | 每个配置键都注册同名 `--<键>` flag（`RegisterFlags`）：枚举 `registerAll` 全键，断言均有对应 flag | U / 任意 / P1 / ❌ |
| CFG-6 | 文件扩展名选解析器：`.yaml`/`.yml`→YAML，`.json`→JSON；空路径/不存在路径**静默跳过**回退默认+env+flag | U / 任意 / P0 / 🟡(`TestLoad_Unified`) |
| CFG-7 | 文件存在但 stat 报非 NotExist 错误 → `Load` 返回包裹错误；解析失败 → `read config` 错误 | U / 任意 / P1 / ❌ |

### 1.2 `LoadBytes` 内存配置（`config/bytes.go`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CFG-8 | `LoadBytes` 解析 YAML 与 JSON，叠加 `TANGO_*` env，语义同 `Load`；空 bytes → 仅默认+env | U / 任意 / P0 / ✅(`bytes_test`) |
| CFG-9 | `detectConfigType`：首个有效字节 `{`/`[`→json，其余→yaml；前导空白/制表/换行跳过 | U / 任意 / P1 / ✅(`TestDetectConfigType`) |

### 1.3 `cfgtree.Tree` 切片与解码（`internal/cfgtree`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CFG-10 | `Sub(key)` 累加路径、共享底层 map（不重建子 viper），保留所有 env/flag-only 叶子；`Sub("")` 返回自身 | U / 任意 / P0 / 🟡 |
| CFG-11 | `Into(dst)`：缺失分支或零 `Tree` 不动 dst（让 `ApplyDefaults` 仍可用）；多段路径任一段非 map → resolve 返回 nil → no-op | U / 任意 / P0 / ❌ |
| CFG-12 | decode hook：`StringToTimeDuration`（`"30s"`→`time.Duration`）、`StringToSlice(",")`（逗号切片）、`WeaklyTypedInput`（env 字符串强转数值/布尔/切片） | U / 任意 / P0 / 🟡 |
| CFG-13 | 键大小写：viper 小写化键，叶子按 mapstructure tag 大小写不敏感匹配 | U / 任意 / P2 / ❌ |

### 1.4 各模块 `FromTree` / `ApplyDefaults` / `Validate` / `RegisterDefaults`

> 每个领域根包都有这四元组（`logging`/`dao`/`parser`/`source`/`process`/`cfgsync`/`role`）。
> 通用契约：`FromTree(t)=t.Sub(<前缀>).Into(&c)+ApplyDefaults+Validate`；校验失败带模块前缀错误。

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CFG-14 | 每模块 `FromTree` 错误信息带模块前缀（如 `dao: ...`、`parser: filter: ...`），便于定位 | U / 任意 / P1 / 🟡 |
| CFG-15 | `dao.Validate`：缺 `dao.mongo.uri` 报 `uri is required`；nil Mongo 视为缺失 | U / 任意 / P0 / ✅(`TestValidation_MissingMongoURI`) |
| CFG-16 | 默认值矩阵逐键断言（见 §0 与 config.md）：`logging.level=info`/`format=text`；`mongo.connectTimeout=10s`/`serverSelectionTimeout=30s`；`store.maxElapsedTime=10s`；`tailer.rescanInterval=30s`/`tailMode=hybrid`/`pollInterval=200ms`/`maxLineBytes=10MiB`/`maxOpenFDs=0`；`process.mode=batch`/`batchSize=1000`；`pipeline.batchWorkers=2`/`flushInterval=1s`/`deadLetterCap=128`；`cfgsync.enabled=false`/`backend=poll`/`documentID=filter`/`pollInterval=5s`/`reconcileInterval=60s`/`collection=_tango_config`；`role.mode=daemon`/`role.gateway.addr=:8080`/`role.cli.function=upload` | U / 任意 / P0 / 🟡(`config_test` 覆盖一部分) |
| CFG-17 | `examples/config` 全部 max/min × yaml/json 能 `Load` 且 max 配置字段完整（无遗漏键） | U / 任意 / P1 / ✅(`examples_test`) |
| CFG-18 | 负数/零时长经 `ApplyDefaults` 归默认（如 `maxOpenFDs<0→0`、各 timeout `<=0→默认`） | U / 任意 / P1 / 🟡(`TestRescanInterval_ZeroFallsBackToDefault` 等) |

---

## 2. 日志基础：`internal/logging`

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| LOG-1 | `Init(cfg)`：level∈{debug,info,warn,error}、format∈{text,json} 正确应用；nil cfg 或无法识别值回退 info/text，永不 panic | U / 任意 / P0 / 🟡 |
| LOG-2 | `Validate` 拒绝非法 level/format（空串=用默认，合法） | U / 任意 / P1 / ❌ |
| LOG-3 | `Recover(ctx)`：goroutine 内 panic 被吞并以 error 级带 stack 记录；无 panic 时 no-op | U / 任意 / P0 / ✅(`logging_test`) |
| LOG-4 | 包级 helper（`WithError`/`WithField(s)`/`Info`/`Warn`/`Error`/`Debug` + `Fields` 别名）在 `Init` 前即可用（默认 std logger 非 nil） | U / 任意 / P1 / 🟡 |
| LOG-5 | JSON format 下输出可被 JSON 解析、含结构化字段 | U / 任意 / P2 / ❌ |

---

## 3. 解析层：`internal/parser`（talog + filter + 门面）

### 3.1 talog 解析与校验（`parser/talog`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| PARSE-1 | 直接 TA payload：含 `#time`/`#type`/`#event_name` 任一根键即识别 | U / 任意 / P0 / ✅(`parser_test`) |
| PARSE-2 | 信封格式：`msg`/`message`/`log` 字段内嵌 JSON 字符串（须以 `{` 开头、可解析、内层含 TA 键）被解析；非 JSON/空/非 `{` 开头 跳过 | U / 任意 / P0 / ✅(`Envelope*`) |
| PARSE-3 | 必填校验：`#type` 与 `#uuid` 非空，否则报错 | U / 任意 / P0 / ✅(`MissingType`,`MissingUUID`) |
| PARSE-4 | user_* 校验：需 `#time` + （`#account_id` 或 `#distinct_id` 至少其一） | U / 任意 / P0 / ✅ |
| PARSE-5 | track* 校验：需 `#time` + `#event_name` + 身份至少其一 | U / 任意 / P0 / ✅ |
| PARSE-6 | 非 user_*/track* 类型报 `unsupported #type`；空行/空 JSON/非 TA payload 报错 | U / 任意 / P0 / ✅ |
| PARSE-7 | `flattenPayload`：`properties.*` 提升到根、删除 `properties` 键，其余顶层字段保留 | U / 任意 / P0 / ✅(`FlattenedProperties`,`NoPropertiesField`) |
| PARSE-8 | `Doc` 注入 `#time`/`#uuid`/`_ts`（`_ts=time.Now().UnixNano()` 单调递增、每次解析新值） | U / 任意 / P0 / ✅(`DocContainsMetaFields`) |
| PARSE-9 | `Record.Category()`：`user_*`→User，其余（含 track/track_update/track_overwrite）→Event；`IsUserType`/`IsEventType` 前缀判定 | U / 任意 / P0 / ✅(`record_test`) |
| PARSE-10 | `toString` 容错：非字符串/nil 字段→`""`（如 `#account_id` 为数字时不崩） | U / 任意 / P1 / 🟡 |
| PARSE-11 | `EnvelopeKeys` 取值顺序（msg→message→log），多个信封键时取首个有效 | U / 任意 / P2 / ❌ |

### 3.2 上报 filter（`parser/filter`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| FILT-1 | include OR 语义：非空时至少一条为真才保留；空 include 放行该阶段 | U / 任意 / P0 / ✅(`Keep_IncludeOR`) |
| FILT-2 | exclude：任一为真即丢弃；在 include 之后 | U / 任意 / P0 / ✅(`Keep_ExcludeOR`,`IncludeThenExclude`) |
| FILT-3 | nil `*Filter` / 空规则 = no-op 全放行；`Empty()` 正确 | U / 任意 / P0 / ✅ |
| FILT-4 | hash 重写：`#ident` → `$env["#ident"]`，仅在字符串字面量外；双引号/单引号/反引号字面量内的 `#` 不动；转义处理 | U / 任意 / P0 / ✅(`TestRewriteHashRefs`) |
| FILT-5 | 编译失败（语法错/非布尔表达式）返回带 index+源 的错误 | U / 任意 / P0 / ✅(`New_CompileError`) |
| FILT-6 | 求值错误：`Keep` 返回 (false, firstErr)，按"未命中"保守处理（include miss / exclude miss）；`expr.AllowUndefinedVariables` 下未定义变量不报错 | U / 任意 / P0 / ✅(`Keep_EvalErrorPropagates`) |
| FILT-7 | 表达式非 bool 返回值报 `did not return bool` | U / 任意 / P1 / 🟡 |
| FILT-8 | 作用于扁平化记录：`#type`/`#event_name`/`properties.*` 提升后可直接引用 | U / 任意 / P0 / ✅(`Keep_PropertiesFlattened`) |

### 3.3 Holder 原子热替换 + 门面（`parser/filter/holder.go`、`parser/parser.go`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| FILT-9 | `Holder` 原子 `Store`/`Current`/`Keep`/`Empty`；nil Holder no-op | U / 任意 / P0 / ✅(`holder_test`) |
| FILT-10 | `Parser.SwapFilter(include,exclude)`：编译成功才原子换；**编译失败保留 last-good 并返回错误**（cfgsync 的 parser 侧保证） | U / 任意 / P0 / ✅(`SwapFilter_CompileFailureKeepsLastGood`) |
| FILT-11 | 热替换并发安全：worker 持续 `Keep` 同时 `Store` 新 filter，`-race` 无数据竞争 | U(race) / 任意 / P0 / ✅(`cfgsync/concurrency_test.TestFilterHotSwap_RaceFree`) |
| FILT-12 | 门面重导出：`parser.Record`/`RecordCategory`/`CategoryUser`/`CategoryEvent`/`EnvelopeKeys` 与 talog 同一类型；消费方无需 import talog/filter | U / 任意 / P2 / 🟡 |

---

## 4. 来源层：`internal/source`（tailer / httpbody / stdin / taapi + 门面）

### 4.1 门面与有限源（`source.go`、`httpbody`、`stdin`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| SRC-1 | `NewLines`：发出全部非空行后关闭 channel；空/nil 切片→立即关闭不发；空字符串行被跳过；尊重 ctx 取消 | U / 任意 / P0 / 🟡 |
| SRC-2 | `NewReader`(stdin)：逐行扫描非空行→channel，EOF/ctx 取消时关闭；nil reader→os.Stdin；`maxLineSize=1MiB` 上限；scan 错误记 warn 不崩 | U / 任意 / P0 / 🟡 |
| SRC-3 | `NewTailer(cfg)`：nil cfg 用零值；按 `LogPattern/RescanInterval/TailMode` 构造并 `WithTuning(PollInterval,MaxLineBytes)` | U / 任意 / P1 / 🟡 |
| SRC-4 | 两源 goroutine 内 `logging.Recover` 兜底 panic | U / 任意 / P2 / ❌ |
| SRC-5 | taapi 占位包可编译、无导出符号（防回归误用） | U / 任意 / P2 / ✅(编译即测) |

### 4.2 tailer 路径与 glob（`tailer.go`，纯逻辑，跨平台）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| TAIL-1 | 路径转换：`toWindowsPath`/`normalizeWindowsPath`/`toLinuxNativePath`/`toNativePath`/`normalizePath` 在 `/c/x`↔`C:\x`↔`/mnt/c/x` 间正确往返；`/var/log/...` 等非盘符路径不动 | U / 任意 / P0 / ✅(`TestToWindowsPath`,`TestRoundTrip`) |
| TAIL-2 | `globMatch`：`*`/`?`/`[...]`/`**`（零或多级目录、连续 `**`、尾随 `**` 全匹配）正确 | U / 任意 / P0 / ✅(`TestGlobMatch`) |
| TAIL-3 | `globBaseDir`：取首个 glob 元字符前的字面前缀作为 walk 根；`./`/`.\`前缀、无前缀（`*.log`/`**/*.log`→`.`）正确 | U / 任意 / P0 / ✅(`TestGlobBaseDir`) |
| TAIL-4 | `discoverFiles`：多 pattern 去重、跳过空 pattern、跳过不可访问路径、相对/`./`相对/`**`相对路径正确发现 | U / 任意 / P0 / ✅(`DiscoverFiles_*`) |
| TAIL-5 | `rescan` 周期发现新文件、不重复已 tail 文件 | I / 任意 / P0 / ✅(`Rescan_*`) |

### 4.3 tailer 文件生命周期 + fd/goroutine 回收 ★核心门禁，Linux-only★

> 对 **poll / event / hybrid 三种模式各跑一遍**。两条独立回收路径：① `reapMissing` 兜底
> （≤1×rescanInterval）；② event/hybrid 的 `os.Stat`+`os.SameFile` ticker 自检快路径（≤~500ms
> `hybridPollInterval`）；poll 模式每周期 `os.Stat`（窗口≤pollInterval）。

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| TAIL-6 | C1 持续 append：输出行数==写入行数，无丢行无重复（三模式） | I / Linux / P0 / ✅(`Lifecycle_C1`,`ContinuousWrite_*`) |
| TAIL-7 | C2 rotate（rename 当前 + 新建同名）：新文件从头被 tail，旧文件残余读完（inode 变更经 `os.SameFile` 检出→重开） | I / Linux / P0 / ✅(`Lifecycle_C2`) |
| TAIL-8 | C3 truncate（size 变小）：检出 `fi.Size()<pos`→seek 0 从头重读，不卡死不 panic | I / Linux / P0 / ✅(`Lifecycle_C3`) |
| TAIL-9 | C4 删除后不重现（lumberjack 备份被删）：负责该文件的 goroutine ≤1×rescanInterval 退出，map 条目删除，fd 释放 | I / Linux / P0 / ✅(`Lifecycle_C4`,`Reap_DeletedFileReleasesTail`) |
| TAIL-10 | **event 模式 ticker 自检**：删除后即便不等 rescan，fd 也在 ~`hybridPollInterval`(500ms) 内经 `tt.Stop()` 释放（v1.5.0 前 event 无 ticker 会挂住） | I / Linux / P0 / 🟡 |
| TAIL-11 | D1 辅助 `countDeletedFDs()` 读 `/proc/self/fd` 统计 ` (deleted)` 条目 | I / Linux / P0 / ✅(`FD_D1`) |
| TAIL-12 | D2 单文件 tail→删除→等 2×rescan→deleted fd 计数回 0 | I / Linux / P0 / ✅(`FD_D2`) |
| TAIL-13 | D3 连续 rotate 100+ 次（保留窗口 N）：稳态 deleted fd ≤ 存活文件数，不随轮转单调增长 | I / Linux / P0 / ✅(`FD_D3`) |
| TAIL-14 | D4 rotate 1000 次后 `NumGoroutine()` 稳定、`Tailer.tailed` map size 不单调增长 | I / Linux / P0 / ✅(`Goroutine_D4`) |
| TAIL-15 | D5 对 event 与 poll 重复 D2+D3，确认三模式都不泄漏 | I / Linux / P0 / 🟡 |
| TAIL-16 | `TailedCount()` 准确反映活动 tail goroutine 数（fd 泄漏直接信号） | U/I / Linux / P0 / 🟡 |
| TAIL-17 | F1 背压：mongo 限速/暂停使 `out`(2000) 打满后不死锁、不 panic、不 OOM | P / Linux / P1 / ❌ |
| TAIL-18 | F2 背压期间删除正在 tail 的文件：`out<-line` 阻塞时 `ctx.Done()` 分支仍能退出、defer 关 fd（fd ≤N 秒释放） | P / Linux / P0 / ❌ |
| TAIL-19 | E1/E2 与真实 `natefinch/lumberjack`（size10MB/backup10）交互 ≥10min：deleted 计数与卷 used 平稳 | P / Linux / P1 / ❌ |
| TAIL-20 | G1 生产速率（~2–3GB/h）连续 rotate 4h：deleted fd / goroutine / RSS / 卷 used 四曲线全程平稳，基线归档 `test/results/` | P / Linux / P2 / ❌ |
| TAIL-21 | 错误重试不刷屏：tailFile 读错误记 warn 后按 pollInterval 重试，不忙等 | U/I / Linux / P2 / ❌ |

---

## 5. 数据访问层：`internal/dao`

### 5.1 连接装配（`dao/mongo`、`dao.New`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| DAO-1 | `ConnectMongo`：`Connect`+`Ping`（Ping 强制 server selection，故不可达/错配在此失败，受 `ServerSelectionTimeout` 约束），Ping 失败时 Disconnect 后返回错误 | I / 任意 / P0 / 🟡 |
| DAO-2 | `MongoDBFromURI`：URI path 取库名；`mongodb://h:27017/tango`→`tango`，无 path→默认 `tango`；非法 URI 报错 | U / 任意 / P0 / ✅(`MongoDBFromURI_DefaultDBWhenURINoDBInPath`) |
| DAO-3 | `MongoResource.Close` nil 安全（nil 接收者 / nil client 返回 nil） | U / 任意 / P1 / ✅(`MongoResourceClose_NilReceiver`) |
| DAO-4 | `dao.New`：nil/部分 cfg 被 `ApplyDefaults` 补齐；装配 `Mongo`+`Store`；连接成功记 `db` 字段日志 | I / 任意 / P0 / 🟡 |
| DAO-5 | `dao.Watch`：在默认库的 collection 上开 change stream，pipeline/opts 原样转发；不支持拓扑透传驱动错误 | I / Linux+RS / P1 / 🟡 |

### 5.2 store 持久化与重试（`dao/store/store.go`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| STORE-1 | `BulkWrite` 无序、`BulkWriteOrdered` 有序；空 models→no-op 返回 nil | I / 任意 / P0 / ✅(`integration_test`) |
| STORE-2 | 指数退避：InitialInterval 200ms、MaxInterval 2s、`MaxElapsedTime` 预算（默认 10s）；超预算返回错误并记 warn | U/I / 任意 / P0 / ✅(`store_test.TestRetry*`) |
| STORE-3 | `isOnlyDuplicateKey`：纯 E11000（无 write-concern err、有 write errors、全 11000）被当成功（`_ts` guard 跳过语义），**不重试不报错**；混入非 11000 或 WCE 则按失败 | U/I / 任意 / P0 / 🟡 |
| STORE-4 | 重试计数：`WriteStats.Retries` 累加（attempt-1）；`TotalRetries()` 正确 | U / 任意 / P1 / ✅(`TestRetrySucceedsBeforeMaxElapsedTime`) |
| STORE-5 | ctx 取消中止重试（`backoff.WithContext`） | U / 任意 / P1 / ✅(`TestRetryWithContextCancellation`) |
| STORE-6 | 集合访问器 `UserCollection`/`EventCollection`/`DeadLetterCollection` 返回正确命名集合 | I / 任意 / P1 / ✅(`TestCollectionAccessors`) |

### 5.3 写模型语义（`dao/store/writemodel.go`）★DocumentDB 安全是硬约束★

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| WM-1 | `user_set`：`$set` 合并 meta+data，filter 带 `$or:tsGuardOr(ts)`（`_ts` 不存在或 `$lte` 入参 ts），upsert | I / 任意 / P0 / ✅(`UserWriteModel_UserSet_Integration`) |
| WM-2 | `user_setOnce`：`$setOnInsert` data + `$max` meta（仅 `_ts` 用 `$max`，其余 meta `$set`） | I / 任意 / P0 / ✅(`UserSetOnce`) |
| WM-3 | `user_add`：`$inc` data + `$max` meta（commutative，乱序安全） | I / 任意 / P0 / ✅(`UserAdd`) |
| WM-4 | `user_unset`：data 键 `$unset`、meta `$set`、filter 带 tsGuard；set/unset 键集不相交 | I / 任意 / P0 / ✅(`UserUnset_Integration`,`UnsetStructure`) |
| WM-5 | `user_del`：`DeleteOne` by `#user_id`，无 ts 检查 | I / 任意 / P0 / ✅(`UserDel`) |
| WM-6 | `user_append`：`$push` + `$each`（标量包成单元素数组）；`user_uniq_append`：`$addToSet` + `$each`（幂等） | I / 任意 / P0 / ✅(`UserAppend`,`UserUniqAppend`) |
| WM-7 | 未知 user 类型→回退 user_set 语义 | U/I / 任意 / P1 / 🟡 |
| WM-8 | `track`：`$setOnInsert` upsert by `#uuid` 幂等（重投/重启重读不改已存在文档） | I / 任意 / P0 / ✅(`Track_Integration`) |
| WM-9 | `track_update`：`$set` + filter `#uuid`+tsGuard（per-uuid `_ts` 防回退） | I / 任意 / P0 / ✅(`TrackUpdate`) |
| WM-10 | `track_overwrite`：`ReplaceOne` + filter `#uuid`+tsGuard，整文档替换 | I / 任意 / P0 / ✅(`TrackOverwrite`) |
| WM-11 | event 缺 `#uuid`/`_ts` 时补齐（`#uuid`=入参、`_ts`=now） | U / 任意 / P1 / 🟡 |
| WM-12 | `_ts` 防回退端到端：先写新 ts 文档→再投旧 ts 同 key→旧的不覆盖（user_set/track_update/track_overwrite 各验一遍） | I / 任意 / P0 / ✅(`writemodel_test.*FilterGuard`) |
| WM-13 | **DocumentDB 兼容**：所有 update 为 document-form（非 aggregation-pipeline），DocumentDB 不报 "Wrong type for parameter u" | I / DocumentDB / P0 / ❌(需 DocumentDB 环境) |
| WM-14 | `DeadLetterModel`：insert `{_ts, line, error}`；nil err→空 error 串 | I / 任意 / P0 / ✅(`DeadLetterModel_Integration`) |
| WM-15 | `splitFields`/`metaKeys`：meta（`#uuid/#type/#time/#user_id/#account_id/#distinct_id/_ts`）与 data 正确分离 | U / 任意 / P1 / ❌ |

### 5.4 身份解析（`dao/store/identity*.go`）★TA 身份规则★

> 规则：account↔user 1:1；account→distinct 1:N；distinct→account 1:1。缓存只读穿透、永不失效
> （绑定不可逆）；多 pod 安全靠唯一索引 + 条件更新，不靠进程锁。

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| ID-1 | 仅 account：已存在→复用 userID；不存在→`atomicCreateForAccountID` 新建+自增 | I / 任意 / P0 / ✅(`OnlyAccountID`) |
| ID-2 | 仅 distinct：已存在→复用；不存在→`atomicCreateForDistinctID` 新建 | I / 任意 / P0 / ✅(`OnlyDistinctID`) |
| ID-3 | 两者皆新：`atomicCreateForBoth` 绑一起新建 | I / 任意 / P0 / ✅(`BothIDs_NewUser`) |
| ID-4 | account 在/distinct 新：`$addToSet` 绑 distinct 到 account 的 user | I / 任意 / P0 / ✅(`AccountExistsDistinctNew`) |
| ID-5 | account 新/distinct 在且未绑 account：条件更新 `atomicBindAccountToDistinct`；竞争失败→给 account 建独立 user | I / 任意 / P0 / ✅(`DistinctExistsAccountNew`) |
| ID-6 | distinct 已绑别的 account：account 不绑、建独立 user | I / 任意 / P0 / ✅(`DistinctAlreadyBound`) |
| ID-7 | 两者皆在：无绑定变更，优先返回 account 的 userID | I / 任意 / P0 / ✅(`BothExist_SameUser`,`BothExist_DifferentUsers`) |
| ID-8 | `#user_id` 自增（`id_counter` `$inc` upsert `FindOneAndUpdate ReturnDocument:After`），唯一递增 | I / 任意 / P0 / ✅(`AutoIncrementUserID`) |
| ID-9 | **并发竞争**：多 goroutine 同 account/distinct 并发 Resolve → 唯一 userID、无重复 user 文档、孤儿文档被删（`-race`） | I(race) / 任意 / P0 / ✅(`ConcurrentAccess`) |
| ID-10 | 缓存命中快路径（两 ID 都缓存→0 IO，返回 account 优先）；半命中走 DB | U/I / 任意 / P1 / 🟡 |
| ID-11 | account 唯一索引冲突（InsertOne dup key）→读回已存在、缓存、（必要时）绑 distinct | I / 任意 / P0 / 🟡 |
| ID-12 | distinct 无唯一索引下的竞争：post-insert 复查，别 pod 赢则删自己孤儿、采纳对方 | I(race) / 任意 / P1 / 🟡 |
| ID-13 | `findByAccountID`/`findByDistinctID`：`ErrNoDocuments`→(nil,nil) 不报错 | U/I / 任意 / P1 / 🟡 |

### 5.5 索引（`dao/store/indexes.go`、`identity.go`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| IDX-1 | `EnsureIndexes` 幂等可重复调用 | I / 任意 / P0 / ✅(`EnsureIndexes_Integration`,`EnsureIndexes_Idempotent`) |
| IDX-2 | user：`#user_id`(unique)、`#account_id`、`#distinct_id`、`_ts` | I / 任意 / P1 / 🟡 |
| IDX-3 | event：(`#event_name,#account_id,#time`)、(`#event_name,#distinct_id,#time`)、`#uuid`(unique)、`_ts` | I / 任意 / P1 / 🟡 |
| IDX-4 | dead_letter：`_ts`；id_mapping：`#user_id`(unique)、`#account_id`(unique sparse)、`#distinct_ids` | I / 任意 / P1 / 🟡 |

### 5.6 Mongo Data API（`dao/ejson`）★完全放开、无白名单/上限★

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| EJ-1 | 校验顺序（先于连接）：未知 action 报错、空 action 报 `action is required`、缺 collection 报错、缺库（请求无 + URI 无）报错、无连接报错 | U/I / 任意 / P0 / ✅(`TestExecute_Validation`) |
| EJ-2 | `findOne`：projection/sort(bson.D 保序)/skip 转发；无匹配→空 Response（不报错） | I / 任意 / P0 / 🟡 |
| EJ-3 | `find`：projection/sort/limit/skip 转发；空结果仍输出 `"documents":[]`（指针+非 nil） | I / 任意 / P0 / 🟡 |
| EJ-4 | `insertOne`：缺 document 报错；返回 `insertedId` | I / 任意 / P0 / 🟡 |
| EJ-5 | `updateOne`：缺 update 报错；upsert 转发；返回 matched/modified（指针保零）/upsertedId（有则带） | I / 任意 / P0 / 🟡 |
| EJ-6 | `deleteOne`：返回 deletedCount | I / 任意 / P0 / 🟡 |
| EJ-7 | `aggregate`：nil pipeline→空 pipeline；drain 全部行 | I / 任意 / P0 / 🟡 |
| EJ-8 | EJSON 编解码：`DecodeRequest`（relaxed，兼容 plain JSON）保留 `$oid`/`$date`/`$numberLong`/`$numberDecimal`；`MarshalEJSON` relaxed 往返无损 | U / 任意 / P0 / ✅(`DecodeRequest_EJSONTypes`,`MarshalEJSON_RoundTrip`) |
| EJ-9 | database 缺省取连接 URI 的库；显式 database 覆盖 | I / 任意 / P1 / 🟡 |
| EJ-10 | **无限制**：任意 db/collection/filter/operator/pipeline 转发，不设 limit/返回数/超时上限（按设计） | I / 任意 / P1 / ❌ |
| EJ-11 | DocumentDB：aggregation-pipeline 形式的 update 引擎错误透传调用方（普通 `$set` 正常） | I / DocumentDB / P2 / ❌ |
| EJ-12 | `EJSON_Integration` 端到端各 action 正确（含 `tests/ejson_test` 经 gateway+cli 两面） | I/E / 任意 / P0 / ✅(`EJSON_Integration`,`tests/ejson_test`) |

### 5.7 SQL Data API（`dao/sql`，外部 mongosql 注入）

> v1.5 变更：`dao/sql` 不再"拷贝" mongosql，而是 `mongosql.New(res.DB)` 注入式依赖外部
> `aura-studio/mongosql/driver`（go.mod require）。tango 侧仅 `Driver` 薄包装 + `Result.MarshalEJSON`。

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| SQL-1 | `New(nil)` / nil DB 资源报 `nil MongoDB resource` | U / 任意 / P0 / ✅(`TestNew_NilResource`) |
| SQL-2 | `(*Dao).SQL` 惰性 `sync.Once` 构造 Driver；构造错误缓存并对后续调用返回同错；不自拨号/不 Close（共享连接池） | U/I / 任意 / P0 / 🟡 |
| SQL-3 | `Exec` 分发：SELECT→find/aggregate/distinct、INSERT/UPDATE/DELETE、INSERT...SELECT；`Result.Kind` 正确 | I / 任意 / P0 / ✅(`SQL_Integration`,`tests/sql_test`) |
| SQL-4 | `Result.MarshalEJSON`：SELECT 行含 BSON 类型 relaxed EJSON | U/I / 任意 / P0 / 🟡 |
| SQL-5 | 表名=集合名、库取自 URI | I / 任意 / P1 / 🟡 |
| SQL-6 | DocumentDB 限制：含表达式 UPDATE（`SET n=n+1`，pipeline 形式）不被支持，错误透传；常量 SET 正常 | I / DocumentDB / P2 / ❌ |
| SQL-7 | 解析错误/不支持语句 透传错误 | I / 任意 / P1 / ❌ |

### 5.8 dao 门面边界（架构约束）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| DAO-6 | 领域间只经根包门面：`process/*` 不 import `dao/store`、`parser/talog|filter`；`role/*` 不 import `source/*` 子包、`parser/filter`；`cfgsync` 不碰 `dao/ejson`、`parser/filter`。可用 `go list -deps` / import 静态检查断言 | U / 任意 / P1 / ❌ |

---

## 6. 处理层：`internal/process`（single / batch / pipeline + core）

### 6.1 门面与模式（`process.go`、`config.go`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| PROC-1 | `ParseMode`：`single`/`batch`/`pipeline` 合法；其余报 `unknown upload mode`；`ModeValue` 空→默认 batch | U / 任意 / P0 / 🟡 |
| PROC-2 | `New` 按 mode 装配对应 Uploader；nil cfg/stats 被默认（stats→NoopStats） | U / 任意 / P0 / 🟡 |
| PROC-3 | `Config.Validate`：mode 合法 + `pipeline.Validate`（`batchSizeMin<=batchSize<=batchSizeMax` 一致性） | U / 任意 / P1 / ✅(`BatchSizeMinMax_AutoDerivation`) |
| PROC-4 | `Uploader` 接口：三实现都满足 `Run(ctx,src)error`+`Stop()`；`Stop` 可在 Run 前/多次调用安全 | U / 任意 / P1 / 🟡 |

### 6.2 core.Processor 逐行分类（`process/core/processor.go`）★全策略共享★

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CORE-1 | 解析失败→`KindParseError` + DeadLetterModel + `OnParseError`+`OnDeadLetter` | U/I / 任意 / P0 / ✅(`TestProcess_ParseError`) |
| CORE-2 | 解析成功→`OnParseOK`；filter 命中丢弃→`KindFiltered`+`OnFiltered`（**不进 dead_letter**） | U/I / 任意 / P0 / ✅(`TestProcess_Filtered`) |
| CORE-3 | filter 求值错误→`OnFilterError`（仍按 keep 结果处理） | U / 任意 / P1 / 🟡 |
| CORE-4 | identity 失败→`KindIdentityError` + DeadLetterModel + `OnIdentityError`+`OnDeadLetter` | I / 任意 / P0 / 🟡 |
| CORE-5 | User→`KindUser`+`OnUserWrite`+`UserWriteModel`；Event→`KindEvent`+`OnEventWrite`，且 `doc["#user_id"]=userID` 注入后建 `EventWriteModel` | U/I / 任意 / P0 / ✅(`NilFilterKeepsEverythingUntilIdentity`) |
| CORE-6 | **逐行 panic recover**：任意阶段 panic→`KindParseError`+dead_letter+warn，worker/进程不崩 | U / 任意 / P0 / 🟡 |
| CORE-7 | nil filter（parser 用 nil 建）保留全部记录 | U / 任意 / P1 / ✅ |

### 6.3 Counters / Stats（`process/core/counters.go`、`stats.go`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| STAT-1 | 10 个计数器并发安全累加（atomic），`-race` 无竞争 | U(race) / 任意 / P0 / ✅(`TestCountersConcurrent`) |
| STAT-2 | `Snapshot()` 一致读出全部计数 | U / 任意 / P1 / ✅(`TestCountersSnapshot`) |
| STAT-3 | `NoopStats` 全部方法 no-op | U / 任意 / P2 / 🟡 |

### 6.4 single 策略（`process/single`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| SINGLE-1 | 逐行即时写（每行一次 BulkWrite）；user/event/dead_letter 分集合 | I / 任意 / P0 / ✅(`gateway server_integration TestServer_Single_*`) |
| SINGLE-2 | Filtered 丢弃不写；parse/identity 错误进 dead_letter，不中断 run | I / 任意 / P0 / ✅(`TestServer_Single_InvalidLine`) |
| SINGLE-3 | 写失败记 `OnWriteError`+error 日志，run 继续；ctx 取消返回 `ctx.Err()`；源 drain 返回 nil | U/I / 任意 / P1 / 🟡 |

### 6.5 batch 策略（`process/batch`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| BATCH-1 | 累积至 `batchSize` flush；源 drain 时 flush 余量；ctx 取消时 flush 余量后返回 `firstErr`/`ctx.Err()` | I / 任意 / P0 / ✅(`TestServer_Batch_*`,`ingest_batch_integration`) |
| BATCH-2 | user/event/dead 三类分别累积、分集合 bulk（无序）；返回首个写错误 | I / 任意 / P0 / ✅ |
| BATCH-3 | 全有效/全无效/混合/空源 行为正确（计数对得上） | I / 任意 / P0 / ✅(`AllValid`,`AllInvalid`,`MixedLines`,`Empty`) |
| BATCH-4 | 非正 batchSize 回退 `DefaultBatchSize=1000` | U / 任意 / P1 / 🟡 |
| BATCH-5 | 大批量（>batchSize 多倍）多次 flush 正确 | I / 任意 / P1 / ✅(`TestServer_Batch_Large`) |

### 6.6 pipeline 策略（`process/pipeline`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| PIPE-1 | N worker + per-worker channel；`RunWorkers` 阻塞至全部退出；worker/dispatch goroutine 各有 `logging.Recover` | I / 任意 / P0 / ✅(`TestServer_Pipeline_Track`) |
| PIPE-2 | 亲和路由 `ExtractRoutingKey`：account>distinct>信封内 account>distinct；无身份→""→worker 0 | U / 任意 / P0 / ✅(`routing_test.*`) |
| PIPE-3 | `RouteIndex`：FNV-1a 一致 hash %n；空 key 或 n<=0→0；分布大致均匀 | U / 任意 / P0 / ✅(`RouteIndex_*`) |
| PIPE-4 | `Dispatch` 防队头阻塞：先非阻塞投亲和 worker→失败试其它 worker→全满才阻塞投亲和或 ctx.Done；lineCh 关或 ctx 取消时关闭所有 worker channel | U/I / 任意 / P0 / ✅(`dispatch_integration.*`,`NoBlockWhenWorkerChannelFull`) |
| PIPE-5 | 同用户写顺序：亲和保证同 key 落同 worker；背压下 spill 可破坏跨 worker 顺序，但 `_ts` guard 保证正确性（旧不覆盖新） | I / 任意 / P0 / 🟡(`AffinityGuarantee`) |
| PIPE-6 | 动态刷新 `ComputeFlushThreshold`：r=backlog/chCap，r=0→max、0.5→initial、1→min，分段线性、clamp 到 [min,max]，sizeMin/initial/max 非法值消毒 | U / 任意 / P0 / ✅(`dynamicbatch_test.*`) |
| PIPE-7 | flush 触发：动态阈值 / `FlushInterval` ticker / 死信批满；invalid 行每 1000 条记一次 warn | U/I / 任意 / P1 / 🟡 |
| PIPE-8 | **最终 flush 用 `context.Background()`**：ctx 取消/channel 关闭后在途 batch 仍落库（不丢数据） | I / 任意 / P0 / 🟡 |
| PIPE-9 | `Run` 始终返回 nil（写错误经 `OnWriteError` 反映，不走返回值） | U/I / 任意 / P1 / 🟡 |
| PIPE-10 | `Batch` 容器：Add/Full/Empty/Len/Reset（保留底层数组）/零容量/超容量 | U / 任意 / P1 / ✅(`batch_test.*`) |
| PIPE-11 | 配置派生：`MinBatchSize`(显式或 batchSize/4≥1)、`MaxBatchSize`(显式或 batchSize*2)、`ChannelSize`(显式或 batchSize*2)、`batchWorkers`默认 2 | U / 任意 / P1 / ✅(`config_test`) |

---

## 7. 运行时动态配置同步：`internal/cfgsync`

> 安全模型目标：**有界陈旧 + 自愈 + 不回退 + 坏配置打不挂**。读侧 Watcher（embed daemon/gateway）+
> 写侧 Publish（gateway/cli/api 同核）。默认 allowlist 只放 `parser.filter`。

### 7.1 Watcher / observe 版本守卫（`cfgsync.go`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CS-1 | nil doc→no-op（未发布=保持现状） | U / 任意 / P0 / ✅(`Observe_NilDocAndMissingVersionAreNoOps`) |
| CS-2 | 缺数值 version→warn + 忽略 | U / 任意 / P0 / ✅ |
| CS-3 | **单调守卫**：`version<=lastVersion` 丢弃（防回退、防重放、防冗余 swap） | U / 任意 / P0 / ✅(`Observe_VersionGuardMonotonic`) |
| CS-4 | onChange 失败：记 warn 保留 last-good、**lastVersion 仍前进**（不重试该版本）、不停 backend；返回错误供计数 | U / 任意 / P0 / ✅(`Observe_ApplyErrorAdvancesGuardAndKeepsLastGood`) |
| CS-5 | nil onChange→只读 no-op（取文档+守卫但不应用） | U / 任意 / P1 / ✅(`Observe_NilOnChangeIsReadOnly`) |
| CS-6 | `Run` 选 backend 失败（未知 backend）直接返回错误；正常时记 watcher starting 日志 | U / 任意 / P1 / 🟡 |

### 7.2 backend 选择与 fetch（`backend.go`、`fetch.go`、`config.go`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CS-7 | `selectBackend`：poll→pollBackend、changestream→changeStreamBackend、其余报错（构造不碰连接） | U / 任意 / P0 / ✅(`TestSelectBackend`) |
| CS-8 | `fetchDoc`：经 `dao.EJSON findOne` by `_id`；缺文档→(nil,nil) | U/I / 任意 / P1 / 🟡 |
| CS-9 | `docVersion` 归一：int/int32/int64/float64→int64，其余→(0,false) | U / 任意 / P0 / ✅(`TestDocVersion`) |
| CS-10 | `Config.Validate` 拒绝未知 backend；非正 interval 经 `ApplyDefaults` 归默认 | U / 任意 / P1 / ✅(`config_test.*`) |

### 7.3 poll backend（`poll.go`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CS-11 | 启动收敛读一次（先于循环），再每 `pollInterval` 读 | I / 任意 / P0 / ✅(`integration_test.TestIntegration_Poll_HotSwap`) |
| CS-12 | 读错误记 warn + 继续（自愈）；ctx 取消时不误报 | U/I / 任意 / P1 / 🟡 |
| CS-13 | 任意拓扑可用（含 standalone mongod） | I / 任意 / P0 / ✅ |

### 7.4 changestream backend（`changestream.go`）★需副本集/DocumentDB★

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CS-14 | **先订阅后快照**：订阅成功后再全量读，消除 read↔subscribe TOCTOU | I / Linux+RS / P0 / 🟡(`TestIntegration_ChangeStream_HotSwap`) |
| CS-15 | reconcile ticker 兜底全量读（丢事件/静默断流仍最终收敛） | I / Linux+RS / P0 / 🟡 |
| CS-16 | 流断（运行时网络/failover）→`resubscribe` 退避 2s + 全量读 fallback，不硬崩；仅 ctx 取消才返回错误 | I / Linux+RS / P1 / ❌ |
| CS-17 | delete 事件（无 fullDocument）→nil→no-op | U/I / Linux+RS / P1 / 🟡 |
| CS-18 | `$match documentKey._id` + `SetFullDocument(UpdateLookup)`：update 事件带全 post-image | I / Linux+RS / P1 / 🟡 |
| CS-19 | **不支持拓扑**（standalone mongod / DocumentDB 未开 modifyChangeStreams / Elastic Cluster）：初次订阅失败→`unsupportedTopologyError` 清晰报错指向 `backend=poll`，不静默降级 | U/I / 任意 / P0 / ✅(`TestUnsupportedTopologyError` 文案；真实拓扑 🟡) |
| CS-20 | pump goroutine 有 `logging.Recover`，observe 只在单线程 Run 循环调用（守卫无锁安全） | U / 任意 / P1 / 🟡 |

### 7.5 Registry / allowlist + filter applier（`registry.go`、`filter.go`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CS-21 | `Apply` 路由非保留字段到 applier；保留字 `_id`/`version` 跳过 | U / 任意 / P0 / ✅(`registry_test.*`) |
| CS-22 | off-allowlist 子树→warn + 返回错误（不静默应用） | U / 任意 / P0 / ✅(`RejectsOffAllowlist`) |
| CS-23 | `asSubDocument` 接受 bson.M / bson.D / map[string]any，其余报错 | U / 任意 / P1 / ✅(`TestAsSubDocument`) |
| CS-24 | `RegisterFilter`：`filter.{include,exclude}`→`parser.SwapFilter`；编译失败保留 last-good | U / 任意 / P0 / ✅(`filter_test.*`) |
| CS-25 | `toStringSlice`：nil/[]string/bson.A/[]any 容错；非字符串元素报错（拒绝坏规则，保留 last-good） | U / 任意 / P0 / ✅(`TestToStringSlice`,`NonStringRuleRejected`) |

### 7.6 写侧 Publish 同核多面（`publish.go` + 三面）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CS-26 | `validatePublishDoc`：剥 `_id`/`version`；空文档/无子树→报错；经一次性 Registry+throwaway parser 干跑校验 allowlist+编译 filter（与 apply 侧同标准） | U / 任意 / P0 / ✅(`publish_test.*`) |
| CS-27 | `Publish`：`updateOne $set + $inc:{version:1}` upsert（DocumentDB 安全，无 pipeline update），读回新 version 返回 | I / 任意 / P0 / ✅(`TestIntegration_Publish_VersionMonotonic`) |
| CS-28 | version 单调：连续 publish 严格递增（并发也只前进） | I / 任意 / P0 / ✅ |
| CS-29 | 三面同核：gateway `POST /config`、cli `function=config`、`api.PublishConfig` 行为一致，写同一 `_tango_config`/documentID 文档 | I/E / 任意 / P0 / ✅(`server_cfgsync_integration`,`cli_cfgsync_integration`,`api_cfgsync_integration`) |
| CS-30 | off-allowlist / 不可编译 filter 在 publish 端即 400/错误拒绝（不落库） | I/E / 任意 / P0 / ✅(各面均有) |

### 7.7 读写端到端与扇出

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CS-31 | Poll 热替换端到端：publish 新 filter → watcher 应用 → live filter 行为改变 | I / 任意 / P0 / ✅(`TestServer_PostConfig_EndToEnd_HotSwap`,`Integration_Poll_HotSwap`) |
| CS-32 | 坏 filter 保留 last-good（端到端） | I / 任意 / P0 / ✅(`Poll_BadFilterKeepsLastGood`) |
| CS-33 | 版本守卫不回退（端到端：旧 version 文档不生效） | I / 任意 / P0 / ✅(`Poll_VersionGuardNoRollback`) |
| CS-34 | 多 watcher 扇出：一次 publish 被所有 daemon/gateway 实例最终接收 | I / 任意 / P1 / ✅(`Poll_FanOutAllWatchers`,`ChangeStream_FanOutAllWatchers`) |
| CS-35 | 安全模型矩阵（逐机制断言）：启动收敛 / 单调守卫 / 校验后再换 / 先订阅后快照 / 消费者边界（仅 daemon/gateway 订阅） | I / 任意 / P1 / 🟡 |

---

## 8. 运行角色：`internal/role`

### 8.1 派发与 api 引擎（`role.go`、`role/api`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| ROLE-1 | `role.Get(mode)`：daemon/gateway/cli 返回对应 Role；`api`/未知→报错 `not runnable` | U / 任意 / P0 / 🟡 |
| ROLE-2 | `role.Config.Validate`：mode∈{daemon,gateway,cli}，否则报错；空 mode→默认 daemon | U / 任意 / P0 / 🟡 |
| API-1 | `api.New`：缺 URI 报 `MongoDB URI is required`；构造 dao/parser/proc/cfgsync；parser 编译失败时关连接并报错 | U/I / 任意 / P0 / ✅(`TestNew_ErrorsWhenURIEmpty`) |
| API-2 | `api.NewFromTree`：切 dao/process/parser/cfgsync 四子树，与 typed `New` 等价 | I / 任意 / P0 / ✅(§9 G4) |
| API-3 | `Upload`/`Run`：按 `process.mode` 跑，返回 `Result{Lines,UserWrites,EventWrites,DeadLetters,Filtered}`；parse/identity 失败计入 dead_letter 不报错；写失败/未知 mode 报错 | I / 任意 / P0 / ✅(`gateway example_test`) |
| API-4 | `EJSON`/`SQL` 经 dao 门面中转，独立于上报链路（proc/parser 不参与） | I / 任意 / P0 / ✅ |
| API-5 | `EnsureIndexes`/`Close` 转发 | I / 任意 / P1 / 🟡 |
| API-6 | `StartCfgsync`：仅 `enabled` 时起 goroutine（注册 filter applier + Watcher，panic recover、ctx 取消退出、致命错误记 log 不崩调用方）；disabled→no-op | I / 任意 / P0 / 🟡 |
| API-7 | `PublishConfig` 转发 `cfgsync.Publish` | I / 任意 / P0 / ✅(`api_cfgsync_integration`) |

### 8.2 daemon 角色（`role/daemon`）★fd 看门狗 + 运行时指标★

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| DMN-1 | `Role.Run`：`signal.NotifyContext(SIGINT,SIGTERM)`；NewFromTree→EnsureIndexes→Run；defer Shutdown 关连接 | E / 任意 / P0 / 🟡(`tests/integration TestDaemonContinuousReport`) |
| DMN-2 | **fail-fast**：`logPattern` 缺失时 `NewFromTree` 在**连 Mongo 之前**报 `source.tailer.logPattern is required`（不应先连库/建索引再失败） | U/I / 任意 / P0 / ❌(§9 G2) |
| DMN-3 | 强制 pipeline：拷贝 procCfg 设 `Mode=pipeline`，`process.New(...).Run(runCtx, tailer)` 阻塞至取消 | E / 任意 / P0 / 🟡 |
| DMN-4 | `reportStats` 每 60s 打三条日志：interval（10 项增量）、cumulative（10 项累计）、runtime（`goroutines`/`open_fds`/`tailed_files`） | E / Linux / P0 / ❌(D 组) |
| DMN-5 | `open_fds`：Linux 经 `/proc/self/fd`（-1 修正 ReadDir 自身 fd），与 `ls /proc/<pid>/fd|wc -l` 量级吻合；非 Linux→-1 不报错 | E / Linux & 非Linux / P0 / ❌ |
| DMN-6 | `tailed_files` == 当前 glob 命中活动文件数；rotate+删除后先升后回落不单调增长 | E / Linux / P0 / ❌ |
| DMN-7 | **fd 看门狗超阈优雅重启（核心门禁）**：`maxOpenFDs>0 && open_fds>threshold` → 打 ERROR `triggering graceful restart` → `cancelRun` → pipeline drain+flush（**在途 batch 全部落库、计数对得上不丢数据**）→ exit 0 干净退出 → 编排器 `restartPolicy: Always` 重新拉起、fd 表清零 | E / Linux / P0 / ❌ |
| DMN-8 | 看门狗默认关闭：`maxOpenFDs=0/负数`（经 ApplyDefaults 归零）→ 即便 fd 很高也不重启，只照常打指标 | E / Linux / P0 / ❌ |
| DMN-9 | 阈值边界：`==threshold` 不触发、`>threshold` 才触发（严格大于） | E / Linux / P0 / ❌ |
| DMN-10 | 非 Linux inert：`open_fds==-1` 时 `-1>threshold` 永假，不误触发 | U/E / 任意 / P1 / ❌ |
| DMN-11 | SIGTERM 不被看门狗干扰：父 ctx→runCtx 取消→优雅退出；`cancelRun` 与信号路径不打架；`reportDone` 正常关闭、`logFinalStats` 照打 | E / Linux / P0 / ❌ |
| DMN-12 | `logFinalStats`：打 shutdown 摘要（10 项 + 总重试数 + uptime + 平均吞吐 lps）；有错走 `SHUTDOWN WITH ERRORS` 分支 | E / 任意 / P1 / ❌ |
| DMN-13 | `maskURI`：脱敏 `user:pass@host` → `***:***@host`；无凭据/无 `://` 原样 | U / 任意 / P1 / ❌ |
| DMN-14 | daemon 内嵌 cfgsync（enabled 时热替换 live filter；changestream 不支持拓扑→记 log 不拖垮 daemon） | E / 任意 / P1 / 🟡 |
| DMN-15 | F2 背压期间触发看门狗：`cancelRun` 后 pipeline 背压下完成 drain 不死锁；受阻则记最坏耗时与硬退出兜底 | P / Linux / P1 / ❌ |

### 8.3 gateway 角色（`role/gateway`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| GW-1 | `Handler` 路由：`/healthz`(200)、`/upload`、`/ejson`、`/sql`、`/config` | E / 任意 / P0 / ✅(`server_test.TestHealthzContract` 等) |
| GW-2 | `decodeBody`：非 POST→405；空 body 容错（io.EOF 不报错）；坏 JSON→400 | U / 任意 / P0 / ✅(`DecodeBody_*`) |
| GW-3 | `/upload`：body `{line?, lines?[]}` 合并为一个 httpbody 源，按 process.mode 跑，返回统计 JSON；引擎错误→500 | E / 任意 / P0 / ✅(`server_integration TestServer_*`) |
| GW-4 | `/ejson`：读 body→`DecodeEJSONRequest`→`engine.EJSON`→`writeEJSON`(application/ejson)；解码错→400、执行错→500 | E / 任意 / P0 / 🟡 |
| GW-5 | `/sql`：JSON `{"sql":...}`，空 sql→400；`engine.SQL`→writeEJSON；执行错→500 | E / 任意 / P0 / 🟡 |
| GW-6 | `/config`：body=配置文档→`PublishConfig`→`{"version":n}`；off-allowlist/坏 filter→400（写前拒绝） | E / 任意 / P0 / ✅(`server_cfgsync_integration`) |
| GW-7 | `writeEJSON`/`writeErr`/`writeJSON`：Content-Type 与 code 正确；marshal 失败→500 | U / 任意 / P1 / ✅(`TestWriteErr`) |
| GW-8 | `Run`：`StartCfgsync` 后 `ListenAndServe`；ctx 取消→10s 优雅 `Shutdown`；监听错误经 errCh 返回 | E / 任意 / P0 / 🟡 |
| GW-9 | `NewFromTree` 返回 `gwCfg.Addr` 正确，role.gateway 段校验 | I / 任意 / P0 / ✅(§9 G3) |
| GW-10 | `EnsureIndexes` 幂等（多次启动安全） | I / 任意 / P1 / ✅(`EnsureIndexes_Idempotent`) |
| GW-11 | 端到端全流程：upload→ejson 查回 / sql 查回 一致 | E / 任意 / P0 / ✅(`TestServer_EndToEnd_FullFlow`) |

### 8.4 cli 角色（`role/cli`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| CLI-1 | `function=upload`（默认）：stdin 读日志→按 process.mode 上报→stdout 打统计 JSON（缩进） | E / 任意 / P0 / 🟡(`tests/integration TestRolesModes`) |
| CLI-2 | `function=ejson`：stdin 读一个 EJSON 请求→`engine.EJSON`→stdout EJSON + 换行；不用 proc/parser 配置 | E / 任意 / P0 / ✅(`tests/ejson_test`) |
| CLI-3 | `function=sql`：stdin 读一条 SQL→`engine.SQL`→stdout EJSON + 换行 | E / 任意 / P0 / ✅(`tests/sql_test`) |
| CLI-4 | `function=config`：stdin 读 JSON 文档→`PublishConfig`→stdout `{"version":n}`；off-allowlist 拒绝 | E / 任意 / P0 / ✅(`cli_cfgsync_integration`) |
| CLI-5 | `Config.Validate`：function∈{upload,ejson,sql,config}，否则报错；空→默认 upload | U / 任意 / P1 / ✅(隐含) |
| CLI-6 | 每个 function 各自构造独立 engine 并 Close（资源不泄漏） | E / 任意 / P1 / 🟡 |

---

## 9. SDK 客户端：`client` + `NewFromTree` 重构等价性

### 9.1 client 公共门面（`client/client.go`、`options.go`）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| SDK-1 | `New`：必须 `WithDaoMongoURI`，否则 `api.New` 报错经 options.err 透传；返回 Client 内嵌同一 `api.Engine` | U/I / 任意 / P0 / ✅(`TestNew_PropagatesConfigError`) |
| SDK-2 | `Upload(ctx, lines...)`→`Result`；语义与 daemon/gateway/cli 同一上报路径 | I / 任意 / P0 / 🟡 |
| SDK-3 | `WithConfigFile`/`WithConfigBytes`：导入 gateway 兼容统一配置，仅应用 dao.* / parser.filter.* / process.*，**忽略 logging/source/role**；TANGO_* env 叠加 | U / 任意 / P0 / ✅(`options_test.*`) |
| SDK-4 | base-then-override：config 选项在前、个别 With* 在后者胜（先文件后覆盖） | U / 任意 / P0 / ✅(`TestConfigBytesThenOverride`) |
| SDK-5 | 各 With* 一一映射真实 config 字段（dao.mongo.* / dao.store.maxElapsedTime / parser.filter.include|exclude 追加 / process.* / process.pipeline.*）；未设保留引擎默认 | U / 任意 / P1 / 🟡 |
| SDK-6 | `WithContext` 只约束初次连接、非配置键；nil ctx 不覆盖 | U / 任意 / P2 / 🟡 |
| SDK-7 | 坏配置（不可编译 filter / 坏 bytes）→`New` 返回错误不建 client | U / 任意 / P1 / ✅(`WithConfigBytes_Invalid`) |
| SDK-8 | `EnsureIndexes`/`Close` 转发；并发 Upload 安全（连接池） | I(race) / 任意 / P1 / ❌ |

### 9.2 `NewFromTree` 重构等价性（保证没改坏接线）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| G1 | daemon：同一 cfgtree 下 `daemon.NewFromTree` 与手工 `FromTree+daemon.New` 行为等价（含 logPattern 校验、启动 banner） | I / 任意 / P0 / 🟡 |
| G2 | daemon fail-fast：logPattern 缺失在连 Mongo 前报错（见 DMN-2） | I / 任意 / P0 / ❌ |
| G3 | gateway：`NewFromTree` 的 `gwCfg.Addr` 正确、Role.Run 能起服务、`/healthz`/`/upload` 通 | E / 任意 / P0 / 🟡 |
| G4 | api：`NewFromTree` 切的四配置与 typed `New` 等价、`Upload` 一致 | I / 任意 / P0 / 🟡 |
| G5 | **typed `New` 回归全绿**：`go test ./internal/role/...`（gateway httptest/api/cli）、`./client/...`、`tests/`（gateway.New/daemon.New/api.New 全 typed） | I/E / 任意 / P0 / ✅ |

---

## 10. 入口与跨切面

### 10.1 main.go

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| MAIN-1 | `resolveConfigPath`：`--config` 非空→原样；否则二进制同级目录按 `tango.yaml→yml→json` 取首个存在的非目录；都无→`""`（回退默认+env+flag） | U / 任意 / P1 / ❌ |
| MAIN-2 | 启动链：`Load→Tree→logging.FromTree+Init→role.FromTree 取 mode→role.Get(mode).Run`；任一步错误返回非零退出码 | E / 任意 / P0 / 🟡 |
| MAIN-3 | `--<键>` flag 全注册（cobra `RegisterFlags`）、`NoArgs`（多余位置参数报错）、`SilenceUsage` | U/E / 任意 / P2 / ❌ |
| MAIN-4 | 无 role 子命令：只认 `role.mode`（防回归引入子命令） | U / 任意 / P2 / ❌ |

### 10.2 跨切面横向需求

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| X-1 | **优雅关闭**：daemon SIGTERM / gateway ctx 取消 / 有限源 drain → 在途数据全部落库、所有 goroutine 退出、连接关闭、deleted fd 清零 | E / Linux / P0 / 🟡(`H4`) |
| X-2 | **panic 隔离**：单行 panic（core.Processor）、各 goroutine（tailer/httpbody/stdin/pipeline worker+dispatch/cfgsync watcher+pump/gateway http）均有 `logging.Recover`，进程不崩 | U/I / 任意 / P0 / 🟡 |
| X-3 | **`-race` 全绿**：filter 热替换、counters、identity 并发、dispatch 并发生产者 等并发面无数据竞争 | U/I(race) / Linux / P0 / ✅(部分) |
| X-4 | **DocumentDB 兼容矩阵**：写模型 document-form、cfgsync `$set+$inc` upsert、无 pipeline update；在真实/模拟 DocumentDB 上不报 "Wrong type for parameter u" | I / DocumentDB / P0 / ❌ |
| X-5 | **跨平台路径**：tailer glob 在 Linux 与 Windows 路径形态下都正确匹配（纯逻辑单元可在两平台跑） | U / 任意 / P1 / ✅ |
| X-6 | **凭据脱敏**：日志中 Mongo URI 永不含明文口令（maskURI 全链路） | U/E / 任意 / P1 / ❌ |
| X-7 | **Data API 无鉴权说明**：`/ejson`/`/sql` 完全放开、无白名单/上限 —— 测试需固化该行为（防"误加限制"回归），并文档化"鉴权由调用方负责" | I / 任意 / P1 / ❌ |
| X-8 | **幂等性**：daemon 重启重读文件 / 重复 upload 同 `#uuid` 事件 → track `$setOnInsert` 不重复计；可接受的 at-least-once 边界记录 | I / Linux / P0 / 🟡(`H3`) |

### 10.3 功能端到端样本（贴近生产）

| ID | 需求 | 类型/环境/优先级/现状 |
|---|---|---|
| E2E-1 | 投喂含 `PaymentOrderState`(track) 与 `user_set` 的样本：filter 只放这两类、Mongo 字段正确 | E / Linux / P0 / 🟡(`H1`) |
| E2E-2 | identity 端到端：account↔distinct 绑定 1:1 与 1:N 符合预期 | E / Linux / P0 / ✅(`H2` 对应 identity 集成) |
| E2E-3 | rotate 跨文件边界事件不丢（at-least-once 记录重复边界） | E / Linux / P0 / 🟡(`H3`) |
| E2E-4 | 四角色 × 三策略矩阵：daemon(pipeline) / gateway(single,batch,pipeline) / cli(各 function) / client(SDK) 全部跑通且统计一致 | E / Linux / P0 / 🟡 |

---

## 11. 现状汇总与缺口清单（按优先级）

### 11.1 已较完整覆盖（✅，回归保持）
talog 解析全分支、filter 逻辑+热替换、tailer 路径/glob/发现/生命周期 C/D 组、store 重试、写模型语义+`_ts` guard、identity 全 case+并发、ejson 编解码+集成、cfgsync observe/registry/publish/poll 集成、process dispatch/routing/dynamicbatch/batch 容器、gateway 上报三策略+`/config`、config 加载/env/flag、SDK options。

### 11.2 P0 缺口（**发布前必补**）
- **DMN-4~DMN-13**：daemon 运行时指标三条日志、fd 看门狗超阈优雅重启+在途不丢数据+干净退出+边界+SIGTERM 不干扰、maskURI（§8.2，Linux）。
- **DMN-2 / G2**：daemon `NewFromTree` fail-fast（连库前校验 logPattern）。
- **G1/G3/G4**：三处 `NewFromTree` 与 typed `New` 等价性显式断言。
- **TAIL-10/15/17/18**：event 模式 ticker 自检、poll/event 的 D2/D3、背压 fd 释放（§4.3，Linux）。
- **X-4 / WM-13 / EJ-11 / SQL-6**：DocumentDB 兼容性（需 DocumentDB 或兼容模拟环境）。
- **CORE-4/CORE-6**：identity 错误进 dead_letter、逐行 panic recover 的直接单测。
- **PIPE-8**：pipeline 最终 flush 用 background ctx 不丢数据的断言。

### 11.3 P1 缺口（重要）
CFG-5/CFG-7/CFG-11、LOG-2/LOG-5、STORE-3 的混合错误分支、EJ-2~EJ-10 各 action 的独立集成断言、CS-16 changestream 断流重订阅、CS-35 安全模型逐机制、API-6 StartCfgsync、X-1/X-2/X-6/X-7、DAO-6 import 边界静态检查、E2E-1/E2E-3/E2E-4。

### 11.4 P2 缺口（增强）
TAIL-19/20 长稳压测、MAIN-1/3/4、SDK-8 并发、各模块门面重导出一致性、filter 非 bool 返回。

---

## 12. 执行与门禁映射

| Release Gate | 对应需求 | 跑法 |
|---|---|---|
| G-A 编译/vet | 全部（编译即测） | 容器 `go build ./... && go vet ./...` |
| G-B race 全绿 | §1-7 U/I + X-3 | 容器 `go test -race ./...` |
| G-C tailer fd/生命周期 | TAIL-6~21 | Linux 容器 `go test -race ./internal/source/tailer/...` + 压测脚本 |
| G-D fd 看门狗 | DMN-4~15 | Linux 容器端到端起 daemon + 制造 fd 超阈 |
| G-E 上报/DataAPI/cfgsync | §5/§6/§7/§8 I/E | 容器 `go test ./internal/... ./tests/...`（需 Mongo；changestream 需 RS） |
| G-F NewFromTree/typed | §9 G1-G5 | 容器 `go test ./internal/role/... ./client/... ./tests/...` |

> DocumentDB 专项（X-4 系列）需独立环境，不在默认 CI 门禁，但**发布到 DocumentDB 目标前必须单独过**。
