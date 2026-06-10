# Tango v1.5 必坑待修清单（fix_todo）

> 范围：当前 `v1.5` 分支上**已知的坑、但还没真正修复 / 没验证 / 没落地生产**的事项。
> 不含功能性 TODO（backfill / taskqueue / filebatch 等见 [`doc/v1.6/todo.md`](v1.6/todo.md)）。
> 用法同其它任务单：做完一条划掉，根因彻底消除后删行。
>
> 现状一句话：**fd 泄漏的修复代码已写完、已 commit、已 tag `v1.5.1`（at `31af894`），
> 但 release gate（Linux/Docker 下的 `-race` + fd 回归 + 看门狗）一条都没跑过，生产也还没部署。**
> 即"改了、但没证明改对、也没上线"。这是当前最大的风险面。

---

## P0 — fd 泄漏修复"已 tag 未验证"（最高优先级）

> 背景：rocket-nano EKS Fargate 盘满根因 = tango tailer 持有已删除日志文件的 fd
> （deleted-but-open 累积撑满 overlay，~5GB/h）。根因细节：inotify 对"本进程持有打开的文件被
> unlink"不发 `IN_DELETE_SELF`（自锁）→ hpcloud/tail `ReOpen:true` 默认永不关 fd。
> 修复在 `d38cd13`：`reapMissing` 反向回收 + per-file `CancelFunc` + event/hybrid 加
> `os.Stat`+`os.SameFile` 自检 ticker + fd 看门狗。**代码已合入 v1.5.1，但下面的门禁全是空勾。**

- [x] ~~**`go test -race` 从未在 Linux 跑过**~~ — 已在 Ubuntu 24.04 容器内 `go test -race ./...` +
      `./internal/source/tailer/...` 全绿（0 FAIL / 0 DATA RACE）。**期间 `-race` 还抓出并修了 4 个真实
      tailer 并发 bug**：`tt.Tell()` 与 hpcloud 内部 reopen 竞争、`Run` 的 `close(out)` 与发送竞争、
      背压下 `tt.Stop()` 死锁致 fd 不释放、hybrid 背压丢行。
- [x] ~~**fd 回收回归未在 Linux 验证**~~ — `TestReap_DeletedFileReleasesTail` poll/hybrid/event 三子用例
      在 Linux 容器真跑全过；另补 `TestReap_C2`（event ticker 隔离）、C4/C5、D1–D5、`-race -count=5`。
- [x] ~~**看门狗优雅重启（E2）未验证**~~ — `TestWatchdog_E2_GracefulRestartDrainsNoLoss`：`triggering
      graceful restart` ERROR + drain 在途 40/40 全落库不丢 + Run 干净返回(=exit 0)；④重启清零属编排器职责。
- [x] ~~**背压下 fd 释放 / drain 不死锁（F2）未验证**~~ — `TestBackpressure_F1/F2`（三模式 `-race`）+
      `TestWatchdog_F2`（背压下 drain 401ms 完成、不死锁、200/200 不丢）。
- [x] ~~**长稳/压测（test.md G1）**~~ — **4h 跑满 VERDICT: PASS**（4536 万行/10.24GB @2.56GB/h，
      `deleted_fd` 全程 0、goroutine 趋势 0、RSS 峰值 259MB、fs_used 有界锯齿）。基线归档
      `test/results/soak_G1.csv` + `soak_G1_summary.txt`。**"真的不泄漏"的硬证据已落地——P0 验证面闭环。**

> ⚠️ 注意：`v1.5.1` 这个 tag 是在测试任务单（`91e0d17`，加了 test.md/test2.md）**之前**就打的，
> 所以 tag 本身不代表通过了任何 release gate。要么补全验证后视为正式可用，要么重打 `v1.5.2`。

## P0 — 生产仍在"裸奔"（修复未落地）

- [ ] **真正的 fd 修复没回到生产**。当前 rocket-nano deployment-6 的止血只是"摘掉 tango 容器"（变回 2/2），
      tango sidecar 根本没在跑。修复（v1.5.1）需 build 镜像（tag=git commit hash）+ 重新部署 tango 回 deployment-6。
- [ ] **部署后必须盯曲线**：重新挂回 tango 后，每 30s 采 `ls -l /proc/<tango-pid>/fd | grep -c deleted` 与卷 `df used`，
      确认 deleted 计数稳定、used 不单调增长（test.md E2）。没有这步就等于没验证生产环境。
- [ ] **临时加的 150Gi ephemeral-storage 要调回**（之前为拖延盘满临时加的，修复生效后应还原）。
- [ ] **看门狗在生产默认是关的**：`source.tailer.maxOpenFDs` 默认 0 = 关闭（`report.go:233` 判定 `threshold > 0`）。
      生产配置必须显式设一个非 0 阈值，否则这层兜底等于没有——修复退化时不会自愈重启。

---

## P1 — 平台局限 / 已知但未根治的坑

- [ ] **上游库 hpcloud/tail 的自锁根因没消除，只是绕过**。当前靠两条兜底：`reapMissing`（≤1×rescan，≤30s）
      + event/hybrid 的 `os.Stat`+`os.SameFile` 自检 ticker（~500ms）。event 模式本质上是"我们自己加了 ticker"
      才不泄漏（v1.5.0 前 event 模式无 ticker，会一直挂住）。
      **风险**：谁要是把 `tailFileEvent`/`tailFileHybridEvent` 里那个 `ticker` 删了、或把 reap 改回去，坑立刻复发，
      且 Windows 测试发现不了。考虑加注释护栏 / fork 或替换上游库，从源头根治。
- [ ] **poll 模式在 Windows 无法删除打开的文件**（`os.Open` 没带 `FILE_SHARE_DELETE`），
      所以 poll 的 deleted-fd 回收**只能在 Linux 验证**，Windows 永远测不出泄漏。
      本机（Windows）开发时务必记得：fd / inode / deleted-but-open 相关的"绿"都是假绿，一律以 Linux 容器结果为准。
- [ ] **看门狗 `open_fds` 非 Linux 返回 -1（inert）**：`-1 > threshold` 永不成立，看门狗在非 Linux 不触发。
      生产是 Linux 没问题，但任何非 Linux 部署目标上这层兜底自动失效——若将来扩展到别的平台需另想办法。

---

## P2 — 收尾 / 待确认

- [x] ~~**功能端到端回归（test.md H1–H4）未跑**~~ — daemon 级 [`test/h_release_gate_test.go`](../test/h_release_gate_test.go)：
      H1 只留 `PaymentOrderState`/`user_set`、H3 rotate 边界不丢、H4 SIGTERM drain + deleted fd 清零；
      H2 identity 1:1/1:N 由 `dao/store` 11 个 `TestIdentityResolver_*` 覆盖（mongo:6 + 真 DocumentDB 双跑全过）。
- [x] ~~**`NewFromTree` 重构等价性（test2.md G 组）未验证**~~ — daemon/api/gateway 三处 `NewFromTree` 等价
      （`TestNewFromTree_G1/G3/G4`）+ daemon fail-fast 在连 Mongo 前校验 logPattern（`G2`，0.00s 返回）+
      typed `New()` 回归（全量 `-race ./...` 含 role/client/test 全绿）。
- [ ] **附带产物清理**：旧分支 `hotfix/logger-dedup-5e3316c73` 确认可删（logger ext 路由改名为 tango 已在 master `3c63ad5c7` 落地，消除了 double-writer 配置问题）。

---

## 备注：根因与误判（留档，避免重复踩坑）

- **实锤根因**：tango 与 logbus 都 tail `ta.production2-*`；ta logger 的 lumberjack(backup:10) 不断 rotate 删旧备份，
  tango tailer 不及时关已删除文件的 fd → deleted-but-open 累积。验证：tango 容器 `/proc/1/fd` 有 41 个 deleted 文件；
  `du /go`=1.2G 但 `df /` overlay=7.2G，6G 差额就是 deleted-but-open。
- **已排除的误判**（别再往这两条上花时间）：
  ① logger double-writer（thinkingdataExt 与 thinkingdata 写同一 ta 文件）——retention 一直正常（文件数稳定 11）；
  ② aws-logging（LoggingDisabled）——所有集群都有此 annotation 且健康，与磁盘无关。
