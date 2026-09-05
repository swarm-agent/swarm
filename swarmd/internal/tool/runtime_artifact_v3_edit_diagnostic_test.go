package tool

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Requirement: Edit rejects absent/ambiguous literal matches without mutation,
// and reports the match count rather than implying a source-head race.
// Threat: a provider retries escaped or stale text and exhausts its repair budget.
// Authority: ArtifactV3AuthorService.Edit/Read; a temp-tree unit test is the
// narrowest layer proving both the diagnostic and unchanged authored bytes.
func TestArtifactV3AuthorEditMismatchDiagnostic(t *testing.T) {
	const original = `<p>Team & Team</p>`
	repo := &artifactV3AuthorRepoFake{base: map[string][]byte{"index.html": []byte(original)}}
	service := NewArtifactV3AuthorService(t.TempDir(), repo, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
	grant, principal := fullArtifactV3Grant(), artifactV3Principal()
	ctx := context.Background()
	for _, tc := range []struct{ old, count string }{{`\u003cp\u003e`, "0"}, {"Team", "2"}} {
		err := service.Edit(ctx, principal, grant, "index.html", []byte(tc.old), []byte("bad"), false)
		if !errors.Is(err, ErrArtifactV3AuthorConflict) || !strings.Contains(err.Error(), "matched "+tc.count+" times") || !strings.Contains(err.Error(), "no file was changed") {
			t.Fatalf("diagnostic: %v", err)
		}
		read, err := service.Read(ctx, principal, grant, "index.html", 0, 4096)
		if err != nil || read.Content != original {
			t.Fatalf("mismatch mutated bytes: %+v %v", read, err)
		}
	}
	if err := service.Edit(ctx, principal, grant, "index.html", []byte("Team"), []byte("Studio"), true); err != nil {
		t.Fatal(err)
	}
	read, err := service.Read(ctx, principal, grant, "index.html", 0, 4096)
	if err != nil || read.Content != `<p>Studio & Studio</p>` {
		t.Fatalf("explicit replacement failed: %+v %v", read, err)
	}
}
