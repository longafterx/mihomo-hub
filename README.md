# MihomoHub

基于 MetaCubeXD 的多实例 Mihomo 控制、监控与历史流量平台。P0–P3 已实现：同源 REST/WebSocket Gateway、服务端认证、后台采集与 SQLite 历史、实例管理与分析页面、内嵌前端单二进制及 Docker 构建。

前端独立保留在 [MetaCubeXD Fork](https://github.com/longafterx/metacubexd)，固定版本见 [build/frontend.lock.yaml](build/frontend.lock.yaml)。Go 仓库只接收静态产物。运行时无需 Node、pnpm 或外部数据库。

## 从源码构建与启动

需要 Go 1.27.3、Node 24.19.0、pnpm 10.34.1、Python 3；两个仓库并列 checkout，前端 HEAD 必须与锁文件一致且构建输入干净。

```sh
sh scripts/build-frontend.sh ../metacubexd
CGO_ENABLED=0 go build -trimpath -tags=frontend -o dist/mihomo-hub ./cmd/mihomo-hub
cp config.example.yaml config.local.yaml
./dist/mihomo-hub serve --config config.local.yaml
```

直接打开 `http://127.0.0.1:8080`。空数据首次启动会自动创建数据库和主密钥，网页引导设置 Hub 登录密码（至少 8 字节），设置成功后自动进入控制台；无需执行初始化命令。以后仅输入密码登录，无需用户名。已有部署保留原密码、主密钥、实例和历史，不会再次进入设置流程。默认仅监听本机。

添加 Controller 后自动由 Hub 管理并持续采集。顶部下拉统一切换实例，状态标签仅展示，悬浮显示 Hub 到 Controller 的 HTTP 连接测试耗时。切换保持当前页面并刷新对应实例的数据，其他实例继续采集。用量页面沿用 XD 的维度切换、统计卡片、排行、趋势与可搜索排序的详情表，默认跟随当前实例，也可筛选全部或保留历史；覆盖率和采集缺口收在采集详情中。

实例页以列表为入口，新增/编辑使用独立弹窗。地址只需填写 IP/主机和端口，新增自动使用 HTTP，编辑保留已有协议；密钥明确选择保留、替换或移除。连接错误区分拒绝连接、超时、鉴权、TLS 和响应格式。新增先测试连接，失败不创建。接入后管理与采集为固定行为，不提供启停开关；需要停止时删除实例。删除时可保留历史并释放名称，同名重建按新 UUID 独立记录。Schema 4 自动启用所有未删除的存量实例及采集，保留原密码、UUID、密钥和历史；已删除墓碑不恢复，升级前请备份。

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
docker compose up -d
docker compose ps
```

`sh scripts/build-image.sh` 不带目录时使用源码多阶段构建，适合 CI 或内存充足的构建机。低内存服务器推荐直接封装已验证的发布目录。

Compose 将端口绑定 `127.0.0.1:8080`，UID/GID 65532，根文件系统只读，数据在 named volume。启动后打开网页设置首次登录密码即可。镜像为 scratch + Go 可执行文件 + CA/许可证，没有 Node/shell。内置 `healthcheck` 查询 `/readyz`；待设置密码时同样可就绪，但管理 API 和 Gateway 保持认证。更改访问入口需设置 `MIHOMOHUB_PUBLIC_ORIGIN` 后重新创建容器。

使用 Tailscale HTTPS 入口时，按本机 Tailscale 版本配置 `tailscale serve` 反向代理到 `http://127.0.0.1:8080`，并将 public origin 设为对应的 `https://设备名.tailnet.ts.net`；用 Tailnet ACL 限制访问。Nginx/Caddy 同样支持，必须保留原 Host/Origin/Cookie，并转发 WebSocket Upgrade。具体见 [Runbook](docs/RUNBOOK.md)；不要直接公开未受限的 Controller。

## 数据与安全

Controller Secret 用 AES-256-GCM 加密存储，浏览器 Endpoint 的 Secret 始终为空。登录 Session、Cookie/Origin、平台 CSRF、受限的 Controller 地址策略、DNS 拨号检查与 Gateway 方法白名单共同构成访问边界。单管理员模式，Session 随服务重启失效。Agent `/api/control/*` 返回 404；不安装、升级或通过 systemd/SSH 管理远程进程。

已支持通过 Controller 重载当前配置、重启 Mihomo、更新代理/规则 Provider、清理 DNS/Fake-IP 缓存、更新 Geo，以及修改运行端口、允许局域网、出口接口和 TUN。维护操作默认限时 180 秒，不自动重试；内核能力、下载来源和设备权限不足时明确报错。修改端口/TUN 是目标设备上的操作，Hub 本身无需额外网络权限。

DNS 配置使用“设置 → 导入完整 YAML → 应用 DNS 并热加载”：Mihomo 原生 `PATCH /configs` 不支持 DNS 修改。导入仅保存完整配置，应用时合并 DNS 并加载整份 YAML，包含其中的代理、规则和端口。YAML 在服务端加密，删除实例时一并清除；不返回完整 YAML、不写入远程文件，重载/重启后恢复设备文件中的配置。外部修改配置后需重新导入。Schema 5 新增加密配置表，升级前请备份；回退需同时恢复旧数据库。决策详见 [ADR-009](docs/ADR/009-controller-maintenance.md)。

主密钥须独立备份。在线数据库备份使用 `backup --output <新文件>`，不要运行中直接复制主 `.db`。恢复、迁移、降级拒绝与 systemd 部署见 [Runbook](docs/RUNBOOK.md)。域名、进程和源 IP 属于敏感历史，默认连接 14 天、分钟 60 天、小时 365 天；删除实例可明确保留或永久清理历史。

实例总量是观测到的累计计数差分；域名/代理/规则/进程/源 IP 是周期快照的采样归因。短连接可能漏采，时间覆盖率不代表归因准确率。断线恢复的候选增量独立展示，不分配到分钟或总量。无观测桶为空白；连接摘要为生命周期量，不是窗口流量。独立 TOP 暂不支持跨维度钻取。旧浏览器历史不导入服务端。

## 验证与维护

```sh
go test ./...
go test -race -tags=integration,frontend ./... -timeout=180s
go vet -tags=integration,frontend ./...
sh scripts/generate-sql.sh
python3 scripts/smoke-cli.py
MIHOMOHUB_EMBED_SMOKE=1 node scripts/smoke-setup.mjs ../metacubexd
MIHOMOHUB_EMBED_SMOKE=1 node scripts/smoke-hub.mjs ../metacubexd
python3 scripts/smoke-container.py
DOCKER_CONTEXT=desktop-linux env -u DOCKER_HOST node scripts/smoke-controls.mjs ../metacubexd
```

前端执行 `pnpm --filter @metacubexd/ui typecheck` 与 `test:unit`。CI 校验 sqlc 生成一致性、竞态、内嵌 UI 浏览器流程、Linux 双架构与容器，不自动推送镜像或发布 Release。

[API](docs/API.md) · [配置](docs/CONFIG.md) · [运维](docs/RUNBOOK.md) · [首次使用验证](docs/FIRST-RUN-VERIFICATION.md) · [P0 验收](docs/P0-VERIFICATION.md) · [P1 验收](docs/P1-VERIFICATION.md) · [P2 验收](docs/P2-VERIFICATION.md) · [P3 验收](docs/P3-VERIFICATION.md) · [Controller/DNS 验收](docs/CONTROLLER-CONTROL-VERIFICATION.md) · [第三方许可](docs/THIRD-PARTY-NOTICES.md)
