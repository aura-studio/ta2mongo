# tango 配置样例

tango 是**单一二进制**（用法见 [../../doc/usage.md](../../doc/usage.md)）。**运行角色由配置键
`role.mode` 选择**（daemon / gateway / cli），不再用子命令。所有角色共用同一套**按包路径映射**的统一 schema：
配置键路径 = 消费它的包路径（`internal/` 下），例如 `logging.level`、`dao.mongo.uri`、
`dao.store.maxElapsedTime`、`parser.filter.*`、`source.tailer.*`、`process.mode`、`process.pipeline.*`、
`role.gateway.*`。每个角色只取自己需要的段。最外层 `config` 包只负责加载与覆盖，不定义具体字段。

每个角色目录提供 yaml 与 json 各两份：**max**（全量，逐字段标注 required/optional 与默认值）
与 **min**（最小，仅 required 字段）。daemon / gateway 另含 `start.sh`。
cli 角色有三种功能（`role.cli.function`：`upload` / `ejson` / `sql`），各一套 max/min × yaml/json，
共 12 份（`cli.upload.*` / `cli.ejson.*` / `cli.sql.*`）；cli 以管道喂 stdin 运行，无 `start.sh`。
另有 `requests.sample.ejson`（EJSON 请求样例）与 `queries.sample.sql`（SQL 语句样例）。

| 角色 | role.mode | 目录 | 主要配置段 |
|------|--------|------|------|
| daemon | `daemon`（默认） | [daemon/](daemon/) | logging · dao · parser · source · process |
| gateway    | `gateway`    | [gateway/](gateway/)       | logging · dao · parser · process · role.gateway |
| cli (upload) | `cli`      | [cli/](cli/)               | logging · dao · parser · process · role.cli（`function=upload`，stdin 日志上报） |
| cli (ejson) | `cli`       | [cli/](cli/)               | logging · dao · role.cli（`function=ejson`，Mongo Data API） |
| cli (sql) | `cli`         | [cli/](cli/)               | logging · dao · role.cli（`function=sql`，SQL Data API） |

> 留空 `--config` 时二进制读取同级目录的 `tango.{yaml,yml,json}`；本目录下的样例需用 `--config` 显式指定。

## 运行

```bash
# 用脚本（内部 go run . --config <role>.max.yaml，角色由配置里的 role.mode 决定）：
examples/config/daemon/start.sh
examples/config/gateway/start.sh --role.gateway.addr :8080

# 或手动指定配置（max 全量 / min 最小皆可，role.mode 决定运行哪个角色）：
tango --config examples/config/daemon/daemon.max.yaml
tango --config examples/config/gateway/gateway.max.yaml --role.gateway.addr :8080

# cli 以管道喂 stdin（upload 读日志数组；ejson 读一个 EJSON Data API 请求；sql 读一条 SQL）：
cat events.ndjson | tango --config examples/config/cli/cli.upload.min.yaml
echo '{"action":"find","collection":"event","filter":{},"limit":5}' | tango --config examples/config/cli/cli.ejson.min.yaml
echo 'SELECT * FROM event LIMIT 5' | tango --config examples/config/cli/cli.sql.min.yaml

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
| cli (upload) | `dao.mongo.uri`（`role.cli.function` 默认即 upload） |
| cli (ejson) | `dao.mongo.uri`、`role.cli.function: ejson` |
| cli (sql) | `dao.mongo.uri`、`role.cli.function: sql` |
