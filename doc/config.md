# tango 配置说明

## 配置来源与优先级

tango 从五个来源读取配置，优先级从低到高：

```
内置默认值  <  YAML 文件  <  环境变量(TANGO_*)  <  命令行 flag  <  远程配置(remoteConfig)
```

高优先级来源只覆盖**明确设置**的字段，其余沿用低优先级值（逐字段合并）。
远程配置（`remoteConfig`，可选）在以上之上再做一层逐字段覆盖，详见
[§9](#9-remoteconfig--远程配置覆盖)。

## 配置结构

配置按用途分组为若干区块，只有进程级的 `mode` / `instanceID` 在顶层：

```yaml
mode: daemon            # 顶层
# instanceID 仅来自环境变量 TANGO_INSTANCE_ID

logging:   { ... }      # 日志
mongo:     { ... }      # 连接与写入重试
source:    { ... }      # 文件采集（daemon/once）
pipeline:  { ... }      # 批量与并行写
filter:    { ... }      # 上传过滤
backfill:  { ... }      # 历史回填（backfill 模式）
remoteConfig: { ... }   # 远程配置覆盖
agent:     { ... }      # 任务 worker（agent 模式）
```

## 环境变量映射

前缀 `TANGO_`，键路径中的 `.` 换成 `_`，全大写。例如：

| 配置键 | 环境变量 |
|--------|----------|
| `mongo.uri` | `TANGO_MONGO_URI` |
| `logging.level` | `TANGO_LOGGING_LEVEL` |
| `pipeline.batchSize` | `TANGO_PIPELINE_BATCHSIZE` |
| `instanceID` | `TANGO_INSTANCE_ID`（agent 模式必填） |

## 命令行 flag（精简）

仅保留三个最常用 flag，分别映射到嵌套键；其余细调走 YAML / env：

| Flag | 映射到 | 说明 |
|------|--------|------|
| `--mongoURI` | `mongo.uri` | MongoDB 连接 URI |
| `--logLevel` | `logging.level` | 日志级别 |
| `--mode` | `mode` | 运行模式 |

---

## 1. mode（顶层）

运行模式，决定启动哪条管线：

| 值 | 子命令 | 说明 |
|----|--------|------|
| `daemon`（默认） | `tango daemon` | tail 日志文件持续导入 |
| `once` | `tango once` | 现有日志全量跑一遍后退出 |
| `ingest` | `tango ingest` | 同步逐行导入（API/CLI） |
| `backfill` | `tango backfill` | 从数数 OpenAPI 回填历史数据 |
| `agent` | `tango agent` | 任务 worker（认领并执行队列任务） |

## 2. instanceID（顶层）

本进程唯一标识，**仅**来自环境变量 `TANGO_INSTANCE_ID`。`agent` 模式必填
（用于定向任务匹配与心跳注册），其他模式忽略。

## 3. logging

| 键 | 默认 | 说明 |
|----|------|------|
| `logging.level` | `info` | `debug` / `info` / `warn` / `error` |
| `logging.format` | `text` | `text` / `json` |

## 4. mongo

| 键 | 默认 | 说明 |
|----|------|------|
| `mongo.uri` | *(必填)* | 连接 URI；数据库名取自 path |
| `mongo.maxElapsedTime` | `10s` | 单次 bulk write 最大重试总时间 |
| `mongo.connectTimeout` | `10s` | 初始连接握手超时 |
| `mongo.serverSelectionTimeout` | `30s` | 等待可用服务器超时 |

## 5. source（daemon / once）

| 键 | 默认 | 说明 |
|----|------|------|
| `source.logPattern` | `[]` | 文件匹配（glob/正则，支持 `**`）；daemon/once 必需 |
| `source.tailMode` | `hybrid` | `hybrid` / `poll` / `event` |
| `source.rescanInterval` | `30s` | 重扫新文件间隔（daemon） |
| `source.pollInterval` | `200ms` | poll/hybrid 到达 EOF 后重读间隔 |
| `source.maxLineBytes` | `10485760` | 单行最大字节数（10MB） |

`tailMode` 三种策略：`hybrid` 事件驱动 + 轮询兜底（推荐）；`poll` 纯轮询，
免疫通知丢失；`event` 纯 kqueue/inotify，延迟最低但可能因通知丢失卡住。

## 6. pipeline

| 键 | 默认 | 说明 |
|----|------|------|
| `pipeline.batchSize` | `1000` | 目标批量；min=batchSize/4，max=batchSize*2 |
| `pipeline.batchWorkers` | `2` | 并行写入 worker 数 |
| `pipeline.flushInterval` | `1s` | 批量定时刷新间隔 |
| `pipeline.channelBuffer` | `0` | 每 worker 行通道缓冲；0=按 batchSize*2 派生 |
| `pipeline.deadLetterCap` | `128` | 每 worker dead-letter 批容量 |

## 7. filter

| 键 | 默认 | 说明 |
|----|------|------|
| `filter.include` | `[]` | expr-lang 表达式列表；非空时须命中其一才保留（OR） |
| `filter.exclude` | `[]` | expr-lang 表达式列表；任一命中即丢弃（在 include 之后） |

表达式作用于扁平化后的记录文档：`#type`、`#event_name` 以及 `properties.*`
提升到顶层的字段都可直接引用。`#` 前缀字段会被透明改写为 `$env["#field"]`，
所以可直接写 `#type == "track"`。被过滤掉的记录**不写 dead_letter**，是有意丢弃。
语法见 https://expr-lang.org/docs/language-definition 。

示例：
```yaml
filter:
  include:
    - '#type == "track" && #event_name in ["login", "pay"]'
    - '#type startsWith "user_"'
  exclude:
    - 'debug == true'
    - 'country == "CN" && level < 3'
```

## 8. backfill（backfill 模式 / agent 执行任务时）

从数数 OpenAPI 异步 SQL 接口拉历史数据，经与 daemon 相同的解析→过滤→写入管线落库。

| 键 | 默认 | 说明 |
|----|------|------|
| `backfill.apiBaseURL` | *(必填)* | 数数 OpenAPI 网关，无尾斜杠 |
| `backfill.token` | *(必填)* | 项目查询 token |
| `backfill.proxy` | `""` | 出站代理 `socks5://`/`http://`/`https://`；空=直连 |
| `backfill.projectID` | *(必填)* | 拼表名 `v_event_<id>` / `v_user_<id>` |
| `backfill.schemaPrefix` | `""` | 表名前缀，如 `ta` → `ta.v_user_35` |
| `backfill.table` | `event` | `event` / `user` |
| `backfill.partDateRange.{start,end}` | — | 分区日期范围（event 表必填，按天分块，`YYYY-MM-DD`） |
| `backfill.eventTimeRange.{start,end}` | — | 可选，用 `#event_time` 收窄（`YYYY-MM-DD HH:MM:SS`） |
| `backfill.pageSize` | `10000` | 服务端分页大小（≥1000） |
| `backfill.pageRetries` | `3` | 单页瞬时错误重试次数（重拉幂等） |
| `backfill.paginate` | `true` | true=submit 带 pageSize 服务端分页+逐页续传；false=单次全量流式 |
| `backfill.pollInterval` | `3s` | 任务状态轮询间隔 |
| `backfill.pollTimeout` | `30m` | 单 chunk submit→FINISHED 最长等待 |
| `backfill.runID` | *(回填必填)* | 断点续传标识；同 runID 重启即续跑 |
| `backfill.progressCollection` | `_backfill_progress` | checkpoint 集合 |
| `backfill.forceSkipExisting` | `true` | event 一律 `$setOnInsert` 跳过已存在 `#uuid` |
| `backfill.skipLocalFilter` | `false` | true=只信 SQL 下推，关闭本地兜底 |
| `backfill.limit` | `0` | >0 时给每条 SQL 加 `LIMIT n`（冒烟测试用） |

关键点：
- **分页**：`pageSize` 必须在 submit 阶段生效（`paginate: true`），否则数数默认
  不分页、整表压进单页。
- **user 表无 `$part_date`**：作为单一 chunk 全量同步，行按 `#user_id` upsert。
- **filter 下推**：`filter.include/exclude` 同时编译为 Presto WHERE 下推到数数侧，
  本地 filter 仍兜底（除非 `skipLocalFilter`）。
- **续传**：进度（天/页）持久化到 `_backfill_progress`，按 `runID` 主键。

## 9. remoteConfig — 远程配置覆盖

从 MongoDB 拉取一个 JSON 文档，逐字段覆盖本地配置。

| 键 | 默认 | 说明 |
|----|------|------|
| `remoteConfig.enabled` | `false` | 开关；false 时完全用本地配置 |
| `remoteConfig.collection` | `_tango_config` | 配置文档所在集合 |
| `remoteConfig.documentID` | `default` | 全局共享配置文档 `_id` |
| `remoteConfig.syncInterval` | `1h` | daemon 重新拉取并热更 filter 的间隔 |

语义：
- **启动时**所有模式拉一次并 merge；**运行中**仅 daemon 每 `syncInterval` 再拉，
  且只有 `filter` 热生效，其余字段改动记日志、下次重启生效。
- **作用域按 db 隔离**：只影响连同一个 db 的实例；不同 db 是独立命名空间。
- **不可远程覆盖**：`mongo`、`remoteConfig`、`mode`、`instanceID`（要先用本地
  `mongo.uri` 才能连库读到这份配置）。
- 文档形如：`{ "_id": "default", "filter": { "include": [...], "exclude": [] } }`，
  可用 client 的 `PublishFilter` / `PublishConfig` 发布。

## 10. agent — 任务 worker（agent 模式）

| 键 | 默认 | 说明 |
|----|------|------|
| `agent.tasksCollection` | `_tango_tasks` | 任务队列集合 |
| `agent.instancesCollection` | `_tango_instances` | 实例心跳注册集合（TTL 自动过期） |
| `agent.pollInterval` | `10s` | 轮询认领间隔 |
| `agent.leaseDuration` | `5m` | 任务租约；超时未续可被其他 agent 回收 |
| `agent.heartbeatInterval` | `30s` | 心跳刷新间隔 |
| `agent.instanceTTL` | `90s` | 心跳超过此时长视为离线 |

任务分发是 pull 模型：原子 claim + 租约保证不重复执行与故障回收；心跳注册仅用于
定向发布 fail-fast 与在线列表，不影响认领正确性。任务执行所需的 TA 连接信息取自
`backfill` 区块，任务 payload 只携带"取什么"。详见 [architecture.md](architecture.md)。

完整带注释的样例见根目录 [`tango.yaml`](../tango.yaml) 与
[`examples/config/`](../examples/config/) 各场景子目录。
