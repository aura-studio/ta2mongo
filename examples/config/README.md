# tango 配置样例

tango 是**单一二进制**（用法见 [../../doc/usage.md](../../doc/usage.md)，字段参考见
[../../doc/config.md](../../doc/config.md)）。命令按**运行角色**划分：report / worker /
gateway / operator，外加兼容用的 daemon / client。

## 角色配置（统一 schema，推荐）

角色配置文件共用同一套统一 schema：顶层分为 `runtime`（logging + mongo，进程级共享）、
`report`、`remoteConfig`、`tasks`、`gateway`、`upload`、`backfill` / `backfillFilter` /
`sql` 等段，每个角色加载器只取自己需要的段。

| 角色 | 子命令 | 目录 | 默认配置名（二进制同级） | 主要配置段 |
|------|--------|------|------|------|
| report  | `tango report run`    | [report/](report/)     | `report.{yaml,yml,json}`   | runtime · report · remoteConfig |
| worker  | `tango worker run`    | [worker/](worker/)     | `worker.{yaml,yml,json}`   | runtime · tasks · remoteConfig |
| gateway | `tango gateway serve` | [gateway/](gateway/)   | `gateway.{yaml,yml,json}`  | runtime · gateway · upload · tasks |
| operator| `tango operator ...`  | [operator/](operator/) | `operator.{yaml,yml,json}` | runtime · upload · backfill · tasks |

```bash
# 角色启动（留空 --config 时自动读取二进制同级的 <role>.{yaml,yml,json}）：
tango report run    --config examples/config/report/report.yaml
tango worker run    --config examples/config/worker/worker.yaml --instanceID worker-001
tango gateway serve --config examples/config/gateway/gateway.yaml --addr :8080
tango operator ingest --config examples/config/operator/operator.yaml '{"#type":"track"}'

# flag 名即配置键（viper 原生层级）：
tango report run --runtime.mongo.uri mongodb://host/db --remoteConfig.enabled
# 环境变量同理（runtime.mongo.uri → TANGO_RUNTIME_MONGO_URI）：
TANGO_RUNTIME_MONGO_URI=mongodb://user:pass@host/db tango report run
```

required 字段速查：

| 角色 | required 字段 |
|------|------|
| report   | `runtime.mongo.uri`、`report.source.logPattern` |
| worker   | `runtime.mongo.uri`、`tasks.instanceID` |
| gateway  | `runtime.mongo.uri` |
| operator | `runtime.mongo.uri` |

## 兼容配置（旧 daemon / client schema）

旧命令保留以便平滑迁移，使用旧的文件 schema。每个 daemon 模式提供 yaml 与 json 各两份：
**max**（全量，逐字段标注 required/optional 与默认值）与 **min**（最小，仅 required 字段）。

| 模式 | 子命令（兼容） | 推荐替代 | 目录 | 默认配置名 |
|------|--------|------|------|------|
| daemon · standalone | `tango daemon standalone` | `tango report run` | [standalone/](standalone/) | `standalone.{yaml,yml,json}` |
| daemon · agent | `tango daemon agent` | `tango report run` + `tango worker run`（或 `tango profile managed`） | [agent/](agent/) | `agent.{yaml,yml,json}` |
| client | `tango client ...` | `tango operator ...` / `tango gateway serve` | [client/](client/) | `client.{yaml,yml,json}` |

旧 daemon 配置分三部分：**generic**（logging + mongo）、**report**（`source` / `pipeline` /
`filter`，其中 `filter.local` 本地规则、`filter.remote` 是 agent 模式的同步源）、**agent**
（任务设置）。这些文件可继续被 daemon / client / profile 兼容命令读取。

```bash
examples/config/standalone/start.sh
examples/config/agent/start.sh --agent.instanceID node-1
tango daemon agent --generic.mongo.uri mongodb://host/db --agent.instanceID node-1
```
