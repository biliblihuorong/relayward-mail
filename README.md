# relayward-mail

Relayward 是一个轻量发信网关：所有程序改连网关，网关独占提供商的真实 key，并在转发时按程序记录、限流、注入退订。单个 Go 二进制 + 一个 SQLite 文件，部署零依赖。

按[计划书](docs/Relayward%20邮件网关计划书)分五个里程碑开发，当前进度：**M1 已完成**。

## 当前进度

| 里程碑 | 状态 | 内容 |
| ------ | ---- | ---- |
| M1 可用的中继 | ✅ 已完成 | SMTP 认证、发件地址校验、转发上游、发送日志、首次启动管理员 token、/healthz |
| M2 管理 API | ✅ 已完成 | 程序和 token 的增删改查、三种角色、审计日志、失败锁定、限流、IP 白名单 |
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

### 管理 API（M2）

所有接口要求 `Authorization: Bearer <token>`，JSON 收发，错误统一为 `{"error":{"code":"...","message":"..."}}`。

| 方法 | 路径 | 说明 | 最低角色 |
| ---- | ---- | ---- | -------- |
| GET | /api/stats | 各程序发送量、成功/失败/限流/拦截数 | viewer |
| GET | /api/messages | 发送日志，支持 app/to/status/since/until 过滤，游标分页 | viewer |
| GET | /api/apps | 程序列表 | viewer |
| GET | /api/apps/{name} | 单个程序配置与统计 | viewer |
| POST | /api/apps | 创建程序，响应含一次性 SMTP 密码 | operator |
| PATCH | /api/apps/{name} | 启停、退订开关、限流、允许发件地址 | operator |
| POST | /api/apps/{name}/rotate | 重置 SMTP 密码，旧密码立即失效 | operator |
| DELETE | /api/apps/{name} | 删除程序（软删除，日志保留） | operator |
| GET | /api/tokens | token 列表（不含明文与哈希） | admin |
| POST | /api/tokens | 创建 token，明文只返回一次 | admin |
| DELETE | /api/tokens/{id} | 吊销 token（吊销 initial 会删除令牌文件） | admin |
| GET | /api/audit | 管理操作审计日志 | admin |

示例：

```bash
TOKEN=$(cat data/initial_admin_token)
# 创建程序
curl -s -X POST http://127.0.0.1:8081/api/apps   -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json"   -d '{"name":"gitea","allowed_from":["NoReply@example.com"],"rate_per_hour":500,"display_name":"Gitea"}'
# 分页查询日志
curl -s "http://127.0.0.1:8081/api/messages?app=gitea&status=failed&limit=50"   -H "Authorization: Bearer $TOKEN"
```

三种角色：admin（全部）、operator（程序与退订）、viewer（只读）。同一 IP 认证失败 10 次（10 分钟窗口）封禁 1 小时；`admin.ip_allowlist` 配置后仅允许指定 IP/CIDR 访问 /api。写操作全部落审计日志（含 token 名、IP、动作、对象）。

### 本机 CLI

```bash
# 创建发信程序（临时方式，正式流程走 API；输出一次性 SMTP 密码）
./bin/relayward admin create-app -config config.yaml gitea -from NoReply@example.com
# 忘记所有管理员 token 时，本机重置（吊销全部并重新生成 initial token）
./bin/relayward admin reset -config config.yaml
```

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

## M2 验收自测结果

验收标准：全程只用 API 完成「创建程序 → 发信 → 重置密码 → 旧密码失效」；viewer token 调写接口返回 403。

| 场景 | 预期 | 实际 |
| ---- | ---- | ---- |
| API 创建 operator token | 201，明文只出现一次 | ✅ `{"token":"rw_...","token_info":{...}}` |
| operator 经 API 创建程序 | 201，返回一次性 SMTP 密码 | ✅ 密码立即可用于 SMTP AUTH |
| API 发出的密码发信 | 成功转发，日志 `sent` | ✅ |
| rotate 后旧密码发信 | 535 拒绝 | ✅ |
| rotate 后新密码发信 | 成功 | ✅ |
| GET /api/stats | 各程序 sent/failed/rate_limited/suppressed 计数 | ✅ |
| GET /api/messages 过滤 + 分页 | app/status 筛选、newest-first 游标 | ✅ |
| operator 读 /api/audit | 403 | ✅ |
| 缺 token / 无效 / 过期 | 同一 401 响应体 | ✅ |
| 10 次认证失败后 | 该 IP 被封 429（`ip_banned`），有效 token 亦然 | ✅ |
| viewer 调全部写接口 | 403 | ✅（71 个自动化测试含角色矩阵） |
| SMTP 侧限流（每小时上限） | 超限 451 + 日志 `rate_limited` | ✅ 单元/集成测试覆盖 |

自动化覆盖：api 包 83%（43 个测试 + 28 个子测试）、ratelimit 令牌桶/锁定窗口、smtpd 限流 451 与认证锁定、store 软删除/复活/审计事务/游标分页、admin reset。

## 安全说明

- SMTP 端口建议只对应用服务器 IP 开放；程序分散在公网多台时必须配置 `smtp.tls_cert/tls_key`，否则密码明文传输。
- 配置 TLS 证书后，AUTH 必须先 STARTTLS。
- 管理端口（默认 8081）的公网防护（HTTPS、失败锁定、IP 白名单）在 M2 完善，此前建议只在内网使用。
- 管理员 token 泄露应急：`relayward admin reset`（M2 提供）或删除数据目录后重启。

## 许可

见 [LICENSE](LICENSE)。
