package launcher

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/pkg/localupdate"
)

func writeRuntimeArtifact(t *testing.T, artifactRoot, version string, omit map[string]bool) {
	t.Helper()
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
		if omit[name] {
			continue
		}
		path := filepath.Join(platformRoot, "root", name)
		if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	for _, name := range []string{"swarmd", "swarmctl", "swarm-fff-search"} {
		if omit[name] {
			continue
		}
		path := filepath.Join(platformRoot, "swarmd", name)
		if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if !omit["libfff_c.so"] {
		if err := os.WriteFile(filepath.Join(platformRoot, "swarmd", "libfff_c.so"), []byte("fff"), 0o644); err != nil {
			t.Fatalf("write libfff: %v", err)
		}
	}
	if !omit["web"] {
		if err := os.WriteFile(filepath.Join(artifactRoot, "web", "index.html"), []byte("<!doctype html><html></html>"), 0o644); err != nil {
			t.Fatalf("write index.html: %v", err)
		}
		if err := os.WriteFile(filepath.Join(artifactRoot, "web", "assets", "app.js"), []byte("console.log('artifact');"), 0o644); err != nil {
			t.Fatalf("write app.js: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "build-info.txt"), []byte("version="+version+"\ncommit=test\n"), 0o644); err != nil {
		t.Fatalf("write build-info: %v", err)
	}
}

func TestValidateUpdateDownloadURLRejectsUntrustedOrInsecureOrigins(t *testing.T) {
	for _, raw := range []string{
		"http://github.com/swarm-agent/swarm/releases/download/v1.2.3/a.tar.gz",
		"https://example.com/a.tar.gz",
		"https://github.com.evil.example/a.tar.gz",
	} {
		if err := validateUpdateDownloadURL(raw); err == nil {
			t.Fatalf("validateUpdateDownloadURL(%q) succeeded", raw)
		}
	}
	if err := validateUpdateDownloadURL("https://release-assets.githubusercontent.com/github-production-release-asset/file"); err != nil {
		t.Fatalf("legitimate GitHub release asset URL rejected: %v", err)
	}
}

func TestExtractTarGzRejectsOversizedFileBeforeWriting(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "release.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "swarm-v1.2.3/huge", Mode: 0o644, Size: maxUpdateFileBytes + 1}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := extractTarGz(archivePath, t.TempDir()); err == nil || !strings.Contains(err.Error(), "extraction limits") {
		t.Fatalf("extractTarGz error = %v, want extraction limits", err)
	}
}

func TestRequirePathWithinRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	if err := requirePathWithin(root, filepath.Join(root, "versions", "v1.2.3")); err != nil {
		t.Fatalf("contained path rejected: %v", err)
	}
	if err := requirePathWithin(root, filepath.Join(root, "..", "escape")); err == nil {
		t.Fatal("escaping path accepted")
	}
}

func TestInstallRuntimeFromArtifactUsesVersionedCurrentLayout(t *testing.T) {
	artifactRoot := t.TempDir()
	const version = "v1.2.3"
	writeRuntimeArtifact(t, artifactRoot, version, nil)

	systemRoot := t.TempDir()
	installRoot := filepath.Join(systemRoot, "share", "swarm")
	binRoot := filepath.Join(systemRoot, "bin")
	t.Setenv("SWARM_SYSTEM_INSTALL_ROOT", installRoot)
	t.Setenv("SWARM_SYSTEM_BIN_DIR", binRoot)
	t.Setenv("SWARM_SYSTEM_BINARY_DIR", filepath.Join(installRoot, "bin"))
	t.Setenv("SWARM_SYSTEM_LIBEXEC_DIR", filepath.Join(installRoot, "libexec"))
	t.Setenv("SWARM_SYSTEM_LIB_DIR", filepath.Join(installRoot, "lib"))
	t.Setenv("SWARM_SYSTEM_SHARE_DIR", filepath.Join(installRoot, "share"))

	report, err := InstallRuntimeFromArtifact(artifactRoot)
	if err != nil {
		t.Fatalf("InstallRuntimeFromArtifact: %v", err)
	}

	versionRoot := filepath.Join(installRoot, "versions", version)
	for _, rel := range []string{
		filepath.Join("libexec", "swarm"),
		filepath.Join("libexec", "swarmdev"),
		filepath.Join("libexec", "rebuild"),
		filepath.Join("libexec", "swarmsetup"),
		filepath.Join("bin", "swarmtui"),
		filepath.Join("bin", "swarmd"),
		filepath.Join("bin", "swarmctl"),
		filepath.Join("bin", "swarm-fff-search"),
		filepath.Join("lib", "libfff_c.so"),
		filepath.Join("share", "index.html"),
		filepath.Join("share", "assets", "app.js"),
		filepath.Join("share", "assets", "app.js.gz"),
		"build-info.txt",
		".version",
	} {
		path := filepath.Join(versionRoot, rel)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
	currentTarget, err := os.Readlink(filepath.Join(installRoot, "current"))
	if err != nil {
		t.Fatalf("read current symlink: %v", err)
	}
	if filepath.Clean(currentTarget) != filepath.Clean(versionRoot) {
		t.Fatalf("current -> %q, want %q", currentTarget, versionRoot)
	}
	linkPath := filepath.Join(report.BinHome, "swarm")
	targetPath, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink %s: %v", linkPath, err)
	}
	wantLauncher := filepath.Join(installRoot, "libexec", "swarm")
	if filepath.Clean(targetPath) != filepath.Clean(wantLauncher) {
		t.Fatalf("swarm link = %q, want %q", targetPath, wantLauncher)
	}
	if got := CurrentRuntimeVersion(installRoot); got != version {
		t.Fatalf("CurrentRuntimeVersion = %q, want %q", got, version)
	}
}

func TestInstallRuntimeFromArtifactDoesNotBreakCurrentRuntimeOnIncompleteArtifact(t *testing.T) {
	systemRoot := t.TempDir()
	installRoot := filepath.Join(systemRoot, "share", "swarm")
	t.Setenv("SWARM_SYSTEM_INSTALL_ROOT", installRoot)
	t.Setenv("SWARM_SYSTEM_BIN_DIR", filepath.Join(systemRoot, "bin"))
	t.Setenv("SWARM_SYSTEM_BINARY_DIR", filepath.Join(installRoot, "bin"))
	t.Setenv("SWARM_SYSTEM_LIBEXEC_DIR", filepath.Join(installRoot, "libexec"))
	t.Setenv("SWARM_SYSTEM_LIB_DIR", filepath.Join(installRoot, "lib"))
	t.Setenv("SWARM_SYSTEM_SHARE_DIR", filepath.Join(installRoot, "share"))

	const version = "v1.2.3"
	goodArtifact := t.TempDir()
	writeRuntimeArtifact(t, goodArtifact, version, nil)
	report, err := InstallRuntimeFromArtifact(goodArtifact)
	if err != nil {
		t.Fatalf("initial InstallRuntimeFromArtifact: %v", err)
	}
	wantCurrent, ok := resolveRuntimeLink(filepath.Join(installRoot, "current"))
	if !ok {
		t.Fatalf("current runtime link missing after initial install")
	}
	launcherTarget, err := os.Readlink(filepath.Join(report.BinHome, "swarm"))
	if err != nil {
		t.Fatalf("read swarm launcher link: %v", err)
	}

	brokenArtifact := t.TempDir()
	writeRuntimeArtifact(t, brokenArtifact, version, map[string]bool{"swarmd": true})
	if _, err := InstallRuntimeFromArtifact(brokenArtifact); err == nil {
		t.Fatalf("InstallRuntimeFromArtifact should reject incomplete artifact")
	}

	current, ok := resolveRuntimeLink(filepath.Join(installRoot, "current"))
	if !ok || current != wantCurrent {
		t.Fatalf("current after failed install = %q ok=%v, want %q", current, ok, wantCurrent)
	}
	if got := CurrentRuntimeVersion(installRoot); got != version {
		t.Fatalf("CurrentRuntimeVersion after failed install = %q, want %q", got, version)
	}
	if !isExecutable(launcherTarget) {
		t.Fatalf("existing launcher target should remain executable after failed install: %s", launcherTarget)
	}
}

func TestInstalledVersionMatchesRejectsIncompleteRuntime(t *testing.T) {
	targetRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(targetRoot, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, "bin", "swarmtui"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write swarmtui: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, "build-info.txt"), []byte("version=v1.2.3\n"), 0o644); err != nil {
		t.Fatalf("write build-info: %v", err)
	}
	if installedVersionMatches(targetRoot, "v1.2.3") {
		t.Fatalf("installedVersionMatches accepted an incomplete runtime")
	}
}

func TestRunGoBuildWithArgsPreservesExistingBinaryOnFailure(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	goBin, err := FindGoBin(repoRoot)
	if err != nil {
		t.Fatalf("FindGoBin: %v", err)
	}
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "go.mod"), []byte("module example.com/broken\n\ngo 1.20\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	outPath := filepath.Join(t.TempDir(), "swarm")
	const original = "old-binary\n"
	if err := os.WriteFile(outPath, []byte(original), 0o755); err != nil {
		t.Fatalf("write existing binary: %v", err)
	}
	if err := runGoBuildWithArgs(projectRoot, projectRoot, goBin, outPath, "./missing"); err == nil {
		t.Fatalf("runGoBuildWithArgs should fail for missing package")
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read existing binary: %v", err)
	}
	if string(data) != original {
		t.Fatalf("existing binary changed after failed build: %q", string(data))
	}
	if !isExecutable(outPath) {
		t.Fatalf("existing binary lost executable bit")
	}
}

func TestCopyFileReplacesReadOnlyExistingTargetWhenDirectoryIsWritable(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source")
	targetPath := filepath.Join(root, "target")
	if err := os.WriteFile(sourcePath, []byte("new"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("old"), 0o444); err != nil {
		t.Fatalf("write target: %v", err)
	}

	if err := copyFile(sourcePath, targetPath); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "new" {
		t.Fatalf("target content = %q, want new", string(data))
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("target mode = %04o, want 0644", got)
	}
}

func TestSwitchRuntimeLinksReplacesLegacyTopLevelRuntimeDirs(t *testing.T) {
	installRoot := t.TempDir()
	targetRoot := filepath.Join(installRoot, "versions", "v1.2.3")
	for _, dir := range []string{"bin", "libexec", "lib", "share"} {
		if err := os.MkdirAll(filepath.Join(targetRoot, dir), 0o755); err != nil {
			t.Fatalf("mkdir target %s: %v", dir, err)
		}
		legacyDir := filepath.Join(installRoot, dir)
		if err := os.MkdirAll(legacyDir, 0o755); err != nil {
			t.Fatalf("mkdir legacy %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(legacyDir, "legacy.txt"), []byte("old layout"), 0o644); err != nil {
			t.Fatalf("write legacy %s: %v", dir, err)
		}
	}

	if err := switchRuntimeLinks(installRoot, targetRoot); err != nil {
		t.Fatalf("switchRuntimeLinks: %v", err)
	}

	currentRoot, ok := resolveRuntimeLink(filepath.Join(installRoot, "current"))
	if !ok || currentRoot != targetRoot {
		t.Fatalf("current = %q ok=%v, want %q", currentRoot, ok, targetRoot)
	}
	for _, dir := range []string{"bin", "libexec", "lib", "share"} {
		linkPath := filepath.Join(installRoot, dir)
		info, err := os.Lstat(linkPath)
		if err != nil {
			t.Fatalf("lstat %s: %v", linkPath, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s should be a symlink after switching runtime links; mode=%s", linkPath, info.Mode())
		}
		target, ok := resolveRuntimeLink(linkPath)
		want := filepath.Join(installRoot, "current", dir)
		if !ok || target != want {
			t.Fatalf("%s = %q ok=%v, want %q", linkPath, target, ok, want)
		}
	}
}

func TestReplaceSymlinkDoesNotRemoveUnexpectedDirectory(t *testing.T) {
	root := t.TempDir()
	linkPath := filepath.Join(root, "last-known-good")
	if err := os.MkdirAll(linkPath, 0o755); err != nil {
		t.Fatalf("mkdir existing dir: %v", err)
	}
	if err := replaceSymlink(linkPath, filepath.Join(root, "target")); err == nil {
		t.Fatalf("replaceSymlink should fail for an existing directory")
	}
	if info, err := os.Lstat(linkPath); err != nil || !info.IsDir() {
		t.Fatalf("existing directory should remain, info=%v err=%v", info, err)
	}
}

func TestMarkPendingRuntimeUpdateAndBootSuccess(t *testing.T) {
	installRoot := t.TempDir()
	previousRoot := filepath.Join(installRoot, "versions", "v1.0.0")
	targetRoot := filepath.Join(installRoot, "versions", "v1.1.0")
	for _, root := range []string{previousRoot, targetRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", root, err)
		}
		version := filepath.Base(root)
		if err := os.WriteFile(filepath.Join(root, "build-info.txt"), []byte("version="+version+"\n"), 0o644); err != nil {
			t.Fatalf("write build-info %s: %v", root, err)
		}
	}
	if err := replaceSymlink(filepath.Join(installRoot, "current"), targetRoot); err != nil {
		t.Fatalf("set current: %v", err)
	}
	if err := markPendingRuntimeUpdate(installRoot, targetRoot, previousRoot, "v1.1.0"); err != nil {
		t.Fatalf("markPendingRuntimeUpdate: %v", err)
	}
	pending, ok := resolveRuntimeLink(filepath.Join(installRoot, "pending-target"))
	if !ok || pending != targetRoot {
		t.Fatalf("pending-target = %q ok=%v, want %q", pending, ok, targetRoot)
	}
	lastKnownGood, ok := resolveRuntimeLink(filepath.Join(installRoot, "last-known-good"))
	if !ok || lastKnownGood != previousRoot {
		t.Fatalf("last-known-good = %q ok=%v, want %q", lastKnownGood, ok, previousRoot)
	}
	if err := markCurrentRuntimeBootSuccessful(installRoot); err != nil {
		t.Fatalf("markCurrentRuntimeBootSuccessful: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(installRoot, "pending-target")); !os.IsNotExist(err) {
		t.Fatalf("pending-target should be removed after successful boot, err=%v", err)
	}
	lastKnownGood, ok = resolveRuntimeLink(filepath.Join(installRoot, "last-known-good"))
	if !ok || lastKnownGood != targetRoot {
		t.Fatalf("last-known-good after success = %q ok=%v, want %q", lastKnownGood, ok, targetRoot)
	}
}

func TestMarkCurrentRuntimeBootSuccessfulRepairsUnactivatedIntent(t *testing.T) {
	installRoot := t.TempDir()
	currentRoot := filepath.Join(installRoot, "versions", "v1.0.0")
	pendingRoot := filepath.Join(installRoot, "versions", "v1.1.0")
	for _, root := range []string{currentRoot, pendingRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "build-info.txt"), []byte("version="+filepath.Base(root)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := replaceSymlink(filepath.Join(installRoot, "current"), currentRoot); err != nil {
		t.Fatal(err)
	}
	if err := replaceSymlink(pendingRuntimeLink(installRoot), pendingRoot); err != nil {
		t.Fatal(err)
	}
	if err := markCurrentRuntimeBootSuccessful(installRoot); err != nil {
		t.Fatalf("repair unactivated intent: %v", err)
	}
	if _, err := os.Lstat(pendingRuntimeLink(installRoot)); !os.IsNotExist(err) {
		t.Fatalf("stale pending intent remains: %v", err)
	}
	got, ok := resolveRuntimeLink(filepath.Join(installRoot, "current"))
	if !ok || got != currentRoot {
		t.Fatalf("current runtime changed during repair: %q ok=%v", got, ok)
	}
}

func TestRollbackPendingRuntimeUpdateRestoresPreviousRuntime(t *testing.T) {
	installRoot := t.TempDir()
	t.Setenv("SWARM_SYSTEM_BIN_DIR", filepath.Join(t.TempDir(), "bin"))
	previousRoot := filepath.Join(installRoot, "versions", "v1.0.0")
	targetRoot := filepath.Join(installRoot, "versions", "v1.1.0")
	for _, root := range []string{previousRoot, targetRoot} {
		for _, dir := range []string{"bin", "libexec", "lib", "share"} {
			if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
				t.Fatalf("mkdir %s/%s: %v", root, dir, err)
			}
		}
		for _, name := range []string{"swarm", "swarmdev", "rebuild", "swarmsetup"} {
			path := filepath.Join(root, "libexec", name)
			if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
		}
		version := filepath.Base(root)
		if err := os.WriteFile(filepath.Join(root, "build-info.txt"), []byte("version="+version+"\n"), 0o644); err != nil {
			t.Fatalf("write build-info %s: %v", root, err)
		}
	}
	if err := switchRuntimeLinks(installRoot, targetRoot); err != nil {
		t.Fatalf("switchRuntimeLinks target: %v", err)
	}
	if err := markPendingRuntimeUpdate(installRoot, targetRoot, previousRoot, "v1.1.0"); err != nil {
		t.Fatalf("markPendingRuntimeUpdate: %v", err)
	}
	rolledBackRoot, err := rollbackPendingRuntimeUpdate(installRoot, errors.New("boom"))
	if err != nil {
		t.Fatalf("rollbackPendingRuntimeUpdate: %v", err)
	}
	if rolledBackRoot != previousRoot {
		t.Fatalf("rolledBackRoot = %q, want %q", rolledBackRoot, previousRoot)
	}
	currentRoot, ok := resolveRuntimeLink(filepath.Join(installRoot, "current"))
	if !ok || currentRoot != previousRoot {
		t.Fatalf("current after rollback = %q ok=%v, want %q", currentRoot, ok, previousRoot)
	}
	if _, err := os.Lstat(filepath.Join(installRoot, "pending-target")); !os.IsNotExist(err) {
		t.Fatalf("pending-target should be removed after rollback, err=%v", err)
	}
}

func TestPreflightReleaseUpdateAcceptsCanonicalWritableInstalledLayout(t *testing.T) {
	systemRoot := t.TempDir()
	installRoot := filepath.Join(systemRoot, "share", "swarm")
	binRoot := filepath.Join(systemRoot, "bin")
	t.Setenv("SWARM_SYSTEM_INSTALL_ROOT", installRoot)
	t.Setenv("SWARM_SYSTEM_BIN_DIR", binRoot)
	for _, dir := range []string{filepath.Join(installRoot, "versions"), binRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runtimeRoot := filepath.Join(installRoot, "versions", "v1.0.0")
	for _, leaf := range []string{"bin", "libexec", "lib", "share"} {
		if err := os.MkdirAll(filepath.Join(runtimeRoot, leaf), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"swarm", "swarmdev", "rebuild", "swarmsetup"} {
		if err := os.WriteFile(filepath.Join(runtimeRoot, "libexec", name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := replaceSymlink(filepath.Join(installRoot, "current"), runtimeRoot); err != nil {
		t.Fatal(err)
	}
	for _, leaf := range []string{"bin", "libexec", "lib", "share"} {
		if err := replaceSymlink(filepath.Join(installRoot, leaf), filepath.Join(installRoot, "current", leaf)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"swarm", "swarmdev", "rebuild", "swarmsetup"} {
		if err := replaceSymlink(filepath.Join(binRoot, name), filepath.Join(installRoot, "libexec", name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := PreflightReleaseUpdate(Profile{InstallRoot: installRoot}); err != nil {
		t.Fatalf("preflight rejected canonical layout: %v", err)
	}
}

func TestPreflightReleaseUpdateRejectsBrokenStableLauncherBeforeActivation(t *testing.T) {
	systemRoot := t.TempDir()
	installRoot := filepath.Join(systemRoot, "share", "swarm")
	binRoot := filepath.Join(systemRoot, "bin")
	t.Setenv("SWARM_SYSTEM_INSTALL_ROOT", installRoot)
	t.Setenv("SWARM_SYSTEM_BIN_DIR", binRoot)
	if err := os.MkdirAll(filepath.Join(installRoot, "versions", "v1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceSymlink(filepath.Join(installRoot, "current"), filepath.Join(installRoot, "versions", "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	err := PreflightReleaseUpdate(Profile{InstallRoot: installRoot})
	if err == nil || !strings.Contains(err.Error(), "repair/reinstall with sudo") {
		t.Fatalf("preflight error = %v, want one-time repair guidance", err)
	}
}

func TestRunUpdateHelperPreflightFailureRefusesBeforeActivation(t *testing.T) {
	profile := Profile{InstallRoot: t.TempDir(), DataDir: t.TempDir()}
	plan := client.UpdateApplyPlan{TargetVersion: "v1.2.3"}

	originalApplyRelease := applyReleaseUpdateForUpdate
	originalResolveLifecycle := resolveLifecycleManagerForUpdate
	originalPreflight := preflightReleaseUpdateForUpdate
	defer func() {
		applyReleaseUpdateForUpdate = originalApplyRelease
		resolveLifecycleManagerForUpdate = originalResolveLifecycle
		preflightReleaseUpdateForUpdate = originalPreflight
	}()

	resolveLifecycleManagerForUpdate = func(Profile) (lifecycleManager, bool, error) {
		return lifecycleManager{Kind: lifecycleKindDirect}, true, nil
	}
	preflightReleaseUpdateForUpdate = func(Profile) error {
		return releaseUpdateRepairError("broken launcher topology")
	}
	applyReleaseUpdateForUpdate = func(context.Context, Profile, client.UpdateApplyPlan) (UpdateResult, error) {
		t.Fatal("activation ran after failed preflight")
		return UpdateResult{}, nil
	}

	err := RunUpdateHelper(profile, plan, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "one-time repair/reinstall with sudo") {
		t.Fatalf("RunUpdateHelper error = %v, want one-time repair guidance", err)
	}
}

func TestRunUpdateHelperApplyFailureLeavesServingRuntimeUntouched(t *testing.T) {
	profile := Profile{InstallRoot: t.TempDir(), DataDir: t.TempDir()}
	plan := client.UpdateApplyPlan{TargetVersion: "v1.2.3"}

	originalStopBackend := stopBackendForUpdate
	originalApplyRelease := applyReleaseUpdateForUpdate
	originalStartBackend := startBackendForUpdate
	originalResolveLifecycle := resolveLifecycleManagerForUpdate
	originalPreflight := preflightReleaseUpdateForUpdate
	defer func() {
		stopBackendForUpdate = originalStopBackend
		applyReleaseUpdateForUpdate = originalApplyRelease
		startBackendForUpdate = originalStartBackend
		resolveLifecycleManagerForUpdate = originalResolveLifecycle
		preflightReleaseUpdateForUpdate = originalPreflight
	}()
	preflightReleaseUpdateForUpdate = func(Profile) error { return nil }

	calls := []string{}
	stopBackendForUpdate = func(Profile) error {
		calls = append(calls, "stop")
		return nil
	}
	applyReleaseUpdateForUpdate = func(context.Context, Profile, client.UpdateApplyPlan) (UpdateResult, error) {
		calls = append(calls, "apply-failed")
		return UpdateResult{}, errors.New("bad candidate")
	}
	startBackendForUpdate = func(Profile, StartBackendOptions) error {
		calls = append(calls, "restart-last-working")
		return nil
	}
	resolveLifecycleManagerForUpdate = func(Profile) (lifecycleManager, bool, error) {
		return lifecycleManager{Kind: lifecycleKindDirect}, true, nil
	}

	err := RunUpdateHelper(profile, plan, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "current daemon and last working runtime remain active") {
		t.Fatalf("RunUpdateHelper() error = %v, want untouched runtime evidence", err)
	}
	if got, want := strings.Join(calls, ","), "apply-failed"; got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
}

func TestFinishReleaseUpdateJobAfterBootPersistsTerminalOutcome(t *testing.T) {
	profile := Profile{DataDir: t.TempDir()}
	for _, tt := range []struct {
		name    string
		status  string
		message string
		errText string
	}{
		{name: "success", status: updateJobStatusCompleted, message: "Updated to v1.2.3."},
		{name: "rollback", status: updateJobStatusFailed, errText: "rolled back to v1.2.2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			jobID := "job-" + tt.name
			if err := localupdate.WriteUpdateJobStatus(profile.DataDir, localupdate.UpdateJobStatus{ID: jobID, Kind: updateKindRelease, Status: updateJobStatusRunning}); err != nil {
				t.Fatalf("seed update status: %v", err)
			}
			finishReleaseUpdateJobAfterBoot(profile, tt.status, tt.message, tt.errText)
			status, ok, err := localupdate.ReadUpdateJobStatusPath(localupdate.UpdateJobStatusPath(profile.DataDir))
			if err != nil || !ok {
				t.Fatalf("read update status: ok=%v err=%v", ok, err)
			}
			if status.Status != tt.status || status.Message != tt.message || status.Error != tt.errText || status.CompletedAtUnix == 0 {
				t.Fatalf("terminal update status = %+v", status)
			}
		})
	}
}

func TestRunUpdateHelperSystemdShutdownFailureDoesNotReportSuccess(t *testing.T) {
	profile := Profile{InstallRoot: t.TempDir(), DataDir: t.TempDir()}
	plan := client.UpdateApplyPlan{TargetVersion: "v1.2.3"}
	t.Setenv(updateJobIDEnv, "job-release-failure")
	if err := localupdate.WriteUpdateJobStatus(profile.DataDir, localupdate.UpdateJobStatus{ID: "job-release-failure", Kind: updateKindRelease, Status: updateJobStatusRunning}); err != nil {
		t.Fatalf("seed update status: %v", err)
	}

	originalApplyRelease := applyReleaseUpdateForUpdate
	originalResolveLifecycle := resolveLifecycleManagerForUpdate
	originalServiceActive := serviceActiveForUpdate
	originalRequestShutdown := requestBackendShutdownForUpdate
	originalPreflight := preflightReleaseUpdateForUpdate
	originalActiveBackendPID := activeBackendPIDForUpdate
	defer func() {
		applyReleaseUpdateForUpdate = originalApplyRelease
		resolveLifecycleManagerForUpdate = originalResolveLifecycle
		serviceActiveForUpdate = originalServiceActive
		requestBackendShutdownForUpdate = originalRequestShutdown
		preflightReleaseUpdateForUpdate = originalPreflight
		activeBackendPIDForUpdate = originalActiveBackendPID
	}()
	preflightReleaseUpdateForUpdate = func(Profile) error { return nil }
	resolveLifecycleManagerForUpdate = func(Profile) (lifecycleManager, bool, error) {
		return lifecycleManager{Kind: lifecycleKindSystemd, Scope: "system", Unit: "swarm.service"}, true, nil
	}
	serviceActiveForUpdate = func(systemdServiceScope, string) (bool, bool, error) { return true, true, nil }
	activeBackendPIDForUpdate = func(Profile) (string, error) { return "111", nil }
	applyReleaseUpdateForUpdate = func(context.Context, Profile, client.UpdateApplyPlan) (UpdateResult, error) {
		return UpdateResult{Version: "v1.2.3"}, nil
	}
	requestBackendShutdownForUpdate = func(Profile, string) error { return errors.New("transport refused") }

	err := RunUpdateHelper(profile, plan, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "request authenticated update restart") {
		t.Fatalf("RunUpdateHelper error = %v, want restart request failure", err)
	}
	status, ok, statusErr := localupdate.ReadUpdateJobStatusPath(localupdate.UpdateJobStatusPath(profile.DataDir))
	if statusErr != nil || !ok {
		t.Fatalf("read update status: ok=%v err=%v", ok, statusErr)
	}
	if status.Status != updateJobStatusFailed || !strings.Contains(status.Error, "transport refused") {
		t.Fatalf("update status = %+v, want failed restart request", status)
	}
}

func TestRunUpdateHelperDirectRestartStartsBackendThenRunsTUIForeground(t *testing.T) {
	profile := Profile{InstallRoot: t.TempDir(), DataDir: t.TempDir()}
	plan := client.UpdateApplyPlan{TargetVersion: "v1.2.3"}
	result := UpdateResult{Version: "v1.2.3", RuntimeRoot: filepath.Join(profile.InstallRoot, "versions", "v1.2.3")}

	originalStopBackend := stopBackendForUpdate
	originalApplyRelease := applyReleaseUpdateForUpdate
	originalStartBackend := startBackendForUpdate
	originalRunTUI := runTUIWithExtraEnvForUpdate
	originalResolveLifecycle := resolveLifecycleManagerForUpdate
	originalRollbackRestart := rollbackPendingUpdateAndRestartForUpdate
	originalPreflight := preflightReleaseUpdateForUpdate
	defer func() {
		stopBackendForUpdate = originalStopBackend
		applyReleaseUpdateForUpdate = originalApplyRelease
		startBackendForUpdate = originalStartBackend
		runTUIWithExtraEnvForUpdate = originalRunTUI
		resolveLifecycleManagerForUpdate = originalResolveLifecycle
		rollbackPendingUpdateAndRestartForUpdate = originalRollbackRestart
		preflightReleaseUpdateForUpdate = originalPreflight
	}()
	preflightReleaseUpdateForUpdate = func(Profile) error { return nil }

	calls := []string{}
	stopBackendForUpdate = func(Profile) error {
		calls = append(calls, "stop")
		return nil
	}
	applyReleaseUpdateForUpdate = func(context.Context, Profile, client.UpdateApplyPlan) (UpdateResult, error) {
		calls = append(calls, "apply")
		return result, nil
	}
	startBackendForUpdate = func(Profile, StartBackendOptions) error {
		calls = append(calls, "start-backend")
		return nil
	}
	runTUIWithExtraEnvForUpdate = func(_ Profile, args []string, extraEnv map[string]string) error {
		calls = append(calls, "run-tui")
		if len(args) != 1 || args[0] != "main" {
			t.Fatalf("relaunch args = %v, want [main]", args)
		}
		if got := strings.TrimSpace(extraEnv[appliedUpdateToastEnv]); got != "Updated to v1.2.3" {
			t.Fatalf("toast env = %q", got)
		}
		return nil
	}
	resolveLifecycleManagerForUpdate = func(Profile) (lifecycleManager, bool, error) {
		return lifecycleManager{Kind: lifecycleKindDirect}, true, nil
	}
	rollbackPendingUpdateAndRestartForUpdate = func(Profile, []string, *os.Process, error) error {
		t.Fatalf("rollback should not be called")
		return nil
	}

	if err := RunUpdateHelper(profile, plan, 0, []string{"main"}); err != nil {
		t.Fatalf("RunUpdateHelper: %v", err)
	}
	want := "apply,stop,start-backend,run-tui"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
}

func TestRunUpdateHelperSystemdActivatesBeforeAuthenticatedRestartRequest(t *testing.T) {
	profile := Profile{InstallRoot: t.TempDir(), DataDir: t.TempDir()}
	plan := client.UpdateApplyPlan{TargetVersion: "v1.2.3"}
	result := UpdateResult{Version: "v1.2.3", RuntimeRoot: filepath.Join(profile.InstallRoot, "versions", "v1.2.3")}
	t.Setenv(updateJobIDEnv, "job-release-restart")
	if err := localupdate.WriteUpdateJobStatus(profile.DataDir, localupdate.UpdateJobStatus{ID: "job-release-restart", Kind: updateKindRelease, Status: updateJobStatusRunning}); err != nil {
		t.Fatalf("seed update status: %v", err)
	}

	originalStopBackend := stopBackendForUpdate
	originalStopSystemd := stopSystemdServiceForUpdate
	originalApplyRelease := applyReleaseUpdateForUpdate
	originalStartBackend := startBackendForUpdate
	originalRestartSystemd := restartSystemdServiceForUpdate
	originalRunTUI := runTUIWithExtraEnvForUpdate
	originalResolveLifecycle := resolveLifecycleManagerForUpdate
	originalServiceActive := serviceActiveForUpdate
	originalRollbackRestart := rollbackPendingUpdateAndRestartForUpdate
	originalRequestShutdown := requestBackendShutdownForUpdate
	originalPreflight := preflightReleaseUpdateForUpdate
	originalActiveBackendPID := activeBackendPIDForUpdate
	originalWaitForReplacement := waitForReplacementDaemonReadyForUpdate
	defer func() {
		stopBackendForUpdate = originalStopBackend
		stopSystemdServiceForUpdate = originalStopSystemd
		applyReleaseUpdateForUpdate = originalApplyRelease
		startBackendForUpdate = originalStartBackend
		restartSystemdServiceForUpdate = originalRestartSystemd
		runTUIWithExtraEnvForUpdate = originalRunTUI
		resolveLifecycleManagerForUpdate = originalResolveLifecycle
		serviceActiveForUpdate = originalServiceActive
		rollbackPendingUpdateAndRestartForUpdate = originalRollbackRestart
		requestBackendShutdownForUpdate = originalRequestShutdown
		preflightReleaseUpdateForUpdate = originalPreflight
		activeBackendPIDForUpdate = originalActiveBackendPID
		waitForReplacementDaemonReadyForUpdate = originalWaitForReplacement
	}()
	preflightReleaseUpdateForUpdate = func(Profile) error { return nil }

	calls := []string{}
	stopBackendForUpdate = func(Profile) error {
		t.Fatalf("direct backend stop should not be used for active systemd service")
		return nil
	}
	stopSystemdServiceForUpdate = func(systemdServiceScope, string) error {
		t.Fatalf("systemd stop must not be used for release update")
		return nil
	}
	applyReleaseUpdateForUpdate = func(context.Context, Profile, client.UpdateApplyPlan) (UpdateResult, error) {
		calls = append(calls, "apply")
		return result, nil
	}
	startBackendForUpdate = func(Profile, StartBackendOptions) error {
		t.Fatalf("direct backend start should not be used for active systemd service")
		return nil
	}
	restartSystemdServiceForUpdate = func(systemdServiceScope, string, bool) error {
		t.Fatalf("systemd restart must not be used for release update")
		return nil
	}
	runTUIWithExtraEnvForUpdate = func(_ Profile, args []string, extraEnv map[string]string) error {
		calls = append(calls, "run-tui")
		if len(args) != 1 || args[0] != "main" {
			t.Fatalf("relaunch args = %v, want [main]", args)
		}
		if got := strings.TrimSpace(extraEnv[appliedUpdateToastEnv]); got != "Updated to v1.2.3" {
			t.Fatalf("toast env = %q", got)
		}
		return nil
	}
	resolveLifecycleManagerForUpdate = func(Profile) (lifecycleManager, bool, error) {
		return lifecycleManager{Kind: lifecycleKindSystemd, Scope: "system", Unit: "swarm.service"}, true, nil
	}
	serviceActiveForUpdate = func(scope systemdServiceScope, unit string) (bool, bool, error) {
		return true, true, nil
	}
	activeBackendPIDForUpdate = func(Profile) (string, error) { return "111", nil }
	waitForReplacementDaemonReadyForUpdate = func(_ Profile, previousPID string) error {
		calls = append(calls, "ready:"+previousPID)
		return nil
	}
	rollbackPendingUpdateAndRestartForUpdate = func(Profile, []string, *os.Process, error) error {
		t.Fatalf("direct rollback should not be used for successful systemd lifecycle")
		return nil
	}
	requestBackendShutdownForUpdate = func(_ Profile, reason string) error {
		calls = append(calls, "shutdown:"+reason)
		return nil
	}

	if err := RunUpdateHelper(profile, plan, 0, []string{"main"}); err != nil {
		t.Fatalf("RunUpdateHelper: %v", err)
	}
	want := "apply,shutdown:update-release,ready:111,run-tui"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
	status, ok, err := localupdate.ReadUpdateJobStatusPath(localupdate.UpdateJobStatusPath(profile.DataDir))
	if err != nil || !ok {
		t.Fatalf("read update status: ok=%v err=%v", ok, err)
	}
	if status.Status != updateJobStatusCompleted || status.CompletedAtUnix == 0 || status.Message != "Updated to v1.2.3." {
		t.Fatalf("update status = %+v, want completed release update", status)
	}
}

func TestWaitForReplacementDaemonReadySucceedsImmediately(t *testing.T) {
	originalProbe := replacementDaemonReadinessProbeForUpdate
	originalLimit := replacementDaemonReadinessLimit
	originalPoll := replacementDaemonReadinessPoll
	defer func() {
		replacementDaemonReadinessProbeForUpdate = originalProbe
		replacementDaemonReadinessLimit = originalLimit
		replacementDaemonReadinessPoll = originalPoll
	}()
	replacementDaemonReadinessLimit = time.Second
	replacementDaemonReadinessPoll = time.Millisecond
	calls := 0
	replacementDaemonReadinessProbeForUpdate = func(Profile, string) error {
		calls++
		return nil
	}

	if err := waitForReplacementDaemonReady(Profile{}, "111"); err != nil {
		t.Fatalf("waitForReplacementDaemonReady: %v", err)
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d, want 1", calls)
	}
}

func TestWaitForReplacementDaemonReadyAllowsDelayedReadiness(t *testing.T) {
	originalProbe := replacementDaemonReadinessProbeForUpdate
	originalLimit := replacementDaemonReadinessLimit
	originalPoll := replacementDaemonReadinessPoll
	defer func() {
		replacementDaemonReadinessProbeForUpdate = originalProbe
		replacementDaemonReadinessLimit = originalLimit
		replacementDaemonReadinessPoll = originalPoll
	}()
	replacementDaemonReadinessLimit = time.Second
	replacementDaemonReadinessPoll = time.Millisecond
	calls := 0
	replacementDaemonReadinessProbeForUpdate = func(Profile, string) error {
		calls++
		if calls < 3 {
			return errors.New("not ready yet")
		}
		return nil
	}

	if err := waitForReplacementDaemonReady(Profile{}, "111"); err != nil {
		t.Fatalf("waitForReplacementDaemonReady: %v", err)
	}
	if calls != 3 {
		t.Fatalf("probe calls = %d, want 3", calls)
	}
}

func TestRunUpdateHelperSystemdReadinessFailureRollsBack(t *testing.T) {
	profile := Profile{InstallRoot: t.TempDir(), DataDir: t.TempDir()}
	plan := client.UpdateApplyPlan{TargetVersion: "v1.2.3"}

	originalApplyRelease := applyReleaseUpdateForUpdate
	originalResolveLifecycle := resolveLifecycleManagerForUpdate
	originalServiceActive := serviceActiveForUpdate
	originalRequestShutdown := requestBackendShutdownForUpdate
	originalPreflight := preflightReleaseUpdateForUpdate
	originalActiveBackendPID := activeBackendPIDForUpdate
	originalWaitForReplacement := waitForReplacementDaemonReadyForUpdate
	originalRollbackRestart := rollbackPendingUpdateAndRestartForUpdate
	defer func() {
		applyReleaseUpdateForUpdate = originalApplyRelease
		resolveLifecycleManagerForUpdate = originalResolveLifecycle
		serviceActiveForUpdate = originalServiceActive
		requestBackendShutdownForUpdate = originalRequestShutdown
		preflightReleaseUpdateForUpdate = originalPreflight
		activeBackendPIDForUpdate = originalActiveBackendPID
		waitForReplacementDaemonReadyForUpdate = originalWaitForReplacement
		rollbackPendingUpdateAndRestartForUpdate = originalRollbackRestart
	}()

	preflightReleaseUpdateForUpdate = func(Profile) error { return nil }
	resolveLifecycleManagerForUpdate = func(Profile) (lifecycleManager, bool, error) {
		return lifecycleManager{Kind: lifecycleKindSystemd, Scope: "system", Unit: "swarm.service"}, true, nil
	}
	serviceActiveForUpdate = func(systemdServiceScope, string) (bool, bool, error) { return true, true, nil }
	activeBackendPIDForUpdate = func(Profile) (string, error) { return "111", nil }
	applyReleaseUpdateForUpdate = func(context.Context, Profile, client.UpdateApplyPlan) (UpdateResult, error) {
		return UpdateResult{Version: "v1.2.3"}, nil
	}
	requestBackendShutdownForUpdate = func(Profile, string) error { return nil }
	waitForReplacementDaemonReadyForUpdate = func(Profile, string) error { return errors.New("readiness timed out") }
	rollbackCalls := 0
	rollbackPendingUpdateAndRestartForUpdate = func(_ Profile, _ []string, _ *os.Process, cause error) error {
		rollbackCalls++
		if cause == nil || !strings.Contains(cause.Error(), "authenticated and ready") {
			t.Fatalf("rollback cause = %v", cause)
		}
		return errors.New("rolled back replacement")
	}

	err := RunUpdateHelper(profile, plan, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "rolled back replacement") {
		t.Fatalf("RunUpdateHelper error = %v, want rollback failure", err)
	}
	if rollbackCalls != 1 {
		t.Fatalf("rollback calls = %d, want 1", rollbackCalls)
	}
}

func TestWaitForReplacementDaemonReadyReturnsBoundedFailure(t *testing.T) {
	originalProbe := replacementDaemonReadinessProbeForUpdate
	originalLimit := replacementDaemonReadinessLimit
	originalPoll := replacementDaemonReadinessPoll
	defer func() {
		replacementDaemonReadinessProbeForUpdate = originalProbe
		replacementDaemonReadinessLimit = originalLimit
		replacementDaemonReadinessPoll = originalPoll
	}()
	replacementDaemonReadinessLimit = 5 * time.Millisecond
	replacementDaemonReadinessPoll = time.Millisecond
	replacementDaemonReadinessProbeForUpdate = func(Profile, string) error {
		return errors.New("replacement refused authentication")
	}

	err := waitForReplacementDaemonReady(Profile{}, "111")
	if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "replacement refused authentication") {
		t.Fatalf("waitForReplacementDaemonReady error = %v, want bounded authentication failure", err)
	}
}
