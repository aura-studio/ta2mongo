# tango 架构说明

## 1. 目标

`tango`：将 ThinkingData 日志（JSON 行）采集并写入 MongoDB 的 `user` / `event` / `dead_letter` 三个集合。

支持四种运行模式：

| 模式 | 定位 |
|------|------|
| **Daemon** | 持续追尾日志文件，批量异步写入，适合后台服务 |
| **Once** | Daemon 的一次性版本，从文件开头全量处理后退出，适合批量迁移、CI/CD |
| **Ingest** | 同步阻塞，逐条写入并等待确认，适合 CLI 单次上传 |
| **API**（`client` 包） | 纯 Go 库，仿 Redis 客户端模式，适合应用内嵌集成 |

---

## 2. 目录结构

```
.
├── main.go
├── tango.yaml              # 示例配置文件
├── global-bundle.pem       # AWS DocumentDB TLS 证书包
├── config/                 # 配置结构与加载（扁平单层，对外可引用）
├── client/                 # 对外 SDK（仿 Redis 客户端）
├── cmd/                    # CLI 入口（daemon/once/ingest 子命令）
├── examples/               # 使用示例
└── internal/               # 所有内部实现
    ├── daemon/             # 持续追尾模式
    ├── once/               # 一次性处理模式
    ├── ingest/             # 同步写入模式
    ├── store/              # MongoDB 持久化（WriteModel、索引、身份解析）
    ├── talog/              # ThinkingData JSON 行解析与校验
    ├── tailer/             # 文件发现与追尾
    ├── dynamicbatch/       # 动态批量阈值计算
    └── pipeline/           # Affinity 路由 + Worker 批处理管线
```

---

## 3. 运行模式

### 3.1 Daemon 模式（高吞吐、异步批量）

适用于后台持续导入日志文件。

#### 数据流

```
┌─────────────────────────────────────────────────────┐
│  Tailer（文件发现 + tail -f + 周期性重扫）              │
└───────────────────┬─────────────────────────────────┘
                    │ lineCh（chan string）
                    ▼
┌─────────────────────────────────────────────────────┐
│  Dispatcher                                         │
│  提取路由键（#account_id 优先，其次 #distinct_id）      │
│  FNV-1a hash → 一致性 worker 分配                     │
└───┬───────────┬────────────────────┬────────────────┘
    │           │                    │
 workerCh[0] workerCh[1]   ...   workerCh[N-1]
    │           │                    │
    ▼           ▼                    ▼
┌─────────┐ ┌─────────┐         ┌─────────┐
│Worker 0 │ │Worker 1 │  ...    │Worker N │
│Parse    │ │Parse    │         │Parse    │
│Identity │ │Identity │         │Identity │
│Batch    │ │Batch    │         │Batch    │
│Flush    │ │Flush    │         │Flush    │
└─────────┘ └─────────┘         └─────────┘
     │           │                    │
     ▼           ▼                    ▼
┌─────────────────────────────────────────────────────┐
│  MongoDB BulkWrite（指数退避重试）                      │
│  Collections: user, event, dead_letter               │
└─────────────────────────────────────────────────────┘
```

- **Affinity 路由**：Dispatcher 保证同一用户的所有操作被路由到同一 worker，防止跨 worker 乱序覆写
- **动态批量**：Worker 根据 channel 积压量动态调整 flush 阈值（空闲时加大批次减少 IO，繁忙时快速清空积压）
- **定时 flush**：除批量触发外，每隔 `flushInterval` 强制刷新，防止低流量时数据滞留

#### 文件发现规则
1. 从 `logPattern`（正则数组）发现匹配文件
2. 对每个匹配文件启动 `tail.TailFile`（从文件末尾开始，Follow + ReOpen）
3. 以 `rescanInterval` 为周期重扫补充新文件

---

### 3.2 Once 模式（一次性全量处理）

Once 本质上是 **Daemon 的一次性版本**，共享完整的处理管线：
- 文件读取从**开头**开始（全量），不 follow、不 reopen、不重扫
- 所有文件读完后退出，打印统计摘要
- 存在错误时以非零退出码退出

**统计摘要字段：**

| 字段 | 说明 |
|------|------|
| `files_discovered` | 匹配到的文件数 |
| `total_lines` | 处理的总行数 |
| `duration` | 总处理耗时 |
| `parsed_ok` | 解析成功行数 |
| `parse_errors` | 解析失败行数 |
| `identity_errors` | 身份解析失败行数 |
| `user_writes` | 写入 user 集合的操作数 |
| `event_writes` | 写入 event 集合的操作数 |
| `dead_letters` | 写入 dead_letter 的行数 |
| `total_retries` | MongoDB 写入重试总次数 |
| `write_errors` | 重试耗尽后仍然失败的批次数 |
| `lines_per_second` | 吞吐量 |

---

### 3.3 Ingest 模式（同步阻塞、逐条确认）

适用于请求-响应式调用，调用方需立即知道写入结果。

```
调用方
  │  Ingest(ctx, line)
  ▼
talog.Parser.ParseLine() ── 失败 ──→ dead_letter + error
  │ 成功
  ▼
IdentityResolver.Resolve() ── 失败 ──→ dead_letter + error
  │ 得到 #user_id
  ▼
按 Category 路由（user_* → user，track* → event）
  │
  ▼
MongoDB Write（指数退避重试）── 失败 ──→ error
  │ 成功
  ▼
返回 nil
```

---

### 3.4 API 模式（`client` 包）

纯 Go 库，适合在自己的服务中内嵌使用：

```go
import "rocket-nano/tools/tango/client"

cli, err := client.New(ctx,
    client.WithURI("mongodb://localhost:27017/tango"),
    client.WithBatchSize(1000),
)
defer cli.Close()

cli.EnsureIndexes(ctx)

// 逐条写入
err = cli.Ingest(ctx, `{"#type":"track","#event_name":"login",...}`)

// 批量写入
err = cli.IngestBatch(ctx, []string{line1, line2, ...})
```

---

### 3.5 四种模式对比

| | Daemon | Once | Ingest | API |
|---|---|---|---|---|
| **输入源** | 追尾日志文件（增量） | 读取日志文件（全量） | CLI 参数 / stdin | 代码传入 |
| **处理方式** | 批量、异步、多 worker | 批量、异步、多 worker | 逐条、同步 | 逐条/批量、同步 |
| **退出条件** | 信号终止 | 所有文件处理完毕 | 所有输入处理完毕 | 调用方控制 |
| **需要 logPattern** | 是 | 是 | 否 | 否 |
| **配置方式** | YAML / flag / env | YAML / flag / env | YAML / flag / env | Options 结构体 |

---

## 4. 日志解析（talog）

### 支持格式
1. **直接 TA payload**：JSON 根对象包含 `#type` 等 TA 键
2. **Envelope 格式**：TA payload 作为 JSON 字符串嵌套在 `msg`/`message`/`log` 字段中

### 校验规则
- user 类型（`user_*`）：`#type`、`#time`、`#uuid` 必填，`#account_id`/`#distinct_id` 至少一个
- event 类型（`track*`）：`#type`、`#time`、`#event_name`、`#uuid` 必填，`#account_id`/`#distinct_id` 至少一个
- 其他 `#type`：直接返回 error

### 文档平铺
`properties` 内的字段直接提升到最外层，写入 `_ts`（UnixNano 摄入时间戳）。

---

## 5. 用户身份解析（IdentityResolver）

所有写入前通过 `IdentityResolver.Resolve()` 解析出数值型 `#user_id`。

**ID Mapping 规则（ThinkingData 规范）：**
- `#account_id` 与 `#user_id` 1:1 对应
- 一个 `#account_id` 可绑定多个 `#distinct_id`
- 一个 `#distinct_id` 只能绑定一个 `#account_id`（绑定不可逆）

**存储：**
- `id_mapping`：`{#user_id, #account_id, #distinct_ids[]}`
- `id_counter`：自增序列（`$inc` 原子操作）

**性能：** 内存缓存（`sync.Map`）热路径零 IO；冷路径依赖 MongoDB 原子操作，多 Pod 安全。

---

## 6. MongoDB 写入模型

### user 集合

| #type | 操作 | 语义 |
|-------|------|------|
| `user_set` | Aggregation `$set` + tsCondSet（upsert） | 覆盖属性，带时间戳保护 |
| `user_setOnce` | `$setOnInsert`（upsert） | 属性已存在则忽略 |
| `user_add` | `$inc`（upsert） | 数值属性累加 |
| `user_unset` | Aggregation tsCondUnset（upsert） | 条件移除字段 |
| `user_del` | `DeleteOne` | 删除整条记录 |
| `user_append` | `$push $each`（upsert） | 列表追加 |
| `user_uniq_append` | `$addToSet $each`（upsert） | 列表去重追加 |

**时间戳保护**：通过 MongoDB 4.2+ aggregation pipeline update，防止旧记录覆写新记录（`incoming._ts >= existing._ts`）。

### event 集合

| #type | 操作 | 语义 |
|-------|------|------|
| `track` | `InsertOne` | 新增事件 |
| `track_update` | `$set`（upsert by `#uuid`） | 字段级更新 |
| `track_overwrite` | `ReplaceOne`（upsert by `#uuid`） | 整条替换 |

### dead_letter 集合
- `InsertOne`：写入 `{_ts, line, error}`，用于解析失败或身份解析失败的行

### 重试策略
指数退避：初始 200ms，最大 2s，总时间上限 `maxElapsedTime`（默认 10s）。

---

## 7. 索引

`EnsureIndexes()` 在启动时调用（幂等）：

- **user**：`#user_id`(unique)、`#account_id`、`#distinct_id`、`_ts`
- **event**：`(#event_name, #account_id, #time)`、`(#event_name, #distinct_id, #time)`、`#uuid`(unique)、`_ts`
- **dead_letter**：`_ts`
- **id_mapping**：`#user_id`(unique)、`#account_id`(unique, sparse)、`#distinct_ids`

---

## 8. 可观测性

### 日志
- 结构化日志（logrus），级别可配
- Daemon 每 60 秒输出周期统计（interval + cumulative）
- Once 退出时输出完整统计摘要

### 错误处理
| 场景 | 处理方式 |
|------|----------|
| 文件 tail 失败 | warn 日志，跳过该文件继续 |
| 正则非法 | warn 日志，跳过该正则 |
| 解析失败 | 写入 dead_letter，每 1000 条输出一次 warn |
| MongoDB 写入失败 | 指数退避重试，耗尽后记录 error；daemon/once 不因此退出 |
| Ingest 任意失败 | 立即返回 error 给调用方 |
