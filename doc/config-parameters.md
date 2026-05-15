# tango 配置参数说明

配置来源优先级（从低到高）：内置默认值 < YAML 文件 < 环境变量（`TANGO_*`）< 命令行参数

所有参数均为**扁平单层结构**，YAML key、CLI flag、环境变量后缀三者名称一致。

---

## 参数一览

| 参数 | 类型 | 默认值 | 环境变量 | 说明 |
|------|------|--------|----------|------|
| `mode` | string | `daemon` | `TANGO_MODE` | 运行模式：`daemon` / `once` / `ingest` |
| `mongoURI` | string | *(必填)* | `TANGO_MONGOURI` | MongoDB 连接 URI，数据库名从路径提取 |
| `logPattern` | string[] | `[]` | `TANGO_LOGPATTERN` | 日志文件路径匹配正则（daemon/once 模式必填） |
| `rescanInterval` | duration | `30s` | `TANGO_RESCANINTERVAL` | 文件重扫间隔，daemon 模式使用（如 `"30s"`、`"1m"`） |
| `batchSize` | int | `1000` | `TANGO_BATCHSIZE` | 目标批量大小；min=batchSize/4，max=batchSize*2 自动推导 |
| `batchWorkers` | int | `2` | `TANGO_BATCHWORKERS` | 并行写入 worker 数 |
| `flushInterval` | duration | `1s` | `TANGO_FLUSHINTERVAL` | 批量定时刷新间隔（如 `500ms`、`2s`） |
| `maxElapsedTime` | duration | `10s` | `TANGO_MAXELAPSEDTIME` | 单次 bulk write 指数退避最大重试总时间 |
| `logLevel` | string | `info` | `TANGO_LOGLEVEL` | 日志级别：`debug` / `info` / `warn` / `error` |

---

## 参数说明

### mode
运行模式。也可通过子命令直接指定（子命令优先级高于此字段）：
- `daemon`：持续追尾日志文件，增量导入，不退出
- `once`：从文件开头读取所有匹配文件，处理完退出并输出统计摘要
- `ingest`：同步阻塞，从 CLI 参数或 stdin 读取单行 JSON

### mongoURI
MongoDB 连接 URI，**必填**。数据库名从 URI 路径中提取，URI 中无路径时默认使用 `tango`。

示例：
```
mongodb://localhost:27017/tango
mongodb+srv://user:pass@cluster.example.com/tango?tls=true
```

### logPattern
正则数组，匹配需要追尾的文件路径。daemon 和 once 模式必填，ingest 模式忽略。

```yaml
logPattern:
  - "/var/log/ta\\.production-.*\\.log"
  - "/data/logs/ta_.*"
```

### batchSize
目标批量大小。Worker 内部会根据当前积压量动态调整实际 flush 阈值：
- 积压为 0（空闲）时使用 `batchSize * 2`（加大批次，减少 IO 次数）
- 积压达到上限（繁忙）时使用 `batchSize / 4`（快速清空积压）
- 中间状态线性插值

### maxElapsedTime
单次 MongoDB bulk write 失败后的指数退避重试策略：初始间隔 200ms，最大间隔 2s，直到总耗时超过此值为止。

---

## tango.yaml 完整示例

```yaml
mode: "daemon"

mongoURI: "mongodb://localhost:27017/tango"

logPattern:
  - "/var/log/ta.*.log"

rescanInterval: "30s"

batchSize: 1000
batchWorkers: 2
flushInterval: "1s"

maxElapsedTime: "10s"

logLevel: "info"
```
