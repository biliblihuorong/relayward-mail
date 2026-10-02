// Package web serves the recipient-facing unsubscribe pages on the public
// listener: a confirmation page (GET, no side effects, so mail security
// scanners cannot trigger an unsubscribe), the actual unsubscribe (POST,
// including RFC 8058 one-click requests from mail clients) and resubscribe.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"relayward-mail/internal/store"
	"relayward-mail/internal/unsub"
)

// maxBodyBytes caps request bodies; one-click and form posts are tiny.
const maxBodyBytes = 64 << 10

const (
	unsubPath    = "/u/"
	csrfCookie   = "rw_csrf"
	csrfField    = "csrf"
	resubAction  = "resubscribe"
	unknownAppCN = "该程序"
)

// Options configures the public unsubscribe surface.
type Options struct {
	Store  *store.Store
	Unsub  *unsub.Manager
	Logger *slog.Logger
}

// handler serves /u/{token}.
type handler struct {
	store  *store.Store
	unsub  *unsub.Manager
	logger *slog.Logger
	tpl    *template.Template
}

//go:embed templates/*.html
var templatesFS embed.FS

// New builds the public unsubscribe handler.
func New(opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	tpl := template.Must(template.ParseFS(templatesFS, "templates/*.html"))
	h := &handler{store: opts.Store, unsub: opts.Unsub, logger: opts.Logger, tpl: tpl}
	return securityHeaders(h.recoverPanics(http.MaxBytesHandler(h, maxBodyBytes)))
}

// recoverPanics logs a handler panic and answers a generic 500 page.
func (h *handler) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				h.logger.Error("panic in unsubscribe handler",
					slog.Any("panic", rec), slog.String("stack", string(debug.Stack())))
				writeError(w, r, http.StatusInternalServerError, "处理失败，请稍后再试")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders applies the plan's response headers. The unsubscribe page
// uses an inline stylesheet, so style-src allows inline styles while scripts
// stay locked to 'self' (the page ships none).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// ServeHTTP routes one unsubscribe request; unknown paths answer a generic
// 404 that reveals nothing about token validity.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, unsubPath) {
		http.NotFound(w, r)
		return
	}
	token := strings.Trim(strings.TrimPrefix(r.URL.Path, unsubPath), "/")
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.confirm(w, r, token)
	case http.MethodPost:
		h.act(w, r, token)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, r, http.StatusMethodNotAllowed, "不支持的请求方法")
	}
}

// confirmData feeds the confirmation template.
type confirmData struct {
	AppName string
	Email   string
	CSRF    string
	Token   string
}

// resultData feeds the unsubscribe / resubscribe result template.
type resultData struct {
	Title           string
	Message         string
	Token           string
	CSRF            string
	ShowResubscribe bool
}

// confirm serves GET /u/{token}: show the page, change nothing (mail
// security scanners fetch links but never submit forms).
func (h *handler) confirm(w http.ResponseWriter, r *http.Request, token string) {
	appID, email, ok := h.parseToken(w, token)
	if !ok {
		return
	}

	data := confirmData{
		AppName: h.appDisplayName(r, appID),
		Email:   email,
		CSRF:    h.csrfToken(w, r),
		Token:   token,
	}
	h.render(w, r, http.StatusOK, "confirm.html", data)
}

// act serves POST /u/{token}: an RFC 8058 one-click unsubscribe (no CSRF
// possible from a mail client — possessing the token is the authorization),
// or a form submission carrying the CSRF pair.
func (h *handler) act(w http.ResponseWriter, r *http.Request, token string) {
	appID, email, ok := h.parseToken(w, token)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, r, http.StatusBadRequest, "无法解析请求")
		return
	}

	if r.PostForm.Get("List-Unsubscribe") == "One-Click" {
		h.unsubscribe(w, r, appID, email, token, store.SourceOneClick, false)
		return
	}

	if !h.csrfValid(r) {
		writeError(w, r, http.StatusBadRequest, "请求校验失败，请返回上一页重试")
		return
	}
	if r.PostForm.Get("action") == resubAction {
		h.resubscribe(w, r, appID, email, token)
		return
	}
	h.unsubscribe(w, r, appID, email, token, store.SourceLink, true)
}

// unsubscribe records the opt-out and renders the result page.
func (h *handler) unsubscribe(w http.ResponseWriter, r *http.Request, appID int64, email, token, source string, offerResubscribe bool) {
	entry := &store.AuditEntry{
		TokenName: "public",
		IP:        remoteIP(r),
		Action:    store.ActionUnsubscribeCreate,
		Target:    email,
		Detail:    auditDetail(appID, source),
	}
	ctx := r.Context()
	if _, _, err := h.store.CreateUnsubscribe(ctx, appID, email, source, entry); err != nil {
		h.logger.Error("record unsubscribe", slog.Int64("app_id", appID), slog.String("err", err.Error()))
		writeError(w, r, http.StatusInternalServerError, "处理失败，请稍后再试")
		return
	}
	h.render(w, r, http.StatusOK, "result.html", resultData{
		Title:           "已退订",
		Message:         "今后将不再向您发送该程序的邮件。",
		Token:           token,
		CSRF:            h.csrfToken(w, r),
		ShowResubscribe: offerResubscribe,
	})
}

// resubscribe removes the opt-out again; missing rows are tolerated so the
// button stays idempotent.
func (h *handler) resubscribe(w http.ResponseWriter, r *http.Request, appID int64, email, token string) {
	ctx := r.Context()
	row, err := h.store.GetUnsubscribe(ctx, appID, email)
	if err == nil {
		entry := &store.AuditEntry{
			TokenName: "public",
			IP:        remoteIP(r),
			Action:    store.ActionUnsubscribeDelete,
			Target:    email,
			Detail:    auditDetail(appID, row.Source),
		}
		err = h.store.DeleteUnsubscribe(ctx, row.ID, entry)
	}
	if err != nil {
		h.logger.Error("resubscribe", slog.Int64("app_id", appID), slog.String("err", err.Error()))
		writeError(w, r, http.StatusInternalServerError, "处理失败，请稍后再试")
		return
	}
	h.render(w, r, http.StatusOK, "result.html", resultData{
		Title:   "已重新订阅",
		Message: "已恢复订阅，之后会继续收到该程序的邮件。",
		Token:   token,
	})
}

// parseToken decrypts the unsubscribe token; invalid tokens answer a generic
// 404 without revealing why.
func (h *handler) parseToken(w http.ResponseWriter, token string) (int64, string, bool) {
	appID, email, err := h.unsub.Parse(token)
	if err != nil {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return 0, "", false
	}
	return appID, email, true
}

// appDisplayName resolves the program name shown on the page; deleted apps
// still resolve from their row, and anything unresolvable degrades to a
// neutral label.
func (h *handler) appDisplayName(r *http.Request, appID int64) string {
	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()
	app, err := h.store.GetAppByID(ctx, appID)
	if err != nil {
		return unknownAppCN
	}
	if app.DisplayName != "" {
		return app.DisplayName
	}
	return app.Name
}

// csrfValid compares the cookie against the form field in constant time.
func (h *handler) csrfValid(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	field := r.PostForm.Get(csrfField)
	if field == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(field)) == 1
}

// csrfToken returns the CSRF token, minting (and setting) the cookie when
// the request does not carry a usable one.
func (h *handler) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) >= 32 {
		return c.Value
	}
	buf := make([]byte, 16) //nolint:gosec // 16 random bytes are the token, not a weak key
	if _, err := rand.Read(buf); err != nil {
		h.logger.Error("generate csrf token", slog.String("err", err.Error()))
		return ""
	}
	val := hex.EncodeToString(buf)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    val,
		Path:     unsubPath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	return val
}

// render writes one template with the given status.
func (h *handler) render(w http.ResponseWriter, _ *http.Request, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.tpl.ExecuteTemplate(w, name, data); err != nil {
		h.logger.Error("render template", slog.String("template", name), slog.String("err", err.Error()))
	}
}

func writeError(w http.ResponseWriter, _ *http.Request, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<!doctype html><html lang=\"zh-CN\"><meta charset=\"utf-8\"><title>出错了</title><p>%s</p>", template.HTMLEscapeString(message))
}

func auditDetail(appID int64, source string) string {
	detail, err := json.Marshal(struct {
		AppID  int64  `json:"app_id"`
		Source string `json:"source"`
	}{appID, source})
	if err != nil {
		return ""
	}
	return string(detail)
}

// remoteIP returns the host part of the peer address (same policy as the
// management API: X-Forwarded-For is not trusted).
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// dbTimeout bounds a store call made from the page handlers.
const dbTimeout = 5 * time.Second
