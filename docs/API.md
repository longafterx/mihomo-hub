# Platform API v1 与 Mihomo Gateway

仅 `/api/v1` 使用 `{"data":...,"meta":{},"error":null}` 包装。失败返回对应 HTTP 状态，`error={"code":"...","message":"..."}`；不会回显输入、SQL 或 Controller 错误 URL。Gateway 保留 Mihomo 原生格式。

## 认证

| 方法和路径 | 权限 | 行为 |
|---|---|---|
| `GET /login` | 公开 | 后端提供的最小登录页，独立于 XD 源码 |
| `POST /api/v1/auth/login` | 同源 | JSON `password`（兼容并忽略旧 `username`）；设置 HttpOnly Session Cookie |
| `GET /api/v1/auth/me` | Session | `csrf_token/expires_at_ms` |
| `POST /api/v1/auth/logout` | Session + CSRF | 撤销会话并断开其 WS，清除 Cookie |
| `GET /api/v1/meta` | Session | 后端/前端版本、API/schema 版本与构建信息 |

管理员密码是 MihomoHub 网页登录密码，与实例 Controller Secret 独立；初始化要求 8–1024 字节，不强制大小写、数字或符号组合。现有密码哈希保持兼容，无需重新初始化；登录页及 CLI 初始化不再要求用户名，数据库保留旧用户名仅作内部标识。

Cookie 名为 `mihomohub_session`，Path=/，SameSite=Strict，HTTPS Origin 下 Secure。只保存 Session ID 的 SHA-256 哈希；每次成功登录生成新 Session 并撤销本浏览器旧 Session。最多 1024 活跃 Session、两个并行密码验证；按物理 IP 限制每分钟 10 次登录（含成功）。

所有平台写请求（登录除外）同时要求 Cookie、`X-CSRF-Token` 和同源 Origin/Referer；登录也要求同源 Origin/Referer。CLI 需手动设置 `Origin`。WS 严格要求 Origin，不能仅用 Referer。所有写请求按 Session 限制每分钟 120 次。

正式发布的 `/meta` 提供实际 frontend_commit 与 frontend_embedded；外部静态目录覆盖时 embedded=false，详见末尾 P3 版本说明。

## 实例 CRUD

| 方法和路径 | 结果 |
|---|---|
| `GET /api/v1/instances` | 200，数组，空列表为 `[]` |
| `POST /api/v1/instances` | 201，创建实例 |
| `POST /api/v1/instances/test` | 200，只读验证未保存的连接配置，不新增或修改实例 |
| `GET /api/v1/instances/{id}` | 200，详情 |
| `GET /api/v1/instances/{id}/config` | 200，仅管理员编辑时按需读取 `controller_url`，不返回 Secret |
| `PATCH /api/v1/instances/{id}` | 200，部分更新 |
| `DELETE /api/v1/instances/{id}?history=retain\|purge` | 200，删除结果 |

创建字段：`name`（非空，至多 128 字节，未删除实例间唯一）、`controller_url`（HTTP(S) 根 URL）、`secret`（默认空，至多 4096 字节，不含 CR/LF）。接入即启用管理和后台采集；`enabled` 与 `collect_enabled` 可省略，兼容旧客户端显式 `true`，显式 `false` 返回 400 `invalid_instance`。不接受用户提供 ID、URL userinfo、基路径、query、fragment 或任意未知字段。

PATCH 允许上述字段，同样禁止关闭管理或采集；省略 Secret 保留原值，`"secret":""` 清空。重命名离线实例无需 DNS 可用；更换 URL/Secret 时重新校验网络策略。任何成功修改会取消旧 generation 的 HTTP/WS 请求；持久化失败恢复此前配置的新 generation。实例和对应审计在同一事务提交后才发布到注册中心。

返回示意：

```json
{
  "data": {
    "id": "<UUID>",
    "name": "my-controller",
    "gateway_url": "/mihomo/<UUID>",
    "has_secret": true,
    "enabled": true,
    "collect_enabled": true,
    "collector_status": "connecting",
    "last_seen_at_ms": null,
    "created_at_ms": 1793923200000,
    "updated_at_ms": 1793923200000
  },
  "meta": {},
  "error": null
}
```

常规列表、详情和变更响应不包含 Controller URL 或 Secret。仅管理员编辑配置接口返回 `controller_url`；Secret 永不回传。时间使用 UTC Unix 毫秒。找不到实例返回 404，同名冲突 409，输入/网络策略/删除选项无效返回 400，持久化失败 500。

`retain` 创建删除墓碑并移除 URL/密文，保留实例 ID 和名称供后续历史表关联；名称不再占用，可以重新新增；新实例分配新 UUID，不继承旧历史。`purge` 物理删除。P1 中所有历史表通过外键关联，purge 会级联清理；retain 保留无凭据墓碑及历史，单管理员仍可按 ID 查询。未指定选项拒绝删除。主动 `/test` 已实现只读版本探测，详见末尾说明。状态与历史接口见下文。

`POST /api/v1/instances/test` 要求 Session、CSRF 和同源校验，接受 `instance_id?`、`controller_url?`、`secret?`。指定实例时省略字段沿用已保存配置（包括密钥）；`secret: ""` 明确测试无密钥连接。未指定实例时必须提供 URL。沿用 Gateway 地址和端口策略，包含 DNS 与拨号检查，最多 5 秒，只读取 `/version`。成功返回 `reachable/version`；失败 502，按原因返回 `controller_connection_refused`、`controller_timeout`、`controller_auth_failed`、`controller_dns_failed`、`controller_tls_failed`、`controller_redirected`、`controller_http_error`、`controller_invalid_response` 或 `controller_unavailable`，不回显上游错误。测试写审计但不修改注册表、generation、凭据或历史。

前端地址表单只要求 IP/主机与端口（默认 9090），新增自动使用 HTTP，编辑拆分已有地址并保留已有 HTTP/HTTPS 协议；底层 API 继续使用 controller_url，浏览器运行 Endpoint 不变。

前端新增先测试再提交，失败不创建实例；新增、编辑与列表没有管理/采集开关。移出 Hub 使用删除并明确选择历史保留或清理。编辑中的测试仅验证当前表单，保存是独立操作。前端采用 POST/PATCH/DELETE 的提交结果更新列表，旧轮询不得覆盖变更，列表刷新失败不得解释为写入失败。网络中断导致写响应未知时，提示先刷新确认结果，避免盲目重试。

Schema 3 将名称唯一约束改为 `WHERE deleted_at_ms IS NULL` 的唯一索引。启动时通过事务重建父表并保留所有 UUID/凭据/历史引用；迁移期间暂关外键以避免级联删除，完成后恢复。旧二进制拒绝打开新 schema；恢复旧版需同时恢复迁移前备份。

Schema 4 将未删除实例统一设置为启用管理与采集，变更记录的 generation 加一；已启用记录、删除墓碑及历史表保持原状。旧版本恢复需使用升级前备份。

顶部实例菜单将当前视图标为“显示中”，其他健康实例标为“采集中”，异常状态显示实际采集状态；顶部标签可直接切换。切换与页面关闭都不改变服务端采集。

## Mihomo 原生 Gateway

前缀 `/mihomo/{UUID}`；请求只能访问已注册实例，接入后统一启用。无 Session 401、不存在 404、禁用 403、非法路径/浏览器 token 参数 400、权限不足 403、上游超时 504、其他网关错误 502。上游自身的 4xx/5xx 作为原生响应透传。

| 方法 | 路径 | 行为 |
|---|---|---|
| GET | `/version`、`/configs`、`/proxies`、`/rules`、`/connections` | 原生查询 |
| GET | `/proxies/{name}`、`/proxies/{name}/delay`、`/group`、`/group/{name}/delay` | 原生查询/测速 |
| GET | `/providers/{proxies\|rules}` 及 Provider/节点健康检查路径 | 保留 Query、204 与状态码 |
| GET + WS Upgrade | `/connections`、`/traffic`、`/memory`、`/logs` | 保留子协议、数据帧、Ping/Pong 和 Close；不协商压缩 |
| PUT / DELETE | `/proxies/{name}` | 切换节点 / 取消固定选择 |
| DELETE | `/connections`、`/connections/{id}` | 关闭连接 |
| PATCH | `/rules/disable` | 原生运行时规则状态 |
| PATCH | `/configs` | 只允许 mode、log-level、ipv6、sniffing、tcp-concurrent、unified-delay、find-process-mode |

所有控制操作仅单管理员可用，并受 `allow_runtime_writes` 控制。Gateway 写请求采用严格同源 Origin/Referer 作为等效 CSRF 防护，以兼容原版 XD。原始 JSON Body 不重序列化。未知操作、PUT configs、Provider 更新、升级、重启、缓存/geo 管理均返回 403；即使 XD 出现相关按钮也不会执行。

编码后的路径（包括名称里的编码斜杠）、重复 Query 键与原始转义保持不变；拒绝路径遍历、坏转义、控制字符和客户端 `token` 参数。服务端注入 Bearer Secret；WS 的 token 仅存在于 Go 到 Controller 的请求中。

只向上游传递必要的内容协商、条件查询和 WebSocket Header；浏览器 Cookie、Authorization、Origin、Forwarded/X-Forwarded-*、CSRF Header 不透传。上游 Set-Cookie、鉴权/转发/CORS Header 与 Trailer 被过滤。HTTP 重定向拒绝，不跟随，也不向浏览器泄漏 Location。响应设置 no-store；附加 CSP sandbox 禁止错误返回的上游 HTML 在平台同源执行脚本，原生状态、类型和正文不变。REST 原生体受大小限制并有界缓冲，NDJSON/SSE 的无限 HTTP 流不作为 P0 兼容目标，请使用 WS。

WS 保持文字与二进制数据，按双向累计消息限制（含分片），同时限制连接数量。实例 generation 取消、注销、过期、进程退出关闭两端 socket；异常断线与超限按连接失败报告，不会伪造成功。

`/api/control/*` 始终 404，不报告 `hasAgent` 或 profiles/kernel-control 能力。`/api/*`、`/mihomo/*` 错误不会回退到 UI HTML。


## P1 状态与诊断

`GET /api/v1/instances/{id}/status` 需 Session。状态为 `disabled`、`connecting`、`online`、`degraded` 或 `capacity_limited`。返回 `last_seen_at_ms`、`last_connected_at_ms`、`last_snapshot_at_ms`（未知为 null）、`last_error_code`、`reconnect_count`、`samples_committed`、`dropped_samples`、`collector_lag_ms`。内存计数随进程/worker 重建清零；持久游标恢复上次快照时间。online 需成功提交有效采样，不仅是握手成功。

错误码包括 `connect_failed`、`stream_interrupted`、`target_rejected`、`invalid_snapshot`、`invalid_snapshot_type`、`invalid_counters`、`writer_backpressure`、`storage_error`。不返回带凭据的原始上游错误。

`GET /api/v1/system/status` 需 Session，返回版本、schema、数据库及 WAL/SHM 合计字节、各 Collector 状态、写池等待计数/时长，以及 Writer 的 `queue_length/queue_capacity/dropped_samples/write_failures/commits/last_write_ms`。Writer 丢弃数统计被拒绝入队的遥测任务；状态任务也可能计入。

## P1 历史查询

全部要求 Session，使用 Platform 包装。支持六个端点：`/api/v1/analytics/{summary,traffic,top,connections,gaps,coverage}`。

| 参数 | 契约 |
|---|---|
| `instance_ids` | 缺省或 `all` 为全部（包含 retain 墓碑）；否则逗号分隔唯一 UUID，最多 256 个；不存在 ID 返回 404 |
| `from/to` | RFC3339 UTC，区间 `[from,to)`，存储/响应 UTC 毫秒；默认最近完整的 24 小时，窗口最多 366 天 |
| `step` | `1m/1h/1d`；缺省窗口 ≤24h 选 1m，≤60d 选 1h，其余选 1d；超过 2000 点要求改大步长 |
| `dimension` | TOP 必填，白名单 `host/proxy/rule/process/src_ip` |
| `limit/offset` | TOP、连接、缺口分页；默认 100/0，limit 1..500，offset 0..100000 |

汇总、趋势、TOP 和 coverage 边界须对齐 UTC 分钟，1h/1d 使用小时数据，边界须对齐 UTC 小时。1d 按 UTC 日分组，首尾可以是部分日。连接窗口最多 31 天。连接/缺口可使用未对齐边界，其 coverage 仅计窗口内完整存储桶，作为保守下界。重复/未知参数、非 UTC、非法步长或维度均 400。只读查询限时 5 秒，存储失败返回 503。

每个历史响应 `meta` 包含 source、指标定义、覆盖率和明确检测的 gap_count（包括当前开放区间）：

```json
{
  "source": "mihomo_connections_ws",
  "metric_definition": "observed_counter_deltas; connection attribution is sampled; recovery candidates excluded; coverage is time, not accuracy",
  "from_ms": 1793923200000,
  "to_ms": 1794009600000,
  "step": "1m",
  "has_gap": true,
  "gap_count": 1,
  "coverage": [{
    "instance_id": "<UUID>",
    "name": "controller-a",
    "deleted": false,
    "observed_ms": 1000,
    "window_ms": 86400000,
    "sample_coverage": 0.000011574074074074073,
    "first_retained_minute_ms": 1793923200000,
    "first_retained_hour_ms": 1793923200000,
    "last_snapshot_at_ms": 1793923201000,
    "has_gap": true
  }]
}
```

覆盖分母是每个所选实例的完整请求窗口，包括注册前、禁用、离线和超出保留期的时间。空数据的字节为 0，结合 coverage=0/has_gap=true 识别未观测区间，不能理解成精确的零使用。最早保留桶为 null 表示没有该分辨率数据；1h/1d 不会从已清理的分钟表重新构造。

- `summary.data`：`total_upload/total_download`、`attributed_upload/attributed_download`、`unattributed_upload/unattributed_download`、`consistent`。归因大于总量时对应 unattributed 为 null、consistent=false；不裁剪归因或伪造维度。
- `traffic.data`：按时间排序的桶数组；每项 `bucket_ms` 加上述方向性字节、`observed_ms`（各实例采集时长之和）、`has_gap`。空桶也返回。字节记在观测增量的分钟，非精确发生时刻。
- `top.data`：`value/upload_bytes/download_bytes` 数组，按上下行总和降序；仅汇总一个维度，禁止跨五维再相加。proxy 使用首次观测链路的 `chains[0]`，缺失维度为 `(unknown)`，rule 为 `rule:rulePayload`。
- `connections.data`：按最近观测倒序，包含实例、epoch、生命周期 UUID、Mihomo ID、首次/末次观测、关闭时间及原因、首次维度和 `lifetime_upload_bytes/lifetime_download_bytes`。这些是该生命周期观测到的累计值，包含首次基线，**不是查询窗口内归因字节**。按观测重叠筛选；关闭时只知道最后观测量，未采到的尾部不可恢复。
- `gaps.data`：`id/instance_id/epoch_id/from_ms/to_ms/reason/resolution/candidate_upload_bytes/candidate_download_bytes`。候选值仅在相同来源、单调恢复时存在，`resolution=uncertain_epoch_and_time`；不计入 summary/traffic。持续等待快照的实例补充 `ongoing/<id>`，`to_ms=null`、`reason=awaiting_snapshot`，这是当前状态派生区间，恢复后被持久恢复记录替代。
- `coverage.data`：与 meta.coverage 相同的逐实例数组。

计数回退或 Controller URL 变更开启新 epoch；普通 WS/Go 重启不直接伪装成内核重启。跨重连/进程重启会重新建立连接归因基线，保留旧游标用于恢复候选量判断。

## P3 版本与连接测试

`GET /api/v1/meta` 要求 Session，返回 `version/backend_commit/frontend_commit/frontend_embedded/build_time/api_version/schema_version`。开发构建的 frontend_commit 为空；静态目录覆盖模式的 embedded=false。正式构建写入两个仓库的实际 Commit。原 P0 的 `frontend_contract_commit` 字段已由实际 `frontend_commit` 替代。

`POST /api/v1/instances/{id}/test` 要求 Session、同源请求和 CSRF，受平台变更速率限制，并写入审计。只请求已注册且启用的 Controller `/version`，5 秒超时，不跟随重定向；最多读取 64KiB。成功返回 `{reachable:true,version:"v1.19.33"}`，失败返回不含目标/密钥/原始响应的 502。禁用实例为 403，不存在为 404。变更实例时取消旧 generation 的测试请求。此测试不修改配置，不启动后台采集。
