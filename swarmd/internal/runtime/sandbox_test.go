package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// TestProjectCacheLimitPrincipalFallback protects daemon automation from failing
// sandbox admission solely because its context has no principal. The runtime's
// projectCacheLimit callback owns this soft cap; testing it directly with a
// temporary limits store proves both fallback and account selection without Docker.
func TestProjectCacheLimitPrincipalFallback(t *testing.T) {
	t.Run("no principal", func(t *testing.T) {
		got, err := projectCacheLimit(context.Background(), nil)
		if err != nil || got != sandbox.DefaultProjectCacheBytes {
			t.Fatalf("projectCacheLimit() = %d, %v; want %d, nil", got, err, sandbox.DefaultProjectCacheBytes)
		}
	})
	t.Run("account limits", func(t *testing.T) {
		db, err := pebblestore.Open(filepath.Join(t.TempDir(), "limits.pebble"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		for i, account := range []string{"account-a", "account-b"} {
			limits := pebblestore.DefaultLimits()
			limits.ProjectCacheBytes = uint64(i+1) << 30
			if _, err := db.SetLimits(account, 0, limits); err != nil {
				t.Fatal(err)
			}
			ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{
				Type: identity.PrincipalTypeUser, UserID: "test-user", AccountScopeID: account,
			})
			got, err := projectCacheLimit(ctx, db)
			if err != nil || got != limits.ProjectCacheBytes {
				t.Fatalf("projectCacheLimit(%s) = %d, %v; want %d, nil", account, got, err, limits.ProjectCacheBytes)
			}
		}
	})
}
