# tango 配置说明

## 配置来源与优先级

tango 从四个来源读取配置，优先级从低到高：

```
内置默认值  <  YAML 文件  <  环境变量（TANGO_*）  <  命令行参数（--flag）
```

高优先级来源只覆盖**明确设置**的字段，未设置的字段继续沿用低优先级来源的值。  
例如：YAML 文件设置了 `batchWorkers: 4`，而命令行只传入了 `--mongoURI`，则最终 `batchWorkers` 取 YAML 的值 `4`。

---

## 1. 内置默认值

程序硬编码的兜底值，无需任何配置文件或环境变量即可启动（`mongoURI` 除外，为必填项）。

| 参数 | 内置默认值 |
|------|-----------|
| `mode` | `daemon` |
| `mongoURI` | *(无默认，必填)* |
| `logPattern` | `[]`（空列表） |
| `rescanInterval` | `30s` |
| `batchSize` | `1000` |
| `batchWorkers` | `2` |
| `flushInterval` | `1s` |
| `maxElapsedTime` | `10s` |
| `logLevel` | `info` |

---

## 2. YAML 配置文件

### 文件路径

默认读取当前目录下的 `tango.yaml`。通过 `--config` flag 可指定其他路径：

```bash
tango daemon --config /etc/tango/production.yaml
```

**文件不存在时静默跳过**，不报错，继续使用默认值 + 环境变量 + 命令行参数。  
文件存在但解析失败（语法错误）时报错退出。

### 格式规则

- 格式：YAML
- 结构：**扁平单层**，不支持嵌套
- Key 命名：**驼峰式**（camelCase），与 CLI flag 和环境变量后缀完全一致
- 只需填写需要覆盖默认值的字段，其余字段可省略

### 完整示例

```yaml
mode: "daemon"

mongoURI: "mongodb://localhost:27017/tango"

logPattern:
  - "/var/log/ta\\.production-.*\\.log"
  - "/data/logs/ta_.*"

rescanInterval: "30s"

batchSize: 1000
batchWorkers: 2
flushInterval: "1s"

maxElapsedTime: "10s"

logLevel: "info"
```

### 时间类型写法

`rescanInterval`、`flushInterval`、`maxElapsedTime` 均为 duration 类型，写法示例：

| 写法 | 含义 |
|------|------|
| `"500ms"` | 500 毫秒 |
| `"1s"` | 1 秒 |
| `"30s"` | 30 秒 |
| `"1m"` | 1 分钟 |
| `"1h"` | 1 小时 |

### logPattern 写法

值为正则表达式数组，每条正则与**文件路径全串**匹配（非前缀匹配）：

```yaml
logPattern:
  - "/var/log/ta\\.production-.*\\.log"   # 转义点号，精确匹配扩展名
  - "/data/logs/ta_.*"                     # 前缀通配
```

> YAML 中反斜杠需双写（`\\.` 表示正则中的 `\.`）。

---

## 3. 环境变量（`TANGO_*`）

### 命名规则

环境变量名 = `TANGO_` 前缀 + 参数名**全大写**：

| 参数（YAML key） | 环境变量 |
|-----------------|---------|
| `mode` | `TANGO_MODE` |
| `mongoURI` | `TANGO_MONGOURI` |
| `logPattern` | `TANGO_LOGPATTERN` |
| `rescanInterval` | `TANGO_RESCANINTERVAL` |
| `batchSize` | `TANGO_BATCHSIZE` |
| `batchWorkers` | `TANGO_BATCHWORKERS` |
| `flushInterval` | `TANGO_FLUSHINTERVAL` |
| `maxElapsedTime` | `TANGO_MAXELAPSEDTIME` |
| `logLevel` | `TANGO_LOGLEVEL` |

### 各类型写法

**字符串**：直接赋值

```bash
TANGO_MONGOURI=mongodb://localhost:27017/tango
TANGO_LOGLEVEL=debug
TANGO_MODE=once
```

**整数**：

```bash
TANGO_BATCHSIZE=2000
TANGO_BATCHWORKERS=4
```

**时间（duration）**：与 YAML 相同格式

```bash
TANGO_RESCANINTERVAL=1m
TANGO_FLUSHINTERVAL=500ms
TANGO_MAXELAPSEDTIME=30s
```

**字符串数组**（`logPattern`）：逗号分隔多个正则

```bash
TANGO_LOGPATTERN="/var/log/ta\\..*\\.log,/data/logs/ta_.*"
```

### 典型使用场景

**容器环境**（Docker / Kubernetes）：将敏感参数（如 `mongoURI`）通过 Secret 注入，其余参数通过 ConfigMap 或 Deployment env 设置：

```yaml
# Kubernetes Deployment 片段
env:
  - name: TANGO_MONGOURI
    valueFrom:
      secretKeyRef:
        name: tango-secret
        key: mongoURI
  - name: TANGO_LOGLEVEL
    value: "info"
  - name: TANGO_BATCHWORKERS
    value: "4"
```

**Shell 脚本**（临时覆盖）：

```bash
TANGO_LOGLEVEL=debug TANGO_BATCHWORKERS=8 tango daemon --config tango.yaml
```

---

## 4. 命令行参数（`--flag`）

### 命名规则

Flag 名与 YAML key 完全一致（驼峰式），通过 `--` 前缀传入：

| 参数 | CLI Flag |
|------|---------|
| `mongoURI` | `--mongoURI` |
| `logPattern` | `--logPattern` |
| `rescanInterval` | `--rescanInterval` |
| `batchSize` | `--batchSize` |
| `batchWorkers` | `--batchWorkers` |
| `flushInterval` | `--flushInterval` |
| `maxElapsedTime` | `--maxElapsedTime` |
| `logLevel` | `--logLevel` |

全局 flag（所有子命令均可使用）：

| Flag | 说明 |
|------|------|
| `--config` | YAML 配置文件路径，默认 `tango.yaml` |

### 各类型写法

**字符串**：

```bash
tango daemon --mongoURI mongodb://localhost:27017/tango --logLevel debug
```

**整数**：

```bash
tango daemon --batchWorkers 8 --batchSize 2000
```

**时间（duration）**：

```bash
tango daemon --rescanInterval 1m --flushInterval 500ms
```

**字符串数组**（`logPattern`）：可**多次传入**同一 flag：

```bash
tango daemon \
  --logPattern '/var/log/ta\.production-.*\.log' \
  --logPattern '/data/logs/ta_.*'
```

### 覆盖行为

命令行参数只有在**明确传入**时才覆盖。若某个 flag 未传入，则该字段取环境变量 / YAML / 默认值中最高优先级的值。

```bash
# tango.yaml 中 batchWorkers: 2，此命令只覆盖 batchWorkers，其余字段仍来自文件
tango daemon --config tango.yaml --batchWorkers 8
```

---

## 参数参考

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `mode` | string | `daemon` | 运行模式：`daemon` / `once` / `ingest` |
| `mongoURI` | string | *(必填)* | MongoDB 连接 URI，数据库名从路径提取 |
| `logPattern` | string[] | `[]` | 日志文件路径匹配正则，daemon/once 模式必填 |
| `rescanInterval` | duration | `30s` | 文件重扫间隔，daemon 模式使用 |
| `batchSize` | int | `1000` | 目标批量大小；自适应范围 batchSize/4 ~ batchSize*2 |
| `batchWorkers` | int | `2` | 并行写入 worker 数 |
| `flushInterval` | duration | `1s` | 批量定时刷新间隔 |
| `maxElapsedTime` | duration | `10s` | 单次 bulk write 指数退避最大重试总时间 |
| `logLevel` | string | `info` | 日志级别：`debug` / `info` / `warn` / `error` |

---

## 参数详解

### mode

运行模式。通常通过子命令直接指定（子命令优先级高于此字段）：

- `daemon`：持续追尾日志文件，增量导入，进程不退出
- `once`：从文件开头全量读取，处理完毕后退出并输出统计摘要
- `ingest`：同步阻塞，从 CLI 参数或 stdin 逐条读取 JSON 行

### mongoURI

MongoDB 连接 URI，**必填**。数据库名从 URI 路径中提取；路径为空时默认使用 `tango`。

```
mongodb://localhost:27017/tango
mongodb://user:pass@host1:27017,host2:27017/tango?replicaSet=rs0
mongodb+srv://user:pass@cluster.example.com/tango?tls=true
```

TLS（如 AWS DocumentDB）示例：

```
mongodb://user:pass@docdb.example.com:27017/tango?tls=true&tlsCAFile=global-bundle.pem
```

### logPattern

正则数组，与**完整文件路径**匹配。daemon 和 once 模式必填，ingest 模式忽略。

```yaml
logPattern:
  - "/var/log/ta\\.production-.*\\.log"
  - "/data/logs/ta_.*"
```

### batchSize

目标批量大小。Worker 内部根据 channel 积压量动态调整实际 flush 阈值：

- 积压为 0（空闲）：使用 `batchSize * 2`，加大批次减少 IO 次数
- 积压达上限（繁忙）：使用 `batchSize / 4`，快速清空积压
- 中间状态线性插值

### batchWorkers

并行写入 MongoDB 的 worker 数。每个 worker 独立维护 batch buffer，通过 Affinity 路由（FNV-1a hash）保证同一用户的操作始终路由到同一 worker，防止乱序覆写。

### maxElapsedTime

单次 MongoDB bulk write 失败后的指数退避重试策略：

- 初始间隔：200ms
- 最大间隔：2s
- 总时间上限：`maxElapsedTime`（默认 10s）
- 超过总时间后记录 error，daemon/once 模式不因此退出

---

## 优先级示例

### 场景 1：仅用 YAML 文件

```bash
tango daemon --config tango.yaml
```

所有参数从 `tango.yaml` 读取，未在文件中设置的字段取内置默认值。

### 场景 2：YAML + 环境变量覆盖敏感参数

```bash
# tango.yaml 中不写 mongoURI，通过环境变量注入
export TANGO_MONGOURI=mongodb://prod-host:27017/tango
tango daemon --config tango.yaml
```

### 场景 3：YAML + 命令行临时调试

```bash
# 临时提升日志级别和 worker 数，不修改配置文件
tango daemon --config tango.yaml --logLevel debug --batchWorkers 8
```

### 场景 4：纯命令行，不使用配置文件

```bash
tango daemon \
  --mongoURI mongodb://localhost:27017/tango \
  --logPattern '/var/log/ta\..*\.log' \
  --batchWorkers 4 \
  --logLevel info
```

### 场景 5：环境变量 + 命令行（命令行优先）

```bash
export TANGO_BATCHWORKERS=4
# 命令行传入 8，最终生效值为 8
tango daemon --mongoURI mongodb://localhost:27017/tango --batchWorkers 8
```
