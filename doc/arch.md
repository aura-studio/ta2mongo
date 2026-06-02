# tango 架构说明

## 1. 目标

`tango` 将 ThinkingData 日志 JSON 行采集并写入 MongoDB 的 `user` / `event` / `dead_letter` 集合，同时提供任务队列、历史回填、SQL 导入和 HTTP 接入能力。

tango 仍是单一二进制，但启动体系按运行角色组织：

| 角色 | 命令 | 生命周期 | 职责 |
|---|---|---|---|
| **Report Service** | `tango report run` | 常驻 | 文件追尾、解析、report filter、identity、批量写 MongoDB |
| **Task Worker Service** | `tango worker run` | 常驻 | 注册心跳，claim/renew/execute task queue 任务 |
| **HTTP Gateway Service** | `tango gateway serve` | 常驻 | 暴露 REST API，把 HTTP 请求转为 SDK 操作或任务发布 |
| **Operator CLI** | `tango operator ...` | 一次性 | ingest/upload/backfill/sql/publish 等人工或脚本操作 |

旧命令 `tango daemon ...` 和 `tango client ...` 保留为兼容入口，但不再作为主架构分类。

## 2. 启动模式重构

原先 `standalone` / `agent` 是部署模式和功能模式混用：

- `daemon standalone` 只做 report。
- `daemon agent` 同时做 report、remote config sync、task worker。
- `client serve` 挂在 client 下，但实际是 gateway 常驻服务。

重构后：

```text
基础角色: report / worker / gateway / operator
兼容 profile: local / managed
```

| 旧入口 | 新入口 | 状态 |
|---|---|---|
| `tango daemon standalone` | `tango report run` | deprecated wrapper |
| `tango daemon agent` | `tango report run` + `tango worker run` | deprecated wrapper |
| `tango client serve` | `tango gateway serve` | deprecated wrapper |
| `tango client <subcmd>` | `tango operator <subcmd>` | deprecated wrapper |
| - | `tango profile local` | compatibility profile |
| - | `tango profile managed` | compatibility profile |

## 3. 目录结构

```text
.
├── main.go
├── cmd/
│   ├── report/      # tango report run
│   ├── worker/      # tango worker run
│   ├── gateway/     # tango gateway serve (+ ServeCommand reused by client wrapper)
│   ├── operator/    # tango operator ... (+ Subcommands reused by client wrapper)
│   ├── profile/     # tango profile local/managed (compatibility profiles)
│   ├── shared/      # cmd glue: config resolution, client building, service runners
│   ├── daemon/      # legacy daemon wrapper (standalone/agent)
│   └── client/      # legacy client wrapper (delegates to operator + gateway)
├── config/          # RoleConfig (unified) + role loaders; DaemonConfig/ClientConfig (legacy); shared runtime Config
├── client/          # 对外 Go SDK
├── doc/ examples/
└── internal/
    ├── core/        # cli remoteconfig filter store talog tailer dynamicbatch taskqueue
    ├── process/     # ingest pipeline
    └── service/
        ├── report/  # report service runtime (report.Service)
        ├── worker/  # task worker service runtime (worker.Service)
        ├── gateway/ # HTTP gateway runtime
        └── backfill/
```

依赖方向保持：

```text
cmd -> config + service/client SDK
service -> process + core
process -> core
core -> external libs only
```

## 4. Report Service

命令：

```bash
tango report run
```

数据流：

```text
Tailer -> Dispatcher(按用户亲和性路由) -> Worker[i](Parse -> Filter -> Identity -> Batch) -> MongoDB BulkWrite
```

职责：

- 读取 `report.source.logPattern`。
- 追尾文件并输出 line channel。
- 解析 TA JSON。
- 应用 report filter。
- 根据 `#account_id` / `#distinct_id` 做用户亲和性路由。
- 批量写入 MongoDB。
- 可选启用 remote config hot reload。

report service 不启动 task worker，也不持有 worker lifecycle。

## 5. Task Worker Service

命令：

```bash
tango worker run --instanceID worker-1
```

职责：

- 注册 `_tango_instances` 心跳。
- 从 `_tango_tasks` claim 任务。
- 执行任务期间续约 lease。
- 完成或失败任务。
- 定期 reap orphaned / stuck tasks。

任务类型：

| 任务类型 | 说明 |
|---|---|
| `report-sync` | 写入 remote config 文档；独立 report service 通过自己的 sync loop 应用 |
| `backfill` | 执行历史回填 |
| `sql` | 执行 SQL 并导入结果 |

独立 `worker run` 不要求 `report.source.logPattern`，也不持有 report 的 `filter.Holder`。worker 与 report 完全解耦：执行 `report-sync` 只写 remote config 文档。

兼容的 `daemon agent` / `profile managed` 仍会同进程启动 report + worker，但二者不再共享 in-process `filter.Holder`——同进程的 report service 通过自己的 remote config sync loop 应用过滤器（agent/managed 模式下 remoteConfig 默认开启），代价是按 `syncInterval` 的延迟而非即时热替换。

## 6. HTTP Gateway Service

命令：

```bash
tango gateway serve
```

gateway 是常驻服务，使用现有 ClientConfig 和 Go SDK，暴露：

```text
GET  /healthz
POST /ingest
POST /upload
POST /backfill
POST /sql
POST /publish/report-sync
POST /publish/backfill
POST /publish/sql
```

HTTP 运行时位于 `internal/service/gateway`；命令层 `cmd/gateway` 只做参数与配置加载，`ServeCommand` 同时被兼容的 `tango client serve` 复用。

## 7. Operator CLI 与 SDK

命令：

```bash
tango operator ingest
tango operator upload
tango operator backfill
tango operator sql
tango operator publish report-sync
tango operator publish backfill
tango operator publish sql
```

operator 是一次性操作入口，复用 `client/` Go SDK。SDK 公共 API 保持稳定。

## 8. 两种 filter

| | 上报 filter | backfill filter |
|---|---|---|
| 使用方 | report service、string/file upload | backfill、sql |
| 维度 | `#type` / `#event_name` / 属性 | 表名(event/user) + 事件/属性，不含 `#type` |
| 表达式 | include / exclude | include / exclude + events 语法糖 |

`config.BackfillFilterConfig.IncludeExprs()` 把 `events` 折叠进 include，再复用 `filter.New` / `filter.CompileToSQL`。

## 9. Taskqueue 可靠性边界

taskqueue 是可靠性敏感模块，重构启动体系时不改变其核心语义：

- `Claim` 原子领取。
- 长任务续租。
- `Complete` / `Fail` 校验 lease。
- `Fail` 设置退避 `notBefore`。
- `Reap` 清理 orphaned / stuck tasks。
- 实例 heartbeat + TTL。

这些逻辑属于 worker service 的核心可靠性边界，不能随命令行重构顺手重写。

## 10. Report-sync 语义

统一的角色路径（独立部署与同进程兼容模式一致）：

```text
operator/gateway publish report-sync
worker claim task and write remote config document
report service poll remote config and apply filter.Holder
```

不再存在「worker 直接热替换同进程 report filter」的路径——`worker.executeReportSync` 只校验表达式能编译，然后写入 remote config 文档。因此 worker 完成 report-sync 表示 **remote config 写入成功**，而非所有 report service 已经应用。所有 report service（含同进程兼容模式）都通过各自的 remote config sync loop 收敛到该过滤器。若后续需要全局确认语义，可引入 config version + ack collection。
