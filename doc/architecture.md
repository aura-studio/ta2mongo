# tango 架构说明

## 1. 目标

`tango`：将 ThinkingData 日志（JSON 行）采集并写入 MongoDB 的 `user` / `event` / `dead_letter` 集合。

经过重构，tango 围绕**两种角色**组织，每种角色有**独立的程序入口、独立的配置文件**，共享 `internal/` 与 `config/`：

| 角色 | 二进制 | 配置文件 | 职责 |
|------|--------|----------|------|
| **Daemon** | `cmd/tangod` | `daemon.{yaml,json}` | 上报：追尾日志 → 解析 → **上报 filter** → 写入 MongoDB。**agent 是 daemon 的一个可开关功能**（`agent.enabled`）。 |
| **Client** | `cmd/tango` | `client.{yaml,json}` | 操作 / SDK：五项分区功能，三种使用方式（CLI / HTTP REST / Go 库）。 |

> 配置文件 YAML、JSON 均支持（按扩展名自动识别）；所有键可用 `TANGO_*` 环境变量覆盖，常用项也有 CLI flag（`--mongoURI` / `--logLevel` / `--instanceID`）。

---

## 2. 目录结构

```
.
├── cmd/
│   ├── tangod/      # daemon 入口（上报 + 可选 agent）
│   └── tango/       # client 入口（CLI 子命令 + serve HTTP）
├── config/          # 共享子结构 + DaemonConfig(daemon.go) / ClientConfig(client.go) + loader.go
├── client/          # 对外 Go 库（embeddable SDK，五项功能的统一实现）
├── doc/ examples/
└── internal/        # 两个二进制共享的内部实现
    ├── daemon/ once/ ingest/      # 处理管线
    ├── backfill/                  # TA OpenAPI 历史回填
    ├── agent/ taskqueue/          # 任务 agent + MongoDB 任务队列
    ├── filter/ remoteconfig/      # 过滤 + 远端配置
    └── store/ talog/ tailer/ pipeline/ dynamicbatch/
```

---

## 3. Daemon 角色（`tangod`）

### 3.1 上报功能（reporting）

数据流（与原 daemon 一致）：

```
Tailer ──lineCh──▶ Dispatcher(按用户亲和性路由) ──▶ Worker[i](Parse→上报Filter→Identity→Batch) ──▶ MongoDB BulkWrite
```

- **上报 filter**（`reportFilter`）：对每条记录生效的 expr 表达式（针对 `#type` / `#event_name` / `properties.*`）。它与 backfill filter 是**两个独立概念**。
- 远端配置（`remoteConfig`）可热更新上报 filter；report-sync 任务即写入该文档。

### 3.2 agent 功能（daemon 的开关项）

`agent.enabled: true` 时，daemon 进程内额外运行 agent：注册心跳、领取并执行已发布的任务、汇报结果，并与上报管线**共享同一个 live filter holder**，使 report-sync 任务可直接热替换上报 filter。

`instanceID` **仅在 agent 配置下**（`agent.instanceID`），开启 agent 时必填；其它情况无意义。

### 3.3 agent 任务（发布式，三种）

| 任务类型 | 说明 | filter 形式 |
|----------|------|-------------|
| **report-sync** | 同步上报 filter：领取者把 payload 的 filter 应用到 daemon 的 live 上报 filter 并持久化到远端配置文档 | 上报 filter（include/exclude） |
| **backfill** | 历史回填 | **backfill filter**：选表 + 事件/属性谓词，**不过滤 #type**；表名属于 filter（配置中无独立 `table` 字段） |
| **sql** | 临时执行一条 SQL 并导入结果 | 复用 backfill filter 的表选择 |

---

## 4. 两种 filter

| | 上报 filter（`reportFilter` / runtime `Filter`） | backfill filter（`backfillFilter`） |
|---|---|---|
| 使用方 | daemon 上报、client 字符串/文件上报 | backfill 任务、client backfill / sql |
| 维度 | `#type` / `#event_name` / 属性 | **表名(event/user)** + 事件/属性（**不含 #type**） |
| 表达式 | `include` / `exclude` | `include` / `exclude` + `events`(语法糖→`#event_name in [...]`) |
| 表名 | — | 在 filter 内（`table`），backfill 配置无独立表名字段 |

`config.BackfillFilterConfig.IncludeExprs()` 把 `events` 折叠进 include，再复用同一套 `filter.New` / `filter.CompileToSQL`，因此本地过滤与 SQL 下推走同一条代码路径。

---

## 5. Client 角色（`tango`）

五项**分区配置**的功能，统一由 `client/` 库实现：

| # | 功能 | 库方法 | 配置段 | 说明 |
|---|------|--------|--------|------|
| 1 | 字符串单次上报（**无重传**） | `Ingest` / `IngestBatch` | `stringUpload` | 一次性写入 |
| 2 | 文件单次上报（**有重传**） | `UploadFiles` | `fileUpload` | 按文件字节偏移检查点；中断/失败后从断点续传，未确认行重发 |
| 3 | backfill 执行 | `RunBackfill` | `backfill` + `backfillFilter` | 复用 internal/backfill |
| 4 | SQL 执行 | `ExecuteSQL` | `sql`（凭据取自 backfill） | 临时 SQL |
| 5 | MongoDB 任务发布 | `PublishReportSync` / `PublishBackfillTask` / `PublishSQLTask` | `publish` | agent 任务机制的发布端 |

三种使用方式（faces）：

- **CLI**：`tango ingest|upload|backfill|sql|publish <report-sync|backfill|sql>`。
- **HTTP/REST**：`tango serve`，暴露 `POST /ingest /upload /backfill /sql /publish/{report-sync,backfill,sql}`。
- **Go 库**：直接 `import rocket-nano/tools/tango/client`。

---

## 6. 任务队列（taskqueue）与可靠性修复

拉模型：agent 用原子 `findOneAndUpdate` 领取任务并打租约；长任务周期续租，持有者宕机则租约过期被他人重领。本次重构修复了以下缺陷：

- **B1** 宕机耗尽重试的任务会永久停在 `claimed`：新增 `Reap` 把 `claimed && 租约过期 && attempts>=maxAttempts` 置为 `failed`。
- **B2** 定向任务目标永久离线则永远 `pending`，且终态任务不清理：`Reap` 对超过宽限期、目标离线的定向 pending 任务置 `failed`；`finishedAt` 上加 TTL 索引清理终态任务。
- **B3** 续租短暂失败导致并发执行 / 过期持有者仍能 finalize：`Complete` / `Fail` 增加 `leaseUntil >= now` 校验，汇报使用有界 context。
- **B4** 重试无退避、瞬间烧尽次数：`Fail` 重试设置 `notBefore = now + 指数退避`，`Claim` 增加 `notBefore <= now` 闸门。
- **B5** 重领覆盖 `startedAt`、初次心跳失败误判离线：`Claim` 用 `$ifNull` 只在首次领取写 `startedAt`；初次心跳带短重试。

agent 在每个空闲轮询周期调用 `Reap` 执行上述维护。

---

## 7. 其余（解析 / 身份 / 写模型 / 索引）

talog 解析、IdentityResolver 身份解析、user/event/dead_letter 写模型与索引、指数退避重试策略均与重构前一致，详见 `store` / `talog` 包源码与本仓库历史版本说明。
