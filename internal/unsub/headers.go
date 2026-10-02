package unsub

import (
	"bytes"
	"errors"
	"strings"
)

const (
	crlf     = "\r\n"
	crlfcrlf = "\r\n\r\n"
	bccName  = "bcc"
)

// AddUnsubscribeHeaders inserts, at the end of the message header block
// (i.e. immediately before the first "\r\n\r\n"), these two headers:
//
//	List-Unsubscribe: <LINK>
//	List-Unsubscribe-Post: List-Unsubscribe=One-Click
//
// where LINK is the full unsubscribe URL. Returns (nil, err) without
// modifying anything when link is empty or contains CR or LF. If data
// contains no "\r\n\r\n" separator it is treated as a header-only message:
// "\r\n" is appended to data unless it already ends with one, then the two
// header lines are appended. The input slice is never modified; a new slice
// is returned on success.
func AddUnsubscribeHeaders(data []byte, link string) ([]byte, error) {
	if link == "" || strings.ContainsAny(link, "\r\n") {
		return nil, errors.New("unsub: link must be non-empty and must not contain CR or LF")
	}
	lines := "List-Unsubscribe: <" + link + ">" + crlf +
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click"

	// Separator present: splice the new lines in at the end of the header
	// block. data[:i] holds the headers without the final header line's CRLF
	// (it is the first half of the separator), so it is re-attached
	// explicitly; data[i:] then starts with "\r\n\r\n", which terminates the
	// List-Unsubscribe-Post line and provides the blank separator line, and
	// the body is copied through untouched.
	if i := bytes.Index(data, []byte(crlfcrlf)); i >= 0 {
		out := make([]byte, 0, len(data)+len(lines)+len(crlf))
		out = append(out, data[:i]...)
		out = append(out, crlf...)
		out = append(out, lines...)
		out = append(out, data[i:]...)
		return out, nil
	}

	// Header-only message. Each appended line ends with CRLF.
	out := make([]byte, 0, len(data)+len(lines)+2*len(crlf))
	out = append(out, data...)
	if !bytes.HasSuffix(out, []byte(crlf)) {
		out = append(out, crlf...)
	}
	out = append(out, lines...)
	out = append(out, crlf...)
	return out, nil
}

// RemoveBcc returns a copy of data with the Bcc header removed from the
// header block, including any folded continuation lines (lines starting with
// space or tab). Header names compare case-insensitively. The header block is
// everything up to the first "\r\n\r\n"; if there is no separator the whole
// input is treated as a header block. All other bytes — remaining header
// order, the separator, and the body — are preserved exactly.
func RemoveBcc(data []byte) []byte {
	// The header region is the header block plus one CRLF: when a separator
	// exists, its first CRLF terminates the last header line and belongs to
	// it, so a Bcc that ends the header block loses its line terminator too.
	// The tail is the blank line (the separator's second CRLF) plus body.
	block, tail := splitHeaderBlock(data)

	out := make([]byte, 0, len(block))
	skipping := false
	for _, line := range bytes.SplitAfter(block, []byte(crlf)) {
		if skipping && isContinuation(line) {
			continue
		}
		skipping = false
		if isBccHeader(line) {
			skipping = true
			continue
		}
		out = append(out, line...)
	}
	return append(out, tail...)
}

// splitHeaderBlock splits data into the header region (every header line,
// each ending with its CRLF) and the remainder (the blank separator line plus
// body, if any). A message without a separator is entirely header region.
func splitHeaderBlock(data []byte) (block, tail []byte) {
	if i := bytes.Index(data, []byte(crlfcrlf)); i >= 0 {
		return data[:i+len(crlf)], data[i+len(crlf):]
	}
	return data, nil
}

// isContinuation reports whether line is a folded continuation line, i.e.
// starts with a space or tab.
func isContinuation(line []byte) bool {
	return len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
}

// isBccHeader reports whether line starts a header whose field name is Bcc,
// compared case-insensitively (trailing whitespace before the colon is
// tolerated).
func isBccHeader(line []byte) bool {
	colon := bytes.IndexByte(line, ':')
	if colon < 0 {
		return false
	}
	name := bytes.TrimRight(line[:colon], " \t")
	return bytes.EqualFold(name, []byte(bccName))
}
