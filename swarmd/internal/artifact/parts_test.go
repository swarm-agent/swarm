package artifact

import (
	"context"
	"testing"
)

func TestCanonicalArtifactBytesEnforcesBound(t *testing.T) {
	variant := testVariant("variant", "note.txt", "text/plain", "text")
	if _, _, _, err := canonicalArtifactBytes(context.Background(), Limits{MaxArtifactBytes: 3}, variant, []byte("four")); err == nil {
		t.Fatal("accepted artifact larger than configured Git ingress bound")
	}
}
