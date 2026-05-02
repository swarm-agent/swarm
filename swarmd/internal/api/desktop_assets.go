package api

import (
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	desktopAssetImmutableCacheControl = "public, max-age=31536000, immutable"
	desktopDocumentCacheControl       = "no-cache"
)

func (s *Server) withDesktopAssets(next http.Handler) http.Handler {
	distDir := strings.TrimSpace(os.Getenv("SWARM_WEB_DIST_DIR"))
	if distDir == "" {
		return next
	}

	staticFS := os.DirFS(distDir)
	if _, err := fs.Stat(staticFS, "index.html"); err != nil {
		return next
	}

	fileServer := http.FileServer(http.Dir(distDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !shouldServeDesktopAsset(r) {
			next.ServeHTTP(w, r)
			return
		}
		serveDesktopAsset(w, r, distDir, staticFS, fileServer)
	})
}

func shouldServeDesktopAsset(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	requestPath := strings.TrimSpace(r.URL.Path)
	if requestPath == "" {
		return false
	}
	if strings.HasPrefix(requestPath, "/v1/") || strings.HasPrefix(requestPath, "/v2/") || strings.HasPrefix(requestPath, "/ws") {
		return false
	}
	switch requestPath {
	case "/healthz", "/readyz":
		return false
	default:
		return true
	}
}

func serveDesktopAsset(w http.ResponseWriter, r *http.Request, distDir string, staticFS fs.FS, fileServer http.Handler) {
	cleanPath := path.Clean("/" + strings.TrimSpace(r.URL.Path))
	if cleanPath == "/" {
		serveDesktopIndex(w, r, staticFS)
		return
	}

	relPath := strings.TrimPrefix(cleanPath, "/")
	if relPath == "" || relPath == "." {
		serveDesktopIndex(w, r, staticFS)
		return
	}

	fullPath := filepath.Join(distDir, filepath.FromSlash(relPath))
	if fileInfo, err := os.Stat(fullPath); err == nil && !fileInfo.IsDir() {
		setDesktopAssetHeaders(w, relPath)
		if contentType := desktopAssetContentType(relPath); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if shouldServeCompressedDesktopAsset(r, relPath) {
			compressedPath := fullPath + ".gz"
			if compressedInfo, err := os.Stat(compressedPath); err == nil && !compressedInfo.IsDir() {
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Add("Vary", "Accept-Encoding")
				http.ServeFile(w, r, compressedPath)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
		return
	}
	serveDesktopIndex(w, r, staticFS)
}

func serveDesktopIndex(w http.ResponseWriter, r *http.Request, staticFS fs.FS) {
	data, err := fs.ReadFile(staticFS, "index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("read desktop index: %w", err))
		return
	}
	w.Header().Set("Cache-Control", desktopDocumentCacheControl)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func setDesktopAssetHeaders(w http.ResponseWriter, relPath string) {
	if isRootServiceWorker(relPath) {
		w.Header().Set("Cache-Control", desktopDocumentCacheControl)
		w.Header().Set("Service-Worker-Allowed", "/")
		return
	}
	if isImmutableDesktopAsset(relPath) {
		w.Header().Set("Cache-Control", desktopAssetImmutableCacheControl)
		return
	}
	w.Header().Set("Cache-Control", desktopDocumentCacheControl)
}

func desktopAssetContentType(relPath string) string {
	if strings.EqualFold(filepath.Ext(relPath), ".webmanifest") {
		return "application/manifest+json"
	}
	return mime.TypeByExtension(filepath.Ext(relPath))
}

func shouldServeCompressedDesktopAsset(r *http.Request, relPath string) bool {
	if r == nil || !isCompressibleDesktopAsset(relPath) {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip")
}

func isCompressibleDesktopAsset(relPath string) bool {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".js", ".css", ".html", ".json", ".map", ".svg", ".txt":
		return true
	default:
		return false
	}
}

func isImmutableDesktopAsset(relPath string) bool {
	cleanPath := path.Clean("/" + strings.TrimSpace(relPath))
	return strings.HasPrefix(cleanPath, "/assets/")
}

func isRootServiceWorker(relPath string) bool {
	cleanPath := path.Clean("/" + strings.TrimSpace(relPath))
	return cleanPath == "/sw.js"
}
