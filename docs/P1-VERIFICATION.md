# P1 验收记录

日期：2026-11-06。P0 验收通过后推进 P1；没有实现 P2/P3，没有修改独立 MetaCubeXD 仓库。

## 实现与模块验证

| 模块 | 实现 | 验证 |
|---|---|---|
| Collector | coder/websocket、逐实例 context、errgroup join、退避 jitter、容量/消息/连接数上限、generation 取消 | 双实例后台运行、断线隔离、自动恢复、URL/Secret 轮换、禁用/删除、写失败、无浏览器采集 |
| Telemetry | 总量/连接差分、epoch、独立 lifecycle、UTC 分桶、缺口/恢复候选量 | 首次基线、重复/乱序、重置、三分钟 gap、Go 重启、ID 冲突/复用、短连接漏采、跨分钟、归因不一致 |
| SQLite/Writer | migration 002、sqlc、原子批次、持久游标、有界队列、小时重算、STRICT 整数 | 注入游标失败回滚、重放幂等、旧 generation、BUSY 有界失败、整数溢出、队列满、退出 flush、背压后的恢复 gap |
| Analytics | 六类历史接口、认证诊断、跨实例/墓碑、只读池、限时/窗口/分页/枚举 | 单/多实例汇总、TOP、连接累计值、时间覆盖、开放 gap、无权限/非法查询拒绝、readonly 多连接 |
| 保留与备份 | 14/60/365 天默认策略、小批次清理、checkpoint、CLI 一致性备份 | 2001 条积压分批、截止边界、活跃摘要保留、级联 purge、含 WAL 快照恢复、0600、拒绝覆盖 |
| Gateway/XD 回归 | 原 P0 行为保留 | 原版 XD 双 Endpoint、连接/规则/日志/切换策略、四类 WS，浏览器无真实 Secret |

主要命令：

```sh
go test ./...
go test -tags=integration ./... -timeout=90s
go test -race -tags=integration ./... -timeout=120s
go vet -tags=integration ./...
sh scripts/generate-sql.sh
CGO_ENABLED=0 go build -trimpath -o dist/mihomo-hub ./cmd/mihomo-hub
python3 scripts/smoke-cli.py
node scripts/smoke-xd.mjs ../metacubexd
```

集成测试通过 goleak 检查整个测试进程的 goroutine 残留。sqlc 固定 v1.31.1，重生成校验无变化；前端 Git 工作区保持干净。CLI 烟测覆盖生产命令、PTY 隐藏密码、初始化、Session、readyz、在线备份、拒绝覆盖和 SIGTERM。所有以上命令均实际通过，非仅列出建议。

## 真实三实例只读验证

测试运行在用户授权的 `root@hugo-ecs`，只读读取现有 neko-master 的后端配置，将凭据经私有 stdin 管道送给测试二进制；没有输出或落入仓库。使用临时独立数据目录，退出自动删除，上传的测试二进制也已清理。

| 实例 | 已验证协议版本 | 重启前采样 | MihomoHub 重启后采样 | 独立恢复 gap |
|---|---|---|---|---|
| hugo-mihomo | Mihomo v1.19.33 | 10 | 5 | 是 |
| hugo-mac | Mihomo v1.19.33 | 10 | 5 | 是 |
| hugo-win | Mihomo v1.19.33 | 10 | 5 | 是 |

三台 `/version` 与 `/connections` HTTP 字段检查通过；真实 WS 顶层 uploadTotal/downloadTotal 为累计整型，connections 为列表，连接含 id/metadata/upload/download/start/chains/rule/rulePayload。无可验证稳定启动 ID。测试包含无 Secret 与有 Secret 两种实例。重启后 Writer 10 次提交，队列为 0、丢弃 0、写失败 0，最后一次提交约 10.4ms（短时观测）。

仅订阅连接流并重启自己的 MihomoHub 测试客户端；未停止/重启 Controller，未更改 neko-master 数据、真实配置文件、节点选择或网络连接。真实场景没有模拟内核重启；此类故障用可控 Mock 验证。

可复用工具 `go build ./tests/livetest`，输入是私有 stdin JSON 数组（name/url/secret），仅用于明确授权的目标。它不读取固定生产路径，不在默认 CI 运行，输出仅采样数、恢复状态和 Writer 指标；临时数据库中的网络元数据退出后删除。

## 容量评估

环境：Apple M3、16 GiB、Go 1.27.3 darwin/arm64、modernc SQLite 1.61.1；本地临时 WAL 数据库。场景为 **10 实例 × 每实例 1000 活跃连接 × 每秒快照**，1000 个独立域名，共享节点/规则/进程/来源，每轮持续 11 秒。不是广泛容量承诺。

初轮最大批次约 810ms（CPU profile 复测约 837ms）、总分配约 1076 MiB。profile 显示明显驱动调用相关调度成本；将同一快照内相同维度值合并后复测：

| 指标 | 最终实测 |
|---|---|
| 实际采样时长 | 11.032 秒 |
| 每实例最少已提交快照 | 11 |
| 最大已观测队列长度 | 0（不包括 Writer 已取出的待提交批次） |
| 丢弃 / 写失败 | 0 / 0 |
| 最大已观测批次提交 | 429.5ms |
| 最大已观测 Collector lag | 1608ms（包括距上次快照的自然间隔） |
| 最大已观测 Go heap | 154.5 MiB |
| 测量段累计分配 | 743.2 MiB |
| 主数据库 + WAL | 14.0 MiB，运行结束大小，非物理写入速率 |
| 写连接池等待 | 0ms（不等同于完整 SQLite 锁等待直方图） |
| 编译后测试进程 CPU | user 6.34s、sys 1.36s，wall 12.14s（含测试初始化/退出） |
| 进程最大 RSS | 约 192.2 MiB |

复现：

```sh
go test -c -tags=integration -o test-results/capacity.test ./tests/integration
MIHOMOHUB_CAPACITY=1 /usr/bin/time -l test-results/capacity.test \
  -test.run '^TestCapacity10x1000$' -test.v -test.timeout=90s
```

Linux 的 time 可改用 `-v`。SQLITE_BUSY 由独立竞争写事务测试验证；保留任务由边界/积压测试验证。这轮没有测量物理磁盘写入速率或长时间保留任务与查询混合负载，不把上述 DB 增长或 pool wait 冒充这些指标。临时 CPU profile/截图位于忽略的 test-results。

## 已知限制与 P2 建议

- 恢复候选量不能证明内核未重启，既不加入精确分钟趋势，也不补造逐域名/进程归因；采样漏掉短连接和未观测的最后字节是协议限制。
- 当前保留完整生命周期的最新观测累计值，历史连接查询不支持把字节精确裁到任意时间窗。聚合边界按 UTC 存储桶对齐。
- 保留清理有时间/批次预算，积压可能跨轮追赶；删除数据不自动缩小主文件。长期 soak、混合查询与物理 I/O 指标属于后续容量工作。
- Collector 状态计数主要为内存，重建 worker 后重新计数；持久游标、历史、缺口与聚合会恢复。Session 需重新登录。
- 主动实例 `/test`、管理员恢复、密钥轮换、正式 embed/容器发布未实现。当前为后端阶段交付，不是完整生产发布。
- P2 建议按 Endpoint Store → 服务端历史适配 → 实例筛选/全局视图 → coverage/gap 展示顺序接入 XD，保持查询缓存按实例区分；旧 IndexedDB 不与服务端数据混算。继续保留 P0 页面与 WS 回归测试。
