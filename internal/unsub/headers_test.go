package unsub

import (
	"bytes"
	"strings"
	"testing"
)

const (
	typicalMsg = "From: a@b.c\r\nTo: d@e.f\r\nSubject: hi\r\n\r\nBody line 1\r\nBody line 2\r\n"
	typicalHdr = "From: a@b.c\r\nTo: d@e.f\r\nSubject: hi\r\n"
	typicalBdy = "Body line 1\r\nBody line 2\r\n"
	link       = "https://relay.example/unsub?tok=abc_123-DEF.456"
)

func TestAddUnsubscribeHeadersTypicalMessage(t *testing.T) {
	data := []byte(typicalMsg)
	want := typicalHdr +
		"List-Unsubscribe: <" + link + ">\r\n" +
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n" +
		"\r\n" + typicalBdy

	got, err := AddUnsubscribeHeaders(data, link)
	if err != nil {
		t.Fatalf("AddUnsubscribeHeaders failed: %v", err)
	}
	if string(got) != want {
		t.Fatalf("output mismatch:\n got %q\nwant %q", got, want)
	}

	// Original headers and body preserved byte-for-byte.
	if !bytes.HasPrefix(got, []byte(typicalHdr)) {
		t.Errorf("original headers were not preserved at the start of the output")
	}
	if !bytes.HasSuffix(got, []byte(typicalBdy)) {
		t.Errorf("body was not preserved at the end of the output")
	}

	// The new headers sit at the END of the header block, i.e. after the
	// existing headers and before the single blank line that precedes the
	// body.
	endOfHdrs := len(typicalHdr)
	newLines := got[endOfHdrs : len(got)-len(crlf)-len(typicalBdy)]
	if !bytes.HasSuffix(newLines, []byte(crlf)) || bytes.Contains(newLines, []byte(crlfcrlf)) {
		t.Errorf("inserted headers not contiguous: %q", newLines)
	}
}

func TestAddUnsubscribeHeadersHeaderOnly(t *testing.T) {
	twoLines := "List-Unsubscribe: <" + link + ">\r\n" +
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n"

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"header-only ending with CRLF", "To: a@b.c\r\n", "To: a@b.c\r\n" + twoLines},
		{"header-only without trailing CRLF", "To: a@b.c", "To: a@b.c\r\n" + twoLines},
		// Per the contract: empty data does not end with CRLF, so one is
		// appended before the two header lines.
		{"empty data", "", "\r\n" + twoLines},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AddUnsubscribeHeaders([]byte(tc.in), link)
			if err != nil {
				t.Fatalf("AddUnsubscribeHeaders failed: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAddUnsubscribeHeadersLinkPreserved(t *testing.T) {
	got, err := AddUnsubscribeHeaders([]byte(typicalMsg), link)
	if err != nil {
		t.Fatalf("AddUnsubscribeHeaders failed: %v", err)
	}
	const marker = "List-Unsubscribe: <"
	start := strings.Index(string(got), marker)
	if start < 0 {
		t.Fatalf("no List-Unsubscribe header in output:\n%s", got)
	}
	rest := got[start+len(marker):]
	inner := rest[:strings.Index(string(rest), ">")]
	if string(inner) != link {
		t.Errorf("link inside angle brackets = %q, want %q", inner, link)
	}
}

func TestAddUnsubscribeHeadersBadLink(t *testing.T) {
	data := []byte(typicalMsg)
	before := append([]byte(nil), data...)

	tests := []struct {
		name string
		link string
	}{
		{"empty link", ""},
		{"link with CR", "https://x/unsub\r\nX-Evil: 1"},
		{"link with LF", "https://x/unsub\nBcc: a@b.c"},
		{"lone CR", "https://x/\rsub"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AddUnsubscribeHeaders(data, tc.link)
			if err == nil {
				t.Fatalf("AddUnsubscribeHeaders(link=%q) = nil error, want error", tc.link)
			}
			if got != nil {
				t.Errorf("result = %q, want nil", got)
			}
			if !bytes.Equal(data, before) {
				t.Error("input data was modified")
			}
		})
	}
}

func TestRemoveBcc(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "simple bcc removed, rest byte-identical",
			in:   "From: a@b.c\r\nBcc: x@y.z\r\nTo: d@e.f\r\n\r\nbody\r\n",
			want: "From: a@b.c\r\nTo: d@e.f\r\n\r\nbody\r\n",
		},
		{
			name: "folded bcc removes both physical lines",
			in:   "From: a@b.c\r\nBcc: a@b.c,\r\n\td@e.f\r\nTo: d@e.f\r\n\r\nbody",
			want: "From: a@b.c\r\nTo: d@e.f\r\n\r\nbody",
		},
		{
			name: "folded bcc with space continuation",
			in:   "Bcc: a@b.c,\r\n d@e.f\r\nTo: x@y.z\r\n\r\nbody",
			want: "To: x@y.z\r\n\r\nbody",
		},
		{
			name: "lowercase bcc removed",
			in:   "bcc: x@y.z\r\nTo: d@e.f\r\n\r\nbody",
			want: "To: d@e.f\r\n\r\nbody",
		},
		{
			name: "uppercase BCC removed",
			in:   "BCC: x@y.z\r\nTo: d@e.f\r\n\r\nbody",
			want: "To: d@e.f\r\n\r\nbody",
		},
		{
			name: "no bcc: identity",
			in:   "From: a@b.c\r\nTo: d@e.f\r\n\r\nbody\r\n",
			want: "From: a@b.c\r\nTo: d@e.f\r\n\r\nbody\r\n",
		},
		{
			name: "bcc in body untouched",
			in:   "From: a@b.c\r\n\r\nBcc: fake@x.y\r\nmore body\r\n",
			want: "From: a@b.c\r\n\r\nBcc: fake@x.y\r\nmore body\r\n",
		},
		{
			name: "similar header name kept",
			in:   "X-Bcc: keep\r\nBcc: drop\r\nBccy: keep\r\n\r\nbody",
			want: "X-Bcc: keep\r\nBccy: keep\r\n\r\nbody",
		},
		{
			name: "header-only without separator",
			in:   "Bcc: a@b.c\r\nTo: d@e.f",
			want: "To: d@e.f",
		},
		{
			name: "bcc as only header",
			in:   "Bcc: a@b.c\r\n\r\nbody",
			want: "\r\nbody",
		},
		{
			name: "remaining header order preserved",
			in:   "A: 1\r\nBcc: x@y.z\r\nC: 2\r\nD: 3\r\n\r\nb",
			want: "A: 1\r\nC: 2\r\nD: 3\r\n\r\nb",
		},
		{
			name: "bcc as last header before separator",
			in:   "To: d@e.f\r\nBcc: x@y.z\r\n\r\nbody",
			want: "To: d@e.f\r\n\r\nbody",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RemoveBcc([]byte(tc.in))
			if string(got) != tc.want {
				t.Errorf("RemoveBcc:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestRemoveBccReturnsCopy(t *testing.T) {
	in := []byte(typicalMsg)
	before := append([]byte(nil), in...)
	got := RemoveBcc(in)
	if !bytes.Equal(in, before) {
		t.Error("input data was modified")
	}
	if !bytes.Equal(got, in) {
		t.Errorf("RemoveBcc without Bcc changed the message:\n got %q\nwant %q", got, in)
	}
	if len(got) > 0 && &got[0] == &in[0] {
		t.Error("RemoveBcc returned the input slice instead of a copy")
	}
}
