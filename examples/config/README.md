# tango 配置样例

tango 是**单一二进制**（用法见 [../../doc/usage.md](../../doc/usage.md)，字段参考见
[../../doc/config.md](../../doc/config.md)）。命令按**运行角色**划分：report / worker /
gateway / operator。所有角色共用同一套统一 RoleConfig schema：顶层分为 `runtime`
（logging + mongo，进程级共享）、`report`、`remoteConfig`、`tasks`、`gateway`、`upload`、
`backfill` / `backfillFilter` / `sql` 等段，每个角色只取自己需要的段。

每个角色目录提供 yaml 与 json 各两份：**max**（全量，逐字段标注 required/optional 与默认值）
与 **min**（最小，仅 required 字段），外加 `start.sh`。

| 角色 | 子命令 | 目录 | 默认配置名（二进制同级） | 主要配置段 |
|------|--------|------|------|------|
| report  | `tango report run`    | [report/](report/)     | `report.{yaml,yml,json}`   | runtime · report · remoteConfig |
| worker  | `tango worker run`    | [worker/](worker/)     | `worker.{yaml,yml,json}`   | runtime · tasks · remoteConfig |
| gateway | `tango gateway serve` | [gateway/](gateway/)   | `gateway.{yaml,yml,json}`  | runtime · gateway · upload · tasks |
| operator| `tango operator ...`  | [operator/](operator/) | `operator.{yaml,yml,json}` | runtime · upload · backfill · tasks |

## 运行

```bash
# 用脚本（内部 go run . <role> ...，默认读取同目录 <role>.max.yaml）：
examples/config/report/start.sh
examples/config/worker/start.sh --instanceID worker-1
examples/config/gateway/start.sh --addr :8080
examples/config/operator/start.sh '{"#type":"track","#event_name":"login","#distinct_id":"u1"}'

# 或手动指定配置（max 全量 / min 最小皆可）：
tango report run    --config examples/config/report/report.max.yaml
tango worker run    --config examples/config/worker/worker.min.json --instanceID worker-1
tango gateway serve --config examples/config/gateway/gateway.max.yaml --addr :8080
tango operator sql  --config examples/config/operator/operator.max.yaml 'SELECT ...'

# 留空 --config 时，角色命令自动读取二进制同级目录的 <role>.{yaml,yml,json}。
# flag 名即配置键（viper 原生层级）：
tango report run --runtime.mongo.uri mongodb://host/db --remoteConfig.enabled
# 环境变量同理用原始层级（runtime.mongo.uri → TANGO_RUNTIME_MONGO_URI）：
TANGO_RUNTIME_MONGO_URI=mongodb://user:pass@host/db examples/config/report/start.sh
```

## required 字段速查

| 角色 | required 字段 |
|------|------|
| report   | `runtime.mongo.uri`、`report.source.logPattern` |
| worker   | `runtime.mongo.uri`、`tasks.instanceID`（可用 `--instanceID` 覆盖） |
| gateway  | `runtime.mongo.uri` |
| operator | `runtime.mongo.uri`（`operator backfill` 另需 `backfill.*`，见 operator.max.yaml） |
