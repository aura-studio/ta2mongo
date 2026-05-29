# tango 配置样例

每个子目录对应一种典型部署场景，附一份可直接改用的 `tango.yaml`。
所有配置采用分组结构（`mongo` / `source` / `pipeline` / `filter` / `backfill`
/ `remoteConfig` / `agent`），只有 `mode` 与 `instanceID` 在顶层。

| 场景 | 目录 | 说明 |
|------|------|------|
| 实时日志采集 | [daemon-tail](daemon-tail/) | tail 日志文件持续导入；含正/反向过滤 |
| 一次性导入 | [once-import](once-import/) | 把现有日志全量跑一遍后退出 |
| 历史回填（事件表） | [backfill-event](backfill-event/) | 按 `$part_date` 分天回填 `v_event_*`，经 socks5 代理 |
| 历史回填（用户表） | [backfill-user](backfill-user/) | 无分区 user 表全量同步 |
| 任务 worker | [agent-worker](agent-worker/) | 长驻进程认领并执行队列任务 |
| 远程配置控制面 | [remote-config](remote-config/) | 数据中心发布过滤规则，daemon 热更 |

运行：

```bash
tango <mode> --config examples/config/<scenario>/tango.yaml
# 或用环境变量覆盖连接串：
TANGO_MONGO_URI=mongodb://host/db tango daemon --config .../tango.yaml
```

> 注意：`mongo.uri`、`backfill.token`、`backfill.proxy` 等敏感值示例里留空或占位，
> 实际使用请用环境变量注入，例如 `TANGO_MONGO_URI`、`TANGO_INSTANCE_ID`。
