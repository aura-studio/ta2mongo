# Tango 测试报告（v1.5 / v1.5.1 fd 泄漏修复验证）

> 生成时间：2026-06-09 09:28 UTC
> 对应任务单：[`doc/test.md`](test.md)、[`doc/test2.md`](test2.md)、[`doc/fix_todo.md`](fix_todo.md)
>
> **铁律遵守**：本报告所有结果均在 **Linux Docker 容器（Ubuntu 24.04）** 内产生，
> **未在 Windows 宿主机上跑任何 fd / inode / deleted-but-open 相关用例**。
> deleted-but-open / `/proc/<pid>/fd` / inode 复用是 Linux 语义，Windows 会"假绿"。

---

## 测试环境

| 项 | 值 |
|---|---|
| 容器基础镜像 | `ubuntu:24.04`（实测 **Ubuntu 24.04.4 LTS**） |
| 平台 | `linux/amd64` |
| Go 工具链 | **go1.26.2 linux/amd64**（官方 tarball，满足 `go.mod` 的 `go 1.26.2`） |
| C 编译器 | gcc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0 — `-race` 需要 cgo |
| Race detector | 启用（`CGO_ENABLED=1`，`go test -race`） |
| Mongo | `mongo:6`，注入 `TANGO_TEST_MONGO_URI=mongodb://mongo:27017` |
| 编排 | [`test/docker-compose.ubuntu.yml`](../test/docker-compose.ubuntu.yml) + [`test/Dockerfile.ubuntu`](../test/Dockerfile.ubuntu) |

> 说明：原 [`test/Dockerfile`](../test/Dockerfile) 基于 `golang:1.23`（Debian）。按"必须 Ubuntu 24.04"
> 要求，本轮新增 `Dockerfile.ubuntu` / `docker-compose.ubuntu.yml`，基于 `ubuntu:24.04` 安装
> 官方 Go 1.26.2 + build-essential + procps + lsof，原文件保留未动。

复现命令：

```bash
docker compose -f test/docker-compose.ubuntu.yml build tango-test
docker compose -f test/docker-compose.ubuntu.yml run --rm tango-test go build ./...
docker compose -f test/docker-compose.ubuntu.yml run --rm tango-test go vet ./...
docker compose -f test/docker-compose.ubuntu.yml run --rm tango-test go test -race ./...
docker compose -f test/docker-compose.ubuntu.yml run --rm tango-test go test -race -v ./internal/source/tailer/...
```

---

## 总览

| 门禁 | 结果 |
|---|---|
| `go build ./...` | ✅ PASS |
| `go vet ./...` | ✅ PASS（无告警，含全部测试文件编译） |
| `go test -race ./...` | ✅ PASS — **19 个含测试的包全绿，0 FAIL，0 DATA RACE** |
| `go test -race ./internal/source/tailer/...` | ✅ PASS（5.52s） |
| **`TestReap_DeletedFileReleasesTail` poll/hybrid/event 三子用例** | ✅ **全部 PASS（含 poll 子用例在 Linux 真跑，未 Skip）** |

> 这是本机 Windows **跑不了**的那条（无 gcc/cgo → race detector 不可用；poll 删除回收 Windows 测不出）。
> 本轮在 Ubuntu 24.04 容器内真跑通过，即 [`fix_todo.md`](fix_todo.md) P0 中
> "`go test -race` 从未在 Linux 跑过" 与 "fd 回收回归未在 Linux 验证" 两条已**落地验证**。

---

## A. 测试环境（Docker, Linux/amd64）

- ✅ **A3 / test2.A1** `go build ./...` 通过（依赖全部成功下载并编译）。
- ✅ **B1 / test2.A2** `go vet ./...` 无告警。

## B. 静态与单元（race）

- ✅ **test.md B2 / test2.md B1** 容器内 `go test -race ./...` 全绿。
- ✅ **test.md B3 / test2.md B2** `go test -race ./internal/source/tailer/...` 全绿。

### `TestReap_DeletedFileReleasesTail`（fd 泄漏核心回归）

```
=== RUN   TestReap_DeletedFileReleasesTail
=== RUN   TestReap_DeletedFileReleasesTail/poll
    tailer: file gone, stopping tail and releasing fd  path=.../app.log
=== RUN   TestReap_DeletedFileReleasesTail/hybrid
    tailer: file gone, stopping tail and releasing fd  path=.../app.log
=== RUN   TestReap_DeletedFileReleasesTail/event
    tailer: file gone, stopping tail and releasing fd  path=.../app.log
--- PASS: TestReap_DeletedFileReleasesTail (0.35s)
    --- PASS: TestReap_DeletedFileReleasesTail/poll   (0.10s)
    --- PASS: TestReap_DeletedFileReleasesTail/hybrid (0.12s)
    --- PASS: TestReap_DeletedFileReleasesTail/event  (0.12s)
```

三种 tail 模式删除文件后均打印 `file gone, stopping tail and releasing fd` 并退出 →
fd 回收路径（`reapMissing` 兜底 + event/hybrid 的 `os.Stat`+`os.SameFile` 自检）在 Linux 下确实生效。

## 包级结果（`go test -race ./...`）

```
ok  github.com/aura-studio/tango/client                    1.046s
ok  github.com/aura-studio/tango/config                    1.366s
ok  github.com/aura-studio/tango/internal/cfgsync          4.190s
ok  github.com/aura-studio/tango/internal/dao/ejson        1.178s
ok  github.com/aura-studio/tango/internal/dao/mongo        1.032s
ok  github.com/aura-studio/tango/internal/dao/sql          1.185s
ok  github.com/aura-studio/tango/internal/dao/store       12.741s   (mongo 集成)
ok  github.com/aura-studio/tango/internal/logging          1.028s
ok  github.com/aura-studio/tango/internal/parser           1.016s
ok  github.com/aura-studio/tango/internal/parser/filter    1.027s
ok  github.com/aura-studio/tango/internal/parser/talog     1.043s
ok  github.com/aura-studio/tango/internal/process/batch   29.051s   (mongo 集成)
ok  github.com/aura-studio/tango/internal/process/core     1.040s
ok  github.com/aura-studio/tango/internal/process/pipeline 1.056s
ok  github.com/aura-studio/tango/internal/role/api         1.211s
ok  github.com/aura-studio/tango/internal/role/cli         1.262s
ok  github.com/aura-studio/tango/internal/role/gateway     7.774s
ok  github.com/aura-studio/tango/internal/source/tailer    5.522s
ok  github.com/aura-studio/tango/tests                     8.020s
（其余 11 个目录为 [no test files]）
```

- ✅ **test2.md G5（typed `New` 回归）**：`internal/role/...`、`client/...`、`tests/` 全绿 →
  `NewFromTree` 重构未破坏 typed `New()` 库语义这条硬证据成立。
- ✅ **H 组功能集成的一部分**（`dao/store`、`process/batch` 的 mongo 集成用例）随 `-race ./...` 一起通过。

---

## 尚未在本轮覆盖（需独立场景/长稳运行，非单测套件能跑）

以下属于"运行时长稳/系统级"门禁，不在 `go test` 套件内，本轮**未执行**，仍为空勾：

- ⬜ **test.md E2 / G1** —— 真实 lumberjack 高速 rotate ≥10min / 生产速率 4 小时长稳，
  采 `/proc/<pid>/fd` deleted 计数与卷 `df used` 四条曲线。需长时间运行 + 指标采集脚本。
- ⬜ **test2.md E2（看门狗优雅重启核心门禁）** —— 低 `maxOpenFDs` 阈值 + 大量 rotate 顶过阈，
  验证 `triggering graceful restart` + drain/flush 在途 batch 全部落库不丢 + exit 0 + 容器重启 fd 清零。
- ⬜ **test2.md F2 / test.md F** —— 背压（mongo 暂停 / `out` channel 打满）下 fd 释放与 drain 不死锁。
- ⬜ **test.md H1–H4** —— `PaymentOrderState`/`user_set` 端到端过滤、identity 1:1/1:N、
  SIGTERM 优雅退出 deleted fd 清零（部分由集成单测覆盖，端到端 daemon 级未单独跑）。

> 建议：这几条以 docker compose 起 daemon + 后台采样脚本的方式单独编排，结果追加到本文件。

---

## 结论

- ✅ **可自动化的 Release Gate 全部通过**：Ubuntu 24.04 容器内 build + vet + `go test -race ./...`
  全绿，**0 FAIL / 0 DATA RACE**；fd 泄漏核心回归 `TestReap_DeletedFileReleasesTail` 的
  poll/hybrid/event 三模式在 Linux 真跑通过。
- ✅ [`fix_todo.md`](fix_todo.md) P0 中"`-race` 从未在 Linux 跑过""fd 回收回归未在 Linux 验证"
  两条**已消除**。
- ⬜ 剩余风险面在**长稳/看门狗/背压**的系统级场景（E2 / G1 / F2 / 看门狗优雅重启），
  需独立运行编排，尚未在本轮覆盖。
