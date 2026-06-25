# tango v1.6 需求文档（file 存量文件一次性导入）

> 状态：v1.6 已发布。本文按**实际发布拓扑**描述 v1.6 = **file**（本地存量文件一次性导入）。
> 历史：file 原是 2026-06-12「三段规划」（file → backfill → taskqueue+worker）的第一段；其后
> **backfill 划入 v1.7**（新域 `internal/backfill`），**taskqueue + worker 暂未纳入**。backfill / worker 的
> 原始需求条目见 git 历史（本文件早前修订）或后续 v1.7 文档。本文档自洽，不依赖已删除的
> `diff.md` / `todo.md`；v1.0 源码参照：`git show 8bc899b:<path>`（tag v1.0.2）。

## 1. 背景与目标

v1.0→v1.1 的大收敛把 tango 从「全功能数据接入/控制平台」砍成纯上报引擎，删除了：
公开 SDK、operator 命令树、taskqueue+worker、TA-OpenAPI backfill、临时 SQL 导入、
remote config、文件单次上传+断点续传（`Files` / `_tango_fileupload`）。

其中多数能力已在前序版本按新架构回归：

| 被删能力 | 回归形态 | 版本 |
| --- | --- | --- |
| 临时 SQL | `dao/sql` 只读 SQL Data API（依赖 mongosql） | v1.3 / v1.4 |
| remote config | `cfgsync`（发布/拉取/热替换 filter） | v1.5 |
| 公开 Go SDK | `client/`（只依赖 `internal/role/api`，import 边界测试卡守） | v1.5 |
| operator 命令树 | cli `role.cli.function=*` | v1.1 起渐进 |
| 文件单次上传 | **`file` 存量文件一次性导入（本版）** | **v1.6** |

**v1.6 目标：加回「文件单次上传」一块**，按现行架构（cfgtree / source / parser / process /
dao / role）重设，不回滚 v1.0 实现——简化为**无断点续传**的纯有限 Source。

基线前提：go 钉 **1.25.5**；mongo **driver v2**；mongosql 不动；所有写路径 DocumentDB 兼容。

## 2. 范围决策（file）

- file **无 checkpoint / 无断点续传**：仿照 daemon 对文件的消费，纯有限 Source；
  重跑全量重导，幂等由写模型保证（event 按 `#uuid` `$setOnInsert`、user 按 `_ts` 守卫）；
  **不建** `_tango_fileupload` 集合，零持久状态、零新集合、零恢复协议。
- **显式文件路径**：`source.file.paths` 是显式路径列表——**不支持 glob、不支持目录**
  （目录路径 `os.Stat` 检出后跳过、不展开）、**不依赖 tailer**。
- 入口**只在 cli（`function=file`）+ api 库（`Engine.File`）+ client SDK（`Client.File`）三处**；
  gateway / daemon **不设** file 入口。client 新面经 Engine 中转，守 import 边界。

### 能力矩阵（v1.6 新增入口）

| 面 | 新增 |
| --- | --- |
| Engine（`internal/role/api`） | `File(ctx, *FileConfig)` |
| client/ 公开 SDK | `File(ctx, paths...)`（经 Engine 中转，守 import 边界） |
| cli | `role.cli.function=file`（读 `source.file.paths`，不读 stdin） |
| gateway / daemon | 不动（无 file 入口） |

## 3. 设计与验收

**需求**：把一批已落盘的存量日志文件（**显式文件路径**）一次性灌入上报链路，读完即止。
与既有能力的边界：tailer=常驻追新增；cli `upload`=stdin；file=存量文件、有限。

**设计**：

- `internal/source/file` 实现 `source.Source`：按 `paths` 列出的显式文件路径 → 逐文件从头读到
  EOF → 发完关 channel；ctx 取消即提前关闭。目录路径 `os.Stat` 检出后跳过、不展开；**不 import tailer**。
  行读取语义对齐 tailer（maxLineBytes，自有实现）。无 dao 依赖（source 层保持干净），无新集合。
- 配置 `source.file.*`：`paths` / `maxLineBytes`（键=包路径惯例）。
- source 门面新增 `NewFile(cfg)`，对齐 `NewLines` / `NewReader` / `NewTailer`。
- `Engine.File(ctx)` = 构造 source → 复用 `Engine.Run`（薄封装）。
- cli `role.cli.function=file`（config `Validate` 同步增加）+ client `File` 面。

**验收**：

- source 单测：显式路径导入 / 目录跳过 / glob 字面不展开 / 行边界 / maxLineBytes / ctx 取消 / 空列表。
- 集成：喂引擎写真实 DocumentDB；**重复导入幂等断言**（重跑结果一致）。
- 文档（arch/usage/config + 图 C）+ 示例 `examples/config/cli/cli.file.{min,max}.{yaml,json}`。
- 门禁全绿（gofmt/vet/全量 test，连真实 DocumentDB）→ 合入 v1.6 → tag `v1.6.0`。

## 4. 约束

- DocumentDB 红线：update 一律普通操作符（无聚合管道）；判错只认数字 code；
  集成测试连真实 DocumentDB（`TANGO_TEST_MONGO_URI` 机制现成）。
- client/ 新面一律经 `internal/role/api` Engine 中转，import 边界测试卡守（client 不 import `internal/source`）。
- 配置键路径 = 包路径（`source.file.*`）；max 示例仅写实际用到的段。

## 5. 不在 v1.6 范围

- **backfill**（TA OpenAPI 历史数据回填，新域 `internal/backfill`）—— **已划入 v1.7**。
- **taskqueue + worker** 控制面（`role.mode=worker` / `POST /publish/backfill` / cli `publish`）—— 暂未纳入。
- 同步执行的 `POST /backfill`。
- gateway / daemon 的 file 入口（仅 cli + api + client）。
- file 的 checkpoint / 断点续传与 `_tango_fileupload` 集合。
- `/ingest` 接口（被 `/upload` 取代）。
- `source/taapi` 占位的兑现。
