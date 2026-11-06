# ADR-008：实例管理的提交结果、历史身份和编辑配置边界

状态：采用，2026-11-06。

实例新增与列表刷新曾共用一个成功/失败结果：POST 已提交后，GET 失败仍会被 UI 报为新增失败。后台轮询还可能把变更前的列表覆盖回来。现在变更接口返回的数据直接更新本地列表，成功变更递增列表代次，变更前的轮询响应或错误作废。写响应在网络中断中丢失仍可能导致结果未知，因此错误提示要求先刷新确认；没有引入自动重试写操作。

保留历史的删除只撤销运行实例与凭据，不销毁 UUID。名称是显示属性，只有未删除实例需要唯一。Schema 3 重建 instances 表以移除列级全局 UNIQUE，再建活动名称唯一索引；保留名称和全部 UUID 引用。迁移用同一启动连接、显式事务和临时关闭外键，避免 DROP 父表触发历史的级联删除，提交后恢复外键。迁移从 schema 2 的真实历史夹具验证全部子表、加密凭据、generation、外键检查和后续 purge 隔离。重新使用名称创建的实例分配新 UUID，绝不自动恢复或合并旧历史。

原设计不向浏览器回传 Controller 原地址，导致编辑表单无法展示当前配置。用户要求明确的编辑流程，因此细化这一边界：仅 Session 管理员通过 GET /api/v1/instances/{id}/config 按需读取地址，响应 no-store；列表、普通详情与变更响应继续只有 Gateway。浏览器不持久化该地址，运行 Endpoint 仍只使用同源 Gateway，真实 Secret 始终不返回，已保存密钥只通过“保留 / 替换 / 移除”操作处理。此接口与现有单管理员权限一致；未来引入多用户必须明确管理员配置权限。

新增草稿测试通过独立 POST /api/v1/instances/test 完成，复用服务端已保存的 Secret 但不回传。受 Session/CSRF/同源、速率限制、SSRF、端口策略和 5 秒超时约束。只读 /version，不先创建再回滚，不改变 Collector 生命周期。启用状态新增先验证连接再保存；停用配置可先保存，普通 API 创建仍允许离线注册。

交互参考 [neko-master 的实例管理](https://github.com/foru17/neko-master/blob/main/apps/web/components/features/backend/backend-config-dialog.tsx)：列表入口、独立新增/编辑弹窗、验证后新增和删除确认。实现沿用 XD 的 Modal、Button、daisyUI 语义色、字体和移动端布局，不引入其前端框架、Agent 或 Surge 功能。
