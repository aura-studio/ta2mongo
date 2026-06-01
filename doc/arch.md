# tango 架构说明

## 1. 目标

`tango`：将 ThinkingData 日志（JSON 行）采集并写入 MongoDB 的 `user` / `event` /
`dead_letter` 集合。v1.0.0 是**单一二进制、纯上报 daemon**，两种运行模式由子命令选择：

| 模式 | 子命令 | 默认配置文件 | 职责 |
|------|--------|----------|------|
| **standalone** | `tango daemon standalone` | `standalone.{yaml,yml,json}` | 纯上报、完全本地自治：filter 写死在本地配置。 |
| **cluster** | `tango daemon cluster` | `cluster.{yaml,yml,json}` | 上报 + 从 MongoDB 控制面文档同步并**热重载**上报 filter。 |

> 配置文件 YAML、JSON 均支持（按扩展名识别）。`--config` 留空时各子命令在**二进制同级目录**
> 查找各自默认文件，找不到则回退到默认值 + 环境变量 + flag。所有键可用 `TANGO_*` 环境变量
> 覆盖；命令行用**完整层级名** flag 覆盖（如 `--generic.mongo.uri`）。

---

## 2. 目录结构

```
.
├── main.go          # 单一入口：装配 cmd/daemon 子命令并执行
├── cmd/
│   └── daemon/      # `tango daemon` 子命令树（standalone / cluster 两种模式）
├── config/          # DaemonConfig(daemon.go) + 运行时 Config(config.go) + loader.go
├── doc/ examples/
└── internal/        # 按单向依赖分三层（service → process → core）
    ├── core/        # 无内部依赖的基础件：cli filter remoteconfig store talog tailer dynamicbatch
    ├── process/     # 仅依赖 core 的处理层：pipeline
    └── service/     # 依赖 process+core 的运行时：daemon
```

> **internal 分层规则**：依赖只能从上往下（service → process → core），不得反向或成环。

---

## 3. 数据流（两种模式共用上报管线）

```
Tailer ──lineCh──▶ Dispatcher(按用户亲和性路由) ──▶ Worker[i](Parse→上报Filter→Identity→Batch) ──▶ MongoDB BulkWrite
```

- **Tailer**：追尾 `report.source.logPattern` 匹配的日志文件（hybrid / poll / event）。
- **Dispatcher**：按 `#account_id`（优先）或 `#distinct_id` 一致性哈希路由到固定 worker，
  保证同一用户的操作顺序处理。
- **上报 filter**（`report.filter.local`）：对每条记录生效的 expr 表达式（针对 `#type` /
  `#event_name` / `properties.*`）。被过滤掉的记录**不写 dead_letter**，是有意丢弃。
- **Store**：批量 bulk-write，按 `#uuid`（event）/ `#user_id`（user）upsert，带指数退避重试
  （`generic.mongo.maxElapsedTime` 为单次写的退避总时长上限）。

---

## 4. cluster 模式：控制面 filter 同步

`tango daemon cluster` 在上报之上启用一个同步循环：

- **启动时**：从 `report.filter.remote` 指定的集合 / 文档拉取一次，把远端 filter 合并覆盖
  本地 filter。
- **运行中**：每 `syncInterval`（默认 1h）再拉取，命中 include/exclude 变更即**热替换** live
  filter（无需重启），让数据中心能渐进放量。
- **不可覆盖**：连接类字段（`generic.mongo.uri`、`report.filter.remote` 本身）永远来自本地文件，
  不接受远端覆盖。

standalone 模式不启用此循环，filter 完全由本地配置决定。

控制面文档形如：

```json
{ "_id": "default", "filter": { "include": ["#type == \"track\""], "exclude": [] } }
```

---

## 5. 配置结构

daemon 配置分两部分：**generic**（`logging` + `mongo`，进程级共享）与 **report**
（上报管线：`source` / `pipeline` / `filter`）。`report.filter` 又分 `local`（本地规则）
与 `remote`（cluster 模式的控制面同步源）。运行时投影为扁平的 `config.Config`
（`Logging` / `Mongo` / `Source` / `Pipeline` / `Filter` / `RemoteConfig`）。详见
[config.md](config.md)。
