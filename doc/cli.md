# ta2mongo 命令行使用说明

## 基本语法

```
ta2mongo [subcommand] [flags]
```

## 全局 Flags

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--config` | string | `ta2mongo.yaml` | YAML 配置文件路径 |

所有子命令共享该 flag。

---

## 子命令

### `ta2mongo daemon`

持续追尾日志文件，增量导入 MongoDB。进程常驻，直到收到 SIGINT/SIGTERM。

```bash
ta2mongo daemon --config ta2mongo.yaml
```

- 需要配置 `ta.logPattern`
- 从文件末尾开始消费（增量）
- 支持 log rotation（ReOpen + Follow）
- 周期性重扫新文件

---

### `ta2mongo once`

Daemon 的一次性版本。从匹配文件的**开头**读取全部内容，保留完整的 daemon 处理流程（affinity 路由、多 worker、batch flush、retry），处理完毕后输出统计摘要并退出。

```bash
ta2mongo once --config ta2mongo.yaml
```

- 需要配置 `ta.logPattern`
- 从文件开头读取（全量）
- 不 follow、不 reopen、不重扫
- 退出时输出详细统计（重试次数、错误数、吞吐量等）
- 存在错误时以非零退出码退出

---

### `ta2mongo ingest [json-line ...]`

同步阻塞式上传。逐条处理 JSON 行，出错直接返回。适合 CLI 单次上传或管道输入。

```bash
# 位置参数传入：
ta2mongo ingest --config ta2mongo.yaml \
  '{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"u1","#account_id":"alice"}'

# 从 stdin 管道读取：
cat events.jsonl | ta2mongo ingest --config ta2mongo.yaml

# 混合：先处理参数，再处理 stdin：
echo '{"#type":"track",...}' | ta2mongo ingest --config ta2mongo.yaml '{"#type":"user_set",...}'
```

- 不需要 `ta.logPattern`
- 每行独立处理：parse → identity → write → 返回结果
- 失败行记录 Error 日志并继续后续行
- 最终以非零退出码报告失败总数
- stdin 单行最大 10 MB，空行跳过

---

## 无子命令时的行为

当不指定子命令时，根据 YAML 配置文件中的 `mode` 字段决定运行模式：

```bash
# 等价于 ta2mongo daemon（mode 默认为 "daemon"）
ta2mongo --config ta2mongo.yaml

# 若 YAML 中设置 mode: "once"
ta2mongo --config ta2mongo.yaml   # 等价于 ta2mongo once
```

**优先级**：子命令 > YAML `mode` 字段。显式指定子命令时忽略配置文件中的 mode。

---

## 模式选择指南

| 场景 | 推荐模式 |
|------|----------|
| 后台服务持续导入日志 | `daemon` |
| 批量迁移 / 历史数据回填 / CI 流水线 | `once` |
| CLI 单次上传少量数据 | `ingest` |
| 应用内嵌集成（Go 库调用） | API 模式（代码引用 `client` 包） |

---

## 退出码

| 退出码 | 含义 |
|--------|------|
| 0 | 成功 |
| 1 | 配置错误 / 连接失败 / 处理异常 |

- `daemon`：正常收到信号退出为 0
- `once`：有任何 parse/identity/write 错误时退出码 1
- `ingest`：有任何行失败时退出码 1

---

## 信号处理

所有模式均监听 `SIGINT`（Ctrl+C）和 `SIGTERM`：

- `daemon`：停止 tail，flush 剩余 batch，断开 MongoDB，退出
- `once`：停止读文件，flush 剩余 batch，输出已完成部分的统计，退出
- `ingest`：中断当前处理，断开 MongoDB，退出

---

## 配置文件示例

```yaml
mode: "daemon"          # daemon / once / ingest

mongo:
  uri: "mongodb://localhost:27017"
  db: "ta2mongo"

ta:
  logPattern:
    - "/mnt/shared-data-log/ta\\.production-.*"

tail:
  rescanSeconds: 30

batch:
  size: 1000
  workerCount: 2
  flushIntervalMs: 1000

retry:
  maxElapsedTime: "10s"

log:
  level: "info"
```
