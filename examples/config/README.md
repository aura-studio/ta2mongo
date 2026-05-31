# tango 配置样例

重构后 tango 拆为两个二进制、两份配置。**推荐样例：**

| 角色 | 目录 | 运行 |
|------|------|------|
| daemon（上报 + 可选 agent） | [daemon/](daemon/) `daemon.yaml` / `daemon.json` | `tangod --config examples/config/daemon/daemon.yaml` |
| client（CLI / HTTP / 库） | [client/](client/) `client.yaml` / `client.json` | `tango <subcommand> --config examples/config/client/client.yaml` |

```bash
# 环境变量可覆盖任意键：
TANGO_MONGO_URI=mongodb://host/db tangod --config examples/config/daemon/daemon.yaml
TANGO_AGENT_INSTANCEID=node-1     tangod --config examples/config/daemon/daemon.json
```

> 注意：`mongo.uri`、`backfill.token`、`backfill.proxy` 等敏感值示例里留空或占位，
> 实际使用请用环境变量注入。`instanceID` 仅在 daemon 的 `agent` 段（`agent.instanceID`）配置。

各场景在两份样例里以注释区分：
- 实时采集 / agent worker → `daemon/`（`agent.enabled` 控制是否兼任 worker）。
- 字符串上报 / 文件上报（断点续传）/ 回填 / SQL / 任务发布 → `client/` 的对应配置段。
