# TODO

本文件只保留尚未完成的任务。任务完成后先改成 `- [x] ~~任务内容~~`，方便本轮核对；下一次整理 TODO 时删除已完成项。

## MongoDB Driver v2 升级

- [ ] 将依赖从 `go.mongodb.org/mongo-driver v1.x` 升级到 `go.mongodb.org/mongo-driver/v2`。
- [ ] 全量替换 MongoDB driver import 路径，避免 v1/v2 BSON 类型混用。
- [ ] 校准 v2 API 差异，包括 `mongo.Connect`、`options`、`WriteModel`、bulk write options、index options、error 类型判断。
- [ ] 保留 DocumentDB 兼容连接参数要求，重点确认 `retryWrites=false`、TLS、server selection timeout、connect timeout。
- [ ] 跑通现有单元测试和可跳过的 MongoDB 集成测试。
- [ ] 使用 `TANGO_TEST_MONGO_URI` 指向真实 DocumentDB 跑 store/gateway/api 关键集成测试。

## Gateway Mongo Data API 方案

- [ ] 确认 gateway 新增 Mongo Data API 的边界：只做受控 CRUD/aggregate，不开放任意 `runCommand`。
- [ ] 采用 `Extended JSON v2 + MQL 子集 + Data API 风格 action` 作为 HTTP body 方案。
- [ ] 定义 action 列表：`findOne`、`find`、`insertOne`、`updateOne`、`deleteOne`、`aggregate`。
- [ ] 定义请求外壳字段：`database`、`collection`、`filter`、`projection`、`sort`、`limit`、`skip`、`document`、`update`、`pipeline`、`upsert`。
- [ ] 定义响应格式，统一使用 relaxed Extended JSON 返回 BSON 类型。
- [ ] 明确 Content-Type 约定：优先支持 `application/ejson`，兼容 `application/json`。
- [ ] 在 Go 层使用官方 driver 的 `bson.UnmarshalExtJSON` / `bson.MarshalExtJSON` 处理 EJSON。
- [ ] 设计 database / collection 白名单配置，避免任意库表访问。
- [ ] 设计 MQL operator 白名单，禁止 `$where`、server-side JavaScript、危险 command 和不支持 DocumentDB 的语法。
- [ ] 设计 aggregation stage 白名单，先只开放 `$match`、`$project`、`$group`、`$sort`、`$limit`、`$skip`。
- [ ] 为 `limit`、body size、执行超时、返回文档数量设置默认上限。
- [ ] 明确 gateway 与现有 `/upload` 的关系：保留 `/upload` 作为 TA 日志上报入口，新 Mongo Data API 使用独立 path。

## Lambda / DocumentDB 部署方案

- [ ] 输出 Lambda 部署建议文档：API Gateway HTTP API -> Lambda/Tango gateway -> DocumentDB。
- [ ] 说明 Lambda execution environment 复用 Mongo client / connection pool 的约束。
- [ ] 说明 Lambda VPC、security group、private subnet、Secrets Manager / VPC endpoint / NAT 的部署要求。
- [ ] 说明 DocumentDB 连接串模板和 CA bundle 放置方式。
- [ ] 给出并发与连接池计算方法，避免 Lambda 并发放大 DocumentDB 连接数。

## 测试与文档

- [ ] 为 EJSON decode/encode 增加单元测试，覆盖 `$oid`、`$date`、`$numberLong`、`$numberDecimal`。
- [ ] 为 MQL/operator/stage 白名单增加拒绝测试。
- [ ] 为每个 action 增加 handler 单元测试。
- [ ] 为 gateway Mongo Data API 增加 MongoDB 集成测试。
- [ ] 为 DocumentDB 兼容路径补充测试说明，使用 `TANGO_TEST_MONGO_URI` 手动验证。
- [ ] 更新 `doc/usage.md`，补充 Mongo Data API 请求示例。
- [ ] 更新 `doc/config.md`，补充 Mongo Data API 白名单、超时、limit 等配置项。
- [ ] 更新 `doc/arch.md`，补充 gateway 中 `/upload` 与 Mongo Data API 的职责分离。
