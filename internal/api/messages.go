package api

import (
	"net/http"
	"time"

	"relayward-mail/internal/store"
)

// handleStats serves GET /api/stats: per-app totals.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.AppStats(r.Context())
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if stats == nil {
		stats = []store.AppStats{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats})
}

// handleMessages serves GET /api/messages with filters and cursor paging.
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	filter := store.MessageFilter{
		App:    q.Get("app"),
		To:     q.Get("to"),
		Status: q.Get("status"),
		Limit:  limit,
		Cursor: q.Get("cursor"),
	}
	if raw := q.Get("since"); raw != "" {
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "since must be an RFC 3339 timestamp")
			return
		}
		filter.Since = &ts
	}
	if raw := q.Get("until"); raw != "" {
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "until must be an RFC 3339 timestamp")
			return
		}
		filter.Until = &ts
	}

	views, next, err := s.store.ListMessages(r.Context(), filter)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	type messageJSON struct {
		ID           int64     `json:"id"`
		Ts           time.Time `json:"ts"`
		App          string    `json:"app"`
		MailFrom     string    `json:"mail_from"`
		RcptTo       string    `json:"rcpt_to"`
		Subject      string    `json:"subject"`
		Size         int64     `json:"size"`
		MessageID    string    `json:"message_id"`
		Status       string    `json:"status"`
		UpstreamResp string    `json:"upstream_resp"`
		ClientIP     string    `json:"client_ip"`
	}
	out := make([]messageJSON, 0, len(views))
	for _, v := range views {
		out = append(out, messageJSON{
			ID: v.ID, Ts: v.Ts, App: v.AppName,
			MailFrom: v.MailFrom, RcptTo: v.RcptTo, Subject: v.Subject,
			Size: v.Size, MessageID: v.MessageID, Status: v.Status,
			UpstreamResp: v.UpstreamResp, ClientIP: v.ClientIP,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out, "next_cursor": next})
}
