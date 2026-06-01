# tango 配置

> **单一二进制，两份配置文件**（角色由 `daemon` / `client` 子命令选择）。
> - `daemon.{yaml,json}` → `tango daemon standalone` / `tango daemon agent`（模式由子命令选）。详见 [examples/config/daemon](../examples/config/daemon)。
> - `client.{yaml,json}` → `tango client <subcmd>`（五项分区功能）。详见 [examples/config/client](../examples/config/client)。
>
> YAML、JSON 均支持（按扩展名识别）。要点：
> - **daemon 配置三部分**：`common`（logging + mongo）、`report`（含 `source` / `pipeline` / `filter` / `remoteConfig`）、`agent`（任务 agent 设置）。**模式由子命令选**:standalone 只用 common+report 纯上报;agent 额外开启 remoteConfig 配置同步与任务派发。
> - **两种 filter**：上报 filter 在 daemon 的 `report.filter` / client 的 `stringUpload.filter`、`fileUpload.filter`；backfill filter 在 `backfillFilter`（含 `table`，**backfill 段不再有独立 table 字段**，且**不过滤 #type**）。
> - **instanceID 仅在 agent 模式**：`agent.instanceID`（`tango daemon agent` 必填）。
> - client 五项功能分区：`stringUpload` / `fileUpload` / `backfill`(+`backfillFilter`) / `sql` / `publish`，外加 `server`(HTTP)。
>
> 下文的 per-key 默认值表是底层 runtime 字段参考，仍然适用于对应的配置段。

## 配置来源与优先级（低 → 高覆盖高）

1. **内置默认值**（最低优先级）
2. **配置文件**（`--config` 指定的 `daemon.*` / `client.*`，支持 YAML/JSON）
3. **环境变量**（`TANGO_*`）
4. **命令行参数**（`--config`、`--mongoURI`、`--logLevel`、`--instanceID`）
5. **远程配置**（MongoDB 文档，仅上报 `filter` 支持热生效；report-sync 任务写入该文档）

---

## 内置默认值

所有配置项均设有默认值。以下为完整默认值列表：

### 顶层（进程级）

| 配置项 | 默认值 |
|-------|--------|
| `mode` | `daemon` |
| `instanceID` | *(进程唯一标识，仅环境变量 `TANGO_INSTANCE_ID`)* |

### logging

| 配置项 | 默认值 |
|-------|--------|
| `logging.level` | `info` |
| `logging.format` | `text` |

### mongo

| 配置项 | 默认值 |
|-------|--------|
| `mongo.uri` | *(必填，无默认值)* |
| `mongo.maxElapsedTime` | `10s` |
| `mongo.connectTimeout` | `10s` |
| `mongo.serverSelectionTimeout` | `30s` |

### source

| 配置项 | 默认值 |
|-------|--------|
| `source.logPattern` | *(daemon/once 必需，无默认值)* |
| `source.tailMode` | `hybrid` |
| `source.rescanInterval` | `30s` |
| `source.pollInterval` | `200ms` |
| `source.maxLineBytes` | `10485760` (10MB) |

### pipeline

| 配置项 | 默认值 |
|-------|--------|
| `pipeline.batchSize` | `1000` |
| `pipeline.batchWorkers` | `2` |
| `pipeline.flushInterval` | `1s` |
| `pipeline.channelBuffer` | `0` (自动推导) |
| `pipeline.deadLetterCap` | `128` |

### filter

| 配置项 | 默认值 |
|-------|--------|
| `filter.include` | `[]` |
| `filter.exclude` | `[]` |

### backfill

| 配置项 | 默认值 |
|-------|--------|
| `backfill.apiBaseURL` | `""` |
| `backfill.token` | `""` |
| `backfill.proxy` | `""` |
| `backfill.projectID` | `0` |
| `backfill.schemaPrefix` | `""` |
| `backfillFilter.table` | `event` （表名属于 backfill filter，不再是 `backfill.table`） |
| `backfillFilter.events` | *(空)* （事件名 IN 过滤的语法糖） |
| `backfill.partDateRange.start` | *(空)* |
| `backfill.partDateRange.end` | *(空)* |
| `backfill.pageSize` | `10000` |
| `backfill.pageRetries` | `3` |
| `backfill.paginate` | `true` |
| `backfill.pollInterval` | `5s` |
| `backfill.pollTimeout` | `60m` |
| `backfill.runID` | `""` |
| `backfill.progressCollection` | `_backfill_progress` |
| `backfill.forceSkipExisting` | `true` |
| `backfill.skipLocalFilter` | `false` |

### remoteConfig

| 配置项 | 默认值 |
|-------|--------|
| `remoteConfig.enabled` | `false` |
| `remoteConfig.collection` | `_tango_config` |
| `remoteConfig.documentID` | `default` |
| `remoteConfig.syncInterval` | `1h` |

### agent

| 配置项 | 默认值 |
|-------|--------|
| `agent.tasksCollection` | `_tango_tasks` |
| `agent.instancesCollection` | `_tango_instances` |
| `agent.pollInterval` | `10s` |
| `agent.leaseDuration` | `5m` |
| `agent.heartbeatInterval` | `30s` |
| `agent.instanceTTL` | `90s` |

---

## YAML 配置文件

### 文件路径

默认在**二进制同级目录**查找 `tango.{yaml,yml,json}`（按此顺序取首个存在者）。通过 `--config` flag 可指定其他路径：

```bash
tango daemon --config /etc/tango/production.yaml
```

**文件不存在时静默跳过**，不报错，继续使用默认值 + 环境变量 + 命令行参数。
文件存在但解析失败（语法错误）时报错退出。

### 格式规则

- 格式：YAML
- 结构：**分组嵌套**，用子区块组织（`mongo` / `source` / `pipeline` / `filter` 等）
- Key 命名：**驼峰式**（camelCase），与 CLI flag 和环境变量后缀完全一致
- 只需填写需要覆盖默认值的字段，其余字段可省略

### 完整示例（根目录 `tango.yaml`）

```yaml
# tango 配置文件
#
# 配置按用途分组为若干区块；只有进程级的 mode / instanceID 留在顶层。
# YAML 键即结构字段；环境变量用 TANGO_ 前缀、"." 换成 "_"
# （如 mongo.uri → TANGO_MONGO_URI）。CLI 仅保留 --mongoURI / --logLevel /
# --mode 三个常用 flag，其余细调走 yaml 或 env。

# 运行模式：daemon（默认）/ once / ingest / backfill / agent
mode: "daemon"

logging:
  level: "info"
  format: "text"

mongo:
  uri: "mongodb://localhost:27017/tango"
  maxElapsedTime: "10s"
  connectTimeout: "10s"
  serverSelectionTimeout: "30s"

source:
  logPattern: ["/var/log/ta.*.log"]
  tailMode: "hybrid"
  rescanInterval: "30s"
  pollInterval: "200ms"
  maxLineBytes: 10485760

pipeline:
  batchSize: 1000
  batchWorkers: 2
  flushInterval: "1s"
  channelBuffer: 0
  deadLetterCap: 128

filter:
  include: []
  exclude: []

backfill:
  apiBaseURL: ""
  token: ""
  proxy: ""
  projectID: 0
  schemaPrefix: ""
  table: "event"
  partDateRange:
    start: "2026-01-01"
    end: "2026-05-28"
  pageSize: 10000
  pageRetries: 3
  paginate: true
  pollInterval: "5s"
  pollTimeout: "60m"
  runID: ""
  progressCollection: "_backfill_progress"
  forceSkipExisting: true
  skipLocalFilter: false

remoteConfig:
  enabled: false
  collection: "_tango_config"
  documentID: "default"
  syncInterval: "1h"

agent:
  tasksCollection: "_tango_tasks"
  instancesCollection: "_tango_instances"
  pollInterval: "10s"
  leaseDuration: "5m"
  heartbeatInterval: "30s"
  instanceTTL: "90s"
```

---

## 环境变量

所有配置项均可通过环境变量覆盖，格式：

```
TANGO_ + 层级字段用 "_" 连接
```

示例：

| 环境变量 | 等价 YAML 路径 |
|---------|---------------|
| `TANGO_MONGO_URI` | `mongo.uri` |
| `TANGO_MODE` | `mode` |
| `TANGO_LOGGING_LEVEL` | `logging.level` |
| `TANGO_SOURCE_LOG_PATTERN` | `source.logPattern` |
| `TANGO_PIPELINE_BATCH_SIZE` | `pipeline.batchSize` |
| `TANGO_FILTER_INCLUDE` | `filter.include` |
| `TANGO_BACKFILL_API_BASE_URL` | `backfill.apiBaseURL` |
| `TANGO_BACKFILL_TOKEN` | `backfill.token` |
| `TANGO_INSTANCE_ID` | *(进程标识，仅环境变量)* |
| `TANGO_REMOTE_CONFIG_ENABLED` | `remoteConfig.enabled` |
| `TANGO_AGENT_LEASE_DURATION` | `agent.leaseDuration` |

**注意**：`instanceID` 只能通过环境变量 `TANGO_INSTANCE_ID` 设置，YAML 中不可写。

---

## 命令行参数

仅暴露最常用的三个 flag：

| flag | 说明 |
|------|------|
| `--config <path>` | 指定 YAML 配置文件路径 |
| `--mongoURI <uri>` | MongoDB 连接 URI |
| `--logLevel <level>` | 日志级别：debug / info / warn / error |

其余所有配置通过 YAML 或环境变量覆盖。

---

## 各区块详细说明

### 日志输出（logging）

```yaml
logging:
  level: "info"   # debug / info / warn / error
  format: "text"  # text（默认）/ json
```

### MongoDB 连接（mongo）

```yaml
mongo:
  uri: "mongodb://localhost:27017/tango"   # 必填
  maxElapsedTime: "10s"                   # 单次 bulk write 最大重试总时间
  connectTimeout: "10s"                   # 连接握手超时
  serverSelectionTimeout: "30s"           # 服务器选择超时
```

### 文件采集（source）

仅 `daemon` / `once` 模式使用。

```yaml
source:
  logPattern: ["/var/log/ta.*.log"]       # glob/正则匹配；daemon/once 必需
  tailMode: "hybrid"                     # hybrid（默认）/ poll / event
  rescanInterval: "30s"                  # daemon 模式重扫新文件间隔
  pollInterval: "200ms"                  # poll/hybrid 模式 EOF 后重读间隔
  maxLineBytes: 10485760                  # 单行最大字节数（默认 10MB）
```

`tailMode` 说明：
- `hybrid`（默认）：事件驱动为主，轮询兜底
- `poll`：纯轮询模式
- `event`：纯事件驱动（依赖 inotify/FSEvents）

### 批量与并行写（pipeline）

```yaml
pipeline:
  batchSize: 1000           # 目标批量大小
  batchWorkers: 2           # 并行写入 worker 数
  flushInterval: "1s"       # 批量定时刷新间隔
  channelBuffer: 0         # 每 worker 行通道缓冲；0=按 batchSize*2 派生
  deadLetterCap: 128       # 每 worker dead-letter 批容量
```

自动批量推导：`min = batchSize / 4`，`max = batchSize * 2`。

### 远程配置覆盖（remoteConfig）

从 MongoDB 拉一个 JSON 文档逐字段覆盖本地配置。

```yaml
remoteConfig:
  enabled: true
  collection: "_tango_config"
  documentID: "default"
  syncInterval: "1h"
```

行为：
- **启动时**：所有模式拉一次
- **运行中**：daemon 每 `syncInterval` 再拉
- **热生效**：仅 `filter` 支持运行时热更新；其他字段需重启
- **不可覆盖**：`mongo` / `remoteConfig` / `mode` / `instanceID`
- **作用域**：按 db 隔离，只影响连同一 db 的实例

发布的远程文档形如：

```json
{
  "_id": "default",
  "filter": { "include": ["#type startsWith \"user_\""], "exclude": [] }
}
```

### 上传过滤（filter）

expr-lang 表达式，作用于扁平化记录：

```yaml
filter:
  include: []    # 正向：非空时记录须至少命中一条才保留；空=全放行
  exclude: []    # 反向：任一命中即丢弃，发生在 include 之后
```

可用字段（`#` 前缀透明映射为 `$env["#field"]`）：
- `#type` — 记录类型
- `#event_name` — 事件名
- `properties.*` — 所有属性均可直接引用

示例：

```yaml
filter:
  include:
    - '#type == "track" && #event_name in ["login", "pay", "PaymentOrderState"]'
    - '#type startsWith "user_"'
  exclude:
    - 'properties.is_loadtest == true'
```

被过滤掉的记录**不写 dead_letter**，是有意丢弃。

### 历史回填（backfill）

从数数 OpenAPI 拉取历史数据，按 `$part_date` 分天导入。

```yaml
backfill:
  apiBaseURL: ""            # 数数 OpenAPI 网关，无尾斜杠
  token: ""                 # 项目查询 token
  proxy: ""                 # 出站代理：socks5:// / http:// / https://；空=直连
  projectID: 0              # 拼表名 v_event_<id> / v_user_<id>
  schemaPrefix: ""          # 表名前缀，如 "ta" → ta.v_user_35
  table: "event"            # event / user
  partDateRange:            # 分区日期范围（event 表必填）
    start: "2026-01-01"
    end: "2026-05-28"
  pageSize: 10000           # 服务端分页大小（>=1000）
  pageRetries: 3            # 单页瞬时错误重试次数
  paginate: true            # true=服务端分页+逐页续传；false=单次全量流式
  pollInterval: "5s"        # 任务状态轮询间隔
  pollTimeout: "60m"        # 单 chunk submit→FINISHED 最长等待
  runID: ""                 # 断点续传标识；同 runID 重启即续跑
  progressCollection: "_backfill_progress"
  forceSkipExisting: true   # event 一律 $setOnInsert 跳过已存在 #uuid
  skipLocalFilter: false    # true=只信 SQL 下推，关闭本地兜底
```

关键行为：
- 续传：进度持久化到 `_backfill_progress`（按 `runID`）；taskId 失效自动重 submit
- 幂等：`#uuid`（event）/ `#user_id`（user）upsert，重跑安全
- filter 同时编译成 Presto WHERE 下推 + 本地兜底
- `user` 表无分区，作为单一 chunk 全量同步

### 任务队列（agent）

pull 模型，原子 claim + 租约保证不重复执行与故障回收。

```yaml
agent:
  tasksCollection: "_tango_tasks"
  instancesCollection: "_tango_instances"
  pollInterval: "10s"       # 轮询认领间隔
  leaseDuration: "5m"       # 任务租约；超时未续可被其他 agent 回收
  heartbeatInterval: "30s"  # 心跳刷新间隔
  instanceTTL: "90s"        # 心跳超过此时长视为离线
```

关键行为：
- 心跳注册仅用于定向 fail-fast 与在线列表，不影响认领正确性
- 任务执行所需的 TA 连接信息取自 `backfill` 区块
- 任务 payload 只携带"取什么"（table / 日期范围 / 显式 SQL）
- 定向任务靠 `TANGO_INSTANCE_ID` 匹配

---

## 场景配置样例

每个子目录对应一种典型部署场景，附一份可直接改用的 `tango.yaml`。
所有配置采用分组结构（`mongo` / `source` / `pipeline` / `filter` / `backfill`
/ `remoteConfig` / `agent`），只有 `mode` 与 `instanceID` 在顶层。

| 场景 | 目录 | 说明 |
|------|------|------|
| 实时日志采集 | `daemon-tail/` | tail 日志文件持续导入；含正/反向过滤 |
| 一次性导入 | `once-import/` | 把现有日志全量跑一遍后退出 |
| 历史回填（事件表） | `backfill-event/` | 按 `$part_date` 分天回填 `v_event_*`，经 socks5 代理 |
| 历史回填（用户表） | `backfill-user/` | 无分区 user 表全量同步 |
| 任务 worker | `agent-worker/` | 长驻进程认领并执行队列任务 |
| 远程配置控制面 | `remote-config/` | 数据中心发布过滤规则，daemon 热更 |

运行：

```bash
tango <mode> --config examples/config/<scenario>/tango.yaml
# 或用环境变量覆盖连接串：
TANGO_MONGO_URI=mongodb://host/db tango daemon --config .../tango.yaml
```

> 注意：`mongo.uri`、`backfill.token`、`backfill.proxy` 等敏感值示例里留空或占位，
> 实际使用请用环境变量注入，例如 `TANGO_MONGO_URI`、`TANGO_INSTANCE_ID`。

### daemon-tail/tango.yaml

```yaml
# 场景：实时日志采集（daemon）
# tail 匹配的日志文件，解析 TA 记录，过滤后持续写入 MongoDB。
mode: "daemon"

logging:
  level: "info"
  format: "text"

mongo:
  uri: "mongodb://localhost:27017/tango"
  maxElapsedTime: "10s"

source:
  logPattern:
    - "/var/log/ta/*.log"
    - "/data/app/**/event-*.log"
  tailMode: "hybrid"
  rescanInterval: "30s"
  pollInterval: "200ms"
  maxLineBytes: 10485760

pipeline:
  batchSize: 2000
  batchWorkers: 4
  flushInterval: "1s"
  channelBuffer: 8000

filter:
  include:
    - '#type startsWith "user_"'
    - '#type == "track" && #event_name in ["login", "pay", "PaymentOrderState"]'
  exclude:
    - 'properties.is_loadtest == true'
```

---

### once-import/tango.yaml

```yaml
# 场景：一次性全量导入（once）
# 把现有日志文件从头读一遍、处理完即退出，并打印统计摘要。
mode: "once"

logging:
  level: "info"

mongo:
  uri: "mongodb://localhost:27017/tango"
  maxElapsedTime: "30s"

source:
  logPattern:
    - "/data/history/ta-*.log"
  maxLineBytes: 20971520

pipeline:
  batchSize: 5000
  batchWorkers: 4
  flushInterval: "2s"

filter:
  include: []
  exclude: []
```

---

### backfill-event/tango.yaml

```yaml
# 场景：历史回填 - 事件表（backfill, table=event）
# 从数数 OpenAPI 按 $part_date 分天拉取 v_event_<projectID>，经 socks5 代理，
# 过滤后写入 MongoDB。filter 同时下推为 Presto WHERE。
mode: "backfill"

logging:
  level: "info"

mongo:
  uri: "mongodb://localhost:27017/tango"
  maxElapsedTime: "30s"

filter:
  include:
    - '#type == "track" && #event_name == "PaymentOrderState"'

backfill:
  apiBaseURL: "https://api.lnk.events"
  token: ""
  proxy: "socks5://user:pass@proxy-host:1080"
  projectID: 35
  schemaPrefix: "ta"
  table: "event"
  partDateRange:
    start: "2024-07-01"
    end: "2024-07-31"
  pageSize: 50000
  pageRetries: 3
  paginate: true
  pollInterval: "5s"
  pollTimeout: "60m"
  runID: "backfill-event-2024-07"
  forceSkipExisting: true
```

---

### backfill-user/tango.yaml

```yaml
# 场景：历史回填 - 用户表（backfill, table=user）
# v_user_<projectID> 无 $part_date 分区，作为单一 chunk 全量同步。
# 行按 #user_id upsert（snapshot），重跑幂等。
mode: "backfill"

logging:
  level: "info"

mongo:
  uri: "mongodb://localhost:27017/tango"
  maxElapsedTime: "30s"

backfill:
  apiBaseURL: "https://api.lnk.events"
  token: ""
  proxy: "socks5://user:pass@proxy-host:1080"
  projectID: 35
  schemaPrefix: "ta"
  table: "user"
  pageSize: 50000
  pageRetries: 3
  paginate: true
  pollInterval: "5s"
  pollTimeout: "60m"
  runID: "backfill-user-full"
  forceSkipExisting: true
```

---

### agent-worker/tango.yaml

```yaml
# 场景：任务 worker（agent）
# 长驻进程：注册心跳 → 从 _tango_tasks 认领任务 → 执行 backfill/sql →
# 续租 → 汇报。定向任务靠 TANGO_INSTANCE_ID 匹配。
mode: "agent"

logging:
  level: "info"

mongo:
  uri: "mongodb://localhost:27017/tango"
  maxElapsedTime: "30s"

backfill:
  apiBaseURL: "https://api.lnk.events"
  token: ""
  proxy: "socks5://user:pass@proxy-host:1080"
  projectID: 35
  schemaPrefix: "ta"
  pageSize: 50000
  pageRetries: 3
  paginate: true
  pollTimeout: "60m"

agent:
  tasksCollection: "_tango_tasks"
  instancesCollection: "_tango_instances"
  pollInterval: "10s"
  leaseDuration: "5m"
  heartbeatInterval: "30s"
  instanceTTL: "90s"
```

---

### remote-config/tango.yaml

```yaml
# 场景：远程配置控制面（daemon + remoteConfig）
# 各 daemon 实例启动时从 MongoDB 拉一份配置文档逐字段覆盖本地；运行中每
# syncInterval 再拉，仅 filter 热生效（无需重启即可调整采集范围）。
mode: "daemon"

logging:
  level: "info"

mongo:
  uri: "mongodb://localhost:27017/tango"

source:
  logPattern:
    - "/var/log/ta/*.log"
  tailMode: "hybrid"

pipeline:
  batchSize: 1000
  batchWorkers: 2

filter:
  include:
    - '#type startsWith "user_"'

remoteConfig:
  enabled: true
  collection: "_tango_config"
  documentID: "default"
  syncInterval: "1h"
```