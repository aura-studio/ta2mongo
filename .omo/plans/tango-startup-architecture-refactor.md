# Tango 启动模式与体系重构方案

## 1. 背景

当前 tango 的启动方式主要按命令树划分：

| 当前命令 | 当前定位 | 实际职责 |
|---|---|---|
| `tango daemon standalone` | daemon standalone | 常驻文件采集、解析、过滤、写入 MongoDB |
| `tango daemon agent` | daemon agent | 常驻文件采集 + 远端配置同步 + MongoDB 任务队列 worker |
| `tango client ingest/upload/backfill/sql/publish` | client | 一次性操作命令 |
| `tango client serve` | client HTTP | 常驻 HTTP/REST gateway |

这个分类的问题是：它把不同维度混在了一起。

- **生命周期**：常驻服务 vs 一次性命令。
- **执行职责**：采集上报、任务消费、HTTP 接入、人工操作。
- **控制入口**：本地 CLI、HTTP 请求、MongoDB 任务队列、远端配置。
- **配置来源**：本地配置、远端配置、task payload、request body。

其中最大的问题是 `tango daemon agent`：它同时启动 report pipeline 和 task worker。两个职责在同一进程内耦合，并通过共享 `filter.Holder` 实现 `report-sync` 热更新。这使得“agent”既像采集服务，又像任务执行服务，命令名和体系边界都不清晰。

另一个问题是 `tango client serve`：它虽然挂在 `client` 下，但实际是一个常驻 HTTP gateway，不是一次性 client operator。

## 2. 重构目标

本次重构的目标不是简单改命令名，而是重新建立 tango 的运行体系。

### 2.1 明确运行角色

把 tango 拆成四类清晰角色：

| 新角色 | 生命周期 | 主要职责 | 控制入口 |
|---|---|---|---|
| **Report Service** | 常驻 | 文件追尾、解析、过滤、写 MongoDB | 本地配置 + 可选远端配置 |
| **Task Worker Service** | 常驻 | 消费 MongoDB task queue，执行 report-sync/backfill/sql | MongoDB 任务队列 |
| **HTTP Gateway Service** | 常驻 | 暴露 REST API，把 HTTP 请求转成 SDK 操作或任务发布 | HTTP 请求 |
| **Operator CLI** | 一次性 | ingest/upload/backfill/sql/publish 等人工或脚本操作 | 本地 CLI |

### 2.2 分离部署模式和功能模式

原来的 `standalone` / `agent` 更像部署 profile，不应该作为核心功能分类。

重构后应区分：

- **功能角色**：report、worker、gateway、operator。
- **部署 profile**：local、managed、all-in-one。

功能角色决定代码边界；部署 profile 只是组合启动方式。

### 2.3 降低进程内耦合

`report-sync` 当前依赖同进程共享 `filter.Holder`。重构后应让 report service 通过统一的 remote config watch/apply 机制热更新 filter，而不是依赖 task worker 和 report pipeline 在同一进程内。

## 3. 目标命令行设计

建议把顶层命令改为按运行角色划分：

```bash
tango report run
tango worker run
tango gateway serve
tango operator ingest
tango operator upload
tango operator backfill
tango operator sql
tango operator publish report-sync
tango operator publish backfill
tango operator publish sql
```

### 3.1 Report Service

```bash
tango report run --config report.yaml
```

职责：

- 发现并追尾本地日志文件。
- 解析 TA JSON line。
- 应用 report filter。
- 做 identity resolve。
- 批量写入 MongoDB。
- 可选启用 remote config watch，使 filter 热更新。

建议参数：

| 参数 | 说明 |
|---|---|
| `--config` | report 配置文件路径 |
| `--mongo.uri` | MongoDB URI override |
| `--logging.level` | 日志级别 |
| `--report.source.log-pattern` | 采集文件模式 |
| `--report.filter.include` | 本地 include filter override |
| `--report.filter.exclude` | 本地 exclude filter override |
| `--remote-config.enabled` | 是否启用远端配置 |
| `--remote-config.collection` | 远端配置集合 |
| `--remote-config.key` | 远端配置 key |

### 3.2 Task Worker Service

```bash
tango worker run --config worker.yaml --instance-id worker-001
```

职责：

- 注册实例心跳。
- 从 MongoDB task queue claim 任务。
- 续约 lease。
- 执行任务。
- 完成/失败/reap 任务。

支持任务：

| 任务类型 | 执行内容 |
|---|---|
| `report-sync` | 写入 remote config 文档，触发 report service 自己 watch/apply |
| `backfill` | 执行历史回填 |
| `sql` | 执行 TA SQL 并导入结果 |

建议参数：

| 参数 | 说明 |
|---|---|
| `--config` | worker 配置文件路径 |
| `--instance-id` | worker 实例 ID，必填 |
| `--mongo.uri` | MongoDB URI override |
| `--logging.level` | 日志级别 |
| `--tasks.collection` | task queue 集合 |
| `--instances.collection` | worker heartbeat 集合 |
| `--tasks.poll-interval` | claim 轮询间隔 |
| `--tasks.lease-ttl` | lease 时长 |

### 3.3 HTTP Gateway Service

```bash
tango gateway serve --config gateway.yaml --addr :8080
```

职责：

- 常驻 HTTP 服务。
- 暴露 ingest/upload/backfill/sql/publish API。
- 持有一个 connected SDK client。
- 把 HTTP request 转换成 SDK 调用或 task publish。

建议路由保持：

| HTTP 路由 | 对应能力 |
|---|---|
| `GET /healthz` | 健康检查 |
| `POST /ingest` | 批量字符串上报 |
| `POST /upload` | 文件上传 |
| `POST /backfill` | 直接执行 backfill |
| `POST /sql` | 直接执行 SQL |
| `POST /publish/report-sync` | 发布 report-sync 任务 |
| `POST /publish/backfill` | 发布 backfill 任务 |
| `POST /publish/sql` | 发布 sql 任务 |

### 3.4 Operator CLI

```bash
tango operator ingest
tango operator upload
tango operator backfill
tango operator sql
tango operator publish report-sync
tango operator publish backfill
tango operator publish sql
```

职责：

- 一次性命令。
- 面向人工、脚本、CI/CD、运维平台。
- 不持有长期生命周期。

## 4. 部署 Profile

命令按角色拆开后，可以再提供 profile 作为组合启动的便利层。

### 4.1 local profile

等价于旧 `daemon standalone`。

```bash
tango profile local --config local.yaml
```

组合：

- report service。
- 不启用 worker。
- 不启用 gateway。
- 可选不启用 remote config。

### 4.2 managed profile

等价于旧 `daemon agent` 的核心场景，但概念上拆成两个服务。

推荐部署方式：

```bash
tango report run --config report.yaml
tango worker run --config worker.yaml --instance-id worker-001
```

如果需要保留单进程便利模式，可提供：

```bash
tango profile managed --config managed.yaml --instance-id node-001
```

但该命令应明确标注为组合 profile，而不是基础角色。

组合：

- report service。
- worker service。
- remote config enabled。

### 4.3 gateway profile

```bash
tango gateway serve --config gateway.yaml --addr :8080
```

独立作为 HTTP 接入层，不再归类为 client。

## 5. 配置体系重构

### 5.1 当前配置问题

当前配置分为：

- daemon config：`generic` + `report` + `agent`。
- client config：`logging` + `mongo` + `stringUpload` + `fileUpload` + `backfill` + `backfillFilter` + `sql` + `publish` + `server`。

问题：

- `generic` 只在 daemon 中存在，client 使用平铺的 `logging` / `mongo`。
- `agent` 同时代表运行模式和任务 worker 配置。
- `server` 藏在 client config 下，但它实际上是 gateway service 配置。
- backfill/sql/publish 既被 operator 使用，也被 gateway/worker 使用，配置归属不清。

### 5.2 建议配置结构

统一为：

```yaml
runtime:
  logging:
    level: info
  mongo:
    uri: mongodb://localhost:27017/tango
    maxElapsedTime: 30s

report:
  source:
    logPattern: []
  pipeline:
    workers: 4
    batchSize: 1000
  filter:
    include: []
    exclude: []

remoteConfig:
  enabled: false
  collection: _tango_config
  key: report
  refreshInterval: 30s

tasks:
  collection: _tango_tasks
  instancesCollection: _tango_instances
  pollInterval: 5s
  leaseTTL: 60s
  instanceTTL: 120s

gateway:
  addr: :8080

upload:
  string:
    batchSize: 1000
    filter:
      include: []
      exclude: []
  file:
    logPattern: []
    checkpointCollection: _tango_upload_checkpoint

backfill:
  apiBaseURL: ""
  token: ""
  projectID: ""

backfillFilter:
  table: event
  events: []
  include: []
  exclude: []

sql:
  defaultTable: event
```

### 5.3 配置归属

| 配置段 | 使用方 |
|---|---|
| `runtime` | 所有角色 |
| `report` | report service |
| `remoteConfig` | report service + worker report-sync |
| `tasks` | worker service + operator publish + gateway publish |
| `gateway` | gateway service |
| `upload` | operator + gateway + SDK |
| `backfill` | worker + operator + gateway |
| `backfillFilter` | worker + operator + gateway |
| `sql` | worker + operator + gateway |

## 6. 新体系架构

### 6.1 目标分层

```text
cmd/
  report/       # tango report run
  worker/       # tango worker run
  gateway/      # tango gateway serve
  operator/     # tango operator ...
  profile/      # optional: local/managed compatibility profiles

config/
  runtime.go
  report.go
  worker.go
  gateway.go
  operator.go
  loader.go

client/         # external Go SDK, keep public API stable

internal/
  core/         # talog, filter, store, taskqueue, remoteconfig, tailer, dynamicbatch
  process/      # ingest pipeline, routing, batch, worker logic
  service/
    report/     # report runtime, formerly service/daemon report part
    worker/     # task worker runtime, formerly service/agent
    gateway/    # HTTP server runtime, formerly cmd/client/serve.go logic
    backfill/   # backfill execution
```

### 6.2 依赖方向

```text
cmd -> config + service/client SDK
service -> process + core
process -> core
client SDK -> core/process/service adapters as needed
core -> external libs only
```

### 6.3 Report-sync 新机制

旧机制：

```text
agent task worker -> shared in-process filter.Holder -> report pipeline
```

新机制：

```text
operator/gateway -> publish report-sync task
worker -> claim report-sync -> write remote config document
report service -> watch/poll remote config -> apply to local filter.Holder
```

好处：

- worker 和 report 可以独立部署。
- report-sync 不再要求任务执行者和采集进程同进程。
- `filter.Holder` 仍保留在 report service 内部，用于热替换。

需要注意：

- `report-sync` 的完成语义从“已应用到本进程 filter”变成“已写入 remote config”。
- 若要确认所有 report service 都已应用，需要新增 config version / ack 机制。初期可以不做，文档中明确语义。

## 7. 兼容策略

建议分阶段迁移，而不是一次性破坏旧命令。

### 7.1 第一阶段：新增命令，保留旧命令

新增：

```bash
tango report run
tango worker run
tango gateway serve
tango operator ...
```

保留旧命令：

```bash
tango daemon standalone
tango daemon agent
tango client ...
tango client serve
```

旧命令输出 warning：

| 旧命令 | 建议迁移 |
|---|---|
| `tango daemon standalone` | `tango report run --remote-config.enabled=false` |
| `tango daemon agent` | `tango profile managed` 或 `tango report run` + `tango worker run` |
| `tango client serve` | `tango gateway serve` |
| `tango client ingest` | `tango operator ingest` |
| `tango client upload` | `tango operator upload` |
| `tango client backfill` | `tango operator backfill` |
| `tango client sql` | `tango operator sql` |
| `tango client publish ...` | `tango operator publish ...` |

### 7.2 第二阶段：文档和示例切换到新命令

- `doc/arch.md` 改成按 report/worker/gateway/operator 讲体系。
- `doc/usage.md` 所有主路径切到新命令。
- `examples/config` 从 standalone/agent/client 改成 report/worker/gateway/operator/profile。

### 7.3 第三阶段：旧命令进入 deprecated 状态

- 保留旧命令 1-2 个版本。
- 输出明确 deprecation notice。
- 不再在文档主流程中出现旧命令。

## 8. 实施计划

### Phase 0: 基线确认

- 跑 `go test ./...`，记录当前测试状态。
- 记录旧命令帮助输出。
- 确认现有 config 示例能启动。

### Phase 1: 命令树重排

- 新增 `cmd/report`，迁移 `daemon standalone` 的 report-only 启动逻辑。
- 新增 `cmd/worker`，迁移 `daemon agent` 中 task agent 启动逻辑。
- 新增 `cmd/gateway`，迁移 `client serve`。
- 新增 `cmd/operator`，迁移 `client` 一次性命令。
- 保留旧命令作为 wrapper/alias。

验收：

- `tango report run --help` 清晰描述 report service。
- `tango worker run --help` 清晰描述 task worker service。
- `tango gateway serve --help` 清晰描述 HTTP gateway。
- `tango operator --help` 只包含一次性操作命令。
- 旧命令仍可运行并提示新命令。

### Phase 2: 服务边界重构

- 将 `internal/service/daemon` 重命名/拆分为 `internal/service/report`。
- 将 `internal/service/agent` 重命名/调整为 `internal/service/worker`。
- 将 HTTP server 逻辑从 `cmd/client/serve.go` 下沉到 `internal/service/gateway`。
- `cmd/*` 只负责参数、配置加载、服务启动。

验收：

- command 层没有复杂业务逻辑。
- report service 不依赖 worker service。
- worker service 不持有 report service 的 in-process filter holder。
- gateway service 可独立启动。

### Phase 3: Report-sync 语义改造

- worker 执行 `report-sync` 时只写 remote config。
- report service 通过 remote config polling/watch 自行 apply。
- 保留 `filter.Holder`，但只在 report service 内部使用。

验收：

- `report-sync` 不要求 worker 和 report 同进程。
- report service 可独立接收远端配置热更新。
- standalone/local 模式可关闭 remote config。

### Phase 4: 配置体系统一

- 引入统一 `runtime` 配置段。
- 将 daemon/client 差异配置改为 role-specific config。
- 保持旧配置兼容读取，或者提供迁移工具/文档。

验收：

- 新配置文件按 role 命名：`report.yaml`、`worker.yaml`、`gateway.yaml`、`operator.yaml`。
- 旧 `standalone.yaml`、`agent.yaml`、`client.yaml` 仍能被 wrapper 命令使用。
- flag 命名与配置 key 一致。

### Phase 5: 文档和示例更新

- 重写 `doc/arch.md` 的角色分类。
- 更新 `doc/usage.md` 的启动命令。
- 更新 `doc/config.md` 的配置字段归属。
- 更新 examples。

验收：

- 文档中不再把 agent 定义为“report + task”的基础模式。
- client serve 不再作为 client 的主要运行形态描述。
- 每种角色都有独立启动示例。

## 9. 风险与注意事项

### 9.1 report-sync 完成语义变化

拆进程后，worker 完成 `report-sync` 只能表示 remote config 写入成功，不能表示所有 report service 已经应用。

可选增强：

- remote config 增加 `version`。
- report service 应用后写入 `_tango_config_ack`。
- operator/gateway 可查询 ack 状态。

### 9.2 配置兼容风险

旧配置结构和新配置结构差异较大，建议至少保留一个版本的兼容加载。

### 9.3 taskqueue 可靠性不能破坏

`taskqueue` 已有 claim、lease、fail、reap、retry backoff 等可靠性逻辑。重构时不要重写其核心算法，只调整调用边界。

### 9.4 store 写模型不能顺手重构

`store/writemodel.go` 涉及 user/event 写入语义、`_ts` 防回退、identity 逻辑。启动体系重构阶段不要顺手改写存储模型。

### 9.5 SDK API 保持稳定

`client/` 是外部 Go SDK。命令行重构不应强制改变 SDK 公共 API。

## 10. 推荐最终架构图

```text
                    +----------------------+
                    |  tango operator ...  |
                    |  one-shot CLI        |
                    +----------+-----------+
                               |
                               v
                         client SDK
                               |
                               v
+----------------+     +-------+--------+      +----------------+
| tango gateway  | --> | MongoDB / TA   | <--- | tango worker   |
| HTTP service   |     | API / storage  |      | task service   |
+----------------+     +-------+--------+      +----------------+
                               ^
                               |
                    +----------+-----------+
                    | tango report run     |
                    | file tail -> Mongo   |
                    +----------------------+
```

角色关系：

- `report` 负责持续采集。
- `worker` 负责异步任务。
- `gateway` 负责 HTTP 接入。
- `operator` 负责一次性人工/脚本操作。
- `client SDK` 是 operator/gateway/外部程序共享的调用层。
- `MongoDB` 既是数据存储，也是 remote config / task queue 控制面。

## 11. 建议结论

建议将 tango 从当前的：

```text
daemon standalone / daemon agent / client / client serve
```

重构为：

```text
report service / worker service / gateway service / operator CLI
```

并把 `standalone`、`agent` 从“基础启动模式”降级为“兼容 profile”。这样命令行参数、配置体系、服务边界和部署方式会更加一致，也能为后续拆分服务、独立部署、任务可靠性增强和 HTTP 接入扩展留下空间。
