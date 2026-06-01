# tango 命令行使用说明

tango 现为**单一二进制**，通过顶层 `daemon` / `client` 子命令选择角色：

```
tango daemon  [flags]              # daemon 角色
tango client  <subcommand> [flags] # client 角色
```

## 通用

- `--config <path>`：配置文件路径，支持 `.yaml` / `.yml` / `.json`（按扩展名识别；文件不存在则静默跳过，回退到默认值 + 环境变量 + flag）。留空时自动在**二进制同级目录**查找 `tango.{yaml,yml,json}`（按此顺序取首个存在者）。daemon 与 client 共用同一默认文件名，但分别按各自的 schema 解析。
- `--mongoURI`、`--logLevel`：常用覆盖项（顶层持久 flag，两种角色共用）。
- 所有键均可用 `TANGO_*` 环境变量覆盖（嵌套键以 `.` → `_` 映射）。daemon 的连接串是 `TANGO_COMMON_MONGO_URI`（配置在 `common.mongo.uri`），client 是 `TANGO_MONGO_URI`；实例 ID 为 `TANGO_AGENT_INSTANCEID`。

---

## `tango daemon` —— daemon 角色

daemon 有两种运行模式,由**子命令**选择(不是配置开关):

```bash
tango daemon standalone --config daemon.yaml                    # 模式 1
tango daemon agent      --config daemon.yaml --instanceID node-1 # 模式 2
```

配置分三部分：**common**（logging + mongo）、**report**（上报管线，含 `source` / `pipeline` / `filter` / `remoteConfig`）、**agent**（任务 agent 设置）。两种模式**都 tail 日志做上报**,故 `report.source.logPattern` 始终必填。

| 模式 | 子命令 | 行为 |
|------|--------|------|
| **standalone** | `tango daemon standalone` | 纯上报、完全本地自治：追尾 `report.source.logPattern` 的日志,应用 `report.filter`,写入 MongoDB。不拉远端配置、不领任务（`agent` 段与 `report.remoteConfig` 被忽略）。 |
| **agent** | `tango daemon agent` | 在上报之上自动开启:① 配置同步——定期拉 `report.remoteConfig` 文档热重载 filter;② 任务派发——注册心跳、领取并执行 `report-sync` / `backfill` / `sql` 任务。`--instanceID`（等价 `agent.instanceID`）**必填**。 |

---

## `tango client` —— client 角色

```
tango client <subcommand> [flags] --config client.yaml
```

| 子命令 | 功能 | 关键 flag |
|--------|------|-----------|
| `tango client ingest [json ...]` | 字符串单次上报（无重传），参数或 stdin 逐行 | — |
| `tango client upload` | 文件单次上报（有重传/断点续传） | `--logPattern`（覆盖 `fileUpload.logPattern`） |
| `tango client backfill` | 执行历史回填（用 `backfill` + `backfillFilter`） | — |
| `tango client sql <statement>` | 执行临时 SQL 并导入 | — |
| `tango client publish report-sync` | 发布上报同步任务 | `--include` `--exclude` `--target` |
| `tango client publish backfill` | 发布回填任务（用配置里的 backfill 段） | `--target` |
| `tango client publish sql <statement>` | 发布临时 SQL 任务 | `--target` |
| `tango client serve` | 启动 HTTP/REST 服务 | `--addr`（覆盖 `server.addr`） |

### HTTP/REST 端点（`tango client serve`）

| 方法 | 路径 | body | 功能 |
|------|------|------|------|
| POST | `/ingest` | `{"line":...}` 或 `{"lines":[...]}` | 字符串上报 |
| POST | `/upload` | `{"patterns":[...],"batchSize":N}` | 文件上报（续传） |
| POST | `/backfill` | `{}` | 回填执行 |
| POST | `/sql` | `{"sql":"..."}` | SQL 执行 |
| POST | `/publish/report-sync` | `{"include":[],"exclude":[],"target":""}` | 发布上报同步任务 |
| POST | `/publish/backfill` | `{"payload":{...},"target":""}` | 发布回填任务 |
| POST | `/publish/sql` | `{"sql":"...","table":"event","target":""}` | 发布 SQL 任务 |
| GET | `/healthz` | — | 健康检查 |

### Go 库（embeddable）

```go
import "rocket-nano/tools/tango/client"

cli, _ := client.New(ctx, client.WithURI("mongodb://localhost:27017/tango"))
defer cli.Close()
cli.EnsureIndexes(ctx)

cli.Ingest(ctx, line)                              // 1) 字符串上报
cli.UploadFiles(ctx, client.UploadRequest{...})    // 2) 文件上报（续传）
cli.RunBackfill(ctx, cc.BackfillRuntime())         // 3) 回填
cli.ExecuteSQL(ctx, cc.SQLRuntime(), "SELECT ...") // 4) SQL
cli.PublishReportSync(ctx, include, exclude, "")   // 5) 任务发布
```
