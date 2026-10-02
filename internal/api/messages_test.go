package api

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"relayward-mail/internal/store"
)

// seedLogData creates two apps and a mixed message log:
//
//	alpha: 2 sent, 1 failed, 1 rate_limited, 1 suppressed
//	beta:  1 sent (with a mixed-case recipient, stored normalized)
func seedLogData(t *testing.T, st *store.Store) (alpha, beta *store.App) {
	t.Helper()
	alpha = seedApp(t, st, "alpha")
	beta = seedApp(t, st, "beta")

	msgs := []*store.Message{
		{AppID: alpha.ID, MailFrom: "NoReply@example.com", RcptTo: "alice@example.com", Subject: "one", Size: 100, MessageID: "<1@example.com>", Status: store.StatusSent},
		{AppID: alpha.ID, MailFrom: "NoReply@example.com", RcptTo: "bob@example.com", Subject: "two", Size: 200, MessageID: "<2@example.com>", Status: store.StatusSent},
		{AppID: alpha.ID, MailFrom: "NoReply@example.com", RcptTo: "carol@example.com", Status: store.StatusFailed},
		{AppID: alpha.ID, MailFrom: "NoReply@example.com", RcptTo: "dave@example.com", Status: store.StatusRateLimited},
		{AppID: alpha.ID, MailFrom: "NoReply@example.com", RcptTo: "eve@example.com", Status: store.StatusSuppressed},
		{AppID: beta.ID, MailFrom: "NoReply@example.com", RcptTo: "Alice@Example.com", Status: store.StatusSent},
	}
	for i, m := range msgs {
		if err := st.InsertMessage(t.Context(), m); err != nil {
			t.Fatalf("insert message %d: %v", i, err)
		}
	}
	return alpha, beta
}

// TestStats checks the per-app aggregates of GET /api/stats.
func TestStats(t *testing.T) {
	st, ts := startAPI(t, nil)
	alpha, beta := seedLogData(t, st)
	viewer := seedToken(t, st, "view", store.RoleViewer)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/stats", viewer, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", status, body)
	}
	stats := bodySlice(t, body, "stats")
	if len(stats) != 2 {
		t.Fatalf("got %d stat rows, want 2: %v", len(stats), body)
	}
	byApp := map[string]map[string]any{}
	for _, s := range stats {
		byApp[str(s, "app")] = s
	}

	alphaStats, ok := byApp["alpha"]
	if !ok {
		t.Fatalf("no stats for alpha: %v", stats)
	}
	if got := num(alphaStats, "app_id"); got != float64(alpha.ID) {
		t.Errorf("alpha app_id = %v, want %d", got, alpha.ID)
	}
	for field, want := range map[string]float64{
		"total":        5,
		"sent":         2,
		"failed":       1,
		"rate_limited": 1,
		"suppressed":   1,
	} {
		if got := num(alphaStats, field); got != want {
			t.Errorf("alpha %s = %v, want %v", field, got, want)
		}
	}

	betaStats, ok := byApp["beta"]
	if !ok {
		t.Fatalf("no stats for beta: %v", stats)
	}
	if got := num(betaStats, "app_id"); got != float64(beta.ID) {
		t.Errorf("beta app_id = %v, want %d", got, beta.ID)
	}
	if got := num(betaStats, "total"); got != 1 {
		t.Errorf("beta total = %v, want 1", got)
	}
	if got := num(betaStats, "sent"); got != 1 {
		t.Errorf("beta sent = %v, want 1", got)
	}
}

// TestMessagesListAll checks the unfiltered message log and its fields.
func TestMessagesListAll(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedLogData(t, st)
	viewer := seedToken(t, st, "view", store.RoleViewer)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/messages", viewer, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", status, body)
	}
	msgs := bodySlice(t, body, "messages")
	if len(msgs) != 6 {
		t.Fatalf("got %d messages, want 6: %v", len(msgs), body)
	}
	if got := str(body, "next_cursor"); got != "" {
		t.Errorf("next_cursor = %q, want empty", got)
	}

	byRcpt := map[string]map[string]any{}
	for _, m := range msgs {
		for _, field := range []string{"id", "ts", "app", "mail_from", "rcpt_to", "subject", "size", "message_id", "status", "upstream_resp", "client_ip"} {
			if _, present := m[field]; !present {
				t.Errorf("message missing field %q: %v", field, m)
			}
		}
		byRcpt[str(m, "rcpt_to")] = m
	}

	alice := byRcpt["alice@example.com"]
	if alice == nil {
		t.Fatalf("no row for alice@example.com: %v", byRcpt)
	}
	if got := str(alice, "app"); got != "alpha" {
		t.Errorf("app = %q, want alpha", got)
	}
	if got := str(alice, "mail_from"); got != "NoReply@example.com" {
		t.Errorf("mail_from = %q, want NoReply@example.com", got)
	}
	if got := str(alice, "subject"); got != "one" {
		t.Errorf("subject = %q, want one", got)
	}
	if got := str(alice, "message_id"); got != "<1@example.com>" {
		t.Errorf("message_id = %q, want <1@example.com>", got)
	}
	if got := num(alice, "size"); got != 100 {
		t.Errorf("size = %v, want 100", got)
	}
	if got := str(alice, "status"); got != store.StatusSent {
		t.Errorf("status = %q, want sent", got)
	}
}

// TestMessagesFilters checks the app, to, status and since query filters.
func TestMessagesFilters(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedLogData(t, st)
	viewer := seedToken(t, st, "view", store.RoleViewer)

	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	tests := []struct {
		name      string
		query     url.Values
		wantCount int
		check     func(t *testing.T, msgs []map[string]any)
	}{
		{
			name:      "by app",
			query:     url.Values{"app": {"alpha"}},
			wantCount: 5,
			check: func(t *testing.T, msgs []map[string]any) {
				for _, m := range msgs {
					if got := str(m, "app"); got != "alpha" {
						t.Errorf("app = %q, want alpha", got)
					}
				}
			},
		},
		{
			name:      "by recipient, normalized",
			query:     url.Values{"to": {"Alice@Example.com"}},
			wantCount: 2,
			check: func(t *testing.T, msgs []map[string]any) {
				for _, m := range msgs {
					if got := str(m, "rcpt_to"); got != "alice@example.com" {
						t.Errorf("rcpt_to = %q, want alice@example.com", got)
					}
				}
			},
		},
		{
			name:      "by status",
			query:     url.Values{"status": {"failed"}},
			wantCount: 1,
			check: func(t *testing.T, msgs []map[string]any) {
				if len(msgs) == 1 && str(msgs[0], "status") != store.StatusFailed {
					t.Errorf("status = %q, want failed", str(msgs[0], "status"))
				}
			},
		},
		{
			name:      "since in the future",
			query:     url.Values{"since": {future}},
			wantCount: 0,
		},
		{
			name:      "since in the past",
			query:     url.Values{"since": {"2000-01-01T00:00:00Z"}},
			wantCount: 6,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body, _ := apiCall(t, ts, http.MethodGet, "/api/messages?"+tc.query.Encode(), viewer, nil)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %v)", status, body)
			}
			msgs := bodySlice(t, body, "messages")
			if len(msgs) != tc.wantCount {
				t.Fatalf("got %d messages, want %d: %v", len(msgs), tc.wantCount, body)
			}
			if tc.check != nil {
				tc.check(t, msgs)
			}
		})
	}
}

// TestMessagesInvalidFilters checks the 400 paths for malformed filters.
func TestMessagesInvalidFilters(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedLogData(t, st)
	viewer := seedToken(t, st, "view", store.RoleViewer)

	tests := []struct{ name, query string }{
		{"bad since", "since=not-a-timestamp"},
		{"bad until", "until=also-not-a-timestamp"},
		{"unknown status", "status=bogus"},
		{"zero limit", "limit=0"},
		{"huge limit", "limit=201"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body, _ := apiCall(t, ts, http.MethodGet, "/api/messages?"+tc.query, viewer, nil)
			wantError(t, status, body, http.StatusBadRequest, "invalid_request")
		})
	}
}

// TestMessagesPagination walks the whole log with limit=1 and a cursor.
func TestMessagesPagination(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedLogData(t, st)
	viewer := seedToken(t, st, "view", store.RoleViewer)

	seen := map[float64]bool{}
	var lastID float64
	cursor := ""
	for pages := 1; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		q := url.Values{"limit": {"1"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		status, body, _ := apiCall(t, ts, http.MethodGet, "/api/messages?"+q.Encode(), viewer, nil)
		if status != http.StatusOK {
			t.Fatalf("page %d: status = %d, want 200 (body %v)", pages, status, body)
		}
		msgs := bodySlice(t, body, "messages")
		if len(msgs) != 1 {
			t.Fatalf("page %d: got %d messages, want 1", pages, len(msgs))
		}
		id := num(msgs[0], "id")
		if seen[id] {
			t.Fatalf("page %d: message %v returned twice", pages, id)
		}
		if pages > 1 && id >= lastID {
			t.Errorf("page %d: id %v not smaller than the previous page's %v", pages, id, lastID)
		}
		seen[id] = true
		lastID = id
		cursor = str(body, "next_cursor")
		if cursor == "" {
			break
		}
	}
	if len(seen) != 6 {
		t.Errorf("walked %d messages, want 6", len(seen))
	}
}

// TestMessagesUnauthorized checks that the log needs a token.
func TestMessagesUnauthorized(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedLogData(t, st)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/messages", "", nil)
	wantError(t, status, body, http.StatusUnauthorized, "unauthorized")
}
