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
