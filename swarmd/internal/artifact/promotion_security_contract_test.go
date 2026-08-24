package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactMaterializationTraversalAndSymlinkContract(t *testing.T) {
	body := []byte("promoted design")
	variant := testVariant("promotion", "design.txt", "text/plain", "text")
	_, variant.DigestSHA256, variant.Size, _ = canonicalArtifactBytes(context.Background(), Limits{}, variant, body)
	workspace := t.TempDir()
	if _, err := MaterializeBytes(context.Background(), Limits{}, variant, body, workspace, "designs/final.txt", false); err != nil { t.Fatal(err) }
	if _, err := MaterializeBytes(context.Background(), Limits{}, variant, body, workspace, "designs/final.txt", false); !errors.Is(err, ErrDestinationConflict) { t.Fatalf("conflict=%v", err) }
	for _, destination := range []string{"/absolute.txt", "../escape.txt", "designs/../escape.txt", `designs\\escape.txt`} {
		if _, err := MaterializeBytes(context.Background(), Limits{}, variant, body, workspace, destination, false); err == nil { t.Fatalf("accepted unsafe destination %q", destination) }
	}
	outside := t.TempDir(); if err := os.Symlink(outside, filepath.Join(workspace, "linked")); err != nil { t.Skip(err) }
	if _, err := MaterializeBytes(context.Background(), Limits{}, variant, body, workspace, "linked/escape.txt", false); err == nil { t.Fatal("followed workspace symlink") }
}
