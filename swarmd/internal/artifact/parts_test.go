package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPartRevisionBytesAreIndependentVerifiedAndRestartDurable(t *testing.T) {
	service := newTestService(t, Limits{})
	body := []byte("independent hero bytes")
	revision := pebblestore.SessionArtifactPartRevision{GraphState: pebblestore.SessionArtifactGraphAuthoritative, ArtifactChainID: "chain-1", PartID: "hero", ID: "hero-r1", AccountScopeID: "account-1", UserID: "user-1", OwnerSessionID: "session-1", MediaType: "text/plain"}
	staged, err := service.StagePart(context.Background(), revision, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	if staged.DigestSHA256 != hex.EncodeToString(digest[:]) || staged.Size != int64(len(body)) {
		t.Fatalf("staged part = %#v", staged)
	}
	blob, err := service.FinalizePart(context.Background(), staged, staged.DigestSHA256, staged.Size)
	if err != nil {
		t.Fatal(err)
	}
	revision.DigestSHA256, revision.Size = blob.DigestSHA256, blob.Size
	restarted, err := New(service.workspacePath, service.limits)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := restarted.ReadPart(context.Background(), revision, 1024)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("restart part read = %q err=%v", got, err)
	}
	wrong := revision
	wrong.DigestSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, _, err := restarted.ReadPart(context.Background(), wrong, 1024); err == nil {
		t.Fatal("part read accepted mismatched digest metadata")
	}
}

func TestPartRevisionStorageIsImmutableBoundedAndCleanupScoped(t *testing.T) {
	service := newTestService(t, Limits{MaxArtifactBytes: 4, MaxSessionBytes: 8})
	revision := pebblestore.SessionArtifactPartRevision{GraphState: pebblestore.SessionArtifactGraphAuthoritative, ArtifactChainID: "chain-1", PartID: "hero", ID: "hero-r1", AccountScopeID: "account-1", UserID: "user-1", OwnerSessionID: "session-1", MediaType: "text/plain"}
	if _, err := service.StagePart(context.Background(), revision, bytes.NewReader([]byte("12345"))); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("quota error = %v", err)
	}
	staged, err := service.StagePart(context.Background(), revision, bytes.NewReader([]byte("1234")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.FinalizePart(context.Background(), staged, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StagePart(context.Background(), revision, bytes.NewReader([]byte("diff"))); !errors.Is(err, ErrConflict) {
		t.Fatalf("immutable conflict = %v", err)
	}
	other := revision
	other.ID = "hero-r2"
	staged, err = service.StagePart(context.Background(), other, bytes.NewReader([]byte("next")))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := service.FinalizePart(context.Background(), staged, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeletePartRevision(revision.OwnerSessionID, revision.ArtifactChainID, revision.PartID, revision.ID); err != nil {
		t.Fatal(err)
	}
	other.DigestSHA256, other.Size = blob.DigestSHA256, blob.Size
	if got, _, err := service.ReadPart(context.Background(), other, 4); err != nil || string(got) != "next" {
		t.Fatalf("sibling revision changed = %q err=%v", got, err)
	}
}
