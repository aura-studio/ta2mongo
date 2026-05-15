# tango 命令行使用说明

## 基本语法

```
tango <subcommand> [flags]
```

## 全局 Flag

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--config` | string | `tango.yaml` | YAML 配置文件路径（文件不存在时静默跳过） |

## 配置 Flags（所有子命令共享）

所有配置参数均可通过命令行 flag 覆盖，flag 名与 YAML key 和环境变量后缀一致：

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--mongoURI` | string | *(必填)* | MongoDB 连接 URI |
| `--logPattern` | string[] | `[]` | 日志文件路径匹配正则（可多次指定） |
| `--rescanInterval` | duration | `30s` | 文件重扫间隔（如 `30s`、`1m`） |
| `--batchSize` | int | `1000` | 目标批量大小 |
| `--batchWorkers` | int | `2` | 并行写入 worker 数 |
| `--flushInterval` | duration | `1s` | 批量定时刷新间隔 |
| `--maxElapsedTime` | duration | `10s` | bulk write 最大重试总时间 |
| `--logLevel` | string | `info` | 日志级别：debug/info/warn/error |

---

## 子命令

### `tango daemon`

持续追尾日志文件，增量导入 MongoDB。进程常驻，直到收到 SIGINT/SIGTERM。

```bash
tango daemon --config tango.yaml

# 不使用配置文件，直接传参：
tango daemon --mongoURI mongodb://localhost:27017/tango \
             --logPattern '/var/log/ta.*.log' \
             --batchWorkers 4
```

- 需要 `logPattern`
- 从文件末尾开始消费（增量）
- 支持 log rotation（ReOpen + Follow）
- 周期性重扫新文件（`rescanInterval`）

---

### `tango once`

从匹配文件的**开头**读取全部内容，处理完毕后输出统计摘要并退出。

```bash
tango once --config tango.yaml

tango once --mongoURI mongodb://localhost:27017/tango \
           --logPattern '/var/log/ta.*.log'
```

- 需要 `logPattern`
- 从文件开头读取（全量）
- 不 follow、不 reopen、不重扫
- 退出时输出详细统计（重试次数、错误数、吞吐量等）
- 存在错误时以非零退出码退出

---

### `tango ingest [json-line ...]`

同步阻塞式上传。逐条处理 JSON 行，出错直接返回。适合 CLI 单次上传或管道输入。

```bash
# 位置参数传入：
tango ingest --mongoURI mongodb://localhost:27017/tango \
  '{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"u1","#account_id":"alice"}'

# 从 stdin 管道读取：
cat events.jsonl | tango ingest --config tango.yaml

# 混合：先处理参数，再处理 stdin：
echo '{"#type":"track",...}' | tango ingest --config tango.yaml '{"#type":"user_set",...}'
```

- 不需要 `logPattern`
- 每行独立处理：parse → identity → write → 返回结果
- 失败行记录 Error 日志并继续后续行
- 最终以非零退出码报告失败总数
- stdin 单行最大 10 MB，空行跳过

---

## 配置优先级

```
内置默认值  <  tango.yaml  <  TANGO_* 环境变量  <  --flag 命令行参数
```

示例（命令行覆盖配置文件中的 batchWorkers）：
```bash
# tango.yaml 中设置 batchWorkers: 2，命令行覆盖为 8
tango daemon --config tango.yaml --batchWorkers 8
```

---

## 模式选择指南

| 场景 | 推荐模式 |
|------|----------|
| 后台服务持续导入日志 | `daemon` |
| 批量迁移 / 历史数据回填 / CI 流水线 | `once` |
| CLI 单次上传少量数据 | `ingest` |
| 应用内嵌集成（Go 库调用） | `client` 包 |

---

## 退出码

| 退出码 | 含义 |
|--------|------|
| `0` | 成功 |
| `1` | 配置错误 / 连接失败 / 处理异常 |

- `daemon`：正常收到信号退出为 0
- `once`：有任何 parse/identity/write 错误时退出码 1
- `ingest`：有任何行失败时退出码 1

---

## 信号处理

所有模式均监听 `SIGINT`（Ctrl+C）和 `SIGTERM`：

- `daemon`：停止 tail，flush 剩余 batch，断开 MongoDB，退出
- `once`：停止读文件，flush 剩余 batch，输出已完成部分的统计，退出
- `ingest`：中断当前处理，断开 MongoDB，退出
