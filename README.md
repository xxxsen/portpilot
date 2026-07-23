# PortPilot

PortPilot 是一个由服务端集中管理代理规则的内网反向代理工具。服务端和客户端使用 Go，管理后台使用 Next.js、React 和 Tailwind CSS。

## 数据路径

- Go 服务端只监听 `admin_addr` 和一个明文 `server_addr`，不处理 TLS。
- 生产环境由 Nginx 终止 TLS，并将 `/control`、`/data/mux`、`/data/direct` 转发到 `server_addr`。
- 控制流量使用独立 WebSocket，不进入 smux。
- TCP 数据优先通过 smux stream 转发，也支持 direct 策略。
- UDP 在公网端和内网端保持 UDP，服务端与客户端之间使用 UDP-over-smux/TCP。
- 代理配置文件是事实来源；SQLite 只保存可丢弃的事件、流量和审计信息。

完整协议、安全和生命周期见 [docs/001-portpilot-design.md](docs/001-portpilot-design.md)；
管理后台、审计、网络活动、SQLite migration 和 CLI 见
[docs/002-admin-console-and-cli-improvements.md](docs/002-admin-console-and-cli-improvements.md)；邮件
Magic Link 敲门授权见 [docs/005-email-magic-link-knock.md](docs/005-email-magic-link-knock.md)。

## 本地开发

需要 Go 1.26、Node.js 20、npm、curl、jq 和 `ss`。

```bash
make bootstrap
make doctor
make dev
```

`make dev` 构建并启动正式的 `cmd/portpilot-server` 和 `cmd/portpilot-client`，同时启动 Next.js
以及外部 TCP/UDP echo 和 SMTP capture fixture。客户端通过 `configs/dev/client.json` 以非 TLS
方式直连本地服务端；fixture 不实现任何 PortPilot 服务端或客户端行为。

Makefile 和开发测试脚本优先使用 `.tools/go`，忽略外部 `GOROOT`，并按 `GOVERSION` 将编译
缓存隔离在 `.cache/go-build/`；仓库工具链不存在时才回退到系统 Go。

默认入口：

- Web：`http://127.0.0.1:3000`
- Admin API：`http://127.0.0.1:5100`
- Control/Data：`127.0.0.1:6100`
- TCP smux：`127.0.0.1:7000`
- UDP-over-TCP：`127.0.0.1:7001`
- TCP direct：`127.0.0.1:7002`
- TCP PROXY Protocol v1：`127.0.0.1:7003`
- TCP 连接限速：`127.0.0.1:7004`
- 邮件授权 TCP：`127.0.0.1:7005`
- 邮件授权 UDP：`127.0.0.1:7006`
- 开发 SMTP capture API：`http://127.0.0.1:18083`

开发管理员账号为 `admin` / `admin`，只允许用于本地配置。

## 配置

- 服务端主配置：参考 `configs/dev/server.json`
- 服务端代理配置：参考 `configs/dev/clients.json`
- 客户端配置：参考 `configs/dev/client.json`
- Nginx 边界：参考 `docker/nginx/default.conf`

所有 JSON 使用严格解析，未知字段会导致启动失败。管理员密码必须是 bcrypt 哈希且 cost
不低于 10；客户端 token 应由至少 32 字节随机数据生成。生产配置应限制为服务账号可读，
按需配置动态代理端口的全局/代理外部来源 CIDR、邮件临时授权、每连接限速和 TCP PROXY
Protocol v1。

服务端和客户端都支持相同的 `log_config`。省略时默认以 `debug` 级别输出到 console；生产
环境可配置 `level`、`console`、`file`、`file_size`、`file_count` 和 `keep_days`。文件相对
路径以对应配置文件所在目录为基准，完整约束见设计文档的“日志与指标”一节。

生成管理员密码哈希：

```bash
./bin/portpilot-server password-hash
```

服务端和客户端使用 Cobra 子命令，运行时必须显式调用 `run --config <path>`。

## 门禁

快速验证一个已运行的开发环境：

```bash
make smoke-dev
make web-e2e
make fault-test
```

确定性 CI 门禁：

```bash
make ci
```

面向 `master` 的 Pull Request 创建或更新时，CI 和 acceptance 会并行运行。acceptance
会自行启动和清理 `make dev`，执行 smoke、Web E2E、故障注入和 15 分钟 chaos soak：

```bash
make acceptance
```

`make acceptance` 不重复执行 `make ci`，也不构建镜像。PR 同时执行两项独立门禁；直接
push `master` 只执行 `make ci`。正式版本 tag 只构建并发布三个镜像，不重复执行 soak。

单元测试不设置覆盖率百分比阈值，但安全、协议和生命周期路径必须有测试。

## 镜像

Makefile 仅提供构建命令：

```bash
make build-server-image
make build-client-image
make build-web-image
make build-images
```

本地构建默认生成 `linux/amd64` 镜像；构建 ARMv8 镜像时显式指定平台和独立标签：

```bash
make build-images IMAGE_PLATFORM=linux/arm64/v8 IMAGE_TAG=arm64
```

本地跨架构构建要求 Docker daemon 已注册 ARM64 QEMU/binfmt；Tag 发布工作流会自动完成该初始化。

默认镜像名为 `portpilot-server`、`portpilot-client`、`portpilot-web`。仓库不提供镜像运行、Compose、容器 smoke 或容器测试命令；所有功能测试均由 `make dev` 启动正式 server/client 后执行。
