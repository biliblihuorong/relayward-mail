package unsub

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
)

// ErrInvalidLink is returned (wrapped) by InjectFooter when the link is empty
// or contains CR or LF.
var ErrInvalidLink = errors.New("unsub: invalid unsubscribe link")

// errSignedOrEncrypted aborts body injection for the whole message: signed or
// encrypted content must reach the recipient untouched. It never escapes the
// package; callers see the original bytes with Injected=false.
var errSignedOrEncrypted = errors.New("unsub: signed or encrypted message")

// InjectResult reports what InjectFooter changed.
type InjectResult struct {
	Injected bool // at least one text part was rewritten
	Parts    int  // number of text parts rewritten
}

// InjectFooter appends the unsubscribe footer to the text parts of the MIME
// message data. footerText is the configured wording with the {app}
// placeholder already replaced by the caller; text/html parts render it as a
// light-gray rule plus a 12px gray line whose 退订 anchor links to link, and
// text/plain parts get a blank line, the "-- " signature delimiter and a
// 退订：<link> line.
//
// Attachments, non-UTF-8 parts and unknown transfer encodings are left
// untouched; multipart/signed and multipart/encrypted abort injection for the
// whole message. Any structural failure returns the original data together
// with the error, so the caller can forward the message unchanged.
func InjectFooter(data []byte, link, footerText string) ([]byte, InjectResult, error) {
	if link == "" || strings.ContainsAny(link, "\r\n") {
		return data, InjectResult{}, fmt.Errorf("inject footer: %w", ErrInvalidLink)
	}
	block, tail := splitHeaderBlock(data)
	if len(tail) == 0 {
		// A message without a header/body separator has no body to inject
		// into and is too malformed to rebuild safely.
		return data, InjectResult{}, fmt.Errorf("inject footer: message has no header/body separator")
	}
	env := injectEnv{link: link, footerText: footerText}

	newBlock, newBody, res, err := rewriteEntity(block, bodyAfterSeparator(tail), env)
	if errors.Is(err, errSignedOrEncrypted) {
		return data, InjectResult{}, nil
	}
	if err != nil {
		return data, InjectResult{}, err
	}
	if !res.Injected {
		return data, res, nil
	}
	out := make([]byte, 0, len(newBlock)+len(crlf)+len(newBody))
	out = append(out, newBlock...)
	out = append(out, crlf...)
	out = append(out, newBody...)
	return out, res, nil
}

// HasDKIMSignature reports whether the message header block contains a
// DKIM-Signature header (case-insensitive). Only the header block is
// examined, so a DKIM mention inside the body does not count.
func HasDKIMSignature(data []byte) bool {
	block, _ := splitHeaderBlock(data)
	skipping := false
	for _, line := range bytes.SplitAfter(block, []byte(crlf)) {
		if len(line) == 0 {
			continue
		}
		if skipping && isContinuation(line) {
			continue
		}
		skipping = false
		if isHeaderNamed(line, "DKIM-Signature") {
			return true
		}
	}
	return false
}

// injectEnv carries the rendering inputs through the part tree.
type injectEnv struct {
	link       string
	footerText string
}

// rewriteEntity rewrites one MIME entity (headers block plus raw body) and
// returns the new header block and body. Block always ends with the final
// header line's CRLF; the caller re-adds the blank separator line.
func rewriteEntity(block, body []byte, env injectEnv) ([]byte, []byte, InjectResult, error) {
	hdr, err := readHeaders(block)
	if err != nil {
		return block, body, InjectResult{}, fmt.Errorf("inject footer: parse headers: %w", err)
	}

	mediaType, params := "text/plain", map[string]string{}
	if ct := hdr.Get("Content-Type"); ct != "" {
		mt, p, err := mime.ParseMediaType(ct)
		if err != nil {
			return block, body, InjectResult{}, fmt.Errorf("inject footer: parse content type: %w", err)
		}
		mediaType, params = mt, p
	}

	switch lower := strings.ToLower(mediaType); {
	case lower == "multipart/signed" || lower == "multipart/encrypted":
		return block, body, InjectResult{}, errSignedOrEncrypted
	case strings.HasPrefix(lower, "multipart/"):
		return rewriteMultipart(mediaType, params, block, body, env)
	default:
		return rewriteLeaf(mediaType, params, hdr, block, body, env)
	}
}

// rewriteMultipart recurses into the children of a multipart body, rebuilding
// it with the original boundary. Children keep their raw bytes unless they
// were rewritten themselves.
func rewriteMultipart(_ string, params map[string]string, block, body []byte, env injectEnv) ([]byte, []byte, InjectResult, error) {
	zero := InjectResult{}
	boundary := params["boundary"]
	if boundary == "" {
		return block, body, zero, fmt.Errorf("inject footer: multipart without boundary")
	}
	frame, err := splitMultipart(body, boundary)
	if err != nil {
		return block, body, zero, err
	}

	newParts := make([][]byte, len(frame.parts))
	total := InjectResult{}
	for i, partRaw := range frame.parts {
		pb, pbody, pres, perr := rewritePartRaw(partRaw, env)
		if perr != nil {
			return block, body, zero, perr
		}
		newParts[i] = append(append(bytes.Clone(pb), crlf...), pbody...)
		total.Injected = total.Injected || pres.Injected
		total.Parts += pres.Parts
	}
	if !total.Injected {
		return block, body, total, nil
	}
	return block, reassembleMultipart(frame, boundary, newParts), total, nil
}

// rewritePartRaw dispatches one raw multipart child, which may consist of a
// body only (parts without a header section are valid).
func rewritePartRaw(partRaw []byte, env injectEnv) ([]byte, []byte, InjectResult, error) {
	block, tail := splitHeaderBlock(partRaw)
	if len(tail) == 0 {
		return rewriteEntity(nil, partRaw, env)
	}
	return rewriteEntity(block, bodyAfterSeparator(tail), env)
}

// bodyAfterSeparator strips the blank separator line (the first CRLF of a
// splitHeaderBlock tail) and returns the raw body.
func bodyAfterSeparator(tail []byte) []byte {
	if len(tail) >= 2 {
		return tail[2:]
	}
	return nil
}

// rewriteLeaf rewrites one text/plain or text/html leaf; everything else —
// attachments, other media types, unsupported charsets or encodings, parts
// that fail to decode — is returned unchanged without an error.
func rewriteLeaf(mediaType string, params map[string]string, hdr textproto.MIMEHeader, block, body []byte, env injectEnv) ([]byte, []byte, InjectResult, error) {
	zero := InjectResult{}
	lower := strings.ToLower(mediaType)
	if (lower != "text/plain" && lower != "text/html") || isAttachment(hdr) {
		return block, body, zero, nil
	}
	switch strings.ToLower(params["charset"]) {
	case "", "utf-8", "utf8", "us-ascii":
	default:
		return block, body, zero, nil
	}

	cte := strings.ToLower(strings.TrimSpace(hdr.Get("Content-Transfer-Encoding")))
	decoded, err := decodeBody(body, cte)
	if err != nil {
		return block, body, zero, nil
	}

	var footered []byte
	if lower == "text/html" {
		footered = injectHTML(decoded, env)
	} else {
		footered = injectPlain(decoded, env)
	}

	var reencoded []byte
	upgradeCTE := false
	switch cte {
	case "base64":
		reencoded = encodeBase64(footered)
	case "quoted-printable":
		reencoded = encodeQP(footered)
	case "", "7bit":
		// The footer always introduces non-ASCII bytes (退订), so 7bit and
		// CTE-less parts are upgraded to quoted-printable.
		reencoded = encodeQP(footered)
		upgradeCTE = true
	default: // 8bit, binary
		reencoded = footered
	}

	if charset := strings.ToLower(params["charset"]); charset == "" || charset == "us-ascii" {
		block = replaceHeader(block, "Content-Type", mime.FormatMediaType(mediaType, withCharset(params, "utf-8")))
	}
	if upgradeCTE {
		block = replaceHeader(block, "Content-Transfer-Encoding", "quoted-printable")
	}
	return block, reencoded, InjectResult{Injected: true, Parts: 1}, nil
}

// decodeBody reverses the transfer encoding; unknown encodings are an error
// so the caller skips the part.
func decodeBody(body []byte, cte string) ([]byte, error) {
	switch cte {
	case "base64":
		clean := bytes.Map(func(r rune) rune {
			if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
				return -1
			}
			return r
		}, body)
		return base64.StdEncoding.DecodeString(string(clean))
	case "quoted-printable":
		return io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
	case "", "7bit", "8bit", "binary":
		return bytes.Clone(body), nil
	default:
		return nil, fmt.Errorf("unsub: unsupported content transfer encoding %q", cte)
	}
}

// encodeQP re-encodes a body as quoted-printable with CRLF line endings.
func encodeQP(b []byte) []byte {
	var buf bytes.Buffer
	w := quotedprintable.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

// encodeBase64 re-encodes a body as base64 wrapped at 76 characters.
func encodeBase64(b []byte) []byte {
	raw := make([]byte, base64.StdEncoding.EncodedLen(len(b)))
	base64.StdEncoding.Encode(raw, b)
	var buf bytes.Buffer
	for len(raw) > 0 {
		n := min(76, len(raw))
		buf.Write(raw[:n])
		buf.WriteString(crlf)
		raw = raw[n:]
	}
	return buf.Bytes()
}

// injectHTML inserts the HTML fragment before the last </body> (any case), or
// appends it when the part has no body tag.
func injectHTML(decoded []byte, env injectEnv) []byte {
	frag := htmlFragment(env)
	idx := bytes.LastIndex(bytes.ToLower(decoded), []byte("</body>"))
	if idx < 0 {
		out := bytes.Clone(decoded)
		if len(out) > 0 && !bytes.HasSuffix(out, []byte(crlf)) {
			out = append(out, crlf...)
		}
		return append(out, frag...)
	}
	out := make([]byte, 0, len(decoded)+len(frag))
	out = append(out, decoded[:idx]...)
	out = append(out, frag...)
	return append(out, decoded[idx:]...)
}

// htmlFragment renders the rule-plus-fine-print block; the leading CRLF
// terminates whatever line precedes it.
func htmlFragment(env injectEnv) []byte {
	return []byte(crlf +
		`<hr style="border:none;border-top:1px solid #dddddd;margin:16px 0;">` + crlf +
		`<p style="color:#999999;font-size:12px;margin:0;">` + htmlTextPrefix(env.footerText) +
		`<a href="` + env.link + `" style="color:#999999;">退订</a></p>`)
}

// htmlTextPrefix escapes the footer wording; a trailing 退订 is dropped
// because the anchor text provides it, otherwise the wording keeps its own
// meaning and is separated from the anchor by a no-break space.
func htmlTextPrefix(footerText string) string {
	const mark = "退订"
	if strings.HasSuffix(footerText, mark) {
		trimmed := strings.TrimRight(strings.TrimSuffix(footerText, mark), " \t\u3000")
		return html.EscapeString(trimmed)
	}
	return html.EscapeString(footerText) + "&nbsp;"
}

// injectPlain appends the plain-text footer: one blank line, the RFC 3676
// signature delimiter and the unsubscribe line.
func injectPlain(decoded []byte, env injectEnv) []byte {
	out := bytes.Clone(decoded)
	if len(out) == 0 || !bytes.HasSuffix(out, []byte(crlf)) {
		out = append(out, crlf...)
	}
	return append(out, []byte("\r\n-- \r\n退订："+env.link+"\r\n")...)
}

// splitMultipart frames a multipart body into the bytes up to and including
// the first delimiter line, the raw child parts, and everything from the
// closing delimiter onwards. The CRLF before a delimiter belongs to the
// framing, not to the part content; a body that starts with the delimiter
// directly (no preamble) is valid.
type multipartFrame struct {
	prefix []byte // preamble plus the first delimiter line, ending with CRLF
	parts  [][]byte
	tail   []byte // closing delimiter line plus epilogue
}

func splitMultipart(body []byte, boundary string) (multipartFrame, error) {
	delim := []byte("--" + boundary)
	var frame multipartFrame

	// Locate the first delimiter line; a body may start with it directly.
	delimStart := 0
	if !bytes.HasPrefix(body, delim) {
		i := bytes.Index(body, []byte(crlf+string(delim)))
		if i < 0 {
			return frame, fmt.Errorf("unsub: boundary %q not found", boundary)
		}
		delimStart = i + len(crlf)
	}
	lineEnd := bytes.Index(body[delimStart:], []byte(crlf))
	if lineEnd < 0 {
		return frame, fmt.Errorf("unsub: unterminated first boundary %q", boundary)
	}
	lineEnd += delimStart

	// A first line of "--boundary--" is an empty multipart body.
	if trimmed := bytes.TrimRight(body[delimStart+len(delim):lineEnd], " \t"); bytes.Equal(trimmed, []byte("--")) {
		frame.tail = body[delimStart:]
		return frame, nil
	}
	frame.prefix = body[:lineEnd+len(crlf)]

	for pos := len(frame.prefix); ; {
		next := bytes.Index(body[pos:], []byte(crlf+string(delim)))
		if next < 0 {
			return frame, fmt.Errorf("unsub: missing final boundary %q", boundary)
		}
		frame.parts = append(frame.parts, body[pos:pos+next])
		pos += next + len(crlf) // body[pos:] now starts with the delimiter

		after := body[pos+len(delim):]
		if trimmed := bytes.TrimRight(after[:min(len(after), 2)], " \t"); bytes.Equal(trimmed, []byte("--")) {
			frame.tail = body[pos:]
			return frame, nil
		}
		if !bytes.HasPrefix(after, []byte(crlf)) {
			return frame, fmt.Errorf("unsub: malformed multipart delimiter for boundary %q", boundary)
		}
		pos += len(delim) + len(crlf) // start of the next part's content
	}
}

// reassembleMultipart rebuilds the body: the original first delimiter and
// preamble, the (possibly rewritten) children separated by delimiters, then
// the captured closing delimiter and epilogue.
func reassembleMultipart(frame multipartFrame, boundary string, parts [][]byte) []byte {
	var out bytes.Buffer
	out.Write(frame.prefix)
	for i, p := range parts {
		if i > 0 {
			out.WriteString("\r\n--" + boundary + crlf)
		}
		out.Write(p)
	}
	if len(parts) > 0 {
		// The CRLF before the closing delimiter is framing and was not part
		// of the last child's content.
		out.WriteString(crlf)
	}
	out.Write(frame.tail)
	return out.Bytes()
}

// readHeaders parses a raw header block; an empty block is a valid headerless
// entity, a malformed one is an error so the caller can fall back to the
// original message.
func readHeaders(block []byte) (textproto.MIMEHeader, error) {
	if len(bytes.TrimSpace(block)) == 0 {
		return textproto.MIMEHeader{}, nil
	}
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(block)))
	hdr, err := tp.ReadMIMEHeader()
	if err != nil && len(hdr) == 0 && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return hdr, nil
}

// isAttachment reports whether the part declares Content-Disposition:
// attachment.
func isAttachment(hdr textproto.MIMEHeader) bool {
	d := hdr.Get("Content-Disposition")
	if d == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(d)
	return err == nil && strings.ToLower(mt) == "attachment"
}

// withCharset returns a copy of params with charset set.
func withCharset(params map[string]string, charset string) map[string]string {
	out := make(map[string]string, len(params)+1)
	for k, v := range params {
		out[k] = v
	}
	out["charset"] = charset
	return out
}

// replaceHeader drops every line of the named header (with folded
// continuations) from the block and appends a fresh "Name: value" line.
func replaceHeader(block []byte, name, value string) []byte {
	out := make([]byte, 0, len(block)+len(name)+len(value)+4)
	skipping := false
	for _, line := range bytes.SplitAfter(block, []byte(crlf)) {
		if len(line) == 0 {
			continue
		}
		if skipping && isContinuation(line) {
			continue
		}
		skipping = false
		if isHeaderNamed(line, name) {
			skipping = true
			continue
		}
		out = append(out, line...)
	}
	return append(out, []byte(name+": "+value+crlf)...)
}

// isHeaderNamed reports whether line starts a header with the given field
// name, compared case-insensitively.
func isHeaderNamed(line []byte, name string) bool {
	colon := bytes.IndexByte(line, ':')
	if colon < 0 {
		return false
	}
	return bytes.EqualFold(bytes.TrimRight(line[:colon], " \t"), []byte(name))
}
