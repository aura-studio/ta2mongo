# ta2mongo 配置参数说明

> 配置文件：`ta2mongo.yaml`（默认路径，可通过 `--config` 指定）  
> 结构：两级 YAML（`mode / mongo / ta / tail / batch / retry / log`）

---

## mode
运行模式选择

- `mode` (string, optional)  
  默认：`"daemon"`  
  可选值：`"daemon"` / `"once"` / `"ingest"`  
  - `daemon`：持续追尾日志文件，增量导入，不退出  
  - `once`：daemon 的一次性版本，从文件开头读取所有匹配文件，处理完退出并输出统计摘要  
  - `ingest`：同步阻塞上传模式（从 CLI 参数或 stdin 读取）  
  
  > 注：也可通过子命令显式指定模式（`ta2mongo daemon` / `ta2mongo once` / `ta2mongo ingest`），子命令优先级高于配置文件。

---

## mongo
MongoDB 连接信息

- `mongo.uri` (string, required)  
  例：`mongodb://localhost:27017`

- `mongo.db` (string, required)  
  默认：`ta2mongo`

---

## ta
ThinkingData 文件匹配规则

- `ta.logPattern` (string[], required)  
  **数组**；每个元素是 **正则**（regex），用于匹配需要 tail 的文件路径。  
  匹配到的文件会被 tail（daemon-only 增量消费）。

---

## tail
文件重扫（rescan）策略

> `tail.rescan` 固定开启，因此仅保留重扫间隔参数。

- `tail.rescanSeconds` (int, required)  
  例如：`30`  
  周期（秒），用于按间隔重扫匹配文件并补充 tail。

---

## batch
写入批处理策略

- `batch.size` (int, optional)  
  默认：`1000`  
  当 user 或 event 任一批达到该条数时触发 flush

- `batch.workerCount` (int, optional)  
  默认：`2`  
  并发 worker 数

- `batch.flushIntervalMs` (int, optional)  
  默认：`1000`  
  距离上次 flush 的时间间隔（毫秒）

---

## retry
Mongo 写入重试策略

- `retry.maxElapsedTime` (duration string, optional)  
  默认：`10s`  
  使用指数退避重试（初始间隔 200ms，最大间隔 2s，直到该时间耗尽）

---

## log
日志输出等级

- `log.level` (string, optional)  
  默认：`info`  
  例：`debug / info / warn / error`

---

## ta2mongo.yaml 示例（当前仓库）
```yaml
# mode 决定运行模式：daemon / once / ingest
mode: "daemon"

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
