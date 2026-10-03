<div align="center">

**English** | [简体中文](README.zh-CN.md)

# Relayward

**A self-hosted mail relay gateway. Point every app at the gateway, keep the real provider key in one place.**

Single static Go binary · one SQLite file · zero runtime dependencies

[30 s promo (EN)](https://github.com/biliblihuorong/relayward-mail/raw/master/docs/media/relayward-promo-en.mp4) · [30 s promo (中文)](https://github.com/biliblihuorong/relayward-mail/raw/master/docs/media/relayward-promo-zh.mp4) · [Deployment guide](docs/DEPLOYMENT.md) 

</div>

---

<div align="center">
<video src="https://github.com/biliblihuorong/relayward-mail/raw/master/docs/media/relayward-promo-en.mp4" controls muted width="720"></video>
<br><sub>30 s promo · if the player does not load, <a href="https://github.com/biliblihuorong/relayward-mail/raw/master/docs/media/relayward-promo-en.mp4">open the video</a> · <a href="https://github.com/biliblihuorong/relayward-mail/raw/master/docs/media/relayward-promo-zh.mp4">中文版</a></sub>
</div>

## Why Relayward

Self-hosted tools (Gitea, Kanboard, Grafana, wikis, CRMs…) all need to send mail, and each one is usually handed your real SMTP/API key. Keys end up scattered, nobody can tell which app sent what, one noisy app can burn your provider reputation, and unsubscribe handling is missing entirely.

Relayward sits in the middle:

```
 Gitea ─┐
 Kanboard ─┤  per-app SMTP     ┌────────────┐   real provider key   ┌───────────────┐
 Grafana ─┼─ credentials ───▶ │ Relayward  │ ───────────────────▶ │ Mail provider │
 Wiki ─┤   (port 587)         │  gateway   │   (only stored here)  └───────────────┘
 CRM ─┘                       └─────┬──────┘
                                    │ log · rate-limit · unsubscribe
                              SQLite + admin API/UI (:8081)
                              unsubscribe pages     (:8080)
```

- **One real key.** Apps authenticate with their own SMTP password; the provider key lives only in the gateway. Rotate it once, in one place.
- **Per-app control.** Allowed sender addresses, hourly rate limit, enable/disable, password reset — all per app.
- **Full send log.** Every message (and every recipient) is recorded: sent, failed, rate-limited, suppressed.
- **Unsubscribe built in.** RFC 8058 one-click `List-Unsubscribe` headers, a body footer link, a confirmation page, per-app/per-recipient suppression.
- **Admin API and embedded web UI**, three roles (admin / operator / viewer), audit log, IP allow-list, failure lockout.

## Features

| Area | What you get |
| ---- | ------------ |
| Relay | SMTP ingress (AUTH PLAIN/LOGIN, STARTTLS), sender-address validation, forwarding to the upstream provider, per-recipient result mapping (upstream 4xx → 451, 5xx → 554) |
| Safety | Token-bucket rate limit per app, per-IP failure lockout (10 failures / 10 min → 1 h ban), argon2id password hashing, SHA-256 hashed admin tokens, panic recovery on every listener |
| Unsubscribe | Per-recipient message split, `List-Unsubscribe` + `List-Unsubscribe-Post`, stateless AES-256-GCM tokens (no e-mail or app name in the link), GET confirm page (safe against mail scanners), POST one-click, re-subscribe |
| Body injection | MIME-aware footer for `text/plain` and `text/html`; skips signed/encrypted parts, attachments, non-UTF-8 parts, and DKIM-signed mail (headers are still injected) |
| Admin | REST API, embedded admin page (`/admin`), audit log, `relayward admin reset` for lost tokens |
| Ops | `/healthz` (cached upstream probe), Docker `HEALTHCHECK`, structured logs, graceful shutdown, log retention (`log_retention_days`, default 90) |

## Quick start

Requires the Go version declared in `go.mod`.

```bash
# 1. Build (static binary, CGO disabled) -> bin/relayward
make build

# 2. Configure
cp config.example.yaml config.yaml
$EDITOR config.yaml                 # upstream host/user, public.base_url (must be https)
export UPSTREAM_KEY=your-provider-smtp-key

# 3. Run
./bin/relayward serve -config config.yaml
```

On first start Relayward generates a super-admin token (prefix `rw_admin_`). It is printed to the log **once** and written to `data/initial_admin_token` (mode 0600); only its SHA-256 hash is stored.

Create a sending app and connect it:

```bash
TOKEN=$(cat data/initial_admin_token)
curl -s -X POST http://127.0.0.1:8081/api/apps \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"gitea","allowed_from":["noreply@example.com"],"rate_per_hour":500,"display_name":"Gitea"}'
# the response contains the app's one-time SMTP password
```

Then configure the app's SMTP settings:

| Setting | Value |
| ------- | ----- |
| Host / port | gateway address, `587` |
| Username | the app name (`gitea`) |
| Password | the one-time password returned above |
| From | only addresses on that app's allow-list |

When every app is switched, rotate the key at your provider and update `UPSTREAM_KEY` on the gateway only.

You can also open `http://127.0.0.1:8081/admin`, paste the token, and do everything from the browser.

> Going to production? Read the **[Deployment guide](docs/DEPLOYMENT.md)** — Docker Compose, nginx/OpenResty HTTPS and SMTP TLS, admin access, backups.

## Ports

| Port | Purpose | Expose to |
| ---- | ------- | --------- |
| 587 | SMTP ingress for your apps | only your app servers (firewall / VPN) |
| 8080 | Public: unsubscribe pages, `/healthz` | the internet, **via HTTPS reverse proxy** |
| 8081 | Admin API, `/admin` UI, `/healthz` | private network / VPN / SSH tunnel only |

## Admin API

All endpoints need `Authorization: Bearer <token>`, speak JSON and return errors as `{"error":{"code":"...","message":"..."}}`.

| Method | Path | Description | Min. role |
| ------ | ---- | ----------- | --------- |
| GET | `/api/stats` | Per-app totals: sent / failed / rate-limited / suppressed | viewer |
| GET | `/api/messages` | Send log; filters `app`, `to`, `status`, `since`, `until`; cursor pagination | viewer |
| GET | `/api/apps`, `/api/apps/{name}` | List / inspect apps | viewer |
| POST | `/api/apps` | Create app; response contains the one-time SMTP password | operator |
| PATCH | `/api/apps/{name}` | Enable/disable, unsubscribe, body injection, rate limit, allowed senders | operator |
| POST | `/api/apps/{name}/rotate` | Reset SMTP password; the old one stops working immediately | operator |
| DELETE | `/api/apps/{name}` | Soft-delete (logs are kept) | operator |
| GET/POST | `/api/unsubscribes` | List / add suppression (idempotent) | viewer / operator |
| DELETE | `/api/unsubscribes/{id}` | Remove suppression (= re-subscribe) | operator |
| GET/POST | `/api/tokens` | List / create tokens (plaintext shown once) | admin |
| DELETE | `/api/tokens/{id}` | Revoke a token | admin |
| GET | `/api/audit` | Audit log of every management write | admin |

Roles: **admin** (everything), **operator** (apps and unsubscribes), **viewer** (read-only).

## Unsubscribe

Apps with unsubscribe enabled (the default) get one outgoing copy **per recipient**, each with its own link; `Bcc` is stripped:

```
List-Unsubscribe: <https://mail.example.com/u/TOKEN>
List-Unsubscribe-Post: List-Unsubscribe=One-Click
```

- `GET /u/{token}` shows a confirmation page and changes nothing (mail scanners can't unsubscribe people by accident).
- The confirm form (CSRF-protected) or a mail client's RFC 8058 one-click `POST` unsubscribes.
- Unsubscribed recipients are not forwarded to, yet the app still receives `250`; the log records `suppressed`.
- Suppression is per **app + address**: unsubscribing from app A does not affect app B.
- `unsubscribe.secret` is auto-generated on first start (`data/unsubscribe_secret`). Changing it invalidates every link already sent.

## Local CLI

```bash
./bin/relayward serve -config config.yaml
./bin/relayward admin create-app -config config.yaml gitea -from noreply@example.com
./bin/relayward admin reset -config config.yaml     # lost every admin token? revoke all and regenerate
./bin/relayward healthcheck -url http://127.0.0.1:8081/healthz
```

## Configuration

See [`config.example.yaml`](config.example.yaml) (every `${VAR}` is expanded from the environment).

| Key | Meaning |
| --- | ------- |
| `data_dir` | SQLite file and secrets (`initial_admin_token`, `unsubscribe_secret`) |
| `smtp.listen`, `smtp.tls_cert`, `smtp.tls_key`, `smtp.max_message_size` | Ingress listener; set cert **and** key to enable STARTTLS (then AUTH requires it) |
| `smtp.proxy_protocol_trusted` | Proxy IPs/CIDRs allowed to send a PROXY protocol v1 header with the real client IP (nginx/OpenResty `stream` TLS termination, see the [Deployment guide](docs/DEPLOYMENT.md)). Empty = off |
| `upstream.*` | Provider host, port, username, password (`${UPSTREAM_KEY}`), `tls: starttls\|none` |
| `public.listen`, `public.base_url` | Recipient-facing listener; `base_url` must be an `https://` URL |
| `admin.listen`, `admin.ip_allowlist` | Admin listener; restrict `/api` to IPs/CIDRs |
| `unsubscribe.secret`, `unsubscribe.footer_text` | Token secret (blank = auto) and footer wording (`{app}` placeholder) |
| `log_retention_days` | Delete message-log rows older than this many days (default 90) |

## Security notes

- Open port 587 only to your app servers. If apps connect over the public internet, configure `smtp.tls_cert`/`tls_key` (or terminate TLS on nginx, see the Deployment guide), otherwise passwords travel in clear text.
- `X-Forwarded-For` is **not** trusted. Behind a reverse proxy the admin IP allow-list and lockout would see the proxy's address, so keep `:8081` off the public proxy and reach it via VPN or an SSH tunnel.
- Lost every admin token? Run `relayward admin reset` on the host.
- Details and the reasoning behind each decision: [`docs/DECISIONS.md`](docs/DECISIONS.md).

## Development

```bash
make test   # CGO_ENABLED=1 go test -race ./...  (needs a C compiler)
make lint   # golangci-lint run
make run    # build and start
```

- Only the libraries in the plan's tech-stack table are used (plus `golang.org/x/sync/errgroup`); new dependencies must be recorded in `docs/DECISIONS.md`.
- Conventional Commits.
- Re-render the promo videos: `pip install pillow` then `python promo/make_video.py all` (needs `ffmpeg`).

## Documentation

| Document | Content |
| -------- | ------- |
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) · [中文](docs/DEPLOYMENT.zh-CN.md) | Deploying to the public internet |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Design decisions and trade-offs (Chinese) |
| [docs/ACCEPTANCE.zh-CN.md](docs/ACCEPTANCE.zh-CN.md) | Milestone acceptance results M1–M5 (Chinese) |
| [docs/Relayward 邮件网关计划书.md](docs/Relayward%20邮件网关计划书.md) | Original design plan (Chinese) |

## Status

All five milestones (M1 relay, M2 admin API, M3 unsubscribe, M4 body injection, M5 embedded admin page) are implemented and covered by automated tests. Not yet verified against real-world mail clients: Gmail's native unsubscribe button and a broader set of real application mail samples.

## License

See [LICENSE](LICENSE).
