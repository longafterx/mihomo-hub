# ADR-009：Controller 维护操作与完整 YAML/DNS 热加载

状态：采用。用户于 2026-11-07 明确要求增加重载、重启、Provider/Geo 更新、缓存清理、端口/LAN/DNS/TUN，并确认 DNS 使用完整 YAML 导入方案。本决策扩展 P0 的写操作白名单，不改变双仓库、Collector、Secret 隔离或 Agent 边界。

## 原生维护接口

Go Gateway 继续保持原生方法、路径、查询、响应码和正文。仅新增明确列出的 `POST /restart`、`POST /configs/geo`、两个 cache flush、`PUT /providers/{proxies|rules}/{name}`、重载 `PUT /configs` 和端口/LAN/接口/TUN PATCH 字段。普通重载不允许指定其他文件或任意 payload；升级、任意文件访问、Controller 密钥/监听修改仍拒绝。

统一单管理员权限、Session、Origin/Referer、限流、审计及地址策略；维护请求使用独立的有界超时（默认 180 秒），覆盖浏览器、Go context、响应写截止时间和上游响应头时限。修改请求不自动重试，网络中断报告“结果未确认”。目标 TUN 需要设备内核和权限支持，Hub 不安装或提权。

## DNS 为什么需要完整配置

已核对 Mihomo `hub/route/configs.go`：`PATCH /configs` 的 configSchema 包含端口、allow-lan 和 tun，未包含 dns；`GET /configs` 只提供 General。仅放行 DNS PATCH 会让内核忽略字段，却返回 204。不能根据 General 拼装完整配置，否则可能清空代理、规则和其他未返回内容。

采用独立平台接口管理管理员导入的完整 YAML。导入仅保存、不操作设备；应用时仅合并 DNS 表单拥有的七个字段，保留其他 DNS 策略与配置，通过 Controller `PUT /configs?force=true` 加载完整 payload。UI 明示这会应用基准中的全部代理、规则和端口。全量配置只有内核接受并返回 204 后才替换已保存基准；内核失败则保留原基准。

不提供配置订阅、远程文件写入、Agent 或 systemd 管理。内核重启/重载会重新读取设备文件；外部配置变动后需重新导入。`imported` 只表示基准已保存，不表示当前内核配置一致。平台不能从原生 API 获取完整 YAML 或确认所有 DNS 字段的当前值。

## 持久化和一致性

Schema 5 的 runtime_configs 使用 master key 的 AEAD 加密整份 YAML，AAD 为 `runtime-config:<instanceID>`，不能跨实例或与 Secret 密文互换。接口只返回 DNS 可编辑字段和元数据，不返回完整 YAML；不写浏览器存储、通用日志或审计正文。数据库备份包含密文，主密钥单独备份。

导入/应用按实例使用固定数量分片锁串行化，避免删除实例后遗留无界锁表。revision 防止多页/并发覆盖；SQLite 事务同时校验 revision、当前 Controller URL、实例未删除并写入审计。URL 更换使旧基准失效；删除即使保留历史也清除配置密文。

Controller 与 SQLite 不能组成一个事务：内核接受配置后若本地保存失败，返回明确的 `runtime_config_save_failed`，要求核对状态并重新导入，绝不假装回滚内核。操作被超时/注销/实例变更取消后可能已在内核执行，提示先检查，不自动重试。

## 兼容验证

协议测试使用双 Fake Controller、真实路由和临时 SQLite。真实功能另用本地 Mihomo v1.19.21 容器验证；Provider、DNS 和 Geo 的来源全部为本地合成服务/镜像自带数据库，不连接用户实例。安装/升级和任意 payload 透传不在此次授权范围内。
