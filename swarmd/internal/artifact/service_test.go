package artifact

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func testVariant(id, filename, mediaType, kind string) pebblestore.SessionArtifactVariant {
	return pebblestore.SessionArtifactVariant{ID: id, CollectionID: "collection-1", AccountScopeID: "account-1", SessionID: "session-1", Filename: filename, MediaType: mediaType, Presentation: pebblestore.SessionArtifactPresentation{Kind: kind}}
}

func TestStatelessMaterializationVerifiesGitProjection(t *testing.T) {
	body := []byte("managed by git")
	variant := testVariant("variant-1", "note.txt", "text/plain", "text")
	_, digest, size, err := canonicalArtifactBytes(context.Background(), Limits{}, variant, body)
	if err != nil {
		t.Fatal(err)
	}
	variant.DigestSHA256, variant.Size = digest, size
	workspace := t.TempDir()
	if _, err := MaterializeBytes(context.Background(), Limits{}, variant, body, workspace, "out/note.txt", false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(workspace, "out", "note.txt"))
	if err != nil || string(got) != string(body) {
		t.Fatalf("materialized=%q err=%v", got, err)
	}
	if _, err := MaterializeBytes(context.Background(), Limits{}, variant, []byte("wrong"), workspace, "bad.txt", false); err == nil {
		t.Fatal("accepted bytes that do not match Git projection")
	}
}
