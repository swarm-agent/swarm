package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: hydrateDesignSources must obtain separate read authorization and
// preserve exact bytes/range hashes. This filesystem layer is the narrowest
// proof against traversal, escaping symlinks, binary input and silent clipping;
// failures return no partial snapshots for acceptance.
func TestDesignSourceHydration(t *testing.T) {
	root := t.TempDir()
	scope := WorkspaceScope{PrimaryPath: root, Roots: []string{root}}
	original := "first\r\nα component\nlast"
	if err := os.WriteFile(filepath.Join(root, "component.tsx"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	ctx := WithDesignSourceReadAuthorizer(context.Background(), func(_ context.Context, got WorkspaceScope, path string) error {
		calls++
		if got.PrimaryPath != root || !filepath.IsAbs(path) {
			t.Fatal("lost scope")
		}
		return nil
	})
	refs := []DesignFileReference{{Path: "component.tsx", LineStart: 2, LineEnd: 2}}
	got, err := hydrateDesignSources(ctx, scope, refs)
	if err != nil || len(got) != 1 {
		t.Fatalf("hydrate: %v %v", got, err)
	}
	if calls != 1 || string(got[0].Content) != "α component\n" || got[0].SourceSHA256 != designSourceDigest([]byte(original)) || got[0].SHA256 != designSourceDigest(got[0].Content) || got[0].LineStart != 2 || got[0].LineEnd != 2 {
		t.Fatal("snapshot provenance lost", got)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"binary": {0, 1}, "invalid": {255}, "oversized": []byte(strings.Repeat("x", pebblestore.MaxDesignContextBytes+1))} {
		if err := os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, ref := range []DesignFileReference{{Path: "../outside"}, {Path: "escape/outside"}, {Path: "missing"}, {Path: "binary"}, {Path: "invalid"}, {Path: "oversized"}, {Path: "."}, {Path: "component.tsx", LineStart: 2}, {Path: "component.tsx", LineStart: 1, LineEnd: 4}} {
		if partial, err := hydrateDesignSources(ctx, scope, append(refs, ref)); err == nil || partial != nil {
			t.Fatalf("accepted/partially returned %+v: %v", ref, err)
		}
	}
	large := []byte(strings.Repeat("x", pebblestore.MaxDesignContextBytes/2+1))
	if err := os.WriteFile(filepath.Join(root, "large"), large, 0600); err != nil {
		t.Fatal(err)
	}
	if partial, err := hydrateDesignSources(ctx, scope, []DesignFileReference{{Path: "large"}, {Path: "large"}}); err == nil || partial != nil {
		t.Fatal("aggregate limit bypassed")
	}
	for _, denied := range []context.Context{context.Background(), WithDesignSourceReadAuthorizer(context.Background(), func(context.Context, WorkspaceScope, string) error { return errors.New("denied") })} {
		if got, err := hydrateDesignSources(denied, scope, refs); err == nil || got != nil {
			t.Fatal("read without permission")
		}
	}
}
