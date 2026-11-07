# 自动初始化与首次密码设置验证

决策见 [ADR-010](ADR/010-browser-first-run.md)。部署入口为 `docker compose up -d` 或直接启动二进制，随后在网页设置登录密码。测试全部使用本地临时目录、端口、容器和数据卷。

| 场景 | 验证方式 |
|---|---|
| 空目录自动建库、迁移、生成 0600 主密钥 | `internal/app/setup_test.go`、`internal/storage/bootstrap_test.go` |
| 待设置时健康就绪，根页面进入引导，管理/Gateway 拒绝匿名 | App/API 测试、CLI 与容器 smoke |
| 密码长度、确认值、未知 JSON 字段、同源校验、限流 | App/API 测试、真实浏览器表单 |
| 并发仅一个请求成功，已有密码不被覆盖 | 并发 HTTP 和八路存储测试；`-race` |
| 管理员和设置审计原子提交，审计失败回滚 | SQLite 注入失败触发器 |
| 设置成功自动创建 Session，密码仅存 Argon2id 哈希 | App/API、浏览器、CLI 与容器 smoke |
| 另一个浏览器已完成设置，旧引导页切回登录 | `scripts/smoke-setup.mjs` |
| 密码已保存但响应丢失时重新读取状态并切回登录 | 浏览器拦截并丢弃真实成功响应 |
| 设置前/后重启保留主密钥，设置后只需原密码登录 | App/API、浏览器与容器 smoke |
| 已有管理员或实例但缺密钥时拒绝启动，损坏密钥不被替换 | App 与存储测试 |
| SQLite 在线备份恢复后沿用原密码、免重新设置 | `scripts/smoke-cli.py` |
| 非 root、只读镜像首次使用与重启持久化 | `scripts/smoke-container.py` |
| 内嵌 XD 原有多实例、实时四类 WS、历史及 Secret 隔离 | `scripts/smoke-hub.mjs` |

```sh
go vet -tags=integration,frontend ./...
go test -race -tags=integration,frontend ./... -timeout=180s
python3 scripts/smoke-cli.py
MIHOMOHUB_EMBED_SMOKE=1 node scripts/smoke-setup.mjs ../metacubexd
MIHOMOHUB_EMBED_SMOKE=1 node scripts/smoke-hub.mjs ../metacubexd
DOCKER_CONTEXT=desktop-linux env -u DOCKER_HOST python3 scripts/smoke-container.py
```

浏览器截图在 `test-results/hub-first-run-{desktop,mobile}.png`。Go 提供设置/登录外壳，XD Fork 无源码改动，前端锁定 SHA 保持不变。Schema 保持 5，原密码哈希、实例 UUID、Controller Secret 密文、导入 YAML 与历史兼容；Session 仍在重启后失效。

首次密码设置只在管理员记录不存在时可用，不作为找回或重置密码的接口。没有默认密码，也不通过环境变量传递初始密码。兼容旧自动化的 `init` 命令仍保留，但已从部署步骤移除。
