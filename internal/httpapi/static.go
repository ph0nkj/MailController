package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/ph0nkj/mailcontroller/web"
)

const contentSecurityPolicy = "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; " +
	"form-action 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; " +
	"connect-src 'self'; font-src 'self'"

var webFiles = http.FileServer(http.FS(webui.FS))

func (a *API) serveWeb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if r.URL.Path == "/favicon.ico" {
		http.Redirect(w, r, "/favicon.svg", http.StatusMovedPermanently)
		return
	}

	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		a.serveIndex(w, r)
		return
	}
	if file, err := webui.FS.Open(name); err == nil {
		info, statErr := file.Stat()
		_ = file.Close()
		if statErr == nil && !info.IsDir() {
			a.serveAsset(w, r, name)
			return
		}
	}

	if path.Ext(name) == "" && acceptsHTML(r) {
		a.serveIndex(w, r)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "resource not found")
}

func (a *API) serveAsset(w http.ResponseWriter, r *http.Request, name string) {
	w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	if strings.HasSuffix(name, ".html") {
		setWebSecurityHeaders(w)
		w.Header().Set("Cache-Control", "no-store")
	}
	webFiles.ServeHTTP(w, r)
}

func (a *API) serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(webui.FS, "index.html")
	if err != nil {
		a.log.Error("read index.html", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "web UI unavailable")
		return
	}
	setWebSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(data); err != nil {
		a.log.Warn("write index.html", "error", err)
	}
}

func setWebSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

func acceptsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}
