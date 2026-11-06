# 实例管理修复验收（2026-11-06）

复现与修复：

- Pinia 回归测试复现 POST 成功、后续 GET 失败被错误报告为新增失败，以及增删后旧轮询覆盖列表。变更响应直接更新列表、轮询代次校验后通过。
- API 集成测试复现无密钥创建成功、retain 删除后同名创建返回 409。Schema 3 仅约束未删除实例名称，新建 UUID 与原 UUID 不同，仍活动的同名创建保持 409。
- 从 schema 2 升级包含全部 9 类历史/采集子表的真实数据库，验证行数、字节总量、generation、密文、foreign_key_check、外键重新启用，以及升级后 purge 不跨实例删除。
- 编辑配置接口测试 Session、no-store、只返回地址不返回密钥；草稿测试覆盖无密钥、沿用/替换/清空密钥、非法 URL、metadata 目标、已删除实例、Session/CSRF/Origin 拒绝与不落库、不改变 generation/密文。

本地验证命令：

```sh
# 后端、SQLite 与 Gateway；均使用临时目录和 Fake Controller。
go test -race -tags=integration,frontend ./... -timeout=180s
go vet -tags=integration,frontend ./...
sh scripts/generate-sql.sh

# 前端：62 个文件、694 项单元测试通过，类型检查通过。
cd ../metacubexd
pnpm --filter @metacubexd/ui typecheck
pnpm --filter @metacubexd/ui test:unit
cd ../mihomo-hub
sh scripts/build-frontend.sh ../metacubexd
MIHOMOHUB_EMBED_SMOKE=1 node scripts/smoke-hub.mjs ../metacubexd
```

浏览器验收包含：连接验证失败不创建；列表 GET 失败仍显示已新增实例；编辑回显与改名；启停；purge/retain；同名重建分配新身份且旧历史仍可查询；移动端列表/编辑弹窗；历史错误与恢复；XD 策略切换、连接、规则、日志及四类 WS；已保存的 Controller Secret 不回传，运行时请求/Endpoint 与浏览器持久存储不携带密钥。截图在忽略的 test-results 目录。

连接测试只验证 /version，不代表后续长期采集始终可用。编辑保存不会强制在线，便于修改离线实例；未保存的连接测试不改变任何配置。写响应因网络中断丢失时，结果仍可能未知，界面提示先刷新核实；没有自动重试写入或自动合并历史。

所有测试和构建在本地执行，未 SSH 到远程服务器，未对真实 Controller 进行压力测试或控制操作。用户已有设计文档与工具元数据不纳入本次提交。

## IP/端口输入与探测原因（2026-11-06）

- 表单改为 IP/主机与端口两个输入框，端口默认 9090，不要求输入协议；新增自动 HTTP，编辑已有 HTTPS 地址时保留协议。IPv4、IPv6、主机名、默认端口以及非法协议/路径/凭据/端口通过工具单元测试验证；数字输入框产生 number 时同样正确处理。
- 草稿测试和已保存实例测试共享固定诊断代码，区分 TCP 拒绝、超时、鉴权失败、DNS、TLS、重定向、异常 HTTP 和无效版本响应。错误不携带原始地址、Secret 或上游响应体。
- 对用户提供的无密钥目标仅从本机和 Hub 本地容器各发起一次 GET /version，均 TCP connection refused；用户提供的服务器 ss 输出也确认 9090 没有监听进程。没有登录或修改远程服务器。用户随后确认连通恢复，未继续对该目标发起探测。
- 此次前端 63 个文件、710 项单元测试通过；Go 全量竞态测试及 vet 通过。


## 密码登录、自动管理采集与多实例入口（2026-11-06）

依据用户明确要求，单管理员登录仅输入密码，继续使用现有哈希，不再要求用户名；兼容旧请求携带 username，返回的会话视图不再含 username。Cookie、同源校验、CSRF、登录限流和退出撤销保持生效。

新增、编辑移除管理/采集开关，列表移除启停按钮；创建后管理和采集始终开启。兼容显式 true，API 明确拒绝 enabled:false 或 collect_enabled:false，不写入、更名或切换代际。需要停止时删除实例，历史仍可保留。Schema 4 自动启用未删除的旧记录，变更 generation；已删除墓碑不恢复，原 UUID、密文与历史不变。

顶部参照 Neko Master 的展示和切换流程，沿用 XD 的主题和字体：当前实例按钮展开菜单，标明“显示中”、其他实例的“采集中”或连接异常状态，底部提供管理入口；横向状态标签可快速切换。菜单支持键盘上下/Home/End、Escape、关闭后焦点恢复、点击外部关闭及窄屏避让。切换仅影响浏览器当前视图，其他实例继续采集。

回归覆盖：仅密码登录及旧用户名兼容、缺失/错误密码拒绝、创建自动采集、false 请求无副作用、存量数据库迁移保留历史/密文/墓碑、实例切换与删除回退、桌面和手机菜单、CRUD/保留历史/同名重建、Session 失效和退出。所有验证都在本地临时数据夹具或 Docker Desktop 执行。


验证结果：前端 typecheck、711 项单元测试通过；Go 全量 race、vet 与 sqlc 生成一致性通过；内嵌前端浏览器流程通过，桌面/手机截图保存在 test-results/hub-switcher-{desktop,mobile}.png；CLI 密码初始化/登录/备份恢复通过。本地镜像容器验收包含升级后的 schema 4、密码登录、默认管理采集和持久化重启。
