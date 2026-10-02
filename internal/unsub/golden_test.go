package unsub

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenLink and goldenFooter are the fixed inputs baked into the expected
// files.
const (
	goldenLink   = "https://mail.example.com/u/abc123_tok"
	goldenFooter = "不想再收到来自 Gitea 的邮件？退订"
)

var update = flag.Bool("update", false, "rewrite golden files")

// TestGoldenSamples compares InjectFooter's output on the testdata samples
// with the checked-in .expected.em files. sample3 (multipart/signed) must
// come out byte-identical to its input.
func TestGoldenSamples(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "sample*.em"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 3 {
		t.Fatalf("only %d golden samples, want at least 3", len(paths))
	}
	for _, path := range paths {
		if strings.HasSuffix(path, ".expected.em") {
			continue
		}
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path) // #nosec G304 -- fixed testdata directory
			if err != nil {
				t.Fatal(err)
			}

			got, res, err := InjectFooter(data, goldenLink, goldenFooter)
			if err != nil {
				t.Fatalf("InjectFooter: %v", err)
			}
			if !res.Injected {
				t.Logf("%s: injection skipped (signed sample?)", filepath.Base(path))
			}

			wantPath := path[:len(path)-len(".em")] + ".expected.em"
			if *update {
				// #nosec G306 -- golden output file, not a secret
				if err := os.WriteFile(wantPath, got, 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(wantPath) // #nosec G304 -- fixed testdata directory
			if err != nil {
				t.Fatalf("read golden: %v (run go test -update to create it)", err)
			}
			if !bytes.Equal(got, want) {
				i := firstDiff(want, got)
				t.Fatalf("output differs from golden at byte %d:\n--- want ---\n%s\n--- got ---\n%s",
					i, preview(want, i), preview(got, i))
			}
		})
	}
}

func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}

// preview prints a byte slice around the given offset with visible CRLF.
func preview(b []byte, at int) string {
	lo := max(0, at-60)
	hi := min(len(b), at+120)
	chunk := bytes.ReplaceAll(b[lo:hi], []byte("\r\n"), []byte("⏎\n"))
	if hi < len(b) {
		chunk = append(chunk, []byte(fmt.Sprintf("\n…(%d more bytes)", len(b)-hi))...)
	}
	return string(chunk)
}
