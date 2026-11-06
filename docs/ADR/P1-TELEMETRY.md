# P1：采集、恢复与历史查询决策

状态：已实现；依据 DESIGN 第 5、7、8、9、12 节及 TECH_STACK。继续使用 Gin、coder/websocket、modernc SQLite、sqlc、goose、slog，无额外服务或前端改动。

1. **恢复候选量和 epoch。** 三台真实 Mihomo v1.19.33 均提供 /connections 顶层累计量，但没有可验证的稳定内核启动 ID。因此仅在连续、未超时的采样区间计精确总量差分；计数回退或 Controller 来源改变才创建新 epoch。WS 重连和 Go 重启恢复旧游标，单调差额写 `uncertain_epoch_and_time` 的 candidate 字段，不进入分钟/小时总量。这比假定单调就证明内核未重启更保守，符合设计要求；无法恢复期间精确时间和逐连接归因。
2. **generation。** 实例配置 generation 持久化，任务由注册中心取消，Manager 等待旧任务退出再启新任务，SQLite 提交再次验证 generation/开关。配置写失败后重新发布此前配置，Manager 也重建已取消任务。generation 不充当内核 epoch。
3. **连接生命周期。** 主键为 instance_id + 独立 lifecycle UUID，另存 epoch 和 Mihomo ID；观察间隙、计数回退或 start 变化结束旧观测生命周期，防止 ID 复用覆盖。只更新活跃摘要、关闭后保留末次观测值，不保存每帧原始快照。维度固定于首次观测；proxy=chains[0]，不是整条链重复计数。
4. **事务与背压。** Engine.Plan 生成新的不可变候选状态，单 Writer 事务提交聚合、连接、gap、cursor/state 后才 ack；失败不推进内存。旧游标时间和旧 generation 拒绝重复。队列有界，入队失败使采集会话关闭，重连后记录恢复缺口。每个实例最多一个在途待确认快照。SQLite STRICT 拒绝 INTEGER 溢出转 REAL；跨行 SUM 溢出也使事务失败。
5. **聚合。** 总量记观测增量的 UTC 分钟；观测覆盖时长跨分钟拆分。小时由受影响小时内的分钟幂等重算覆盖。恢复候选量仅保存在 gap。同快照中相同维度值先合并，降低大量连接共享节点/规则时的冗余 SQL 执行。
6. **查询精度。** 只读池和快照事务保证一份响应的指标/游标一致；显式 UTC 对齐边界避免假称能裁出桶内精确值。连接/gap 任意边界的 coverage 仅计完整桶，作为保守下界。窗口完整时长作为每实例分母，注册前/禁用/已清理时段都会降低覆盖；它不是归因准确率。归因超过总量保留原始值，unattributed=null，不裁剪。
7. **历史权限与删除。** 单管理员可访问 retain 墓碑的历史；API all 包含这些实例。purge 外键级联删除全部历史，retain 清除 URL/密文但保留 ID/名称。停采或删除时结束开放的观测摘要，关闭时间取末次观测，非声称实际网络关闭。
8. **维护。** 短事务清理、全局时间和批次数预算、被动 checkpoint。VACUUM INTO 创建包含 WAL 的一致性备份，0600、拒绝覆盖。恢复在新目录进行并单独配置备份主密钥；没有自动覆盖生产库或自动降级。
9. **运维边界。** Collector 状态/错误码、队列/丢弃/写失败/延迟可通过认证诊断接口查询；无公开 pprof、Prometheus 或第三方遥测。采集显式 opt-in，默认不扩大 P0 实例的隐私采集范围。长期 soak、精细运行指标、正式发布与 UI 接入继续留在 P2/P3。
