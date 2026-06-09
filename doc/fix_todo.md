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

- [ ] **`go test -race` 从未在 Linux 跑过**（本机 Windows 无 gcc/cgo，跑不了 race detector）。
      必须在 `test/docker-compose.yml` 里补：`go test -race ./...` 与 `go test -race ./internal/source/tailer/...` 全绿
      （见 [`doc/test2.md`](test2.md) B1/B2、[`doc/test.md`](test.md) B2/B3）。
- [ ] **fd 回收回归未在 Linux 验证**：`TestReap_DeletedFileReleasesTail` 的 **poll / hybrid / event** 三个子用例
      必须在 Linux 容器真跑（poll 子用例在非 Linux `t.Skip`，Windows 下会"假绿"）。见 test2.md B2 / C 组、test.md D 组。
- [ ] **看门狗优雅重启（E2）未验证**：低阈值 `maxOpenFDs` + 大量 rotate 备份顶过阈 → 必须证明
      ① 打 `triggering graceful restart`；② drain+flush 在途 batch **全部落库、计数对得上、不丢数据**；
      ③ exit 0 干净退出；④ 容器重启后 fd 表清零。
- [ ] **背压下 fd 释放 / drain 不死锁（F2）未验证**：`out` channel(2000) 打满 + 删除正在 tail 的文件时，
      `ctx.Done()` 分支仍能让 tail goroutine 退出、defer 关 fd；看门狗 `cancelRun` 后 drain 不死锁。
- [ ] **长稳/压测（test.md G1）未跑**：~2–3 GB/h、size100/backup10 连续 rotate 4h，
      `deleted fd / goroutine / RSS / 卷 used` 四条曲线全程平稳、无单调上升。这是"真的不泄漏"的硬证据。

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

- [ ] **功能端到端回归（test.md H1–H4）未跑**：`PaymentOrderState`/`user_set` 过滤、identity 1:1 与 1:N 绑定、
      rotate 跨文件边界事件不丢、SIGTERM 优雅退出 drain 完成 + deleted fd 清零。
- [ ] **`NewFromTree` 重构等价性（test2.md G 组）未验证**：`d38cd13`/`31af894` 给 api/gateway/daemon 加了
      `NewFromTree`，需证明与手工 `FromTree`+`New` 等价、daemon 在连 Mongo 之前就 fail-fast 校验 `logPattern`、
      且 typed `New()` 回归全绿（这是"没把库语义改坏"的硬证据）。
- [ ] **附带产物清理**：旧分支 `hotfix/logger-dedup-5e3316c73` 确认可删（logger ext 路由改名为 tango 已在 master `3c63ad5c7` 落地，消除了 double-writer 配置问题）。

---

## 备注：根因与误判（留档，避免重复踩坑）

- **实锤根因**：tango 与 logbus 都 tail `ta.production2-*`；ta logger 的 lumberjack(backup:10) 不断 rotate 删旧备份，
  tango tailer 不及时关已删除文件的 fd → deleted-but-open 累积。验证：tango 容器 `/proc/1/fd` 有 41 个 deleted 文件；
  `du /go`=1.2G 但 `df /` overlay=7.2G，6G 差额就是 deleted-but-open。
- **已排除的误判**（别再往这两条上花时间）：
  ① logger double-writer（thinkingdataExt 与 thinkingdata 写同一 ta 文件）——retention 一直正常（文件数稳定 11）；
  ② aws-logging（LoggingDisabled）——所有集群都有此 annotation 且健康，与磁盘无关。
