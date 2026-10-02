package smtpd

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"mime/quotedprintable"

	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"

	"relayward-mail/internal/store"
	"relayward-mail/internal/unsub"
)

// seedSplitApp inserts an app with the unsubscribe switch on.
func seedSplitApp(t *testing.T, st *store.Store, name string) *store.App {
	t.Helper()
	hash, err := store.HashPassword("pw-" + name)
	if err != nil {
		t.Fatal(err)
	}
	app := &store.App{
		Name:          name,
		PasswordHash:  hash,
		Enabled:       true,
		Unsubscribe:   true,
		BodyInjection: true,
		AllowedFrom:   []string{"NoReply@example.com"},
		RatePerHour:   500,
	}
	if err := st.CreateApp(t.Context(), app, nil); err != nil {
		t.Fatalf("seed app %s: %v", name, err)
	}
	return app
}

func testManager(t *testing.T) *unsub.Manager {
	t.Helper()
	mgr, err := unsub.NewManager([]byte("split-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

const splitBaseURL = "https://mail.example.com"

// splitFooter exercises the {app} placeholder replacement.
const splitFooter = "不想再收到来自 {app} 的邮件？退订"

// newSplitGateway wires a gateway whose backend carries a real unsubscribe
// manager and footer, relaying into a recording fake upstream.
func newSplitGateway(t *testing.T) (*store.Store, *fakeUpstream, string) {
	t.Helper()
	st := openStore(t)
	up := &fakeUpstream{}
	be := NewBackend(st, newTestRelay(t, startFakeSMTPUpstream(t, up)), nil, nil, nil, testManager(t), splitBaseURL, splitFooter)
	return st, up, startSMTP(t, be)
}

// sendToMany performs one transaction to several recipients.
func sendToMany(t *testing.T, cl *smtp.Client, user, pass, from string, tos []string, body string) error {
	t.Helper()
	if err := cl.Auth(sasl.NewPlainClient("", user, pass)); err != nil {
		return err
	}
	if err := cl.Mail(from, nil); err != nil {
		return err
	}
	for _, to := range tos {
		if err := cl.Rcpt(to, nil); err != nil {
			return err
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(body)); err != nil {
		return err
	}
	return w.Close()
}

// tokenFromLink extracts the opaque token out of a List-Unsubscribe header.
func tokenFromLink(t *testing.T, data []byte) string {
	t.Helper()
	marker := splitBaseURL + "/u/"
	idx := bytes.Index(data, []byte(marker))
	if idx < 0 {
		t.Fatalf("no unsubscribe link in %q", data)
	}
	rest := data[idx+len(marker):]
	end := bytes.IndexByte(rest, '>')
	if end < 0 {
		t.Fatalf("unterminated link in %q", data)
	}
	return string(rest[:end])
}

func TestSplitPerRecipientHeadersAndBcc(t *testing.T) {
	st, up, addr := newSplitGateway(t)
	app := seedSplitApp(t, st, "gitea")

	cl := dialSMTP(t, addr)
	body := "From: NoReply@example.com\r\nTo: a@example.com\r\nCc: c@example.com\r\nBcc: secret@example.com\r\nSubject: batch\r\nMessage-Id: <m1@gitea>\r\n\r\nhello\r\n"
	if err := sendToMany(t, cl, "gitea", "pw-gitea", "NoReply@example.com",
		[]string{"a@example.com", "b@example.com"}, body); err != nil {
		t.Fatalf("send: %v", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.messages) != 2 {
		t.Fatalf("upstream got %d messages, want 2 (one per recipient)", len(up.messages))
	}

	seen := map[string][]byte{}
	for _, msg := range up.messages {
		if len(msg.Rcpts) != 1 {
			t.Fatalf("split message carried %d recipients: %+v", len(msg.Rcpts), msg)
		}
		seen[msg.Rcpts[0]] = msg.Data
		data := string(msg.Data)
		if strings.Contains(data, "Bcc:") {
			t.Errorf("Bcc header leaked to %s: %q", msg.Rcpts[0], data)
		}
		if !strings.Contains(data, "To: a@example.com") || !strings.Contains(data, "Cc: c@example.com") {
			t.Errorf("To/Cc headers must stay untouched: %q", data)
		}
		if !strings.Contains(data, "List-Unsubscribe: <"+splitBaseURL+"/u/") {
			t.Errorf("missing List-Unsubscribe header: %q", data)
		}
		if !strings.Contains(data, "List-Unsubscribe-Post: List-Unsubscribe=One-Click") {
			t.Errorf("missing one-click header: %q", data)
		}
	}

	tokA := tokenFromLink(t, seen["a@example.com"])
	tokB := tokenFromLink(t, seen["b@example.com"])
	if tokA == tokB {
		t.Fatal("both recipients received the same token")
	}
	appID, email, err := testManager(t).Parse(tokA)
	if err != nil || appID != app.ID || email != "a@example.com" {
		t.Fatalf("parse token A: appID=%d email=%q err=%v", appID, email, err)
	}
	_, emailB, err := testManager(t).Parse(tokB)
	if err != nil || emailB != "b@example.com" {
		t.Fatalf("parse token B: email=%q err=%v", emailB, err)
	}
}

func TestSplitSuppressesUnsubscribed(t *testing.T) {
	st, up, addr := newSplitGateway(t)
	app := seedSplitApp(t, st, "gitea")
	if _, _, err := st.CreateUnsubscribe(t.Context(), app.ID, "b@example.com", store.SourceLink, nil); err != nil {
		t.Fatal(err)
	}

	cl := dialSMTP(t, addr)
	body := "Subject: still fine\r\n\r\nhello\r\n"
	if err := sendToMany(t, cl, "gitea", "pw-gitea", "NoReply@example.com",
		[]string{"a@example.com", "b@example.com"}, body); err != nil {
		t.Fatalf("send must succeed (250) despite suppression: %v", err)
	}

	up.mu.Lock()
	if len(up.messages) != 1 || up.messages[0].Rcpts[0] != "a@example.com" {
		t.Fatalf("upstream messages = %+v, want exactly the unsuppressed recipient", up.messages)
	}
	up.mu.Unlock()

	views, _, err := st.ListMessages(t.Context(), store.MessageFilter{App: "gitea"})
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, v := range views {
		statuses[v.RcptTo] = v.Status
	}
	if statuses["a@example.com"] != store.StatusSent || statuses["b@example.com"] != store.StatusSuppressed {
		t.Fatalf("log statuses = %+v, want a=sent b=suppressed", statuses)
	}
}

func TestSplitAllSuppressed(t *testing.T) {
	st, up, addr := newSplitGateway(t)
	app := seedSplitApp(t, st, "gitea")
	for _, email := range []string{"a@example.com", "b@example.com"} {
		if _, _, err := st.CreateUnsubscribe(t.Context(), app.ID, email, store.SourceAPI, nil); err != nil {
			t.Fatal(err)
		}
	}

	cl := dialSMTP(t, addr)
	if err := sendToMany(t, cl, "gitea", "pw-gitea", "NoReply@example.com",
		[]string{"a@example.com", "b@example.com"}, "Subject: x\r\n\r\ny\r\n"); err != nil {
		t.Fatalf("all-suppressed send must answer 250: %v", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.messages) != 0 {
		t.Fatalf("nothing may reach upstream, got %+v", up.messages)
	}
	views, _, _ := st.ListMessages(t.Context(), store.MessageFilter{App: "gitea"})
	if len(views) != 2 {
		t.Fatalf("expected 2 suppressed log rows, got %+v", views)
	}
}

func TestSplitDisabledForwardsAsIs(t *testing.T) {
	st, up, addr := newSplitGateway(t)
	seedApp(t, st, "plain", true) // unsubscribe switch off

	cl := dialSMTP(t, addr)
	body := "From: NoReply@example.com\r\nBcc: secret@example.com\r\nSubject: as-is\r\n\r\nhello\r\n"
	if err := sendToMany(t, cl, "plain", "pw-plain", "NoReply@example.com",
		[]string{"a@example.com", "b@example.com"}, body); err != nil {
		t.Fatalf("send: %v", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.messages) != 1 {
		t.Fatalf("unsubscribe-off apps must relay one copy, got %d", len(up.messages))
	}
	msg := up.messages[0]
	if len(msg.Rcpts) != 2 {
		t.Fatalf("expected both recipients on one envelope: %+v", msg)
	}
	if !bytes.Contains(msg.Data, []byte("Bcc: secret@example.com")) {
		t.Errorf("unsubscribe-off must forward as-is including Bcc: %q", msg.Data)
	}
	if bytes.Contains(msg.Data, []byte("List-Unsubscribe")) {
		t.Errorf("no headers may be injected on the as-is path: %q", msg.Data)
	}
}

// rcptFilteringUpstream rejects recipients containing the marker with a
// temporary error; everything else is accepted and recorded.
type rcptFilteringUpstream struct {
	marker string
	fakeUpstream
}

func (u *rcptFilteringUpstream) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &rcptFilteringSession{backend: u}, nil
}

type rcptFilteringSession struct {
	backend *rcptFilteringUpstream
	msg     relayed
}

func (s *rcptFilteringSession) Reset()                   { s.msg = relayed{} }
func (s *rcptFilteringSession) Logout() error            { return nil }
func (s *rcptFilteringSession) AuthMechanisms() []string { return []string{sasl.Plain} }
func (s *rcptFilteringSession) Auth(_ string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, _, _ string) error { return nil }), nil
}
func (s *rcptFilteringSession) Mail(from string, _ *smtp.MailOptions) error {
	s.msg.From = from
	return nil
}
func (s *rcptFilteringSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	if strings.Contains(to, s.backend.marker) {
		return &smtp.SMTPError{Code: 451, Message: "try again"}
	}
	s.msg.Rcpts = append(s.msg.Rcpts, to)
	return nil
}
func (s *rcptFilteringSession) Data(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.msg.Data = data
	s.backend.mu.Lock()
	s.backend.messages = append(s.backend.messages, s.msg)
	s.backend.mu.Unlock()
	return nil
}

func TestSplitPartialFailureReturnsTempError(t *testing.T) {
	st := openStore(t)
	up := &rcptFilteringUpstream{marker: "bad@"}
	be := NewBackend(st, newTestRelay(t, startFakeSMTPUpstream(t, up)), nil, nil, nil, testManager(t), splitBaseURL, splitFooter)
	addr := startSMTP(t, be)
	seedSplitApp(t, st, "gitea")

	cl := dialSMTP(t, addr)
	err := sendToMany(t, cl, "gitea", "pw-gitea", "NoReply@example.com",
		[]string{"good@example.com", "bad@example.com"}, "Subject: partial\r\n\r\nx\r\n")
	if code := smtpErrorCode(t, err); code != 451 {
		t.Fatalf("err = %v, want 451 on partial upstream failure", err)
	}

	views, _, _ := st.ListMessages(t.Context(), store.MessageFilter{App: "gitea"})
	statuses := map[string]string{}
	for _, v := range views {
		statuses[v.RcptTo] = v.Status
	}
	if statuses["good@example.com"] != store.StatusSent || statuses["bad@example.com"] != store.StatusFailed {
		t.Fatalf("log statuses = %+v, want good=sent bad=failed", statuses)
	}
}

func TestSplitMissingManagerTempFails(t *testing.T) {
	st := openStore(t)
	up := &fakeUpstream{}
	be := NewBackend(st, newTestRelay(t, startFakeSMTPUpstream(t, up)), nil, nil, nil, nil, "", "")
	addr := startSMTP(t, be)
	seedSplitApp(t, st, "gitea")

	cl := dialSMTP(t, addr)
	err := sendToMany(t, cl, "gitea", "pw-gitea", "NoReply@example.com",
		[]string{"a@example.com"}, "Subject: x\r\n\r\ny\r\n")
	if code := smtpErrorCode(t, err); code != 451 {
		t.Fatalf("err = %v, want 451 when the gateway cannot split", err)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.messages) != 0 {
		t.Fatalf("nothing may be forwarded without the token manager, got %+v", up.messages)
	}
}

func TestSplitInjectsBodyFooter(t *testing.T) {
	st, up, addr := newSplitGateway(t)
	seedSplitApp(t, st, "gitea") // no display name: {app} falls back to the name

	cl := dialSMTP(t, addr)

	// HTML part: the fine print carries the {app} wording and the link.
	html := "From: NoReply@example.com\r\nSubject: html\r\nMessage-Id: <b1@g>\r\n" +
		"Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 7bit\r\n\r\n" +
		"<p>Hello html world.</p>\r\n</body>\r\n"
	if err := sendToMany(t, cl, "gitea", "pw-gitea", "NoReply@example.com", []string{"a@example.com"}, html); err != nil {
		t.Fatalf("html send: %v", err)
	}

	// Plain part: a blank line, the signature delimiter and the link line.
	plain := "From: NoReply@example.com\r\nSubject: plain\r\nMessage-Id: <b2@g>\r\n\r\nHello plain world.\r\n"
	cl2 := dialSMTP(t, addr)
	if err := sendToMany(t, cl2, "gitea", "pw-gitea", "NoReply@example.com", []string{"a@example.com"}, plain); err != nil {
		t.Fatalf("plain send: %v", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.messages) != 2 {
		t.Fatalf("upstream got %d messages, want 2", len(up.messages))
	}

	htmlData := string(decodeQP(bodyAfterHeaders(t, up.messages[0].Data)))
	if !strings.Contains(htmlData, "不想再收到来自 gitea 的邮件？") {
		t.Errorf("{app} placeholder not replaced with the app name:\n%s", htmlData)
	}
	linkPrefix := `<a href="` + splitBaseURL + `/u/`
	if i, j := strings.Index(htmlData, linkPrefix), strings.Index(htmlData, "</body>"); i < 0 || j < 0 || i > j {
		t.Errorf("html fragment missing or not before </body>:\n%s", htmlData)
	}

	plainData := string(decodeQP(bodyAfterHeaders(t, up.messages[1].Data)))
	if !strings.Contains(plainData, "Hello plain world.\r\n\r\n-- \r\n退订："+splitBaseURL+"/u/") {
		t.Errorf("plain footer missing or wrong link:\n%s", plainData)
	}
}

// bodyAfterHeaders splits a relayed message into its raw body.
func bodyAfterHeaders(t *testing.T, data []byte) []byte {
	t.Helper()
	sep := bytes.Index(data, []byte("\r\n\r\n"))
	if sep < 0 {
		t.Fatalf("malformed relayed message: %q", data)
	}
	return data[sep+4:]
}

// decodeQP decodes a quoted-printable body.
func decodeQP(data []byte) []byte {
	r := quotedprintable.NewReader(bytes.NewReader(data))
	out, err := io.ReadAll(r)
	if err != nil {
		return data
	}
	return out
}

func TestSplitSkipsBodyInjectionForDKIM(t *testing.T) {
	st, up, addr := newSplitGateway(t)
	seedSplitApp(t, st, "gitea")

	cl := dialSMTP(t, addr)
	body := "From: NoReply@example.com\r\nDKIM-Signature: v=1; a=rsa-sha256; d=example.com\r\nSubject: signed\r\n\r\nSigned body stays intact.\r\n"
	if err := sendToMany(t, cl, "gitea", "pw-gitea", "NoReply@example.com", []string{"a@example.com"}, body); err != nil {
		t.Fatalf("send: %v", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.messages) != 1 {
		t.Fatalf("upstream got %d messages, want 1", len(up.messages))
	}
	data := string(up.messages[0].Data)
	if strings.Contains(data, "退订") || strings.Contains(data, "quoted-printable") {
		t.Errorf("body must stay untouched for DKIM-signed mail:\n%s", data)
	}
	if !strings.Contains(data, "List-Unsubscribe:") {
		t.Errorf("unsubscribe headers must still be injected:\n%s", data)
	}
}

func TestSplitRespectsBodyInjectionOff(t *testing.T) {
	st, up, addr := newSplitGateway(t)
	app := seedSplitApp(t, st, "gitea")
	on, off := true, false
	if _, err := st.UpdateApp(t.Context(), app.Name, store.AppUpdate{BodyInjection: &off}, nil); err != nil {
		t.Fatal(err)
	}
	_ = on

	cl := dialSMTP(t, addr)
	body := "From: NoReply@example.com\r\nSubject: no footer\r\n\r\nBody without footer.\r\n"
	if err := sendToMany(t, cl, "gitea", "pw-gitea", "NoReply@example.com", []string{"a@example.com"}, body); err != nil {
		t.Fatalf("send: %v", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	data := string(up.messages[0].Data)
	if strings.Contains(data, "退订") {
		t.Errorf("footer injected despite body_injection=off:\n%s", data)
	}
	if !strings.Contains(data, "List-Unsubscribe:") {
		t.Errorf("unsubscribe headers must still be injected:\n%s", data)
	}
}

func TestSplitInjectionFailureForwardsUnchanged(t *testing.T) {
	st, up, addr := newSplitGateway(t)
	seedSplitApp(t, st, "gitea")

	cl := dialSMTP(t, addr)
	// Declares a multipart body but is unparseable: InjectFooter errors and
	// the message must be forwarded with headers only, bytes untouched.
	body := "From: NoReply@example.com\r\nSubject: broken\r\nContent-Type: multipart/mixed; boundary=\"XX\"\r\n\r\n--XX\r\nno final boundary here\r\n"
	if err := sendToMany(t, cl, "gitea", "pw-gitea", "NoReply@example.com", []string{"a@example.com"}, body); err != nil {
		t.Fatalf("send must succeed despite the broken body: %v", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.messages) != 1 {
		t.Fatalf("upstream got %d messages, want 1", len(up.messages))
	}
	msg := up.messages[0].Data
	if !bytes.Contains(msg, []byte("no final boundary here\r\n")) || bytes.Contains(msg, []byte("List-Unsubscribe: <")) == false {
		t.Errorf("expected original body plus injected headers:\n%q", msg)
	}
	if bytes.Contains(msg, []byte("退订：")) {
		t.Errorf("footer must not be injected into an unparseable body:\n%q", msg)
	}
}
