# tango 命令行使用说明

tango 是**单一二进制**（纯上报 daemon），由子命令选择运行模式：

```
tango daemon standalone [flags]    # 纯上报、本地 filter
tango daemon cluster    [flags]    # 上报 + 从 MongoDB 同步并热重载 filter
```

## 通用

- `--config <path>`：配置文件路径，支持 `.yaml` / `.yml` / `.json`（按扩展名识别；文件不存在则
  静默跳过，回退到默认值 + 环境变量 + flag）。**留空时各子命令在二进制同级目录查找各自默认文件**
  （按 yaml→yml→json 取首个存在者），互不读取对方的文件：
  - `tango daemon standalone` → `standalone.{yaml,yml,json}`
  - `tango daemon cluster` → `cluster.{yaml,yml,json}`
- **命令行用「完整层级名」flag 覆盖配置**（viper 原生层级，flag 名即配置键）：
  `--generic.mongo.uri`、`--generic.logging.level`。`--config` 是文件路径、非配置键。
- 所有键均可用 `TANGO_*` 环境变量覆盖（viper 原始层级：嵌套键 `.` → `_`、整体转大写），
  如 `generic.mongo.uri` → `TANGO_GENERIC_MONGO_URI`。

---

## `tango daemon standalone`

```bash
tango daemon standalone                                  # 默认读同级 standalone.{yaml,yml,json}
tango daemon standalone --config /etc/tango/standalone.yaml
tango daemon standalone --generic.mongo.uri mongodb://host/db
```

追尾 `report.source.logPattern` 匹配的日志，应用 `report.filter.local`，写入 MongoDB。
不拉远端配置，filter 完全由本地决定。`report.source.logPattern` 必填。

---

## `tango daemon cluster`

```bash
tango daemon cluster                                     # 默认读同级 cluster.{yaml,yml,json}
tango daemon cluster --config /etc/tango/cluster.yaml
```

在 standalone 上报的基础上，启动时从 `report.filter.remote` 指定的 MongoDB 文档拉取一次，
之后每 `syncInterval`（默认 1h）再拉取，命中变更即**热重载**上报 filter（无需重启）。
连接类字段不可被远端覆盖。`report.source.logPattern` 同样必填。

---

## 配置

两种模式共用 `generic` + `report` 两段配置，仅 `report.filter.remote` 在 cluster 模式生效。
字段说明见 [config.md](config.md)，完整样例见 [examples/config](../examples/config)。
