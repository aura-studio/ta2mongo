# Tango daemon 上报能力压测报告（测试环境 / 真 DocumentDB）

> 生成时间：2026-06-09。在**测试环境**实跑：EC2 跳板机（us-east-1）**在 VPC 内**直连
> Amazon DocumentDB 测试集群（us-east-1），用 tango daemon 真实上报链路
> `tail → parse → filter → identity → DocumentDB bulk` 压吞吐。
>
> 压测器：[`test/perf/main.go`](../../test/perf/main.go)（静态交叉编译为 linux/amd64，scp 到 EC2 跑，
> 用一份扔后即弃的 `tango_perf_<ts>` 库，结束即 drop）。

## 环境

| 项 | 值 |
|---|---|
| 压测主机 | EC2（us-east-1，8 vCPU，15 GB），**VPC 内**直连，非隧道 |
| 数据库 | Amazon DocumentDB（us-east-1，`replicaSet=rs0`，TLS，`readPreference=primary`，`retryWrites=false`） |
| 上报角色 | tango **daemon**（强制 pipeline），poll tailer 读预填日志文件 |
| 负载 | `n` 条 `#event_name=PaymentOrderState` track 事件，500 个不同 `#account_id`（给 identity 缓存施压），batch=1000 |
| 二进制 | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build`（静态，22 MB） |

复现（在 EC2 上）：
```bash
wget -q https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem
export TANGO_TEST_MONGO_URI='mongodb://USER:PASS@<docdb>:27017/?tls=true&tlsCAFile=/tmp/global-bundle.pem&replicaSet=rs0&readPreference=primary&retryWrites=false'
./tango-perf -n 20000 -workers 4 -batch 1000
```

## 结果

| 配置 | 落库 | 耗时 | 吞吐 | 写入速率 |
|---|---|---|---|---|
| n=20000，workers=4，batch=1000 | **20000 / 20000** | 17.0 s | **≈1175 events/s** | 0.21 MB/s |
| n=20000，workers=8，batch=1000 | **20000 / 20000** | 13.7 s | **≈1456 events/s** | 0.26 MB/s |

- 全部 20000 条事件**完整落库、零 dead-letter**（修复前因下述 bug 卡死）。
- workers 4→8 吞吐 +24%（1175→1456），**说明瓶颈在 DocumentDB 写延迟**（随并发提升但非线性，
  受该实例写能力封顶），不是 daemon 单线程瓶颈。

## ★ 本次压测顺带抓出并修复了一个生产 bug（commit `4f277ef`）

第一次跑（修复前）**永远跑不完**：日志里反复出现
```
identity resolve failed, sending to dead letter  error=" E11000 duplicate key error collection: id_counter index: _id_"
```
**根因**：pipeline 多 worker 在"首次见到新用户"时并发去创建 `id_counter`（`{_id:"user_id"}`）单文档，
`FindOneAndUpdate(upsert)` 的插入竞争让除一个外全部拿到 `E11000`；`nextUserID` **没有重试**，
错误一路上抛 → identity Resolve 失败 → 事件被丢进 `dead_letter` 而非写入 `event`，于是 `event` 计数
永远到不了 N。该竞争在原生 MongoDB 是潜伏的，在 **DocumentDB 的 upsert 并发语义下高概率触发**。

**修复**：`nextUserID` 对 duplicate-key 错误重试（≤8 次）——输家重试时会看到已存在的计数文档、直接 `$inc`，
每个不同 account 仍拿到唯一 `#user_id`；非 dup 错误与 ctx 取消立即返回。
回归用例 `TestIdentityResolver_ConcurrentColdCounter`（64 并发不同新账号、冷计数器，断言 0 错误 + 64 个唯一 id）。
修复后本压测 `20000/20000` 通过。

## 解读与生产建议

- 这是**单个测试 DocumentDB 实例**的 in-VPC 数字，反映的是「该实例写能力 + identity 冷启动」的综合上报吞吐；
  rocket-nano 生产实例规格不同，数字会变，但**结论（写延迟主导、并发可扩、修复后不丢数据）通用**。
- 提吞吐的杠杆：① 加 `pipeline.batchWorkers`（已验证 4→8 有效）；② 增大 `batchSize` 摊薄往返；
  ③ 给 DocumentDB 扩实例/分片；④ 预热 `id_counter`（首条之后就不再有冷启动竞争）。
- identity 缓存有效：500 个用户里只有首现的 500 次走库，其余 19500 条是缓存命中——所以瓶颈是**写**不是 identity 查。

> 备注：fd 泄漏长稳（G1 4h soak）与本压测正交，各自独立验证。
