package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestServeDesktopAssetServesGzipVariant(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	assetPath := filepath.Join(dir, "assets", "index-abc.js")
	if err := os.WriteFile(assetPath, []byte("console.log('hello');"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	gzPath := assetPath + ".gz"
	if err := os.WriteFile(gzPath, []byte("gzipped-data"), 0o644); err != nil {
		t.Fatalf("write gz asset: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/index-abc.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	rec := httptest.NewRecorder()

	serveDesktopAsset(rec, req, dir, os.DirFS(dir), http.FileServer(http.Dir(dir)))

	resp := rec.Result()
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != desktopAssetImmutableCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, desktopAssetImmutableCacheControl)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "gzipped-data" {
		t.Fatalf("body = %q, want gzipped-data", string(body))
	}
}

func TestServeDesktopAssetFallsBackToIndexForRoutes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>shell</html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/some/app/route", nil)
	rec := httptest.NewRecorder()

	serveDesktopAsset(rec, req, dir, os.DirFS(dir), http.FileServer(http.Dir(dir)))

	resp := rec.Result()
	defer resp.Body.Close()
	if got := resp.Header.Get("Cache-Control"); got != desktopDocumentCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, desktopDocumentCacheControl)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "<html>shell</html>" {
		t.Fatalf("body = %q", string(body))
	}
}

func TestServeDesktopAssetSetsServiceWorkerScopeHeaders(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sw.js"), []byte("self.addEventListener('fetch', () => {})"), 0o644); err != nil {
		t.Fatalf("write service worker: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
	rec := httptest.NewRecorder()

	serveDesktopAsset(rec, req, dir, os.DirFS(dir), http.FileServer(http.Dir(dir)))

	resp := rec.Result()
	defer resp.Body.Close()
	if got := resp.Header.Get("Cache-Control"); got != desktopDocumentCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, desktopDocumentCacheControl)
	}
	if got := resp.Header.Get("Service-Worker-Allowed"); got != "/" {
		t.Fatalf("Service-Worker-Allowed = %q, want /", got)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/javascript; charset=utf-8", got)
	}
}

func TestServeDesktopAssetSetsManifestContentType(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.webmanifest"), []byte(`{"name":"Swarm"}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil)
	rec := httptest.NewRecorder()

	serveDesktopAsset(rec, req, dir, os.DirFS(dir), http.FileServer(http.Dir(dir)))

	resp := rec.Result()
	defer resp.Body.Close()
	if got := resp.Header.Get("Cache-Control"); got != desktopDocumentCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, desktopDocumentCacheControl)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/manifest+json" {
		t.Fatalf("Content-Type = %q, want application/manifest+json", got)
	}
}
