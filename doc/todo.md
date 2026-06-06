# TODO

本文件只保留尚未完成的任务。任务完成后先改成 `- [x] ~~任务内容~~`，方便本轮核对；下一次整理 TODO 时删除已完成项。
（已完成并合入的历史项——MongoDB Driver v2 升级、Gateway Mongo Data API/ejson、相关测试与文档——已清理，见 git 历史。）

## SQL 支持（拷贝 mongosql 到 internal/dao/sql）

> 目标：把 `github.com/aura-studio/mongosql` 的 SQL→MongoDB 翻译+执行能力以**拷贝**方式引入
> `internal/dao/sql`，作为 dao 子包、依赖 `dao/mongo`（注入连接），并**完全仿照 ejson** 在 dao 根包
> 中转、贯通 api / gateway / cli 三端。不引入 mongosql 的 MySQL 协议层（`mysql/`），不作为外部 module 依赖。
> 源提交：mongosql `fix/sql-semantics-correctness`（95072c1）。

### 0. 依赖与构建打底（先跑通隔离编译）

- [x] ~~拷贝 mongosql `driver/`(2) + `translator/`(11) 共 13 个 .go 到 `internal/dao/sql/`：`driver/driver.go`→`sql.go`、`driver/schema.go`→`schema.go`、`translator/**` 原样到 `internal/dao/sql/translator/**`；跳过 `mysql/`、`tests/`、`scripts/`、`doc/`。~~
- [x] ~~改包名：`package driver` → `package sql`（sql.go、schema.go）。~~
- [x] ~~改 import 路径：`github.com/aura-studio/mongosql/translator...` → `github.com/aura-studio/tango/internal/dao/sql/translator...`（全部拷贝文件）。~~
- [x] ~~tango go.mod 增加 `vitess.io/vitess v0.24.1`；`go mod tidy` 拉齐 vitess 及其间接依赖。~~
- [x] ~~处理 Go 版本：`go get` 自动把 `go` 指令从 1.25.0 上调到 1.26.2（GOTOOLCHAIN=auto 拉取 go1.26.2 工具链）。~~
- [x] ~~`go build ./internal/dao/sql/...` 隔离编译通过（EC2）。~~
- [x] ~~`gofmt -l internal/dao/sql` 干净。~~

### 1. sql 包适配（注入连接，仿 ejson 的 Execute(res,…)）

- [x] ~~去掉自拨号 `Connect` 和 `Close` 的 `Disconnect`（连接由 tango 持有，不可在此关闭）。~~
- [x] ~~新增 `New(res *mongo.MongoResource) (*Driver, error)`：用 `res.Client`/`res.DB` + `translator.New()` 构造，不再 dial。~~
- [x] ~~保留 `Exec(ctx, sql) (*Result, error)` 及 `execFind/Aggregate/Insert/Update/Delete/InsertSelect`、`drainCursor`、`SchemaStore` 原逻辑。~~
- [x] ~~给 `Result` 增加 `MarshalEJSON()`（`bson.MarshalExtJSON`，因为 rows 内含 BSON 类型，需 EJSON 编码）。~~
> 备注：未拷贝 DDL（CREATE/ALTER TABLE 在 mysql 层），schema 为空时 AUTO_INCREMENT/DEFAULT/ON UPDATE 自动跳过，DML/SELECT 仍可用。

### 2. dao 根包中转（仿 ejson 门面）

- [x] ~~`dao.go` 增加 `type SQLResult = sql.Result`。~~
- [x] ~~`Dao` 持有惰性初始化的 `*sql.Driver`（`sync.Once`，避免非 SQL 角色启动开销/失败）。~~
- [x] ~~`func (d *Dao) SQL(ctx, query string) (*SQLResult, error)` 中转到 `sql.Driver.Exec`。~~

### 3. 三端入口（仿 ejson）

- [x] ~~api：`func (c *Engine) SQL(ctx, query string) (*dao.SQLResult, error)` → `c.dao.SQL`。~~
- [x] ~~gateway：路由 `POST /sql`（`handleSQL`）；请求体 JSON `{"sql":"..."}`；响应 relaxed EJSON（`writeEJSON` 泛化为 `ejsonMarshaler` 接口）；`Server.SQL` 透传。~~
- [x] ~~cli：`role.cli.function=sql`；`RunSQL(ctx, daoCfg, in, out)` 读 stdin 全文为一条 SQL → `eng.SQL` → EJSON 写 out；`role.go` 派发。~~
- [x] ~~cli config：`function` 取值新增 `sql`（`FunctionSQL`，`Validate` 允许 upload|ejson|sql）。~~

### 4. 测试（连真实 DocumentDB，仿 ejson）

- [x] ~~`internal/dao/sql/sql_test.go`：单元（New(nil) 报错）+ 集成（insert→select→update→delete 往返，gated `TANGO_TEST_MONGO_URI`，连真实 DocumentDB 通过）。~~
- [x] ~~`tests/sql_test.go`：gateway `POST /sql` + cli `RunSQL` 端到端（连真实 DocumentDB 通过）。~~
- [x] ~~注意 DocumentDB 限制：含表达式的 UPDATE 走 pipeline 形式，DocumentDB 不支持；测试用常量 `SET n = 10`（plain $set）验证通过。~~

### 5. 示例与文档（仿 ejson）

- [x] ~~`examples/config/cli/cli.sql.{min,max}.{yaml,json}`（4 份，`function=sql`）+ `queries.sample.sql` + `examples/config/README.md` 更新。~~
- [x] ~~`doc/usage.md`：`POST /sql` 与 cli sql 示例、库用法（并修正遗留的 `--role.cli.function data`→`ejson`）。~~
- [x] ~~`doc/config.md`：`role.cli.function` 增加 `sql`。~~
- [x] ~~`doc/arch.md`：新增 §5.3 SQL（dao/sql 子包、依赖 dao/mongo + vitess、三端入口）、目录树、§3.1 文件清单、依赖表。~~

### 6. 收尾验证（EC2 + 真实 DocumentDB）

- [x] ~~干净检出：`gofmt -l`、`go vet ./...`、全量 `go test ./... -count=1`（带 `TANGO_TEST_MONGO_URI`）全绿（EC2 + 真实 DocumentDB）。~~
- [x] ~~手测：`curl -X POST :18099/sql -d '{"sql":"SELECT ..."}'` 与 `echo 'INSERT/SELECT/DELETE ...' | tango --config cli.sql.{min.yaml,max.json}` 均通过（真实 DocumentDB）。~~

## Lambda / DocumentDB 部署方案

- [ ] 输出 Lambda 部署建议文档：API Gateway HTTP API -> Lambda/Tango gateway -> DocumentDB。
- [ ] 说明 Lambda execution environment 复用 Mongo client / connection pool 的约束。
- [ ] 说明 Lambda VPC、security group、private subnet、Secrets Manager / VPC endpoint / NAT 的部署要求。
- [ ] 说明 DocumentDB 连接串模板和 CA bundle 放置方式。
- [ ] 给出并发与连接池计算方法，避免 Lambda 并发放大 DocumentDB 连接数。
