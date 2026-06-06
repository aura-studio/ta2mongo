# TODO

本文件只保留尚未完成的任务。任务完成后先改成 `- [x] ~~任务内容~~`，方便本轮核对；下一次整理 TODO 时删除已完成项。

## MongoDB Driver v2 升级

- [x] ~~将依赖从 `go.mongodb.org/mongo-driver v1.x` 升级到 `go.mongodb.org/mongo-driver/v2`。~~
- [x] ~~全量替换 MongoDB driver import 路径，避免 v1/v2 BSON 类型混用。~~
- [x] ~~校准 v2 API 差异，包括 `mongo.Connect`、`options`、`WriteModel`、bulk write options、index options、error 类型判断。~~
- [x] ~~保留 DocumentDB 兼容连接参数要求，重点确认 `retryWrites=false`、TLS、server selection timeout、connect timeout。~~
- [x] ~~跑通现有单元测试和可跳过的 MongoDB 集成测试。~~
- [x] ~~使用 `TANGO_TEST_MONGO_URI` 指向真实 DocumentDB 跑 store/gateway/api 关键集成测试。~~

## Gateway Mongo Data API 方案

> 决策调整：按"最大化功能、忽略安全限制"实现，并对齐 upload 在 **cli / gateway / api 三端**落地
> （功能核心 `internal/dao/ejson` 完全一致，仅入口不同）。原方案中的白名单 / operator 黑名单 /
> stage 限制 / 各类上限**全部放弃**（下方标注）。

- [x] ~~确认 Mongo Data API 的边界：固定 6 个 action（无任意 `runCommand`），但 action 之内完全放开（无白名单/无上限）。~~
- [x] ~~采用 `Extended JSON v2 + Data API 风格 action` 作为 body 方案（MQL 不做子集限制，原样转发）。~~
- [x] ~~定义 action 列表：`findOne`、`find`、`insertOne`、`updateOne`、`deleteOne`、`aggregate`。~~
- [x] ~~定义请求外壳字段：`database`、`collection`、`filter`、`projection`、`sort`、`limit`、`skip`、`document`、`update`、`pipeline`、`upsert`。~~
- [x] ~~定义响应格式，统一使用 relaxed Extended JSON 返回 BSON 类型。~~
- [x] ~~明确 Content-Type 约定：优先支持 `application/ejson`，兼容 `application/json`。~~
- [x] ~~在 Go 层使用官方 driver 的 `bson.UnmarshalExtJSON` / `bson.MarshalExtJSON` 处理 EJSON。~~
- [x] ~~（放弃）设计 database / collection 白名单配置~~ —— 按决策完全放开，可访问任意库表。
- [x] ~~（放弃）设计 MQL operator 白名单~~ —— 不限制 operator，原样转发驱动。
- [x] ~~（放弃）设计 aggregation stage 白名单~~ —— 不限制 stage，原样转发驱动。
- [x] ~~（放弃）为 `limit`、body size、执行超时、返回文档数量设置默认上限~~ —— 不设任何上限。
- [x] ~~明确 gateway 与现有 `/upload` 的关系：保留 `/upload` 作为 TA 日志上报入口，新 Mongo Data API 使用独立 path `/ejson`。~~
- [x] ~~三端实现：gateway `POST /ejson`、cli `role.cli.function=ejson`（stdin→stdout）、库 `api.Engine.EJSON`，共享 `internal/dao/ejson` 核心（经 dao 根包中转）。~~

## Lambda / DocumentDB 部署方案

- [ ] 输出 Lambda 部署建议文档：API Gateway HTTP API -> Lambda/Tango gateway -> DocumentDB。
- [ ] 说明 Lambda execution environment 复用 Mongo client / connection pool 的约束。
- [ ] 说明 Lambda VPC、security group、private subnet、Secrets Manager / VPC endpoint / NAT 的部署要求。
- [ ] 说明 DocumentDB 连接串模板和 CA bundle 放置方式。
- [ ] 给出并发与连接池计算方法，避免 Lambda 并发放大 DocumentDB 连接数。

## 测试与文档

- [x] ~~为 EJSON decode/encode 增加单元测试，覆盖 `$oid`、`$date`、`$numberLong`、`$numberDecimal`。~~（`internal/dao/ejson/ejson_test.go`）
- [x] ~~（放弃）为 MQL/operator/stage 白名单增加拒绝测试~~ —— 已无白名单；改为校验 unknown action / 缺字段拒绝。
- [x] ~~为每个 action 增加 handler 测试。~~（`dao/ejson` 集成测试逐 action 往返 + gateway/cli 端到端）
- [x] ~~为 gateway Mongo Data API 增加 MongoDB 集成测试。~~（`tests/ejson_test.go`，httptest + cli，连真实 DocumentDB 通过）
- [x] ~~为 DocumentDB 兼容路径补充测试说明，使用 `TANGO_TEST_MONGO_URI` 手动验证。~~（测试均读 `TANGO_TEST_MONGO_URI`，无则跳过）
- [x] ~~更新 `doc/usage.md`，补充 Mongo Data API 请求示例。~~
- [x] ~~更新 `doc/config.md`，补充 `role.cli.function` 配置项（白名单/上限已放弃，无对应配置）。~~
- [x] ~~更新 `doc/arch.md`，补充 gateway 中 `/upload` 与 Mongo Data API 的职责分离。~~
