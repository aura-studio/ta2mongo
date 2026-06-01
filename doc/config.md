# tango 配置参考

本文是**字段参考**（每个键的含义、required/optional、默认值）。命令行用法见
[usage.md](usage.md)，架构见 [arch.md](arch.md)，完整样例见 [examples/config](../examples/config)。

## 配置文件与默认名

| 模式 | 子命令 | `--config` 留空时默认读取 |
|------|--------|------|
| standalone | `tango daemon standalone` | `standalone.{yaml,yml,json}` |
| cluster | `tango daemon cluster` | `cluster.{yaml,yml,json}` |

默认文件在**二进制同级目录**按 `yaml → yml → json` 取首个存在者；各子命令只读自己的文件。
文件缺失或为空时静默跳过。

## 来源与优先级（低 → 高）

1. 内置默认值
2. 配置文件（YAML/JSON，按扩展名识别）
3. `TANGO_*` 环境变量（viper 原始层级：`.` → `_`、转大写）
4. CLI flag（完整层级名，如 `--generic.mongo.uri`；`--config` 是文件路径）
5. （cluster 模式）远程配置文档——仅覆盖上报 `filter`，启动时拉取 + 每 `syncInterval` 热重载

### 环境变量示例

| 配置键 | 环境变量 |
|--------|----------|
| `generic.mongo.uri` | `TANGO_GENERIC_MONGO_URI` |
| `generic.logging.level` | `TANGO_GENERIC_LOGGING_LEVEL` |
| `report.source.tailMode` | `TANGO_REPORT_SOURCE_TAILMODE` |

---

## 配置结构

两部分：`generic`（进程级共享）与 `report`（上报管线）。无 `enabled` 开关——模式由子命令决定。
`report.filter.remote` 仅在 cluster 模式生效。

### generic（两种模式都用）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `generic.logging.level` | optional | `info` | `debug`/`info`/`warn`/`error` |
| `generic.logging.format` | optional | `text` | `text`/`json` |
| `generic.mongo.uri` | **required** | — | MongoDB 连接串；库名取自 URI 路径 |
| `generic.mongo.maxElapsedTime` | optional | `10s` | 单次 bulk-write 退避重试总时长上限 |
| `generic.mongo.connectTimeout` | optional | `10s` | 初次连接握手超时 |
| `generic.mongo.serverSelectionTimeout` | optional | `30s` | 选择可用节点超时 |

### report（两种模式都用）

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
| `report.filter.local.include` | optional | `[]`(全放行) | expr 表达式，OR 语义命中其一即保留 |
| `report.filter.local.exclude` | optional | `[]` | 命中其一即丢弃（在 include 之后） |

### report.filter.remote（仅 cluster 模式生效）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `report.filter.remote.collection` | optional | `_tango_config` | 控制面文档所在集合 |
| `report.filter.remote.documentID` | optional | `default` | 控制面文档 `_id` |
| `report.filter.remote.syncInterval` | optional | `1h` | 重新拉取并热重载的间隔 |

> 是否启用同步由模式决定（cluster 开、standalone 关）。连接类字段（`generic.mongo.uri`、
> `report.filter.remote` 本身）永不可被远端覆盖；只有上报 `filter` 支持运行时热生效。

完整样例：[standalone.max.yaml](../examples/config/standalone/standalone.max.yaml)（全量+注释）、
[standalone.min.yaml](../examples/config/standalone/standalone.min.yaml)（仅 required）；yaml/json
各有 max/min 两份，cluster 同理见 [examples/config/cluster](../examples/config/cluster)。

---

## 上报 filter 表达式

expr-lang，作用于扁平化记录，`#` 前缀字段可直接引用：

```yaml
filter:
  local:
    include:
      - '#type == "track" && #event_name in ["login", "pay"]'
      - '#type startsWith "user_"'
    exclude:
      - 'properties.is_loadtest == true'
```

被过滤掉的记录**不写 dead_letter**，是有意丢弃。
