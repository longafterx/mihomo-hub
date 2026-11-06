# MihomoHub 运维手册

发布包包含内嵌 UI、版本清单与校验和。Controller 的安装、升级和重启不属于本平台职责。

## 在线备份

```sh
umask 077
./dist/mihomo-hub backup --config config.local.yaml --output backups/hub-2026-11-06.db
```

目标必须是新文件。命令使用 SQLite `VACUUM INTO` 获取提交数据的一致性快照，包含 WAL，输出权限 0600。无需停止正常采集，但备份会消耗 I/O；在大库上安排适当时间和磁盘空间。失败时移除不完整目标，已有文件从不覆盖。

**主密钥另行安全备份。** 数据库备份包含管理员哈希、加密 Controller Secret 和敏感网络历史；将主密钥和数据库副本分开保管。不要在运行中仅复制主 `.db`，也不要将密钥粘贴进日志、Issue 或命令参数。

## 恢复到新目录

1. 在隔离的新目录准备备份副本，不覆盖仍在使用的数据目录。
2. 将数据库快照复制为新目录的 `mihomo-hub.db`，设权限 0600。新目录不带旧库的 `-wal/-shm`。
3. 从独立备份恢复对应的原始 32 字节 `master.key`，设权限 0600；目录建议 0700。不要再次 init 生成不同密钥。
4. 准备新配置，指向这两个新路径。先停止原 MihomoHub 采集进程，以免两份 Collector 同时写各自历史；无需停止 Mihomo Controller。
5. 使用同版本或支持该 schema 的较新二进制启动。服务自动执行版本迁移，拒绝比自身新的 schema；没有自动降级。
6. 用备份中的管理员账号重新登录，检查 readyz、实例状态、Analytics coverage/gaps。Session 不在备份中。恢复首轮不产生累计尖峰；无法确认 epoch 的增长进入独立 candidate gap。
7. 验证成功后再切换入口并保留原目录用于人工回退。回退也应使用对应版本/备份，不应直接让旧二进制打开已升级的数据库。

自动化已验证 WAL 未 checkpoint 时的备份、打开快照恢复、游标/聚合一致、备份不可覆盖和文件权限。

## 日常检查

登录后查询 `/api/v1/system/status`，关注 write_failures、dropped_samples、queue_length 和 collector_lag_ms；单实例离线不影响全局 readyz。使用 `/analytics/gaps` 查看恢复候选量，`/analytics/coverage` 查看请求窗口采集时长及实际最早保留桶。

清理按预算逐轮执行。大量历史积压时保留期可能延迟收敛；持续观察数据库增长并调整规模。删除历史会释放 SQLite 可复用页，不保证主文件立即缩小；P1 不在服务中自动执行原地 VACUUM。

## 单二进制与 systemd

1. 校验发布包 SHA256SUMS，将对应架构的 `mihomo-hub` 安装到 `/usr/local/bin/`；用 `version` 核对前端 SHA、后端 SHA、schema 和 embedded=true。
2. 创建专用系统用户 `mihomo-hub`；创建 `/var/lib/mihomo-hub`（0700，属主为该用户）和 `/etc/mihomo-hub/config.yaml`（0640，用户可读）。配置数据库 `/var/lib/mihomo-hub/mihomo-hub.db`，密钥 `/var/lib/mihomo-hub/secrets/master.key`，监听及 public origin 使用实际入口。
3. 以服务用户在交互终端执行 `mihomo-hub init --config /etc/mihomo-hub/config.yaml`。不要把密码放入 shell 历史、环境变量、日志或配置文件。
4. 安装本目录的 `mihomo-hub.service`，执行 `systemctl daemon-reload`、`systemctl enable --now mihomo-hub`。用 `journalctl -u mihomo-hub`、`/readyz` 和登录后 `/api/v1/system/status` 检查服务。

服务单元启用只读系统目录、独立临时目录、无附加能力和 0077 umask；唯一持久可写目录是 `/var/lib/mihomo-hub`。默认账号没有远程内核控制能力。

## 容器与权限

从锁定前端生成静态资源后执行 `sh scripts/build-image.sh`。`docker compose run --rm -it mihomo-hub init` 初始化同一个 named volume，再 `docker compose up -d`。镜像默认非 root 65532:65532；named volume 首次挂载使用镜像目录权限。bind mount 需预先由管理员创建 0700 目录并设置 `chown 65532:65532`。

`docker compose exec mihomo-hub /mihomo-hub version` 核对版本；`docker compose exec mihomo-hub /mihomo-hub backup --output /var/lib/mihomo-hub/backups/新文件.db` 在线备份（先创建可写 backups 目录）。镜像没有 shell/curl，使用内置 `healthcheck`。将备份复制到受限宿主路径，主密钥独立保管。

恢复时停 Hub 容器，将快照和对应密钥放入新的空 volume，再以服务 UID 验证 0600 权限后启动；不运行 init。不要复制旧 WAL/SHM 文件覆盖快照，也不要更改 neko-master 或 Controller 进程。

## HTTPS、反向代理与 Tailscale

使用独立 Origin 根路径。Tailscale Serve 代理到本机 `http://127.0.0.1:8080`，公网入口通过 Tailnet ACL 控制；`MIHOMOHUB_PUBLIC_ORIGIN=https://设备名.tailnet.ts.net` 必须与浏览器实际 Origin 完全一致。根据已安装 Tailscale 版本使用对应的 Serve 命令；不要把端口直接绑定公网。容器修改环境后 `docker compose up -d --force-recreate`。

Nginx 示例中的 `server_name` 和证书路径由运维填写，TLS 应由可信证书终止：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $http_host;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_read_timeout 75s;
}
```

保留原始 Origin、Cookie；不要让不可信客户端绕过代理直达服务端口。程序不会依据 `X-Forwarded-*` 信任客户端或推断 Origin。HTTPS public origin 会设置 Secure Cookie；浏览器时钟、主机名和端口需要一致。

## 升级、回退与故障恢复

升级前在线备份数据库并单独备份主密钥，记录现有 `version` 与 schema；停止旧 Hub，替换二进制或使用固定标签/摘要镜像，启动后验证 readyz、重新登录、实例列表、历史及缺口。升级只迁移 SQLite，不修改 Controller 配置。内嵌 index/config 不长期缓存，哈希资源 immutable，旧 XD Worker 由 `/sw.js` 退役。

新程序自动执行向前迁移；旧程序遇到较新 schema 明确拒绝启动。没有自动 down migration。回退必须停止新进程，用升级前的完整快照、对应主密钥和旧二进制启动；不要让旧版打开升级后的库。Session 只在内存，重启或恢复后重新登录；连接恢复的不确定增量会记录 gap，不伪造精确分钟流量。

主密钥丢失时不要生成新密钥尝试解密旧库；从独立备份恢复匹配密钥。若主密钥被泄露，应隔离入口并替换相关 Controller 凭据，重新登记到新密钥的数据目录；首版没有在线主密钥轮换和密码重置命令。密码遗失请从已知账号的安全备份恢复；不要直接编辑管理员哈希。

测试环境可执行 `scripts/smoke-cli.py`、`MIHOMOHUB_TEST_BINARY=<已构建本机发布二进制> python3 scripts/smoke-cli.py` 和 `scripts/smoke-container.py`，验证真实入口、持久化与备份。全部使用临时测试数据，不调用真实内核变更接口。
