# tango 配置参考

本文是**字段参考**（每个键的含义、required/optional、默认值）。命令行用法见
[usage.md](usage.md)，设计与数据流见 [arch.md](arch.md)。完整可运行样例见
[examples/config](../examples/config)。

## 配置文件与角色命令

| 角色 / 模式 | 子命令 | schema | `--config` 留空时默认读取 |
|------|--------|--------|------|
| report service | `tango report run` | DaemonConfig 的 generic + report | `report.{yaml,yml,json}`，fallback `standalone.{yaml,yml,json}` |
| worker service | `tango worker run` | DaemonConfig 的 generic + agent + report.filter.remote | `worker.{yaml,yml,json}`，fallback `agent.{yaml,yml,json}` |
| gateway service | `tango gateway serve` | ClientConfig | `client.{yaml,yml,json}` |
| operator CLI | `tango operator <subcmd>` | ClientConfig | `client.{yaml,yml,json}` |
| legacy daemon standalone | `tango daemon standalone` | DaemonConfig | `standalone.{yaml,yml,json}` |
| legacy daemon agent | `tango daemon agent` | DaemonConfig | `agent.{yaml,yml,json}` |
| legacy client | `tango client <subcmd>` | ClientConfig | `client.{yaml,yml,json}` |

默认文件在**二进制同级目录**按 `yaml → yml → json` 取首个存在者；各子命令只读自己的
文件（standalone 不读 agent 文件，反之亦然）。文件缺失或解析为空时静默跳过。

## 来源与优先级（低 → 高）

1. 内置默认值
2. 配置文件（YAML/JSON，按扩展名识别）
3. `TANGO_*` 环境变量
4. CLI flag（完整层级名，如 `--mongo.uri` / `--instanceID` / legacy `--generic.mongo.uri` / `--agent.instanceID`；`--config` 是文件路径）
5. 远程配置文档（仅 report service 启用 remote config 或 legacy agent/profile managed 时，仅上报 `filter` 热生效；report-sync 任务写入该文档）

### 环境变量映射

`TANGO_` 前缀 + 嵌套键 `.` → `_`、转大写。注意 **daemon 与 client 的连接串前缀不同**
（schema 不同）：

| 配置键 | 环境变量 |
|--------|----------|
| report/worker alias `mongo.uri` | `TANGO_MONGO_URI` |
| report/worker alias `logging.level` | `TANGO_LOGGING_LEVEL` |
| legacy daemon `generic.mongo.uri` | `TANGO_GENERIC_MONGO_URI` |
| legacy daemon `generic.logging.level` | `TANGO_GENERIC_LOGGING_LEVEL` |
| daemon `report.source.tailMode` | `TANGO_REPORT_SOURCE_TAILMODE` |
| worker alias `instanceID` | `TANGO_INSTANCEID` |
| legacy daemon `agent.instanceID` | `TANGO_AGENT_INSTANCEID` |
| gateway/operator `mongo.uri` | `TANGO_MONGO_URI` |

---

## report / worker / legacy daemon 配置（DaemonConfig）

三部分：`generic` / `report` / `agent`。新角色命令会把角色化参数映射到这个 schema：

- `tango report run --mongo.uri` → `generic.mongo.uri`
- `tango report run --logging.level` → `generic.logging.level`
- `tango worker run --instanceID` → `agent.instanceID`
- `tango worker run --mongo.uri` → `generic.mongo.uri`

legacy `daemon standalone` 只用 `generic` + `report`；legacy `daemon agent` 额外启用 `report.filter.remote` 配置同步与 `agent` 任务派发。

新 `worker run` 只用 `generic` + `agent` + `report.filter.remote`，不要求 `report.source.logPattern`。

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

### report.filter.remote（report remote config / worker report-sync / legacy agent 生效）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `report.filter.remote.collection` | optional | `_tango_config` | 配置文档所在集合 |
| `report.filter.remote.documentID` | optional | `default` | 配置文档 `_id` |
| `report.filter.remote.syncInterval` | optional | `1h` | 重新拉取并热重载的间隔 |

> 在 `tango report run` 中，可通过 `--remote-config.enabled` 启用同步；legacy agent/profile managed 默认启用。连接类字段（`generic.mongo.uri` 等）永不可被远端覆盖；只有上报 `filter` 支持运行时热生效。

### agent（worker service / legacy agent 生效）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `agent.instanceID` | **required**(worker / legacy agent) | — | 实例唯一标识；worker 可用 `--instanceID`，legacy 可用 `--agent.instanceID` |
| `agent.tasksCollection` | optional | `_tango_tasks` | 任务队列集合 |
| `agent.instancesCollection` | optional | `_tango_instances` | 实例心跳集合（TTL 过期） |
| `agent.pollInterval` | optional | `10s` | 轮询认领任务间隔 |
| `agent.leaseDuration` | optional | `5m` | 任务租约；超时未续可被其他 agent 回收 |
| `agent.heartbeatInterval` | optional | `30s` | 心跳刷新间隔 |
| `agent.instanceTTL` | optional | `90s` | 心跳超过此时长视为离线 |

完整样例：[standalone.max.yaml](../examples/config/standalone/standalone.max.yaml)（全量+注释）、
[standalone.min.yaml](../examples/config/standalone/standalone.min.yaml)（仅 required）；yaml/json
各有 max/min 两份，agent 同理见 [examples/config/agent](../examples/config/agent)。

---

## gateway / operator / legacy client 配置（ClientConfig）

gateway 和 operator 复用现有 ClientConfig。它使用扁平的 `mongo`（连接串 `TANGO_MONGO_URI`），按五项功能分区，外加 `server`：

| 段 | 关键键（默认） | 用途 |
|----|----|----|
| `logging` / `mongo` | `mongo.uri`**(required)**、`mongo.maxElapsedTime`(10s) | 共享连接/日志 |
| `stringUpload` | `batchSize`(1000)、`filter.{include,exclude}` | 字符串单次上报（无重传） |
| `fileUpload` | `logPattern`、`maxLineBytes`(10MB)、`pipeline.*`、`filter.*`、`checkpointCollection`(`_tango_fileupload`) | 文件上报（断点续传） |
| `backfill` | 见下「backfill」 | 历史回填执行 |
| `backfillFilter` | `table`(`event`)、`events`、`include`、`exclude` | 回填选表与谓词 |
| `sql` | `schemaPrefix` | 临时 SQL 的执行覆盖（连接取自 backfill） |
| `publish` | `tasksCollection`(`_tango_tasks`)、`instancesCollection`(`_tango_instances`)、`instanceTTL`(90s) | 任务发布端 |
| `server` | `addr`(`:8080`) | `tango gateway serve` / legacy `tango client serve` 监听地址 |

### backfill（client backfill / sql；agent 的 backfill/sql 任务参数来自任务 payload，不读此段）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `backfill.apiBaseURL` | **required** | — | 数数 OpenAPI 网关，无尾斜杠 |
| `backfill.token` | **required** | — | 项目查询 token |
| `backfill.projectID` | **required** | — | 拼表名 `v_event_<id>` / `v_user_<id>` |
| `backfill.runID` | **required** | — | 断点续传标识；同 runID 重启即续跑 |
| `backfill.partDateRange.{start,end}` | event 表 **required** | — | `YYYY-MM-DD`；user 表无分区不需要 |
| `backfill.pageSize` | optional | `10000` | 服务端分页大小（≥1000） |
| `backfill.paginate` | optional | `true` | true=分页+逐页续传；false=单次全量流式 |
| `backfill.pollInterval` | optional | `3s` | 任务状态轮询间隔 |
| `backfill.pollTimeout` | optional | `30m` | 单 chunk submit→FINISHED 最长等待 |
| `backfill.pageRetries` | optional | `3` | 单页瞬时错误重试次数 |
| `backfill.progressCollection` | optional | `_backfill_progress` | 续传检查点集合 |
| `backfill.forceSkipExisting` | optional | `true` | event 一律 `$setOnInsert` 跳过已存在 `#uuid` |
| `backfill.skipLocalFilter` | optional | `false` | true=只信 SQL 下推，关闭本地兜底 |
| `backfill.proxy` | optional | `""` | `http`/`https`/`socks5`；空=直连 |
| `backfill.schemaPrefix` | optional | `""` | 表名前缀，如 `ta` → `ta.v_event_35` |
| `backfill.limit` | optional | `0` | >0 时每条 SQL 追加 `LIMIT n`（冒烟用） |

完整样例：[examples/config/client](../examples/config/client)。

---

## 两种 filter

| | 上报 filter | backfill filter |
|---|---|---|
| 位置 | daemon `report.filter.local` / client `stringUpload.filter`、`fileUpload.filter` | `backfillFilter` |
| 维度 | `#type` / `#event_name` / `properties.*` | **表名(event/user)** + 事件/属性（**不含 #type**） |
| 表达式 | `include` / `exclude`（expr-lang） | `include` / `exclude` + `events`(语法糖 → `#event_name in [...]`) |

上报 filter 示例（expr-lang，作用于扁平化记录，`#` 前缀字段可直接引用）：

```yaml
filter:
  include:
    - '#type == "track" && #event_name in ["login", "pay"]'
    - '#type startsWith "user_"'
  exclude:
    - 'properties.is_loadtest == true'
```

被过滤掉的记录**不写 dead_letter**，是有意丢弃。
