package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
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

// Requirement: runtime file arguments preserve leading/trailing whitespace.
// Threat: identifier normalization silently corrupts CSS/HTML or defeats exact
// edits. Exercise executeArtifactV3Author, not only its underlying service.
func TestArtifactV3AuthorRuntimePreservesLiteralBytes(t *testing.T) {
	runtime := NewRuntime(1)
	service := NewArtifactV3AuthorService(t.TempDir(), &artifactV3AuthorRepoFake{}, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
	runtime.SetArtifactV3AuthorService(service)
	grant := fullArtifactV3Grant()
	grant.Initial = true
	grant.BaseCommitOID = ""
	ctx := WithArtifactV3AuthorRunContext(context.Background(), ArtifactV3AuthorRunContext{Grant: grant})
	scope := WorkspaceScope{SessionID: "child", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}}
	for _, args := range []map[string]any{
		{"action": "create_file", "path": "index.html", "content": "  body\n"},
		{"action": "edit_file", "path": "index.html", "old_string": "  body\n", "new_string": "\n changed  \n"},
	} {
		if _, err := runtime.executeArtifactV3Author(ctx, scope, "call", args); err != nil {
			t.Fatal(err)
		}
	}
	read, err := service.Read(ctx, artifactV3Principal(), grant, "index.html", 0, 4096)
	if err != nil || read.Content != "\n changed  \n" {
		t.Fatalf("literal bytes changed: %+v %v", read, err)
	}
}

// Requirement: parallel calls within one grant cannot lose successful edits or
// race build/finish evidence. Threat: simultaneous O_TRUNC writers silently
// discard styles/labels or retain trailing bytes. Use one temp-tree service and
// a bounded 16-call wave; sibling grants remain independently lockable.
func TestArtifactV3AuthorConcurrentEditsPreserveAllChanges(t *testing.T) {
	var tokens []string
	for i := 0; i < 16; i++ {
		tokens = append(tokens, fmt.Sprintf("token-%02d", i))
	}
	service := NewArtifactV3AuthorService(t.TempDir(), &artifactV3AuthorRepoFake{base: map[string][]byte{"index.html": []byte(strings.Join(tokens, "\n"))}}, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
	grant, principal := fullArtifactV3Grant(), artifactV3Principal()
	if _, err := service.Inspect(context.Background(), principal, grant); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 16)
	for _, token := range tokens {
		go func(token string) {
			<-start
			results <- service.Edit(context.Background(), principal, grant, "index.html", []byte(token), []byte(token+"-changed"), false)
		}(token)
	}
	close(start)
	for range tokens {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("parallel edits deadlocked")
		}
	}
	read, err := service.Read(context.Background(), principal, grant, "index.html", 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range tokens {
		if strings.Count(read.Content, token+"-changed") != 1 {
			t.Fatalf("successful edit lost: %s in %q", token, read.Content)
		}
	}
	if len(service.operationLocks) != 0 {
		t.Fatal("idle operation locks leaked")
	}
}
