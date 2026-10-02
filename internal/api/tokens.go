package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"relayward-mail/internal/store"
)

// initialAdminTokenFile is removed when the bootstrap token is revoked.
const initialAdminTokenFile = "initial_admin_token"

// tokenJSON is the wire form of an admin token; the hash is never exposed.
type tokenJSON struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Role      string     `json:"role"`
	ExpiresAt *time.Time `json:"expires_at"`
	LastUsed  *time.Time `json:"last_used"`
	CreatedBy *int64     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
}

func toTokenJSON(t *store.AdminToken) tokenJSON {
	return tokenJSON{
		ID:        t.ID,
		Name:      t.Name,
		Role:      t.Role,
		ExpiresAt: t.ExpiresAt,
		LastUsed:  t.LastUsed,
		CreatedBy: t.CreatedBy,
		CreatedAt: t.CreatedAt,
	}
}

// handleListTokens serves GET /api/tokens (admin only, no plaintext).
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListAdminTokens(r.Context())
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	out := make([]tokenJSON, 0, len(tokens))
	for i := range tokens {
		out = append(out, toTokenJSON(&tokens[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// createTokenRequest is the POST /api/tokens body.
type createTokenRequest struct {
	Name      string     `json:"name"`
	Role      string     `json:"role"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// handleCreateToken serves POST /api/tokens. The plaintext token is returned
// exactly once.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" || len(req.Name) > 64 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "name must be 1 to 64 characters")
		return
	}
	switch req.Role {
	case store.RoleAdmin, store.RoleOperator, store.RoleViewer:
	default:
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "role must be admin, operator or viewer")
		return
	}
	if req.ExpiresAt != nil && req.ExpiresAt.Before(time.Now()) {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "expires_at must be in the future")
		return
	}

	raw, err := store.RandomToken("rw_", 32)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	creator := tokenFromContext(r.Context())
	tok := &store.AdminToken{
		Name:      req.Name,
		TokenHash: store.HashToken(raw),
		Role:      req.Role,
		ExpiresAt: req.ExpiresAt,
	}
	var createdBy *int64
	if creator != nil {
		createdBy = &creator.ID
		tok.CreatedBy = createdBy
	}

	detail, _ := json.Marshal(map[string]any{"role": req.Role})
	audit := s.auditEntry(r, store.ActionTokenCreate, req.Name, string(detail))
	if err := s.store.CreateAdminToken(r.Context(), tok, audit); err != nil {
		s.writeStoreError(w, err)
		return
	}

	s.logger.Info("admin token created",
		slog.String("token_name", req.Name),
		slog.String("role", req.Role))
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      raw,
		"token_info": toTokenJSON(tok),
	})
}

// handleRevokeToken serves DELETE /api/tokens/{id}. Revoking the bootstrap
// token also deletes the initial_admin_token file, per the plan.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "token id must be a number")
		return
	}

	tok, err := s.store.GetAdminTokenByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, codeTokenNotFound, "token not found")
		return
	}
	audit := s.auditEntry(r, store.ActionTokenRevoke, tok.Name, "")
	if err := s.store.DeleteAdminToken(r.Context(), id, audit); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if tok.Name == "initial" && s.dataDir != "" {
		if err := os.Remove(filepath.Join(s.dataDir, initialAdminTokenFile)); err != nil && !os.IsNotExist(err) {
			s.logger.Warn("remove initial token file", slog.String("err", err.Error()))
		}
	}

	s.logger.Info("admin token revoked", slog.String("token_name", tok.Name))
	w.WriteHeader(http.StatusNoContent)
}

// handleAudit serves GET /api/audit.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	entries, next, err := s.store.ListAuditLogs(r.Context(), store.AuditFilter{
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	type auditJSON struct {
		ID        int64     `json:"id"`
		Ts        time.Time `json:"ts"`
		TokenID   *int64    `json:"token_id"`
		TokenName *string   `json:"token_name"`
		IP        *string   `json:"ip"`
		Action    string    `json:"action"`
		Target    *string   `json:"target"`
		Detail    *string   `json:"detail"`
	}
	out := make([]auditJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, auditJSON{
			ID: e.ID, Ts: e.Ts, TokenID: e.TokenID, TokenName: e.TokenName,
			IP: e.IP, Action: e.Action, Target: e.Target, Detail: e.Detail,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out, "next_cursor": next})
}

// parseLimit reads the ?limit= query parameter (default 50, max 200).
func parseLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 || n > 200 {
		return 0, fmt.Errorf("limit must be an integer between 1 and 200")
	}
	return n, nil
}
