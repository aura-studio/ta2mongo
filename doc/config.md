# tango 配置参考

本文是**字段参考**（每个键的含义、required/optional、默认值）。命令行用法见
[usage.md](usage.md)，设计与数据流见 [arch.md](arch.md)。完整可运行样例见
[examples/config](../examples/config)。

所有角色共用**单一 schema**，且**配置键路径 = 消费它的包路径**（`internal/` 下）。
最外层 `config` 包不定义任何字段，只做加载/覆盖；每个角色只取自己需要的段。

## 配置文件与角色命令

| 角色 | 命令 | 主要配置段 | `--config` 留空时默认读取 |
|------|--------|--------|------|
| Daemon | `tango daemon` | `logging` · `dao` · `parser` · `source` · `process` | `daemon.{yaml,yml,json}` |
| Gateway | `tango gateway` | `logging` · `dao` · `parser` · `process` · `role.gateway` | `gateway.{yaml,yml,json}` |
| CLI | `tango cli` | `logging` · `dao` · `parser` · `process` | `cli.{yaml,yml,json}` |

默认文件在**二进制同级目录**按 `yaml → yml → json` 取首个存在者。文件缺失或解析为空时静默跳过
（回退到默认值 + 环境变量 + flag）。

## 来源与优先级（低 → 高）

1. 内置默认值
2. 配置文件（YAML/JSON，按扩展名识别）
3. `TANGO_*` 环境变量
4. CLI flag（flag 名即配置键，如 `--dao.mongo.uri`；`--config` 是文件路径）

### 环境变量映射

`TANGO_` 前缀 + 嵌套键 `.` → `_`、转大写。

| 配置键 | 环境变量 |
|--------|----------|
| `dao.mongo.uri` | `TANGO_DAO_MONGO_URI` |
| `logging.level` | `TANGO_LOGGING_LEVEL` |
| `source.tailer.tailMode` | `TANGO_SOURCE_TAILER_TAILMODE` |
| `role.gateway.addr` | `TANGO_ROLE_GATEWAY_ADDR` |

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
| `process.batchSize` | optional | `1000` | single/batch 策略 bulk-write 批大小 |
| `process.pipeline.batchSize` | optional | `1000` | pipeline 单次 bulk-write 目标条数 |
| `process.pipeline.batchSizeMin` | optional | `0`(自动 = batchSize/4) | 自适应下限 |
| `process.pipeline.batchSizeMax` | optional | `0`(自动 = batchSize*2) | 自适应上限 |
| `process.pipeline.batchWorkers` | optional | `2` | 并行写 worker 数 |
| `process.pipeline.flushInterval` | optional | `1s` | 未满批次刷新间隔 |
| `process.pipeline.channelBuffer` | optional | `0`(自动 = batchSize*2) | 每 worker 通道缓冲 |
| `process.pipeline.deadLetterCap` | optional | `128` | 每 worker 死信批容量 |

### role.daemon（daemon） → `internal/role/daemon`

暂无字段：daemon 完全由顶层 `logging`/`dao`/`parser`/`source`/`process` 驱动。该段仅为 schema 对称保留。

### role.gateway（gateway） → `internal/role/gateway`

只含 gateway 专属字段；上传的处理参数与过滤器复用顶层共享模块 `process.*` 与 `parser.filter.*`
（与 daemon 同一套），不在 `role.gateway` 下重复定义。

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `role.gateway.addr` | optional | `:8080` | HTTP 监听地址；`--addr` 覆盖 |
| `role.gateway.defaultMode` | optional | `batch` | 请求未带 `mode` 时的策略：`single`/`batch`/`pipeline` |

完整样例：[daemon](../examples/config/daemon/daemon.max.yaml)、
[gateway](../examples/config/gateway/gateway.max.yaml)。

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
