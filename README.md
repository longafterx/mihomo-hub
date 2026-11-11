# MihomoHub

基于 MetaCubeXD 的多实例 Mihomo 管理面板，统一切换实例、管理内核配置，并持续采集流量，查看历史用量及域名、代理、规则等统计。

## Docker Compose 部署

需要 Docker 和 Compose。镜像支持 Linux amd64/arm64，默认使用 `ghcr.io/longafterx/mihomo-hub:latest`。

```bash
mkdir -p ~/app/mihomo-hub
cd ~/app/mihomo-hub
curl -fL https://raw.githubusercontent.com/longafterx/mihomo-hub/main/compose.yaml -o compose.yaml
docker compose pull
docker compose up -d
```

打开 http://127.0.0.1:11235 ，首次访问设置登录密码，然后添加 Mihomo 实例。无需执行 `init`。

服务器通过 IP 远程访问时，将 `compose.yaml` 的端口映射改为 `"11235:11235"`，并在同目录创建 `.env`，填写浏览器实际访问的地址：

```dotenv
MIHOMOHUB_PUBLIC_ORIGIN=http://你的服务器IP:11235
```

修改后执行 `docker compose up -d`。通过域名或反向代理访问时，同样将 `MIHOMOHUB_PUBLIC_ORIGIN` 设置为实际入口。

数据保存在 `hub-data` 数据卷中，重建容器会保留。不要使用 `docker compose down -v`，它会删除数据。

更新镜像：

```bash
docker compose pull
docker compose up -d
```

查看运行状态和日志：

```bash
docker compose ps
docker compose logs -f --tail=100
```
