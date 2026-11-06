# P3 验收记录

## 实现

- `frontend` 构建标签内嵌锁定前端的完整静态资源（包括 `_nuxt`）；无标签源码测试无需生成 dist。
- 静态路径/HEAD/MIME/缓存/API 隔离测试，旧 Worker 退役脚本；登录页、Cookie/Origin、Session、CSRF、地址策略、方法白名单与 Secret 加密。
- 公网 Controller 的实际拨号强制 TLS，DNS 重绑定检查、证书验证和只读 `/test` 探测。
- 严格 SHA/Node/pnpm/frozen-lockfile 前端构建、真实版本元信息、无 CGO Linux 双架构发布包、SHA256SUMS、CycloneDX 清单与第三方许可证。
- Go 多阶段构建、固定基础镜像摘要、scratch 非 root 运行、只读根文件系统 Compose、内置 healthcheck；systemd/HTTPS/Tailscale/备份恢复 Runbook。
- CI 执行前后端测试、内嵌浏览器验收、Linux 双架构和 OCI 容器构建，仅上传候选产物，不自动发布。

## 已通过

- `go test -tags=integration,frontend ./...`、竞态检查、`go vet`。
- 内嵌模式 Chromium 全流程：`MIHOMOHUB_EMBED_SMOKE=1 node scripts/smoke-hub.mjs`。
- CLI 交互密码不回显、0600 密钥/备份、拒绝覆盖、SIGTERM、快照恢复后重新登录和 healthcheck。
- 60 秒混合负载：10 × 1000 连接、约 1Hz、每秒 3 种历史查询；最少每实例 60 次提交，丢样 0、写入失败 0、写池等待 0ms、最大查询 46ms、最大事务 702.1ms、最高 lag 1826ms、峰值 Go heap 154.8MiB、DB+WAL 20.5MiB。属于短时本机容量测量，不是长期吞吐保证。

## 待最终记录

发布包的实际 SHA、双架构产物、隔离容器运行和真实三实例只读复验将在完整验证后补充。

## 限制

单管理员、单机 SQLite；Session 重启失效；没有在线主密钥轮换/密码重置；首次发布需项目所有者选择本项目许可证。前端提交目前仅在本地，CI 的跨仓库 checkout 需其已同步到 Fork。没有部署或重启真实 Controller、没有修改 neko-master 服务。统计与浏览器迁移限制见 P1/P2 验收。
