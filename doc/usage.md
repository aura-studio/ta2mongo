# tango 命令行使用说明

tango 是单一二进制，按运行角色划分。所有上报角色共享同一引擎，区别在数据来源/入口：

```bash
tango daemon    # 常驻采集上报：追尾日志文件 → 写 MongoDB
tango gateway   # 常驻 HTTP gateway：暴露 /upload（httpbody 源）
tango cli       # 一次性：从 stdin 读日志数组上报（--mode 选策略）
# api 角色无命令：作为 Go 库被业务代码 import（见“作为库使用”）
```

## 通用规则

- **角色由子命令指定**（`daemon` / `gateway` / `cli`），不在配置里指明。
- **三个途径完全一致**：每个配置键都可经 ① 配置文件、② `TANGO_*` 环境变量、③ `--<键>` 命令行参数 三种方式设置，键名一致。优先级（低→高）：默认值 < 文件 < 环境变量 < 命令行参数。
  - 文件键：`dao.mongo.uri`
  - 环境变量：嵌套键 `.` 转 `_` 并大写加 `TANGO_` 前缀 → `TANGO_DAO_MONGO_URI`
  - 命令行：flag 名即键路径 → `--dao.mongo.uri`
  - **配置键路径 = 包路径**（`internal/` 下）：`logging.*`、`dao.mongo.*`、`dao.store.*`、`parser.filter.*`、`source.tailer.*`、`process.*`、`role.gateway.*`。
- **唯一的例外**：`--config <path>`（配置文件路径，`.yaml`/`.yml`/`.json`）只有命令行这一种途径；它不是配置键。留空时各命令在二进制同级目录查找 `<role>.{yaml,yml,json}`，缺失则静默回退到默认值 + 环境变量 + flag。
- 另有运行参数 `--mode`（仅 `tango cli`，选择本次 single/batch/pipeline），它是运行时参数，也不是配置键。

| 角色命令 | 默认配置文件 | 主要配置段 |
|---|---|---|
| `tango daemon` | `daemon.{yaml,yml,json}` | `logging` · `dao` · `parser` · `source` · `process` |
| `tango gateway` | `gateway.{yaml,yml,json}` | `logging` · `dao` · `parser` · `process` · `role.gateway` |
| `tango cli` | `cli.{yaml,yml,json}` | `logging` · `dao` · `parser` · `process` |

## Daemon Service

```bash
tango daemon --config daemon.yaml
tango daemon --dao.mongo.uri mongodb://localhost:27017/tango
```

职责：追尾 `source.tailer.logPattern` 匹配的日志文件 → 解析 TA JSON → 上报 filter → identity resolve → 流水线批量写 MongoDB。

常用参数（任意配置键都有同名 flag，下面只列最常用的）：

| 参数 | 说明 |
|---|---|
| `--dao.mongo.uri` | MongoDB 连接串（配置键 `dao.mongo.uri`） |
| `--logging.level` | 日志级别（配置键 `logging.level`） |
| `--source.tailer.logPattern` | 追尾文件模式（配置键 `source.tailer.logPattern`） |

## HTTP Gateway Service

```bash
tango gateway --config gateway.yaml
tango gateway --role.gateway.addr :8080      # 监听地址即普通配置键
```

gateway 是常驻 HTTP 服务，读取共享段 `logging` + `dao` + `parser` + `process`，外加 gateway 专属的
`role.gateway`（`addr` / `defaultMode`）。上传的批量/流水线参数与过滤器即顶层 `process.*` / `parser.filter.*`。
监听地址用配置键 `role.gateway.addr`（文件 / `TANGO_ROLE_GATEWAY_ADDR` / `--role.gateway.addr` 三选一）。

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

eng, _ := api.New(ctx, &dao.Config{Mongo: &daomongo.Config{URI: "mongodb://localhost:27017/tango"}}, nil, nil)
defer eng.Close()
eng.EnsureIndexes(ctx)

res, _ := eng.Upload(ctx, process.ModeBatch, lines)            // batch
res, _ = eng.Upload(ctx, process.ModeSingle, []string{line})  // single（逐行即时写）
res, _ = eng.Upload(ctx, process.ModePipeline, lines)         // pipeline（异步流水线）
```
