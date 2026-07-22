# PortPilot 运行可见性与管理导航

## 1. 文档定位

本文记录 PortPilot 已实现的代理运行状态、总览关注项、全局搜索、历史周期对比、数据导出、
易失缓存目录和 GeoIP 更新契约。

基础架构与数据面协议见
[`001-portpilot-design.md`](001-portpilot-design.md)，管理后台、事件审计和连接明细见
[`002-admin-console-and-cli-improvements.md`](002-admin-console-and-cli-improvements.md)，历史聚合、
Top K 和 IP 下钻见 [`003-network-history-analytics.md`](003-network-history-analytics.md)。

这些能力不增加客户端协议或客户端配置字段，不改变 TCP、UDP-over-TCP、smux、direct 或控制
连接的数据路径。

## 2. 共同约束

- 客户端与代理配置文件仍是配置事实来源；SQLite 和 GeoIP 下载文件均可删除重建。
- JSON 时间使用 UTC Unix 毫秒；导出文件内时间使用 RFC3339 UTC 毫秒。
- 历史范围使用半开区间 `[from, to)`。
- 代理由 `client_id + proxy_id` 唯一标识，不能假设 `proxy_id` 跨客户端唯一。
- 管理 API 要求管理员 session，POST 导出额外要求 CSRF。
- 数据库或历史聚合不可用时，数据面和进程内运行状态继续工作；UI 明确显示 unavailable 或
  degraded，不能把缺失数据伪装为零。
- 左侧导航保持“总览、客户端、代理规则、网络活动、事件与审计”，不为本组能力增加新 tab。

## 3. 代理运行状态

`GET /api/proxies/runtime` 返回配置中每一条代理的服务端运行快照，包括被禁用的代理。查询支持
客户端、状态、协议、文本搜索和分页，固定按故障优先级、客户端 ID、代理 ID 排序。

### 3.1 状态模型

| availability | 含义 |
| --- | --- |
| `disabled` | 客户端或代理按配置禁用 |
| `waiting_client` | listener 已就绪，但客户端没有当前控制会话 |
| `degraded` | listener 和客户端存在，但要求的数据承载条件不完整 |
| `ready` | 当前建链前置条件满足 |
| `error` | 预期 listener 缺失或监听循环异常退出 |

运行状态按以下优先级计算：客户端禁用、代理禁用、listener 缺失、listener 故障、客户端离线、
客户端 degraded/draining、smux 不可用、ready。`transport=auto` 且允许 direct fallback 时，smux
不可用仍可为 ready，并返回 `effective_transport=direct_fallback`。

Ready 只表示 PortPilot 的建链前置条件满足，不承诺客户端内网目标可达。

### 3.2 观测内容

每个代理快照包含：

- listener 状态、绑定时间、最近接入时间和脱敏后的最近故障；
- 当前或最近客户端 session、心跳时间、控制 RTT、mux session 和 active stream；
- 活跃/等待连接数及实时上下行速率；
- 最近 24 小时终态连接、失败率、流量和最近失败；
- 实际 transport、bind、connect、协议和启用状态。

listener 观测和实时速率来自进程内快照；24 小时历史来自 SQLite 并短期缓存。主动 reconcile 或
服务关闭不会记为 listener 故障。

### 3.3 管理后台

“代理规则”页面可按运行状态筛选，页面可见时每 5 秒刷新当前页；页面隐藏时停止轮询，并取消
被新请求取代的旧请求。后台刷新保留原内容和稳定表格尺寸。

点击代理行打开运行详情，展示状态依据、listener、session、实时活动、24 小时历史，以及来源
访问策略、PROXY Protocol 和上下行限速配置。详情可跳转到已按客户端和代理筛选的“网络活动”。
URL 保存 `view`、`client_id` 和 `proxy_id`，刷新与浏览器前进/后退可恢复；对象不存在时清理失效
参数。

## 4. 总览需要处理

`GET /api/attention?limit=8|20` 返回当前快照中需要管理员关注的确定性条目。它不是持久化告警
系统：没有确认、静默或通知，问题恢复后条目立即消失，也不写入事件审计。

条目来源包括：

- 核心观测状态 degraded；
- listener error；
- 启用客户端离线；
- 客户端或其代理 degraded；
- 历史聚合不可用、回填或 degraded；
- 已配置 GeoIP 但 reader/update 状态 degraded；
- 当前与上一周期相比出现连接数或失败率突增的代理。

离线代理按客户端合并；listener 故障保持独立。排序为 critical、warning、info，同级按首次观测
时间和稳定 ID。条目持续存在时保留首次观测时间，恢复后再次出现会重新计时。

总览在统计带与客户端状态之间展示“需要处理”。默认显示 8 条，可原位展开到 20 条；空状态也
保留该区域。页面可见时每 10 秒刷新，失败时保留旧内容并标记可能过期。每个条目携带服务端
枚举的内部 destination，点击后打开已有页面、筛选或详情，服务端不返回任意 URL。

## 5. 全局搜索

登录后的顶部栏提供全局搜索，`Ctrl+K` 或 `Cmd+K` 可打开命令面板。搜索对象为：

- 客户端 ID 和备注；
- 代理 ID、备注、bind 和 connect；
- 规范化来源 IP；
- activity ID。

`GET /api/search?q=<query>&limit=20` 要求 2 到 128 字符，每类最多 5 项，总计最多 20 项。配置
搜索在内存完成；合法 IP 才查询来源历史，8 到 32 位十六进制前缀才查询 activity。普通文本不
扫描连接历史。

匹配按完全匹配、ID 前缀、备注完全匹配、地址前缀、包含匹配依次降权；同分按类型和稳定 ID
排序。历史查询有独立短超时；SQLite 不可用时仍返回配置结果，并以 `partial` 和
`unavailable_sources` 表示部分结果。接口按管理员 session 限流。

命令面板支持防抖、取消旧请求、分组、上下键、Enter 和 Escape。关闭面板会清空输入，不保存
localStorage、sessionStorage 或搜索历史。目标使用枚举 view 和受校验参数，客户端、代理、来源
IP 与 activity 均可深链恢复。

## 6. 历史周期对比

`GET /api/network/analytics/comparison` 复用历史洞察的 range、custom 时间、客户端、代理、协议
和 Top K 参数。对当前范围 `[from,to)`，上一周期固定为等长的 `[from-duration,from)`，两个周期
使用共同可用的基础分辨率。

对比只统计 `closed`、`failed`、`interrupted` 终态连接，返回：

- 连接数、失败数、失败率、独立 IP、上行、下行和总流量变化；
- 对齐后的当前/上一周期曲线；
- 当前周期新出现的来源 IP 和国家；
- 按连接与流量排序的 Top IP 排名变化；
- 每个代理的连接、失败率或流量突增信号。

上一周期为零而当前大于零时使用 `new`，不计算无穷百分比；当前归零使用 `inactive`。失败率的
绝对变化是百分点差。上一周期超出保留期、任一周期不完整或聚合未就绪时返回不可比较原因，
当前周期数据仍可使用，不显示误导性的 0%。任一周期估算流量超过 10% 时仍返回流量变化，但
不产生流量突增提示。

“网络活动 / 历史洞察”在指标下显示“较上一周期”，提供可选的上一周期虚线叠加，并在“变化
提示”中展示代理变化、新来源和国家变化。对比请求与当前周期请求并发，范围变化时取消旧请求；
后台刷新失败时保留最近一次有效对比。

## 7. 筛选结果导出

以下页面可导出当前筛选结果：

| API | 数据集 |
| --- | --- |
| `POST /api/exports/network-connections` | 实时或历史连接明细 |
| `POST /api/exports/network-dimensions` | 历史客户端/代理汇总 |
| `POST /api/exports/audit-events` | 事件与审计 |

请求体包含 `format=csv|json` 和对应列表接口的 `filters`。未知、分页或互斥筛选会被拒绝。服务端
在请求开始时冻结结束时间，确保计数与内容一致；审计导出不会把自身的 pending 事件包含进去。

### 7.1 下载与资源边界

每次成功响应都是直接交给浏览器下载的 ZIP，服务端不会把导出结果写入 `cache_dir`、系统临时
目录或其他本地路径。ZIP 在有界、分块的进程内内存中完整生成并校验后，使用
`Content-Disposition: attachment`、`Content-Type: application/zip` 和准确的
`Content-Length` 返回。

ZIP 根目录固定包含：

```text
<dataset>.csv 或 <dataset>.json
manifest.json
```

manifest 包含版本、数据集、格式、导出时间、规范化筛选、行数、未压缩字节和数据文件 SHA-256。
下载文件名由服务端生成，格式为 `portpilot-<dataset>-YYYYMMDDTHHMMSSZ.zip`。

固定限制为最多 10,000 行、50 MiB 未压缩数据、32 MiB ZIP、30 秒生成/发送、全服务同时一个
导出、每个管理员 session 同时一个导出且每分钟最多五次。超过数据边界返回 413；繁忙或限流
返回 429，结果不会静默截断。客户端取消、服务关闭和响应写入失败都会取消任务、释放内存与
并发许可。

### 7.2 格式与审计

CSV 使用 UTF-8、RFC 4180 quoting 和固定表头；字符串首个非空字符为 `= + - @` 时前置单引号，
避免表格公式注入。JSON 使用标准 encoder 并保留原始字符串。两种格式都不包含 password、token、
signature、ticket、cookie、CSRF 或 session secret。

导出开始前写入 `data_export/pending` 审计，完成后收敛为 success 或 failure。审计只保存数据集、
格式、筛选摘要 hash、行数、字节数、耗时和稳定原因，不保存自由搜索文本、来源 IP 或导出内容。
导出对话框展示当前筛选、预计行数、格式和限制；下载完成后释放浏览器 Blob URL。

## 8. 易失缓存目录

服务端主配置使用 `cache_dir`，不再接受用户配置 SQLite 文件路径。固定布局为：

```text
<cache_dir>/
  portpilot.db
  portpilot.db-wal
  portpilot.db-shm
  geoip/
    country.mmdb
    metadata.json
  quarantine/
```

配置路径可以跨重启复用，但其中内容仍是可删除、可重建的派生数据。服务端创建目录时使用私有
权限，拒绝 cache 根、固定文件或受管子路径为 symlink。SQLite 路径始终为
`<cache_dir>/portpilot.db`。

`cache_dir` 为空时，服务端在系统临时目录创建本进程专用目录，并在正常关闭时删除整个目录。
这只影响 SQLite 与下载的 GeoIP；导出仍只使用内存，绝不写入该目录。配置指定的 cache 根目录
不会在关闭时删除。

数据库不存在时执行全部 migrations。checksum、schema、integrity 或损坏错误会把数据库及
wal/shm 移到 quarantine，再从空库执行 migrations；权限、磁盘空间、只读文件系统或 quarantine
失败会阻止启动。quarantine 只保留有限的近期副本。任何数据库重建都不修改客户端身份、token、
代理规则或 listener 配置。

## 9. GeoIP source 与每周更新

`observability.geoip_country_source` 支持：

- 空字符串：关闭公共 IP 国家分类；
- 本地 MMDB 路径：直接打开，并按文件变化热重载；
- HTTPS URL：下载到 `<cache_dir>/geoip/country.mmdb`，每 7 天做一次条件更新。

远程 source 只允许 HTTPS，不允许 userinfo、fragment 或降级到 HTTP 的 redirect。URL query 可
承载下载服务凭据，但原始 URL、query、license key、Authorization 和 redirect Location 不进入
日志、审计、健康、metrics 或 metadata。跨 host redirect 不转发凭据 header。

服务启动时优先装载现有有效 cache。没有可用 reader 时不阻断控制面或数据面，公共 IP 暂时归为
unknown，并在后台下载；存在旧 reader 时，下载、校验或替换失败继续使用旧 reader。

更新先使用 ETag/Last-Modified 做条件 HEAD，必要时 GET；HEAD 不支持时退化为条件 GET。成功或
304 后将下一次检查设为 168 小时，失败按 1、2、4、8、16、24 小时退避，此后每 24 小时重试。
下载和解包均有大小与时间上限。

下载内容只接受原始 MMDB，或包含唯一 MMDB regular file 的 tar.gz。ZIP/CSV 明确拒绝，因
`maxminddb-golang` 不能直接查询 MaxMind CSV 下载包。候选文件必须通过 SHA-256、数据库类型和
最小读取校验，随后在同一目录 fsync、原子 rename 并交换 reader；并发 lookup 可继续使用旧
reader 直到引用释放。

GeoIP 仍直接查询内存映射 MMDB，并使用有界进程内 LRU；不建立 SQLite 点查缓存。终态聚合把
当时的国家分类与 database fingerprint 固化到 rollup，更新 MMDB 不重写历史数据。

## 10. API 与可观测性

本组稳定 API：

```text
GET  /api/proxies/runtime
GET  /api/attention
GET  /api/search
GET  /api/network/analytics/comparison
POST /api/exports/network-connections
POST /api/exports/network-dimensions
POST /api/exports/audit-events
```

`/api/metrics/summary` 汇总代理 availability、listener failure、attention severity、搜索、周期
对比、导出和 GeoIP 更新计数。指标不以 client ID、proxy ID、来源 IP、URL 或 host 作为无界
label。健康和 API 不返回 SQLite/MMDB 绝对路径、SQL、底层网络错误或 secret。

运行状态和 attention 的轮询均在页面隐藏时暂停，并取消旧请求。所有详情与筛选使用 URL 状态，
刷新、分享和浏览器前进/后退可以恢复；失效对象参数会被清理。
