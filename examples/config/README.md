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

---

## 旧版分组样例（runtime 字段参考，仍可被 `config.Load` 解析）

下列目录是重构前的单文件 `tango.yaml` 样例，保留作为各配置段字段的参考：
[daemon-tail](daemon-tail/)、[once-import](once-import/)、[backfill-event](backfill-event/)、
[backfill-user](backfill-user/)、[agent-worker](agent-worker/)、[remote-config](remote-config/)。
