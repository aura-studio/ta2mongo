# tango daemon 两种模式

`tango daemon` 有两种运行模式,由**子命令**选择(不是配置文件里的开关)。每个子目录
含一份 `daemon.yaml` 与一个 `start.sh`:

| 场景 | 模式 | 子命令 | 目录 | 配置同步 | 任务派发 |
|------|------|--------|------|:---:|:---:|
| 1 | standalone | `tango daemon standalone` | [01-standalone](01-standalone/) | ❌ | ❌ |
| 2 | agent | `tango daemon agent` | [02-agent](02-agent/) | ✅ | ✅ |

## 运行

```bash
# 直接用脚本（内部 go run ./cmd/tango daemon <mode>）：
examples/config/scenarios/01-standalone/start.sh
examples/config/scenarios/02-agent/start.sh --instanceID node-1

# 或手动指定配置：
tango daemon standalone --config examples/config/scenarios/01-standalone/daemon.yaml
tango daemon agent      --config examples/config/scenarios/02-agent/daemon.yaml --instanceID node-1

# 敏感值用环境变量注入，可覆盖任意键：
TANGO_COMMON_MONGO_URI=mongodb://user:pass@host/db \
  examples/config/scenarios/02-agent/start.sh
```

## 两种模式的区别

两种模式**都 tail 日志做上报**(report 段必填 `logPattern`),区别在于是否接受中心指挥:

- **standalone（场景 1）**：纯上报,完全本地自治。filter 写死在本地配置,不拉远端
  配置、不领任务。配置只需 `common` + `report`(`agent` 段与 `report.remoteConfig`
  即使写了也被忽略)。

- **agent（场景 2）**：在上报之上自动开启两件事——
  - **配置同步**：定期拉取 `_tango_config` 文档热重载上报 filter（连接类字段如
    `common.mongo.uri` 永不可被远端覆盖）;
  - **任务派发**：注册心跳、轮询 `_tango_tasks` 队列、原子领取并执行任务:
    - `backfill`：按 payload 的 table/projectID/range/filter 拼 SQL 拉历史数据入库;
    - `sql`：执行 payload 中显式给定的 SQL,结果行流式入库;
    - `report-sync`：热替换 live filter 并持久化（推送式配置变更）。

  agent 模式下 `agent.instanceID` 必填（数据库命名空间内唯一），否则启动校验失败。
