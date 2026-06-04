# tango 配置样例

tango 是**单一二进制**（用法见 [../../doc/usage.md](../../doc/usage.md)）。**运行角色由配置键
`role.mode` 选择**（daemon / gateway / cli），不再用子命令。所有角色共用同一套**按包路径映射**的统一 schema：
配置键路径 = 消费它的包路径（`internal/` 下），例如 `logging.level`、`dao.mongo.uri`、
`dao.store.maxElapsedTime`、`parser.filter.*`、`source.tailer.*`、`process.mode`、`process.pipeline.*`、
`role.gateway.*`。每个角色只取自己需要的段。最外层 `config` 包只负责加载与覆盖，不定义具体字段。

每个角色目录提供 yaml 与 json 各两份：**max**（全量，逐字段标注 required/optional 与默认值）
与 **min**（最小，仅 required 字段），外加 `start.sh`。

| 角色 | role.mode | 目录 | 主要配置段 |
|------|--------|------|------|
| daemon | `daemon`（默认） | [daemon/](daemon/) | logging · dao · parser · source · process |
| gateway    | `gateway`    | [gateway/](gateway/)       | logging · dao · parser · process · role.gateway |

> 留空 `--config` 时二进制读取同级目录的 `tango.{yaml,yml,json}`；本目录下的样例需用 `--config` 显式指定。

## 运行

```bash
# 用脚本（内部 go run . --config <role>.max.yaml，角色由配置里的 role.mode 决定）：
examples/config/daemon/start.sh
examples/config/gateway/start.sh --role.gateway.addr :8080

# 或手动指定配置（max 全量 / min 最小皆可，role.mode 决定运行哪个角色）：
tango --config examples/config/daemon/daemon.max.yaml
tango --config examples/config/gateway/gateway.max.yaml --role.gateway.addr :8080

# flag 名即配置键（viper 原生层级），角色也可用 flag 覆盖：
tango --role.mode daemon --dao.mongo.uri mongodb://host/db
# 环境变量同理用原始层级（role.mode → TANGO_ROLE_MODE，dao.mongo.uri → TANGO_DAO_MONGO_URI）：
TANGO_DAO_MONGO_URI=mongodb://user:pass@host/db examples/config/daemon/start.sh
```

## required 字段速查

| 角色 | required 字段 |
|------|------|
| daemon | `dao.mongo.uri`、`source.tailer.logPattern` |
| gateway    | `dao.mongo.uri` |
