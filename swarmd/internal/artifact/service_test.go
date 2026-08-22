package artifact

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestStageAcceptsPreviewableSVGAndRejectsNonSVGXML(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("svg-preview", "preview.svg", "image/svg+xml", "image")
	payload := `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>`
	staged, err := service.Stage(context.Background(), variant, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("Stage previewable SVG: %v", err)
	}
	if staged.MediaType != "image/svg+xml" || staged.Presentation.Kind != "image" || !staged.Presentation.Previewable {
		t.Fatalf("staged SVG presentation = %+v", staged)
	}

	invalid := testVariant("svg-invalid", "invalid.svg", "image/svg+xml", "image")
	if _, err := service.Stage(context.Background(), invalid, strings.NewReader(`<?xml version="1.0"?><html/>`)); err == nil {
		t.Fatal("Stage accepted non-SVG XML as image/svg+xml")
	}

	malformed := testVariant("svg-malformed", "malformed.svg", "image/svg+xml", "image")
	malformedPayload := `<svg xmlns="http://www.w3.org/2000/svg"><style>/* Precision Grid & Guides */</style></svg>`
	if _, err := service.Stage(context.Background(), malformed, strings.NewReader(malformedPayload)); err == nil {
		t.Fatal("Stage accepted malformed XML after the SVG root element")
	}

	unclosed := testVariant("svg-unclosed", "unclosed.svg", "image/svg+xml", "image")
	if _, err := service.Stage(context.Background(), unclosed, strings.NewReader(`<svg xmlns="http://www.w3.org/2000/svg"><g>`)); err == nil {
		t.Fatal("Stage accepted an unclosed SVG document")
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

func TestImportPackageCopiesOrdinaryDirectoryWithPrivateEntries(t *testing.T) {
	service := newTestService(t, Limits{})
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>package</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "site.css"), []byte("body{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	variant := testVariant("package-import", "bundle.zip", "application/zip", "package")
	staged, err := service.ImportPackage(context.Background(), variant, dir)
	if err != nil {
		t.Fatalf("ImportPackage: %v", err)
	}
	blob, err := service.Finalize(context.Background(), staged, staged.DigestSHA256, staged.Size)
	if err != nil {
		t.Fatalf("Finalize package: %v", err)
	}
	variant.Status = pebblestore.SessionArtifactStatusReady
	variant.DigestSHA256 = blob.DigestSHA256
	variant.Size = blob.Size
	data, _, err := service.Read(context.Background(), variant, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 2 || archive.File[0].Name != "assets/site.css" || archive.File[1].Name != "index.html" {
		t.Fatalf("package entries = %+v", archive.File)
	}
	for _, entry := range archive.File {
		if entry.Mode().Perm() != 0o600 {
			t.Fatalf("package entry mode = %v", entry.Mode())
		}
	}
}

func TestStagePackageCreatesSortedPrivateArchiveFromMemory(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("package-memory", "bundle.zip", "application/zip", "package")
	entries := []PackageEntry{
		{Name: "index.html", Data: []byte("<h1>managed</h1>")},
		{Name: "assets/site.css", Data: []byte("body{}")},
	}

	staged, err := service.StagePackage(context.Background(), variant, entries)
	if err != nil {
		t.Fatalf("StagePackage: %v", err)
	}
	entries[0].Data[0] = 'X'
	blob, err := service.Finalize(context.Background(), staged, staged.DigestSHA256, staged.Size)
	if err != nil {
		t.Fatalf("Finalize package: %v", err)
	}
	variant.Status = pebblestore.SessionArtifactStatusReady
	variant.DigestSHA256 = blob.DigestSHA256
	variant.Size = blob.Size
	data, _, err := service.Read(context.Background(), variant, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 2 || archive.File[0].Name != "assets/site.css" || archive.File[1].Name != "index.html" {
		t.Fatalf("package entries = %+v", archive.File)
	}
	content, err := archive.File[1].Open()
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(content)
	closeErr := content.Close()
	if readErr != nil || closeErr != nil || string(got) != "<h1>managed</h1>" {
		t.Fatalf("package content = %q, read=%v close=%v", got, readErr, closeErr)
	}
}

func TestStagePackageRejectsEntryAndAggregateQuotasAndUnsafeNames(t *testing.T) {
	service := newTestService(t, Limits{
		MaxArtifactBytes:     1 << 20,
		MaxSessionBytes:      2 << 20,
		MaxPackageFiles:      2,
		MaxPackageEntryBytes: 3,
		MaxPackageBytes:      5,
	})
	variant := testVariant("package-limits", "bundle.zip", "application/zip", "package")
	cases := []struct {
		name    string
		entries []PackageEntry
		quota   bool
	}{
		{name: "entry quota", entries: []PackageEntry{{Name: "one", Data: []byte("1234")}}, quota: true},
		{name: "aggregate quota", entries: []PackageEntry{{Name: "one", Data: []byte("123")}, {Name: "two", Data: []byte("123")}}, quota: true},
		{name: "count quota", entries: []PackageEntry{{Name: "one", Data: []byte("1")}, {Name: "two", Data: []byte("2")}, {Name: "three", Data: []byte("3")}}, quota: true},
		{name: "traversal", entries: []PackageEntry{{Name: "../escape", Data: []byte("1")}}},
		{name: "noncanonical", entries: []PackageEntry{{Name: "a//b", Data: []byte("1")}}},
		{name: "duplicate", entries: []PackageEntry{{Name: "same", Data: []byte("1")}, {Name: "same", Data: []byte("2")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.StagePackage(context.Background(), variant, tc.entries)
			if tc.quota && !errors.Is(err, ErrQuotaExceeded) {
				t.Fatalf("error = %v, want ErrQuotaExceeded", err)
			}
			if !tc.quota && err == nil {
				t.Fatal("unsafe package accepted")
			}
		})
	}
}

func TestMaterializeReadyFileUsesWorkspaceRelativeDestinationAndExplicitOverwrite(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("materialize-file", "note.txt", "text/plain", "text")
	staged, err := service.Stage(context.Background(), variant, strings.NewReader("managed file"))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := service.Finalize(context.Background(), staged, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	variant.Status, variant.DigestSHA256, variant.Size = pebblestore.SessionArtifactStatusReady, blob.DigestSHA256, blob.Size
	workspace := t.TempDir()
	got, err := service.Materialize(context.Background(), variant, workspace, "designs/note.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Destination != "designs/note.txt" || got.Package || got.Files != 1 || got.Bytes != int64(len("managed file")) {
		t.Fatalf("materialized = %+v", got)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "designs", "note.txt")); err != nil || string(data) != "managed file" {
		t.Fatalf("materialized bytes = %q err=%v", data, err)
	}
	if _, err := service.Materialize(context.Background(), variant, workspace, "designs/note.txt", false); !errors.Is(err, ErrDestinationConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "designs", "note.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Materialize(context.Background(), variant, workspace, "designs/note.txt", true); err != nil {
		t.Fatalf("explicit overwrite: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "designs", "note.txt")); err != nil || string(data) != "managed file" {
		t.Fatalf("overwritten bytes = %q err=%v", data, err)
	}
	for _, unsafe := range []string{"/absolute.txt", "../escape.txt", "designs/../escape.txt", `designs\\escape.txt`} {
		if _, err := service.Materialize(context.Background(), variant, workspace, unsafe, false); err == nil {
			t.Fatalf("accepted unsafe destination %q", unsafe)
		}
	}
}

func TestMaterializeReadyVideoUsesVideoArtifactLimit(t *testing.T) {
	service := newTestService(t, Limits{MaxArtifactBytes: 4, MaxVideoArtifactBytes: 32, MaxSessionBytes: 64})
	variant := testVariant("materialize-video", "clip.mp4", "video/mp4", "video")
	body := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00isommp42")
	staged, err := service.Stage(context.Background(), variant, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := service.Finalize(context.Background(), staged, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	variant.Status, variant.DigestSHA256, variant.Size = pebblestore.SessionArtifactStatusReady, blob.DigestSHA256, blob.Size
	workspace := t.TempDir()
	if _, err := service.Materialize(context.Background(), variant, workspace, "renders/clip.mp4", false); err != nil {
		t.Fatalf("materialize video above document limit: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "renders", "clip.mp4")); err != nil || !bytes.Equal(data, body) {
		t.Fatalf("materialized video bytes = %q err=%v", data, err)
	}
}

func TestMaterializePackageRejectsSymlinkAndAmbiguousEntries(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("materialize-package", "bundle.zip", "application/zip", "package")
	staged, err := service.StagePackage(context.Background(), variant, []PackageEntry{{Name: "index.html", Data: []byte("<h1>safe</h1>")}, {Name: "assets/site.css", Data: []byte("body{}")}})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := service.Finalize(context.Background(), staged, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	variant.Status, variant.DigestSHA256, variant.Size = pebblestore.SessionArtifactStatusReady, blob.DigestSHA256, blob.Size
	workspace := t.TempDir()
	got, err := service.Materialize(context.Background(), variant, workspace, "variants/selected", false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Package || got.Files != 2 || got.Bytes != int64(len("<h1>safe</h1>body{}")) {
		t.Fatalf("materialized package = %+v", got)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "variants", "selected", "assets", "site.css")); err != nil || string(data) != "body{}" {
		t.Fatalf("package entry = %q err=%v", data, err)
	}
	if _, err := service.Materialize(context.Background(), variant, workspace, "variants/selected", false); !errors.Is(err, ErrDestinationConflict) {
		t.Fatalf("package conflict error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "variants", "selected", "stale.txt"), []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Materialize(context.Background(), variant, workspace, "variants/selected", true); err != nil {
		t.Fatalf("overwrite package: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "variants", "selected", "stale.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overwrite retained stale package entry: %v", err)
	}

	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "linked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "linked", "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := service.Materialize(context.Background(), variant, workspace, "linked/escape/package", false); err == nil {
		t.Fatal("materialize package followed workspace symlink")
	}

	ambiguous := zipBytesMany(t, []struct {
		name string
		data []byte
	}{{"a", []byte("file")}, {"a/b", []byte("child")}})
	unsafe := testVariant("ambiguous-package", "ambiguous.zip", "application/zip", "package")
	staged, err = service.Stage(context.Background(), unsafe, bytes.NewReader(ambiguous))
	if err != nil {
		t.Fatal(err)
	}
	blob, err = service.Finalize(context.Background(), staged, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	unsafe.Status, unsafe.DigestSHA256, unsafe.Size = pebblestore.SessionArtifactStatusReady, blob.DigestSHA256, blob.Size
	if _, err := service.Materialize(context.Background(), unsafe, workspace, "ambiguous", false); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous package error = %v", err)
	}
}

func TestMaterializeBatchPublishesTwentySourcesAtomicallyAndReportsDigests(t *testing.T) {
	service := newTestService(t, Limits{})
	inputs := make([]BatchMaterializeInput, 0, 20)
	for index := 0; index < 20; index++ {
		variant := testVariant(fmt.Sprintf("batch-%02d", index), fmt.Sprintf("file-%02d.txt", index), "text/plain", "text")
		content := fmt.Sprintf("content-%02d", index)
		staged, err := service.Stage(context.Background(), variant, strings.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		blob, err := service.Finalize(context.Background(), staged, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		variant.Status, variant.DigestSHA256, variant.Size = pebblestore.SessionArtifactStatusReady, blob.DigestSHA256, blob.Size
		inputs = append(inputs, BatchMaterializeInput{Service: service, Variant: variant})
	}
	workspace := t.TempDir()
	results, err := MaterializeBatch(context.Background(), inputs, workspace, "imports/selected", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 20 {
		t.Fatalf("batch result count = %d", len(results))
	}
	for index, result := range results {
		if result.DigestSHA256 != inputs[index].Variant.DigestSHA256 || result.MediaType != "text/plain" || result.Files != 1 {
			t.Fatalf("batch result %d = %#v", index, result)
		}
		data, err := os.ReadFile(filepath.Join(workspace, "imports", "selected", fmt.Sprintf("file-%02d.txt", index)))
		if err != nil || string(data) != fmt.Sprintf("content-%02d", index) {
			t.Fatalf("batch file %d = %q err=%v", index, data, err)
		}
	}
}

func TestMaterializeBatchPreflightAndStagingFailureLeaveNoFinalDestination(t *testing.T) {
	service := newTestService(t, Limits{})
	ready := func(id, filename, content string) pebblestore.SessionArtifactVariant {
		variant := testVariant(id, filename, "text/plain", "text")
		staged, err := service.Stage(context.Background(), variant, strings.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		blob, err := service.Finalize(context.Background(), staged, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		variant.Status, variant.DigestSHA256, variant.Size = pebblestore.SessionArtifactStatusReady, blob.DigestSHA256, blob.Size
		return variant
	}
	workspace := t.TempDir()
	first := ready("batch-first", "same.txt", "first")
	second := ready("batch-second", "SAME.txt", "second")
	if _, err := MaterializeBatch(context.Background(), []BatchMaterializeInput{{Service: service, Variant: first}, {Service: service, Variant: second}}, workspace, "imports/preflight", false); err == nil || !strings.Contains(err.Error(), "duplicate or ambiguous") {
		t.Fatalf("duplicate preflight error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "imports", "preflight")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight left final destination: %v", err)
	}
	invalid := first
	invalid.ID = "missing-bytes"
	if _, err := MaterializeBatch(context.Background(), []BatchMaterializeInput{{Service: service, Variant: first}, {Service: service, Variant: invalid}}, workspace, "imports/stage-failure", false); err == nil || !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("expected source preflight failure, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "imports", "stage-failure")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging failure left final destination: %v", err)
	}
}

func TestMaterializeBatchExpandsPackageIntoDerivedSafeSubdirectory(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("batch-package", "site.zip", "application/zip", "package")
	staged, err := service.StagePackage(context.Background(), variant, []PackageEntry{{Name: "index.html", Data: []byte("safe")}, {Name: "assets/site.css", Data: []byte("body{}")}})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := service.Finalize(context.Background(), staged, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	variant.Status, variant.DigestSHA256, variant.Size = pebblestore.SessionArtifactStatusReady, blob.DigestSHA256, blob.Size
	workspace := t.TempDir()
	results, err := MaterializeBatch(context.Background(), []BatchMaterializeInput{{Service: service, Variant: variant}}, workspace, "imports", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Destination != "imports/site" || !results[0].Package || results[0].Files != 2 {
		t.Fatalf("package batch result = %#v", results)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "imports", "site", "assets", "site.css")); err != nil || string(data) != "body{}" {
		t.Fatalf("package batch file = %q err=%v", data, err)
	}
}

func TestFinalizedPrivateBytesSurviveServiceRestart(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("variant-restart", "restart.txt", "text/plain", "text")
	staged, err := service.Stage(context.Background(), variant, strings.NewReader("durable"))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := service.Finalize(context.Background(), staged, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(service.workspacePath, service.limits)
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	variant.Status = pebblestore.SessionArtifactStatusReady
	variant.DigestSHA256 = blob.DigestSHA256
	variant.Size = blob.Size
	data, _, err := restarted.Read(context.Background(), variant, 1024)
	if err != nil || string(data) != "durable" {
		t.Fatalf("restart read = %q, err=%v", data, err)
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

func TestConcurrentStageAndFinalizeSameVariantIsIdempotent(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("variant-concurrent", "note.txt", "text/plain", "text")
	payload := []byte("same concurrent artifact")

	const writers = 8
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			staged, err := service.Stage(context.Background(), variant, bytes.NewReader(payload))
			if err == nil {
				_, err = service.Finalize(context.Background(), staged, staged.DigestSHA256, staged.Size)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent stage/finalize: %v", err)
		}
	}

	ready := variant
	digest := sha256.Sum256(payload)
	ready.Status = pebblestore.SessionArtifactStatusReady
	ready.DigestSHA256 = hex.EncodeToString(digest[:])
	ready.Size = int64(len(payload))
	data, _, err := service.Read(context.Background(), ready, 1024)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("read concurrent artifact = %q, err=%v", data, err)
	}
}

func TestImportFileCopiesOrdinaryFileIntoManagedStorage(t *testing.T) {
	service := newTestService(t, Limits{})
	variant := testVariant("import-file", "source.txt", "text/plain", "text")
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("legacy ordinary file"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged, err := service.ImportFile(context.Background(), variant, source)
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	blob, err := service.Finalize(context.Background(), staged, staged.DigestSHA256, staged.Size)
	if err != nil {
		t.Fatalf("Finalize imported file: %v", err)
	}
	variant.Status = pebblestore.SessionArtifactStatusReady
	variant.DigestSHA256 = blob.DigestSHA256
	variant.Size = blob.Size
	data, _, err := service.Read(context.Background(), variant, 1024)
	if err != nil || string(data) != "legacy ordinary file" {
		t.Fatalf("imported bytes = %q err=%v", data, err)
	}
}

func TestPackageExpandedByteQuotaRejectsCompressedBomb(t *testing.T) {
	service := newTestService(t, Limits{MaxArtifactBytes: 1 << 20, MaxSessionBytes: 2 << 20, MaxPackageFiles: 10, MaxPackageBytes: 32})
	variant := testVariant("package-expanded", "bundle.zip", "application/zip", "package")
	compressed := zipBytes(t, "large.txt", bytes.Repeat([]byte("a"), 1024))
	if int64(len(compressed)) >= 1024 {
		t.Fatalf("zip fixture did not compress: compressed=%d expanded=%d", len(compressed), 1024)
	}
	if _, err := service.Stage(context.Background(), variant, bytes.NewReader(compressed)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expanded package quota error = %v, want ErrQuotaExceeded", err)
	}
}

func TestDeleteVariantAndCollectionRemoveOnlyVerifiedOwnedBytes(t *testing.T) {
	service := newTestService(t, Limits{})
	variants := []pebblestore.SessionArtifactVariant{
		testVariant("variant-1", "one.txt", "text/plain", "text"),
		testVariant("variant-2", "two.txt", "text/plain", "text"),
		testVariant("variant-other", "other.txt", "text/plain", "text"),
	}
	variants[2].CollectionID = "collection-2"
	for _, variant := range variants {
		staged, err := service.Stage(context.Background(), variant, strings.NewReader(variant.ID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Finalize(context.Background(), staged, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.DeleteVariant("session-1", "collection-1", "variant-1"); err != nil {
		t.Fatalf("DeleteVariant: %v", err)
	}
	if err := service.DeleteVariant("session-1", "collection-1", "variant-1"); err != nil {
		t.Fatalf("idempotent DeleteVariant: %v", err)
	}
	if _, err := os.Stat(filepath.Join(service.collectionDir("session-1", "collection-1"), opaqueKey("variant", "variant-2"))); err != nil {
		t.Fatalf("sibling variant removed: %v", err)
	}
	if err := service.DeleteCollection("session-1", "collection-1"); err != nil {
		t.Fatalf("DeleteCollection: %v", err)
	}
	if err := service.DeleteCollection("session-1", "collection-1"); err != nil {
		t.Fatalf("idempotent DeleteCollection: %v", err)
	}
	if _, err := os.Stat(service.collectionDir("session-1", "collection-2")); err != nil {
		t.Fatalf("other collection removed: %v", err)
	}
}

func TestDeleteVariantRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	service := newTestService(t, Limits{})
	outside := t.TempDir()
	protected := filepath.Join(outside, "protected.txt")
	if err := os.WriteFile(protected, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	collection := service.collectionDir("session-1", "collection-1")
	if err := os.MkdirAll(collection, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(collection, opaqueKey("variant", "variant-link"))); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := service.DeleteVariant("session-1", "collection-1", "variant-link"); err == nil {
		t.Fatal("DeleteVariant accepted symlink variant root")
	}
	if data, err := os.ReadFile(protected); err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: data=%q err=%v", data, err)
	}
}

func TestDeleteSessionRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	service := newTestService(t, Limits{})
	outside := t.TempDir()
	protected := filepath.Join(outside, "protected.txt")
	if err := os.WriteFile(protected, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, service.sessionDir("session-symlink")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := service.DeleteSession("session-symlink"); err == nil {
		t.Fatal("DeleteSession accepted symlink session root")
	}
	if data, err := os.ReadFile(protected); err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: data=%q err=%v", data, err)
	}
}

func TestStageAndImportVideoMP4Artifact(t *testing.T) {
	service := newTestService(t, Limits{})
	mp4Header := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00isommp42")
	mp4Content := append(mp4Header, bytes.Repeat([]byte{0x00}, 64)...)

	t.Run("stage mp4 reader", func(t *testing.T) {
		variant := testVariant("video-1", "output.mp4", "video/mp4", "video")
		staged, err := service.Stage(context.Background(), variant, bytes.NewReader(mp4Content))
		if err != nil {
			t.Fatalf("Stage video: %v", err)
		}
		if staged.MediaType != "video/mp4" || staged.Presentation.Kind != "video" || !staged.Presentation.Previewable {
			t.Fatalf("staged video presentation = %+v, mediaType = %s", staged.Presentation, staged.MediaType)
		}
		blob, err := service.Finalize(context.Background(), staged, "", 0)
		if err != nil {
			t.Fatalf("Finalize video: %v", err)
		}
		variant.Status = pebblestore.SessionArtifactStatusReady
		variant.DigestSHA256 = blob.DigestSHA256
		variant.Size = blob.Size
		readBytes, _, err := service.Read(context.Background(), variant, 1024)
		if err != nil {
			t.Fatalf("Read video: %v", err)
		}
		if !bytes.Equal(readBytes, mp4Content) {
			t.Fatalf("read video content mismatch")
		}
	})

	t.Run("import mp4 file", func(t *testing.T) {
		sourceFile := filepath.Join(t.TempDir(), "rendered.mp4")
		if err := os.WriteFile(sourceFile, mp4Content, 0o600); err != nil {
			t.Fatal(err)
		}
		variant := testVariant("video-2", "rendered.mp4", "video/mp4", "video")
		staged, err := service.ImportFile(context.Background(), variant, sourceFile)
		if err != nil {
			t.Fatalf("ImportFile video: %v", err)
		}
		if staged.MediaType != "video/mp4" || staged.Presentation.Kind != "video" || !staged.Presentation.Previewable {
			t.Fatalf("staged video presentation = %+v", staged.Presentation)
		}
	})
}

func TestVideoSpecificQuotaAllowsLargerVideoThanDocumentLimit(t *testing.T) {
	service := newTestService(t, Limits{
		MaxArtifactBytes:      100,
		MaxVideoArtifactBytes: 500,
		MaxSessionBytes:       2000,
	})

	mp4Header := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00isommp42")
	mp4Content := append(mp4Header, bytes.Repeat([]byte{0x01}, 200)...) // 224 bytes (> 100 max artifact bytes)
	docContent := bytes.Repeat([]byte("a"), 200)

	// Document of 200 bytes should exceed MaxArtifactBytes (100)
	docVariant := testVariant("doc-1", "doc.txt", "text/plain", "text")
	if _, err := service.Stage(context.Background(), docVariant, bytes.NewReader(docContent)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("document of 200 bytes expected ErrQuotaExceeded, got %v", err)
	}

	// Video of 224 bytes should succeed within MaxVideoArtifactBytes (500)
	videoVariant := testVariant("video-1", "clip.mp4", "video/mp4", "video")
	staged, err := service.Stage(context.Background(), videoVariant, bytes.NewReader(mp4Content))
	if err != nil {
		t.Fatalf("video of 224 bytes stage failed: %v", err)
	}
	if _, err := service.Finalize(context.Background(), staged, "", 0); err != nil {
		t.Fatalf("video finalize failed: %v", err)
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

func zipBytesMany(t *testing.T, entries []struct {
	name string
	data []byte
}) []byte {
	t.Helper()
	var out bytes.Buffer
	archive := zip.NewWriter(&out)
	for _, item := range entries {
		entry, err := archive.Create(item.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(item.data); err != nil {
			t.Fatal(err)
		}
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
