# tango 配置样例

tango 是**单一二进制**（用法见 [../../doc/usage.md](../../doc/usage.md)，字段参考见
[../../doc/config.md](../../doc/config.md)）。命令按**运行角色**划分：daemon /
gateway 两种模式。两者共用同一套统一 RoleConfig schema：顶层分为 `runtime`
（logging + mongo，进程级共享）、`report`、`gateway`、`upload` 等段，每个角色只取
自己需要的段。

每个角色目录提供 yaml 与 json 各两份：**max**（全量，逐字段标注 required/optional 与默认值）
与 **min**（最小，仅 required 字段），外加 `start.sh`。

| 角色 | 命令 | 目录 | 默认配置名（二进制同级） | 主要配置段 |
|------|--------|------|------|------|
| daemon | `tango daemon` | [daemon/](daemon/) | `daemon.{yaml,yml,json}` | runtime · report |
| gateway    | `tango gateway`    | [gateway/](gateway/)       | `gateway.{yaml,yml,json}`    | runtime · gateway · upload |

## 运行

```bash
# 用脚本（内部 go run . <role> ...，默认读取同目录 <role>.max.yaml）：
examples/config/daemon/start.sh
examples/config/gateway/start.sh --addr :8080

# 或手动指定配置（max 全量 / min 最小皆可）：
tango daemon --config examples/config/daemon/daemon.max.yaml
tango gateway    --config examples/config/gateway/gateway.max.yaml --addr :8080

# 留空 --config 时，角色命令自动读取二进制同级目录的 <role>.{yaml,yml,json}。
# flag 名即配置键（viper 原生层级）：
tango daemon --runtime.mongo.uri mongodb://host/db
# 环境变量同理用原始层级（runtime.mongo.uri → TANGO_RUNTIME_MONGO_URI）：
TANGO_RUNTIME_MONGO_URI=mongodb://user:pass@host/db examples/config/daemon/start.sh
```

## required 字段速查

| 角色 | required 字段 |
|------|------|
| daemon | `runtime.mongo.uri`、`report.source.logPattern` |
| gateway    | `runtime.mongo.uri` |
