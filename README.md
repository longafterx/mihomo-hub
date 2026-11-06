# MihomoHub

基于 MetaCubeXD 的多实例 Mihomo 控制、监控与历史流量平台。P0–P3 已实现：同源 REST/WebSocket Gateway、服务端认证、后台采集与 SQLite 历史、实例管理与分析页面、内嵌前端单二进制及 Docker 构建。

前端独立保留在 [MetaCubeXD Fork](https://github.com/longafterx/metacubexd)，固定版本见 [build/frontend.lock.yaml](build/frontend.lock.yaml)。Go 仓库只接收静态产物。运行时无需 Node、pnpm 或外部数据库。

## 从源码构建与启动

需要 Go 1.27.3、Node 24.19.0、pnpm 10.34.1、Python 3；两个仓库并列 checkout，前端 HEAD 必须与锁文件一致且构建输入干净。

```sh
sh scripts/build-frontend.sh ../metacubexd
CGO_ENABLED=0 go build -trimpath -tags=frontend -o dist/mihomo-hub ./cmd/mihomo-hub
cp config.example.yaml config.local.yaml
./dist/mihomo-hub init --config config.local.yaml
./dist/mihomo-hub serve --config config.local.yaml
```

打开 `http://127.0.0.1:8080/login`，在实例管理页面添加 Controller，选择启用采集。全局选择器切换运行实例；历史页面可查单实例或全部实例。`init` 通过交互终端隐藏输入密码（至少 8 字节），不接受密码参数或环境变量，不覆盖已有账号。默认仅监听本机。

正式版本包使用下面的脚本写入真实版本元信息、生成 Linux amd64/arm64 包、SHA256SUMS、CycloneDX 依赖清单和第三方许可证：

```sh
sh scripts/build-release.sh --frontend ../metacubexd --version 0.3.0
cd dist
sha256sum -c SHA256SUMS
```

后端构建输入必须已提交；没有内嵌资源的普通 `go build` 是后端开发模式。`server.static_dir` 仅用于开发覆盖内嵌资源。`version` 显示内嵌状态、两个仓库 Commit 与 schema；正式包带可核对的 manifest。

## Docker Compose

先完成上面的前端/发布构建，再封装对应宿主架构的发布目录（arm64 主机改为 linux_arm64）：

```sh
sh scripts/build-image.sh dist/mihomo-hub_0.3.0_linux_amd64
docker compose run --rm -it mihomo-hub init
docker compose up -d
docker compose ps
```

`sh scripts/build-image.sh` 不带目录时使用源码多阶段构建，适合 CI 或内存充足的构建机。低内存服务器推荐直接封装已验证的发布目录。

Compose 将端口绑定 `127.0.0.1:8080`，UID/GID 65532，根文件系统只读，数据在 named volume。镜像为 scratch + Go 可执行文件 + CA/许可证，没有 Node/shell。内置 `healthcheck` 查询 `/readyz`。更改访问入口需设置 `MIHOMOHUB_PUBLIC_ORIGIN` 后重新创建容器。

使用 Tailscale HTTPS 入口时，按本机 Tailscale 版本配置 `tailscale serve` 反向代理到 `http://127.0.0.1:8080`，并将 public origin 设为对应的 `https://设备名.tailnet.ts.net`；用 Tailnet ACL 限制访问。Nginx/Caddy 同样支持，必须保留原 Host/Origin/Cookie，并转发 WebSocket Upgrade。具体见 [Runbook](docs/RUNBOOK.md)；不要直接公开未受限的 Controller。

## 数据与安全

Controller Secret 用 AES-256-GCM 加密存储，浏览器 Endpoint 的 Secret 始终为空。登录 Session、Cookie/Origin、平台 CSRF、受限的 Controller 地址策略、DNS 拨号检查与 Gateway 方法白名单共同构成访问边界。单管理员模式，Session 随服务重启失效。Agent `/api/control/*` 返回 404；不管理远程 Mihomo 进程，不允许升级、重启和配置替换。

主密钥须独立备份。在线数据库备份使用 `backup --output <新文件>`，不要运行中直接复制主 `.db`。恢复、迁移、降级拒绝与 systemd 部署见 [Runbook](docs/RUNBOOK.md)。域名、进程和源 IP 属于敏感历史，默认连接 14 天、分钟 60 天、小时 365 天；删除实例可明确保留或永久清理历史。

实例总量是观测到的累计计数差分；域名/代理/规则/进程/源 IP 是周期快照的采样归因。短连接可能漏采，时间覆盖率不代表归因准确率。断线恢复的候选增量独立展示，不分配到分钟或总量。无观测桶为空白；连接摘要为生命周期量，不是窗口流量。独立 TOP 暂不支持跨维度钻取。旧浏览器历史不导入服务端。

## 验证与维护

```sh
go test ./...
go test -race -tags=integration,frontend ./... -timeout=180s
go vet -tags=integration,frontend ./...
sh scripts/generate-sql.sh
python3 scripts/smoke-cli.py
MIHOMOHUB_EMBED_SMOKE=1 node scripts/smoke-hub.mjs ../metacubexd
python3 scripts/smoke-container.py
```

前端执行 `pnpm --filter @metacubexd/ui typecheck` 与 `test:unit`。CI 校验 sqlc 生成一致性、竞态、内嵌 UI 浏览器流程、Linux 双架构与容器，不自动推送镜像或发布 Release。

[API](docs/API.md) · [配置](docs/CONFIG.md) · [运维](docs/RUNBOOK.md) · [P0 验收](docs/P0-VERIFICATION.md) · [P1 验收](docs/P1-VERIFICATION.md) · [P2 验收](docs/P2-VERIFICATION.md) · [P3 验收](docs/P3-VERIFICATION.md) · [第三方许可](docs/THIRD-PARTY-NOTICES.md)
