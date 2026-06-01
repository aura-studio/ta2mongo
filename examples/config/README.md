# tango 配置样例

tango 现为**单一二进制**，通过 `daemon` / `client` 子命令选择角色，仍用两份配置。**推荐样例：**

| 角色 | 目录 | 运行 |
|------|------|------|
| daemon — standalone（纯上报） | [daemon/](daemon/) `daemon.yaml` / `daemon.json` | `tango daemon standalone --config examples/config/daemon/daemon.yaml` |
| daemon — agent（上报+同步+任务） | [daemon/](daemon/) `daemon.yaml` / `daemon.json` | `tango daemon agent --config examples/config/daemon/daemon.yaml --instanceID node-1` |
| client（CLI / HTTP / 库） | [client/](client/) `client.yaml` / `client.json` | `tango client <subcommand> --config examples/config/client/client.yaml` |

daemon 配置分为三部分：**common**（logging + mongo，进程级共享）、**report**（上报管线，
含 `source` / `pipeline` / `filter` / `remoteConfig`）、**agent**（任务 agent 设置）。
**运行模式由子命令选择**（不是配置开关）：`standalone` 只用 common + report 做纯上报；
`agent` 在上报之上自动开启 `remoteConfig` 配置同步与 agent 任务派发，并需要 `agent.instanceID`。

```bash
# 环境变量可覆盖任意键（嵌套键：. → _，加 TANGO_ 前缀）：
TANGO_COMMON_MONGO_URI=mongodb://host/db tango daemon standalone --config examples/config/daemon/daemon.yaml
TANGO_AGENT_INSTANCEID=node-1            tango daemon agent      --config examples/config/daemon/daemon.json
```

> 注意：`common.mongo.uri`、`backfill.token`、`backfill.proxy` 等敏感值示例里留空或占位，
> 实际使用请用环境变量注入。`instanceID` 仅在 daemon 的 `agent` 段（`agent.instanceID`）配置。

各场景在两份样例里以注释区分：
- 实时采集（standalone）/ 集群 worker（agent）→ `daemon/`（由子命令选模式）。
- 字符串上报 / 文件上报（断点续传）/ 回填 / SQL / 任务发布 → `client/` 的对应配置段。

## daemon 两种模式（开箱即用）

[scenarios/](scenarios/) 给出两套完整的 `daemon.yaml` + `start.sh`：

| 场景 | 模式 | 子命令 | 目录 | 配置同步 | 任务派发 |
|------|------|--------|------|:---:|:---:|
| 1 | standalone | `tango daemon standalone` | [scenarios/01-standalone](scenarios/01-standalone/) | ❌ | ❌ |
| 2 | agent | `tango daemon agent` | [scenarios/02-agent](scenarios/02-agent/) | ✅ | ✅ |
