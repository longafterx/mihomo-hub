# Controller 维护与 DNS 配置验收

2026-11-07 在本地完成验证。后端扩展 Gateway 白名单，前端沿用 XD 设置和 Provider 入口；DNS 使用用户确认的完整 YAML 导入方案，见 [ADR-009](ADR/009-controller-maintenance.md)。此次验证未登录或操作远程服务器，也未对已注册实例执行维护操作。

## 功能与边界

| 功能 | 实现与验收 |
| --- | --- |
| 修改端口、允许局域网 | 原生 PATCH；真实 Controller 读回新值，浏览器操作 XD 控件后再读回。端口允许 0–65535，拒绝 null、小数、负数和越界值。 |
| 出口接口、TUN | 原生 PATCH；TUN 编辑 stack/device 时携带当前 enable，避免误关闭。真实容器验证启停和 TUN 网卡创建；出口接口及其他 TUN 参数由协议测试验证透传。 |
| Provider 更新 | 代理和规则 Provider 均验证更新后的实际内容；编码名称、双实例和 Secret 隔离由协议测试覆盖。 |
| DNS/Fake-IP 缓存清理 | 原生维护请求，验证 Controller 接受；不声称能通过原生 API 完整读取缓存来证明每个条目已删除。 |
| Geo 更新 | 本地提供镜像自带 Geo 数据，配置实际使用 Geo 规则，核验下载请求与成功响应。 |
| 重载配置、重启内核 | 重新读取测试容器挂载的配置文件，验证端口恢复和 Controller 可再次访问。原生 restart 返回 200，其余多项维护返回 204，Gateway 保留原始状态码。 |
| 完整 YAML 导入、DNS 热加载 | 导入仅保存，之后合并七个 DNS 字段并加载整份配置。切换两个本地 DNS 源，查询实际分别返回 `192.0.2.11` 和 `192.0.2.22`。 |

普通 Gateway 不开放任意配置文件路径、任意 payload、升级或 Controller 密钥/监听修改。DNS 不走无效的原生 PATCH，使用平台专用接口。维护操作默认超时 180 秒，可配置为 1–600 秒；浏览器和 Go 均不自动重试写操作，未确认的结果要求先检查状态。

完整 YAML 使用主密钥加密保存，接口仅返回 DNS 字段和 revision，不返回原文。应用完整配置也会应用其中的代理、规则和端口。它不会写入目标设备文件，重载或重启后仍以设备文件为准；外部修改配置后需重新导入。已导入的标记仅说明存在基准，不表示当前内核与基准一致。缺少完整配置、revision 冲突、Controller 地址变更、上游失败和保存失败均有独立错误处理。删除实例即使保留历史也会清除配置密文。

## 自动化结果

- Go 单元测试、集成测试、完整竞态检查和 vet 通过。
- 前端 66 个测试文件、726 项单元测试通过，typecheck、改动文件 ESLint 和 Hub 静态构建通过。
- 协议测试覆盖鉴权、跨站拒绝、只读开关、超时、写截止时间、双实例 Secret 隔离、加密保存、DNS 合并、revision 并发冲突、实例删除、审计失败回滚及上游失败不更新基准。
- 原有 Hub 浏览器烟测通过，覆盖登录、实例管理/切换、WebSocket、用量与历史页面。
- 真实 Controller 浏览器烟测通过，覆盖桌面和手机设置页、YAML 导入、DNS 应用以及上表列出的真实操作。

主要复现命令（先在前端目录执行单元测试/typecheck，再在后端准备内嵌产物）：

```sh
# ../metacubexd
pnpm --filter @metacubexd/ui test:unit
pnpm --filter @metacubexd/ui typecheck

# mihomo-hub
sh scripts/build-frontend.sh ../metacubexd
go test -race -tags=integration,frontend ./... -timeout=180s
go vet -tags=integration,frontend ./...
sh scripts/generate-sql.sh
python3 scripts/smoke-cli.py
MIHOMOHUB_EMBED_SMOKE=1 node scripts/smoke-hub.mjs ../metacubexd
DOCKER_CONTEXT=desktop-linux env -u DOCKER_HOST node scripts/smoke-controls.mjs ../metacubexd
DOCKER_CONTEXT=desktop-linux env -u DOCKER_HOST python3 scripts/smoke-container.py
```

真实测试使用本地 Docker Desktop 的 `metacubex/mihomo:latest`，实际版本 **v1.19.21（arm64）**、本机镜像 ID 为 `sha256:541ba55896f53991c9957901f40313cd98da427d11628fef17ad3662410844a1`。`latest` 会变化，其他版本需要另行验证，不能据此宣称全部兼容。临时测试 Controller 限制 256 MiB、1 CPU、64 个进程；Provider、DNS 和 Geo 均来自本地测试服务，不下载公共源，不执行压测。测试结束清理临时容器、服务及数据库。截图位于忽略目录 `test-results/hub-controls-desktop.png` 和 `test-results/hub-controls-mobile.png`。

## 部署与限制

Schema 5 新增 `runtime_configs` 表，升级前应在线备份 SQLite 并单独备份主密钥；回退需同时恢复旧镜像和旧数据库。发布包 manifest 记录后端 SHA、前端 SHA 和 schema，Go 嵌入固定前端版本，不复制前端源代码。

TUN 需要目标设备本身具备网络权限和内核支持；Hub 不为目标安装驱动或提权。Geo/Provider 的可用性依赖目标设备能访问其配置的下载源。Controller 与 SQLite 无法组成同一个事务，内核接受后本地保存失败会明确提示核对状态、重新导入，不会假装已经回滚内核。
