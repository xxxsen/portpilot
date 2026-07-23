# PortPilot 邮件 Magic Link 敲门授权

## 1. 能力边界

邮件敲门为动态代理端口增加一层短期来源 IP 授权。外部用户在公开页面提交白名单邮箱，收到
一次性 Magic Link，并从申请时的同一公网 IP 打开链接；授权有效期内，该 IP 可以访问显式配置
`allow_knock_only=true` 的代理。

该能力遵循以下边界：

- 它是全局、单租户的邮箱白名单，不为不同邮箱分配代理范围或角色。
- 敲门授权不能绕过全局或代理级来源 CIDR；判定顺序固定为全局 ACL、代理 ACL、敲门授权。
- 未启用 `allow_knock_only` 的既有代理行为不变。
- TCP 已建立的 relay 不因授权到期或撤销而中断；后续新连接会被拒绝。
- UDP 每个外部 datagram 都重新判定，因此到期或撤销后，已有 flow 的后续 datagram 也会丢弃。
- token、pending 请求和 grant 只存在于服务端内存，服务重启后全部失效，不写入 SQLite、
  配置文件或 `cache_dir`。
- 敲门只授权来源 IP，不改变客户端访问内网目标的能力，也不操作系统防火墙。

## 2. 服务端配置

敲门配置位于服务端主配置的 `knock_config`，资源边界位于现有 `limits`：

```json
{
  "real_ip_header": "X-Real-IP",
  "limits": {
    "max_knock_requests_per_hour": 10,
    "max_knock_pending_tokens": 1024,
    "max_knock_grants": 4096,
    "max_knock_mail_queue": 128
  },
  "knock_config": {
    "enabled": true,
    "public_base_url": "https://portpilot.example.com",
    "magic_link_ttl": "10m",
    "grant_ttl": "10m",
    "resend_interval": "1m",
    "allowed_emails": [
      "owner@example.com",
      "backup@example.com"
    ],
    "smtp": {
      "host": "smtp.example.com",
      "port": 465,
      "security": "tls",
      "user_name": "owner@example.com",
      "password": "<smtp-app-password>",
      "from": "owner@example.com",
      "tls_ca_file": ""
    }
  }
}
```

`security` 只允许：

| 值 | 行为 |
| --- | --- |
| `tls` | 连接建立时立即使用 TLS，通常使用 465 端口 |
| `starttls` | 明文建立 SMTP 会话后强制升级 TLS；服务端不支持 STARTTLS 时发送失败 |

SMTP TLS 最低版本为 TLS 1.2，证书主机名固定校验 `smtp.host`。`tls_ca_file` 为空时使用系统根
证书；相对路径以服务端配置文件目录为基准。没有跳过证书校验或明文 SMTP 模式。

`public_base_url` 必须是无 userinfo、query、fragment 和额外 path 的 HTTPS origin。邮箱会被
trim、转小写并按纯邮箱地址校验；重复项或 display name 会被拒绝。SMTP password 是可逆部署
凭据，服务端配置文件必须限制为服务账号可读，推荐使用邮件服务商的应用专用密码。它不会出现在
配置 API、Web、health、日志或审计中。

省略 `knock_config` 时功能默认关闭。敲门配置不支持热更新，修改后重启服务端生效。

## 3. 代理配置

TCP 和 UDP tunnel 的 `data` 都支持：

```json
{
  "network": "tcp",
  "transport": "smux",
  "bind": ":7005",
  "connect": "192.168.50.11:22",
  "allow_knock_only": true,
  "access_policy": {
    "allow_cidrs": [],
    "deny_cidrs": []
  }
}
```

`allow_knock_only` 缺省为 `false`。服务端启动和管理后台保存配置时都会执行跨配置校验：
`allow_knock_only=true` 与 `knock_config.enabled=false` 不能同时存在。

管理后台的代理编辑器提供“仅允许已邮件授权 IP”开关。敲门全局关闭时该开关不可选；代理列表和
运行详情会展示是否要求邮件授权。代理页面同时展示当前有效授权、授权邮箱、来源 IP、剩余时间，
管理员可在自绘确认窗体中撤销授权。

## 4. 申请与验证流程

公开页面为 `/knock`，验证页面为 `/knock/verify`，两者不要求管理员 session：

1. 页面通过 `GET /api/knock/status` 显示当前来源 IP 和授权倒计时。
2. 用户提交邮箱到 `POST /api/knock/requests`。
3. 服务端规范化当前来源 IP 和邮箱，执行来源、邮箱、全局及重发间隔限流。
4. 白名单邮箱会创建 32 字节密码学随机 token，并通过有界 SMTP worker 发送邮件。
5. Magic Link 形如 `https://portpilot.example.com/knock/verify#token=<token>`。
6. 验证页面立即从 fragment 读取并清除 token，再 POST 到 `/api/knock/verify`。
7. token 有效、未使用且当前 IP 与申请 IP 相同时，服务端先写成功审计，再原子创建或刷新 grant。

token 只以 SHA-256 hash 保存在内存，单次使用，到期边界为 `now >= expires_at`。同一邮箱重新
申请会使旧 token 失效；同一来源再次成功验证会刷新 grant TTL。邮件发送失败或队列无法接收时，
对应 token 会被删除。

fragment 不会随 HTTP 请求发送，因此 token 不进入 Nginx URL 日志、Referer 或邮件扫描器的
GET 请求。`GET /knock/verify` 永远不创建授权；不同来源 IP 即使 POST 到验证接口也只能得到
`source_ip_mismatch`，并且不会消费 token。

`real_ip_header` 只用于经 Nginx 到达的公开敲门、Admin 和客户端 HTTP 入口。配置该字段时取
逗号分隔值的第一个 IP，缺失或为空时回退 `RemoteAddr`。动态 TCP/UDP 代理端口不读取 HTTP
header，始终使用外部 socket peer。

## 5. HTTP API

公开接口：

| API | 作用 |
| --- | --- |
| `GET /api/knock/status` | 当前来源 IP、是否授权、授权时间和剩余秒数 |
| `POST /api/knock/requests` | 请求授权邮件，JSON body 为 `{"email":"..."}` |
| `POST /api/knock/verify` | 消费一次性 token，JSON body 为 `{"token":"..."}` |

语法合法的邮件请求对白名单、非白名单、SMTP 失败、队列满和 pending 满统一返回 202 与相同
提示，避免枚举邮箱。限流返回 429 和 `Retry-After`；非法 JSON、content type、body 大小或
Origin 返回 400；全局关闭返回 503。公开响应不返回白名单、SMTP 状态、邮箱或 pending token。

验证成功返回来源 IP、授权和到期 Unix 毫秒时间、剩余秒数及是否刷新。无效、已用或过期 token
返回 401；来源不匹配返回 403；并发消费返回 409；授权容量或审计存储不可用返回 503。

公开 POST 只接受 `application/json`，body 最大 4 KiB。`Origin` 存在时必须等于
`public_base_url` 的 origin，缺失时允许；不启用跨域 CORS。API 响应设置 `Cache-Control:
no-store` 和 `Referrer-Policy: no-referrer`。

管理员接口：

| API | 要求 | 作用 |
| --- | --- | --- |
| `GET /api/knock/grants` | 管理员 session | 列出当前有效授权 |
| `DELETE /api/knock/grants/{source_ip}` | 管理员 session 和 CSRF | 幂等撤销来源 IP 授权 |

## 6. 数据面判定

服务端对每次新 TCP accept、TCP 数据流正式配对前，以及每个外部 UDP datagram 调用同一判定：

1. 从 socket peer 规范化来源 IP，mapped IPv4 统一为 IPv4。
2. 应用全局 `access_policy`。
3. 应用代理 `access_policy`。
4. 若 `allow_knock_only=true`，检查内存中该 IP 的 grant。
5. 通过后才允许创建 ticket、smux stream、direct 数据连接或 UDP flow。

敲门 manager 不可用时受保护代理 fail closed。拒绝只写限频结构化日志和低基数指标，不写入
事件审计，以免高频探测冲掉管理记录。来源 ACL、限速、PROXY Protocol、smux/direct 选择和资源
上限继续按原数据面契约执行。

## 7. 邮件队列与资源边界

SMTP worker 使用单 worker 有界队列。每封邮件首次失败后等待 2 秒重试一次；两次失败
标记失败并删除 token。发送、连接和 TLS 握手都受 10 秒超时约束。服务关闭时停止接收新任务，
在关闭上下文内等待在途任务，并使未发送任务失败。

应用层每小时分别限制单来源和单邮箱请求数，并设置固定全局上限；同一邮箱还受
`resend_interval` 限制。验证接口每来源每分钟最多 30 次并具有固定全局上限。限流状态、
pending token、grant 和邮件队列都受配置上限约束。

## 8. 审计、日志与健康

成功验证写入 `knock_grant_created`：

- 操作者类型为 external，操作者为规范化邮箱，IP 为授权来源；
- 上下文包含申请、授权、到期时间、TTL、是否刷新和截断后的 User-Agent。

管理员撤销写入 `knock_grant_revoked`：

- 操作者为管理员用户名，IP 为管理员请求来源；
- 上下文包含目标 IP、授权邮箱、授权和到期时间。

邮件请求、非白名单邮箱、SMTP/token 失败、自然到期和代理拒绝不写审计。邮件运行日志仅使用
邮箱 SHA-256 摘要的前 12 个十六进制字符，不记录完整邮箱、SMTP password、raw token 或完整
Magic Link。

summary 提供 active token/grant、邮件队列深度、邮件 queued/sent/failed、请求限流、验证
成功/失败、来源不匹配、授权撤销和最近邮件成功/失败时间。外部访问拒绝使用稳定原因
`knock_required`。SMTP 连续失败只让 health detail 的 `knock_mailer` 进入 degraded，不使服务端
readiness 失败。

## 9. Nginx 部署

Go 服务端仍不处理入站 TLS。Nginx 必须把 `/knock` 和 `/knock/verify` 交给 Web，把所有
`/api/` 原样转发到服务端 `admin_addr`，不能移除 `/api` 前缀。邮件申请与 token 验证使用比
普通 Admin API 更严格的独立 `limit_req_zone`：

```nginx
limit_req_zone $binary_remote_addr zone=portpilot_knock_mail:10m rate=5r/m;
limit_req_zone $binary_remote_addr zone=portpilot_knock_verify:10m rate=30r/m;

location = /api/knock/requests {
    limit_req zone=portpilot_knock_mail burst=5 nodelay;
    proxy_pass http://portpilot-host:5100;
    proxy_set_header X-Real-IP $remote_addr;
}

location = /api/knock/verify {
    limit_req zone=portpilot_knock_verify burst=10 nodelay;
    proxy_pass http://portpilot-host:5100;
    proxy_set_header X-Real-IP $remote_addr;
}

location ^~ /api/ {
    proxy_pass http://portpilot-host:5100;
    proxy_set_header X-Real-IP $remote_addr;
}

location / {
    proxy_pass http://portpilot-web:3000;
}
```

完整参考配置见 `docker/nginx/default.conf`。Nginx 还应设置 HSTS、`Referrer-Policy:
no-referrer`、`X-Content-Type-Options: nosniff`，并只启用 TLS 1.2/1.3。

## 10. 开发与验证

`make dev` 启动正式 server、client 和 Web，同时启动外部 TCP/UDP echo 与带测试 CA 的 SMTP
capture fixture。开发配置提供受保护 TCP/UDP 代理；fixture 只负责接收邮件，不实现任何
PortPilot 行为。

相关变更至少执行：

```bash
make ci
make dev
make smoke-dev
make web-e2e
make fault-test
make acceptance
```

smoke 覆盖邮件请求、扫描器来源不匹配、TCP/UDP 授权前后、ACL 优先级、撤销和服务重启失效。
token/grant 到期使用 fake clock 或短生命周期测试，不通过实际等待默认 TTL 验收。
