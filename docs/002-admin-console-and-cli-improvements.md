# PortPilot 管理、可观测性与 CLI

## 1. 文档定位

本文记录 PortPilot 已实现的管理后台、审计、连接活动、SQLite migration 和命令行契约。
底层控制协议、smux、UDP-over-TCP、listener 与 session 生命周期见
[`001-portpilot-design.md`](001-portpilot-design.md)，终态聚合、Top K、GeoIP 和历史洞察
交互见 [`003-network-history-analytics.md`](003-network-history-analytics.md)。运行状态、全局搜索、
周期对比、导出和 cache 生命周期见
[`004-operational-insights-and-navigation.md`](004-operational-insights-and-navigation.md)。

稳定边界如下：

- 客户端与代理配置文件是配置事实来源，SQLite 始终可丢弃。
- Go 服务端不处理 TLS；管理 API 和控制/数据入口由部署侧 Nginx 提供 TLS。
- 管理员、客户端和数据 ticket 使用相互独立的认证域。
- 正常代理流量进入“网络活动”；运行态拒绝和转发异常进入结构化日志，管理操作、配置变更、
  客户端身份/生命周期和系统生命周期进入“事件与审计”。
- Web 不使用浏览器原生 `alert`、`confirm` 或 `prompt`。

## 2. 客户端配置元数据

客户端配置包含以下管理字段：

```json
{
  "client_id": "office-a",
  "client_token": "<random-secret>",
  "remark": "上海办公室出口",
  "enabled": true,
  "created_at": 1784449800000,
  "updated_at": 1784452200000,
  "proxy_list": []
}
```

`remark` 是必填的可读名称。`created_at` 和 `updated_at` 是服务端权威的 UTC Unix
毫秒 `int64`，在配置文件和 API 中均使用 JSON number：

- 创建客户端时，两个时间由服务端写入。
- `created_at` 创建后保持不变。
- 备注、启停、token 或代理规则发生语义变化时，`updated_at` 单调前进。
- 请求中的两个时间值不能覆盖服务端时间。
- 客户端上线、下线、重连和流量变化不修改配置时间。

旧配置由服务端在开放 listener 前补全。缺少的备注使用 `client_id`，缺少的时间使用配置
文件 mtime；补全在配置锁内原子提交、增加 revision，并写入脱敏审计。该时间表示首次可推定
的配置时间，不代表精确历史创建时间。

客户端 ID 全局唯一且不可修改。需要更换 ID 时应创建新客户端并迁移代理规则。

## 3. 管理员会话

登录请求可指定 `session_ttl`：

| 值 | 有效期 |
| --- | --- |
| `10m` | 10 分钟 |
| `30m` | 30 分钟，缺省值 |
| `1h` | 1 小时 |
| `24h` | 24 小时 |

会话使用绝对过期时间，不采用滑动续期。`/api/auth/me` 返回
`authenticated_at`、`expires_at` 和 `session_ttl`；Cookie 的 `Max-Age` 与 `Expires`
和服务端会话一致。服务端重启会清空内存会话。

登录成功、失败和限流拒绝都写入审计。审计包含规范化用户名、来源、截断后的 User-Agent、
所选 TTL、结果和原因，不包含密码、password hash、Cookie 或 CSRF secret。

所有管理写接口同时要求有效管理员会话和 CSRF token。服务端主配置的 `real_ip_header`
为空时使用规范化 socket `RemoteAddr`；非空时读取同名 header 的第一个逗号分隔 IP，空值或
非法值回退 socket peer。该规则同样用于客户端控制 WebSocket，生产 Nginx 必须以
`$remote_addr` 覆盖同名 header。非法 header 写限频结构化警告日志，不进入审计。
动态 TCP/UDP 代理 listener 不读取 HTTP header。

## 4. 客户端和代理管理

客户端窄接口：

| API | 用途 |
| --- | --- |
| `GET /api/clients` | 查询备注、ID、启停、在线、Agent、控制会话、mux、代理数及配置时间 |
| `GET /api/clients/{client_id}` | 查询完整 Agent、能力、来源、心跳 RTT 和最近离线信息 |
| `POST /api/clients` | 创建客户端并返回一次性 token |
| `PATCH /api/clients/{client_id}` | 修改备注和启停状态 |
| `POST /api/clients/{client_id}/rotate-token` | 轮换 token 并关闭旧控制/数据会话 |
| `POST /api/clients/{client_id}/disconnect` | 主动断开当前客户端会话 |

创建和轮换响应中的 `one_time_token` 只出现一次。后续查询只返回脱敏标记；Web 只在 React
内存中持有该值，用户确认保存后立即清除。一次性 token 不进入 URL、浏览器存储、审计或
SQLite。

配置写入使用 `expected_revision` 乐观锁。配置管理器还使用进程内写锁、稳定 lock file、
同目录临时文件、fsync 和原子 rename。冲突返回 `409`，调用方重新读取后决定是否重试。

代理创建 Dialog 始终包含客户端选择器。当前筛选为空时仍可打开 Dialog，但未选择具体
客户端时保存按钮不可用。代理启停继续通过 revision-safe 配置写入；删除和 token 轮换使用
自绘确认窗体。

代理编辑器提供三个稳定配置区：

- 外部访问：可选择“仅内网访问”或代理级 canonical CIDR allow/deny，并只读展示服务端全局
  策略；仅内网模式会禁用 CIDR 输入框，保存时只持久化模式标记。
- 目标来源传递：TCP 可开启 PROXY Protocol v1；UDP 固定关闭。
- 连接限速：上行、下行分别配置不限速或 KiB/s、MiB/s、GiB/s。

代理列表展示有效来源策略、PROXY Protocol 和双向限速摘要。客户端列表展示版本、OS/arch
和最后通信，详情 Drawer 展示完整 Agent、capabilities、控制来源、epoch、RTT 与离线原因。
总览的客户端状态同时展示最后心跳；点击任意客户端直接打开同一详情 Drawer。每行末尾提供
网络活动图标入口，跳转后自动进入客户端视图并按对应 `client_id` 筛选。

## 5. 事件与审计

“事件与审计”回答谁在何时对什么执行了什么操作，以及结果和原因。主要事件包括：

- 管理员登录、退出和会话过期；
- 客户端创建、修改、token 轮换和主动断开；
- 配置更新及客户端/代理实体差异；
- 客户端 session 生命周期；
- 客户端 Agent 信息非法；
- 数据库重建、配置元数据补全和服务启停。

外部来源策略拒绝、UDP 丢包、PROXY Protocol 写入失败以及 challenge、ticket、epoch、帧、
握手限流等运行态安全拒绝只写结构化日志，不进入审计。高频日志按稳定维度限频；外部来源
拒绝仍保留精确聚合指标，连接级失败仍进入“网络活动”。正常 payload 不写日志或审计。

### 5.1 数据模型

`audit_events` 保存事件摘要、时间、分类、结果、actor、操作者来源 IP、客户端/代理/连接关联、
reason code 和脱敏结构化上下文。`audit_config_changes` 保存配置事件的实体级
create/update/delete 以及脱敏 before/after。

配置差异由服务端根据已验证的旧配置和规范化新配置计算。token 键被删除，只能记录
`token_configured`；password、session secret、CSRF、ticket 和 payload 不得进入差异。

配置修改先同步创建 `pending` 审计和差异，再写配置文件，最后收敛为 `success` 或
`failure`。服务启动时按配置 revision 恢复遗留 `pending`，无法证明结果时标记 `unknown`。
审计不可写时拒绝新登录和配置修改，但不停止已有代理流量。

### 5.2 查询

```http
GET /api/events?page=1&page_size=25&query=&category=&event_type=&outcome=&date_from=&date_to=
GET /api/events/{event_id}
```

`page_size` 只允许 25、50 或 100。日期为 RFC3339，使用
`date_from <= occurred_at < date_to` 的半开区间。列表按时间和 ID 稳定倒序；详情返回完整
脱敏上下文和配置差异。

Web 提供搜索、分类、事件、结果、日期、分页和详情 Drawer。列表单独展示 IP 列；管理员主动
触发的登录、配置和客户端管理事件显示规范化操作者 IP，非管理员事件显示 `-`。“事件与审计”
固定为导航最后一项。

## 6. 网络活动

“网络活动”保存连接生命周期和 payload 计数，不保存业务载荷：

- TCP：每个外部 TCP 连接对应一条活动记录。
- UDP：每个 `(client_id, proxy_id, external_source)` flow 对应一条活动记录。
- 上传指外部用户到内网目标，下载指内网目标到外部用户。
- TCP 字节只计算代理 payload；UDP 字节只计算原始 datagram payload。
- UDP 包数按成功转发的数据报计数。

活动状态为 `accepted`、`connecting`、`active`、`closing`、`closed`、`failed` 或
`interrupted`。

连接模型同时保存建链时或当前的限速与 PROXY Protocol 状态：

- `up/down_limit_bytes_per_second`；
- TCP `up/down_throttled_millis`；
- UDP `up/down_rate_limited_packets/bytes`；
- `proxy_protocol_enabled/version/header_sent`。

实时连接在当前速率旁显示配置上限；详情显示 TCP 累计限速等待、UDP 限速丢包和 PROXY
Protocol header 状态。正常流量和历史 Top K 只统计成功转发的 payload。

### 6.1 实时和持久化

进程内 activity registry 保存活跃连接、不可变上下文和原子计数。relay 成功读写时只更新
内存计数，不为每个 chunk 或 datagram 写 SQLite。

registry 使用独立的一秒内存采样计算最近窗口的上下行平均速率，单位为 bytes/s。该采样不
触发 SQLite 写入，也不与可配置的持久化采样周期绑定。实时快照返回
`up_bytes_per_second`、`down_bytes_per_second`、`rate_sampled_at` 和
`rate_window_millis`。这些字段是进程内瞬时观测值，不进入 SQLite；首次采样前速率为零并明确
标记为等待采样。

有界后台 writer 负责：

- 创建、状态变化和最终连接记录；
- 按采样周期写入非零流量增量；
- 更新全局和客户端小时 rollup；
- 按保留策略分批清理。

网络持久化故障不能阻塞数据面。活跃视图来自 registry；SQLite 恢复后最终累计优先写入。
活动数量受 `observability.max_active_connections` 限制。

服务启动时，数据库中未结束的连接被标记为 `interrupted`，reason code 为
`server_restart`。细粒度 sample 过期后，连接详情仍保留最终累计和生命周期。

### 6.2 查询

```http
GET /api/network/summary?range=24h&client_id=
GET /api/network/connections?scope=active&page=1&page_size=25&query=&client_id=&protocol=
GET /api/network/connections?scope=history&page=1&page_size=25&query=&client_id=&protocol=&state=&date_from=&date_to=&sort=
GET /api/network/connections/{activity_id}
GET /api/network/clients?page=1&page_size=50&query=
```

汇总范围只允许 `1h`、`24h`、`7d` 和 `30d`。`scope=active` 只读取 registry，返回进程
当前仍存活的 `accepted`、`connecting`、`active` 或 `closing` 连接；数据库中尚未完成异步
收尾的记录不能进入实时结果。`scope=history` 只读取 SQLite 中的 `closed`、`failed` 或
`interrupted` 终态记录。状态与 scope 不兼容时返回 `400 invalid_query`。未传 scope 时保留
registry 与 SQLite 合并、按 activity ID 去重的兼容行为。排序只允许接受时间、流量或持续
时间的固定枚举。

Web 提供整体和单客户端视图、时间范围、指标带、流量趋势、客户端汇总及连接详情。“连接”
使用并排的“实时连接 / 历史连接”双选项，一次只展示用户选中的一栏；两栏保留各自的分页和
缓存结果。搜索、客户端和协议筛选同时作用于两栏，状态与排序只作用于历史连接。实时表展示
当前上行/下行速率、采样窗口和累计流量；历史表展示最终状态和累计流量。详情中的细粒度
sample 以独立流量图展示。

页面可见时只定时刷新当前选中的连接栏，隐藏时停止轮询，旧请求通过 `AbortController` 取消。
后台刷新期间保留已有数据或等高空态，表格使用固定列布局和等宽数字，趋势图关闭刷新动画，
避免速率位数或空结果刷新引发页面重排。趋势图在 SVG 绘图区内为坐标轴标签保留安全边距，
最高、最低刻度以及较长的流量单位不得被容器裁剪。视图、连接栏、客户端、范围、筛选、排序、
搜索和两个页码保存在 URL query 中，刷新或分享链接后可以恢复，URL 不包含 secret。窄屏实时表
保留时间、对象和双向速率，历史表保留时间、对象、状态和总流量，其余上下文进入全屏详情。

### 6.3 历史洞察

“网络活动”内部提供“历史洞察 / 连接明细”一级切换，默认进入历史洞察，不在左侧导航新增
页面。历史洞察只统计终态连接，提供客户端/代理维度、连接数与流量 Top K、国家分类和来源 IP
曲线。完整统计口径、API、保留与 GeoIP 契约见
[`003-network-history-analytics.md`](003-network-history-analytics.md)。

## 7. SQLite migrations

业务 DDL 只允许位于根目录 `migrations/*.sql`。`migrations/embed.go` 将 SQL 嵌入 server，
store 在开放任何端口前执行 migration。

文件名使用四位版本与小写描述，例如 `0001_init.sql`。migration 必须严格递增、DDL-only，
并在单个事务内提交业务 DDL 和 `schema_migrations` ledger。ledger 保存版本、文件名、
SHA-256 checksum 和应用时间。

每次启动都会：

1. 发现并校验 migration 文件；
2. 校验已应用文件名和 checksum；
3. 应用 pending migrations；
4. 对实际 schema 与完整 migration 结果进行 drift 校验；
5. 执行 SQLite integrity 和 foreign-key 检查。

具备 ledger 的数据库发生 checksum 变化、schema drift、损坏或升级失败时，服务端失败退出，
不会自动丢弃数据库。

旧的无 ledger PortPilot 数据库会先通过 SQLite 一致性备份保留，再重建为空库并应用完整
migration。SQLite 被手动删除后也会从 migrations 重建。两种情况都不改变客户端、token 或
代理配置，自动备份按数量保留。

`observability` 配置控制审计、连接、sample 保留时间、采样/清理周期和最大活动数。服务端
严格校验范围；清理使用有界批次，不能删除活跃连接或阻塞 relay。

健康接口公开 `database`、`audit_writer`、`activity_writer` 和 `migration_version`，不公开
数据库路径、SQL 或 secret。

`client_agent_snapshots` 只保存每个 client ID 最近一次经过鉴权的 Agent 与控制会话快照。
在线值以内存 session 为准；last seen 最多每 60 秒异步落库一次，离线时强制写最终时间和
归一原因。SQLite 删除后客户端重连会自动重建快照，不影响身份、代理配置或 listener。

## 8. Cobra 命令行

服务端命令树：

```text
portpilot-server
├── run
├── validate-config
├── password-hash
├── version
└── completion
```

客户端命令树：

```text
portpilot-client
├── run
├── validate-config
├── version
└── completion
```

根命令不再隐式启动服务；运行时必须显式使用 `run --config <path>`。`validate-config`
严格解析配置但不监听端口。`completion` 支持 bash、zsh、fish 和 powershell。

生成管理员 bcrypt hash：

```bash
portpilot-server password-hash
printf '%s\n' 'password' | portpilot-server password-hash --password-stdin
```

交互模式从终端无回显读取并二次确认。stdin 模式读取一行，适合自动化。cost 允许 10 至
16，默认 12；空密码、超过 bcrypt 72-byte 限制或确认不一致都会失败。命令没有
`--password` flag，成功时 stdout 只输出 hash。

## 9. 验证与运维

本地完整服务只通过正式入口启动：

```bash
make dev
make smoke-dev
make web-e2e
make fault-test
make soak-test
```

`make dev` 运行正式 server/client、Next.js 和仅作为内网目标的 TCP/UDP echo fixture。
fixture 不实现 PortPilot 控制、鉴权、smux 或代理逻辑。

确定性门禁使用 `make ci`，覆盖 Go/Web lint、类型检查、构建、单元测试、race、migration
规则和真实 loopback 集成测试。面向 `master` 的 PR 同时运行 `make ci` 和
`make acceptance`，后者默认执行 15 分钟 soak；正式版本 tag 不重复验收。镜像只通过
`make build-images` 构建 `portpilot-server`、`portpilot-client` 和 `portpilot-web`；
仓库不提供 Docker/Compose 运行目标。正式版本标签为三张镜像同时发布 `linux/amd64` 和
`linux/arm64/v8` 的多架构 manifest。
