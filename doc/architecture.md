# ta2mongo 架构说明

## 1. 目标
`ta2mongo`：将 ThinkingData 日志（JSON 行）采集并写入 MongoDB 的 `user` / `event` 两个集合。

> 运行模式：daemon-only（增量追尾 + 周期性重扫匹配文件，便于持续导入）。

---

## 2. 包结构（去掉 internal）
实现拆分为一组顶层包，各自承担单一职责：

- `config`：配置结构、默认值、校验、从 viper 读取 YAML
- `parser`：解析一行日志 JSON，提取 `#type/#uuid` 与 `Doc`
- `matches`：根据 `ta.logPattern`（正则数组）发现需要 tail 的文件路径
- `store`：MongoDB 写入（upsert / bulkWrite + retry）与索引创建、统计
- `runner`：daemon 主流程（tail 文件、按 batch 刷写、并发 worker）

CLI 在 `tools/ta2mongo/main.go`，只负责启动：
- 读取 `--config` YAML
- 构造 logger
- `runner.EnsureIndexes()`
- `runner.RunDaemon()`

---

## 3. 运行流程（daemon-only）
### 3.1 文件发现（tail source）
1. 从 `cfg.Ta.LogPattern`（`[]string`，每个字符串是正则）开始发现文件
2. `matches.CollectMatches(patterns)`：
   - 对每个正则编译 `regexp.Compile`
   - 从正则的“前缀（meta 之前）”推导 `WalkDir` baseDir
   - 遍历得到的文件路径，对完整路径做 `re.MatchString(path)` 过滤
   - 返回去重后的文件路径列表
3. `runner.startDaemonSource()` 对每个匹配文件启动 `tail.TailFile`：
   - `Whence=2, Offset=0`：从文件末尾开始（增量消费）
   - `ReOpen=true, Follow=true`：文件轮转/追加可继续消费
4. 初始化扫描完成后，**始终开启重扫**：
   - 以 `tail.rescanSeconds` 为周期重扫匹配文件并补充 tail

---

### 3.2 日志解析与批处理（workers）
1. tail 输出一行字符串，进入 `lineCh`
2. 多个 worker 并发消费 `lineCh`
3. 对每一行调用 `parser.ParseLine(line)`：
   - 识别 thinkingdata payload（字段包含 `#time/#type/#event_name` 等）
   - 或识别 envelope（msg/message/log 中是 JSON 字符串）
   - 生成 `TaRecord{Type, UUID, Doc}`
4. 根据 `TaRecord.Type` 分流到：
   - `user`：`user_*` 类型（当前支持：`user_set/user_unset/user_setOnce/user_add/user_append/user_del`）
   - `event`：`track/track_update/track_overwrite`（其余情况按 event 写）
5. 按以下策略 flush bulk：
   - 触发条件：
     - `batch.size`：user/event 任一批达到 size
     - `batch.flushIntervalMs`：距离上次 flush 已超过该间隔
   - flush 使用 `MongoStore.BulkWriteWithRetry(...)`
     - `BulkWrite(ordered=false)`
     - 指数退避重试，直到 `retry.maxElapsedTime`

---

### 3.3 Mongo 写入模型
- 写入用 `#uuid` 作为唯一 key
- 使用 update + `$set` + `SetUpsert(true)`：
  - 若不存在则 upsert 插入
  - 若已存在则更新文档中字段（覆盖 `$set` 里的内容）

---

### 3.4 索引管理
- `runner.EnsureIndexes()` 调用 `store.EnsureIndexes()`（配置项已固定开启）
  - `user` / `event` 创建复合索引（按 `#time` + account/distinct 等）
  - `#uuid` 创建 unique index

---

## 4. 可观测性与错误处理
- tail 失败：记录 warn，跳过该文件并继续
- 正则非法：warn 并跳过该正则
- Mongo bulk 写入失败：
  - 采用指数退避重试到 `retry.maxElapsedTime`
  - 重试耗尽后输出 `Warn` 日志（runner 侧也会记录 `Error`）
  - daemon 不会因为单次 bulk 失败而退出（继续消费后续批次）
  - 若持续失败，队列会形成背压（上游 tail 与 channel 速率不匹配）

---

## 5. 配置驱动（关键点）
所有行为由 `tools/ta2mongo/ta2mongo.yaml` 驱动，对应字段见：
- `mongo.*`
- `ta.logPattern`
- `tail.rescanSeconds`
- `batch.*`
- `retry.*`
- `log.level`
