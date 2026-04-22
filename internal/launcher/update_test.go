package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallRuntimeFromArtifactUsesVersionedCurrentLayout(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(platformRoot, "swarmd", "libfff_c.so"), []byte("fff"), 0o644); err != nil {
		t.Fatalf("write libfff: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "web", "index.html"), []byte("<!doctype html><html></html>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "web", "assets", "app.js"), []byte("console.log('artifact');"), 0o644); err != nil {
		t.Fatalf("write app.js: %v", err)
	}
	const version = "v1.2.3"
	if err := os.WriteFile(filepath.Join(artifactRoot, "build-info.txt"), []byte("version="+version+"\ncommit=test\n"), 0o644); err != nil {
		t.Fatalf("write build-info: %v", err)
	}

	xdgRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdgRoot, "data"))
	t.Setenv("XDG_BIN_HOME", filepath.Join(xdgRoot, "bin"))

	report, err := InstallRuntimeFromArtifact(artifactRoot)
	if err != nil {
		t.Fatalf("InstallRuntimeFromArtifact: %v", err)
	}

	versionRoot := filepath.Join(xdgRoot, "data", "swarm", "versions", version)
	for _, rel := range []string{
		filepath.Join("libexec", "swarm"),
		filepath.Join("libexec", "swarmdev"),
		filepath.Join("libexec", "rebuild"),
		filepath.Join("libexec", "swarmsetup"),
		filepath.Join("bin", "swarmtui"),
		filepath.Join("bin", "swarmd"),
		filepath.Join("bin", "swarmctl"),
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
	currentTarget, err := os.Readlink(filepath.Join(xdgRoot, "data", "swarm", "current"))
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
	wantLauncher := filepath.Join(xdgRoot, "data", "swarm", "libexec", "swarm")
	if filepath.Clean(targetPath) != filepath.Clean(wantLauncher) {
		t.Fatalf("swarm link = %q, want %q", targetPath, wantLauncher)
	}
	if got := CurrentRuntimeVersion(filepath.Join(xdgRoot, "data", "swarm")); got != version {
		t.Fatalf("CurrentRuntimeVersion = %q, want %q", got, version)
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

func TestRollbackPendingRuntimeUpdateRestoresPreviousRuntime(t *testing.T) {
	installRoot := t.TempDir()
	xdgRoot := t.TempDir()
	t.Setenv("XDG_BIN_HOME", filepath.Join(xdgRoot, "bin"))
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
