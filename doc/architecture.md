# ta2mongo 架构说明

## 1. 目标
`ta2mongo`：将 ThinkingData 日志（JSON 行）采集并写入 MongoDB 的 `user` / `event` / `dead_letter` 三个集合。

> 支持四种运行模式，分别适用于不同场景：
> - **Daemon 模式**：增量追尾日志文件 + 周期性重扫，批量异步写入，适合持续导入场景。
> - **Once 模式**：Daemon 的一次性版本——从文件开头读取所有匹配文件，保留完整的 daemon 处理流程（affinity 路由、多 worker、batch flush、retry），读完即退出并输出详细统计摘要。适合批量迁移、数据恢复、CI/CD 等一次性场景。
> - **Ingest 模式**：同步阻塞式处理单行 JSON，逐条写入并等待确认，出错直接返回。适合 CLI 单次上传。
> - **API 模式**（`client` 包）：纯 Go 库，仿照 Redis 客户端模式——创建连接池、初始化时填入地址和数据库，在应用中直接调用。适合应用内嵌集成（HTTP 服务、微服务等）。

---

## 2. 包结构
实现拆分为一组顶层包，各自承担单一职责：

- `config`：配置结构、默认值、校验、从 viper 读取 YAML
- `talog`：解析一行日志 JSON，提取 `#type/#uuid` 与 `Doc`；执行合规校验
- `tailer`：根据 `ta.logPattern`（正则数组）发现需要 tail 的文件路径，启动文件追尾
- `store`：MongoDB 写入（按 `#type` 语义构建不同 WriteModel + bulkWrite + retry）、索引创建、用户身份解析
- `daemon`：daemon 主流程（tail 文件、affinity 路由、按 batch 刷写、并发 worker、dead_letter 落盘）
- `once`：一次性处理（daemon 的临时版，完整保留 daemon 处理流程，读完文件即退出并输出统计）
- `ingest`：CLI 同步阻塞上传（单行解析 + 身份解析 + 逐条/批量同步写入）
- `client`：纯 API 客户端库（仿 Redis 客户端模式，创建连接池 + 方法调用，适合应用内嵌集成）

CLI 在根目录 `main.go`，提供三个子命令：

```
ta2mongo daemon   # 后台 daemon 模式
ta2mongo once     # 一次性处理模式（daemon 的临时版）
ta2mongo ingest   # 同步阻塞 ingest 模式
```

- 无子命令时根据 YAML 配置中的 `mode` 字段决定运行模式（默认 `daemon`）
- 使用 cobra 解析 `--config` 参数（PersistentFlag，所有子命令共享）
- 读取 YAML 配置（默认 `ta2mongo.yaml`）
- 构造 logrus logger
- 各子命令各自执行 `EnsureIndexes()` + 核心逻辑
- 监听 `SIGINT`/`SIGTERM` 优雅退出

---

## 3. 运行模式

### 3.1 Daemon 模式（高吞吐、异步批量）

适用场景：后台持续导入日志文件，追求高吞吐量。

#### 3.1.1 文件发现（tail source）
1. 从 `cfg.TA.LogPattern`（`[]string`，每个字符串是正则）开始发现文件
2. `tailer` 包：
   - 对每个正则编译 `regexp.Compile`
   - 从正则的"前缀（meta 之前）"推导 `WalkDir` baseDir
   - 遍历得到的文件路径，对完整路径做 `re.MatchString(path)` 过滤
   - 返回去重后的文件路径列表
3. 对每个匹配文件启动 `tail.TailFile`：
   - `Whence=2, Offset=0`：从文件末尾开始（增量消费）
   - `ReOpen=true, Follow=true`：文件轮转/追加可继续消费
4. 初始化扫描完成后，**始终开启重扫**：
   - 以 `tail.rescanSeconds` 为周期重扫匹配文件并补充 tail

#### 3.1.2 Affinity 路由与并发 Worker

```
┌─────────────────────────────────────────────────────┐
│  Tailer (文件发现 + tail -f)                          │
│  周期性重扫新文件                                      │
└───────────────────┬─────────────────────────────────┘
                    │ lineCh (chan string, cap 2000)
                    ▼
┌─────────────────────────────────────────────────────┐
│  Dispatcher                                         │
│  提取路由键 (#account_id 优先, 其次 #distinct_id)      │
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
│  MongoDB BulkWrite (指数退避重试)                      │
│  Collections: user, event, dead_letter               │
└─────────────────────────────────────────────────────┘
```

- Dispatcher 保证同一用户的所有操作被路由到同一 worker，防止跨 worker 乱序覆写
- 路由键提取支持直接 payload 和 envelope 格式（`msg`/`message`/`log`）
- 路由键为空时确定性地路由到 worker 0

#### 3.1.3 Worker 批处理逻辑
1. 从专属 channel 接收日志行
2. 调用 `parser.ParseLine(line)` 校验并解析
3. 解析失败：加入 `deadBatch`
4. 解析成功：调用 `IdentityResolver.Resolve()` 解析 `#user_id`
5. 按 `record.Category()` 分流到 `userBatch` 或 `eventBatch`
6. Flush 触发条件：
   - `batch.size`：user/event 任一批达到 size
   - `batch.flushIntervalMs`：距离上次 flush 超过间隔
7. User 集合使用 **ordered writes** 保证批内操作顺序
8. Event/dead_letter 集合使用 **unordered writes**

---

### 3.2 Once 模式（一次性全量处理）

适用场景：批量数据迁移、历史数据回填、数据恢复、CI/CD 流水线等需要一次性处理所有文件并获得明确结果的场景。

Once 模式本质上是 **Daemon 模式的一次性版本**：
- 文件发现：复用与 daemon 相同的 `ta.logPattern` 正则匹配逻辑
- 文件读取：从文件 **开头** 读取（不是从末尾），不 follow、不 reopen、不重扫
- 处理流程：完整保留 daemon 的处理管线（affinity 路由 → 多 worker → batch flush → ordered/unordered bulk write → 指数退避 retry）
- 退出条件：所有匹配文件读取完毕且所有 batch 刷写完成后退出

#### 3.2.1 统计摘要

Once 模式在退出时输出详细的统计信息，强调处理过程中是否遇到异常：

| 统计项 | 说明 |
|--------|------|
| `files_discovered` | 匹配到的文件数 |
| `total_lines` | 处理的总行数 |
| `duration` | 总处理耗时 |
| `parsed_ok` | 解析成功的行数 |
| `parse_errors` | 解析失败的行数 |
| `identity_errors` | 身份解析失败的行数 |
| `user_writes` | 写入 user 集合的操作数 |
| `event_writes` | 写入 event 集合的操作数 |
| `dead_letters` | 写入 dead_letter 集合的行数 |
| `total_retries` | MongoDB 写入重试总次数（指数退避） |
| `write_errors` | 重试耗尽后仍然失败的写入批次数 |
| `lines_per_second` | 吞吐量 |

如果存在任何错误（parse_errors > 0 或 identity_errors > 0 或 write_errors > 0），程序以非零退出码退出，并在摘要中明确标注 `COMPLETED WITH ERRORS`。

#### 3.2.2 CLI 使用方式

```bash
# 子命令方式：
ta2mongo once --config ta2mongo.yaml

# 或通过 YAML 配置 mode 字段：
# mode: "once"
ta2mongo --config ta2mongo.yaml
```

---

### 3.3 Ingest 模式（同步阻塞、逐条确认）

适用场景：请求-响应式调用，调用方需要立即得知写入结果（HTTP API handler、CLI 单次上传、SDK 集成）。

#### 3.3.1 公共 API（`ingest` 包）

| API | 说明 |
|-----|------|
| `New(ctx, cfg, logger)` | 创建 `Ingester`，建立独立的 MongoDB 连接 |
| `NewFromClient(client, cfg, logger)` | 复用已有 MongoDB 连接创建 `Ingester`（daemon 与 ingest 共存于同一进程时使用） |
| `Ingester.Ingest(ctx, line) error` | 核心方法：解析 → 身份解析 → 同步写入 MongoDB，阻塞直到写入确认或失败 |
| `Ingester.IngestBatch(ctx, lines) error` | 批量便捷方法：收集所有 write model 后一次性刷写，仍为同步阻塞 |
| `Ingester.EnsureIndexes(ctx)` | 创建所需索引（幂等） |
| `Ingester.Close()` | 断开 MongoDB 连接（仅 `New` 创建时调用，`NewFromClient` 不需要） |

`Ingester` 是并发安全的，可在多个 goroutine 中共享使用。

#### 3.3.2 处理流程

```
调用方
  │
  │  Ingest(ctx, line)
  ▼
┌──────────────────────────────┐
│  talog.Parser.ParseLine()    │── 失败 ──→ dead_letter + 返回 error
└──────────┬───────────────────┘
           │ 成功
           ▼
┌──────────────────────────────┐
│  IdentityResolver.Resolve()  │── 失败 ──→ dead_letter + 返回 error
└──────────┬───────────────────┘
           │ 得到 #user_id
           ▼
┌──────────────────────────────┐
│  按 Category 路由             │
│  user_* → UserCollection      │
│  track* → EventCollection     │
└──────────┬───────────────────┘
           │
           ▼
┌──────────────────────────────┐
│  MongoDB Write (指数退避重试) │── 失败 ──→ 返回 error
└──────────┬───────────────────┘
           │ 成功
           ▼
       返回 nil
```

- 每次调用独立执行完整的 parse → identity → write 流程
- 解析失败和身份解析失败的行会写入 `dead_letter` 集合后返回错误
- 复用与 daemon 相同的 `talog.Parser`、`store.Store`、`store.IdentityResolver` 和 WriteModel 构建函数，零代码重复

#### 3.3.3 CLI 使用方式

```bash
# 单行参数：
ta2mongo ingest --config ta2mongo.yaml \
  '{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"u1","#account_id":"alice"}'

# 从 stdin 读取（管道）：
cat events.jsonl | ta2mongo ingest --config ta2mongo.yaml

# 混合：先处理参数，再处理 stdin：
echo '{"#type":"track",...}' | ta2mongo ingest --config ta2mongo.yaml '{"#type":"user_set",...}'
```

- 支持从位置参数和 stdin 管道读取日志行
- stdin 单行最大支持 10 MB
- 跳过空行
- 任一行处理失败时记录错误并继续，最终以非零退出码报告失败数

---

### 3.4 API 模式（纯客户端库）

适用场景：应用内嵌集成，在自己的 Go 服务中直接调用 ta2mongo 写入能力（HTTP 服务、微服务、后台任务等）。

API 模式不是 CLI 子命令，而是一个独立的 Go 包 `client`，仿照 Redis/数据库客户端的设计模式：

- **创建时初始化连接池**：传入 MongoDB URI + DB 名称，内部自动管理连接池
- **方法调用**：`Ingest(ctx, line)` 逐条写入、`IngestBatch(ctx, lines)` 批量写入
- **并发安全**：Client 可在多个 goroutine 中共享
- **无需配置文件**：所有参数通过 `Options` 结构体传入

#### 3.4.1 API 接口

```go
import "rocket-nano/tools/ta2mongo/client"

// 创建客户端（类似 redis.NewClient）
cli, err := client.New(ctx, client.Options{
    URI:            "mongodb://localhost:27017",
    DB:             "ta2mongo",
    MaxElapsedTime: 10 * time.Second,  // 可选，默认 10s
    BatchSize:      1000,              // 可选，默认 1000
})
defer cli.Close()

// 初始化索引（启动时调用一次）
cli.EnsureIndexes(ctx)

// 逐条写入
err = cli.Ingest(ctx, `{"#type":"track","#event_name":"login",...}`)

// 批量写入
err = cli.IngestBatch(ctx, []string{line1, line2, ...})

// 健康检查
err = cli.Ping(ctx)

// 查看重试统计
retries := cli.Stats().TotalRetries()
```

#### 3.4.2 与 Ingest 模式的区别

| | Ingest 模式（CLI） | API 模式（`client` 包） |
|---|---|---|
| **使用方式** | 命令行工具 | Go 库，代码调用 |
| **配置方式** | YAML 配置文件 | `Options` 结构体 |
| **生命周期** | 进程级（启动→处理→退出） | 应用级（创建→长期使用→关闭） |
| **连接管理** | 每次运行建立/断开连接 | 连接池常驻，复用连接 |
| **典型场景** | CLI 一次性上传 | HTTP handler、微服务内嵌 |

---

### 3.5 四种模式对比

| | Daemon 模式 | Once 模式 | Ingest 模式 | API 模式 |
|---|---|---|---|---|
| **定位** | 持续运行的后台服务 | Daemon 的一次性版本 | CLI 上传工具 | 应用内嵌客户端库 |
| **输入源** | 追尾日志文件（增量） | 读取日志文件（全量，从头开始） | 单行 JSON（CLI 参数 / stdin） | 代码传入 JSON 行 |
| **处理方式** | 批量、异步、N 个 worker | 批量、异步、N 个 worker | 逐条、同步、阻塞 | 逐条/批量、同步、阻塞 |
| **写入策略** | 积攒批次后 bulk write | 积攒批次后 bulk write | 每行立即 write | 逐条或批量 write |
| **退出条件** | 信号终止（SIGINT/SIGTERM） | 所有文件处理完毕 | 所有输入处理完毕 | 调用方控制 |
| **适用场景** | 后台持续导入 | 批量迁移、数据恢复、CI/CD | CLI 单次上传 | HTTP 服务、微服务 |
| **吞吐量** | 高（批量 + 并发） | 高（批量 + 并发） | 较低（同步保证） | 中等（批量时较高） |
| **错误反馈** | 日志记录 + dead_letter | 统计摘要 + dead_letter + 非零退出码 | 立即返回 error + dead_letter | 返回 error + dead_letter |
| **配置方式** | YAML 文件 | YAML 文件 | YAML 文件 | Options 结构体（无需文件） |
| **配置要求** | 需要 `ta.logPattern` | 需要 `ta.logPattern` | 不需要 `ta.logPattern` | 仅需 URI + DB |

---

## 4. 日志解析与校验

### 4.1 支持的格式
1. **直接 TA payload**：JSON 根对象包含 `#time`/`#type`/`#event_name` 等 TA 键
   ```json
   {"#type":"user_set","#account_id":"alice","#distinct_id":"dev123","#time":"2024-01-01","#uuid":"u1","properties":{"name":"Alice"}}
   ```
2. **Envelope 格式**：TA payload 作为 JSON 字符串嵌套在 `msg`/`message`/`log` 字段中
   ```json
   {"level":"info","msg":"{\"#type\":\"track\",\"#account_id\":\"bob\",\"#time\":\"2024-01-01\",\"#uuid\":\"u2\",\"#event_name\":\"login\"}"}
   ```

### 4.2 校验规则
合规校验（"存在且有值"定义：不为 `null` 且不为空字符串 `""`）：
- user 类型（`user_*`）：`#type`、`#time`、`#uuid` 必须存在且有值，`#account_id`/`#distinct_id` 至少一个存在且有值
- event 类型（`track*`）：`#type`、`#time`、`#event_name`、`#uuid` 必须存在且有值，`#account_id`/`#distinct_id` 至少一个存在且有值
- 不属于 user/event 的 `#type`：直接返回 error

### 4.3 文档平铺
校验通过后生成 `Record{Type, UUID, AccountID, DistinctID, Doc}`：
- `Doc`：字段平铺到最外层（`properties` 内的字段直接提升，不拼接前缀），写入 `_ts`（UnixNano 摄入时间戳）

---

## 5. 用户身份解析（IdentityResolver）

所有操作（user 和 event）在写入前都会通过 `IdentityResolver.Resolve()` 解析出数值型 `#user_id`。

### 5.1 ID Mapping 规则（ThinkingData 规范）
- `#account_id` 和 `#user_id` 是 1:1（一个账号对应唯一用户）
- 一个 `#account_id` 可绑定多个 `#distinct_id`
- 一个 `#distinct_id` 只能绑定到一个 `#account_id`
- 绑定关系不可逆

### 5.2 存储结构
- `id_mapping` 集合：`{#user_id: int64, #account_id: string|null, #distinct_ids: []string}`
- `id_counter` 集合：`{_id: "user_id", seq: int64}`（自增序列）

### 5.3 性能优化
- 热路径：`sync.Map` 内存缓存，命中时零 IO
- 冷路径：MongoDB 原子操作（unique index + conditional update + `$addToSet`）
- 多 Pod 安全：依赖 MongoDB 原子操作保证正确性，不依赖进程内互斥锁

---

## 6. Mongo 写入模型

### 6.1 user 集合（按 `#type` 语义）

所有 user 操作以 `#user_id`（解析后的数值 ID）作为 filter key。

| #type | MongoDB 操作 | 说明 |
|-------|-------------|------|
| `user_set` | Aggregation pipeline `$set` + `tsCondSet`（upsert） | 覆盖用户属性，带时间戳保护 |
| `user_setOnce` | `$max` meta + `$setOnInsert` data（upsert） | 属性已存在则忽略 |
| `user_add` | `$max` meta + `$inc` data（upsert） | 数值型属性累加 |
| `user_unset` | Aggregation pipeline `tsCondUnset`（upsert） | 条件移除属性字段 |
| `user_del` | `DeleteOne` | 删除整条用户记录 |
| `user_append` | `$max` meta + `$push` with `$each`（upsert） | 列表属性追加元素 |
| `user_uniq_append` | `$max` meta + `$addToSet` with `$each`（upsert） | 列表属性去重追加 |

**时间戳保护机制**（`tsCondSet` / `tsCondUnset`）：
- 使用 `_ts`（摄入时间戳）防止旧记录覆写新记录
- 条件：`incoming _ts >= existing _ts`
- 通过 MongoDB 4.2+ aggregation pipeline update 实现
- 缺失 `_ts` 时视为 0（upsert insert 场景）

**各操作详细语义（ThinkingData 规范）：**
- `user_set`：覆盖一个或多个用户属性；如果该属性已有值存在，则覆盖先前值
- `user_setOnce`：初始化一个或多个用户属性；如果该属性已有值存在，则忽略本次操作
- `user_add`：为数值型用户属性做累加
- `user_unset`：移除该用户的一个或多个属性字段
- `user_del`：删除整条用户记录
- `user_append`：为列表类型属性追加元素
- `user_uniq_append`：为列表类型属性追加元素，并对全列表执行一次去重；去重保证前后原有元素顺序不变

### 6.2 event 集合（按 `#type` 语义）
| #type | MongoDB 操作 | 说明 |
|-------|-------------|------|
| `track` | `InsertOne` | 新增事件记录 |
| `track_update` | `$set`（upsert, filter by `#uuid`） | 字段级别更新 |
| `track_overwrite` | `ReplaceOne`（upsert, filter by `#uuid`） | 整条替换 |

**各操作详细语义（ThinkingData 规范）：**
- `track`：对 event 表进行 insert（新增事件记录）
- `track_update`：对 event 表进行字段级别 update
- `track_overwrite`：先删除再插入的 upsert 语义——先移除同一幂等键（`#uuid`）对应的旧记录，再插入新记录（实现上使用 `ReplaceOne` 等价达成）

### 6.3 dead_letter 集合
- `InsertOne`：写入 `{_ts, line, error}`

### 6.4 重试策略
所有 bulk write 使用指数退避重试：
- 初始间隔：200ms
- 最大间隔：2s
- 最大总耗时：`retry.maxElapsedTime`（默认 10s）
- 支持 context 取消

---

## 7. 索引管理
`EnsureIndexes()` 在启动时调用（daemon 和 ingest 模式均在处理数据前执行，幂等）：

- `user` 集合：
  - `{#user_id: 1}` (unique)
  - `{#account_id: 1}`
  - `{#distinct_id: 1}`
  - `{_ts: 1}`
- `event` 集合：
  - `{#event_name: 1, #account_id: 1, #time: 1}`
  - `{#event_name: 1, #distinct_id: 1, #time: 1}`
  - `{#uuid: 1}` (unique)
  - `{_ts: 1}`
- `dead_letter` 集合：
  - `{_ts: 1}`
- `id_mapping` 集合：
  - `{#user_id: 1}` (unique)
  - `{#account_id: 1}` (unique, sparse)
  - `{#distinct_ids: 1}`

---

## 8. 可观测性与错误处理

### Daemon 模式
- tail 失败：记录 warn，跳过该文件并继续
- 正则非法：warn 并跳过该正则
- 解析失败/不合规：写入 `dead_letter` 集合，每 1000 条输出一次 warn 日志
- Mongo bulk 写入失败：
  - 采用指数退避重试到 `retry.maxElapsedTime`
  - 重试耗尽后输出 `Warn`/`Error` 日志
  - daemon 不会因为单次 bulk 失败而退出（继续消费后续批次）

### Once 模式
- 与 Daemon 相同的错误处理（不因单次失败而终止）
- 额外追踪所有重试次数和写入失败次数
- 退出时输出完整统计摘要
- 如果存在任何错误，以非零退出码退出

### Ingest 模式
- 解析失败：写入 `dead_letter` 集合，**立即返回 error 给调用方**
- 身份解析失败：写入 `dead_letter` 集合，立即返回 error
- Mongo 写入失败：经指数退避重试后仍失败，立即返回 error
- CLI 模式下：每行失败单独记录 Error 日志，继续处理后续行，最终以非零退出码报告失败总数

---

## 9. 配置驱动（关键点）
所有行为由 `ta2mongo.yaml` 驱动，对应字段见：
- `mode`：运行模式（`daemon` / `once` / `ingest`，默认 `daemon`；也可通过子命令显式指定）
- `mongo.*`：MongoDB 连接（三种模式共用）
- `ta.logPattern`：日志文件匹配正则（daemon 和 once 模式需要，ingest 模式忽略）
- `tail.rescanSeconds`：文件重扫周期（仅 daemon 模式）
- `batch.*`：批处理参数（影响 daemon 和 once 模式；ingest 模式复用 retry 配置）
- `retry.*`：指数退避重试参数（三种模式共用）
- `log.level`：日志级别（三种模式共用）
