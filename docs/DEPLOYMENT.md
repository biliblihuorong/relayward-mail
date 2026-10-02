# Deploying Relayward to the public internet

**English** | [简体中文](DEPLOYMENT.zh-CN.md) · Back to [README](../README.md)

This guide takes you from a blank Linux VPS to a gateway that your applications can reach over the internet, with HTTPS unsubscribe pages and TLS on the SMTP port.

> The commands below were written against the code in this repository (ports, paths, config keys, container layout). The Docker image build, container start-up, `/data` volume permissions and the binding of port 587 were tested locally; the nginx, certbot and firewall steps have not been run end-to-end on a real VPS yet, so rehearse on a throw-away server first.

## 0. What you are building

```
 apps (any network) ──STARTTLS :587──▶ ┌─────────────────────┐ ──STARTTLS──▶ mail provider
                                        │ relayward (Docker)  │
 recipients ──HTTPS :443──▶ nginx ───▶ │  :8080 public       │
                                        │  :8081 admin ◀── SSH tunnel / VPN (never public)
                                        └─────────────────────┘
```

| Port | Purpose | Exposure |
| ---- | ------- | -------- |
| 587 | SMTP ingress for apps | Internet **only with TLS configured**; ideally restricted to your app servers' IPs |
| 8080 | Unsubscribe pages and `/healthz` | Bound to `127.0.0.1`, published through nginx on 443 |
| 8081 | Admin API, `/admin` UI | Bound to `127.0.0.1`, reached through an SSH tunnel or VPN |

Why `8081` stays private: Relayward does **not** trust `X-Forwarded-For`. Behind a reverse proxy, the admin IP allow-list and the failure lockout would only ever see the proxy's address. Do not put the admin port on a public proxy.

## 1. Prerequisites

- A Linux server with a public IPv4 address (1 vCPU / 512 MB RAM is plenty), root or sudo access.
- A domain name you control, e.g. `mail.example.com`.
- An account at an SMTP provider (SES, SendGrid, Mailgun, Postmark, your own relay…) with: host, port 587, username, password/API key. The sending domain's **SPF / DKIM / DMARC** must be set up at that provider. Relayward does not sign mail itself.
- Docker Engine with the Compose plugin (or see [Option B](#option-b-systemd-without-docker)).
- Outbound access from the server to your provider on port 587.

## 2. DNS

Create an `A` record (and `AAAA` if you have IPv6) pointing the gateway host name at the server:

| Type | Name | Value |
| ---- | ---- | ----- |
| A | `mail.example.com` | your server IP |

`public.base_url` in the configuration uses this name; unsubscribe links in every mail point at it, so choose it carefully. Mails already sent keep the old link, so if you ever rename it, keep the old name serving `/u/...` too.

## 3. Firewall

Example with `ufw`:

```bash
sudo ufw default deny incoming
sudo ufw allow 22/tcp                 # SSH
sudo ufw allow 80/tcp                 # ACME challenge + HTTP->HTTPS redirect
sudo ufw allow 443/tcp                # unsubscribe pages
# SMTP ingress: restrict to your app servers if they have fixed IPs (recommended)
sudo ufw allow from 203.0.113.10 to any port 587 proto tcp
# ...or open to everyone (only with TLS configured in step 6):
# sudo ufw allow 587/tcp
sudo ufw enable
```

> Docker publishes ports by editing iptables directly and **bypasses ufw**. That is why the compose file below binds 8080/8081 to `127.0.0.1` explicitly. Check with `ss -tlnp` after starting.

If your cloud provider has a separate security group / firewall panel, mirror the same rules there.

## 4. Get the code and build the image

```bash
sudo mkdir -p /opt/relayward && cd /opt/relayward
sudo git clone https://github.com/biliblihuorong/relayward-mail.git src
sudo docker build -t relayward:latest ./src
```

The image is a static binary on `distroless/static:nonroot` (uid/gid 65532), with `/data` pre-created and owned by that user.

## 5. Configuration

`/opt/relayward/config.yaml`:

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
  base_url: https://mail.example.com      # must be https

admin:
  listen: ":8081"
  ip_allowlist: []                         # optional: e.g. ["10.0.0.0/8"] when reached over a VPN

unsubscribe:
  secret: ${UNSUB_SECRET}                  # leave empty to auto-generate into /data/unsubscribe_secret
  footer_text: "Don't want these emails? Unsubscribe"

log_retention_days: 90
```

`/opt/relayward/.env` (mode `0600`, never commit it):

```bash
UPSTREAM_KEY=your-provider-smtp-password-or-api-key
# UNSUB_SECRET=   # optional; empty = auto-generated and stored in the data volume
```

```bash
sudo chmod 600 /opt/relayward/.env
```

> `smtp.tls_cert` and `smtp.tls_key` must be set together. Once set, clients must `STARTTLS` before `AUTH`. Without them, passwords cross the network in clear text. That is acceptable only inside a private network.

## 6. Certificates and the HTTPS front door (nginx + certbot)

One certificate for `mail.example.com` serves both the HTTPS unsubscribe pages (nginx) and STARTTLS on port 587 (Relayward).

```bash
sudo apt install -y nginx certbot
sudo mkdir -p /var/www/certbot /opt/relayward/certs
```

First, an HTTP-only site so certbot can validate. `/etc/nginx/sites-available/relayward`:

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

Now add the HTTPS server block (append to the same file) and reload:

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name mail.example.com;

    ssl_certificate     /etc/letsencrypt/live/mail.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/mail.example.com/privkey.pem;

    # Only the public listener is proxied. The admin port is NOT exposed here.
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

Relayward reads the certificate **at start-up only**, so install a renewal hook that copies the renewed files and restarts the container. `/etc/letsencrypt/renewal-hooks/deploy/relayward.sh`:

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
sudo /etc/letsencrypt/renewal-hooks/deploy/relayward.sh 2>/dev/null || true   # first copy; the restart fails harmlessly until compose.yaml exists
sudo certbot renew --dry-run
```

## 7. Run it (Docker Compose)

`/opt/relayward/compose.yaml`:

```yaml
services:
  relayward:
    image: relayward:latest
    restart: unless-stopped
    env_file: .env
    ports:
      - "587:587"                 # SMTP ingress (TLS configured above)
      - "127.0.0.1:8080:8080"     # public pages -> nginx only
      - "127.0.0.1:8081:8081"     # admin -> SSH tunnel / VPN only
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
sudo docker compose ps               # STATUS should become "healthy"
sudo docker compose logs relayward   # look for: starting relayward ... / generated initial admin token
ss -tlnp | grep -E ':(587|8080|8081)\b'   # 8080 and 8081 must show 127.0.0.1
```

> Containers get `net.ipv4.ip_unprivileged_port_start=0` from Docker, so the non-root process can bind 587 inside the container. On Podman, add `--sysctl net.ipv4.ip_unprivileged_port_start=0` or publish a high port.

### Get the initial admin token

It is printed once at first start, and also stored in the data volume:

```bash
sudo docker compose logs relayward | grep "generated initial admin token"
```

Store the `rw_admin_...` value in your password manager. Then create day-to-day tokens and revoke the initial one:

```bash
TOKEN=rw_admin_xxxxxxxx
# from your laptop, with the tunnel from the next section running:
curl -s -X POST http://127.0.0.1:8081/api/tokens \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"ops-alice","role":"operator"}'
```

(See the README for the full token API.)

### Reach the admin UI

```bash
ssh -N -L 8081:127.0.0.1:8081 user@mail.example.com
# then open http://127.0.0.1:8081/admin in your browser and paste the token
```

Prefer a VPN (WireGuard/Tailscale)? Bind `8081` to the VPN interface address instead of `127.0.0.1` and set `admin.ip_allowlist` to your VPN CIDR.

### Create an app

In the admin UI (or via `POST /api/apps`), create an app. The one-time SMTP password is shown **once**. CLI alternative:

```bash
sudo docker compose exec relayward /relayward admin create-app \
  -config /etc/relayward/config.yaml gitea -from noreply@example.com
```

## Option B: systemd without Docker

```bash
# Build anywhere with Go, then copy the binary
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o relayward ./cmd/relayward
sudo install -m 0755 relayward /usr/local/bin/relayward

sudo useradd --system --home /var/lib/relayward --shell /usr/sbin/nologin relayward
sudo install -d -o relayward -g relayward -m 0700 /var/lib/relayward
sudo install -d -m 0755 /etc/relayward
# config.yaml: same as above but data_dir: /var/lib/relayward and cert paths of your choice
# /etc/relayward/relayward.env (0600, root:root): UPSTREAM_KEY=...
```

`/etc/systemd/system/relayward.service`:

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

The user running the service must be able to read the certificate files; a certbot deploy hook that copies them to `/etc/relayward/certs` (owner `relayward`, key `0600`) and runs `systemctl restart relayward` does the job.

## 8. Verify

```bash
# 1. Public listener through HTTPS
curl -i https://mail.example.com/healthz              # 200 {"status":"ok"}

# 2. SMTP TLS on 587
openssl s_client -starttls smtp -connect mail.example.com:587 -crlf </dev/null | head -20

# 3. End-to-end send (swaks), using the app's one-time password
swaks --server mail.example.com --port 587 --tls \
      --auth LOGIN --auth-user gitea --auth-password 'THE-ONE-TIME-PASSWORD' \
      --from noreply@example.com --to you@yourdomain.com

# 4. The log shows it (through the tunnel)
curl -s "http://127.0.0.1:8081/api/messages?app=gitea&limit=5" -H "Authorization: Bearer $TOKEN"
```

Open the received mail: it should carry an unsubscribe footer, and a `List-Unsubscribe` header pointing at `https://mail.example.com/u/...`. Opening that link must show the confirmation page.

Then point your real applications at `mail.example.com:587` (STARTTLS, username = app name, password = the one-time password). When every app is switched, **rotate the provider key** and update only `UPSTREAM_KEY` in `/opt/relayward/.env`, then `docker compose up -d`.

## 9. Operating it

| Task | How |
| ---- | --- |
| Logs | `docker compose logs -f relayward` (JSON lines on stdout) |
| Monitoring | Point Uptime Kuma or similar at `https://mail.example.com/healthz` (503 = `degraded`: database or upstream problem) |
| Upgrade | `cd /opt/relayward/src && git pull && docker build -t relayward:latest . && cd .. && docker compose up -d`. Migrations run automatically at start-up |
| Rotate upstream key | Edit `.env`, `docker compose up -d` |
| Rotate an app's password | `POST /api/apps/{name}/rotate` or the UI button; the old one dies immediately |
| Lost all admin tokens | `docker compose exec relayward /relayward admin reset -config /etc/relayward/config.yaml` |
| Log retention | `log_retention_days` (default 90); pruned at start-up and every 24 h |

### Backups

Back up the **whole data volume**. It holds the SQLite database (apps, hashed passwords, log, suppressions, tokens) and `unsubscribe_secret`. **Losing `unsubscribe_secret` invalidates every unsubscribe link already sent.**

```bash
# consistent copy: stop briefly, archive, start
cd /opt/relayward
sudo docker compose stop relayward
sudo docker run --rm -v relayward_relayward-data:/data -v "$PWD":/backup busybox \
     tar czf /backup/relayward-data-$(date +%F).tgz -C /data .
sudo docker compose start relayward
```

(The volume name is `<compose-project>_relayward-data`; check with `docker volume ls`.) Keep copies off the server and restrict them: the archive contains secrets.

## 10. Hardening checklist

- [ ] `8080` and `8081` listen on `127.0.0.1` only (`ss -tlnp`).
- [ ] Port 587 restricted to known app IPs, or TLS enabled and verified with `openssl s_client`.
- [ ] `public.base_url` is the real HTTPS name and `https://.../healthz` works.
- [ ] `.env` is `0600`; the provider key lives nowhere else (apps no longer hold it).
- [ ] Initial admin token stored in a password manager; day-to-day work uses operator/viewer tokens.
- [ ] Each app has a tight `allowed_from` list and a realistic `rate_per_hour`.
- [ ] Certificate renewal tested (`certbot renew --dry-run`) and the deploy hook restarts Relayward.
- [ ] Off-server backup of the data volume, including `unsubscribe_secret`.
- [ ] SPF, DKIM and DMARC are published for your sending domain at the provider.
- [ ] OS updates enabled (`unattended-upgrades`), SSH key-only login.

## 11. Troubleshooting

| Symptom | Likely cause |
| ------- | ------------ |
| App gets `535 authentication failed` | Wrong app name/password, app disabled, or app was rotated. After 10 failures in 10 min the source IP is banned for 1 h |
| `530`/AUTH not offered | TLS is configured, so the client must use STARTTLS before AUTH |
| `550 5.7.1 sender address not allowed` | The `From` address is not on the app's `allowed_from` list |
| `451` | Rate limit hit (`rate_limited` in the log), upstream temporarily down, or a transient gateway error: retry later |
| `554` | The provider rejected the message permanently; see the log entry for the upstream reply |
| `/healthz` returns 503 | Database error or upstream unreachable; check `docker compose logs` and your provider credentials / outbound port 587 |
| Unsubscribe link returns 404 | Token invalid or `unsubscribe.secret` changed since the mail was sent |
| Container exits at start | Config error: the message names the key (e.g. `public.base_url must use https`, `upstream.password is required`) |
| `permission denied` on `/data` | Bind-mounting a host directory? `chown 65532:65532` it. A named volume needs nothing |
| Admin UI unreachable | The tunnel is down, or you used the public name; `8081` is intentionally not public |
