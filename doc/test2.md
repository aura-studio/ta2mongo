# Tango v1.5.1 发布前测试任务单（fd 看门狗 + 运行时指标 + NewFromTree 重构）

> 用法：这是**任务单**，做完一条就删掉一条。剩下的就是还没做的。
>
> 铁律：**所有 fd / inode / deleted-but-open 相关测试必须在 Linux Docker 容器里跑**。
> `/proc/<pid>/fd`、`open_fds` 计数、`IN_DELETE_SELF` 自锁都是 Linux 语义，Windows 复现不出来、
> 会"假绿"。复用 `test/docker-compose.yml`（`tango-test` + `mongo:6`，注入 `TANGO_TEST_MONGO_URI`）。
>
> 本单覆盖 **v1.5.1 相对 v1.5.0 的增量**（commit `d38cd13` + `31af894`）：
> 1. tailer reaping 收口（`scanAndTail` 反向回收 + per-file `CancelFunc`）。
> 2. hybrid/**event** 的 `os.Stat`+`os.SameFile` 删除/inode 自检（event 模式原本无 ticker）。
> 3. fd 看门狗 `source.tailer.maxOpenFDs`（默认 0=关；超阈→优雅 drain+flush+退出，交编排器重启）。
> 4. 运行时指标日志：`reportStats` 每 60s 打 `goroutines / open_fds / tailed_files`。
> 5. `NewFromTree` 重构：`api` / `gateway` / `daemon` 三处；typed `New()` 必须保持不变。
>
> v1.5.0 已覆盖的 reaping 核心门禁见 `doc/test.md` 的 D/E/G 组，本单不重复，只补增量。
>
> **Release Gate = B2 + C 组 + E2 + F2 + G 组全过。**

---

## A. 测试环境（Docker，Linux/amd64）
- [ ] A1 复用 `test/docker-compose.yml`：`docker compose -f test/docker-compose.yml run --rm tango-test go build ./...` 通过。
- [ ] A2 容器内 `go vet ./...` 无告警（含全部测试文件编译）。

## B. 静态与单元（race）
- [ ] B1 容器内 `go test -race ./...` 全绿（这是本机 Windows 跑不了的那条，必须在 Linux 补）。
- [ ] B2 容器内 `go test -race ./internal/source/tailer/...` 全绿，重点确认 `TestReap_DeletedFileReleasesTail` 的 **poll/hybrid/event** 三个子用例都过（poll 子用例在非 Linux 会 `t.Skip`，Linux 下必须真跑）。

## C. 删除回收时延（reaping + 自检，三种模式各一遍）
> 验证两条独立回收路径都在工作：① `reapMissing` 兜底（≤ 1×rescanInterval）；② ticker 自检快路径（≤ ~500ms 释放 fd）。
- [ ] C1 **hybrid**：tail 一个文件 → 删除 → 断言负责该文件的 goroutine 退出、`Tailer.TailedCount()` 在 ≤ 1×rescan 内归零，`/proc/self/fd` 中对应 `(deleted)` 条目消失。
- [ ] C2 **event**：同 C1。重点验证 event 模式**新加的 ticker** 生效——删除后即便不等 rescan，fd 也在 ~500ms~1×hybridPollInterval 内被 `tt.Stop()` 释放（v1.5.0 前 event 模式无 ticker，会一直挂住）。
- [ ] C3 **poll**：同 C1（poll 本就每周期 `os.Stat`，泄露窗口 ≤200ms；这里确认 reaping 也把空转的重试 goroutine 收掉）。
- [ ] C4 **in-place rotation**（同名 path、inode 变化，非删除）：`os.SameFile` 检测到 inode 变更 → hybrid/event 主动 `return` 重开新 inode，从头读新文件，不卡在旧 inode 上。
- [ ] C5 连续 rotate 200 次（hybrid）：稳态 `TailedCount()` ≈ 存活文件数、`NumGoroutine()` 平稳、`(deleted)` fd 计数不随轮转单调增长。

## D. 运行时指标日志（`reportStats`）
- [ ] D1 启动 daemon，等 ≥ 60s，确认每 60s 一条 `report: runtime stats`，字段含 `goroutines` / `open_fds` / `tailed_files` 三项。
- [ ] D2 Linux 下 `open_fds` 为正整数且与 `ls /proc/<pid>/fd | wc -l` 量级吻合（允许 ±2 误差，含 ReadDir 自身 fd 的 -1 修正）。
- [ ] D3 `tailed_files` 数值 == 当前 glob 命中的活动文件数；制造一次 rotate+删除后该值先升后回落，不单调增长。
- [ ] D4 非 Linux（宿主机直接 `go run`，仅此条可在 Windows/macOS 跑）：`open_fds` 打印为 `-1`（unknown），不报错、不 panic。

## E. fd 看门狗（`source.tailer.maxOpenFDs`）★本轮重点★
> 注意：看门狗判定在 `reportStats` 的 60s tick 内，故检测时延 ≤ `statsReportInterval`(60s)。压测时可临时把常量调小以缩短等待，但**回归用例必须验证默认 60s 路径**。
- [ ] E1 **默认关闭**：不配 `maxOpenFDs`（或配 0/负数，经 `ApplyDefaults` 归零）→ 即便人为把 fd 顶到很高，进程**不重启**，只照常打指标。
- [ ] E2 **超阈优雅重启（核心门禁）**：配一个低阈值（如 50）+ 让 glob 命中大量 rotate 备份把 fd 顶过阈 → 观察：① 打一条 `ERROR ... triggering graceful restart`；② pipeline 走 drain+flush，**在途 batch 全部落库 Mongo（计数对得上，不丢数据）**；③ 进程以 exit 0 干净退出；④ 容器 `restartPolicy: Always` 下被重新拉起、fd 表清零。
- [ ] E3 **阈值边界**：`open_fds == threshold` 不触发，`> threshold` 才触发（确认是严格大于）。
- [ ] E4 **非 Linux inert**：`open_fds == -1` 时条件 `-1 > threshold` 永不成立，看门狗不误触发。
- [ ] E5 **SIGTERM 不被看门狗干扰**：正常 SIGTERM 走父 ctx → `runCtx` 取消 → 优雅退出；确认看门狗的 `cancelRun` 与信号路径互不打架、`reportDone` 正常关闭、`logFinalStats` 照打。

## F. 背压下的 fd 释放与重启
- [ ] F1 mongo 写入暂停 / `out` channel(2000) 打满，期间删除正在 tail 的文件：fd 仍在 ≤ N 秒内释放（验证 `out<-line` 阻塞时 `ctx.Done()` 分支仍能让 tail goroutine 退出、defer 关 fd）。
- [ ] F2 背压期间触发看门狗（fd 超阈）：`cancelRun` 后 pipeline 能在背压下完成 drain（不死锁）；若 drain 受阻，记录最坏耗时与是否需要硬退出兜底。

## G. `NewFromTree` 重构等价性 ★保证没改坏接线★
- [ ] G1 **daemon**：同一份 cfgtree 下，`daemon.NewFromTree(ctx, cfg)` 与手工 `dao/parser/source/process/cfgsync.FromTree` + `daemon.New(...)` 构造出的 Service 行为等价（连同 logPattern 校验、启动 banner）。
- [ ] G2 **daemon fail-fast**：`logPattern` 缺失时 `NewFromTree` 在**连 Mongo 之前**就报 `source.tailer.logPattern is required`（不应先 `dao.New` 连库、再 `EnsureIndexes`、最后才失败）。
- [ ] G3 **gateway**：`gateway.NewFromTree(ctx, cfg)` 返回的 `gwCfg.Addr` 正确；`Role.Run` 能据此起 HTTP 服务，`/healthz`、`/upload` 通。
- [ ] G4 **api**：`api.NewFromTree` 切的 dao/process/parser/cfgsync 四配置与 `api.New` 显式传等价；`Upload` 行为一致。
- [ ] G5 **typed New 未破（回归）**：现有套件全绿——`go test ./internal/role/...`（gateway httptest、api、cli）、`./client/...`（SDK 走 `api.New` typed）、`tests/`（`gateway.New`/`daemon.New`/`api.New` 均 typed）。这条是"没把库语义改坏"的硬证据。

---

## Release Gate（全勾才发 v1.5.1）
- [ ] B1 + B2：容器内 `-race` 全绿
- [ ] C 组：三种模式删除回收 + event 自检 + inode 复用全过
- [ ] E2：看门狗优雅重启，在途数据不丢、进程干净退出被重启
- [ ] F2：背压下 fd 释放 / 看门狗 drain 不死锁
- [ ] G 组：三个 `NewFromTree` 等价、fail-fast 正确、**typed `New` 回归全绿**
