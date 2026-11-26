package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/ph0nkj/mailcontroller/web"
)

const contentSecurityPolicy = "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; " +
	"form-action 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; " +
	"connect-src 'self'; font-src 'self'"

var webFiles = http.FileServer(http.FS(webui.FS))

// assetETagCache stores content hashes of embedded assets. The assets are
// immutable for the lifetime of the process, so each file is hashed once.
var assetETagCache sync.Map

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
		http.Redirect(w, r, "/img/logo.png", http.StatusMovedPermanently)
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
	w.Header().Set("Cache-Control", "public, max-age=3600, must-revalidate")
	if strings.HasSuffix(name, ".html") {
		setWebSecurityHeaders(w)
		w.Header().Set("Cache-Control", "no-store")
		webFiles.ServeHTTP(w, r)
		return
	}
	if etag := assetETag(name); etag != "" {
		w.Header().Set("ETag", etag)
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	webFiles.ServeHTTP(w, r)
}

// assetETag returns a weak validator for one embedded asset. A weak ETag is
// used because the response may be served either identity-encoded or gzipped.
func assetETag(name string) string {
	if cached, ok := assetETagCache.Load(name); ok {
		return cached.(string)
	}
	data, err := fs.ReadFile(webui.FS, name)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	tag := `W/"` + hex.EncodeToString(sum[:8]) + `"`
	assetETagCache.Store(name, tag)
	return tag
}

func etagMatches(header, tag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		switch {
		case candidate == "":
			continue
		case candidate == "*":
			return true
		case strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(tag, "W/"):
			return true
		}
	}
	return false
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
