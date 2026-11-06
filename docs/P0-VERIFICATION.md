# P0 实现与验收记录

> 本文保留 P0 阶段验收状态；当前 P1 的新增能力与限制见 [P1-VERIFICATION.md](P1-VERIFICATION.md)。
验证日期：2026-11-06。环境：macOS arm64，Go 1.27.3，Node 24.19.0，pnpm 10.34.1。前端：独立 Fork `longafterx/metacubexd@4fd88e621722fc9274f0120c53ee1b13ba2d706e`。仅访问本地合成 Controller，未操作用户的真实 Mihomo。

## 交付范围

| 模块 | 实现文件 | 验证 |
|---|---|---|
| CLI/装配/退出 | `cmd/mihomo-hub/main.go`、`internal/app/app.go` | 正式 CLI 的 PTY 初始化、隐藏密码、Session、SIGTERM |
| 配置 | `internal/config/config.go`、`config.example.yaml` | 默认值、严格 YAML、未知字段、Origin/端口/大小校验、环境覆盖 |
| SQLite/goose | `internal/storage/storage.go`、`migrations/{migrations.go,001_init.sql}` | 重复迁移、未来版本拒绝、WAL、读写探活、三条连接 FK/busy_timeout |
| sqlc/Repository | `sqlc.yaml`、`queries/instance.sql`、`internal/storage/{instances.go,admin.go,generated/*.go}` | CRUD 持久化、事务审计、写失败回滚、重启恢复、生成可重现 |
| Secret | `internal/storage/secrets.go` | AES-GCM 回读、实例 ID 绑定、篡改拒绝、权限检查、SQLite/WAL 不含明文 |
| Instance Registry | `internal/instance/instance.go` | UUID、多实例、禁用/删除/更新取消旧 generation、失败恢复 |
| 认证 | `internal/auth/{password.go,session.go,browser.go,limiter.go}` | 真假凭证、过期、撤销、Cookie、CSRF/Origin、登录限流 |
| Controller 安全拨号 | `internal/mihomo/transport.go` | URL、端口、内网/公网、metadata、混合 DNS、拨号时 rebinding 拒绝 |
| Gateway | `internal/gateway/{gateway.go,socket.go}` | REST/WS 契约、Header/Query/路径、大小/容量/超时、透明关闭、会话释放 |
| Gin API | `internal/httpapi/router.go` | CRUD、权限、错误包装、无敏感日志、Agent 404、无 HTML 回退 |
| 登录/外部静态目录 | `internal/web/{web.go,login.go}` | 登录 JS 正常 200、表单 POST、正式 XD 页面、静态资源 404 |
| 测试资源 | `internal/testutil/fakemihomo/fake.go`、`tests/fixtures/{configs,connections,proxies,rules,version}.json` | 两个独立 Mock，持续 WS、文本/二进制与正常/异常关闭 |
| 自动化 | 各模块 `*_test.go`、`tests/integration/gateway_test.go`、`tests/smoketest/server/main.go` | 单元、真实临时 SQLite、完整 HTTP/WS、goleak、race |
| 工程工具 | `scripts/{generate-sql.sh,smoke-cli.py,build-xd-smoke.mjs,smoke-xd.mjs}`、`build/frontend.lock.yaml`、`.github/workflows/test.yml`、`.gitignore`、`go.mod/go.sum` | 固定版本、代码生成、无 CGO 构建、浏览器与 CLI 烟测 |
| 使用说明 | `README.md`、`docs/{API,CONFIG,P0-VERIFICATION}.md`、`docs/ADR/P0-DECISIONS.md` | 运行方式、明确接口/限制和下一阶段边界 |

## 验收矩阵

| P0 标准 | 结果 | 证据 |
|---|---|---|
| 注册/修改/禁用/删除两个 Controller | 通过 | `TestRegistryAndRESTContract`，retain/purge 分别验证 |
| version/proxies/connections 与上游同构 | 通过 | 对 fixtures 逐字节比较，同时验证 configs/rules |
| 四类 WS 持续转发并释放 | 通过 | 两实例 × 四流，多条消息、二进制回声、子协议、正常/异常关闭，goleak 无泄漏 |
| 原版 XD 查看连接、切代理、规则、日志 | 通过（离线字体构建） | Playwright 通过真实登录页手工输入两个 Gateway Endpoint；所有 Gateway 请求无错误 |
| 未登录禁止 API/WS，浏览器无真实 Secret | 通过 | 401/403 契约测试；浏览器网络、WS URL、localStorage/IndexedDB 检查 |
| 未注册 ID/非法 URL/禁用实例拒绝 | 通过 | Mock 请求计数证明拒绝请求不触达上游；metadata/重绑定/重定向/公网默认拒绝 |
| 单测、竞态、端到端契约 | 通过 | 以下命令；CI 已提供，未声称远端 CI 已运行 |

额外验证：URL 编码中文、编码斜杠、重复 Query、Bearer 服务端注入、Cookie/Forwarded 剥除、上游 401/418/204、二进制 HTTP 响应、超限与超时、缺 CSRF/坏 Origin、显式禁用运行时写入、危险 API 403、审计失败事务回滚、重启 Session 失效。WS 在测试中跨过普通 HTTP 的 250ms 读写截止时间仍保持可用；实例变更、注销和关闭服务能中断已有 WS。

## 实际执行

```sh
go test ./...
go test -tags=integration ./... -timeout=90s
go test -race -tags=integration ./... -timeout=120s
go vet ./...
CGO_ENABLED=0 go build -trimpath -o dist/mihomo-hub ./cmd/mihomo-hub
sh scripts/generate-sql.sh
python3 scripts/smoke-cli.py
node scripts/build-xd-smoke.mjs ../metacubexd
node scripts/smoke-xd.mjs ../metacubexd
```

sqlc 重新生成前后 SHA-256 一致。CLI 烟测输出：

```text
PASS: production CLI, interactive init, hidden password, 0600 key, readyz, Session, SIGTERM
```

浏览器烟测输出：

```text
PASS: unchanged XD, 2 endpoints, connections, rules, logs, proxy switching, 4 WS streams, empty browser secrets
```

浏览器截图位于本地忽略目录 `test-results/xd-smoke.png`。前端仓库 `git status --short` 为空；依赖和 `.output` 是其忽略的本地构建产物，没有修改源码或锁文件。

## 已知限制与下一阶段

- 标准 `pnpm build:ui` 在此环境因 Google Fonts 网络超时失败。已成功运行离线辅助构建，其余应用代码与固定 Commit 相同，字体使用 CSS fallback；不能据此声称正式 PWA/发布构建已验收。
- 合成 fixtures 只证明当前 XD 与 Gateway 的消费契约。尚未使用真实 Mihomo 响应验证不同内核版本，未做生产容量压测；本次没有访问设计文档中的示例地址。
- P0 不采集数据；采集状态返回 not_implemented，最后在线时间为空。`collect_enabled` 保存意图，未来默认值变更需说明。Analytics、主动 test/status 等接口尚未提供。
- 运行时控制采用明确白名单。原版 XD 的未授权功能可能仍有按钮，后端明确返回 403；P2 再做 UI 能力适配。无限 HTTP NDJSON/SSE 流不在 P0 目标内，实时数据走 WS。
- Session 仅内存存储、重启失效；单管理员，没有密码重置或密钥轮换。删除 retain 会占用原名称。反向代理后用户共用代理 IP 的登录限流桶。
- 无正式 UI Embed、Docker/跨平台发布、在线备份恢复、生产安全审计或 PWA 更新验收；这些仍属 P3。已配置后端 Linux CI，但尚未在远端执行。
- 下一步严格按 P1：先设计并测试 epoch 判定、持久游标和 gap/recovery 模型，再实现每实例 Collector、重连、差分和批量写入。无法确认同一内核运行 epoch 的重启恢复窗口应明确记为不确定，不能伪装为精确分钟流量。完成 P1 验收后再进入 P2 的 Endpoint/历史页面适配。
