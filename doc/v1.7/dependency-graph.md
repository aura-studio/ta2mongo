# tango v1.7 · `internal/` 逻辑依赖框架

> 本图描述 **v1.7 分支**（`internal/` 各包之间的 import 依赖）。依赖边是用
> `go list -f '{{.ImportPath}} {{.Imports}}'` 从编译器视角抽取的**真实直接 import**（非文档推断），
> 只保留模块内（`github.com/aura-studio/tango/...`）的边。架构总览见 [`../v1.6/arch.md`](../v1.6/arch.md) §2/§3/§7。
>
> 一句话结构：**严格单向分层、无环**——角色层 → 编排领域 → 数据领域（根包）→ 子包 → 基础层 → `cfgtree` 叶子；
> 领域之间只经**根包**互引（root-fronts-subpackages），全仓**唯一**跨域进子包的例外是 `backfill → parser/filter`
> （把 expr 选择 filter 下推成 TA SQL 的 `CompileToSQL`）。

## 1. 分层依赖总览（internal/）

```mermaid
graph TD
  classDef role     fill:#1f6feb,stroke:#0b3d91,color:#fff;
  classDef orch     fill:#8957e5,stroke:#4c2889,color:#fff;
  classDef domain   fill:#2da44e,stroke:#116329,color:#fff;
  classDef found    fill:#9a6700,stroke:#5c3d00,color:#fff;
  classDef leaf     fill:#6e7781,stroke:#373e47,color:#fff;
  classDef ext      fill:#eaeef2,stroke:#8c959f,color:#24292f;

  %% ---------- 入口 / 消费方（internal 外，仅作上下文）----------
  main["main.go"]:::ext
  client["client/ (公开 SDK)"]:::ext
  config["config/ (注册+装配 Tree)"]:::ext

  %% ---------- 角色层 internal/role ----------
  role["role (Get(mode) 派发)"]:::role
  api["role/api · 引擎 Engine"]:::role
  daemon["role/daemon"]:::role
  gateway["role/gateway"]:::role
  cli["role/cli"]:::role

  %% ---------- 编排领域（顶层域，非角色）----------
  backfill["backfill (TA OpenAPI 回填)"]:::orch
  cfgsync["cfgsync (运行时配置同步)"]:::orch
  process["process (上传策略)"]:::orch

  %% ---------- 数据领域根包 ----------
  dao["dao"]:::domain
  parser["parser"]:::domain
  source["source"]:::domain
  pfilter["parser/filter"]:::domain

  %% ---------- 基础 / 叶子 ----------
  logging["logging"]:::found
  cfgtree["cfgtree (依赖中立叶子)"]:::leaf

  %% ===== 入口/消费方 =====
  main --> config & role & logging
  client --> config & api
  config -. 注册所有领域键 .-> role

  %% ===== 角色层 =====
  role --> cli & daemon & gateway
  gateway --> api
  cli --> api

  api --> backfill & cfgsync & process & dao & parser & source & logging
  daemon --> cfgsync & process & dao & parser & source & logging
  gateway --> cfgsync & process & dao & parser & logging
  cli --> backfill & cfgsync & process & dao & parser & source

  %% ===== 编排领域 =====
  backfill --> process & dao & logging
  backfill -->|"⚠️ 唯一跨域进子包"| pfilter
  cfgsync --> dao & parser & logging
  process --> dao & parser & source

  %% ===== 数据领域 =====
  %% 注意：source 根包本身不 import logging（只有它的四个子包 import），故此处不画 source-->logging
  dao --> logging
  parser --> pfilter
  pfilter --> logging

  %% ===== 基础 =====
  logging --> cfgtree
  role & api & daemon & gateway & cli --> cfgtree
  backfill & cfgsync & process & dao & parser & source --> cfgtree
```

> 图中 `cfgtree` 收口了几乎所有 `*.Config` 模块的边（每个领域都依赖它的 `cfgtree.Tree` 载体）。
> `config`（在 `internal/` 外）按设计 import 了所有领域用于注册键，为保持框架可读，只画一条虚线示意。

## 2. 严格分层（自底向上，箭头一律指向更低层）

| 层 | 包 | 模块内直接依赖 |
|---|---|---|
| **L0 叶子** | `cfgtree` | （无——依赖中立配置载体，只依赖外部 `mapstructure`） |
| **L1 基础** | `logging` | `cfgtree` |
| **L2 子包** | `dao/mongo` | （无 internal） |
| | `parser/talog` | （无 internal） |
| | `source/taapi` | （无 internal）——**预留** stub，尚未接入 `source` 根包（`source` 不 import 它） |
| | `dao/store` | `logging` |
| | `parser/filter` | `logging` |
| | `source/file` · `source/httpbody` · `source/stdin` · `source/tailer` | `logging` |
| | `dao/ejson` · `dao/sql` | `dao/mongo` |
| | `process/core` | `dao` · `parser` · `logging` |
| | `process/single` · `process/batch` · `process/pipeline` | `process/core` · `dao` · `parser` · `source`（`single`/`pipeline` 另 `logging`） |
| **L3 领域根包** | `dao` | `dao/{store,mongo,ejson,sql}` · `cfgtree` · `logging` |
| | `parser` | `parser/{talog,filter}` · `cfgtree` |
| | `source` | `source/{file,httpbody,stdin,tailer}` · `cfgtree` |
| | `process` | `process/{core,single,batch,pipeline}` · `dao` · `parser` · `source` · `cfgtree` |
| **L4 编排领域** | `cfgsync` | `dao` · `parser` · `logging` · `cfgtree` |
| | `backfill` | `dao` · **`parser/filter`** · `process` · `logging` · `cfgtree` |
| **L5 角色** | `role/api`（引擎） | `backfill` · `cfgsync` · `process` · `dao` · `parser` · `source` · `logging` · `cfgtree` |
| | `role/daemon` | `cfgsync` · `process` · `dao` · `parser` · `source` · `logging` · `cfgtree` |
| | `role/gateway` | `role/api` · `cfgsync` · `process` · `dao` · `parser` · `logging` · `cfgtree` |
| | `role/cli` | `role/api` · `backfill` · `cfgsync` · `process` · `dao` · `parser` · `source` · `cfgtree` |
| | `role` | `role/{cli,daemon,gateway}` · `cfgtree` |
| **（外）入口** | `config` | 所有领域（仅用其 `RegisterDefaults` 注册键） · `cfgtree` |
| | `main.go` | `config` · `role` · `logging` |
| | `client/` | `config` · `role/api` |

层与层之间**只向下**，同层之间无边——这就是「无环」的结构保证（见 §4）。

## 3. 领域内部组成（root-fronts-subpackages）

每个领域有唯一根包对外，子包是实现细节；跨领域只准引用**根包**（唯一例外见 §4）。
> 本节只画**领域内部组成**（根包 fronts 哪些子包、子包间关系），为可读做了简化：子包的**跨域出边**
> （如 `process/{single,batch,pipeline} → dao`/`parser`/`source`、`process/core → logging`、各 source 子包 → `logging`）
> 不在此重复，完整边以 §2 表为准。图中 `logging` 在各子图里就近重画，实际是同一个包。

```mermaid
graph LR
  subgraph dao_g["dao"]
    daoR["dao"] --> store["store"] & mongo["mongo"] & ejson["ejson"] & sql["sql"]
    ejson --> mongo
    sql --> mongo
    store --> logA["logging"]
  end
  subgraph parser_g["parser"]
    parserR["parser"] --> talog["talog"] & filterN["filter"]
    filterN --> logB["logging"]
  end
  subgraph source_g["source"]
    sourceR["source"] --> file["file"] & httpbody["httpbody"] & stdin["stdin"] & tailer["tailer"]
    file & httpbody & stdin & tailer --> logC["logging"]
    taapi["taapi (预留·未接入)"]
  end
  subgraph process_g["process"]
    processR["process"] --> core["core"] & single["single"] & batch["batch"] & pipeline["pipeline"]
    single & batch & pipeline --> core
    core --> daoX["dao"] & parserX["parser"]
  end
```

## 4. 关键不变量与例外

1. **无环 / 严格单向**：所有模块内 import 边都从高层指向低层（角色 → 编排 → 领域 → 子包 → `logging` → `cfgtree`），
   不存在反向边或同层边。`GOTOOLCHAIN=go1.25.5 go build ./...` 与 `go list -deps ./...` 均通过（Go 编译期本就拒绝 import cycle），即结构无环。
2. **root-fronts-subpackages**：领域之间只经根包互引（`dao`/`parser`/`source`/`process` 各自重导出门面），
   不存在 `process/* → dao/store`、`role/* → source/tailer` 这类跨域子包引用。
3. **⚠️ 唯一例外：`backfill → parser/filter`**（不经 `parser` 根包）。backfill 直接用
   `filter.CompileToSQL(include, exclude)` 把 expr 选择 filter 编译成 Presto WHERE 下推到 TA SQL；
   这是回填（非上报数据路径）刻意保留的少数直引子包。除此之外**没有**第二条跨域进子包的边。
4. **`role/api` ↔ `backfill` 不成环**：`role/api → backfill`（`Engine.RunBackfill` 内嵌 runner），
   `backfill → process`（用 `process.Counters` 类型）但**不** import `role/api`；event 回填路径经一个把
   `lines` 喂给 `Engine.Upload` 的**注入回调**复用上报管线，从而打破 `api ↔ backfill` 的潜在环。
5. **`client` / `config` 的边界**：公开 SDK `client` 只 import `role/api`（经 `api.BackfillConfig` 等别名拿配置，
   不直接 import `internal/backfill`、`internal/source`、`internal/dao`，由 importboundary 测试守门）；
   `config` 是唯一允许「横向 import 所有领域」的包，但只为注册配置键，不参与运行时数据流。
6. **`cfgtree` 是叶子**：依赖中立，不 import 任何 internal 包；`logging` 仅依赖 `cfgtree`。
   二者是被最广泛复用的底座（图 §1 中几乎每个节点都最终汇到它们）。

## 5. v1.7 相对 v1.6（file）新增的依赖

v1.7 = 当前 v1.6（`file` 源 + v1.5.11 daemon 修复）逻辑性叠加 **backfill 域**。新增的依赖边只有：

- `internal/backfill →` `dao` · `parser/filter` · `process` · `logging` · `cfgtree`（全新顶层域）
- `internal/parser/filter →` `logging`（`sql.go` 的 `CompileToSQL`，本就在 filter 子包内，不新增跨域边）
- `internal/role/api →` `backfill`、`internal/role/cli →` `backfill`（角色层接入回填面）
- `config →` `backfill`（注册 `backfill.*` 键）

**未**引入 worker / taskqueue 域（那是旧 v1.6.2 线，不在 v1.7）。
