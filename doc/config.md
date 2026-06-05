# tango 配置参考

本文是**字段参考**（每个键的含义、required/optional、默认值）。命令行用法见
[usage.md](usage.md)，设计与数据流见 [arch.md](arch.md)。完整可运行样例见
[examples/config](../examples/config)。

所有角色共用**单一 schema**，且**配置键路径 = 消费它的包路径**（`internal/` 下）。
最外层 `config` 包不定义任何字段，只做加载/覆盖；每个角色只取自己需要的段。

**角色由配置键 `role.mode` 指定**（`daemon`/`gateway`/`cli`，默认 `daemon`），不再用子命令。`role.mode=cli` 是 gateway `POST /upload` 的控制台等价入口（从 stdin 读取）。注意区分两个 mode：`role.mode` 选运行角色，`process.mode` 选上传策略（`single`/`batch`/`pipeline`）。**三个途径完全一致**：
每个配置键都可经 配置文件 / `TANGO_*` 环境变量 / `--<键>` 命令行参数 三种方式设置，键名相同、可互换；
唯一例外是 `--config <path>`（只有命令行、不是配置键）。

## 角色（role.mode）与配置段

| role.mode | 主要配置段 |
|------|--------|
| `daemon`（默认） | `logging` · `dao` · `parser` · `source` · `process` |
| `gateway` | `logging` · `dao` · `parser` · `process` · `role.gateway` |
| `cli` | `logging` · `dao` · `parser` · `process` · `role.cli`（`function=data` 时仅 `logging` · `dao` · `role.cli`） |

`--config` 留空时在**二进制同级目录**按 `tango.yaml → tango.yml → tango.json` 取首个存在者。
文件缺失或解析为空时静默跳过（回退到默认值 + 环境变量 + flag）。

## 来源与优先级（低 → 高）

1. 内置默认值
2. 配置文件（YAML/JSON，按扩展名识别）
3. `TANGO_*` 环境变量
4. CLI flag（**每个配置键都有同名 flag**，如 `--dao.mongo.uri`；只有用户显式传入的 flag 才覆盖文件/环境变量。`--config` 是文件路径、非配置键）

### 环境变量映射

`TANGO_` 前缀 + 嵌套键 `.` → `_`、转大写。

| 配置键 | 环境变量 |
|--------|----------|
| `dao.mongo.uri` | `TANGO_DAO_MONGO_URI` |
| `logging.level` | `TANGO_LOGGING_LEVEL` |
| `process.mode` | `TANGO_PROCESS_MODE` |
| `role.mode` | `TANGO_ROLE_MODE` |
| `source.tailer.tailMode` | `TANGO_SOURCE_TAILER_TAILMODE` |
| `role.gateway.addr` | `TANGO_ROLE_GATEWAY_ADDR` |
| `role.cli.function` | `TANGO_ROLE_CLI_FUNCTION` |

---

## Schema（键路径 = 包路径）

### logging（所有角色） → `internal/logging`

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `logging.level` | optional | `info` | `debug`/`info`/`warn`/`error` |
| `logging.format` | optional | `text` | `text`/`json` |

### dao（所有角色） → `internal/dao`

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `dao.mongo.uri` | **required** | — | MongoDB 连接串；库名取自 URI 路径 |
| `dao.mongo.connectTimeout` | optional | `10s` | 初次连接握手超时 |
| `dao.mongo.serverSelectionTimeout` | optional | `30s` | 选择可用节点超时 |
| `dao.store.maxElapsedTime` | optional | `10s` | 单次 bulk-write 退避重试总时长上限（属于 store，不属于 mongo 连接） |

### parser（daemon / cli） → `internal/parser/filter`

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `parser.filter.include` | optional | `[]`(全放行) | expr 表达式，OR 语义命中其一即保留 |
| `parser.filter.exclude` | optional | `[]` | 命中其一即丢弃（在 include 之后） |

### source（daemon） → `internal/source/tailer`

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `source.tailer.logPattern` | **required**（daemon） | — | 至少一条 glob/正则，匹配要追尾的日志文件路径 |
| `source.tailer.tailMode` | optional | `hybrid` | `hybrid`/`poll`/`event` |
| `source.tailer.rescanInterval` | optional | `30s` | 重新扫描新文件的间隔 |
| `source.tailer.pollInterval` | optional | `200ms` | poll/hybrid 模式轮询节奏 |
| `source.tailer.maxLineBytes` | optional | `10485760`(10MB) | 单行最大字节 |

### process（daemon / cli） → `internal/process{,/pipeline}`

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `process.mode` | optional | `batch` | 上传策略：`single`/`batch`/`pipeline`。gateway / cli / api 统一读取该配置；daemon 常驻追尾固定使用 pipeline 语义 |
| `process.batchSize` | optional | `1000` | single/batch 策略 bulk-write 批大小 |
| `process.pipeline.batchSize` | optional | `1000` | pipeline 单次 bulk-write 目标条数 |
| `process.pipeline.batchSizeMin` | optional | `0`(自动 = batchSize/4) | 自适应下限 |
| `process.pipeline.batchSizeMax` | optional | `0`(自动 = batchSize*2) | 自适应上限 |
| `process.pipeline.batchWorkers` | optional | `2` | 并行写 worker 数 |
| `process.pipeline.flushInterval` | optional | `1s` | 未满批次刷新间隔 |
| `process.pipeline.channelBuffer` | optional | `0`(自动 = batchSize*2) | 每 worker 通道缓冲 |
| `process.pipeline.deadLetterCap` | optional | `128` | 每 worker 死信批容量 |

### role.mode → `internal/role`

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `role.mode` | optional | `daemon` | 运行角色：`daemon` / `gateway` / `cli`。替代旧的角色子命令，单一 `tango` 二进制据此分发。 |

### role.daemon（daemon） → `internal/role/daemon`

暂无字段：daemon 完全由顶层 `logging`/`dao`/`parser`/`source`/`process` 驱动。该段仅为 schema 对称保留。

### role.gateway（gateway） → `internal/role/gateway`

只含 gateway 专属字段；上传的处理参数与过滤器复用顶层共享模块 `process.*` 与 `parser.filter.*`
（与 daemon 同一套），不在 `role.gateway` 下重复定义。

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `role.gateway.addr` | optional | `:8080` | HTTP 监听地址 |

gateway 同时暴露独立的 Mongo Data API 路径 `POST /data`（与 `/upload` 互不影响，无额外配置项，完全放开）。
用法与 action/字段见 [usage.md](usage.md#mongo-data-apidata--cli-data--apidata)。

### role.cli（cli） → `internal/role/cli`

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `role.cli.function` | optional | `upload` | cli 角色功能：`upload`（从 stdin 读日志数组上报）或 `data`（从 stdin 读一个 EJSON Mongo Data API 请求、输出 EJSON 响应，等价 `POST /data`） |

完整样例：[daemon](../examples/config/daemon/daemon.max.yaml)、
[gateway](../examples/config/gateway/gateway.max.yaml)、
[cli data](../examples/config/cli/cli.data.yaml)。

---

## 上报 filter

上报 filter（顶层 `parser.filter`，daemon 与 gateway 上传共享）维度为
`#type` / `#event_name` / `properties.*`，用 `include` / `exclude`（expr-lang）
表达式。示例（作用于扁平化记录，`#` 前缀字段可直接引用）：

```yaml
parser:
  filter:
    include:
      - '#type == "track" && #event_name in ["login", "pay"]'
      - '#type startsWith "user_"'
    exclude:
      - 'properties.is_loadtest == true'
```

被过滤掉的记录**不写 dead_letter**，是有意丢弃。
