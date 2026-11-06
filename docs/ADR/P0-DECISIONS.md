# P0 决策细化

依据 DESIGN v1.0.1 与 TECH_STACK v1.0，未改变双仓库、统计语义或阶段边界。

1. HTTP 固定 Gin + 标准库 ReverseProxy；框架仅存在于 transport，Service/Repository 使用 context。Gateway 不解析 Mihomo JSON；仅配置 PATCH 检查顶层权限，并在透明 WS 字节流上累计长度限制，不重写帧。
2. modernc SQLite v1.61.1、goose v3.29.0、sqlc v1.31.1 已实际运行。Gin v1.12.0 最低 Go 1.25，其余核心当前依赖最低 Go 1.26；仓库固定本机当前 Go 1.27.3。版本在 go.mod/go.sum 或生成脚本锁定。
3. P0 使用一个 SQLite 连接串行化短事务；无后台采集 Writer。每条连接通过 DSN 配置 FK/busy_timeout，集成测试临时扩为三条连接验证。P1 的 batch writer/read pool 需按真实负载设计。
4. 管理员密码 Argon2id（64 MiB、3 轮、2 lane、16-byte salt、32-byte hash）；Session 随机 ID，仅其 SHA-256 哈希存内存，重启失效。P0 已实现加密密钥与同源防护，避免基础凭据在等待 P3 期间明文落库。
5. 原版 XD 不提供自定义 CSRF Header。Platform API 使用 Token + Origin/Referer，Gateway 运行时写操作使用严格 Origin/Referer + SameSite Cookie；WS 必须 Origin。固定配置 Origin/Host，不信任 Forwarded Header。无需修改 XD 即可验证。
6. 默认允许内网/Tailscale/loopback，公网显式开启且 HTTPS；端口可配置白名单。所有 DNS 答案检查，拨号重新检查并连接数字 IP，拒绝 metadata 和重定向。P0 只接受 Controller 根 URL。
7. 禁止内核重启、升级、文件配置替换与 Provider 更新；只允许文档列出的运行时控制和配置字段。P0 不增加危险操作放行开关。
8. 删除必须 retain/purge；retain 保留无凭据墓碑且占用名称，purge 物理删除。P1 必须给历史外键/清理补测试，不能静默改变保留语义。
9. 外部静态目录用于同源 P0 验证，Go 提供独立最小登录页；XD 源码不复制进后端。正式 Embed/容器/备份/密钥轮换仍属 P3。默认不宣称静态目录具有可信构建来源。
10. `coder/websocket` v1.8.15 只用于 Fake 与测试客户端，不在服务端生产调用链中。没有提前实现 Collector。前端契约由固定 SHA、原版页面烟测和合成 fixtures 验证；不将合成数据当真实版本采样证据。
