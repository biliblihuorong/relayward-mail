# 部署 Relayward

[English](DEPLOYMENT.md) | **简体中文** · 返回 [README](../README.zh-CN.md)

目标：在已经装有 nginx（或 OpenResty / 1Panel）并有证书的服务器上，用 Docker 跑 Relayward，做到：

- 收件人通过 **HTTPS** 打开退订链接；
- 你的应用通过 **TLS** 发信；

而 Relayward 本身不需要任何证书。

> 本地已实测：镜像构建、`docker compose` 启动（`./data` 绑定挂载）、PROXY protocol 监听。nginx 与证书相关步骤尚未在真实服务器上完整跑通，建议先在临时机器上试一遍。

## 1. 整体结构

```
 应用   ──TLS :465────▶ ┐
 收件人 ──HTTPS :443───▶ ├ nginx / OpenResty（证书放在这里）
                        ┘        │ 在 127.0.0.1 上走明文 HTTP / SMTP
                                 ▼
                 Docker 里的 Relayward（:8080 公开页、:587 SMTP、:8081 管理）
                                 │ STARTTLS :587
                                 ▼
                           你的邮件提供商

 你 ── SSH 隧道 ──▶ 127.0.0.1:8081（管理端，绝不公开）
```

| 宿主机端口 | 作用 | 谁能访问 |
| ---------- | ---- | -------- |
| 443 | 退订页（HTTPS）→ Relayward `8080` | 所有人（证书在 nginx） |
| 465 | SMTP over TLS → Relayward `587` | 你的应用（nginx `stream`，证书在 nginx） |
| 8081 | 管理 API 和 `/admin` 页面 | **只有你**，经 SSH 隧道 / VPN。不要挂到公网代理 |

## 2. Relayward 还需要配置证书吗？

**用 nginx 终结 TLS 的话（本文方案）不需要。** Relayward 只在 `127.0.0.1` 上明文监听，公网侧的加密全部由 nginx 完成。同一张 `mail.example.com` 的证书同时用于 443 和 465。

| TLS 由谁处理 | Relayward 要证书吗 | 应用怎么连 |
| ------------ | ------------------ | ---------- |
| nginx（**本文方案**） | 不需要 | 465 端口，选 "SSL/TLS" |
| Relayward 自己 | 需要：设置 `smtp.tls_cert`、`smtp.tls_key` 并把文件挂进容器 | 587 端口，选 "STARTTLS" |
| 都不处理（明文） | 不需要 | 只适合纯内网 |

nginx 无法代理 STARTTLS，所以前面放了 nginx 时，应用必须用**隐式 TLS（465）**。退订链接的 `base_url` 必须是 `https://…`，同样需要 nginx 上的证书。

## 3. 前置条件

- 一台装好 Docker 和 Compose 插件的 Linux 服务器，nginx/OpenResty 上有你域名（如 `mail.example.com`，DNS `A` 记录指向服务器）的有效证书。
- 邮件提供商（SES、SendGrid、Mailgun、Postmark 等）的 SMTP 账号：主机、587 端口、用户名、密码/API key。发信域名的 SPF / DKIM / DMARC 要在提供商处配好，Relayward 不做 DKIM 签名。
- 服务器能访问提供商的 587 端口。

## 4. 启动 Relayward

下面的文件在仓库 [`deploy/`](../deploy) 目录里也有现成的。

```bash
git clone https://github.com/biliblihuorong/relayward-mail.git
cd relayward-mail/deploy

cp .env.example .env              && chmod 600 .env
cp config.docker.yaml config.yaml
mkdir -p data && sudo chown -R 65532:65532 data    # 容器以 uid 65532 运行
```

**`.env`**：只放密钥。

```bash
UPSTREAM_KEY=你的提供商SMTP密码或API-key
UNSUB_SECRET=        # 留空：首次启动自动生成并存到 ./data
```

**`config.yaml`**：改标注的几项（`upstream.*`、`public.base_url`）。

```yaml
data_dir: /data

smtp:
  listen: ":587"
  max_message_size: 10MB
  proxy_protocol_trusted: ["172.16.0.0/12"]   # 让 nginx 传递真实客户端 IP（见第 5 节）

upstream:
  host: smtp.your-provider.com
  port: 587
  username: apikey
  password: ${UPSTREAM_KEY}
  tls: starttls

public:
  listen: ":8080"
  base_url: https://mail.example.com           # 你的域名，必须是 https

admin:
  listen: ":8081"
  ip_allowlist: []

unsubscribe:
  secret: ${UNSUB_SECRET}
  footer_text: "不想再收到此类邮件？点此退订"

log_retention_days: 90
```

**`docker-compose.yml`**：数据都放在旁边的普通文件夹里（不使用 Docker 卷）：

```yaml
services:
  relayward:
    image: relayward:latest
    build:
      context: ..
    container_name: relayward
    restart: unless-stopped
    env_file: .env
    ports:
      - "127.0.0.1:8080:8080"   # 退订页   -> nginx 443
      - "127.0.0.1:2525:587"    # SMTP 明文 -> nginx stream 465
      - "127.0.0.1:8081:8081"   # 管理     -> 仅 SSH 隧道
    volumes:
      - ./config.yaml:/etc/relayward/config.yaml:ro
      - ./data:/data
```

```bash
docker compose up -d --build
docker compose ps                 # 等待变为 healthy
docker compose logs relayward     # 找到：generated initial admin token
```

用 `ss -tlnp | grep -E '8080|2525|8081'` 检查：三个端口都必须显示 `127.0.0.1`。

### `admin.ip_allowlist` 留空会怎样？

`admin.ip_allowlist` 是管理 **API** 的额外 IP 过滤。**留空完全可以**，意思只是"不做 IP 过滤"。管理端口依然有两层保护：(1) 只绑定在 `127.0.0.1`，服务器之外的人根本连不上；(2) 需要管理员 token，token 错误多次会被锁定。只有当你通过 VPN 访问管理端口时才需要填，例如 `["10.8.0.0/24"]`。

## 5. nginx / OpenResty 配置

### 5.1 退订页的 HTTPS

给 `mail.example.com` 加一个普通站点（1Panel：*网站 → 创建 → 反向代理*，然后启用 HTTPS 并选你的证书）：

```nginx
server {
    listen 443 ssl;
    server_name mail.example.com;
    ssl_certificate     /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        client_max_body_size 64k;
    }
}
```

只代理 `8080`，不要代理 `8081`。

### 5.2 SMTP over TLS（465 端口）

在 `nginx.conf` 顶层、与 `http {}` 同级（不是写在里面）加一个 `stream` 块。需要 `stream` 和 `stream_ssl` 模块（`nginx -V` / `openresty -V` 里应有 `--with-stream` 和 `--with-stream_ssl_module`）。

```nginx
stream {
    server {
        listen 465 ssl;
        ssl_certificate     /path/to/fullchain.pem;     # 与 5.1 同一张证书
        ssl_certificate_key /path/to/privkey.pem;
        ssl_protocols       TLSv1.2 TLSv1.3;

        proxy_pass          127.0.0.1:2525;
        proxy_protocol      on;    # 告诉 Relayward 真实客户端 IP
        proxy_timeout       5m;
    }
}
```

nginx 的 `proxy_protocol on` 和 `config.yaml` 里的 `smtp.proxy_protocol_trusted` 必须**同时配置**。两者缺一都会出问题：不开 `proxy_protocol on`，Relayward 只能看到 nginx 的地址（`allowed_from`、限流、登录失败锁定就无法按客户端区分）；开了 `proxy_protocol on` 却没配 `proxy_protocol_trusted`，所有连接都会失败。Relayward 只信任你列出的网段发来的头，其他来源发来的一律不认，外部无法伪造 IP。

### 5.3 1Panel 里 OpenResty 跑在容器中

OpenResty 容器里的 `127.0.0.1` 不是宿主机。改成加入同一个 Docker 网络：在 `docker-compose.yml` 里删掉 `ports` 段，加上

```yaml
    networks: [1panel-network]
networks:
  1panel-network:
    external: true
```

然后 5.1 用 `proxy_pass http://relayward:8080;`，5.2 用 `proxy_pass relayward:587;`。OpenResty 容器要发布 465 端口，防火墙 / 云安全组放行 465 和 443。用 `docker network inspect 1panel-network` 查网段，如果不在 `172.16.0.0/12` 内，把它写进 `proxy_protocol_trusted`。

## 6. 创建第一个应用

```bash
docker compose logs relayward | grep "generated initial admin token"   # rw_admin_...
ssh -N -L 8081:127.0.0.1:8081 user@mail.example.com                     # 在你自己的电脑上
# 浏览器打开 http://127.0.0.1:8081/admin，粘贴 token，创建应用
```

应用的 SMTP 密码**只显示一次**。也可以用命令行：

```bash
docker compose exec relayward /relayward admin create-app \
  -config /etc/relayward/config.yaml gitea -from noreply@example.com
```

应用里填：服务器 `mail.example.com`，端口 **465**，加密 **SSL/TLS**，用户名 = 应用名，密码 = 那个一次性密码。

## 7. 验证

```bash
curl -i https://mail.example.com/healthz                                    # 200 {"status":"ok"}
openssl s_client -connect mail.example.com:465 -crlf </dev/null | head -20   # 你的证书 + "220"
swaks --server mail.example.com --port 465 --tls-on-connect \
      --auth LOGIN --auth-user gitea --auth-password '一次性密码' \
      --from noreply@example.com --to you@yourdomain.com
```

收到的邮件应带退订页脚，以及指向 `https://mail.example.com/u/...` 的 `List-Unsubscribe` 头。

## 8. 日常运维

| 事项 | 做法 |
| ---- | ---- |
| 日志 | `docker compose logs -f relayward` |
| 监控 | `https://mail.example.com/healthz`（503 = 数据库或上游有问题） |
| 升级 | `git pull && docker compose up -d --build`（迁移在启动时自动执行） |
| 更换提供商 key | 改 `.env`，`docker compose up -d` |
| 丢了所有管理员 token | `docker compose exec relayward /relayward admin reset -config /etc/relayward/config.yaml` |
| 证书续期 | Relayward 不用管（证书在 nginx），照常 reload nginx 即可 |

**备份** = `data/` 文件夹（SQLite 数据库 + `unsubscribe_secret`）。丢了 `unsubscribe_secret`，所有已发出的退订链接都会失效。做一致性备份：

```bash
docker compose stop relayward && tar czf relayward-data-$(date +%F).tgz data && docker compose start relayward
```

备份请放到服务器之外，里面有密钥。

## 9. 故障排查

| 现象 | 可能原因 |
| ---- | -------- |
| 容器启动即退出 | 配置错误，日志会指出配置项（如 `public.base_url must use https`、`upstream.password is required`） |
| `/data` 报 `permission denied` | 执行 `sudo chown -R 65532:65532 data` |
| SMTP 连上立刻断开（`421`） | nginx 开了 `proxy_protocol on`，但 `proxy_protocol_trusted` 没配或没覆盖 nginx 的地址 |
| 应用报乱码 / TLS 错误 | 应用在 465 上用了 STARTTLS（要选 SSL/TLS）；或配了 `proxy_protocol_trusted` 却没开 `proxy_protocol on` |
| `535 authentication failed` | 应用名/密码错误。10 分钟内失败 10 次，该 IP 被封禁 1 小时 |
| `550 5.7.1 sender address not allowed` | `From` 不在该应用的 `allowed_from` 里 |
| `451` | 触发限流或提供商暂时不可用，稍后重试 |
| 退订链接 404 | 令牌无效，或发信后 `unsubscribe.secret` 被更换 |
| `/healthz` 返回 503 | 数据库错误或提供商不可达：检查凭据和出站 587 |
| 管理页打不开 | SSH 隧道断了；`8081` 本来就不对公网开放 |

## 加固清单

- [ ] `8080`、`2525`、`8081` 只监听 `127.0.0.1`（`ss -tlnp`）。
- [ ] 防火墙只开 22、80/443、465（应用服务器 IP 固定的话，465 限定来源）。
- [ ] `.env` 权限 `0600`，提供商 key 不存在于别处。
- [ ] 初始管理员 token 存入密码管理器，日常用 operator/viewer token。
- [ ] 每个应用都有严格的 `allowed_from` 和合理的 `rate_per_hour`。
- [ ] `data/` 有异地备份。
- [ ] 发信域名已发布 SPF、DKIM、DMARC。
