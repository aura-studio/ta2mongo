# 实时发布策略（Change Streams + 主动拉取）

## 背景

Tango 中的"任务发布"和"配置发布"采用**完全相同的同步原理**——写者 push 文档到 MongoDB，消费者周期性 `find` 拉取：

| 流程 | 写入 | 消费侧轮询 | 原默认间隔 |
|------|------|-----------|------------|
| 任务发布 | `_tango_tasks` 集合 `InsertOne` (`internal/core/taskqueue/queue.go:107`) | worker `Claim` 轮询 (`internal/service/worker/worker.go:90-113`) | 10s |
| 配置发布 | `_tango_config` 集合 `$set` (`internal/service/worker/report_sync.go`) | report 轮询 `Fetch` (`internal/service/report/report.go:119-176`) | 1h |

**关键观察：** 配置发布在架构上是任务发布的一个子流程——operator 发任务 → worker 拉取(0~10s) → 写配置文档 → report 轮询拉取(0~1h)。两段轮询串联，端到端最长 ~1h+10s。

**目标：** 把两段轮询都换成 change stream，实现端到端 sub-second 实时发布；standalone MongoDB 上两段都透明降级为轮询。Change stream 作为通用基础设施，同时服务任务消费和配置消费。

---

## 方案：通用 Change Stream Watcher，统一应用到配置与任务

### 核心思路

- 抽出通用 `Watcher`（`internal/core/mongowatch/`）封装 change stream 全生命周期：开启、resume token、指数退避重连、standalone 错误识别
- **配置消费侧（report.go）：** Watch `_tango_config` 文档，事件到达时 `Fetch → Merge → filter.Store`；ticker 30s 作为安全网/降级路径
- **任务消费侧（worker.go）：** Watch `_tango_tasks` 集合的 `insert` 事件（按 `target` 字段过滤本实例），事件到达时立即调用 `Claim`；ticker 30s 兜底（重试 backoff 到期、reap、stream 重连窗口）
- standalone MongoDB 上两侧自动降级为轮询行为

### 端到端数据流

```
operator.Publish → Insert _tango_tasks
                         │
                ┌────────┴────────┐
        oplog 复制              poll ticker (30s 兜底)
                │                  │
       worker Watcher < 1s    worker poll
                └────────┬────────┘
                    Claim 成功
                         │
                  执行 report_sync
                         │
                $set _tango_config
                         │
                ┌────────┴────────┐
        oplog 复制              poll ticker (30s 安全网)
                │                  │
       report Watcher < 1s    report poll
                └────────┬────────┘
                  Fetch → Merge → FilterChanged
                         │
                filter.Store(newFilter)  ← 原子热换
```

**端到端延迟：**
- replica set：~1-2s（两段 change stream 串行）
- standalone：~60s 上限（两段 30s ticker 串行）

### 降级路径

```
任一侧 Watch() 返回 CommandError{Code:40573}  →  对应侧 notifyCh = nil
该侧 select 只响应 ticker.C  →  行为退化为对应的 polling 行为
两侧独立判断、独立降级
```

---

## 实现细节

### 通用 Watcher：`internal/core/mongowatch/watcher.go`

封装 change stream 全生命周期。约 120 行。

```go
type Watcher struct {
    coll        *mongo.Collection
    pipeline    mongo.Pipeline  // 由调用方提供 $match
    logger      *logrus.Logger
    resumeToken bson.Raw
}

func New(coll *mongo.Collection, pipeline mongo.Pipeline, logger *logrus.Logger) *Watcher

// Watch 开启 change stream，事件到达时向返回的 channel 发信号。
// 仅在 standalone MongoDB 时返回 non-nil error，调用方据此降级为轮询。
// ctx 取消时 channel 被关闭，goroutine 退出。
func (w *Watcher) Watch(ctx context.Context) (<-chan struct{}, error)

func IsStandaloneError(err error) bool  // mongo.CommandError{Code: 40573}
```

内部要点：
- `SetMaxAwaitTime(10s)` 防止网络分区时永久阻塞
- 重连使用 `SetResumeAfter(resumeToken)` 断点续传
- 退避：指数退避，上限 30s

### 配置侧：`internal/service/report/report.go` `syncRemoteConfig`

```go
var notifyCh <-chan struct{}
if !d.cfg.RemoteConfig.DisableChangeStream {
    w := remoteconfig.NewWatcher(coll, d.cfg.RemoteConfig.DocumentID, d.logger)
    if ch, err := w.Watch(ctx); err == nil {
        notifyCh = ch
    } else {
        d.logger.WithError(err).Warn("report: change stream unavailable; using polling")
    }
}

ticker := time.NewTicker(d.cfg.RemoteConfig.SyncInterval)
defer ticker.Stop()

applySync := func() { /* Fetch → Merge → Validate → FilterChanged → Store */ }

for {
    select {
    case <-ctx.Done(): return
    case _, ok := <-notifyCh:
        if !ok { notifyCh = nil }  // stream 关闭，退化为纯 ticker
        applySync()
    case <-ticker.C:
        applySync()
    }
}
```

`notifyCh` 为 nil 时，nil channel case 永不触发，行为完全等同今日的轮询。

### 任务侧：`internal/service/worker/worker.go` `Run`

```go
var notifyCh <-chan struct{}
if !a.cfg.Worker.DisableChangeStream {
    w := taskqueue.NewTaskWatcher(
        a.mongo.DB.Collection(a.cfg.Worker.TasksCollection),
        a.cfg.InstanceID,
        a.logger,
    )
    if ch, err := w.Watch(ctx); err == nil {
        notifyCh = ch
    } else {
        a.logger.WithError(err).Warn("worker: task change stream unavailable; using polling")
    }
}

poll := time.NewTicker(a.cfg.Worker.PollInterval)
defer poll.Stop()

for {
    // drain：能 claim 多少就 claim 多少
    for {
        task, err := a.queue.Claim(ctx, a.cfg.InstanceID, a.cfg.Worker.LeaseDuration)
        if err == nil { a.runTask(ctx, task); continue }
        if err == taskqueue.ErrNoTask { a.reap(ctx); break }
        a.logger.WithError(err).Warn("worker: claim failed"); break
    }
    select {
    case <-ctx.Done(): return nil
    case _, ok := <-notifyCh:
        if !ok { notifyCh = nil }
    case <-poll.C:
    }
}
```

**关键点：**
- Change stream 唤醒 ≠ 一定 claim 成功：多 worker 同时被唤醒，只有一个赢得 `findOneAndUpdate`，其他 `ErrNoTask` 退回等待。语义天然正确。
- `target` 字段 `$match` 减少噪声：`{operationType: "insert", $or: [{"fullDocument.target": ""}, {"fullDocument.target": instanceID}]}`
- `notBefore` backoff 到期不会产生 insert 事件（属于 update），仍依赖 ticker 拉起——ticker 不可省略的原因之一
- standalone 降级：notifyCh = nil，行为等同今日轮询

### 配置项

`config/config.go` 两个 struct 各加一个开关字段：

```go
// RemoteConfig.DisableChangeStream / WorkerConfig.DisableChangeStream
// 强制 polling-only 模式（即使在 replica set 上）。默认 false。
DisableChangeStream bool `mapstructure:"disableChangeStream"`
```

`config/config.go` 默认常量统一为 30s：

```go
DefaultRemoteConfigInterval = 30 * time.Second   // was time.Hour
DefaultWorkerPollInterval   = 30 * time.Second   // was 10 * time.Second
```

**语义统一：** PollInterval/SyncInterval 都从"主轮询间隔"变为"change stream 安全网 + standalone 降级时的主轮询间隔"，两侧默认 30s。

**对 `notBefore` backoff 的影响：** 失败重试任务 backoff 到期靠 ticker 拉起，30s 比 10s 多 20s 最坏唤醒延迟——但 `retryBase=2s` 起步、`retryCap=5min` 上限，本身就是粗粒度退避，30s 兜底完全可接受。

---

## 关键文件

### 配置侧（已实施）

| 文件 | 变更 | 规模 |
|------|------|------|
| `internal/core/remoteconfig/watcher.go` | 新建 | ~120 行 |
| `internal/service/report/report.go:123-176` | 修改 | ~40 行 |
| `config/config.go:292-309` | 修改 | +4 行字段 |
| `config/defaults.go:84-94` | 修改 | 常量 1h → 30s |

### 任务侧（待实施）

| 文件 | 变更 | 规模 |
|------|------|------|
| `internal/core/mongowatch/watcher.go` | 新建（重构） | 把 remoteconfig/watcher.go 泛化迁移过来，~120 行 |
| `internal/core/remoteconfig/watcher.go` | 简化为 thin wrapper | ~30 行 |
| `internal/core/taskqueue/watcher.go` | 新建 | 给 worker 用的帮助函数：构造 `_tango_tasks` insert pipeline + target 过滤，~30 行 |
| `internal/service/worker/worker.go:73-114` | 修改 `Run` | notifyCh + drain 循环改造，~30 行 |
| `config/config.go` (WorkerConfig + 常量) | 修改 | +3 行字段；`DefaultWorkerPollInterval` 10s → 30s |

合计约 330 行新增/修改，3 个新文件，4 个文件小幅修改。

---

## 向后兼容性

- 未配置 `disableChangeStream` 的现有 YAML：两侧均自动尝试 change stream，standalone 自动降级，无感知
- 已配置 `remoteConfig.syncInterval` 的现有 YAML：值继续生效，仅语义从"主轮询间隔"变为"安全网间隔"
- 已配置 `worker.pollInterval` 的现有 YAML：值继续生效，作为 backoff/reap/兜底的 ticker
- Gateway / Operator 角色：仅作为 publisher 不参与消费，不涉及任何改动

---

## 验证

1. **单元测试** `mongowatch.IsStandaloneError`：构造 `mongo.CommandError{Code:40573}`，断言返回 true
2. **单元测试** 通用 `Watcher`（mock cursor）：notifyCh 每事件一次信号、resumeToken 更新、ctx 取消后退出
3. **集成测试 - 配置（replica set）**：写入 `_tango_config`，断言 report 侧 notifyCh 在 2s 内收到信号
4. **集成测试 - 任务（replica set）**：`InsertOne` 到 `_tango_tasks`，断言 worker 侧 notifyCh 在 2s 内收到信号且 `Claim` 立即返回该任务
5. **多 worker 竞争**：N 个 worker 同时被唤醒，只有一个 claim 成功，其余 `ErrNoTask` 退回等待
6. **降级测试（standalone）**：两侧 `Watch()` 都返回 error，notifyCh 为 nil，ticker-only 路径运行如旧
7. **端到端**：`tango report run` + `tango worker run`，通过 gateway 发 `TaskReportSync`，从发布到 `"hot-reloaded filter from remote config"` 日志 < 2s（replica set）；standalone 应在 ~60s 内
