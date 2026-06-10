# Tango v1.5 发布前测试任务单

> 用法：这是**任务单**，做完一条就删掉一条。剩下的就是还没做的。
>
> 铁律：**所有测试必须在 Linux Docker 容器里跑**。deleted-but-open / unlink / inode 复用 /
> `/proc/<pid>/fd` 都是 Linux 语义，Windows 复现不出来、会"假绿"。**禁止在宿主机(Windows)或
> 线上环境跑。**
>
> 本轮必须拦的缺陷：tailer 在日志被 rotate/删除后不释放 fd（`hpcloud/tail` `ReOpen:true`
> + `tailFileHybridEvent` 文件删除时 `continue` 不退出）→ deleted-but-open 文件堆积撑满磁盘。
> **Release Gate = D 组 + E2 + G1 全过。**

---

## A. 测试环境（Docker，Linux/amd64）
- [x] ~~A1 新建 `test/Dockerfile`：`FROM golang:1.23`，`apt-get install -y procps lsof`（要 `ls /proc`、`lsof`）。~~
- [x] ~~A2 新建 `test/docker-compose.yml`：服务 `tango-test`（挂载源码）+ `mongo:6`；给 `tango-test` 注入 `TANGO_TEST_MONGO_URI=mongodb://mongo:27017`。~~
- [x] ~~A3 `docker compose -f test/docker-compose.yml run --rm tango-test go build ./...` 通过。~~

## B. 静态与单元
- [x] ~~B1 容器内 `go vet ./...` 无告警。~~
- [x] ~~B2 容器内 `go test -race ./...` 全绿（无 mongo 的纯单元）。~~
- [x] ~~B3 容器内 `go test -race ./internal/source/tailer/...` 全绿。~~

## C. tailer 文件生命周期（每条都对 **poll / event / hybrid** 三种模式各跑一遍）
- [x] ~~C1 持续 append：tail 输出行数 == 写入行数，无丢行、无重复。~~
- [x] ~~C2 rotate（`rename` 当前文件 + 新建同名）：新文件从头被 tail，旧文件残余被读完。~~
- [x] ~~C3 truncate（文件 size 变小）：tail 从头重读，不卡死、不 panic。~~
- [x] ~~C4 删除后**不重现**（模拟 lumberjack 备份被删）：负责该文件的 goroutine 在 ≤ 2×rescanInterval 内退出。~~

## D. fd / goroutine 泄漏回归　★核心门禁，重点在 hybrid 模式★
- [x] ~~D1 写测试辅助 `countDeletedFDs()`：读 `/proc/self/fd`，统计指向 ` (deleted)` 的条目数。~~
- [x] ~~D2 单文件：tail 一个文件 → 删除 → 等 2×rescan → **deleted fd 计数回到 0**。~~
- [x] ~~D3 连续 rotate 100 次（每次新建+写满+删最旧，保留窗口 N 个）：**稳态 deleted fd ≤ 当前存活文件数，且不随轮转次数单调增长**。~~
- [x] ~~D4 goroutine：rotate 1000 次后 `runtime.NumGoroutine()` 稳定（±少量），`Tailer.tailed` map size 不单调增长。~~
- [x] ~~D5 对 **event** 与 **poll** 模式重复 D2+D3，确认三种模式都不泄漏 fd。~~

## E. 与真实 lumberjack 交互（集成）
- [x] ~~E1 用 `natefinch/lumberjack`（size=10MB、backup=10、compress=false）高速写 `ta.test-*.log`；tango **hybrid** tail 同一 glob，持续 ≥ 10 分钟。~~（11min @2560MB/h，2.13M 行无 stall）
- [x] ~~E2 期间每 30s 采 `ls -l /proc/<tango-pid>/fd | grep -c deleted` 与该卷 `df` used：**deleted 计数稳定、used 不单调增长**。~~（deleted_fd 全程 0；fs_used 100–109MB 锯齿，trend 0.0MB）

## F. 背压（下游慢/阻塞）
- [x] ~~F1 mongo 写入人为限速或暂停，`out` channel（2000）打满后：进程不死锁、不丢 panic、不 OOM。~~
- [x] ~~F2 背压期间删除正在 tail 的文件：fd 仍在 ≤ N 秒内释放（验证 `out<-line` 阻塞不会卡住 fd 释放路径）。~~

## G. 长稳 / 压测
- [x] ~~G1 模拟生产速率（~2–3 GB/h，size100/backup10）连续 rotate **4 小时**：`deleted fd / goroutine / RSS / 卷 used` 四条曲线全程平稳（无单调上升）。~~（**VERDICT: PASS**：4h 整、4536 万行/10.24GB @2.56GB/h；`deleted_fd` 全程 **0**、goroutine 趋势 **0**、RSS 峰值 259MB、fs_used 1001–1100MB 锯齿 trend +17.3MB，7 项不变量全过）
- [x] ~~G2 把 G1 的四条曲线数据归档到 `test/results/`（作为基线，留待回归对比）。~~（[`soak_G1.csv`](../test/results/soak_G1.csv) 481 个采样点 + [`soak_G1_summary.txt`](../test/results/soak_G1_summary.txt)）

## H. 功能端到端（集成，需 mongo）
- [x] ~~H1 投喂含 `PaymentOrderState` / `user_set` 的样本日志：只过滤这两类、写 mongo 字段正确。~~
- [x] ~~H2 identity 解析：`account_id ↔ distinct_id` 绑定正确，1:1 与 1:N 规则符合预期。~~
- [x] ~~H3 rotate 跨文件边界时事件不丢；若实现是 at-least-once，记录可接受的重复边界。~~
- [x] ~~H4 daemon 收 SIGTERM 优雅退出：drain 完成、所有 tail goroutine 退出、deleted fd 清零。~~

## I. 修复验收（改完 tailer 再回归）
- [x] ~~I1 代码核查：`tailFileHybridEvent` / `tailFileEvent` 在文件删除后 `return` + `Stop()`（不再 `continue` 空等）；对"删除不重现"的备份用 `ReOpen:false` 或走 poll/inode 路径。~~（删除时 `return false`→`defer stopTail()`；inode 漂移由 stat-ticker `os.SameFile` 兜底）
- [x] ~~I2 代码核查：tail goroutine 退出处有 `delete(t.tailed, path)`。~~（`startFile` 的 defer 中）
- [x] ~~I3 D 组 + E2 + G1 全部重跑通过。~~（D ✓ `-race -count=5`；E2 ✓ PASS；G1 ✓ PASS）

---

## Release Gate（全勾才发 v1.5）
- [x] ~~D 组全过（fd/goroutine 不泄漏）~~
- [x] ~~E2 与 G1：deleted fd 与卷 used 全程平稳~~（E2 11min PASS；G1 4h PASS，deleted_fd 全程 0）
- [x] ~~H1–H4 功能正确~~
- [x] ~~I1–I3 修复落地并回归通过~~

> **🎉 Release Gate 全过（2026-06-10）**。全程 Linux Docker（Ubuntu 24.04 + Go 1.26.2，`-race`）；
> 期间顺带修了 4 个 tailer 并发 bug + 1 个 identity `id_counter` 竞争（DocumentDB 压测抓出）。
> 详见 [`result.md`](result.md)、[`v1.5/perf-daemon-throughput.md`](v1.5/perf-daemon-throughput.md)。
