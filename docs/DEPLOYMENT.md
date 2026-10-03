# Deploying Relayward

**English** | [简体中文](DEPLOYMENT.zh-CN.md) · Back to [README](../README.md)

Goal: run Relayward in Docker on a server that already has nginx (or OpenResty / 1Panel) with a certificate, so that

- recipients open unsubscribe links over **HTTPS**, and
- your apps send mail over **TLS**,

while Relayward itself never needs a certificate.

> Tested locally: image build, `docker compose` start-up with `./data` bind-mounted, and the PROXY-protocol listener. The nginx/certificate steps have not been run on a real server yet, so try them on a throw-away machine first.

## 1. How it fits together

```
 apps       ──TLS :465────▶ ┐
 recipients ──HTTPS :443───▶ ├ nginx / OpenResty (holds the certificate)
                            ┘        │ plain HTTP / SMTP on 127.0.0.1
                                     ▼
                     Relayward in Docker (:8080 public, :587 SMTP, :8081 admin)
                                     │ STARTTLS :587
                                     ▼
                               your mail provider

 you ── SSH tunnel ──▶ 127.0.0.1:8081 (admin, never public)
```

| Port (host) | What | Who may reach it |
| ----------- | ---- | ---------------- |
| 443 | Unsubscribe pages (HTTPS) → Relayward `8080` | Everyone (nginx, certificate on nginx) |
| 465 | SMTP over TLS → Relayward `587` | Your apps (nginx `stream`, certificate on nginx) |
| 8081 | Admin API and `/admin` page | **Only you**, via SSH tunnel / VPN. Never put it behind a public proxy |

## 2. Do I need a certificate for Relayward?

**No, if nginx terminates TLS** (this guide). Relayward only listens in plain text on `127.0.0.1`; nginx holds the certificate and encrypts everything on the public side. One certificate for `mail.example.com` covers both 443 and 465.

| Where TLS is handled | Certificate needed in Relayward? | Apps connect with |
| -------------------- | -------------------------------- | ----------------- |
| nginx (**this guide**) | No | port 465, "SSL/TLS" |
| Relayward itself | Yes: set `smtp.tls_cert` and `smtp.tls_key`, mount the files | port 587, "STARTTLS" |
| Nobody (plain) | No | Only acceptable on a private network |

nginx cannot proxy STARTTLS, so with nginx in front, apps must use **implicit TLS (465)**. The `base_url` for unsubscribe links must be `https://…`, which also needs the certificate on nginx.

## 3. Prerequisites

- A Linux server with Docker + the Compose plugin, and nginx/OpenResty with a valid certificate for your domain (e.g. `mail.example.com`, DNS `A` record pointing to the server).
- An SMTP account at a provider (SES, SendGrid, Mailgun, Postmark…): host, port 587, username, password/API key. Set up SPF / DKIM / DMARC for your sending domain there; Relayward does not sign mail.
- The server can reach the provider on port 587.

## 4. Start Relayward

The files below are also in [`deploy/`](../deploy).

```bash
git clone https://github.com/biliblihuorong/relayward-mail.git
cd relayward-mail/deploy

cp .env.example .env              && chmod 600 .env
cp config.docker.yaml config.yaml
mkdir -p data && sudo chown -R 65532:65532 data    # the container runs as uid 65532
```

**`.env`**: secrets only.

```bash
UPSTREAM_KEY=your-provider-smtp-password-or-api-key
UNSUB_SECRET=        # leave empty: generated once and stored in ./data
```

**`config.yaml`**: edit the three marked values (`upstream.*`, `public.base_url`).

```yaml
data_dir: /data

smtp:
  listen: ":587"
  max_message_size: 10MB
  proxy_protocol_trusted: ["172.16.0.0/12"]   # lets nginx pass the real client IP (see §5)

upstream:
  host: smtp.your-provider.com
  port: 587
  username: apikey
  password: ${UPSTREAM_KEY}
  tls: starttls

public:
  listen: ":8080"
  base_url: https://mail.example.com           # your domain, must be https

admin:
  listen: ":8081"
  ip_allowlist: []

unsubscribe:
  secret: ${UNSUB_SECRET}
  footer_text: "Don't want these emails? Unsubscribe"

log_retention_days: 90
```

**`docker-compose.yml`**: all data sits in plain folders next to it (no Docker volumes):

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
      - "127.0.0.1:8080:8080"   # unsubscribe pages -> nginx 443
      - "127.0.0.1:2525:587"    # SMTP (plain)      -> nginx stream 465
      - "127.0.0.1:8081:8081"   # admin             -> SSH tunnel only
    volumes:
      - ./config.yaml:/etc/relayward/config.yaml:ro
      - ./data:/data
```

```bash
docker compose up -d --build
docker compose ps                 # wait for "healthy"
docker compose logs relayward     # find: generated initial admin token
```

Check `ss -tlnp | grep -E '8080|2525|8081'`: all three must show `127.0.0.1`.

### What do the admin settings do? (`ip_allowlist`)

`admin.ip_allowlist` is an extra filter on the admin **API**. **Leaving it empty is fine**: it simply means "no IP filter". The admin port is still protected by (1) being bound to `127.0.0.1` only, so nobody outside the server can open it, and (2) the admin token plus a lockout after repeated wrong tokens. Fill it in only if you reach the admin port over a VPN, e.g. `["10.8.0.0/24"]`.

## 5. nginx / OpenResty

### 5.1 HTTPS for the unsubscribe pages

Add a normal site for `mail.example.com` (in 1Panel: *Websites → Create → Reverse proxy*, then enable HTTPS with your certificate):

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

Only proxy `8080`. Do not proxy `8081`.

### 5.2 SMTP over TLS (port 465)

Add a `stream` block at the top level of `nginx.conf`, next to `http {}` (not inside it). It needs the `stream` and `stream_ssl` modules (`nginx -V` / `openresty -V` should show `--with-stream` and `--with-stream_ssl_module`).

```nginx
stream {
    server {
        listen 465 ssl;
        ssl_certificate     /path/to/fullchain.pem;     # same certificate as 5.1
        ssl_certificate_key /path/to/privkey.pem;
        ssl_protocols       TLSv1.2 TLSv1.3;

        proxy_pass          127.0.0.1:2525;
        proxy_protocol      on;    # tell Relayward the real client IP
        proxy_timeout       5m;
    }
}
```

`proxy_protocol on` and `smtp.proxy_protocol_trusted` in `config.yaml` must be set **together**. Without the first, Relayward only sees nginx's address (so `allowed_from`, rate limits and the failed-login lockout stop working per client); with the first but not the second, every connection fails. Relayward only trusts the header from the networks you list and never from anyone else, so outsiders cannot fake their IP.

### 5.3 Using 1Panel with OpenResty in a container

`127.0.0.1` inside the OpenResty container is not the host. Join the same Docker network instead: in `docker-compose.yml`, remove the `ports` block and add

```yaml
    networks: [1panel-network]
networks:
  1panel-network:
    external: true
```

then use `proxy_pass http://relayward:8080;` (5.1) and `proxy_pass relayward:587;` (5.2). Publish port 465 on the OpenResty container, and open 465 and 443 in the firewall / cloud security group. Find the network's subnet with `docker network inspect 1panel-network` and put it in `proxy_protocol_trusted` if it is not inside `172.16.0.0/12`.

## 6. Create the first app

```bash
docker compose logs relayward | grep "generated initial admin token"   # rw_admin_...
ssh -N -L 8081:127.0.0.1:8081 user@mail.example.com                     # on your own PC
# open http://127.0.0.1:8081/admin, paste the token, create an app
```

The app's SMTP password is shown **once**. Or from the CLI:

```bash
docker compose exec relayward /relayward admin create-app \
  -config /etc/relayward/config.yaml gitea -from noreply@example.com
```

Point the app at `mail.example.com`, port **465**, encryption **SSL/TLS**, username = app name, password = the one-time password.

## 7. Verify

```bash
curl -i https://mail.example.com/healthz                                    # 200 {"status":"ok"}
openssl s_client -connect mail.example.com:465 -crlf </dev/null | head -20   # your certificate + "220"
swaks --server mail.example.com --port 465 --tls-on-connect \
      --auth LOGIN --auth-user gitea --auth-password 'one-time-password' \
      --from noreply@example.com --to you@yourdomain.com
```

The received mail should have an unsubscribe footer and a `List-Unsubscribe` header pointing to `https://mail.example.com/u/...`.

## 8. Day to day

| Task | How |
| ---- | --- |
| Logs | `docker compose logs -f relayward` |
| Monitor | `https://mail.example.com/healthz` (503 = database or upstream problem) |
| Upgrade | `git pull && docker compose up -d --build` (migrations run on start) |
| Change provider key | edit `.env`, `docker compose up -d` |
| Lost all admin tokens | `docker compose exec relayward /relayward admin reset -config /etc/relayward/config.yaml` |
| Certificate renewed | nothing to do for Relayward (nginx holds it); reload nginx as usual |

**Backup** = the `data/` folder (SQLite database + `unsubscribe_secret`). Losing `unsubscribe_secret` breaks every unsubscribe link already sent. For a consistent copy:

```bash
docker compose stop relayward && tar czf relayward-data-$(date +%F).tgz data && docker compose start relayward
```

Keep backups off the server; they contain secrets.

## 9. Troubleshooting

| Symptom | Likely cause |
| ------- | ------------ |
| Container exits on start | Config error; the log names the key (e.g. `public.base_url must use https`, `upstream.password is required`) |
| `permission denied` on `/data` | `sudo chown -R 65532:65532 data` |
| SMTP connects but closes at once (`421`) | `proxy_protocol on` in nginx, but `proxy_protocol_trusted` missing/not covering nginx's address |
| SMTP: garbage / TLS error from the app | App uses STARTTLS on 465 (use SSL/TLS), or `proxy_protocol_trusted` set without `proxy_protocol on` |
| `535 authentication failed` | Wrong app name/password. 10 failures in 10 minutes block that IP for an hour |
| `550 5.7.1 sender address not allowed` | `From` is not in the app's `allowed_from` |
| `451` | Rate limit or provider temporarily unavailable; retry later |
| Unsubscribe link 404 | Invalid token, or `unsubscribe.secret` changed after sending |
| `/healthz` is 503 | Database error or provider unreachable: check provider credentials and outbound port 587 |
| Admin page won't open | SSH tunnel is down; `8081` is intentionally not public |

## Hardening checklist

- [ ] `8080`, `2525` and `8081` listen on `127.0.0.1` only (`ss -tlnp`).
- [ ] Firewall allows only 22, 80/443 and 465 (restrict 465 to your app servers if their IPs are fixed).
- [ ] `.env` is `chmod 600`, the provider key exists nowhere else.
- [ ] Initial admin token saved in a password manager, then use operator/viewer tokens for daily work.
- [ ] Every app has a strict `allowed_from` and a sensible `rate_per_hour`.
- [ ] `data/` is backed up off-server.
- [ ] SPF, DKIM, DMARC published for the sending domain.
