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
	t.Setenv("SWARM_STARTUP_CONFIG", filepath.Join(t.TempDir(), "swarm.conf"))
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))
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

func TestLoadRuntimeProfileUsesDaemonStorageRoots(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "state")
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	runtimeRoot := filepath.Join(t.TempDir(), "run")
	logRoot := filepath.Join(t.TempDir(), "logs")
	configRoot := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	t.Setenv("SWARM_STARTUP_CONFIG", filepath.Join(t.TempDir(), "swarm.conf"))
	t.Setenv("STATE_DIRECTORY", dataRoot)
	t.Setenv("CACHE_DIRECTORY", cacheRoot)
	t.Setenv("RUNTIME_DIRECTORY", runtimeRoot)
	t.Setenv("LOGS_DIRECTORY", logRoot)
	t.Setenv("CONFIGURATION_DIRECTORY", configRoot)

	profile, err := LoadRuntimeProfile("dev", nil)
	if err != nil {
		t.Fatalf("LoadRuntimeProfile: %v", err)
	}

	if profile.DataDir != filepath.Join(dataRoot, "dev") {
		t.Fatalf("DataDir = %q", profile.DataDir)
	}
	if profile.CacheDir != filepath.Join(cacheRoot, "dev") {
		t.Fatalf("CacheDir = %q", profile.CacheDir)
	}
	if profile.StateRoot != filepath.Join(runtimeRoot, "dev") {
		t.Fatalf("StateRoot = %q", profile.StateRoot)
	}
	if profile.LogFile != filepath.Join(logRoot, "dev", "swarmd.log") {
		t.Fatalf("LogFile = %q", profile.LogFile)
	}
	if profile.ConfigDir != filepath.Join(configRoot, "dev") {
		t.Fatalf("ConfigDir = %q", profile.ConfigDir)
	}
	if profile.EnvMap()["SWARM_STARTUP_CONFIG"] != profile.Startup.Path {
		t.Fatalf("SWARM_STARTUP_CONFIG = %q, want %q", profile.EnvMap()["SWARM_STARTUP_CONFIG"], profile.Startup.Path)
	}
	if profile.EnvMap()["STATE_DIRECTORY"] != profile.DataDir {
		t.Fatalf("STATE_DIRECTORY = %q, want %q", profile.EnvMap()["STATE_DIRECTORY"], profile.DataDir)
	}
	if profile.EnvMap()["RUNTIME_DIRECTORY"] != profile.StateRoot {
		t.Fatalf("RUNTIME_DIRECTORY = %q, want %q", profile.EnvMap()["RUNTIME_DIRECTORY"], profile.StateRoot)
	}
}

func TestLoadRuntimeProfileRejectsHomeDerivedStartupConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	t.Setenv("SWARM_STARTUP_CONFIG", filepath.Join(home, ".config", "swarm", "swarm.conf"))
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))

	if _, err := LoadRuntimeProfile("main", nil); err == nil {
		t.Fatal("LoadRuntimeProfile accepted HOME-derived SWARM_STARTUP_CONFIG")
	}
}

func TestLoadRuntimeProfileRejectsHomeDerivedDaemonRoots(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	t.Setenv("SWARM_STARTUP_CONFIG", filepath.Join(t.TempDir(), "swarm.conf"))
	t.Setenv("STATE_DIRECTORY", filepath.Join(home, ".local", "share", "swarmd"))
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))

	if _, err := LoadRuntimeProfile("main", nil); err == nil {
		t.Fatal("LoadRuntimeProfile accepted HOME-derived STATE_DIRECTORY")
	}
}
