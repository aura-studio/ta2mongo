# tango 命令行使用说明

重构后 tango 拆为两个二进制：`tangod`（daemon 角色）与 `tango`（client 角色）。

## 通用

- `--config <path>`：配置文件路径，支持 `.yaml` / `.yml` / `.json`（按扩展名识别；文件不存在则静默跳过，回退到默认值 + 环境变量 + flag）。
- `--mongoURI`、`--logLevel`：常用覆盖项。
- 所有键均可用 `TANGO_*` 环境变量覆盖（如 `TANGO_MONGO_URI`、`TANGO_AGENT_INSTANCEID`）。

---

## `tangod` —— daemon 角色

```bash
tangod --config daemon.yaml
```

- 追尾 `source.logPattern` 匹配的日志，应用 `reportFilter`，写入 MongoDB。
- `agent.enabled: true` 时进程内同时运行 agent（注册心跳、领取/执行任务）。
- `--instanceID`：等价 `agent.instanceID`，开启 agent 时必填。

---

## `tango` —— client 角色

```
tango <subcommand> [flags] --config client.yaml
```

| 子命令 | 功能 | 关键 flag |
|--------|------|-----------|
| `tango ingest [json ...]` | 字符串单次上报（无重传），参数或 stdin 逐行 | — |
| `tango upload` | 文件单次上报（有重传/断点续传） | `--logPattern`（覆盖 `fileUpload.logPattern`） |
| `tango backfill` | 执行历史回填（用 `backfill` + `backfillFilter`） | — |
| `tango sql <statement>` | 执行临时 SQL 并导入 | — |
| `tango publish report-sync` | 发布上报同步任务 | `--include` `--exclude` `--target` |
| `tango publish backfill` | 发布回填任务（用配置里的 backfill 段） | `--target` |
| `tango publish sql <statement>` | 发布临时 SQL 任务 | `--target` |
| `tango serve` | 启动 HTTP/REST 服务 | `--addr`（覆盖 `server.addr`） |

### HTTP/REST 端点（`tango serve`）

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
