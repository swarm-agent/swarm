package launcher

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
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

func TestInstallRuntimeFromArtifactCopiesReleaseLayout(t *testing.T) {
	artifactRoot := t.TempDir()
	platformRoot := filepath.Join(artifactRoot, runtime.GOOS+"-"+runtime.GOARCH)
	for _, dir := range []string{
		filepath.Join(platformRoot, "root"),
		filepath.Join(platformRoot, "swarmd"),
		filepath.Join(artifactRoot, "web", "assets"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for _, name := range []string{"swarm", "swarmdev", "rebuild", "swarmsetup", "swarmtui"} {
		path := filepath.Join(platformRoot, "root", name)
		if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	for _, name := range []string{"swarmd", "swarmctl"} {
		path := filepath.Join(platformRoot, "swarmd", name)
		if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	indexHTML := filepath.Join(artifactRoot, "web", "index.html")
	if err := os.WriteFile(indexHTML, []byte("<!doctype html><html></html>"), 0o644); err != nil {
		t.Fatalf("write %s: %v", indexHTML, err)
	}
	assetPath := filepath.Join(artifactRoot, "web", "assets", "app.js")
	if err := os.WriteFile(assetPath, []byte("console.log('artifact');"), 0o644); err != nil {
		t.Fatalf("write %s: %v", assetPath, err)
	}

	xdgRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdgRoot, "data"))
	t.Setenv("XDG_BIN_HOME", filepath.Join(xdgRoot, "bin"))

	fffLib := filepath.Join(platformRoot, "swarmd", "libfff_c.so")
	if err := os.WriteFile(fffLib, []byte("fff"), 0o644); err != nil {
		t.Fatalf("write %s: %v", fffLib, err)
	}

	report, err := InstallRuntimeFromArtifact(artifactRoot)
	if err != nil {
		t.Fatalf("InstallRuntimeFromArtifact: %v", err)
	}

	for _, rel := range []string{
		filepath.Join("swarm", "libexec", "swarm"),
		filepath.Join("swarm", "libexec", "swarmdev"),
		filepath.Join("swarm", "libexec", "rebuild"),
		filepath.Join("swarm", "libexec", "swarmsetup"),
		filepath.Join("swarm", "bin", "swarmtui"),
		filepath.Join("swarm", "bin", "swarmd"),
		filepath.Join("swarm", "bin", "swarmctl"),
		filepath.Join("swarm", "lib", "libfff_c.so"),
		filepath.Join("swarm", "share", "index.html"),
		filepath.Join("swarm", "share", "assets", "app.js"),
		filepath.Join("swarm", "share", "assets", "app.js.gz"),
	} {
		path := filepath.Join(xdgRoot, "data", rel)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
	linkPath := filepath.Join(report.BinHome, "swarm")
	targetPath, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink %s: %v", linkPath, err)
	}
	if targetPath != filepath.Join(xdgRoot, "data", "swarm", "libexec", "swarm") {
		t.Fatalf("swarm link = %q, want %q", targetPath, filepath.Join(xdgRoot, "data", "swarm", "libexec", "swarm"))
	}
}
