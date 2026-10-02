# relayward-mail

Relayward 是一个轻量发信网关：所有程序改连网关，网关独占提供商的真实 key，并在转发时按程序记录、限流、注入退订。单个 Go 二进制 + 一个 SQLite 文件，部署零依赖。

按[计划书](docs/Relayward%20邮件网关计划书)分五个里程碑开发，当前进度：**M1 已完成**。

## 当前进度

| 里程碑 | 状态 | 内容 |
| ------ | ---- | ---- |
| M1 可用的中继 | ✅ 已完成 | SMTP 认证、发件地址校验、转发上游、发送日志、首次启动管理员 token、/healthz |
| M2 管理 API | 未开始 | 程序和 token 的增删改查、三种角色、审计日志、失败锁定、限流 |
| M3 退订 | 未开始 | 按收件人拆信、退订头注入、退订页面、拦截、退订 API |
| M4 正文注入 | 未开始 | MIME 解析，在纯文本和 HTML 部分插入退订链接 |
| M5 内嵌管理页 | 未开始 | embed 打包的管理页面，只调用管理 API |

## 快速开始

### 构建

```bash
make build    # 产物在 bin/relayward（Windows 自动加 .exe）
```

要求 Go 1.23+，`CGO_ENABLED=0`，静态二进制无外部依赖。

### 配置

```bash
cp config.example.yaml config.yaml
# 编辑 config.yaml，并设置环境变量：
export UPSTREAM_KEY=你的提供商发信key
```

配置项说明见 `config.example.yaml` 内注释。所有 `${VAR}` 引用从环境变量展开。

### 启动

```bash
./bin/relayward serve -config config.yaml
```

首次启动会生成超级管理员 token（前缀 `rw_admin_`）：明文只打印到启动日志一次，同时写入 `data/initial_admin_token`（权限 0600），数据库只存 SHA-256 哈希。请立即妥善保管。

### 创建发信程序账号（M2 之前的临时方式）

```bash
./bin/relayward admin create-app -config config.yaml gitea -from NoReply@example.com
# 输出一次性 SMTP 密码；不传 -from 时默认只允许 NoReply@example.com
```

M2 的管理 API 上线后，此 CLI 保留为本机应急手段，正式流程走 API。

### 把程序切到网关

把各程序（Gitea 等）的 SMTP 配置改为：

- 主机：网关地址，端口 587
- 用户名：程序名（如 `gitea`）
- 密码：`create-app` 输出的一次性密码
- 发件人：只能用该程序允许列表内的地址

所有程序切换完成后，在提供商后台轮换 key，只更新网关的 `UPSTREAM_KEY`。

### 健康检查

```bash
curl http://127.0.0.1:8081/healthz
# 200 {"status":"ok"} / 503 {"status":"degraded"}
# 带 viewer 及以上 token 时返回明细（数据库、上游、最近成功转发时间、版本、运行时长）
```

上游连通性由后台每 60 秒探测一次，健康检查只读缓存结果。可直接用于 Docker HEALTHCHECK、Uptime Kuma。

## 开发

```bash
make test   # go test -race ./...（race 需要 C 编译器，Windows 上如无 gcc 先安装）
make lint   # golangci-lint run
make run    # 构建并启动
```

### 工程约定

- 仅依赖计划书「技术选型」表中的库（另允许 `golang.org/x/sync/errgroup`）；新增依赖须记入 `docs/DECISIONS.md`。
- 未定义行为的处理决定记录在 [`docs/DECISIONS.md`](docs/DECISIONS.md)。
- 提交信息使用 Conventional Commits。

## M1 验收自测结果

验收标准：用任一程序通过网关发信成功，日志表有记录；错误密码被拒。

| 场景 | 预期 | 实际 |
| ---- | ---- | ---- |
| 正确凭据 + 允许的发件人发信 | 转发到上游，日志表记录 `sent` | ✅ 上游收到原文，`last_relay_success_at` 更新 |
| 错误密码认证 | 535 拒绝 | ✅ `535 5.7.8 authentication failed` |
| 已停用程序认证 | 535 拒绝 | ✅（同错误响应，不泄露账号状态） |
| 未授权发件人（spoof） | 拒收 | ✅ `550 5.7.1 sender address not allowed` |
| 首次启动 | 生成管理员 token，明文只出现一次 | ✅ 日志打印一次 + 写入 `data/initial_admin_token` |
| 无效 token 请求 /healthz | 不返回明细 | ✅ 只返回 `{"status":"ok"}` |
| 上游掉线 | /healthz 变 degraded，探测告警 | ✅ 60 秒探测捕获并记 WARN |
| 单元/集成测试 | 全部通过（含 -race） | ✅ 覆盖率：api 93%、smtpd 88%、config 91%、store 75%、relay 63% |

自动化覆盖：SMTP 认证（PLAIN/LOGIN）、发件人校验、多收件人逐条记录、上游 4xx/5xx/断连分别映射 451/554/451、日志落库、argon2id 加解密、token 生成与首次启动幂等、/healthz 各状态。

## 安全说明

- SMTP 端口建议只对应用服务器 IP 开放；程序分散在公网多台时必须配置 `smtp.tls_cert/tls_key`，否则密码明文传输。
- 配置 TLS 证书后，AUTH 必须先 STARTTLS。
- 管理端口（默认 8081）的公网防护（HTTPS、失败锁定、IP 白名单）在 M2 完善，此前建议只在内网使用。
- 管理员 token 泄露应急：`relayward admin reset`（M2 提供）或删除数据目录后重启。

## 许可

见 [LICENSE](LICENSE)。
