package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"relayward-mail/internal/store"
)

// Stable machine-readable error codes.
const (
	codeUnauthorized   = "unauthorized"
	codeForbidden      = "forbidden"
	codeIPForbidden    = "ip_forbidden"
	codeIPBanned       = "ip_banned"
	codeNotFound       = "not_found"
	codeAppNotFound    = "app_not_found"
	codeAppExists      = "app_exists"
	codeTokenNotFound  = "token_not_found"
	codeUnsubNotFound  = "unsubscribe_not_found"
	codeInvalidRequest = "invalid_request"
	codeInternal       = "internal_error"

	codeCaptchaFailed      = "captcha_failed"      // Cloudflare rejected the widget token
	codeCaptchaUnavailable = "captcha_unavailable" // siteverify could not be reached
)

// errorBody matches the plan's unified error shape.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError writes the unified JSON error response.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// writeStoreError maps store sentinel errors to API responses; unexpected
// errors become a generic 500 and are logged with full detail.
func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "resource not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, codeAppExists, "resource already exists")
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "invalid request")
	default:
		s.logger.Error("api internal error", slog.String("err", err.Error()))
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// decodeJSON decodes a JSON request body into v.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, fmt.Sprintf("malformed JSON body: %v", err))
		return false
	}
	return true
}

// ipRule is one parsed allowlist entry: a single IP or a CIDR range.
type ipRule struct {
	ip  net.IP
	net *net.IPNet
}

// parseIPAllowlist compiles the configured entries; invalid entries are
// logged and skipped. An allowlist that ends up empty disables filtering,
// which fails open on purpose: a typo must not cut the operator off from
// token management on a remote host.
func parseIPAllowlist(entries []string) []ipRule {
	var rules []ipRule
	for _, raw := range entries {
		if raw == "" {
			continue
		}
		if ip := net.ParseIP(raw); ip != nil {
			rules = append(rules, ipRule{ip: ip})
			continue
		}
		_, cidr, err := net.ParseCIDR(raw)
		if err != nil {
			slog.Error("admin.ip_allowlist entry ignored", slog.String("entry", raw), slog.String("err", err.Error()))
			continue
		}
		rules = append(rules, ipRule{net: cidr})
	}
	return rules
}

// ipAllowed reports whether ip matches at least one allowlist rule.
func ipAllowed(rules []ipRule, ip net.IP) bool {
	for _, rule := range rules {
		if rule.net != nil {
			if rule.net.Contains(ip) {
				return true
			}
			continue
		}
		if rule.ip.Equal(ip) {
			return true
		}
	}
	return false
}
