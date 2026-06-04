# tango 配置样例

tango 是**单一二进制**（用法见 [../../doc/usage.md](../../doc/usage.md)）。命令按**运行角色**
划分：daemon / gateway / cli 等。所有角色共用同一套**按包路径映射**的统一 schema：
配置键路径 = 消费它的包路径（`internal/` 下），例如 `logging.level`、`dao.mongo.uri`、
`dao.store.maxElapsedTime`、`parser.filter.*`、`source.tailer.*`、`process.mode`、`process.pipeline.*`、
`role.gateway.*`。每个角色只取自己需要的段。最外层 `config` 包只负责加载与覆盖，不定义具体字段。

每个角色目录提供 yaml 与 json 各两份：**max**（全量，逐字段标注 required/optional 与默认值）
与 **min**（最小，仅 required 字段），外加 `start.sh`。

| 角色 | 命令 | 目录 | 默认配置名（二进制同级） | 主要配置段 |
|------|--------|------|------|------|
| daemon | `tango daemon` | [daemon/](daemon/) | `daemon.{yaml,yml,json}` | logging · dao · parser · source · process |
| gateway    | `tango gateway`    | [gateway/](gateway/)       | `gateway.{yaml,yml,json}`    | logging · dao · role.gateway |

## 运行

```bash
# 用脚本（内部 go run . <role> ...，默认读取同目录 <role>.max.yaml）：
examples/config/daemon/start.sh
examples/config/gateway/start.sh --role.gateway.addr :8080

# 或手动指定配置（max 全量 / min 最小皆可）：
tango daemon --config examples/config/daemon/daemon.max.yaml
tango gateway    --config examples/config/gateway/gateway.max.yaml --role.gateway.addr :8080

# 留空 --config 时，角色命令自动读取二进制同级目录的 <role>.{yaml,yml,json}。
# flag 名即配置键（viper 原生层级）：
tango daemon --dao.mongo.uri mongodb://host/db
# 环境变量同理用原始层级（dao.mongo.uri → TANGO_DAO_MONGO_URI）：
TANGO_DAO_MONGO_URI=mongodb://user:pass@host/db examples/config/daemon/start.sh
```

## required 字段速查

| 角色 | required 字段 |
|------|------|
| daemon | `dao.mongo.uri`、`source.tailer.logPattern` |
| gateway    | `dao.mongo.uri` |
