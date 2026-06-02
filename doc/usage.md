# tango 命令行使用说明

tango 是单一二进制，但启动方式按运行角色划分：

```bash
tango report run                 # 常驻采集上报服务
tango worker run                 # 常驻任务 worker 服务
tango gateway serve              # 常驻 HTTP/REST gateway
tango operator <subcommand>      # 一次性操作命令
```

旧命令仍保留兼容：

```bash
tango daemon standalone          # deprecated: use tango report run
tango daemon agent               # deprecated: use tango report run + tango worker run, or tango profile managed
tango client <subcommand>        # deprecated: use tango operator <subcommand>
tango client serve               # deprecated: use tango gateway serve
```

## 通用规则

- `--config <path>`：配置文件路径，支持 `.yaml` / `.yml` / `.json`。文件不存在时静默跳过，回退到默认值 + 环境变量 + flag。
- 留空时各角色命令在二进制同级目录查找自己的默认文件。
- CLI flag 名即配置键（viper 原生层级）。角色命令使用统一 schema 的键：`--runtime.mongo.uri`、`--runtime.logging.level`、`--remoteConfig.enabled`、`--tasks.instanceID`（worker 另提供 `--instanceID` 简写别名）。旧 daemon 命令仍用 `--generic.mongo.uri`、`--agent.instanceID`。
- 所有键均可用 `TANGO_*` 环境变量覆盖，嵌套键 `.` 转 `_` 并大写（如 `runtime.mongo.uri` → `TANGO_RUNTIME_MONGO_URI`）。

| 角色命令 | 默认配置文件 | 文件 schema |
|---|---|---|
| `tango report run` | `report.{yaml,yml,json}` | 统一 RoleConfig（runtime + report + remoteConfig） |
| `tango worker run` | `worker.{yaml,yml,json}` | 统一 RoleConfig（runtime + tasks + remoteConfig） |
| `tango gateway serve` | `gateway.{yaml,yml,json}` | 统一 RoleConfig（runtime + gateway + upload + tasks） |
| `tango operator ...` | `operator.{yaml,yml,json}` | 统一 RoleConfig（runtime + upload + backfill + tasks） |
| `tango profile local` | `local.{yaml,yml,json}` → `standalone.{yaml,yml,json}` | 旧 daemon schema（兼容） |
| `tango profile managed` | `managed.{yaml,yml,json}` → `agent.{yaml,yml,json}` | 旧 daemon schema（兼容） |

> profile 与 daemon / client 兼容命令继续读取旧的 daemon / client 文件 schema；report / worker / gateway / operator 角色命令读取统一 RoleConfig schema。

## Report Service

```bash
tango report run --config report.yaml
tango report run --runtime.mongo.uri mongodb://localhost:27017/tango --remoteConfig.enabled
```

职责：

- 追尾 `report.source.logPattern` 匹配的日志文件。
- 解析 TA JSON line。
- 应用 report filter。
- 做 identity resolve。
- 批量写入 MongoDB。
- 可选启用 remote config hot reload。

常用参数：

| 参数 | 说明 |
|---|---|
| `--runtime.mongo.uri` | MongoDB 连接串（配置键 `runtime.mongo.uri`） |
| `--runtime.logging.level` | 日志级别（配置键 `runtime.logging.level`） |
| `--remoteConfig.enabled` | 启用远端配置热更新（配置键 `remoteConfig.enabled`） |

## Task Worker Service

```bash
tango worker run --config worker.yaml --instanceID worker-1
tango worker run --runtime.mongo.uri mongodb://localhost:27017/tango --instanceID worker-1
```

职责：

- 注册实例心跳。
- 从 MongoDB task queue claim 任务。
- 续约 lease。
- 执行 `report-sync` / `backfill` / `sql` 任务。
- Complete / Fail / Reap 任务。

常用参数：

| 参数 | 说明 |
|---|---|
| `--tasks.instanceID` | worker 实例 ID，必填（配置键 `tasks.instanceID`） |
| `--instanceID` | 简写别名，映射到 `tasks.instanceID` |
| `--runtime.mongo.uri` | MongoDB 连接串 |
| `--runtime.logging.level` | 日志级别 |

`worker run` 不要求 `report.source.logPattern`，因为它不启动文件追尾管线。

## HTTP Gateway Service

```bash
tango gateway serve --config gateway.yaml --addr :8080
```

gateway 是常驻 HTTP 服务，读取统一 RoleConfig（`runtime` + `gateway` + `upload` + `tasks`），底层复用 Go SDK。它不再作为 `client` 的形态描述。`--addr` 覆盖 `gateway.addr`。

| 方法 | 路径 | body | 功能 |
|---|---|---|---|
| GET | `/healthz` | - | 健康检查 |
| POST | `/ingest` | `{"line":...}` 或 `{"lines":[...]}` | 字符串上报 |
| POST | `/upload` | `{"patterns":[...],"batchSize":N}` | 文件上报 |
| POST | `/backfill` | `{}` | 直接执行 backfill |
| POST | `/sql` | `{"sql":"..."}` | 直接执行 SQL |
| POST | `/publish/report-sync` | `{"include":[],"exclude":[],"target":""}` | 发布 report-sync 任务 |
| POST | `/publish/backfill` | `{"payload":{...},"target":""}` | 发布 backfill 任务 |
| POST | `/publish/sql` | `{"sql":"...","table":"event","target":""}` | 发布 SQL 任务 |

## Operator CLI

```bash
tango operator ingest [json-line ...]
tango operator upload --logPattern '/var/log/ta/.*\.log'
tango operator backfill
tango operator sql 'SELECT * FROM v_event_35 LIMIT 10'
tango operator publish report-sync --include '#type == "track"'
tango operator publish backfill --target worker-1
tango operator publish sql 'SELECT * FROM v_event_35 LIMIT 10'
```

| 子命令 | 功能 | 关键 flag |
|---|---|---|
| `ingest` | 字符串单次上报，无重传 | - |
| `upload` | 文件单次上报，有断点续传 | `--logPattern` |
| `backfill` | 执行历史回填 | - |
| `sql <statement>` | 执行临时 SQL 并导入 | - |
| `publish report-sync` | 发布上报同步任务 | `--include`、`--exclude`、`--target` |
| `publish backfill` | 发布回填任务 | `--target` |
| `publish sql <statement>` | 发布临时 SQL 任务 | `--target` |

## Profile 兼容层

profile 是组合启动方式，不是基础角色。

```bash
tango profile local
tango profile managed --agent.instanceID node-1
```

| profile | 等价关系 | 建议 |
|---|---|---|
| `local` | 旧 `daemon standalone` | 新部署优先使用 `tango report run` |
| `managed` | 旧 `daemon agent`，同进程启动 report + worker | 新部署优先拆成 `tango report run` + `tango worker run` |

## Go SDK

```go
import "rocket-nano/tools/tango/client"

cli, _ := client.New(ctx, client.WithURI("mongodb://localhost:27017/tango"))
defer cli.Close()
cli.EnsureIndexes(ctx)

cli.Ingest(ctx, line)
cli.UploadFiles(ctx, client.UploadRequest{...})
cli.RunBackfill(ctx, cc.BackfillRuntime())
cli.ExecuteSQL(ctx, cc.SQLRuntime(), "SELECT ...")
cli.PublishReportSync(ctx, include, exclude, "")
```
