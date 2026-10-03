# 把 Relayward 部署到公网

[English](DEPLOYMENT.md) | **简体中文** · 返回 [README](../README.zh-CN.md)

本文带你从一台空的 Linux VPS 开始，搭出一个应用可经公网访问的网关：退订页面走 HTTPS，SMTP 端口启用 TLS。

> 下面的命令依据本仓库代码编写（端口、路径、配置项、容器布局）。Docker 镜像构建、容器启动、`/data` 卷权限与 587 端口绑定已在本地实测；nginx、certbot、防火墙等步骤尚未在真实 VPS 上完整跑通，建议先在一台临时服务器上演练。

## 0. 最终架构

```
 应用（任意网络）──STARTTLS :587──▶ ┌─────────────────────┐ ──STARTTLS──▶ 邮件提供商
                                     │ relayward (Docker)  │
 收件人 ──HTTPS :443──▶ nginx ────▶ │  :8080 公开         │
                                     │  :8081 管理 ◀── SSH 隧道 / VPN（绝不公开）
                                     └─────────────────────┘
```

| 端口 | 用途 | 暴露方式 |
| ---- | ---- | -------- |
| 587 | 应用发信的 SMTP 入口 | 公网**必须启用 TLS**；最好限定为应用服务器 IP |
| 465（可选） | 隐式 TLS 的 SMTP，由 nginx/OpenResty `stream` 终结后转发到 Relayward 的 587 | 见[在 nginx / OpenResty 上终结 SMTP 的 TLS](#在-nginx--openresty-上终结-smtp-的-tls可选) |
| 8080 | 退订页面、`/healthz` | 只绑 `127.0.0.1`，经 nginx 的 443 对外 |
| 8081 | 管理 API、`/admin` 页面 | 只绑 `127.0.0.1`，通过 SSH 隧道或 VPN 访问 |

为什么 `8081` 必须保持私有：Relayward **不信任** `X-Forwarded-For`。放在反向代理后，管理端的 IP 白名单和失败锁定只会看到代理的地址。不要把管理端口挂到公网代理上。

## 1. 前置条件

- 一台带公网 IPv4 的 Linux 服务器（1 核 / 512 MB 内存足够），有 root 或 sudo 权限。
- 一个你能控制的域名，例如 `mail.example.com`。
- 一个 SMTP 提供商账号（SES、SendGrid、Mailgun、Postmark 或自建中继等），拿到：主机、587 端口、用户名、密码/API key。发信域名的 **SPF / DKIM / DMARC** 要在提供商处配置好，Relayward 自己不做 DKIM 签名。
- Docker Engine 及 Compose 插件（或用[方案 B](#方案-bsystemd不用-docker)）。
- 服务器能出站访问提供商的 587 端口。

## 2. DNS

为网关主机名创建 `A` 记录（有 IPv6 再加 `AAAA`）指向服务器：

| 类型 | 名称 | 值 |
| ---- | ---- | -- |
| A | `mail.example.com` | 服务器 IP |

配置里的 `public.base_url` 用这个名字，每封邮件里的退订链接都指向它，请慎重选择。已发出的邮件保留旧链接，所以将来若要改名，旧名字也要继续提供 `/u/...`。

## 3. 防火墙

以 `ufw` 为例：

```bash
sudo ufw default deny incoming
sudo ufw allow 22/tcp                 # SSH
sudo ufw allow 80/tcp                 # ACME 验证 + HTTP 跳转 HTTPS
sudo ufw allow 443/tcp                # 退订页面
# SMTP 入口：应用服务器 IP 固定时只放行它们（推荐）
sudo ufw allow from 203.0.113.10 to any port 587 proto tcp
# 或对所有人开放（仅在第 6 步配置了 TLS 之后）：
# sudo ufw allow 587/tcp
sudo ufw enable
```

> Docker 发布端口时会直接改 iptables，**绕过 ufw**。所以下面的 compose 文件把 8080/8081 明确绑到 `127.0.0.1`。启动后用 `ss -tlnp` 检查。

云厂商如果另有安全组/防火墙面板，请同步配置同样的规则。

## 4. 获取代码并构建镜像

```bash
sudo mkdir -p /opt/relayward && cd /opt/relayward
sudo git clone https://github.com/biliblihuorong/relayward-mail.git src
sudo docker build -t relayward:latest ./src
```

镜像是 `distroless/static:nonroot`（uid/gid 65532）上的静态二进制，`/data` 已预先创建并归该用户所有。

## 5. 配置

`/opt/relayward/config.yaml`：

```yaml
data_dir: /data

smtp:
  listen: ":587"
  tls_cert: /certs/fullchain.pem
  tls_key: /certs/privkey.pem
  max_message_size: 10MB

upstream:
  host: smtp.your-provider.com
  port: 587
  username: apikey
  password: ${UPSTREAM_KEY}
  tls: starttls

public:
  listen: ":8080"
  base_url: https://mail.example.com      # 必须是 https

admin:
  listen: ":8081"
  ip_allowlist: []                         # 可选：经 VPN 访问时填 ["10.0.0.0/8"] 之类

unsubscribe:
  secret: ${UNSUB_SECRET}                  # 留空则自动生成并存入 /data/unsubscribe_secret
  footer_text: "不想再收到此类邮件？点此退订"

log_retention_days: 90
```

`/opt/relayward/.env`（权限 `0600`，切勿提交到仓库）：

```bash
UPSTREAM_KEY=你的提供商SMTP密码或API-key
# UNSUB_SECRET=   # 可选；留空 = 自动生成并保存在数据卷
```

```bash
sudo chmod 600 /opt/relayward/.env
```

> `smtp.tls_cert` 与 `smtp.tls_key` 必须同时设置。设置后客户端必须先 `STARTTLS` 再 `AUTH`。不设置的话密码在网络上是明文传输，只适合纯内网。

## 6. 证书与 HTTPS 入口（nginx + certbot）

一张 `mail.example.com` 的证书同时用于 HTTPS 退订页面（nginx）和 587 端口的 STARTTLS（Relayward）。

```bash
sudo apt install -y nginx certbot
sudo mkdir -p /var/www/certbot /opt/relayward/certs
```

先建一个只有 HTTP 的站点让 certbot 验证。`/etc/nginx/sites-available/relayward`：

```nginx
server {
    listen 80;
    server_name mail.example.com;
    location /.well-known/acme-challenge/ { root /var/www/certbot; }
    location / { return 301 https://$host$request_uri; }
}
```

```bash
sudo ln -sf /etc/nginx/sites-available/relayward /etc/nginx/sites-enabled/relayward
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t && sudo systemctl reload nginx
sudo certbot certonly --webroot -w /var/www/certbot -d mail.example.com
```

然后在同一文件末尾追加 HTTPS 的 server 块并重载：

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name mail.example.com;

    ssl_certificate     /etc/letsencrypt/live/mail.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/mail.example.com/privkey.pem;

    # 只代理 public 监听；管理端口不在这里暴露。
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        client_max_body_size 64k;
    }
}
```

```bash
sudo nginx -t && sudo systemctl reload nginx
```

Relayward **只在启动时**读取证书，所以要装一个续期钩子：证书续期后复制新文件并重启容器。`/etc/letsencrypt/renewal-hooks/deploy/relayward.sh`：

```bash
#!/bin/sh
set -e
src=/etc/letsencrypt/live/mail.example.com
dst=/opt/relayward/certs
cp "$src/fullchain.pem" "$dst/fullchain.pem"
cp "$src/privkey.pem"   "$dst/privkey.pem"
chown 65532:65532 "$dst/fullchain.pem" "$dst/privkey.pem"
chmod 600 "$dst/privkey.pem"
cd /opt/relayward && docker compose restart relayward
```

```bash
sudo chmod +x /etc/letsencrypt/renewal-hooks/deploy/relayward.sh
sudo /etc/letsencrypt/renewal-hooks/deploy/relayward.sh 2>/dev/null || true   # 首次复制证书；compose.yaml 还没建时重启失败无妨
sudo certbot renew --dry-run
```

## 7. 启动（Docker Compose）

`/opt/relayward/compose.yaml`：

```yaml
services:
  relayward:
    image: relayward:latest
    restart: unless-stopped
    env_file: .env
    ports:
      - "587:587"                 # SMTP 入口（上面已配 TLS）
      - "127.0.0.1:8080:8080"     # 公开页面 -> 仅 nginx
      - "127.0.0.1:8081:8081"     # 管理 -> 仅 SSH 隧道 / VPN
    volumes:
      - ./config.yaml:/etc/relayward/config.yaml:ro
      - ./certs:/certs:ro
      - relayward-data:/data
volumes:
  relayward-data:
```

```bash
cd /opt/relayward
sudo docker compose up -d
sudo docker compose ps               # STATUS 应变为 healthy
sudo docker compose logs relayward   # 应看到：starting relayward ... / generated initial admin token
ss -tlnp | grep -E ':(587|8080|8081)\b'   # 8080 和 8081 必须显示 127.0.0.1
```

> Docker 会给容器设置 `net.ipv4.ip_unprivileged_port_start=0`，所以非 root 进程能在容器内绑定 587。Podman 需加 `--sysctl net.ipv4.ip_unprivileged_port_start=0`，或发布高位端口。

### 取得初始管理员 token

首次启动时只打印一次，同时存放在数据卷里：

```bash
sudo docker compose logs relayward | grep "generated initial admin token"
```

把 `rw_admin_...` 存进密码管理器。之后创建日常使用的 token，并吊销初始 token：

```bash
TOKEN=rw_admin_xxxxxxxx
# 在你自己的电脑上，下一节的隧道开着时：
curl -s -X POST http://127.0.0.1:8081/api/tokens \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"ops-alice","role":"operator"}'
```

（完整 token 接口见 README。）

### 访问管理页

```bash
ssh -N -L 8081:127.0.0.1:8081 user@mail.example.com
# 然后在浏览器打开 http://127.0.0.1:8081/admin，粘贴 token
```

想用 VPN（WireGuard / Tailscale）？把 `8081` 绑到 VPN 网卡地址而不是 `127.0.0.1`，并把 `admin.ip_allowlist` 设为 VPN 网段。

### 创建程序

在管理页（或 `POST /api/apps`）创建程序，一次性 SMTP 密码**只显示一次**。命令行方式：

```bash
sudo docker compose exec relayward /relayward admin create-app \
  -config /etc/relayward/config.yaml gitea -from noreply@example.com
```

## 在 nginx / OpenResty 上终结 SMTP 的 TLS（可选）

如果证书在 nginx（或 OpenResty / 1Panel）上，可以用 `stream` 模块让它为 SMTP 终结 TLS。此时客户端使用 **465 端口的隐式 TLS**（选 "SSL/TLS"，不是 STARTTLS；nginx 无法代理 STARTTLS）。

`smtp.tls_cert` / `smtp.tls_key` 留空，Relayward 不要暴露在公网（与 nginx 同一 Docker 网络，或只绑 `127.0.0.1`），并开启 PROXY protocol，这样 Relayward 仍能看到真实客户端 IP（`allowed_from`、限流、登录失败锁定都依赖它）：

```nginx
# nginx.conf 顶层，与 http {} 同级
stream {
    server {
        listen 465 ssl;
        ssl_certificate     /path/to/fullchain.pem;
        ssl_certificate_key /path/to/privkey.pem;
        ssl_protocols       TLSv1.2 TLSv1.3;
        proxy_pass          relayward:587;   # 共享 Docker 网络上的容器名
        proxy_protocol      on;              # 传递真实客户端 IP
        proxy_timeout       5m;
    }
}
```

```yaml
smtp:
  listen: ":587"
  proxy_protocol_trusted: ["172.18.0.0/16"]   # nginx 容器所在网段 / IP
```

只有 `proxy_protocol_trusted` 里的来源才会被要求发送该头（缺失则断开），其他来源按普通连接处理，所以外部客户端无法伪造地址。**不要**只开 `proxy_protocol on` 而不配 `proxy_protocol_trusted`：Relayward 会把头当成 SMTP 命令，所有连接都会失败。nginx 容器的地址用 `docker network inspect <网络名>` 查看。

验证：`openssl s_client -connect mail.example.com:465 -crlf` 应显示你的证书和 `220` 欢迎语。

## 方案 B：systemd，不用 Docker

```bash
# 在任意装了 Go 的机器上构建，再拷到服务器
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o relayward ./cmd/relayward
sudo install -m 0755 relayward /usr/local/bin/relayward

sudo useradd --system --home /var/lib/relayward --shell /usr/sbin/nologin relayward
sudo install -d -o relayward -g relayward -m 0700 /var/lib/relayward
sudo install -d -m 0755 /etc/relayward
# config.yaml：同上，但 data_dir: /var/lib/relayward，证书路径自定
# /etc/relayward/relayward.env（0600，root:root）：UPSTREAM_KEY=...
```

`/etc/systemd/system/relayward.service`：

```ini
[Unit]
Description=Relayward mail gateway
After=network-online.target
Wants=network-online.target

[Service]
User=relayward
Group=relayward
EnvironmentFile=/etc/relayward/relayward.env
ExecStart=/usr/local/bin/relayward serve -config /etc/relayward/config.yaml
Restart=on-failure
RestartSec=3
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/relayward
PrivateTmp=true
ProtectHome=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now relayward
journalctl -u relayward -n 50 --no-pager
```

运行服务的用户必须能读到证书文件；用 certbot 部署钩子把证书复制到 `/etc/relayward/certs`（属主 `relayward`，私钥 `0600`）并执行 `systemctl restart relayward` 即可。

## 8. 验证

```bash
# 1. 经 HTTPS 访问公开监听
curl -i https://mail.example.com/healthz              # 200 {"status":"ok"}

# 2. 587 端口 TLS
openssl s_client -starttls smtp -connect mail.example.com:587 -crlf </dev/null | head -20

# 3. 端到端发信（swaks），使用该程序的一次性密码
swaks --server mail.example.com --port 587 --tls \
      --auth LOGIN --auth-user gitea --auth-password '一次性密码' \
      --from noreply@example.com --to you@yourdomain.com

# 4. 日志里能看到（经隧道）
curl -s "http://127.0.0.1:8081/api/messages?app=gitea&limit=5" -H "Authorization: Bearer $TOKEN"
```

打开收到的邮件：应带退订页脚，以及指向 `https://mail.example.com/u/...` 的 `List-Unsubscribe` 头；打开该链接必须出现确认页。

然后把真实应用指向 `mail.example.com:587`（STARTTLS，用户名 = 程序名，密码 = 一次性密码）。所有程序切换完成后，**在提供商后台轮换 key**，只更新 `/opt/relayward/.env` 里的 `UPSTREAM_KEY`，再 `docker compose up -d`。

## 9. 日常运维

| 事项 | 做法 |
| ---- | ---- |
| 日志 | `docker compose logs -f relayward`（stdout 为 JSON 行） |
| 监控 | 用 Uptime Kuma 等监控 `https://mail.example.com/healthz`（503 = `degraded`：数据库或上游有问题） |
| 升级 | `cd /opt/relayward/src && git pull && docker build -t relayward:latest . && cd .. && docker compose up -d`；迁移在启动时自动执行 |
| 轮换上游 key | 改 `.env`，`docker compose up -d` |
| 重置某程序密码 | `POST /api/apps/{name}/rotate` 或页面按钮，旧密码立即失效 |
| 丢了所有管理员 token | `docker compose exec relayward /relayward admin reset -config /etc/relayward/config.yaml` |
| 日志保留 | `log_retention_days`（默认 90），启动时和每 24 小时清理一次 |

### 备份

备份**整个数据卷**：里面有 SQLite 数据库（程序、密码哈希、日志、退订、token）和 `unsubscribe_secret`。**丢了 `unsubscribe_secret`，所有已发出的退订链接都会失效。**

```bash
# 一致性备份：短暂停机、打包、再启动
cd /opt/relayward
sudo docker compose stop relayward
sudo docker run --rm -v relayward_relayward-data:/data -v "$PWD":/backup busybox \
     tar czf /backup/relayward-data-$(date +%F).tgz -C /data .
sudo docker compose start relayward
```

（卷名是 `<compose 项目名>_relayward-data`，用 `docker volume ls` 确认。）备份请放到服务器之外并限制访问：压缩包里有密钥。

## 10. 加固清单

- [ ] `8080`、`8081` 只监听 `127.0.0.1`（`ss -tlnp`）。
- [ ] 587 限定为已知应用 IP，或已启用 TLS 并用 `openssl s_client` 验证。
- [ ] `public.base_url` 是真实的 HTTPS 域名，`https://.../healthz` 可访问。
- [ ] `.env` 权限 `0600`；提供商 key 不再存在于别处（各程序已不持有）。
- [ ] 初始管理员 token 存入密码管理器，日常操作用 operator/viewer token。
- [ ] 每个程序都有严格的 `allowed_from` 和合理的 `rate_per_hour`。
- [ ] 已测试证书续期（`certbot renew --dry-run`），部署钩子会重启 Relayward。
- [ ] 数据卷（含 `unsubscribe_secret`）有异地备份。
- [ ] 提供商处已为发信域名发布 SPF、DKIM、DMARC。
- [ ] 系统自动更新（`unattended-upgrades`），SSH 仅密钥登录。

## 11. 故障排查

| 现象 | 可能原因 |
| ---- | -------- |
| 程序收到 `535 authentication failed` | 程序名/密码错误、程序已停用或密码已被重置。10 分钟内失败 10 次，来源 IP 封禁 1 小时 |
| `530` / 没有提供 AUTH | 已配置 TLS，客户端必须先 STARTTLS 再 AUTH |
| `550 5.7.1 sender address not allowed` | `From` 地址不在该程序的 `allowed_from` 列表 |
| `451` | 触发限流（日志里是 `rate_limited`）、上游暂时不可用或网关临时错误：稍后重试 |
| `554` | 提供商永久拒信；日志里有上游的回复 |
| `/healthz` 返回 503 | 数据库错误或上游不可达；看 `docker compose logs`，检查提供商凭据和出站 587 |
| 退订链接 404 | 令牌无效，或发信之后 `unsubscribe.secret` 被更换 |
| 容器启动即退出 | 配置错误：报错信息会指出配置项（如 `public.base_url must use https`、`upstream.password is required`） |
| `/data` 报 `permission denied` | 用了宿主机目录挂载？执行 `chown 65532:65532`。命名卷无需处理 |
| 管理页打不开 | 隧道断了，或用了公网域名；`8081` 本来就不对公网开放 |
