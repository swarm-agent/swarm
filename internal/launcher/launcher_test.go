package launcher

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCompressedDesktopAssetsCreatesGzipFiles(t *testing.T) {
	dir := t.TempDir()
	assetsDir := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	assetPath := filepath.Join(assetsDir, "index-abc.js")
	original := []byte("console.log('speed');")
	if err := os.WriteFile(assetPath, original, 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	if err := writeCompressedDesktopAssets(dir); err != nil {
		t.Fatalf("writeCompressedDesktopAssets: %v", err)
	}

	compressedPath := assetPath + ".gz"
	file, err := os.Open(compressedPath)
	if err != nil {
		t.Fatalf("open compressed asset: %v", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	decoded, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if string(decoded) != string(original) {
		t.Fatalf("decoded = %q, want %q", string(decoded), string(original))
	}
}
