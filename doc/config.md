# tango 配置参考

本文是**字段参考**（每个键的含义、required/optional、默认值）。命令行用法见
[usage.md](usage.md)，设计与数据流见 [arch.md](arch.md)。完整可运行样例见
[examples/config](../examples/config)。

四个角色命令（report / worker / gateway / operator）共用**统一 RoleConfig schema**，
每个角色只取自己需要的段。没有 legacy 兼容 schema。

## 配置文件与角色命令

| 角色 | 子命令 | RoleConfig 子集 | `--config` 留空时默认读取 |
|------|--------|--------|------|
| report service | `tango report run` | `runtime` + `report` + `remoteConfig` | `report.{yaml,yml,json}` |
| worker service | `tango worker run` | `runtime` + `tasks` + `remoteConfig` | `worker.{yaml,yml,json}` |
| gateway service | `tango gateway serve` | `runtime` + `gateway` + `upload` + `tasks` | `gateway.{yaml,yml,json}` |
| operator CLI | `tango operator <subcmd>` | `runtime` + `upload` + `backfill` + `tasks` | `operator.{yaml,yml,json}` |

默认文件在**二进制同级目录**按 `yaml → yml → json` 取首个存在者；各子命令只读自己的
文件。文件缺失或解析为空时静默跳过（回退到默认值 + 环境变量 + flag）。

## 来源与优先级（低 → 高）

1. 内置默认值
2. 配置文件（YAML/JSON，按扩展名识别）
3. `TANGO_*` 环境变量
4. CLI flag（flag 名即配置键，如 `--runtime.mongo.uri` / `--tasks.instanceID`；`--config` 是文件路径）
5. 远程配置文档（report service 启用 `remoteConfig.enabled` 或兼容 agent/profile managed 时，仅上报 `filter` 热生效；report-sync 任务写入该文档）

### 环境变量映射

`TANGO_` 前缀 + 嵌套键 `.` → `_`、转大写。

| 配置键 | 环境变量 |
|--------|----------|
| `runtime.mongo.uri` | `TANGO_RUNTIME_MONGO_URI` |
| `runtime.logging.level` | `TANGO_RUNTIME_LOGGING_LEVEL` |
| `report.source.tailMode` | `TANGO_REPORT_SOURCE_TAILMODE` |
| `remoteConfig.enabled` | `TANGO_REMOTECONFIG_ENABLED` |
| `tasks.instanceID` | `TANGO_TASKS_INSTANCEID` |
| `gateway.addr` | `TANGO_GATEWAY_ADDR` |

---

## 统一 RoleConfig schema

### runtime（所有角色）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `runtime.logging.level` | optional | `info` | `debug`/`info`/`warn`/`error` |
| `runtime.logging.format` | optional | `text` | `text`/`json` |
| `runtime.mongo.uri` | **required** | — | MongoDB 连接串；库名取自 URI 路径 |
| `runtime.mongo.maxElapsedTime` | optional | `10s` | 单次 bulk-write 退避重试总时长上限 |
| `runtime.mongo.connectTimeout` | optional | `10s` | 初次连接握手超时 |
| `runtime.mongo.serverSelectionTimeout` | optional | `30s` | 选择可用节点超时 |

### report（report service）

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

### remoteConfig（report service + worker report-sync）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `remoteConfig.enabled` | optional | `false` | report service 是否监听远端配置热更新；worker 端固定启用 |
| `remoteConfig.collection` | optional | `_tango_config` | 配置文档所在集合 |
| `remoteConfig.documentID` | optional | `default` | 配置文档 `_id` |
| `remoteConfig.syncInterval` | optional | `1h` | 重新拉取并热重载的间隔 |

> 连接类字段（`runtime.mongo.uri` 等）永不可被远端覆盖；只有上报 `filter` 支持运行时热生效。worker 执行 report-sync 仅写入该文档，report service 通过自己的 sync loop 应用。

### tasks（worker service + operator/gateway publish）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `tasks.instanceID` | worker **required** | — | worker 实例唯一标识；可用 `--instanceID` 简写 |
| `tasks.collection` | optional | `_tango_tasks` | 任务队列集合 |
| `tasks.instancesCollection` | optional | `_tango_instances` | 实例心跳集合（TTL 过期） |
| `tasks.pollInterval` | optional | `10s` | 轮询认领任务间隔 |
| `tasks.leaseTTL` | optional | `5m` | 任务租约；超时未续可被其他 worker 回收 |
| `tasks.heartbeatInterval` | optional | `30s` | 心跳刷新间隔 |
| `tasks.instanceTTL` | optional | `90s` | 心跳超过此时长视为离线 |

> publish 端（operator/gateway）只用到 `tasks.collection` / `tasks.instancesCollection` / `tasks.instanceTTL`。

### gateway（gateway service）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `gateway.addr` | optional | `:8080` | HTTP 监听地址；`--addr` 覆盖 |

### upload（operator + gateway + SDK）

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `upload.string.batchSize` | optional | `1000` | 字符串上报批大小（无重传） |
| `upload.string.filter.{include,exclude}` | optional | `[]` | 字符串上报的上报 filter |
| `upload.file.logPattern` | optional | `[]` | 文件上报匹配模式；`--logPattern` 覆盖 |
| `upload.file.maxLineBytes` | optional | `10485760`(10MB) | 单行最大字节 |
| `upload.file.pipeline.*` | optional | 同 report.pipeline | 文件上报管线参数 |
| `upload.file.filter.{include,exclude}` | optional | `[]` | 文件上报的上报 filter |
| `upload.file.checkpointCollection` | optional | `_tango_fileupload` | 断点续传偏移集合 |

### backfill / backfillFilter / sql（worker + operator + gateway）

`backfill` 段字段（operator backfill / sql 直接执行时读取；worker 的 backfill/sql 任务参数来自任务 payload）：

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

| 键 | required/optional | 默认 | 说明 |
|----|----|----|----|
| `backfillFilter.table` | optional | `event` | 回填选表：`event`/`user` |
| `backfillFilter.events` | optional | `[]` | event 表语法糖 → `#event_name in [...]` |
| `backfillFilter.include` / `.exclude` | optional | `[]` | 回填谓词（下推到 SQL + 本地兜底） |
| `sql.schemaPrefix` | optional | `""` | 临时 SQL 的 schema 前缀覆盖 |

完整样例：[report](../examples/config/report/report.yaml)、[worker](../examples/config/worker/worker.yaml)、
[gateway](../examples/config/gateway/gateway.yaml)、[operator](../examples/config/operator/operator.yaml)。

---

## 两种 filter

| | 上报 filter | backfill filter |
|---|---|---|
| 位置 | `report.filter` / `upload.string.filter` / `upload.file.filter` | `backfillFilter` |
| 维度 | `#type` / `#event_name` / `properties.*` | **表名(event/user)** + 事件/属性（**不含 #type**） |
| 表达式 | `include` / `exclude`（expr-lang） | `include` / `exclude` + `events`(语法糖 → `#event_name in [...]`) |

上报 filter 示例（expr-lang，作用于扁平化记录，`#` 前缀字段可直接引用）：

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
