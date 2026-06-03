# tango 命令行使用说明

tango 是单一二进制，按运行角色划分。所有上报角色共享同一引擎，区别在数据来源/入口：

```bash
tango daemon    # 常驻采集上报：追尾日志文件 → 写 MongoDB
tango gateway   # 常驻 HTTP gateway：暴露 /upload（httpbody 源）
tango cli       # 一次性：从 stdin 读日志数组上报（--mode 选策略）
# api 角色无命令：作为 Go 库被业务代码 import（见“作为库使用”）
```

## 通用规则

- `--config <path>`：配置文件路径，支持 `.yaml` / `.yml` / `.json`。文件不存在时静默跳过，回退到默认值 + 环境变量 + flag。
- 留空时各角色命令在二进制同级目录查找自己的默认文件。
- **配置键路径 = 包路径**（`internal/` 下）。CLI flag 名即配置键，如 `--dao.mongo.uri`、`--logging.level`、`--role.gateway.addr`。
- 所有键均可用 `TANGO_*` 环境变量覆盖，嵌套键 `.` 转 `_` 并大写（如 `dao.mongo.uri` → `TANGO_DAO_MONGO_URI`）。

| 角色命令 | 默认配置文件 | 主要配置段 |
|---|---|---|
| `tango daemon` | `daemon.{yaml,yml,json}` | `logging` · `dao` · `parser` · `source` · `process` |
| `tango gateway` | `gateway.{yaml,yml,json}` | `logging` · `dao` · `role.gateway` |
| `tango cli` | `cli.{yaml,yml,json}` | `logging` · `dao` · `parser` · `process` |

## Daemon Service

```bash
tango daemon --config daemon.yaml
tango daemon --dao.mongo.uri mongodb://localhost:27017/tango
```

职责：追尾 `source.tailer.logPattern` 匹配的日志文件 → 解析 TA JSON → 上报 filter → identity resolve → 流水线批量写 MongoDB。

常用参数：

| 参数 | 说明 |
|---|---|
| `--dao.mongo.uri` | MongoDB 连接串（配置键 `dao.mongo.uri`） |
| `--logging.level` | 日志级别（配置键 `logging.level`） |

## HTTP Gateway Service

```bash
tango gateway --config gateway.yaml --addr :8080
```

gateway 是常驻 HTTP 服务，读取 `logging` + `dao` + `role.gateway` 段。`--addr` 覆盖 `role.gateway.addr`。

| 方法 | 路径 | body | 功能 |
|---|---|---|---|
| GET | `/healthz` | - | 健康检查 |
| POST | `/upload` | `{"mode":"single\|batch\|pipeline","line":...,"lines":[...]}` | 日志数组上报（mode 省略走配置默认 batch） |

请求体的 `line` / `lines` 会被包成一个 httpbody 源，按 `mode` 选 single / batch / pipeline
三种上传策略之一写入 MongoDB，返回本次统计（行数 / user / event / 死信等）。

## CLI

```bash
# 从 stdin 读 newline 分隔的 TA JSON 日志数组，按 --mode 上报，打印统计 JSON
cat events.ndjson | tango cli --mode batch --dao.mongo.uri mongodb://localhost:27017/tango
```

`--mode` 取 `single` / `batch` / `pipeline`（默认 `batch`）。

## 作为库使用（api 角色）

`internal/role/api` 是可复用引擎（仅本仓库内部 import）。gateway / cli 都内嵌它，三者上传能力完全一致：

```go
import (
    "rocket-nano/tools/tango/internal/dao"
    daomongo "rocket-nano/tools/tango/internal/dao/mongo"
    "rocket-nano/tools/tango/internal/process"
    "rocket-nano/tools/tango/internal/role/api"
)

cli, _ := api.New(ctx, &dao.Config{Mongo: &daomongo.Config{URI: "mongodb://localhost:27017/tango"}}, nil, nil)
defer cli.Close()
cli.EnsureIndexes(ctx)

res, _ := cli.Upload(ctx, process.ModeBatch, lines)            // batch
res, _ = cli.Upload(ctx, process.ModeSingle, []string{line})  // single（逐行即时写）
res, _ = cli.Upload(ctx, process.ModePipeline, lines)         // pipeline（异步流水线）
```
