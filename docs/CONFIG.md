# 配置契约

配置优先级：内置默认值 → `--config <YAML>` → 下列显式环境变量。无配置文件也可启动；不自动读取 `.env`，未知 YAML 字段和多文档输入会报错。配置变更需重启。

| YAML 键 | 默认值 | 环境变量/说明 |
|---|---|---|
| `server.listen` | `127.0.0.1:11235` | `MIHOMOHUB_LISTEN` |
| `server.public_origin` | `http://127.0.0.1:11235` | `MIHOMOHUB_PUBLIC_ORIGIN`；仅 Origin，不带路径或末尾斜杠 |
| `server.static_dir` | 空 | `MIHOMOHUB_STATIC_DIR`；开发用外部 Hub 静态目录；空值使用内嵌 UI（源码构建则显示开发说明） |
| `database.path` | `data/mihomo-hub.db` | `MIHOMOHUB_DATABASE_PATH` |
| `database.busy_timeout_ms` | `5000` | 1–60000；通过 DSN 对每条物理连接设置 |
| `security.master_key_file` | `data/secrets/master.key` | `MIHOMOHUB_MASTER_KEY_FILE`；32 原始字节，普通文件、权限 0600 |
| `security.session_hours` | `12` | 1–168；进程重启撤销所有 Session |
| `gateway.allow_public_controllers` | `false` | 显式开启后公网 Controller 仍必须 HTTPS |
| `gateway.allowed_ports` | `[]` | 空列表允许任意合法 TCP 端口；仍受地址策略限制 |
| `gateway.allow_runtime_writes` | `true` | false 时禁止 Gateway 写操作及 YAML 导入/DNS 应用；实例管理仍可用 |
| `gateway.request_timeout_seconds` | `30` | 1–300；普通 REST 总时限 |
| `gateway.maintenance_timeout_seconds` | `180` | 1–600；重载/重启、Provider/Geo 更新、缓存清理和 DNS 热加载总时限 |
| `gateway.max_body_bytes` | `1048576` | 1–64 MiB；Gateway 和实例请求上限 |
| `gateway.max_response_bytes` | `16777216` | 1–64 MiB；REST 有界缓冲，超限返回 502 |
| `gateway.max_ws_message_bytes` | `16777216` | 1–64 MiB；双向限制，分片累计，超限断开 |
| `gateway.max_websockets` | `128` | 1–4096；包括正在握手的连接，满额返回 503 |
| `log.level` | `info` | `MIHOMOHUB_LOG_LEVEL`；debug/info/warn/error，始终 JSON |

只有表中列出的 `MIHOMOHUB_*` 环境变量会被读取。相对路径以进程工作目录解析。密码与主密钥内容不支持命令行或环境变量传入。

网络默认允许 loopback、RFC1918、IPv6 ULA 和 Tailscale `100.64.0.0/10`。始终拒绝云 metadata（包括 `169.254.0.0/16`、`100.100.100.200`、`fd00:ec2::254`）、未指定/多播等目标；DNS 的所有答案必须满足策略，拨号时再次解析检查并直接连接已验证 IP。HTTP_PROXY 等代理环境变量不作用于 Controller 连接。默认 CA 校验始终开启，无跳过 TLS 校验选项。

监听非 loopback 地址须明确配置 `server.listen`。反向代理必须保留外部 `Host`，将 `server.public_origin` 设置为浏览器实际 HTTPS Origin，外部必须使用 TLS；Cookie 的 Secure 由该配置决定。Gin 不信任任何 `X-Forwarded-*`，限流使用物理连接 IP（反向代理后的用户共用其限流桶）；P0 暂不提供可信代理 CIDR 配置。

服务端 `ReadHeaderTimeout=5s`、`ReadTimeout=30s`、`IdleTimeout=60s`、`MaxHeaderBytes=64KiB`。平台普通响应写截止时间 35 秒；Gateway 使用对应请求总时限加 5 秒，DNS 应用使用维护总时限加 5 秒。上游响应头限时取两类时限的较大值，普通请求仍由自己的 context 限制。WS 不采用普通 REST 总时限，空闲日志流可持续到 Session 过期，慢浏览器单次写操作最多等待 15 秒。Controller 连接建立与 TLS 握手各限 5 秒，每目标最大 32 条 HTTP/WS 连接。

## P1 采集、写入与保留

以下均使用 YAML 配置，不增加隐式环境变量映射。

| YAML 键 | 默认值 | 限制/含义 |
|---|---|---|
| `collector.max_instances` | 32 | 1..256；超出显示 capacity_limited，释放名额后重试 |
| `collector.max_connections` | 10000 | 1..100000；单快照上限，超限不接受部分列表 |
| `collector.queue_capacity` | 64 | 1..4096；有界待写任务队列 |
| `collector.batch_size` | 32 | 1..256；批量阈值，每个实例等待事务 ack 后推进 |
| `collector.flush_interval_ms` | 250 | 1..5000；即使未满批次也提交 |
| `collector.snapshot_timeout_seconds` | 15 | 1..300；等待下一 /connections 快照的时限 |
| `collector.max_snapshot_gap_ms` | 5000 | 100..300000；超过时不把恢复增长分配进分钟桶 |
| `collector.reconnect_min_ms` | 1000 | ≥1；指数退避起点 |
| `collector.reconnect_max_ms` | 60000 | ≥min、≤300000；另加至多 25% jitter |
| `retention.connections_days` | 14 | 1..3650；只清理已关闭摘要，活跃/未结束记录保留 |
| `retention.minute_stats_days` | 60 | 1..3650；分钟总量和五维 |
| `retention.hourly_stats_days` | 365 | 1..3650，至少覆盖 minute；小时总量、五维和 gap |

Collector 使用与 Gateway 相同的地址策略、TLS 和 `max_ws_message_bytes`，另建独立长连接，不消耗浏览器 Gateway 的 `max_websockets` 配额。建连 5 秒；正常 Mihomo 默认约 1 秒推送一次 /connections，采样间隔不是客户端强制的计时器。阈值应覆盖正常推送与 Writer 排队时间。

写池单连接；Analytics 另有最多 4 条只读连接，DSN 逐连接设置 readonly/query_only/FK/busy_timeout。每批事务默认最多 10 秒，SQLite BUSY 最多 3 次有界尝试；Writer 收到提交结果后才 ack。退出先取消并 join Collector，再在全局 10 秒预算内排空 Writer。

维护每分钟一次，在 5 秒预算内按每表每批至多 1000 行、每轮至多 20 批清理，然后被动 checkpoint。截止值以前才删除，边界记录保留。清理可能逐轮追赶积压；实际保留边界通过 Analytics 的 first_retained_* 返回。清理释放可复用页，不自动 VACUUM 压缩主文件。

接入实例即启用后台采集；Schema 4 已将未删除的存量实例统一启用管理与采集。

Schema 6 移除无业务读取的 `collector_state`、`instance_epochs`，以及 `sample_id`、`enabled`、`collect_enabled` 冗余列。未删除实例统一参与管理与采集，旧 API 的启用字段继续兼容；采集状态由内存提供，恢复基线仍由持久化游标保存，周期边界和原因保存在连接、缺口数据中。分钟和小时统计按同一份增量在同一事务内提交，不再每批扫描分钟历史重建小时数据。

## 发布与容器

`version` 输出版本、后端 Commit、前端 Commit、schema、构建时间与内嵌标志，不需要读取主密钥或初始化数据库。`healthcheck` 在 2 秒内访问配置监听地址的本地 `/readyz`；容器用它探活，无需 curl、shell 或 Node。

主服务容器默认 UID/GID 为 65532，数据目录 `/var/lib/mihomo-hub` 必须可写且归该用户；根文件系统可只读。Compose 的 `init-data` 一次性容器自动创建宿主机挂载目录，并将目录本身设为 `65532:65532`、0700，完成后主服务才启动；不递归修改已有文件。Named volume 首次挂载继承镜像目录权限。直接使用 `docker run` 挂载宿主机目录时，需自行准备目录权限。镜像将 CA 证书内置，HTTPS Controller 使用系统信任链，不提供跳过 TLS 校验选项。

`server.public_origin` 决定 Cookie Secure 属性与 Origin 校验，应与浏览器实际地址一致。HTTPS 反向代理保留 Host、Origin、Cookie，支持四条 WS 的 Upgrade；程序不信任 X-Forwarded-* 推导安全来源。只支持独立 Origin 根路径，不支持将 Hub 挂在任意 URL 子路径。
