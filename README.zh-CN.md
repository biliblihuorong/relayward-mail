<div align="center">

[English](README.md) | **简体中文**

# Relayward

**自托管的发信网关：所有程序改连网关，真实的提供商 key 只存放在一处。**

单个静态 Go 二进制 · 一个 SQLite 文件 · 运行零依赖

[30 秒宣传片（中文）](docs/media/relayward-promo-zh.mp4) · [30 s promo (EN)](docs/media/relayward-promo-en.mp4) · [部署指南](docs/DEPLOYMENT.zh-CN.md) · [Deployment guide](docs/DEPLOYMENT.md)

</div>

---

[![Relayward 30 秒宣传片](docs/media/relayward-promo-zh.gif)](docs/media/relayward-promo-zh.mp4)

<sub>点击动图观看完整 30 秒视频（含配乐）· [English video](docs/media/relayward-promo-en.mp4)</sub>

## 为什么需要 Relayward

Gitea、Kanboard、Grafana、Wiki、CRM 等自托管工具都要发邮件，通常每个都被塞进一份真实的 SMTP/API key。结果是：key 到处都是，说不清哪个程序发了什么，某个程序狂发会拖垮提供商信誉，退订也没人管。

Relayward 放在中间：

```
 Gitea ─┐
 Kanboard ─┤  各程序独立的       ┌────────────┐    真实提供商 key     ┌──────────┐
 Grafana ─┼─ SMTP 凭据 ──────▶ │ Relayward  │ ───────────────────▶ │ 邮件提供商 │
 Wiki ─┤    (端口 587)         │   网关     │    （只存在这里）      └──────────┘
 CRM ─┘                       └─────┬──────┘
                                    │ 日志 · 限流 · 退订
                          SQLite + 管理 API/页面 (:8081)
                          退订页面              (:8080)
```

- **只有一把真实 key**：程序用各自的 SMTP 密码认证，提供商 key 只在网关里，轮换一次即可。
- **按程序管控**：允许的发件地址、每小时限流、启停、重置密码，全部按程序独立设置。
- **完整发送日志**：每封邮件、每个收件人都有记录：sent、failed、rate_limited、suppressed。
- **退订内置**：RFC 8058 一键退订头、正文页脚链接、确认页、按程序 + 收件人拦截。
- **管理 API 与内嵌网页**：admin / operator / viewer 三种角色，审计日志，IP 白名单，失败锁定。

## 功能一览

| 方面 | 内容 |
| ---- | ---- |
| 中继 | SMTP 入口（AUTH PLAIN/LOGIN、STARTTLS）、发件地址校验、转发上游、按收件人映射结果（上游 4xx → 451，5xx → 554） |
| 安全 | 按程序令牌桶限流、按 IP 失败锁定（10 分钟内 10 次失败封 1 小时）、argon2id 密码哈希、管理 token 只存 SHA-256、所有监听均有 panic 恢复 |
| 退订 | 按收件人拆信、`List-Unsubscribe` + `List-Unsubscribe-Post`、无状态 AES-256-GCM 令牌（链接里看不到邮箱和程序名）、GET 确认页（防邮件扫描误触）、POST 一键退订、重新订阅 |
| 正文注入 | 解析 MIME，在 `text/plain` 与 `text/html` 追加页脚；跳过签名/加密部件、附件、非 UTF-8 部件，带 DKIM 签名的邮件不改正文（仍注入退订头） |
| 管理 | REST API、内嵌管理页（`/admin`）、审计日志、`relayward admin reset` 找回 token |
| 运维 | `/healthz`（上游探测缓存）、Docker `HEALTHCHECK`、结构化日志、优雅停机、日志保留（`log_retention_days`，默认 90 天） |

## 快速开始

需要 `go.mod` 中声明的 Go 版本。

```bash
# 1. 构建（静态二进制，禁用 CGO）-> bin/relayward
make build

# 2. 配置
cp config.example.yaml config.yaml
$EDITOR config.yaml                 # 上游主机/账号、public.base_url（必须是 https）
export UPSTREAM_KEY=你的提供商发信key

# 3. 启动
./bin/relayward serve -config config.yaml
```

首次启动会生成超级管理员 token（前缀 `rw_admin_`）：明文只打印到启动日志**一次**，同时写入 `data/initial_admin_token`（权限 0600），数据库只存 SHA-256 哈希。

创建发信程序并接入：

```bash
TOKEN=$(cat data/initial_admin_token)
curl -s -X POST http://127.0.0.1:8081/api/apps \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"gitea","allowed_from":["noreply@example.com"],"rate_per_hour":500,"display_name":"Gitea"}'
# 响应里包含该程序的一次性 SMTP 密码
```

然后把程序的 SMTP 配置改为：

| 设置 | 值 |
| ---- | -- |
| 主机 / 端口 | 网关地址，`587` |
| 用户名 | 程序名（如 `gitea`） |
| 密码 | 上一步返回的一次性密码 |
| 发件人 | 只能用该程序允许列表内的地址 |

所有程序切换完成后，在提供商后台轮换 key，只更新网关的 `UPSTREAM_KEY`。

也可以直接打开 `http://127.0.0.1:8081/admin`，粘贴 token，全部在浏览器里完成。

> 要上线到公网？请阅读 **[部署指南](docs/DEPLOYMENT.zh-CN.md)**：Docker、HTTPS 反向代理、587 端口 TLS、DNS、防火墙、备份。

## 端口

| 端口 | 用途 | 对谁开放 |
| ---- | ---- | -------- |
| 587 | 程序发信的 SMTP 入口 | 仅应用服务器（防火墙 / VPN） |
| 8080 | 公开：退订页面、`/healthz` | 公网，**经 HTTPS 反向代理** |
| 8081 | 管理 API、`/admin` 页面、`/healthz` | 仅内网 / VPN / SSH 隧道 |

## 管理 API

所有接口要求 `Authorization: Bearer <token>`，JSON 收发，错误统一为 `{"error":{"code":"...","message":"..."}}`。

| 方法 | 路径 | 说明 | 最低角色 |
| ---- | ---- | ---- | -------- |
| GET | `/api/stats` | 各程序发送量、成功/失败/限流/拦截数 | viewer |
| GET | `/api/messages` | 发送日志，支持 app/to/status/since/until 过滤，游标分页 | viewer |
| GET | `/api/apps`、`/api/apps/{name}` | 程序列表 / 详情 | viewer |
| POST | `/api/apps` | 创建程序，响应含一次性 SMTP 密码 | operator |
| PATCH | `/api/apps/{name}` | 启停、退订开关、正文注入、限流、允许发件地址 | operator |
| POST | `/api/apps/{name}/rotate` | 重置 SMTP 密码，旧密码立即失效 | operator |
| DELETE | `/api/apps/{name}` | 软删除（日志保留） | operator |
| GET/POST | `/api/unsubscribes` | 退订列表 / 手动添加（幂等） | viewer / operator |
| DELETE | `/api/unsubscribes/{id}` | 删除退订记录（= 恢复订阅） | operator |
| GET/POST | `/api/tokens` | token 列表 / 创建（明文只返回一次） | admin |
| DELETE | `/api/tokens/{id}` | 吊销 token | admin |
| GET | `/api/audit` | 管理写操作审计日志 | admin |

角色：**admin**（全部）、**operator**（程序与退订）、**viewer**（只读）。

## 退订

退订开关打开的程序（默认打开）按**收件人**拆信：每个收件人一封独立副本，带各自的退订链接，并剥掉 `Bcc`：

```
List-Unsubscribe: <https://mail.example.com/u/TOKEN>
List-Unsubscribe-Post: List-Unsubscribe=One-Click
```

- `GET /u/{token}` 只显示确认页，不生效（邮件安全扫描不会误退订）。
- 表单「确认退订」（带 CSRF 校验）或邮件客户端的 RFC 8058 一键退订 `POST` 才生效。
- 已退订的收件人不再转发，但程序仍收到 `250`，日志记为 `suppressed`。
- 退订按「程序 + 邮箱」隔离：退订 A 程序不影响 B 程序。
- `unsubscribe.secret` 首次启动自动生成（`data/unsubscribe_secret`）；更换它会使所有已发出的链接失效。

## 本机 CLI

```bash
./bin/relayward serve -config config.yaml
./bin/relayward admin create-app -config config.yaml gitea -from noreply@example.com
./bin/relayward admin reset -config config.yaml     # 忘记所有管理员 token：吊销全部并重新生成
./bin/relayward healthcheck -url http://127.0.0.1:8081/healthz
```

## 配置

见 [`config.example.yaml`](config.example.yaml)（所有 `${VAR}` 从环境变量展开）。

| 配置项 | 含义 |
| ------ | ---- |
| `data_dir` | SQLite 文件与密钥文件（`initial_admin_token`、`unsubscribe_secret`） |
| `smtp.listen`、`smtp.tls_cert`、`smtp.tls_key`、`smtp.max_message_size` | 入口监听；证书和私钥**同时**配置才启用 STARTTLS（启用后 AUTH 必须先 STARTTLS） |
| `upstream.*` | 提供商主机、端口、用户名、密码（`${UPSTREAM_KEY}`）、`tls: starttls\|none` |
| `public.listen`、`public.base_url` | 面向收件人的监听；`base_url` 必须是 `https://` |
| `admin.listen`、`admin.ip_allowlist` | 管理监听；用 IP/CIDR 限制 `/api` |
| `unsubscribe.secret`、`unsubscribe.footer_text` | 令牌密钥（留空自动生成）与页脚文案（支持 `{app}` 占位符） |
| `log_retention_days` | 删除早于该天数的发送日志（默认 90） |

## 安全说明

- 587 端口只对应用服务器开放；程序经公网连接时必须配置 `smtp.tls_cert`/`tls_key`，否则密码明文传输。
- **不信任** `X-Forwarded-For`。放在反向代理后，管理端的 IP 白名单与失败锁定看到的是代理地址，所以不要把 `:8081` 挂到公网代理上，请经 VPN 或 SSH 隧道访问。
- 丢了所有管理员 token：在主机上运行 `relayward admin reset`。
- 各项设计取舍与理由见 [`docs/DECISIONS.md`](docs/DECISIONS.md)。

## 开发

```bash
make test   # CGO_ENABLED=1 go test -race ./...（需要 C 编译器）
make lint   # golangci-lint run
make run    # 构建并启动
```

- 仅依赖计划书「技术选型」表中的库（另允许 `golang.org/x/sync/errgroup`）；新增依赖须记入 `docs/DECISIONS.md`。
- 提交信息使用 Conventional Commits。
- 重新生成宣传片：`pip install pillow`，然后 `python promo/make_video.py all`（需要 `ffmpeg`）。

## 文档

| 文档 | 内容 |
| ---- | ---- |
| [docs/DEPLOYMENT.zh-CN.md](docs/DEPLOYMENT.zh-CN.md) · [English](docs/DEPLOYMENT.md) | 部署到公网 |
| [docs/DECISIONS.md](docs/DECISIONS.md) | 设计决策与取舍 |
| [docs/ACCEPTANCE.zh-CN.md](docs/ACCEPTANCE.zh-CN.md) | M1–M5 各里程碑验收自测结果 |
| [docs/Relayward 邮件网关计划书.md](docs/Relayward%20邮件网关计划书.md) | 原始设计计划书 |

## 当前状态

五个里程碑（M1 中继、M2 管理 API、M3 退订、M4 正文注入、M5 内嵌管理页）均已实现并有自动化测试覆盖。尚未在真实环境验证：Gmail 原生退订按钮，以及更多真实程序的邮件样本。

## 许可

见 [LICENSE](LICENSE)。
