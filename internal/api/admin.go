package api

import (
	"bytes"
	"embed"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
)

// The M5 admin page is a single embedded asset bundle that talks to the
// management API only — no new endpoints, no external resources (the CSP
// stays default-src 'self').
//
//go:embed admin/admin.html admin/app.js admin/style.css
var adminFS embed.FS

// adminAssets maps the request paths under /admin to embedded files.
var adminAssets = map[string]string{
	"/admin":           "admin/admin.html",
	"/admin/app.js":    "admin/app.js",
	"/admin/style.css": "admin/style.css",
}

// handleAdmin serves the embedded management page and its assets.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	file, ok := adminAssets[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := adminFS.ReadFile(file)
	if err != nil {
		s.logger.Error("read embedded admin asset", slog.String("file", file), slog.String("err", err.Error()))
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	if ct := mime.TypeByExtension(path.Ext(file)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// Assets are compiled into the binary: after an upgrade the browser must
	// not keep serving a stale app.js, so nothing is cacheable without
	// revalidation. The HTML itself is never stored at all.
	if strings.HasSuffix(file, ".html") {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	_, _ = io.Copy(w, bytes.NewReader(data))
}
