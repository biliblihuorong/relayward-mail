package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAdminAssets(t *testing.T) {
	_, ts := startAPI(t, nil)

	for path, want := range map[string][2]string{
		"/admin":           {"text/html", "app.js"},
		"/admin/app.js":    {"javascript", "loadStats"},
		"/admin/style.css": {"text/css", "token-box"},
	} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			content := string(body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if !strings.Contains(resp.Header.Get("Content-Type"), want[0]) {
				t.Errorf("Content-Type = %q, want %q", resp.Header.Get("Content-Type"), want[0])
			}
			if !strings.Contains(content, want[1]) {
				t.Errorf("content missing %q", want[1])
			}
			if resp.Header.Get("Content-Security-Policy") == "" {
				t.Error("security headers missing")
			}
		})
	}
}

func TestAdminUnknownAsset404(t *testing.T) {
	_, ts := startAPI(t, nil)
	resp, err := http.Get(ts.URL + "/admin/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
