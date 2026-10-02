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
