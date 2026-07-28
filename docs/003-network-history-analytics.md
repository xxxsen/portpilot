# PortPilot 历史网络洞察

## 1. 文档定位

本文记录已经落地的历史网络统计、GeoIP、管理 API 和 Web 交互契约。连接实时状态、连接明细、
SQLite migration 通用规则见
[`002-admin-console-and-cli-improvements.md`](002-admin-console-and-cli-improvements.md)；
控制连接、smux、UDP-over-TCP 和数据面鉴权见
[`001-portpilot-design.md`](001-portpilot-design.md)。

历史洞察是现有“网络活动”页面的默认一级视图，不在左侧导航新增独立入口。“网络活动”的另一
个一级视图是“连接明细”，其中继续使用“实时连接 / 历史连接”切换。

## 2. 统计边界

历史洞察只统计已经进入以下终态的 TCP 连接和 UDP flow：

- `closed`
- `failed`
- `interrupted`

`accepted`、`connecting`、`active` 和 `closing` 只出现在实时连接，不进入历史指标、Top K、
国家分布或 IP 曲线。长连接结束后会回补它此前产生的历史流量，因此最近时间桶可能在断链后
增加。

### 2.1 来源与方向

- TCP 来源取服务端代理 listener 接受连接时的 socket peer。
- UDP 来源取 flow key 中的外部 socket peer。
- `external_addr` 保留原始 `host:port`，`external_ip` 保存规范化后的纯 IP。
- IPv4-mapped IPv6 通过 `netip.Addr.Unmap` 归一为 IPv4。
- IPv6 zone 被移除；解析失败归入 `invalid`，不能影响转发。
- `up_bytes` 表示外部用户到内网目标，`down_bytes` 表示内网目标到外部用户。
- TCP 只计算代理 payload；UDP 只计算原始 datagram payload。

代理端口必须直连外部用户。PortPilot 不信任应用层 forwarded header，也未启用 PROXY
protocol；如果代理 listener 位于四层负载均衡器之后，来源 IP 会是负载均衡器的 socket IP。

### 2.2 时间归属

- 连接数和失败数进入 `accepted_at` 所在时间桶。
- 已持久化 sample 流量进入 sample 的 `bucket_at` 时间桶。
- 最终累计中无法由 sample 解释的差额进入 `closed_at` 时间桶，并标记为 estimated。
- `first_seen_at` 是范围内最早接受时间。
- `last_seen_at` 是范围内最近活动时间。

连接在范围开始前建立、在范围内产生流量并最终结束时，该范围可以有流量但没有新增连接数。

### 2.3 Top K

Top K 允许 5、10 或 20，默认 10。

连接数排名：

```text
connections DESC, traffic_bytes DESC, source_ip ASC
```

流量排名：

```text
traffic_bytes DESC, connections DESC, source_ip ASC
```

两份榜单独立返回，不为相同 IP 去重。每项同时返回它占当前范围和 scope 的连接数比例与流量
比例；分母为零时比例为零。

## 3. 持久化与终态事务

`migrations/0002_network_history_analytics.sql` 在现有连接表上增加：

```text
external_ip
analytics_aggregated_at
```

并创建：

```text
network_history_ip_rollups
network_history_proxy_dimensions
network_analytics_meta
```

`network_history_ip_rollups` 同时保存 5 分钟和 1 小时粒度，维度为：

```text
resolution + bucket + client + proxy + protocol + source IP + GeoIP classification
```

每行保存连接、失败、字节、包、estimated 字节、首次和最近活动。统计表不引用连接明细外键，
因此统计保留期可以长于单条连接。

`network_history_proxy_dimensions` 保存历史客户端和代理的最后备注。对象从配置文件删除后，历史
统计仍可展示 ID、备注和删除状态；是否仍存在由 API 与当前配置快照合并判断。

### 3.1 原子聚合

连接进入终态时，activity writer 在同一个 SQLite 事务内：

1. 读取 `analytics_aggregated_at`。
2. 写入连接最终状态、累计、关闭时间和规范化来源 IP。
3. 按时间顺序裁剪 sample，任何方向都不能超过最终累计。
4. 把未被 sample 解释的非负差额记为 estimated。
5. 写入 5 分钟和 1 小时 rollup。
6. 更新历史客户端/代理备注。
7. 设置 `analytics_aggregated_at`。
8. 提交事务。

rollup 和幂等标记同事务提交。重试、服务重启和后台回填对同一 activity 最多计数一次。sample
累计超过最终累计时只接受剩余值，记录 overflow 指标并把 analytics 标记为 degraded。

缓存失效发生在事务提交边界之前：新查询会错过旧版本缓存并等待 SQLite 事务结束，因此不会在
聚合提交后重新写回旧缓存。

### 3.2 启动恢复与回填

服务开放端口前先把数据库中的非终态连接改为 `interrupted`。activity 服务随后使用与在线
终态完全相同的聚合函数处理它们。

后台回填按 `accepted_at DESC, activity_id DESC` 从新到旧扫描终态且
`analytics_aggregated_at IS NULL` 的记录，每批最多 500 条，每条独立事务并主动让出 CPU。
`network_analytics_meta.complete_from` 随批次向过去推进；故障时保留已完成进度并返回
`degraded`，恢复后继续。

回填期间 API 仍返回部分统计，同时用 completeness 标记 `backfilling`。Web 必须显示覆盖范围，
不能把部分结果展示为完整排名。

## 4. GeoIP

GeoIP 只做国家级分类，reader 使用 `github.com/oschwald/maxminddb-golang/v2`。来源 IP 仅在本地
MMDB 中查询，不会发送给 GeoIP 下载地址或在线查询服务。`geoip_country_source` 可以是本地 MMDB
路径或远程 HTTPS source；完整下载、更新和 cache 生命周期见
[`004-operational-insights-and-navigation.md`](004-operational-insights-and-navigation.md)。

地址分类为：

| kind | 含义 |
| --- | --- |
| `country` | MMDB 命中的两位大写国家代码 |
| `private` | loopback 或私网地址 |
| `reserved` | unspecified、link-local、multicast、文档网段等保留地址 |
| `unknown` | 未配置 MMDB 或公共地址未命中 |
| `invalid` | 无法解析 |

### 4.1 查询与缓存

MMDB 是内存映射的二进制索引。PortPilot 直接调用 reader，并使用有界进程内 LRU 吸收重复 IP；
不建立 SQLite GeoIP 点查缓存。

终态聚合把 `geo_kind`、`country_code` 和 `geo_database_fingerprint` 固化到 rollup。国家汇总无需
在页面请求中逐 IP 查询 MMDB；数据库更新只影响后续终态连接，已有历史贡献保留当时分类和
fingerprint，不执行全库重写。

### 4.2 Source、启动与热更新

- 空 source 关闭公共 IP 国家分类。
- 本地 source 缺失、不可读、格式错误或不含 Country 数据会使服务启动失败；文件变化时校验并
  原子交换 reader。
- 远程 source 只允许 HTTPS。服务优先装载 cache 中的有效 MMDB；无 reader 时进入 degraded 并
  在后台下载，不阻断控制面、listener 或数据面。
- 远程数据库每 7 天条件检查一次；失败按 1 小时到 24 小时退避，现有有效 reader 继续使用。
- 下载只接受 raw MMDB 或包含唯一 MMDB 的 tar.gz；ZIP/CSV 不能由 reader 直接查询并被拒绝。
- 新 reader 完整校验后原子替换；lookup 使用引用计数快照，旧 reader 在最后一个引用释放后关闭。
- source URL、query、下载凭据、cache 路径和底层错误不进入健康、metrics、审计或普通日志。

## 5. 配置与保留

服务端主配置使用 `cache_dir` 作为 SQLite 和远程 GeoIP 等派生数据的易失缓存根目录。SQLite
固定为 `<cache_dir>/portpilot.db`；不再提供独立数据库路径。`cache_dir` 为空时使用进程专用系统
临时目录，并在正常关闭时删除。

服务端 `observability` 包含：

```json
{
  "network_statistics_retention": "2160h",
  "network_fine_rollup_retention": "48h",
  "geoip_country_source": ""
}
```

约束：

- `network_statistics_retention` 为 30 到 365 天，默认 90 天。
- `network_fine_rollup_retention` 为 1 到 7 天，默认 48 小时，且不大于统计保留期。
- `geoip_country_source` 为空表示关闭 GeoIP；本地相对路径以服务端配置文件目录为基准，远程只
  允许 HTTPS。
- 旧配置缺少字段时先补默认值再严格校验；已废弃字段按未知字段拒绝。

默认保留关系：

```text
network_samples                    7d
network_connections               30d
5m network_history_ip_rollups     48h
1h network_history_ip_rollups     90d
network_history_proxy_dimensions  随最后一个 rollup
```

5 分钟和 1 小时 rollup 独立分批清理，每批最多 1000 行且每轮有总批次上限。未成功聚合的终态
连接不能被连接明细清理删除。SQLite 删除或被判定损坏后由 migrations 重建；客户端、token、
代理规则和 listener 不受影响。

## 6. 管理 API

所有接口使用管理员 session 鉴权、`Cache-Control: no-store` 和 2 秒查询 deadline：

```http
GET /api/network/analytics/overview
GET /api/network/analytics/dimensions
GET /api/network/analytics/top-ips
GET /api/network/analytics/ip-series
```

公共参数：

```text
range=1h|24h|7d|30d|90d|custom
from=<unix_millis>
to=<unix_millis>
client_id=<id>
proxy_id=<id>
protocol=tcp|udp
```

`proxy_id` 要求同时提供 `client_id`。未知但格式合法的历史对象返回空结果，不返回 404。
自定义范围使用 `[from, to)`，不能晚于当前时间、不能超过统计保留期，服务端对齐完整桶并保证
series 不超过 180 点。

快捷范围：

| 范围 | 存储粒度 | 输出粒度 | 点数 |
| --- | --- | --- | --- |
| 1h | 5 分钟 | 5 分钟 | 12 |
| 24h | 5 分钟 | 1 小时 | 24 |
| 7d | 1 小时 | 6 小时 | 28 |
| 30d | 1 小时 | 1 天 | 30 |
| 90d | 1 小时 | 1 天 | 90 |

快捷范围的 `effective_to` 对齐到当前输出桶的结束边界，输出桶始终落在自然时间边界上，
因此最后一个点是正在形成的当前桶，后续空闲桶由服务端明确补零。自定义范围仍向内对齐到
完整基础桶，不能统计到用户给定 `to` 之后的数据。

dimensions 使用 `page=1`、`page_size=25|50|100`、最长 128 字符搜索和固定排序：

```text
traffic_desc
connections_desc
unique_ips_desc
failures_desc
```

Top IP 使用 `k=5|10|20`；IP series 的 `source_ip` 必须通过 `netip` 解析并重新规范化。所有
series 的空桶由服务端补零。

稳定错误码：

```text
400 invalid_range
400 invalid_scope
400 invalid_ip
400 invalid_query
401 unauthorized
503 activity_store_unavailable
```

回填和 GeoIP 降级返回 200，并通过每个响应的 `range`、`scope` 和 `completeness` 描述实际
范围、分辨率、统计/明细可用起点、estimated 比例、回填状态和 GeoIP fingerprint。

## 7. 查询性能

查询只读取 rollup，不扫描 `network_connections`。store 使用索引完成范围和 scope 过滤；
dimensions 和 Top K 先执行主聚合，再只对当前页或候选 IP 批量补齐主要国家和最近分类。

最多 256 项的进程内派生查询缓存以完整 range、scope、协议、分页、排序、Top K 和来源 IP
为 key。终态聚合或保留清理会推进缓存版本并清空旧项；SQLite 被删除或服务重启时缓存自然
消失。

参考 benchmark 用 10 万条小时 rollup 表达 100 万终态连接、10 万来源 IP、100 个客户端和
每客户端 20 个代理：

```bash
make analytics-benchmark
```

热查询目标为 overview、Top K 和 IP series p95 小于 300ms，dimensions 第一页 p95 小于
500ms；任何管理查询仍受 2 秒硬超时约束。`EXPLAIN QUERY PLAN` 测试必须确认 analytics 使用
rollup 索引且不扫描连接明细。

## 8. Web 交互

“网络活动”页面结构：

1. 整体/客户端、协议和客户端选择。
2. 当前活跃连接与当前活跃客户端。
3. “历史洞察 / 连接明细”切换，默认历史洞察。
4. 连接明细中的“实时连接 / 历史连接”切换。

历史洞察包含：

- 1h、24h、7d、30d、90d 和自定义范围。
- 当前客户端/代理 scope 面包屑。
- 历史连接、独立来源 IP、国家数、失败率、上传和下载。
- 相互独立的流量趋势与连接趋势。
- 可搜索、分页、固定排序的客户端/代理汇总。
- 桌面双栏、移动端分段切换的连接数/流量 Top K。
- 选中 IP 的流量与连接曲线。
- 可按流量、连接数或独立 IP 排序的国家分类表。

客户端视图有明确的“全部代理”行。点击维度行更新当前 scope；点击 Top IP 会同时高亮两份
榜单中的相同 IP，并按需加载 IP series。“查看该 IP 的历史连接”复用现有历史连接表并带入
搜索，不创建第三套连接列表。

页面可见时每 60 秒刷新；切换范围或 scope 时取消旧请求。刷新保留旧内容、固定图表和表格
尺寸并关闭图表动画。图表提供可展开数据表。1440、1024、768 和 390px 不能出现页面横向
滚动、文本重叠或坐标轴裁剪。

页面状态保存在 URL：

```text
network_section
network_view
client_id
proxy_id
range
from
to
protocol
top_k
top_metric
source_ip
```

来源 IP 会进入浏览器历史，这是下钻链接的功能需要；URL 不包含 token、管理员 session、
数据库路径或 MMDB 路径。

## 9. 健康与指标

`/health/ready` 增加：

```text
network_analytics.status
network_analytics.backfill_complete_from
network_analytics.unaggregated_connections
network_analytics.processed_connections
geoip.status
geoip.database_fingerprint
geoip.last_reload_at
```

`/api/metrics/summary` 提供终态成功/失败/跳过、聚合总耗时、回填进度和失败、未聚合数量、
回填 lag、estimated 字节、sample overflow、GeoIP 分类/重载计数，以及四个 analytics
endpoint 的查询次数、总耗时和超时次数。

健康和指标不返回 SQLite/MMDB 路径、SQL、完整底层错误或 secret。普通日志不输出完整来源
IP；单连接定位使用 activity ID。

## 10. 验证

确定性门禁：

```bash
make ci
make analytics-benchmark
```

全服务测试必须由正式进程入口启动：

```bash
make dev
make smoke-dev
make web-e2e
make fault-test
make soak-test
make acceptance
```

面向 `master` 的 PR 同时执行 `make ci` 和 `make acceptance`，后者包含 15 分钟 soak；
正式版本 tag 仅构建和发布 `portpilot-server`、`portpilot-client`、`portpilot-web`，
不重复执行 soak。

`make smoke-dev` 通过真实 TCP smux、TCP direct 和 UDP-over-TCP 生成不同来源 IP、连接数和
流量，等待 UDP flow 进入终态后校验维度、两种 Top K 和 IP series。Playwright 在
1440x900 与 390x844 验证默认历史洞察、scope/IP 下钻、刷新稳定性、坐标轴边界和横向溢出。
