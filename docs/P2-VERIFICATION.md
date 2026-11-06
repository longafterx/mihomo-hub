# P2 验收记录

前端独立仓库：`../metacubexd`，最终锁定提交 `0cc6463a9a7fa29122623a8c2d9162de0b090008`（主体实现 `cee46ca5527d7462ac4fe50317cd6f7688ad7c17`），上游基线 `4fd88e621722fc9274f0120c53ee1b13ba2d706e`。没有将 Vue 源码复制到 Go 仓库。

完成：服务端注册中心及增删改/启禁用；全局实例切换；同源空 Secret Endpoint；服务端 SQLite 单实例/全部/retain 历史；总量、采样归因、差额、五种 TOP、趋势、覆盖边界、分页缺口及连接摘要；加载/离线/错误/重试/失效会话；旧 localStorage 显式保留/清理；Hub 不写浏览器流量数据库。运行时页面继续复用 XD。

验证已通过：

- 前端 `typecheck`、新增及修改文件 ESLint、提交前格式检查。
- 全部单元测试：61 文件，687 项；新增 7 项覆盖同源 Endpoint、UTC 窗口/限额、CSRF、历史请求过滤、未知桶、取消后迟到响应及健康轮询 Endpoint 身份稳定。
- `MIHOMOHUB_BUILD=true pnpm build:ui`：静态产物成功，PWA 关闭，字体本地提供。保留上游 Monaco 大 chunk 的构建提示。
- `node scripts/smoke-hub.mjs ../metacubexd`：真实 Chromium + Go + SQLite + 两个 Fake Controller；新增/编辑/启禁用/删除、刷新持久化、retain/purge、单实例与全部历史、五种 TOP、移动端、503 清空旧值并重试、Cookie 失效跳转、注销；连接/规则/日志/代理切换和四条 WS 均通过。
- 浏览器请求、WS URL、localStorage 和 IndexedDB 状态未发现夹具 Controller 密钥；浏览器不发送 Controller token。

关键问题已修复：移动端 flex 压缩汇总卡片；并发 401 导致重复导航。截图在忽略的 `test-results/hub-smoke.png`，不包含真实实例或密钥。

口径与限制见 ADR-007：独立 TOP 不支持跨维度钻取；旧 IndexedDB 不迁移；连接生命周期量不能作为窗口量；时间覆盖率不代表归因准确率。新增文案提供中英，其他已支持语言暂使用英文。
