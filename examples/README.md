# ta2mongo examples (client)

## 前提
`client.New(...)` 目前要求把 MongoDB 数据库名放在 URI 的 path 里，例如：
`mongodb://localhost:27017/ta2mongo`

示例都放在 `examples/client/` 下：每个示例是一个可运行的 `main` 程序（Go 源码不放在 README 的代码块里）。

## 1) Ingest（单条）
- 代码：`examples/client/ingest/main.go`
- 运行：
  go run ./examples/client/ingest --uri "mongodb://localhost:27017/ta2mongo"

## 2) IngestBatch（多条）
- 代码：`examples/client/ingestbatch/main.go`
- 运行：
  go run ./examples/client/ingestbatch --uri "mongodb://localhost:27017/ta2mongo"
