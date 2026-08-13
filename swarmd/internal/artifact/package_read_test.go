package artifact

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestServiceReadPackageManifestAndEntry(t *testing.T) {
	service, variant := readyPackageFixture(t, []packageTestEntry{{name: "index.html", body: []byte("<main>ready</main>")}, {name: "assets/site.css", body: []byte("body{}")}})
	manifest, body, _, err := service.ReadPackage(context.Background(), variant, "", 64)
	if err != nil || body != nil || len(manifest) != 2 || manifest[0].Name != "assets/site.css" || manifest[0].Size != 6 || manifest[1].Name != "index.html" {
		t.Fatalf("manifest=%#v body=%q err=%v", manifest, body, err)
	}
	manifest, body, _, err = service.ReadPackage(context.Background(), variant, "index.html", 64)
	if err != nil || manifest != nil || string(body) != "<main>ready</main>" {
		t.Fatalf("entry manifest=%#v body=%q err=%v", manifest, body, err)
	}
	for _, entry := range []string{"../index.html", "/index.html", `assets\site.css`, "assets//site.css", " index.html"} {
		if _, _, _, err := service.ReadPackage(context.Background(), variant, entry, 64); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("unsafe entry %q error = %v", entry, err)
		}
	}
	if _, _, _, err := service.ReadPackage(context.Background(), variant, "index.html", 4); err == nil || err != ErrQuotaExceeded {
		t.Fatalf("oversize entry error = %v", err)
	}
	if _, _, _, err := service.ReadPackage(context.Background(), variant, "index.html", 0); err == nil || !strings.Contains(err.Error(), "limit is required") {
		t.Fatalf("missing read limit error = %v", err)
	}
}

func TestServiceReadPackageRejectsDuplicateAndSpecialEntries(t *testing.T) {
	cases := []struct {
		name    string
		entries []packageTestEntry
	}{
		{name: "duplicate", entries: []packageTestEntry{{name: "same.txt", body: []byte("one")}, {name: "same.txt", body: []byte("two")}}},
		{name: "symlink", entries: []packageTestEntry{{name: "link", body: []byte("target"), mode: os.ModeSymlink | 0o777}}},
		{name: "special", entries: []packageTestEntry{{name: "device", mode: os.ModeDevice | 0o600}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, variant := readyPackageFixture(t, tc.entries)
			if _, _, _, err := service.ReadPackage(context.Background(), variant, "", 64); err == nil {
				t.Fatal("expected unsafe package rejection")
			}
		})
	}
}

type packageTestEntry struct {
	name string
	body []byte
	mode os.FileMode
}

func readyPackageFixture(t *testing.T, entries []packageTestEntry) (*Service, pebblestore.SessionArtifactVariant) {
	t.Helper()
	root := t.TempDir()
	service := &Service{root: root, limits: normalizeLimits(Limits{})}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	variant := pebblestore.SessionArtifactVariant{ID: "variant-1", CollectionID: "collection-1", AccountScopeID: "account-1", SessionID: "session-1", Status: pebblestore.SessionArtifactStatusReady, Filename: "design.zip", MediaType: "application/zip"}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		} else {
			header.SetMode(0o600)
		}
		current, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := current.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	dir, err := service.variantDir(variant.SessionID, variant.CollectionID, variant.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	content := archive.Bytes()
	if err := os.WriteFile(filepath.Join(dir, "content"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	variant.DigestSHA256, variant.Size = hex.EncodeToString(digest[:]), int64(len(content))
	return service, variant
}
