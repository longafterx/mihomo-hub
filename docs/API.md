# Platform API v1 与 Mihomo Gateway

仅 `/api/v1` 使用 `{"data":...,"meta":{},"error":null}` 包装。失败返回对应 HTTP 状态，`error={"code":"...","message":"..."}`；不会回显输入、SQL 或 Controller 错误 URL。Gateway 保留 Mihomo 原生格式。

## 认证

| 方法和路径 | 权限 | 行为 |
|---|---|---|
| `GET /login` | 公开 | 后端提供的首次密码设置/登录页，独立于 XD 源码 |
| `GET /api/v1/auth/setup` | 公开 | 返回 `setup_required` 和密码字节长度边界，不返回账号、凭据或密钥 |
| `POST /api/v1/auth/setup` | 同源、仅首次 | JSON `password/confirm_password`；原子创建单管理员并自动设置 Session Cookie |
| `POST /api/v1/auth/login` | 同源 | JSON `password`（兼容并忽略旧 `username`）；设置 HttpOnly Session Cookie |
| `GET /api/v1/auth/me` | Session | `csrf_token/expires_at_ms` |
| `POST /api/v1/auth/logout` | Session + CSRF | 撤销会话并断开其 WS，清除 Cookie |
| `GET /api/v1/meta` | Session | 后端/前端版本、API/schema 版本与构建信息 |

管理员密码是 MihomoHub 网页登录密码，与实例 Controller Secret 独立；首次设置要求 8–1024 字节，不强制大小写、数字或符号组合。空数据库直接启动，自动迁移建表和创建主密钥，打开根地址跳转到 `/login` 完成首次设置。现有密码哈希保持兼容，无需重新设置；登录页只要求密码，数据库保留旧用户名仅作内部标识。

首次设置和审计在同一事务内完成，并发请求只允许一个成功，重复设置返回 409 `already_initialized`，不覆盖密码；确认不一致或长度不符返回 400 `invalid_password`。尚未设置时登录返回 409 `setup_required`，其他管理 API 和 Gateway 仍返回 401。状态接口始终 `no-store`，网页遇到其他浏览器已完成设置时切回普通登录。`/readyz` 表示服务可用，待设置密码时也返回 200。已存在管理员或实例但缺少主密钥时拒绝启动，要求恢复原密钥，禁止自动生成替代密钥。

Cookie 名为 `mihomohub_session`，Path=/，SameSite=Strict，HTTPS Origin 下 Secure。只保存 Session ID 的 SHA-256 哈希；每次成功登录生成新 Session 并撤销本浏览器旧 Session。最多 1024 活跃 Session；设置密码与登录共用两个并行哈希计算名额、按物理 IP 每分钟 10 次请求的限流（含成功）。

所有平台写请求（登录和首次设置密码除外）同时要求 Cookie、`X-CSRF-Token` 和同源 Origin/Referer；登录和首次设置也要求同源 Origin/Referer。CLI 需手动设置 `Origin`。WS 严格要求 Origin，不能仅用 Referer。已认证写请求按 Session 限制每分钟 120 次。

正式发布的 `/meta` 提供实际 frontend_commit 与 frontend_embedded；外部静态目录覆盖时 embedded=false，详见末尾 P3 版本说明。

## 实例 CRUD

| 方法和路径 | 结果 |
|---|---|
| `GET /api/v1/instances` | 200，数组，空列表为 `[]` |
| `POST /api/v1/instances` | 201，创建实例 |
| `POST /api/v1/instances/test` | 200，只读验证未保存的连接配置，不新增或修改实例 |
| `GET /api/v1/instances/{id}` | 200，详情 |
| `GET /api/v1/instances/retained` | 200，已删除实例目录，仅 ID、名称、删除时刻 |
| `GET /api/v1/instances/{id}/config` | 200，仅管理员编辑时按需读取 `controller_url`，不返回 Secret |
| `PATCH /api/v1/instances/{id}` | 200，部分更新 |
| `DELETE /api/v1/instances/{id}?history=retain\|purge` | 200，删除结果 |

创建字段：`name`（非空，至多 128 字节，未删除实例间唯一）、`controller_url`（HTTP(S) 根 URL）、`secret`（默认空，至多 4096 字节，不含 CR/LF）。接入即启用管理和后台采集；`enabled` 与 `collect_enabled` 可省略，兼容旧客户端显式 `true`，显式 `false` 返回 400 `invalid_instance`。不接受用户提供 ID、URL userinfo、基路径、query、fragment 或任意未知字段。

`enabled`、`collect_enabled` 在未删除实例的响应中恒为 `true`，仅保留 API 兼容，不再作为独立开关持久化；实例是否仍受管理由删除标记决定。

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

`retain` 创建删除墓碑并移除 URL/密文，保留实例 ID 和名称供后续历史表关联；名称不再占用，可以重新新增；新实例分配新 UUID，不继承旧历史。`purge` 物理删除，也适用于已经 retain 的身份；不存在或已清理的 ID 返回 404。实例管理页“已保留的历史”提供带确认的清理入口，不影响同名新实例。P1 中所有历史表通过外键关联，purge 会级联清理；retain 保留无凭据墓碑及历史，单管理员仍可按 ID 查询。未指定选项拒绝删除。主动 `/test` 已实现只读版本探测，详见末尾说明。状态与历史接口见下文。

`POST /api/v1/instances/test` 要求 Session、CSRF 和同源校验，接受 `instance_id?`、`controller_url?`、`secret?`、`auto_detect?`。指定实例时省略字段沿用已保存配置（包括密钥）；`secret: ""` 明确测试无密钥连接。未指定实例时必须提供 URL。沿用 Gateway 地址和端口策略，包含 DNS 与拨号检查，最多 5 秒，只读取 `/version`。成功返回 `reachable/version/controller_url`；`auto_detect: true` 先执行不携带凭据的 TLS 握手，可信 HTTPS 直接使用，只有明确收到 HTTP 响应才尝试内网 HTTP；证书错误和握手中断不降级。失败 502，按原因返回 `controller_connection_refused`、`controller_timeout`、`controller_auth_failed`、`controller_dns_failed`、`controller_tls_failed`、`controller_redirected`、`controller_http_error`、`controller_invalid_response` 或 `controller_unavailable`，不回显上游错误。测试写审计但不修改注册表、generation、凭据或历史。

前端地址表单只要求 IP/主机与端口（默认 9090），新增及修改地址时由后端识别 HTTP/HTTPS，编辑未改变的地址仍保留原协议；底层 API 继续使用 controller_url，浏览器运行 Endpoint 不变。

前端新增或修改连接地址先测试再提交，失败不创建或替换连接地址；新增、编辑与列表没有管理/采集开关。移出 Hub 使用删除并明确选择历史保留或清理。编辑中的测试仅验证当前表单，保存是独立操作。前端采用 POST/PATCH/DELETE 的提交结果更新列表，旧轮询不得覆盖变更，列表刷新失败不得解释为写入失败。网络中断导致写响应未知时，提示先刷新确认结果，避免盲目重试。

Schema 3 将名称唯一约束改为 `WHERE deleted_at_ms IS NULL` 的唯一索引。启动时通过事务重建父表并保留所有 UUID/凭据/历史引用；迁移期间暂关外键以避免级联删除，完成后恢复。旧二进制拒绝打开新 schema；恢复旧版需同时恢复迁移前备份。

Schema 4 将未删除实例统一设置为启用管理与采集，变更记录的 generation 加一；已启用记录、删除墓碑及历史表保持原状。旧版本恢复需使用升级前备份。

顶部实例菜单将当前视图标为“显示中”，其他健康实例标为“采集中”，异常状态显示实际采集状态；顶部标签仅展示，悬浮按需测试连接延迟，手动切换统一使用下拉菜单。切换与页面关闭都不改变服务端采集。

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
| PATCH | `/configs` | 允许原运行字段以及 port、socks-port、mixed-port、redir-port、tproxy-port、allow-lan、interface-name、tun；DNS 走平台完整配置接口 |
| GET | `/dns/query` | 原生 DNS 查询，保留 name/type 参数 |
| PUT | `/configs` | 仅重载设备当前配置；path/payload 必须为空 |
| PUT | `/providers/{proxies\|rules}/{name}` | 更新单个 Provider；中文和编码斜杠保持不变 |
| POST | `/restart`、`/configs/geo`、`/cache/dns/flush`、`/cache/fakeip/flush` | 重启内核、更新 Geo、清理缓存；不接受操作参数 |

所有控制操作仅单管理员可用，并受 `allow_runtime_writes` 控制。Gateway 写请求采用严格同源 Origin/Referer 作为等效 CSRF 防护，以兼容原版 XD。原始 JSON Body 不重序列化。未知操作、升级、任意配置路径/任意 payload、Controller 监听与密钥修改仍返回 403。原生 PATCH 不支持 DNS 字段，拒绝它以避免假成功。维护操作使用 `maintenance_timeout_seconds`（默认 180 秒）；不自动重试。原生错误和版本不支持的 404/405 保留，并由页面显示失败原因；网络中断提示结果未确认，而非假成功。

编码后的路径（包括名称里的编码斜杠）、重复 Query 键与原始转义保持不变；拒绝路径遍历、坏转义、控制字符和客户端 `token` 参数。服务端注入 Bearer Secret；WS 的 token 仅存在于 Go 到 Controller 的请求中。

只向上游传递必要的内容协商、条件查询和 WebSocket Header；浏览器 Cookie、Authorization、Origin、Forwarded/X-Forwarded-*、CSRF Header 不透传。上游 Set-Cookie、鉴权/转发/CORS Header 与 Trailer 被过滤。HTTP 重定向拒绝，不跟随，也不向浏览器泄漏 Location。响应设置 no-store；附加 CSP sandbox 禁止错误返回的上游 HTML 在平台同源执行脚本，原生状态、类型和正文不变。REST 原生体受大小限制并有界缓冲，NDJSON/SSE 的无限 HTTP 流不作为 P0 兼容目标，请使用 WS。

WS 保持文字与二进制数据，按双向累计消息限制（含分片），同时限制连接数量。实例 generation 取消、注销、过期、进程退出关闭两端 socket；异常断线与超限按连接失败报告，不会伪造成功。

`/api/control/*` 始终 404，不报告 `hasAgent` 或 profiles/kernel-control 能力。`/api/*`、`/mihomo/*` 错误不会回退到 UI HTML。


## 完整 YAML 与 DNS 热加载

`/api/v1/instances/{id}/runtime-config` 是平台接口，不伪造 Mihomo 或 Agent 协议：

| 方法 | 路径 | 行为 |
|---|---|---|
| GET | `/runtime-config` | 返回 imported/revision/updated_at_ms/dns，不返回完整 YAML |
| PUT | `/runtime-config` | `{yaml,revision}` 导入该实例完整配置；只加密保存，不立即应用 |
| PATCH | `/runtime-config/dns` | `{dns,revision}` 合并 DNS 并通过 Controller `PUT /configs?force=true` 热加载整份配置 |

GET 要求 Session；PUT/PATCH 要求 Session、CSRF、同源与 `allow_runtime_writes=true`，使用平台变更限流和审计。revision 初始为 0；导入和应用成功均递增。过期提交返回 409 `runtime_config_conflict`。必须导入单文档 YAML，包含 rules/proxies/proxy-providers 中至少一个完整配置部分，不能只有 DNS 片段。浏览器文件限 512 KiB；平台 JSON 请求仍受 max_body_bytes 限制。

DNS 可编辑字段为 enable、listen、enhanced-mode（fake-ip/redir-host）、nameserver、fallback、fake-ip-range、use-hosts；其余 DNS 策略和完整配置保留。完整热加载同时应用导入配置中的代理、规则、端口等，不读取部分 `/configs` 响应来拼装配置。导入来源由管理员负责；在设备上修改配置后需重新导入。URL 改变后旧 YAML 不可应用；删除实例即使 retain 历史也清除 YAML。完整 YAML 使用实例及用途绑定的 AEAD 加密，纳入数据库一致性备份，主密钥单独保管。

`imported=true` 仅表示已保存基准，不表示它与当前内核一致。Mihomo 无原生完整 YAML/DNS 配置读取接口，Hub 无法检测外部改动。热加载不写设备文件，重载/重启后以设备文件为准。请求超时或断线不能证明操作未执行；返回 `runtime_config_unconfirmed`，应先检查状态和日志。内核接受但 Hub 保存失败返回 503 `runtime_config_save_failed`，不得盲目重试。Schema 5 新增加密配置表，不改动现有实例和历史。

## P1 状态与诊断

`POST /api/v1/instances/{id}/test` 使用已保存的 Controller 地址与服务端 Secret，仅请求 `/version`。成功返回 `{reachable:true,version,latency_ms}`，其中 `latency_ms` 是 Go 测得的 Hub → Controller HTTP 请求耗时（整数毫秒，包含连接建立，不包含浏览器链路和审计写入），小于 1 毫秒时为 0；它不是代理节点延迟或 ICMP ping。草稿测试 `/api/v1/instances/test` 返回同样字段。失败不返回成功延迟，保留原有脱敏错误码；均要求 Session、同源、CSRF 并受平台限流控制。

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

## 更新连续性与会话恢复

仅修改名称、提交相同连接参数不递增 generation，也不重建采集与 Gateway 流。连接参数实际变化先原子保存，保存失败保留原会话；保存成功后递增 generation，数据库写入检查拒绝旧采样，随后取消旧会话。删除、密钥变更、注销与服务关闭仍会撤销关联连接。

平台客户端遇到明确的 403 `csrf_failed` 时合并刷新 `/auth/me` 并最多重发一次，适用于另一个标签页重新登录后 Cookie 已更新、旧页仍缓存旧 CSRF 的场景。该拒绝发生在执行变更之前；网络中断、普通 403、5xx 均不重试变更。

历史查询显式 `instance_ids` 最多选择 256 个；省略或使用 `all` 的全局汇总不受历史身份总数 256 的限制，仍受时间范围、点数、分页与查询超时限制。
