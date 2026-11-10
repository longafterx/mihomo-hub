# 六项缺陷修复验收（2026-11-10）

所有验证使用本地临时数据库、隔离 Controller 夹具和 Docker Desktop，不连接远程服务器。前端锁定 `1bc7f58648a9a8e31f8649106ae26f1b752042cf`，数据库 schema 保持 5。

| 问题 | 修复及验证 |
| --- | --- |
| 改名、同值或失败保存打断连接 | 先持久化再发布连接变更；只有 URL/Secret 实际变化才递增 generation。集成测试验证改名、同值、重名冲突、数据库失败后 WS 继续接收，采集不重启；删除、密钥变更仍撤销连接。 |
| 跨标签页重新登录后写入 403 | 仅 `csrf_failed` 刷新会话并重发一次，合并并发刷新。4 项单测覆盖恢复、非 CSRF 拒绝、存储失败及重试上限；浏览器验证旧标签页无需刷新即可恢复。 |
| 界面无法新增 HTTPS Controller | 只填 IP/主机与端口，后端先无凭据探测 TLS。HTTP、受信 HTTPS、错误证书不降级、不发送凭据及错误密钥均有测试；浏览器验证自动新增受信 HTTPS。 |
| 超过 256 个历史身份导致全局查询失败 | 256 上限仅针对显式选择的 ID。测试建立 257 个墓碑及 2 个活动实例，验证六类全局查询以及最后一个墓碑的数据参与汇总。 |
| retain 后没有永久清理入口 | 管理页新增“已保留的历史”，按旧 ID 清理并确认不可撤销；API 认证、重复清理 404、审计失败回滚、同名新实例隔离均有集成测试。浏览器覆盖桌面/手机的确认、取消和清理。 |
| Compose 改端口后 Origin 不匹配 | 默认 Origin 随 `MIHOMOHUB_PORT` 变化；显式 Origin 优先。只读校验默认端口、自定义端口和 HTTPS Origin 三种配置。 |

验证结果：前端 69 个测试文件、740 项单测通过，类型检查及改动文件检查通过；Go 全量 `-race -tags=integration,frontend`、`go vet` 通过，sqlc 重新生成无额外差异。正式内嵌前端的 Hub、导航及六项回归浏览器脚本通过。

可复现命令（Go 与前端依赖需先准备好）：

```sh
go test -race -tags=integration,frontend ./... -timeout=180s
go vet -tags=integration,frontend ./...
sh scripts/generate-sql.sh
python3 scripts/check-compose.py
MIHOMOHUB_EMBED_SMOKE=1 node scripts/smoke-hub.mjs ../metacubexd
MIHOMOHUB_EMBED_SMOKE=1 node scripts/smoke-navigation.mjs ../metacubexd
MIHOMOHUB_EMBED_SMOKE=1 node scripts/smoke-regressions.mjs ../metacubexd
DOCKER_CONTEXT=desktop-linux env -u DOCKER_HOST python3 scripts/smoke-container.py
```

HTTPS 使用系统受信 CA；自签名证书仍会报 TLS 错误。只有明确收到 HTTP 响应才回退内网明文，握手中断或未知协议不会尝试发送 Secret。全局历史查询仍有时间范围、点数、分页及超时限制；永久清理历史不能撤销。浏览器回归中的受信证书仅注入本地测试进程，不改变正式应用的信任配置。
