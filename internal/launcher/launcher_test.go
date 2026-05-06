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

func TestDevFrontendAssetsNeedRebuildDetectsSourceChanges(t *testing.T) {
	webDir := t.TempDir()
	webDistDir := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(filepath.Join(webDir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<div id=\"root\"></div>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "package.json"), []byte(`{"scripts":{"build":"vite build"}}`), 0o644); err != nil {
		t.Fatalf("write package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "src", "main.tsx"), []byte("console.log('one')"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.MkdirAll(webDistDir, 0o755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDistDir, "index.html"), []byte("built"), 0o644); err != nil {
		t.Fatalf("write dist index: %v", err)
	}
	fingerprint, err := frontendSourceFingerprint(webDir)
	if err != nil {
		t.Fatalf("frontendSourceFingerprint: %v", err)
	}
	if err := writeFrontendSourceFingerprint(webDistDir, fingerprint); err != nil {
		t.Fatalf("writeFrontendSourceFingerprint: %v", err)
	}

	profile := Profile{WebDir: webDir, WebDistDir: webDistDir}
	needs, err := DevFrontendAssetsNeedRebuild(profile)
	if err != nil {
		t.Fatalf("DevFrontendAssetsNeedRebuild: %v", err)
	}
	if needs {
		t.Fatal("DevFrontendAssetsNeedRebuild = true before source change")
	}

	if err := os.WriteFile(filepath.Join(webDir, "src", "main.tsx"), []byte("console.log('two')"), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	needs, err = DevFrontendAssetsNeedRebuild(profile)
	if err != nil {
		t.Fatalf("DevFrontendAssetsNeedRebuild after source change: %v", err)
	}
	if !needs {
		t.Fatal("DevFrontendAssetsNeedRebuild = false after source change")
	}
}

func TestEnvMapIncludesInstalledLibDirInLDLibraryPath(t *testing.T) {
	xdgRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdgRoot, "data"))
	t.Setenv("LD_LIBRARY_PATH", "/opt/existing/lib")

	profile, err := LoadRuntimeProfile("main", nil)
	if err != nil {
		t.Fatalf("LoadRuntimeProfile: %v", err)
	}

	got := profile.EnvMap()["LD_LIBRARY_PATH"]
	want := filepath.Join(xdgRoot, "data", "swarm", "lib") + string(os.PathListSeparator) + "/opt/existing/lib"
	if got != want {
		t.Fatalf("LD_LIBRARY_PATH = %q, want %q", got, want)
	}
}
