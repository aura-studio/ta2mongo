# tango 配置样例

tango 是**单一二进制、纯上报 daemon**（用法见 [../../doc/usage.md](../../doc/usage.md)，字段参考见
[../../doc/config.md](../../doc/config.md)）。两种模式由子命令选择,每种各一套样例:**max**(全量、
逐字段标注 required/optional 与默认值)与 **min**(最小、仅 required),yaml 与 json 各一份。

| 模式 | 子命令 | 目录 | 默认配置名(二进制同级) |
|------|--------|------|------|
| standalone | `tango daemon standalone` | [standalone/](standalone/) | `standalone.{yaml,yml,json}` |
| cluster | `tango daemon cluster` | [cluster/](cluster/) | `cluster.{yaml,yml,json}` |

daemon 配置分两部分：**generic**（logging + mongo，进程级共享）、**report**（上报管线：
`source` / `pipeline` / `filter`，其中 `filter.local` 是本地规则、`filter.remote` 是 cluster
模式的 MongoDB 配置同步源）。

每个目录含：`<mode>.max.yaml`、`<mode>.min.yaml`、`<mode>.max.json`、`<mode>.min.json`、`start.sh`。

## 运行

```bash
# 用脚本（内部 go run . daemon <mode>）：
examples/config/standalone/start.sh
examples/config/cluster/start.sh

# 或手动指定配置（max 全量 / min 最小皆可）：
tango daemon standalone --config examples/config/standalone/standalone.max.yaml
tango daemon cluster    --config examples/config/cluster/cluster.min.json

# 留空 --config 时，子命令自动读取二进制同级目录的 standalone/cluster.{yaml,yml,json}。
# 命令行用「完整层级名」flag 覆盖（viper 原生层级）：
tango daemon cluster --generic.mongo.uri mongodb://host/db
# 环境变量同理用原始层级：
TANGO_GENERIC_MONGO_URI=mongodb://user:pass@host/db examples/config/cluster/start.sh
```

## required 字段速查

两种模式相同:`generic.mongo.uri`、`report.source.logPattern`。

> 两种模式都 tail 日志上报,故 `report.source.logPattern` 始终必填。cluster 比 standalone 多了
> 从 `report.filter.remote` 指定的控制面文档同步并热重载上报 filter（standalone 忽略 filter.remote）。
