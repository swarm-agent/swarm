package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestArtifactPromotionFileConflictTraversalAndSymlinkContract(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("promotion-file-contract", "design.txt", "text/plain", "text")
	staged, err := service.Stage(context.Background(), variant, strings.NewReader("promoted design"))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := service.Finalize(context.Background(), staged, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	variant.Status, variant.DigestSHA256, variant.Size = pebblestore.SessionArtifactStatusReady, blob.DigestSHA256, blob.Size
	workspace := t.TempDir()

	if _, err := service.Materialize(context.Background(), variant, workspace, "designs/final.txt", false); err != nil {
		t.Fatalf("first promotion: %v", err)
	}
	if _, err := service.Materialize(context.Background(), variant, workspace, "designs/final.txt", false); !errors.Is(err, ErrDestinationConflict) {
		t.Fatalf("promotion conflict = %v, want ErrDestinationConflict", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "designs", "final.txt"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Materialize(context.Background(), variant, workspace, "designs/final.txt", true); err != nil {
		t.Fatalf("explicit overwrite: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "designs", "final.txt")); err != nil || string(data) != "promoted design" {
		t.Fatalf("overwritten content = %q err=%v", data, err)
	}

	for _, destination := range []string{"/absolute.txt", "../escape.txt", "designs/../escape.txt", `designs\\escape.txt`} {
		if _, err := service.Materialize(context.Background(), variant, workspace, destination, false); err == nil {
			t.Fatalf("unsafe destination %q was accepted", destination)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := service.Materialize(context.Background(), variant, workspace, "linked/escape.txt", false); err == nil {
		t.Fatal("promotion followed a workspace symlink")
	}
}

func TestArtifactPromotionPackageRejectsSpecialEntriesAndReplacesAtomically(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("promotion-package-contract", "bundle.zip", "application/zip", "package")
	staged, err := service.StagePackage(context.Background(), variant, []PackageEntry{
		{Name: "index.html", Data: []byte("<h1>selected</h1>")},
		{Name: "assets/site.css", Data: []byte("body{}")},
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := service.Finalize(context.Background(), staged, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	variant.Status, variant.DigestSHA256, variant.Size = pebblestore.SessionArtifactStatusReady, blob.DigestSHA256, blob.Size
	workspace := t.TempDir()

	if _, err := service.Materialize(context.Background(), variant, workspace, "variants/selected", false); err != nil {
		t.Fatalf("promote package: %v", err)
	}
	stale := filepath.Join(workspace, "variants", "selected", "stale.txt")
	if err := os.WriteFile(stale, []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Materialize(context.Background(), variant, workspace, "variants/selected", false); !errors.Is(err, ErrDestinationConflict) {
		t.Fatalf("package conflict = %v", err)
	}
	if _, err := service.Materialize(context.Background(), variant, workspace, "variants/selected", true); err != nil {
		t.Fatalf("replace package: %v", err)
	}
	if _, err := os.Lstat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement retained stale file: %v", err)
	}

	unsafeCases := []struct {
		name string
		zip  []byte
	}{
		{name: "traversal", zip: zipBytes(t, "../outside.txt", []byte("escape"))},
		{name: "ambiguous", zip: zipBytesMany(t, []struct{ name string; data []byte }{{"a", []byte("file")}, {"a/b", []byte("child")}})},
	}
	for _, tc := range unsafeCases {
		t.Run(tc.name, func(t *testing.T) {
			bad := testVariant("promotion-"+tc.name, tc.name+".zip", "application/zip", "package")
			if _, err := service.Stage(context.Background(), bad, strings.NewReader(string(tc.zip))); err == nil {
				t.Fatal("artifact ingestion accepted an unsafe package")
			}
		})
	}
}
