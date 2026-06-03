# tango 配置参考

本文是**字段参考**（每个键的含义、required/optional、默认值）。命令行用法见
[usage.md](usage.md)，设计与数据流见 [arch.md](arch.md)。完整可运行样例见
[examples/config](../examples/config)。

两个角色命令（standalone / gateway）共用**统一 RoleConfig schema**，每个角色只取
自己需要的段。

## 配置文件与角色命令

| 角色 | 命令 | RoleConfig 子集 | `--config` 留空时默认读取 |
|------|--------|--------|------|
| standalone service | `tango standalone` | `runtime` + `report` | `standalone.{yaml,yml,json}` |
| gateway service | `tango gateway` | `runtime` + `gateway` + `upload` | `gateway.{yaml,yml,json}` |

默认文件在**二进制同级目录**按 `yaml → yml → json` 取首个存在者；各命令只读自己的
文件。文件缺失或解析为空时静默跳过（回退到默认值 + 环境变量 + flag）。

## 来源与优先级（低 → 高）

1. 内置默认值
2. 配置文件（YAML/JSON，按扩展名识别）
3. `TANGO_*` 环境变量
4. CLI flag（flag 名即配置键，如 `--runtime.mongo.uri`；`--config` 是文件路径）

### 环境变量映射

`TANGO_` 前缀 + 嵌套键 `.` → `_`、转大写。

| 配置键 | 环境变量 |
|--------|----------|
| `runtime.mongo.uri` | `TANGO_RUNTIME_MONGO_URI` |
| `runtime.logging.level` | `TANGO_RUNTIME_LOGGING_LEVEL` |
| `report.source.tailMode` | `TANGO_REPORT_SOURCE_TAILMODE` |
| `gateway.addr` | `TANGO_GATEWAY_ADDR` |

---

## 统一 RoleConfig schema

### runtime（所有角色）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `runtime.logging.level` | optional | `info` | `debug`/`info`/`warn`/`error` |
| `runtime.logging.format` | optional | `text` | `text`/`json` |
| `runtime.mongo.uri` | **required** | — | MongoDB 连接串；库名取自 URI 路径 |
| `runtime.mongo.connectTimeout` | optional | `10s` | 初次连接握手超时 |
| `runtime.mongo.serverSelectionTimeout` | optional | `30s` | 选择可用节点超时 |
| `runtime.store.maxElapsedTime` | optional | `10s` | 单次 bulk-write 退避重试总时长上限（属于 store，不属于 mongo 连接） |

> 注：`maxElapsedTime` 自 v1.1 起从 `runtime.mongo.*` 迁移到 `runtime.store.*`（重试预算归属 store 模块）。旧配置文件需把该键改名。

### report（standalone service）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `report.source.logPattern` | **required** | — | 至少一条 glob/正则，匹配要追尾的日志文件路径 |
| `report.source.tailMode` | optional | `hybrid` | `hybrid`/`poll`/`event` |
| `report.source.rescanInterval` | optional | `30s` | 重新扫描新文件的间隔 |
| `report.source.pollInterval` | optional | `200ms` | poll/hybrid 模式轮询节奏 |
| `report.source.maxLineBytes` | optional | `10485760`(10MB) | 单行最大字节 |
| `report.pipeline.batchSize` | optional | `1000` | 单次 bulk-write 目标条数 |
| `report.pipeline.batchSizeMin` | optional | `0`(自动 = batchSize/4) | 自适应下限 |
| `report.pipeline.batchSizeMax` | optional | `0`(自动 = batchSize*2) | 自适应上限 |
| `report.pipeline.batchWorkers` | optional | `2` | 并行写 worker 数 |
| `report.pipeline.flushInterval` | optional | `1s` | 未满批次刷新间隔 |
| `report.pipeline.channelBuffer` | optional | `0`(自动 = batchSize*2) | 每 worker 通道缓冲 |
| `report.pipeline.deadLetterCap` | optional | `128` | 每 worker 死信批容量 |
| `report.filter.include` | optional | `[]`(全放行) | expr 表达式，OR 语义命中其一即保留 |
| `report.filter.exclude` | optional | `[]` | 命中其一即丢弃（在 include 之后） |

### gateway（gateway service）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `gateway.addr` | optional | `:8080` | HTTP 监听地址；`--addr` 覆盖 |

### upload（gateway + SDK）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `upload.string.batchSize` | optional | `1000` | 字符串上报批大小（无重传） |
| `upload.string.filter.{include,exclude}` | optional | `[]` | 字符串上报的上报 filter |
| `upload.file.logPattern` | optional | `[]` | 文件上报匹配模式（`/upload` 默认值） |
| `upload.file.maxLineBytes` | optional | `10485760`(10MB) | 单行最大字节 |
| `upload.file.pipeline.*` | optional | 同 report.pipeline | 文件上报管线参数 |
| `upload.file.filter.{include,exclude}` | optional | `[]` | 文件上报的上报 filter |
| `upload.file.checkpointCollection` | optional | `_tango_fileupload` | 断点续传偏移集合 |

完整样例：[standalone](../examples/config/standalone/standalone.max.yaml)、
[gateway](../examples/config/gateway/gateway.max.yaml)。

---

## 上报 filter

上报 filter 作用于 `report.filter` / `upload.string.filter` / `upload.file.filter`，
维度为 `#type` / `#event_name` / `properties.*`，用 `include` / `exclude`（expr-lang）
表达式。示例（作用于扁平化记录，`#` 前缀字段可直接引用）：

```yaml
report:
  filter:
    include:
      - '#type == "track" && #event_name in ["login", "pay"]'
      - '#type startsWith "user_"'
    exclude:
      - 'properties.is_loadtest == true'
```

被过滤掉的记录**不写 dead_letter**，是有意丢弃。
