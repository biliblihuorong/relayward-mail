# 验收自测记录（M1–M5）

本文件从旧版 README 迁出，按里程碑记录各自的验收场景与自测结果。English readers: see [README.md](../README.md) for the feature overview; this log is kept in Chinese.

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

## M3 验收自测结果

验收标准（计划书）：Gmail 显示原生退订按钮；点击后再发信状态为 suppressed；另一个程序照常发到。Gmail 原生按钮依赖真实 Gmail 环境，自测以「与 Gmail 相同的 RFC 8058 一键退订 POST」等价模拟（真实二进制 + 假上游 + 真实 HTTP/SMTP 客户端）。

| 场景 | 预期 | 实际 |
| ---- | ---- | ---- |
| 2 个 RCPT + Bcc 头 + 退订开 | 拆成 2 封，各带各自退订链接，Bcc 剥离，To/Cc 原样 | ✅ 上游收到 2 封单收件人副本，令牌互不相同 |
| 令牌内容 | 链接中看不到邮箱与程序名，可解出 (app, email) | ✅ AES-256-GCM，篡改/伪造一律 404 |
| `GET /u/{token}` 确认页 | 显示确认文案，不产生退订 | ✅ 中文确认页（程序名 + 邮箱），退订表无记录 |
| RFC 8058 一键退订 POST（模拟 Gmail） | 直接退订成功 | ✅ 200，退订表 source=one_click，审计记录 public + IP |
| 退订后再发信 | 该收件人状态 `suppressed`，程序仍收 250 | ✅ 上游未收到该副本，日志一行 suppressed |
| 另一个程序发同一邮箱 | 照常送达 | ✅ kanboard → bob 照常 sent（退订按程序隔离） |
| 表单 POST 无 CSRF / 篡改 CSRF | 400 拒绝 | ✅ |
| 表单确认退订 + 重新订阅按钮 | 退订生效；重新订阅后恢复投递 | ✅ source=link，重新订阅后再次发信送达 |
| POST /api/unsubscribes 手动退订 | 201 + 邮箱小写规范化；重复添加幂等 200 | ✅ 手动退订后发信 suppressed，DELETE 后恢复 |
| 退订列表 / 过滤 / 分页 | viewer 可读，operator 可写，viewer 写 403 | ✅ app/email 过滤、游标分页、角色矩阵 |
| 审计 | unsubscribe.create / unsubscribe.delete 落审计 | ✅ 含 token 名（public / admin）与 IP |
| `unsubscribe.secret` 未设置 | 首次启动自动生成存入 data_dir | ✅ `unsubscribe_secret` 文件生成，重启后复用 |

自动化覆盖：unsub 包 93%（token 加解密/篡改检测/跨密钥、头部注入、Bcc 剥离含折行）、smtpd 88%（拆信逐收件人注入、拦截 250、全部拦截、退订关原样转发含 Bcc、部分上游失败 451、manager 缺失 451）、api 83%（退订端点 + 角色 + 幂等 + 审计）、web 81%（确认页无副作用、一键退订、CSRF、重新订阅、安全响应头）、store 79%（退订表 CRUD/过滤/分页/审计事务）。

## M4 验收自测结果

验收标准（计划书）：用 3 个以上真实程序的邮件样本测试，内容不乱码、附件完好。

样本在 `internal/unsub/testdata/`（Gitea 风格 alternative、GitLab 风格 mixed+PDF 附件、PGP 签名件），golden 文件对比输出，并用 Python email 解析器独立复核解码结果。

| 场景 | 预期 | 实际 |
| ---- | ---- | ---- |
| multipart/alternative（7bit 纯文本 + qp HTML，中文主题） | 两部分都注入页脚，内容解码后无乱码 | ✅ 纯文本升级 qp 且补 charset=utf-8；HTML 在 `</body>` 前插入灰线小字 |
| multipart/mixed 含 base64 PDF 附件（中文 base64 HTML + qp 纯文本） | 附件字节级不变，两个文本部分注入 | ✅ 附件解码后与原文件完全一致 |
| multipart/signed（PGP） | 正文完全不注入，仅注退订头 | ✅ 输出与输入逐字节一致（签名区完整） |
| 真实二进制冒烟（golden 样本原文过网关） | 上游收到的副本可解析、页脚与退订头齐全 | ✅ 14/14，含 PATCH body_injection=false 后停止注入 |
| DKIM-Signature 存在 | 跳过正文注入 + WARN 日志，退订头照常 | ✅ |
| 残缺 multipart（缺闭合边界） | 正文原样转发，不报错给程序 | ✅ 250，原文 + 退订头 |
| CRLF 完整性 | 输出无裸 LF | ✅ 单测断言 + golden 对比 |

自动化覆盖：unsub 91%（注入/跳过/转义/确定性/golden）、smtpd 89%（HTML 与纯文本注入、DKIM 跳过、开关关闭、解析失败回退）、store 78%（body_injection 列读写）、api 83%（body_injection 创建与 PATCH）。

## M5 验收自测结果

验收标准（计划书）：不用 curl 也能完成日常管理。

真实浏览器（Chrome + 无障碍树驱动）实测，全程页面点击、零 curl：

| 场景 | 预期 | 实际 |
| ---- | ---- | ---- |
| 打开 :8081/admin | 页面渲染，输入框 + 五个视图 + 健康检查 | ✅ |
| 粘贴 token 保存连接 | 显示「已连接」，统计视图自动加载 | ✅ |
| 页面表单创建程序 | 一次性 SMTP 密码只显示一次，表格新增行 | ✅ kanboard + 一次性密码 |
| 页面「重置密码」 | 新密码只显示一次 | ✅ |
| 页面添加退订 | 列表出现（程序 / 邮箱 / 来源 api / 时间） | ✅ |
| 审计视图 | 看到 unsubscribe.create 记录（token 名 + IP） | ✅ |
| 健康检查按钮 | 显示「健康状态：ok」 | ✅ |
| 「恢复订阅」按钮 | 行删除，列表变「暂无数据」 | ✅ |

自动化覆盖：/admin 与静态资源 200、Content-Type、安全响应头、未知路径 404。

## 冒烟测试（真实二进制）

每次里程碑用真实二进制 + 本地假上游 + 真实 HTTP/SMTP 客户端跑端到端冒烟：M1 8 项、M2 13 项、M3 36 项、M4 14 项全部通过（脚本不入库，覆盖上述验收场景的真实进程行为，包括三端口同时监听与 0600 密钥文件生成）。
