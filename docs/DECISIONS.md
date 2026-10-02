# 开发决定记录（DECISIONS.md）

计划书未明确定义的行为，按「最简单、最安全」原则处理并记录于此。

## M1（2026-10-02）

| 日期 | 问题 | 决定 | 理由 |
| ---- | ---- | ---- | ---- |
| 2026-10-02 | 迁移文件 0001 是否只建 M1 用到的表 | `0001_init.sql` 一次性建立计划书数据模型的全部五张表和索引 | 计划书已给出完整 schema；已发布的迁移文件禁止修改，避免后续里程碑重复拆分建表迁移 |
| 2026-10-02 | M1 没有 管理 API（M2），无法创建发信程序，M1 无法独立验收 | 新增本机 CLI 子命令 `relayward admin create-app NAME [-from ADDR]`，打印一次性 SMTP 密码 | 「每个阶段结束都能独立使用」；本机命令不新增网络攻击面；不写审计日志（审计随 M2 API 一起实现） |
| 2026-10-02 | Dockerfile 要求 distroless（无 shell/curl）又要内置 HEALTHCHECK 调 /healthz | 新增子命令 `relayward healthcheck [-url URL]`，默认从 config 推导 admin 监听地址并 GET /healthz | 满足计划书对 distroless + HEALTHCHECK 的同时要求的最小做法 |
| 2026-10-02 | /healthz 在 M1 挂在哪个端口 | M1 先在 admin 监听（默认 :8081）上提供 /healthz，M2 在其上扩展管理 API；public 监听（:8080）M3 一起启动 | admin HTTP 服务是 /healthz 的宿主，避免 M1 空开 public 端口 |
| 2026-10-02 | 上游 5xx 永久拒绝时对程序返回什么 | 上游 4xx / 连接失败 → 451；上游 5xx → 554，日志记 `failed` | 计划书只定义了临时失败返回 451；永久失败若也返回 451 会导致程序无限重试 |
| 2026-10-02 | MAIL FROM 不在允许列表时返回什么状态码 | 550 5.7.1 `sender address not allowed` | 拒收语义明确；错误信息不含内部细节 |
| 2026-10-02 | 转发是否需要缓存整个消息 | DATA 阶段将消息读入内存（上限 `max_message_size`，默认 10MB），用于统计大小、提取 Subject / Message-ID 并转发 | M1 最简单做法；上限受 go-smtp 强制，不产生无限内存占用 |
| 2026-10-02 | SQLite 连接池大小 | 整个进程单连接（`SetMaxOpenConns(1)`），打开时设置 WAL / busy_timeout / foreign_keys | 单连接串行化写入，规避 SQLITE_BUSY；M1 规模下读并发不是瓶颈 |
| 2026-10-02 | SMTP 是否允许无 TLS 明文认证 | 配置了 `tls_cert` 时 AUTH 必须先 STARTTLS；未配置证书时允许明文 AUTH | 计划书允许内部网络不配证书；配了证书则默认收紧 |
| 2026-10-02 | SMTP 问候语 / EHLO 域名用什么 | 取 `public.base_url` 的 host，解析失败回退 `relayward` | 计划书配置里没有独立字段，用退订页域名最自然 |
| 2026-10-02 | 日志级别如何配置 | 环境变量 `LOG_LEVEL`（debug/info/warn/error，默认 info） | 计划书要求「级别可配置」但未指定载体，环境变量零配置成本 |
| 2026-10-02 | 日志保留天数清理（`log_retention_days`）落在哪个里程碑 | 随 M1 的发送日志一起实现（后台每日清理 `messages`） | 清理是日志功能的组成部分，M1 交付后即可长期独立运行 |
| 2026-10-02 | 上游转发客户端选 go-smtp 还是标准库 | 标准库 `net/smtp`（计划书两个选项之一） | go-smtp v0.25 客户端没有公开的单连接 STARTTLS 方法且 EHLO 名固定为 localhost；net/smtp 支持显式 STARTTLS、自定义 EHLO 名。注：`-race` 在 Windows 需要本机 C 编译器（gcc），本地已通过 scoop 安装，CI/Linux 原生可用 |
| 2026-10-02 | 配置加载对未知字段的处理 | 严格模式（`KnownFields(true)`），未知字段报错 | 拼写错误的配置键静默失效比启动失败更危险 |
| 2026-10-02 | `public.base_url` 的协议约束 | 只允许 `https`，否则启动报错 | 该地址会出现在发给收件人的退订链接中（M3），必须 HTTPS |

## M2（2026-10-02）

| 日期 | 问题 | 决定 | 理由 |
| ---- | ---- | ---- | ---- |
| 2026-10-02 | 删除程序后 messages 表 app_id 如何保持可追溯 | 迁移 0002 给 apps 加 `deleted_at` 软删除；同名字段重建时原地复活同一行（保留 id），日志 join 不丢名字 | 计划书要求「软删除或保留 id」；复活机制避免同名 409 死锁 |
| 2026-10-02 | 审计要求记录 token 名称，但 token 可能事后被吊销 | 迁移 0002 给 audit_log 加 `token_name` 列，写审计时固化名称 | 仅靠 token_id join 在吊销后丢失名称 |
| 2026-10-02 | 限流 0 值语义 | rate_per_hour 只接受正整数（0/负数 → 400），不提供「不限量」模式 | 防误配导致网关被打爆；计划书默认 500 |
| 2026-10-02 | 反向代理后的客户端 IP 来源 | 只取 TCP 对端地址，不信任 X-Forwarded-For | XFF 可伪造；代理场景所有请求共享代理 IP，运营需在代理层还原真实 IP |
| 2026-10-02 | 被锁定 IP 的 HTTP 响应 | 429 `{"error":{"code":"ip_banned"}}`，即使持有效 token 也拒绝 | 计划书状态码表有 429；与「统一 401」不冲突（401 针对凭据状态） |
| 2026-10-02 | /healthz 是否受失败锁定与 IP 白名单约束 | 不受；锁定与白名单只作用于 /api/* | 监控与 Docker HEALTHCHECK 的可用性不应依赖凭据，且 healthz 不泄露信息 |
| 2026-10-02 | CLI 写操作（create-app / admin reset）是否写审计 | 写；token_name 记为 "local-cli"/"system"，ip 记为 "local" | 计划书「所有写操作记录审计」；本地命令没有 Bearer token |
| 2026-10-02 | 统计接口的时间范围 | 全量统计，不提供时间窗参数 | 计划书未定义查询参数，选最简单做法；后续需要再加 since |
| 2026-10-02 | 创建的管理 token 前缀 | `rw_` + 43 字符 base64url；初始 token 按计划书保持 `rw_admin_` | 计划书只规定了初始 token 格式 |
| 2026-10-02 | /api 上不带 token 的请求是否计入失败锁定 | 计入（与无效 token 同样对待） | 缺 token 的探测同样是爆破行为 |
