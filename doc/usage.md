# tango 命令行使用说明

tango 是单一二进制，按运行角色划分为两种模式：

```bash
tango standalone                 # 常驻采集上报服务：追尾日志 → 写 MongoDB
tango gateway                    # 常驻 HTTP gateway：暴露 ingest / upload 上报接口
```

## 通用规则

- `--config <path>`：配置文件路径，支持 `.yaml` / `.yml` / `.json`。文件不存在时静默跳过，回退到默认值 + 环境变量 + flag。
- 留空时各角色命令在二进制同级目录查找自己的默认文件。
- CLI flag 名即配置键（viper 原生层级）。两种模式使用统一 schema 的键：`--runtime.mongo.uri`、`--runtime.logging.level`、`--gateway.addr`。
- 所有键均可用 `TANGO_*` 环境变量覆盖，嵌套键 `.` 转 `_` 并大写（如 `runtime.mongo.uri` → `TANGO_RUNTIME_MONGO_URI`）。

| 角色命令 | 默认配置文件 | 文件 schema（统一 RoleConfig 的子集） |
|---|---|---|
| `tango standalone` | `standalone.{yaml,yml,json}` | runtime + report |
| `tango gateway` | `gateway.{yaml,yml,json}` | runtime + gateway + upload |

## Standalone Service

```bash
tango standalone --config standalone.yaml
tango standalone --runtime.mongo.uri mongodb://localhost:27017/tango
```

职责：

- 追尾 `report.source.logPattern` 匹配的日志文件。
- 解析 TA JSON line。
- 应用上报 filter。
- 做 identity resolve。
- 批量写入 MongoDB。

常用参数：

| 参数 | 说明 |
|---|---|
| `--runtime.mongo.uri` | MongoDB 连接串（配置键 `runtime.mongo.uri`） |
| `--runtime.logging.level` | 日志级别（配置键 `runtime.logging.level`） |

## HTTP Gateway Service

```bash
tango gateway --config gateway.yaml --addr :8080
```

gateway 是常驻 HTTP 服务，读取统一 RoleConfig（`runtime` + `gateway` + `upload`），底层复用 Go SDK，只暴露上报日志接口。`--addr` 覆盖 `gateway.addr`。

| 方法 | 路径 | body | 功能 |
|---|---|---|---|
| GET | `/healthz` | - | 健康检查 |
| POST | `/ingest` | `{"line":...}` 或 `{"lines":[...]}` | 字符串上报 |
| POST | `/upload` | `{"patterns":[...],"batchSize":N}` | 文件上报 |

## Go SDK

```go
import "rocket-nano/tools/tango/client"

cli, _ := client.New(ctx, client.WithURI("mongodb://localhost:27017/tango"))
defer cli.Close()
cli.EnsureIndexes(ctx)

cli.Ingest(ctx, line)
cli.IngestBatch(ctx, lines)
cli.UploadFiles(ctx, client.UploadRequest{...})
```
