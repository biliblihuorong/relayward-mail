# Relayward 邮件网关计划书

Oct 2, 2026 · @MiskaShell

## 背景与目标

做一个轻量的发信网关 Relayward（仓库 relayward-mail）：所有程序改连网关，网关独占提供商的真实 key，并在转发时按程序记录、限流、注入退订。

现状是多个自建开源程序共用一个 NoReply 邮箱（如 NoReply@example.com）和同一个发信 key。问题有三个：不知道谁发了什么，key 泄露后无法定位和单独吊销，收件人也没有退订途径。

第一版要达成：

- 每个程序一个独立 SMTP 账号，可单独停用、重置。
- 每封信记录元数据，可按程序、收件人、时间查询。
- 按「程序 + 收件人」支持一键退订，退订后网关自动拦截。
- 全部配置可通过管理 API 操作，管理 API 可放公网。
- 单个 Go 二进制 + 一个 SQLite 文件，部署零依赖。

第一版不做：多台网关汇总的大管理平台、收信功能、自建投递（仍由提供商发出）、邮件打开和点击追踪。

## 总体架构

网关是一个 Go 二进制，开三个端口：SMTP 给程序、退订页面给收件人、管理 API 给你自己，数据都在一个 SQLite 文件里。

&#91;embedded content: 网关架构 · 三个端口、一个 SQLite\]

程序只知道自己的网关账号，真实 key 只在网关配置里；收件人退订和管理员调用 API 都走 HTTPS。

## 核心功能

一封信进入网关后依次经过认证、拆信、拦截判断、注入、转发、记录六步。

### 1. SMTP 认证

- 监听内网 SMTP 端口（默认 587），支持 STARTTLS，只接受 AUTH PLAIN / LOGIN。
- 用户名即程序名，密码以 argon2id 哈希存储。
- 停用的程序返回 535 认证失败。
- MAIL FROM 校验：每个程序配置允许的发件地址（默认只允许 NoReply@example.com），不符合则拒收。

### 2. 按收件人拆信

- 一封信有 N 个 RCPT TO，就拆成 N 封单独转发，每封带各自的退订链接。
- 退订开关关闭的程序不拆，原样转发。
- 去掉 Bcc 头，保留 To / Cc 原样。

### 3. 拦截

- 退订开关打开时，查退订表 `(app, email)`，已退订则不转发。
- 对程序仍返回 250 成功，日志记为 `suppressed`，避免程序报错重试。
- 限流：每个程序配置每小时上限（令牌桶），超限返回 451 临时错误，程序会自动重试。

### 4. 退订注入

令牌用 AES-256-GCM 加密，链接里看不到邮箱和程序名；无状态计算，不存储：

```
key   = HKDF-SHA256(secret, "unsubscribe-v1")
token = base64url( 版本号 1B | nonce 12B | AES-GCM(key, app_id | email) )
# GCM 自带完整性校验，篡改或伪造的 token 解密即失败
# 链接长度约 80 字符；更换 secret 会使旧链接全部失效
```

每封信加两个头（RFC 8058 一键退订）：

```
List-Unsubscribe: <https://mail.example.com/u/TOKEN>
List-Unsubscribe-Post: List-Unsubscribe=One-Click
```

正文注入为第二阶段，形式是「尾部水平线 + 一行小字」，只改文本部分，不碰附件：

- **text/html**：在 `</body>` 前插入一条浅灰水平线和一行 12px 灰色小字，如「不想再收到来自 Gitea 的邮件？退订」，「退订」为链接。样式全部写成行内 style，因为很多邮箱客户端会删掉 `<style>`。没有 `</body>` 时追加到末尾。
- **text/plain**：末尾追加一个空行、分隔线 ` --  ` 和一行「退订：<链接>」。
- 按原部分的编码（quoted-printable / base64）和字符集解码、插入、再编码。
- 签名或加密的邮件（multipart/signed、multipart/encrypted）不改正文，只加头部。
- 小字文案可在配置中改，支持 `{app}` 占位符，取程序的 display\_name。

### 5. 退订页面

- `GET /u/{token}`：显示确认页（「确认不再接收来自 X 的邮件」），不生效，防止邮件安全扫描误触。
- `POST /u/{token}`：真正生效；邮箱客户端的一键退订也走这里。
- 退订成功页提供「重新订阅」按钮。
- 令牌解密失败返回 404，不揭示任何信息。

### 6. 转发与记录

- 用配置文件中的真实 key 转发到提供商 SMTP。
- 上游临时失败时对程序返回 451，由程序重试；第一版不做网关内部队列。
- 日志只记元数据：时间、程序、发件人、收件人、主题、大小、Message-ID、结果、上游响应，不存正文。
- 日志默认保留 90 天，可配置。

## 管理与认证

管理 API 放公网，靠「首次启动生成管理员 token + 强制 HTTPS + 失败锁定 + 审计日志」四层防护。

### 首次启动

1. 检测到数据库中没有任何管理员 token。
2. 随机生成一个超级管理员 token（前缀 `rw_admin_`，32 字节随机数）。
3. 明文只打印到启动日志一次，同时写入 `data/initial_admin_token`（权限 0600）。
4. 数据库只存 SHA-256 哈希。
5. 管理员用它登录后，可以创建新 token、吊销初始 token；初始 token 被吊销后自动删除那个文件。
6. 忘记所有 token 时，在服务器上执行 `relayward admin reset` 重新生成。这个命令只能本机执行，不开放 API。

### Token 体系

| 角色     | 能做什么                         | 用途             |
| -------- | -------------------------------- | ---------------- |
| admin    | 全部操作，包括管理 token         | 你自己           |
| operator | 管理程序账号、退订；不能管 token | 脚本、自动化开通 |
| viewer   | 只读：统计、日志、退订列表       | 以后的汇总平台   |

每个 token 有名称、创建人、可选过期时间、最后使用时间。请求方式为 `Authorization: Bearer <token>`。

### 管理 API

| 方法   | 路径                    | 说明                                         | 最低角色 |
| ------ | ----------------------- | -------------------------------------------- | -------- |
| GET    | /api/stats              | 各程序发送量、成功 / 失败 / 拦截数           | viewer   |
| GET    | /api/messages           | 发送日志，可按 app、to、时间、结果筛选，分页 | viewer   |
| GET    | /api/apps               | 程序列表及状态                               | viewer   |
| GET    | /api/apps/{name}        | 单个程序配置与统计                           | viewer   |
| POST   | /api/apps               | 创建程序，返回一次性 SMTP 密码               | operator |
| PATCH  | /api/apps/{name}        | 启停、退订开关、限流、允许发件地址           | operator |
| POST   | /api/apps/{name}/rotate | 重置 SMTP 密码                               | operator |
| DELETE | /api/apps/{name}        | 删除程序，日志保留                           | operator |
| GET    | /api/unsubscribes       | 退订列表                                     | viewer   |
| POST   | /api/unsubscribes       | 手动添加退订                                 | operator |
| DELETE | /api/unsubscribes/{id}  | 恢复订阅                                     | operator |
| GET    | /api/tokens             | token 列表（不含明文）                       | admin    |
| POST   | /api/tokens             | 创建 token，返回一次性明文                   | admin    |
| DELETE | /api/tokens/{id}        | 吊销 token                                   | admin    |
| GET    | /api/audit              | 管理操作审计日志                             | admin    |

健康检查：`GET /healthz`，三个端口都响应，不需要 token。

- 正常返回 200 `{"status":"ok"}`；数据库不可用或上游连不上时返回 503 `{"status":"degraded"}`。
- 带 viewer 及以上 token 请求时，返回各组件明细：数据库、上游连通性、最近一次成功转发时间、版本号、运行时长。
- 上游连通性由后台每 60 秒探测一次（只建立连接和 EHLO，不发信），健康检查只读缓存结果，不会被刷成对上游的压力。
- 可直接用于 Docker HEALTHCHECK、Uptime Kuma 等监控。

上游提供商的 key 不开放 API，只在配置文件中修改。

### 公网暴露的防护

- **强制 HTTPS**：由反向代理（Caddy / Nginx）终止 TLS，或网关内置 ACME 自动证书。
- **失败锁定**：同一 IP 10 分钟内认证失败 10 次，封禁 1 小时；SMTP 认证同理。
- **统一错误响应**：token 不存在、已吊销、已过期都返回同一个 401。
- **可选 IP 白名单**：配置后只允许指定 IP 段访问管理 API，默认关闭。
- **审计日志**：所有写操作记录 token 名称、IP、动作、对象、时间。
- **管理 API 与退订页面分端口**：以后想收紧时，可以只关管理端口的公网访问。

## 数据模型

SQLite 单文件，五张表，开启 WAL 模式。

```sql
CREATE TABLE apps (
  id               INTEGER PRIMARY KEY,
  name             TEXT UNIQUE NOT NULL,      -- 即 SMTP 用户名
  password_hash    TEXT NOT NULL,             -- argon2id
  enabled          INTEGER NOT NULL DEFAULT 1,
  unsubscribe      INTEGER NOT NULL DEFAULT 1, -- 退订开关
  allowed_from     TEXT NOT NULL,             -- JSON 数组
  rate_per_hour    INTEGER NOT NULL DEFAULT 500,
  display_name     TEXT,                      -- 退订页显示的程序名
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
);

CREATE TABLE messages (
  id            INTEGER PRIMARY KEY,
  app_id        INTEGER NOT NULL,
  ts            INTEGER NOT NULL,
  mail_from     TEXT NOT NULL,
  rcpt_to       TEXT NOT NULL,
  subject       TEXT,
  size          INTEGER,
  message_id    TEXT,
  status        TEXT NOT NULL,   -- sent / suppressed / rate_limited / failed
  upstream_resp TEXT,
  client_ip     TEXT
);
CREATE INDEX idx_msg_app_ts ON messages(app_id, ts);
CREATE INDEX idx_msg_rcpt   ON messages(rcpt_to);

CREATE TABLE unsubscribes (
  id       INTEGER PRIMARY KEY,
  app_id   INTEGER NOT NULL,
  email    TEXT NOT NULL,          -- 小写规范化
  ts       INTEGER NOT NULL,
  source   TEXT NOT NULL,          -- link / one_click / api
  UNIQUE(app_id, email)
);

CREATE TABLE admin_tokens (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL,
  token_hash  TEXT UNIQUE NOT NULL, -- SHA-256
  role        TEXT NOT NULL,        -- admin / operator / viewer
  expires_at  INTEGER,
  last_used   INTEGER,
  created_by  INTEGER,
  created_at  INTEGER NOT NULL
);

CREATE TABLE audit_log (
  id        INTEGER PRIMARY KEY,
  ts        INTEGER NOT NULL,
  token_id  INTEGER,
  ip        TEXT,
  action    TEXT NOT NULL,   -- app.create / app.rotate / token.revoke ...
  target    TEXT,
  detail    TEXT             -- JSON
);
```

说明：

- 删除程序时不删 messages，只是 app 行做软删除或保留 id，保证日志可追溯。
- 退订是按程序记录的：用户退订了 A，仍能收到 B 的邮件。
- 日志清理由后台任务每天执行一次，按配置的保留天数删除。

## 配置与部署

配置文件只放启动必需和不应走 API 的内容，程序账号等都在数据库里。

```yaml
# config.yaml
data_dir: ./data

smtp:
  listen: ":587"
  tls_cert: /etc/relayward/cert.pem   # 可选，空则不开 STARTTLS
  tls_key:  /etc/relayward/key.pem
  max_message_size: 10MB

upstream:
  host: smtp.provider.com
  port: 587
  username: apikey
  password: ${UPSTREAM_KEY}            # 从环境变量读
  tls: starttls

public:                                # 退订页面
  listen: ":8080"
  base_url: https://mail.example.com

admin:                                 # 管理 API
  listen: ":8081"
  ip_allowlist: []                     # 空 = 不限制

unsubscribe:
  secret: ${UNSUB_SECRET}              # 首次启动未设置则自动生成并存入 data_dir
  footer_text: "不想再收到此类邮件？点此退订"

log_retention_days: 90
```

部署方式：

1. 下载二进制或拉取 Docker 镜像，写好 config.yaml 和环境变量。
2. 首次启动，从日志或 `data/initial_admin_token` 取得管理员 token。
3. 用 Caddy / Nginx 反代 8080 和 8081 并申请证书，可以是两个子域名（如 `mail.example.com` 和 `mailadmin.example.com`）。
4. 调用 `POST /api/apps` 为每个程序创建账号，把各程序的 SMTP 配置改成网关地址和新账号。
5. 所有程序切换完成后，在提供商后台轮换 key，只更新网关配置。旧 key 从此失效。

SMTP 端口建议只对你的服务器 IP 开放（云防火墙或安全组）。如果程序分散在多台公网服务器上，必须配置 TLS 证书，否则 SMTP 密码会明文传输。

## 技术选型

用 Go 实现，全部依赖为纯 Go，交叉编译无需 CGO。仓库与 module 名为 relayward-mail，二进制与主包名为 relayward。

| 用途            | 库                                                 |
| --------------- | -------------------------------------------------- |
| SMTP 服务端     | github.com/emersion/go-smtp                        |
| SMTP AUTH       | github.com/emersion/go-sasl                        |
| MIME 解析与重组 | github.com/emersion/go-message                     |
| 转发上游        | 标准库 net/smtp，或 go-smtp 自带客户端             |
| 数据库          | modernc.org/sqlite                                 |
| HTTP 路由       | 标准库 net/http（Go 1.22+ 路由支持方法和路径参数） |
| 密码哈希        | golang.org/x/crypto/argon2                         |
| 限流            | golang.org/x/time/rate                             |
| 配置            | gopkg.in/yaml.v3                                   |
| 页面模板        | 标准库 html/template + embed                       |

目录结构：

```
relayward-mail/
├── cmd/relayward/main.go     # 入口、子命令（serve / admin reset）
├── internal/config/            # 配置加载
├── internal/store/             # SQLite 访问与迁移
├── internal/smtpd/             # SMTP 服务端、认证、拆信
├── internal/relay/             # 上游转发
├── internal/unsub/             # token、头部与正文注入
├── internal/web/               # 退订页面
├── internal/api/               # 管理 API、token 中间件、审计
├── internal/ratelimit/         # 发信限流 + 认证失败锁定
└── web/templates/              # 内嵌页面模板
```

## 里程碑

分五个阶段交付，每个阶段结束都能独立使用。

1. **M1 可用的中继**：SMTP 认证、发件地址校验、转发上游、发送日志、首次启动生成管理员 token、健康检查 /healthz。
   - 验收：用 swaks 或任一程序通过网关发信成功，日志表有记录；错误密码被拒。
2. **M2 管理 API**：程序和 token 的增删改查、三种角色、审计日志、失败锁定、限流。
   - 验收：全程只用 API 完成「创建程序 → 发信 → 重置密码 → 旧密码失效」；viewer token 调写接口返回 403。
3. **M3 退订**：按收件人拆信、退订头注入、退订页面、拦截、退订 API。
   - 验收：Gmail 显示原生退订按钮；点击后再发信状态为 suppressed；另一个程序照常发到。
4. **M4 正文注入**：MIME 解析，在纯文本和 HTML 部分插入退订链接。
   - 验收：用 3 个以上真实程序的邮件样本测试，内容不乱码、附件完好。
5. **M5 内嵌管理页**：用 embed 打包一个简单页面，输入 token 后可看统计、查日志、管理程序和退订。页面只调用管理 API，不开新接口。
   - 验收：不用 curl 也能完成日常管理。

M1 完成后就可以开始把程序逐个切到网关上，全部切完就轮换上游 key。

## 后续规划与风险

汇总管理平台以后只需用 viewer / operator token 调用各网关的同一套 API，网关本身不用改。

可能的后续功能：

- 网关内部发送队列与重试，上游故障时不依赖程序重试。
- 接收提供商的退信 / 投诉 webhook，硬退信地址自动加入拦截表。
- 按主题关键词豁免退订拦截（如验证码、密码重置）。
- 异常告警：某程序发送量突增、失败率过高时通知。
- Prometheus 指标接口。

风险与应对：

| 风险                   | 影响                               | 应对                                                         |
| ---------------------- | ---------------------------------- | ------------------------------------------------------------ |
| 网关宕机               | 所有程序发不出信                   | systemd / Docker 自动重启；监控 /healthz；紧急时临时改回直连提供商 |
| 管理员 token 泄露      | 他人可创建发信账号                 | token 由使用者自行保管；审计日志完整记录所有写操作，便于定位和恢复；本机 `admin reset` 可吊销全部 token |
| 正文注入破坏邮件       | 乱码或排版错乱                     | 只改文本部分不动附件；解析失败时只注入头部、原样转发；按程序可关闭正文注入 |
| 程序自己做了 DKIM 签名 | 改正文后签名失效，邮件可能进垃圾箱 | 网关检测到 DKIM-Signature 头时自动跳过正文注入并记录警告；推荐做法是关掉程序的 DKIM，由提供商统一签名 |

## Go 开发规范（交给 AI 实现时遵守）

AI 编码助手按本计划书实现时，以下规范为强制要求；与上文冲突时以上文的功能设计为准。

### 工作方式

1. 按里程碑顺序实现，一次只做一个里程碑，不提前实现后续功能。
2. 不实现计划书以外的功能。遇到未定义的行为，选最简单、最安全的做法，并记入 `docs/DECISIONS.md`（日期、问题、决定、理由）。
3. 每个里程碑结束前，以下命令必须全部通过：`go build ./...`、`go vet ./...`、`golangci-lint run`、`go test -race ./...`。
4. 每个里程碑更新 README 中对应的使用说明，并列出验收标准的自测结果。
5. 提交信息使用 Conventional Commits（`feat:`、`fix:`、`test:`、`docs:`、`refactor:`），一次提交只做一件事。

### 基本约定

- Go 1.23 及以上；`CGO_ENABLED=0`，产物为静态二进制。
- 代码由 `gofmt` 和 `goimports` 格式化，不接受手动对齐。
- 依赖只用「技术选型」表中的库，另允许 `golang.org/x/sync/errgroup`；新增任何依赖须写入 DECISIONS.md 并说明理由。
- 标识符、代码注释、日志信息用英文；面向收件人的退订页面和邮件小字用中文，并可配置。
- 所有导出的类型和函数写 godoc 注释，以名称开头。
- 版本号通过 `-ldflags "-X main.version=..."` 注入，在 /healthz 和启动日志中输出。

### 代码结构

- 业务代码全部放在 `internal/` 下，按「技术选型」中的目录划分；`cmd/relayward/main.go` 只做参数解析、组装和启动，不写业务逻辑。
- 包名用单个小写单词，禁止 `utils`、`common`、`helpers` 这类包名。
- 禁止可变全局变量和带副作用的 `init()`。依赖通过构造函数 `New(...)` 传入。
- 接口定义在使用方，保持小而专一；不为只有一个实现的类型预先抽接口，测试需要替身时除外。
- 单个函数尽量不超过 60 行，单个文件尽量不超过 500 行。

### 错误处理

- 每个 error 都必须处理，禁止用 `_` 丢弃（`Close` 的返回值在只读场景下例外）。
- 向上传递时用 `fmt.Errorf("relay to upstream: %w", err)` 包装，判断用 `errors.Is` / `errors.As`。
- 可预期的错误定义为哨兵错误，如 `ErrAppNotFound`、`ErrRateLimited`。
- 除启动阶段的配置错误外禁止 `panic`。HTTP 和 SMTP 处理器外层加 recover，记录后返回 500 / 451。
- 返回给 API 调用方和 SMTP 客户端的错误不包含内部细节（SQL、文件路径、上游原始响应），细节只进日志。

### 并发与生命周期

- 所有可能阻塞的函数第一个参数为 `context.Context`，上游连接、数据库查询都设超时。
- 主程序用 `signal.NotifyContext` 监听 SIGINT / SIGTERM，用 errgroup 管理各服务；收到信号后停止接新连接，等待进行中的请求最多 30 秒。
- 每个 goroutine 必须有明确的退出路径，禁止「开了就不管」。
- 共享状态用 mutex 保护，所有测试在 `-race` 下运行。

### 日志

- 只用标准库 `log/slog`，默认 JSON 输出，级别可配置。
- 字段名统一 snake\_case，常用字段固定为 `app`、`rcpt`、`msg_id`、`remote_ip`、`status`、`duration_ms`。
- **禁止**写入日志：SMTP 密码、管理 token、上游 key、退订 secret、邮件正文、完整退订链接。

### 安全

- 随机数只用 `crypto/rand`。
- 密码哈希用 argon2id（m=19 MiB、t=2、p=1，参数与盐存入哈希字符串）；token 与哈希比较用 `subtle.ConstantTimeCompare`。
- SQL 一律使用参数化查询，禁止拼接字符串。
- `http.Server` 必须设置 `ReadHeaderTimeout`、`ReadTimeout`、`WriteTimeout`、`IdleTimeout`；请求体用 `http.MaxBytesReader` 限制为 1 MB。
- 写入邮件头的任何值都要校验不含 CR / LF，防止头部注入。
- 退订页面和管理页面加安全响应头：`Content-Security-Policy`、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`；退订确认表单带 CSRF 防护。
- 密钥只从配置文件或环境变量读取，代码和测试中不得出现真实密钥。

### 数据库

- 不使用 ORM，直接写 SQL，集中在 `internal/store`。
- 迁移文件放在 `internal/store/migrations/0001_init.sql` 这样的文件中，用 embed 打包，启动时按 `PRAGMA user_version` 顺序执行；已发布的迁移文件禁止修改。
- 连接开启 `journal_mode=WAL`、`busy_timeout=5000`、`foreign_keys=ON`；写连接池限制为 1。
- 时间在数据库中存 UTC Unix 秒；邮箱地址入库前统一转小写并去掉首尾空白。
- 多步写操作放在事务里，审计日志与对应的写操作在同一事务中提交。

### API 规范

- 请求和响应都是 JSON，字段名 snake\_case，时间用 RFC 3339 UTC 字符串。
- 错误格式统一为 `{"error":{"code":"app_not_found","message":"..."}}`，`code` 为稳定的机器可读标识。
- 状态码：创建成功 201、删除成功 204、参数错误 400、未认证 401、权限不足 403、不存在 404、冲突 409、被限流 429。
- 列表接口用游标分页：`?limit=50&cursor=...`，响应带 `next_cursor`；`limit` 最大 200。
- 所有写接口写审计日志。一次性明文（SMTP 密码、管理 token）只在创建或重置的响应里出现一次。

### 测试

- 采用表驱动测试；邮件样本放在 `testdata/`，正文注入用 golden 文件对比输出。
- 必须覆盖：token 加解密与篡改检测、SMTP 认证与发件人校验、拆信、拦截、限流、各角色权限、DKIM 邮件跳过注入。
- 集成测试用 go-smtp 在本地起一个假上游，验证端到端的转发内容，不连接真实提供商。
- `internal/unsub`、`internal/api`、`internal/smtpd` 的测试覆盖率不低于 70%。

### 构建与交付

- 提供 `Makefile`，包含 `build`、`test`、`lint`、`run` 四个目标。
- 提供 `.golangci.yml`，至少启用 errcheck、govet、staticcheck、gosec、revive、errorlint、bodyclose、unused。
- 提供多阶段 `Dockerfile`：构建阶段用官方 golang 镜像，运行阶段用 `gcr.io/distroless/static`，以非 root 用户运行，内置 HEALTHCHECK 调用 /healthz。
- 提供 `config.example.yaml`，所有密钥字段用占位符。
