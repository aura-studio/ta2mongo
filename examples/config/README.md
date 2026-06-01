# tango 配置样例

tango 是**单一二进制**（用法见 [../../doc/usage.md](../../doc/usage.md)，字段参考见
[../../doc/config.md](../../doc/config.md)）。**v1.0.0 只暴露 daemon 入口**（client/HTTP
入口暂未接线）。每个 daemon 模式提供 yaml 与 json 各两份：**max**（全量，逐字段标注
required/optional 与默认值）与 **min**（最小，仅 required 字段）。

| 模式 | 子命令 | 目录 | 默认配置名（二进制同级） |
|------|--------|------|------|
| daemon · standalone | `tango daemon standalone` | [standalone/](standalone/) | `standalone.{yaml,yml,json}` |
| daemon · agent | `tango daemon agent` | [agent/](agent/) | `agent.{yaml,yml,json}` |

daemon 配置分三部分：**generic**（logging + mongo，进程级共享）、**report**（上报管线：
`source` / `pipeline` / `filter`，其中 `filter.local` 是本地规则、`filter.remote` 是
agent 模式的 MongoDB 配置同步源）、**agent**（任务 agent 设置）。

每个目录含：`<mode>.max.yaml`、`<mode>.min.yaml`、`<mode>.max.json`、`<mode>.min.json`、`start.sh`。

## 运行

```bash
# 用脚本（内部 go run . daemon <mode>）：
examples/config/standalone/start.sh
examples/config/agent/start.sh --agent.instanceID node-1

# 或手动指定配置（max 全量 / min 最小皆可）：
tango daemon standalone --config examples/config/standalone/standalone.max.yaml
tango daemon agent      --config examples/config/agent/agent.min.json --agent.instanceID node-1

# 留空 --config 时，子命令自动读取二进制同级目录的 standalone/agent.{yaml,yml,json}。
# 命令行用「完整层级名」flag 覆盖任意键（viper 原生层级）：
tango daemon agent --generic.mongo.uri mongodb://host/db --agent.instanceID node-1
# 环境变量同理用原始层级（daemon 连接串是 TANGO_GENERIC_MONGO_URI）：
TANGO_GENERIC_MONGO_URI=mongodb://user:pass@host/db examples/config/agent/start.sh
```

## required 字段速查

| 模式 | required 字段 |
|------|------|
| standalone | `generic.mongo.uri`、`report.source.logPattern` |
| agent | `generic.mongo.uri`、`report.source.logPattern`、`agent.instanceID` |

> 两种模式都 tail 日志上报，故 `report.source.logPattern` 始终必填。standalone 不使用
> `agent` 段与 `report.filter.remote`（写了也忽略）。
