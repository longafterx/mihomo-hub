# 发布 Docker 镜像

镜像地址：`ghcr.io/longafterx/mihomo-hub`。发布由后端仓库的 `.github/workflows/release.yml` 管理，前端仍在独立 Fork 中维护。

## 发布步骤

1. 将前端提交推送到 `longafterx/metacubexd`，更新后端 `build/frontend.lock.yaml` 的固定 SHA。
2. 提交并推送后端构建输入。确认两个仓库都不包含待发布的未提交源码。
3. 为后端提交创建版本标签并推送，例如：

   ```sh
   git tag -a v0.3.0 -m 'MihomoHub 0.3.0'
   git push origin main
   git push origin v0.3.0
   ```

4. 在 GitHub Actions 中查看 `release`：前端类型与单元测试、Go 竞态及集成测试、浏览器回归、双架构构建、隔离容器验收全部通过后，才登录 GHCR 并推送镜像。工作流自带 `GITHUB_TOKEN` 和 `packages: write` 权限，无需在仓库配置 PAT。
5. 首次发布后，在 GitHub Package 设置中确认可见性。GitHub 新建 Container Package 默认可能为 private；供匿名拉取时需由仓库所有者改为 public。`org.opencontainers.image.source` 将镜像关联到源仓库。

稳定标签 `v0.3.0` 生成镜像标签 `0.3.0` 和 `latest`。预发布标签如 `v0.4.0-rc.1` 仅生成对应版本，不更新 `latest`。版本标签用于固定发布，后续修复应递增补丁版本，不重建或移动已发布标签。手动 `workflow_dispatch` 仅验证，不发布镜像；可对失败的版本标签工作流重新运行。

## 验证与部署

```sh
docker buildx imagetools inspect ghcr.io/longafterx/mihomo-hub:0.3.0
docker pull ghcr.io/longafterx/mihomo-hub:0.3.0
docker run --rm ghcr.io/longafterx/mihomo-hub:0.3.0 version
docker compose pull
docker compose up -d
```

镜像包含 Linux amd64/arm64 及构建 provenance、SBOM。`version` 应显示 `frontend_embedded=true`、实际的前后端 SHA 和 schema。工作流摘要记录发布 digest；需要不可变部署时，将 `MIHOMOHUB_IMAGE` 设为 `ghcr.io/longafterx/mihomo-hub@sha256:<digest>`。

升级前在线备份数据库并单独备份主密钥。发布与容器验证不会访问真实 Controller、部署目标服务器或更改本地已有容器；部署仍由使用者执行。数据挂载与恢复见 [Runbook](RUNBOOK.md)。
