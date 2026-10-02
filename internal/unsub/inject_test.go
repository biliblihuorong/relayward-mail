package unsub

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
	"testing"
)

const testLink = "https://mail.example.com/u/tok1"

// decodePart fetches one part of a multipart body by content type. It uses
// NextRawPart so the caller controls transfer decoding.
func decodePart(t *testing.T, body []byte, boundary, mediaType string) []byte {
	t.Helper()
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		p, err := mr.NextRawPart()
		if err == io.EOF {
			t.Fatalf("part %s not found", mediaType)
		}
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		if p.Header.Get("Content-Type") != "" && strings.HasPrefix(p.Header.Get("Content-Type"), mediaType) {
			data, err := io.ReadAll(p)
			if err != nil {
				t.Fatalf("read part: %v", err)
			}
			return data
		}
		if p.Header.Get("Content-Type") == "" && mediaType == "text/plain" {
			data, err := io.ReadAll(p)
			if err != nil {
				t.Fatalf("read part: %v", err)
			}
			return data
		}
	}
}

func decodeCTE(t *testing.T, data []byte, cte string) []byte {
	t.Helper()
	switch cte {
	case "base64":
		out, err := base64.StdEncoding.DecodeString(strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' {
				return -1
			}
			return r
		}, string(data)))
		if err != nil {
			t.Fatalf("decode base64: %v", err)
		}
		return out
	case "quoted-printable":
		out, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(data)))
		if err != nil {
			t.Fatalf("decode qp: %v", err)
		}
		return out
	default:
		return data
	}
}

// assertCRLF fails when data contains a lone LF.
func assertCRLF(t *testing.T, data []byte) {
	t.Helper()
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' && (i == 0 || data[i-1] != '\r') {
			t.Fatalf("bare LF at offset %d", i)
		}
	}
}

func TestInjectFooterValidatesLink(t *testing.T) {
	data := []byte("Subject: x\r\n\r\nbody\r\n")
	for name, link := range map[string]string{
		"empty":   "",
		"with CR": "https://x.test/u/a\r\nBcc: evil",
		"with LF": "https://x.test/u/a\n",
	} {
		t.Run(name, func(t *testing.T) {
			out, res, err := InjectFooter(data, link, "退订")
			if err == nil {
				t.Fatal("expected an error")
			}
			if res.Injected || res.Parts != 0 {
				t.Fatalf("result = %+v, want zero", res)
			}
			if !bytes.Equal(out, data) {
				t.Fatal("input must be returned unchanged on error")
			}
		})
	}
}

func TestInjectFooterNoSeparator(t *testing.T) {
	data := []byte("Subject: headers only\r\n")
	out, res, err := InjectFooter(data, testLink, "退订")
	if err == nil || res.Injected {
		t.Fatalf("err = %v res = %+v, want error and zero result", err, res)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("input must be returned unchanged on error")
	}
}

func TestInjectPlainPartUpgradesToQP(t *testing.T) {
	data := "From: a@b.c\r\nSubject: hi\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"Content-Transfer-Encoding: 7bit\r\n\r\n" +
		"Plain body line.\r\nSecond line.\r\n"

	out, res, err := InjectFooter([]byte(data), testLink, "不想再收到此类邮件？退订")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Injected || res.Parts != 1 {
		t.Fatalf("result = %+v, want one injected part", res)
	}
	assertCRLF(t, out)

	// Header upgrades: CTE 7bit → quoted-printable, charset → utf-8.
	head := string(out[:bytes.Index(out, []byte("\r\n\r\n"))])
	if !strings.Contains(head, "Content-Transfer-Encoding: quoted-printable") {
		t.Errorf("CTE not upgraded:\n%s", head)
	}
	if !strings.Contains(head, "charset=utf-8") || strings.Contains(head, "us-ascii") {
		t.Errorf("charset not rewritten:\n%s", head)
	}

	// Decoding the body must yield the original text plus the plain footer.
	decoded := decodeCTE(t, out[bytes.Index(out, []byte("\r\n\r\n"))+4:], "quoted-printable")
	want := "Plain body line.\r\nSecond line.\r\n\r\n-- \r\n退订：" + testLink + "\r\n"
	if string(decoded) != want {
		t.Fatalf("decoded body = %q, want %q", decoded, want)
	}
}

func TestInjectBareMessageAddsHeaders(t *testing.T) {
	data := []byte("From: a@b.c\r\nSubject: bare\r\n\r\njust text\r\n")

	out, res, err := InjectFooter(data, testLink, "退订")
	if err != nil || !res.Injected {
		t.Fatalf("err = %v res = %+v", err, res)
	}
	head := string(out[:bytes.Index(out, []byte("\r\n\r\n"))])
	if !strings.Contains(head, "Content-Type: text/plain; charset=utf-8") {
		t.Errorf("Content-Type header not added:\n%s", head)
	}
	if !strings.Contains(head, "Content-Transfer-Encoding: quoted-printable") {
		t.Errorf("CTE header not added:\n%s", head)
	}
	// Original headers stay in place and first.
	if !strings.HasPrefix(string(out), "From: a@b.c\r\nSubject: bare\r\n") {
		t.Errorf("original headers disturbed:\n%s", head)
	}
}

func TestInjectBase64HTMLBeforeBody(t *testing.T) {
	htmlBody := "<html><body style=\"color:#000;\"><p>报告内容</p></body></html>\r\n"
	enc := base64.StdEncoding.EncodeToString([]byte(htmlBody))
	var wrapped strings.Builder
	for i := 0; i < len(enc); i += 76 {
		end := min(i+76, len(enc))
		wrapped.WriteString(enc[i:end])
		wrapped.WriteString("\r\n")
	}
	data := []byte("Subject: weekly\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + wrapped.String())

	out, res, err := InjectFooter(data, testLink, "不想再收到此类邮件？退订")
	if err != nil || !res.Injected {
		t.Fatalf("err = %v res = %+v", err, res)
	}

	// CTE and Content-Type header bytes must be untouched (already valid).
	head := string(out[:bytes.Index(out, []byte("\r\n\r\n"))])
	if !strings.Contains(head, "Content-Transfer-Encoding: base64") {
		t.Errorf("CTE changed:\n%s", head)
	}

	raw := out[bytes.Index(out, []byte("\r\n\r\n"))+4:]
	decoded := decodeCTE(t, raw, "base64")
	idx := strings.Index(string(decoded), "<hr style=\"border:none;border-top:1px solid #dddddd;margin:16px 0;\">")
	closeIdx := strings.Index(string(decoded), "</body>")
	if idx < 0 || closeIdx < 0 || idx > closeIdx {
		t.Fatalf("fragment not before </body>: %q", decoded)
	}
	if !strings.Contains(string(decoded), "<a href=\""+testLink+"\" style=\"color:#999999;\">退订</a></p></body>") {
		t.Errorf("anchor missing or misplaced: %q", decoded)
	}
	// The original markup stays intact.
	if !strings.Contains(string(decoded), "<p>报告内容</p>") {
		t.Errorf("original content damaged: %q", decoded)
	}
}

func TestInjectQPRoundTripExact(t *testing.T) {
	body := "第一行内容。\r\nSecond line with = sign needs encoding: a=b.\r\n"
	data := []byte("Subject: qp\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" + string(encodeQP([]byte(body))))

	out, res, err := InjectFooter(data, testLink, "退订")
	if err != nil || !res.Injected {
		t.Fatalf("err = %v res = %+v", err, res)
	}
	decoded := decodeCTE(t, out[bytes.Index(out, []byte("\r\n\r\n"))+4:], "quoted-printable")
	want := body + "\r\n-- \r\n退订：" + testLink + "\r\n"
	if string(decoded) != want {
		t.Fatalf("decoded = %q, want %q", decoded, want)
	}
}

func TestInjectAlternativeBothLeaves(t *testing.T) {
	data := []byte("Subject: alt\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"BB\"\r\n\r\n" +
		"--BB\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"Content-Transfer-Encoding: 7bit\r\n\r\n" +
		"plain words\r\n" +
		"--BB\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		string(encodeQP([]byte("<html><body><p>rich</p></body></html>"))) + "\r\n" +
		"--BB--\r\n")

	out, res, err := InjectFooter(data, testLink, "退订")
	if err != nil || !res.Injected || res.Parts != 2 {
		t.Fatalf("err = %v res = %+v, want two injected parts", err, res)
	}
	// Boundary and parent Content-Type survive byte-for-byte.
	if !bytes.Contains(out, []byte("Content-Type: multipart/alternative; boundary=\"BB\"\r\n")) {
		t.Errorf("parent Content-Type changed")
	}
	sep := bytes.Index(out, []byte("\r\n\r\n"))
	plain := decodeCTE(t, decodePart(t, out[sep+4:], "BB", "text/plain"), "quoted-printable")
	htmlBody := decodeCTE(t, decodePart(t, out[sep+4:], "BB", "text/html"), "quoted-printable")
	if !strings.Contains(string(plain), "-- \r\n退订：") {
		t.Errorf("plain leaf not injected: %q", plain)
	}
	if !strings.Contains(string(htmlBody), "<hr style=") {
		t.Errorf("html leaf not injected: %q", htmlBody)
	}
}

func TestInjectSkipsAttachmentAndCharset(t *testing.T) {
	data := []byte("Subject: mixed\r\n" +
		"Content-Type: multipart/mixed; boundary=\"MM\"\r\n\r\n" +
		"--MM\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: 7bit\r\n\r\n" +
		"read me\r\n" +
		"--MM\r\n" +
		"Content-Type: application/pdf; name=\"a.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"a.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte("%PDF-fake")) + "\r\n" +
		"--MM\r\n" +
		"Content-Type: text/plain; charset=gbk\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" +
		"\xb4\xbf\xb2\xe2 (gbk bytes)\r\n" +
		"--MM--\r\n")

	out, res, err := InjectFooter(data, testLink, "退订")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Injected || res.Parts != 1 {
		t.Fatalf("result = %+v, want exactly one injected part", res)
	}
	if bytes.Contains(out, []byte("退订")) && bytes.Count(out, []byte("退订")) != 1 {
		t.Errorf("footer leaked into skipped parts")
	}
	// Attachment and gbk part are byte-identical.
	if !bytes.Contains(out, []byte(base64.StdEncoding.EncodeToString([]byte("%PDF-fake")))) {
		t.Errorf("attachment bytes changed")
	}
	if !bytes.Contains(out, []byte("\xb4\xbf\xb2\xe2 (gbk bytes)")) {
		t.Errorf("gbk part changed")
	}
}

func TestInjectSignedAndEncryptedUntouched(t *testing.T) {
	for _, ct := range []string{
		`multipart/signed; protocol="application/pgp-signature"; boundary="S1"`,
		`multipart/encrypted; protocol="application/pgp-encrypted"; boundary="S1"`,
	} {
		data := []byte("Subject: s\r\nContent-Type: " + ct + "\r\n\r\n--S1\r\n\r\ninner\r\n--S1--\r\n")
		out, res, err := InjectFooter(data, testLink, "退订")
		if err != nil {
			t.Fatalf("%s: %v", ct, err)
		}
		if res.Injected {
			t.Fatalf("%s: injected %+v", ct, res)
		}
		if !bytes.Equal(out, data) {
			t.Fatalf("%s: message was modified", ct)
		}
	}
}

func TestInjectMalformedMultipartErrors(t *testing.T) {
	data := []byte("Subject: broken\r\nContent-Type: multipart/mixed; boundary=\"XX\"\r\n\r\n--XX\r\n\r\nnever closed\r\n")
	out, res, err := InjectFooter(data, testLink, "退订")
	if err == nil {
		t.Fatal("expected an error for the missing final boundary")
	}
	if res.Injected || !bytes.Equal(out, data) {
		t.Fatalf("original must be returned: res=%+v changed=%v", res, !bytes.Equal(out, data))
	}
}

func TestInjectHTMLEscapingAndAnchor(t *testing.T) {
	cases := []struct {
		footer string
		want   string
	}{
		{"不想再收到来自 Gitea 的邮件？退订", "不想再收到来自 Gitea 的邮件？"},
		{"不想再收到此类邮件", "不想再收到此类邮件&nbsp;"},
		{"从 <b>App</b> & 服务的邮件退订", "从 &lt;b&gt;App&lt;/b&gt; &amp; 服务的邮件"},
		{"<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;&nbsp;"},
	}
	data := []byte("Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		string(encodeQP([]byte("<html><body>x</body></html>"))))
	for _, tc := range cases {
		t.Run(tc.footer, func(t *testing.T) {
			out, res, err := InjectFooter(data, testLink, tc.footer)
			if err != nil || !res.Injected {
				t.Fatalf("err = %v res = %+v", err, res)
			}
			decoded := string(decodeCTE(t, out[bytes.Index(out, []byte("\r\n\r\n"))+4:], "quoted-printable"))
			if !strings.Contains(decoded, ">"+tc.want+"<a href=\"") {
				t.Errorf("prefix %q missing in %q", tc.want, decoded)
			}
			if strings.Count(decoded, "退订") != 1 {
				t.Errorf("anchor text must be the only 退订: %q", decoded)
			}
		})
	}
}

func TestInjectHTMLWithoutBodyTag(t *testing.T) {
	data := []byte("Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		string(encodeQP([]byte("<div>fragment only</div>"))))
	out, res, err := InjectFooter(data, testLink, "退订")
	if err != nil || !res.Injected {
		t.Fatalf("err = %v res = %+v", err, res)
	}
	decoded := string(decodeCTE(t, out[bytes.Index(out, []byte("\r\n\r\n"))+4:], "quoted-printable"))
	if !strings.HasSuffix(decoded, "<a href=\""+testLink+"\" style=\"color:#999999;\">退订</a></p>") {
		t.Errorf("fragment not appended at end: %q", decoded)
	}
}

func TestInjectHTMLBodyTagCaseInsensitive(t *testing.T) {
	data := []byte("Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		string(encodeQP([]byte("<html><body><p>x</p></BODY></html>"))))
	out, _, err := InjectFooter(data, testLink, "退订")
	if err != nil {
		t.Fatal(err)
	}
	decoded := string(decodeCTE(t, out[bytes.Index(out, []byte("\r\n\r\n"))+4:], "quoted-printable"))
	if !strings.Contains(decoded, "退订</a></p></BODY>") {
		t.Errorf("fragment not before uppercase </BODY>: %q", decoded)
	}
}

func TestInjectDeterministic(t *testing.T) {
	data := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\nsame input\r\n")
	a, _, _ := InjectFooter(data, testLink, "退订")
	b, _, _ := InjectFooter(data, testLink, "退订")
	if !bytes.Equal(a, b) {
		t.Fatal("injection is not deterministic")
	}
}

func TestInjectNonTextLeafUntouched(t *testing.T) {
	data := []byte("Content-Type: application/json; charset=utf-8\r\n\r\n{\"a\":1}\r\n")
	out, res, err := InjectFooter(data, testLink, "退订")
	if err != nil {
		t.Fatal(err)
	}
	if res.Injected || !bytes.Equal(out, data) {
		t.Fatalf("non-text part must stay untouched: res=%+v changed=%v", res, !bytes.Equal(out, data))
	}
}

func TestHasDKIMSignature(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"present", "DKIM-Signature: v=1; a=rsa-sha256\r\nSubject: x\r\n\r\nbody\r\n", true},
		{"lowercase", "dkim-signature: v=1\r\n\r\nbody\r\n", true},
		{"folded", "DKIM-Signature: v=1;\r\n\th=from\r\nSubject: x\r\n\r\nbody\r\n", true},
		{"absent", "Subject: x\r\n\r\nbody\r\n", false},
		{"in body only", "Subject: x\r\n\r\nDKIM-Signature: v=1\r\n", false},
		{"similar name", "X-DKIM-Signature: v=1\r\n\r\nbody\r\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasDKIMSignature([]byte(tc.data)); got != tc.want {
				t.Fatalf("HasDKIMSignature = %v, want %v", got, tc.want)
			}
		})
	}
}

// silence unused-import lint when helpers evolve
var (
	_ = textproto.MIMEHeader{}
	_ = mime.FormatMediaType
)
