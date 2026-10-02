package api

import (
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"relayward-mail/internal/store"
)

// unsubscribeJSON is the wire form of one unsubscribe row.
type unsubscribeJSON struct {
	ID     int64     `json:"id"`
	AppID  int64     `json:"app_id"`
	App    string    `json:"app"`
	Email  string    `json:"email"`
	Source string    `json:"source"`
	Ts     time.Time `json:"ts"`
}

// handleListUnsubscribes serves GET /api/unsubscribes with app/email filters
// and cursor paging.
func (s *Server) handleListUnsubscribes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	views, next, err := s.store.ListUnsubscribes(r.Context(), store.UnsubscribeFilter{
		App:    q.Get("app"),
		Email:  q.Get("email"),
		Limit:  limit,
		Cursor: q.Get("cursor"),
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	out := make([]unsubscribeJSON, 0, len(views))
	for _, v := range views {
		out = append(out, unsubscribeJSON{
			ID: v.ID, AppID: v.AppID, App: v.AppName,
			Email: v.Email, Source: v.Source, Ts: v.Ts,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"unsubscribes": out, "next_cursor": next})
}

// createUnsubscribeRequest is the POST /api/unsubscribes body.
type createUnsubscribeRequest struct {
	App   string `json:"app"`
	Email string `json:"email"`
}

// handleCreateUnsubscribe serves POST /api/unsubscribes: record one (app,
// email) pair as unsubscribed. Creating an existing pair again is idempotent
// and returns the row that is already on file.
func (s *Server) handleCreateUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var req createUnsubscribeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.App == "" || strings.TrimSpace(req.Email) == "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "app and email are required")
		return
	}

	app, err := s.store.GetAppByName(r.Context(), req.App)
	if err != nil {
		writeError(w, http.StatusNotFound, codeAppNotFound, "app not found")
		return
	}

	addr, err := mail.ParseAddress(strings.TrimSpace(req.Email))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "email must be a valid address")
		return
	}
	email := store.NormalizeEmail(addr.Address)

	entry := s.auditEntry(r, store.ActionUnsubscribeCreate, email, "")
	row, created, err := s.store.CreateUnsubscribe(r.Context(), app.ID, email, store.SourceAPI, entry)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"unsubscribe": unsubscribeJSON{
		ID: row.ID, AppID: row.AppID, App: app.Name,
		Email: row.Email, Source: row.Source, Ts: row.Ts,
	}})
}

// handleDeleteUnsubscribe serves DELETE /api/unsubscribes/{id}: remove the
// row, which restores delivery for that (app, email) pair.
func (s *Server) handleDeleteUnsubscribe(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "id must be a positive integer")
		return
	}

	if err := s.store.DeleteUnsubscribe(r.Context(), id, s.auditEntry(r, store.ActionUnsubscribeDelete, raw, "")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeUnsubNotFound, "unsubscribe not found")
			return
		}
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
