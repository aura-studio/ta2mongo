# tango 命令行使用说明

tango 是单一二进制，**运行角色由配置键 `role.mode` 选择**（不再用子命令）。所有上报角色共享同一引擎，区别在数据来源/入口：

```bash
tango --config daemon.yaml      # role.mode=daemon：常驻采集上报，追尾日志文件 → 写 MongoDB
tango --config gateway.yaml     # role.mode=gateway：常驻 HTTP gateway，暴露 /upload（httpbody 源）
tango --config cli.yaml         # role.mode=cli：一次性，从 stdin 读日志数组上报（process.mode 选策略）
tango --role.mode gateway       # 角色也可用 flag / 环境变量覆盖（等价 TANGO_ROLE_MODE=gateway）
# api 角色无运行入口：作为 Go 库被业务代码 import（见“作为库使用”）
```

## 通用规则

- **角色由配置键 `role.mode` 指定**（`daemon` / `gateway` / `cli`，默认 `daemon`），和其它配置键一样可经文件 / `TANGO_ROLE_MODE` / `--role.mode` 设置；不再有角色子命令。
- **三个途径完全一致**：每个配置键都可经 ① 配置文件、② `TANGO_*` 环境变量、③ `--<键>` 命令行参数 三种方式设置，键名一致。优先级（低→高）：默认值 < 文件 < 环境变量 < 命令行参数。
  - 文件键：`dao.mongo.uri`
  - 环境变量：嵌套键 `.` 转 `_` 并大写加 `TANGO_` 前缀 → `TANGO_DAO_MONGO_URI`
  - 命令行：flag 名即键路径 → `--dao.mongo.uri`
  - **配置键路径 = 包路径**（`internal/` 下）：`logging.*`、`dao.mongo.*`、`dao.store.*`、`parser.filter.*`、`source.tailer.*`、`process.*`、`role.gateway.*`。
- **唯一的例外**：`--config <path>`（配置文件路径，`.yaml`/`.yml`/`.json`）只有命令行这一种途径；它不是配置键。留空时在二进制同级目录查找 `tango.{yaml,yml,json}`，缺失则静默回退到默认值 + 环境变量 + flag。
- 不要混淆两个 `mode`：`role.mode` 选**运行角色**（daemon/gateway/cli）；`process.mode` 选**上传策略**（`single`/`batch`/`pipeline`，默认 `batch`，CLI/gateway/api 共用）。

| role.mode | 主要配置段 |
|---|---|
| `daemon`（默认） | `logging` · `dao` · `parser` · `source` · `process` |
| `gateway` | `logging` · `dao` · `parser` · `process` · `role.gateway` |
| `cli` | `logging` · `dao` · `parser` · `process` |

## Daemon Service

```bash
tango --config daemon.yaml                                       # role.mode=daemon（写在配置里）
tango --role.mode daemon --dao.mongo.uri mongodb://localhost:27017/tango
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
tango --config gateway.yaml                          # role.mode=gateway（写在配置里）
tango --role.mode gateway --role.gateway.addr :8080  # 角色与监听地址都是普通配置键
```

gateway 是常驻 HTTP 服务，读取共享段 `logging` + `dao` + `parser` + `process`，外加 gateway 专属的
`role.gateway`（`addr`）。上传模式、批量/流水线参数与过滤器即顶层 `process.*` / `parser.filter.*`。
监听地址用配置键 `role.gateway.addr`（文件 / `TANGO_ROLE_GATEWAY_ADDR` / `--role.gateway.addr` 三选一）。

| 方法 | 路径 | body | 功能 |
|---|---|---|---|
| GET | `/healthz` | - | 健康检查 |
| POST | `/upload` | `{"line":...,"lines":[...]}` | 日志数组上报，策略由 `process.mode` 决定 |

请求体的 `line` / `lines` 会被包成一个 httpbody 源，按 `process.mode` 选 single / batch / pipeline
三种上传策略之一写入 MongoDB，返回本次统计（行数 / user / event / 死信等）。

## CLI Upload

```bash
# 从 stdin 读 newline 分隔的 TA JSON 日志数组，按 process.mode 上报，打印统计 JSON
cat events.ndjson | tango --role.mode cli --process.mode batch --dao.mongo.uri mongodb://localhost:27017/tango
```

`process.mode` 取 `single` / `batch` / `pipeline`（默认 `batch`），可来自配置文件、`TANGO_PROCESS_MODE` 或 `--process.mode`。
`role.mode=cli` 是 gateway `POST /upload` 的控制台等价入口（从 stdin 读取）。

## 作为库使用（api 角色）

`internal/role/api` 是可复用引擎（仅本仓库内部 import）。gateway / cli 都内嵌它，三者上传能力完全一致：

```go
import (
    "rocket-nano/tools/tango/internal/dao"
    daomongo "rocket-nano/tools/tango/internal/dao/mongo"
    "rocket-nano/tools/tango/internal/process"
    "rocket-nano/tools/tango/internal/role/api"
)

eng, _ := api.New(ctx, &dao.Config{Mongo: &daomongo.Config{URI: "mongodb://localhost:27017/tango"}}, &process.Config{Mode: string(process.ModeBatch)}, nil)
defer eng.Close()
eng.EnsureIndexes(ctx)

res, _ := eng.Upload(ctx, lines)
```
