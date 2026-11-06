# P3 验收记录

## 固定版本与交付物

- 后端构建源码提交：`90f3cbc`（完整 SHA 见发布包 manifest）。
- 前端：`0cc6463a9a7fa29122623a8c2d9162de0b090008`；上游基线 `4fd88e621722fc9274f0120c53ee1b13ba2d706e`。
- 版本 `0.3.0`，API v1，SQLite schema 2；Go 1.27.3、Node 24.19.0、pnpm 10.34.1。
- `dist/` 已生成 Linux amd64、Linux arm64、macOS arm64 的 `.tar.gz`，每包包含内嵌 UI 的二进制、manifest、配置、运维文档、CycloneDX 清单、CA 根证书与第三方许可证。哈希见同目录 `SHA256SUMS`，全部通过校验。
- 发布目录可直接用 `Dockerfile.release` 组装 scratch 镜像，无需在部署机编译或下载基础镜像；脚本核对二进制/CA 的 SHA256，并从 manifest 设置版本与平台。另保留 Go 多阶段源码 Dockerfile，基础镜像固定摘要，适合 CI 构建机。

## 功能与边界

`frontend` 构建标签内嵌完整 Nuxt 资源（包含 `_nuxt`），无标签开发测试不要求 dist。HTML/config 不长期缓存，哈希资源 immutable；缺失资源/API 不回退 HTML。`/sw.js` 退役旧 Worker；登录页随二进制提供。补充测试修复了 Gin NoRoute 下静态成功响应仍为 404 的问题。

版本 API/CLI 提供实际两个仓库 SHA、schema、构建时间和内嵌标志。发布脚本验证前端 SHA、干净构建输入、Node/pnpm/frozen-lockfile，过滤 UI 构建环境覆盖项；使用固定时间戳生成包。CA 来自固定 Node 版本随版 Mozilla 根证书，manifest 记录来源与校验和；运行时没有 Node。

安全验证覆盖 Session 生命周期与撤销、HttpOnly/SameSite/Secure Cookie、WS Origin、CSRF、DNS/SSRF/端口/重定向、方法/配置白名单、消息和请求大小上限、Secret 加密与日志脱敏。公网 Controller 的实际拨号强制 TLS，并验证证书。Gateway 附加 CSP sandbox，阻止上游 HTML 在平台 Origin 执行脚本，保留原生状态、类型与正文。这是本次实现范围内的边界检查，不等同于独立全量渗透审计。

`POST /instances/{id}/test` 已实现认证、CSRF 和 generation 取消，只读取 `/version`，不改变 Controller 或采集状态。systemd、Compose、HTTPS/Tailscale、权限、在线备份、恢复与拒绝降级的操作见 Runbook。

## 已执行且通过

| 验证 | 结果 |
|---|---|
| 前端 typecheck、ESLint、格式检查 | 通过 |
| 前端全部单元测试 | 61 文件，687 项通过 |
| Go 单元/集成、`-race -tags=integration,frontend`、vet | 通过，含 goleak、事务故障、认证和协议回归 |
| sqlc 重新生成一致性 | 通过，生成目录无差异 |
| 内嵌 UI Chromium | CRUD/刷新、retain/purge、SQLite 单/全实例历史、五维 TOP、移动端、失败重试、会话失效/注销、XD 运行页及 4 WS 均通过 |
| 实际 macOS 发布二进制 CLI | 隐藏密码、0600 密钥/备份、版本/API 对照、内嵌 UI、拒绝覆盖、SIGTERM、快照恢复后重新登录与 healthcheck 通过 |
| Linux 两种架构 | CGO=0 静态 ELF 构建通过；两种镜像均在本地 Docker 验收 |
| 本地 arm64 容器 | 原生执行；非 root 65532、只读 rootfs、移除能力、交互初始化、登录、实例重启持久化、在线备份、无 Node 均通过 |
| 本地 amd64 容器 | Docker Desktop 兼容层执行，同一组验收通过；不等同于原生 amd64 性能测试 |
| 相同输入重复打包 | 三个平台压缩包 SHA256 两次完全一致 |
| Compose 配置 | `docker compose config --quiet` 通过 |
| 真实三实例只读联调 | 每台 Hub 重启前 10 样本、重启后 5 样本，恢复 gap=true，丢样/写入失败均 0；临时历史自动移除 |

真实实例联调仅读取 `/connections`。没有对真实 Controller 执行配置修改、内核控制或进程重启。

## 本地容量测量

10 实例 × 1000 活跃连接 × 约 1Hz，持续 60 秒，同时每秒执行 summary/TOP/connections 查询：每实例最少提交 60 样本，丢样 0、写入失败 0、写池等待 0ms、最大查询 46ms、最大事务 702.1ms、最高 lag 1826ms、峰值 Go heap 154.8MiB、DB+WAL 20.5MiB。此压测在本地执行，属于短时容量评估，不是长期吞吐保证。

曾在 hugo-ecs 尝试源码镜像编译与隔离容器验收，期间机器发生死机/重启；未找到可确认原因的 OOM/panic 记录。用户要求后已停止远端操作，最终容器与后续测试均在本地 Docker 完成。远端隔离测试容器和卷已由脚本清理；为遵守停止远端操作的要求，未进一步清理 `/tmp/mihomohub-p3-build`、临时联调二进制和构建镜像/缓存。不要在这台低内存服务机继续做源码编译或压测。

## 已知限制与下一步

单管理员、单机 SQLite；Session 重启失效；无在线主密钥轮换/密码重置；统计、短连接、旧浏览器历史和跨维度钻取限制见 P1/P2。首版没有自动迁移回退，降级必须配套恢复旧快照与密钥。

代码只做本地提交，未推送仓库、发布 GitHub Release 或推送镜像。跨仓库 CI 已实现，但本次未触发 GitHub Actions；远端 checkout 需要先同步固定的前端提交。项目自身许可证仍需所有者决定，第三方许可已随包保留。

建议下一阶段先在本地做长时间稳定性验证与上游同步演练，再基于实际使用量考虑历史查询性能、联合维度数据模型和密钥轮换；不要把采样归因标成精确账单。
