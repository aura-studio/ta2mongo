# tango 配置样例

tango 是**单一二进制**，通过子命令选择角色与模式（详见 [../../doc/usage.md](../../doc/usage.md)
与字段参考 [../../doc/config.md](../../doc/config.md)）。每个模式各一套样例：**全量版**（含每个字段
的 required/optional 与默认值注释）+ **精简版**（只含 required 字段）。

| 模式 | 子命令 | 目录 | 默认配置名 |
|------|--------|------|------|
| daemon · standalone | `tango daemon standalone` | [standalone/](standalone/) | `standalone.{yaml,yml,json}` |
| daemon · agent | `tango daemon agent` | [agent/](agent/) | `agent.{yaml,yml,json}` |
| client | `tango client <subcmd>` | [client/](client/) | `client.{yaml,yml,json}` |

每个 daemon 目录含：

- `<mode>.yaml` —— **全量版**，逐字段标注 `[required]` / `[optional]`（默认值）。
- `<mode>.min.yaml` —— **精简版**，仅 required 字段，其余走默认。
- `<mode>.json` —— JSON 形式的全量版。
- `start.sh` —— 启动脚本（内部 `go run . daemon <mode>`）。

## 运行

```bash
# 用脚本：
examples/config/standalone/start.sh
examples/config/agent/start.sh --instanceID node-1

# 或手动指定配置：
tango daemon standalone --config examples/config/standalone/standalone.yaml
tango daemon agent      --config examples/config/agent/agent.yaml --instanceID node-1
tango client serve      --config examples/config/client/client.yaml

# 留空 --config 时，子命令自动读取二进制同级目录的 standalone/agent/client.{yaml,yml,json}。
# 敏感值用环境变量注入（daemon 连接串前缀是 TANGO_COMMON_MONGO_URI）：
TANGO_COMMON_MONGO_URI=mongodb://user:pass@host/db examples/config/agent/start.sh
```

## required 字段速查

| 模式 | required 字段 |
|------|------|
| standalone | `common.mongo.uri`、`report.source.logPattern` |
| agent | `common.mongo.uri`、`report.source.logPattern`、`agent.instanceID` |
| client | `mongo.uri`（回填/SQL 另需 `backfill.apiBaseURL`/`token`/`projectID`/`runID` 等） |

> daemon 两种模式都 tail 日志上报，故 `report.source.logPattern` 始终必填。standalone
> 不使用 `agent` 段与 `report.remoteConfig`（写了也忽略）。
