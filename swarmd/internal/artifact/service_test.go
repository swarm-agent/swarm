package artifact

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestStageFinalizeReadPrivateAndIdempotent(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("variant-1", "note.txt", "text/plain", "text")
	payload := []byte("private artifact")

	staged, err := service.Stage(context.Background(), variant, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	wantDigest := sha256.Sum256(payload)
	if staged.DigestSHA256 != hex.EncodeToString(wantDigest[:]) || staged.Size != int64(len(payload)) {
		t.Fatalf("staged digest/size = %q/%d", staged.DigestSHA256, staged.Size)
	}
	blob, err := service.Finalize(context.Background(), staged, staged.DigestSHA256, staged.Size)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	variant.Status = pebblestore.SessionArtifactStatusReady
	variant.DigestSHA256 = blob.DigestSHA256
	variant.Size = blob.Size
	data, _, err := service.Read(context.Background(), variant, 1024)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("Read = %q", data)
	}

	dir, err := service.variantDir(variant.SessionID, variant.CollectionID, variant.ID, false)
	if err != nil {
		t.Fatalf("variantDir: %v", err)
	}
	assertMode(t, service.root, 0o700)
	assertMode(t, service.sessionDir(variant.SessionID), 0o700)
	assertMode(t, filepath.Join(dir, "content"), 0o600)
	if strings.Contains(service.root, filepath.Clean(service.workspacePath)+string(filepath.Separator)) {
		t.Fatalf("artifact root %q is inside workspace %q", service.root, service.workspacePath)
	}

	repeated, err := service.Stage(context.Background(), variant, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("repeat Stage: %v", err)
	}
	if _, err := service.Finalize(context.Background(), repeated, repeated.DigestSHA256, repeated.Size); err != nil {
		t.Fatalf("repeat Finalize: %v", err)
	}
	if _, err := service.Stage(context.Background(), variant, bytes.NewReader([]byte("different"))); !errors.Is(err, ErrConflict) {
		t.Fatalf("different Stage error = %v, want ErrConflict", err)
	}
}

func TestStageRejectsQuotaTraversalAndUnsafePresentation(t *testing.T) {
	service := newTestService(t, Limits{MaxArtifactBytes: 4, MaxSessionBytes: 6, MaxPackageFiles: 10, MaxPackageBytes: 1024})
	variant := testVariant("variant-1", "note.txt", "text/plain", "text")
	if _, err := service.Stage(context.Background(), variant, strings.NewReader("12345")); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("artifact quota error = %v", err)
	}
	bad := variant
	bad.ID = "../escape"
	if _, err := service.Stage(context.Background(), bad, strings.NewReader("x")); err == nil {
		t.Fatal("Stage accepted traversal id")
	}
	bad = variant
	bad.Filename = "../escape.txt"
	if _, err := service.Stage(context.Background(), bad, strings.NewReader("x")); err == nil {
		t.Fatal("Stage accepted traversal filename")
	}
	bad = variant
	bad.MediaType = "application/octet-stream"
	bad.Presentation = pebblestore.SessionArtifactPresentation{Kind: "image", Previewable: true}
	if _, err := service.Stage(context.Background(), bad, strings.NewReader("x")); err == nil {
		t.Fatal("Stage accepted unsafe binary preview")
	}

	first := testVariant("first", "one.bin", "application/octet-stream", "download")
	staged, err := service.Stage(context.Background(), first, strings.NewReader("1234"))
	if err != nil {
		t.Fatalf("first Stage: %v", err)
	}
	if _, err := service.Finalize(context.Background(), staged, "", 0); err != nil {
		t.Fatalf("first Finalize: %v", err)
	}
	second := testVariant("second", "two.bin", "application/octet-stream", "download")
	if _, err := service.Stage(context.Background(), second, strings.NewReader("123")); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("session quota error = %v", err)
	}
}

func TestStorageSymlinkAndImportSymlinkRejected(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("variant-1", "note.txt", "text/plain", "text")
	if err := os.MkdirAll(service.sessionDir(variant.SessionID), 0o700); err != nil {
		t.Fatal(err)
	}
	collectionPath := filepath.Join(service.sessionDir(variant.SessionID), "variants", opaqueKey("collection", variant.CollectionID))
	if err := os.MkdirAll(filepath.Dir(collectionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), collectionPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := service.Stage(context.Background(), variant, strings.NewReader("x")); err == nil {
		t.Fatal("Stage accepted symlink in storage path")
	}

	service = newTestService(t, Limits{})
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := source + "-link"
	if err := os.Symlink(source, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	fresh := testVariant("variant-2", "source.txt", "text/plain", "text")
	if _, err := service.ImportFile(context.Background(), fresh, link); err == nil {
		t.Fatal("ImportFile accepted symlink source")
	}
}

func TestPackageLimitsAndTraversalRejected(t *testing.T) {
	service := newTestService(t, Limits{MaxArtifactBytes: 1 << 20, MaxSessionBytes: 2 << 20, MaxPackageFiles: 1, MaxPackageBytes: 5})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.txt"), []byte("123"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.txt"), []byte("4"), 0o600); err != nil {
		t.Fatal(err)
	}
	variant := testVariant("package-1", "bundle.zip", "application/zip", "package")
	if _, err := service.ImportPackage(context.Background(), variant, dir); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("package file quota error = %v", err)
	}

	unsafe := zipBytes(t, "../escape.txt", []byte("escape"))
	unsafeService := newTestService(t, Limits{MaxArtifactBytes: 1 << 20, MaxSessionBytes: 2 << 20, MaxPackageFiles: 10, MaxPackageBytes: 1 << 20})
	if _, err := unsafeService.Stage(context.Background(), variant, bytes.NewReader(unsafe)); err == nil {
		t.Fatal("Stage accepted package traversal entry")
	}

	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.txt"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "one.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := service.ImportPackage(context.Background(), variant, dir); err == nil {
		t.Fatal("ImportPackage accepted symlink")
	}
}

func TestReconcileRemovesStagingWithoutPromoting(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("variant-1", "note.txt", "text/plain", "text")
	staged, err := service.Stage(context.Background(), variant, strings.NewReader("incomplete"))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if staged.token == "" {
		t.Fatal("Stage returned no opaque token")
	}

	restarted, err := New(service.workspacePath, service.limits)
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	report, err := restarted.Reconcile(variant.SessionID)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.RemovedStaging != 1 || report.RemovedBytes != staged.Size {
		t.Fatalf("Reconcile report = %+v", report)
	}
	if _, err := restarted.Finalize(context.Background(), staged, "", 0); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Finalize after reconcile error = %v", err)
	}
}

func TestFinalizeConcurrentHandlesAreIdempotentForSameBytes(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("variant-1", "note.txt", "text/plain", "text")
	payload := []byte("same")
	first, err := service.Stage(context.Background(), variant, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Stage(context.Background(), variant, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finalize(context.Background(), first, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finalize(context.Background(), second, "", 0); err != nil {
		t.Fatalf("second Finalize: %v", err)
	}
}

func TestDeleteSessionRemovesOnlyOwnedSession(t *testing.T) {
	service := newTestService(t, Limits{})
	for _, sessionID := range []string{"session-1", "session-2"} {
		variant := testVariant("variant-1", "note.txt", "text/plain", "text")
		variant.SessionID = sessionID
		staged, err := service.Stage(context.Background(), variant, strings.NewReader(sessionID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Finalize(context.Background(), staged, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.DeleteSession("session-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := os.Stat(service.sessionDir("session-1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted session still exists: %v", err)
	}
	if _, err := os.Stat(service.sessionDir("session-2")); err != nil {
		t.Fatalf("other session removed: %v", err)
	}
}

func newTestService(t *testing.T, limits Limits) *Service {
	t.Helper()
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "private-data"))
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	service, err := New(workspace, limits)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return service
}

func testVariant(id, filename, mediaType, presentation string) pebblestore.SessionArtifactVariant {
	return pebblestore.SessionArtifactVariant{
		ID:             id,
		CollectionID:   "collection-1",
		AccountScopeID: "account-1",
		SessionID:      "session-1",
		Status:         pebblestore.SessionArtifactStatusStaging,
		Filename:       filename,
		MediaType:      mediaType,
		Presentation:   pebblestore.SessionArtifactPresentation{Kind: presentation, Previewable: presentation != "download" && presentation != "package"},
	}
}

func zipBytes(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	archive := zip.NewWriter(&out)
	entry, err := archive.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(entry, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %q = %v, want %v", path, got, want)
	}
}
