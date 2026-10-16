package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/worktree"
)

// Purpose: publisher identity must survive review allocation's Git routing into
// the real sandbox cache admission boundary. Stop at cache admission so this
// focused regression needs neither Docker nor a provider. An absent principal
// must still be rejected before any Git command can execute.
func TestPublishReviewCheckoutPrincipal(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := s.pipelineCall(ctx)
	if err != nil {
		t.Fatal(err)
	}
	principal, ok := identity.PrincipalFromContext(c.request.Context())
	if !ok {
		t.Fatal("publisher has no trusted principal")
	}
	reached := errors.New("trusted review cache admission reached")
	reads := 0
	engine := &landPrincipalEngine{}
	m, err := sandbox.NewManager(ctx, sandbox.Config{Mode: sandbox.ModeRequired, Memory: "4g", MaxWorkerRuns: 2, UID: 10001, GID: 10001, StateDir: t.TempDir(), ProjectCacheLimit: func(ctx context.Context) (uint64, error) {
		reads++
		got, ok := identity.PrincipalFromContext(ctx)
		if !ok {
			return 0, identity.ErrPrincipalRequired
		}
		if got.UserID != principal.UserID || got.AccountScopeID != principal.AccountScopeID {
			return 0, errors.New("wrong review principal")
		}
		return 0, reached
	}}, engine)
	if err != nil {
		t.Fatal(err)
	}
	old := sandbox.Default()
	sandbox.SetDefault(m)
	defer sandbox.SetDefault(old)
	trees := &worktree.Service{}
	folder := t.TempDir()
	m.SetLayout(sandbox.Layout{Projects: func() ([]string, error) { return []string{folder}, nil }})
	_, err = trees.AllocateReviewWorkspace(principal, folder, "review-principal", strings.Repeat("a", 40))
	if err == nil || !strings.Contains(err.Error(), reached.Error()) || reads == 0 {
		t.Fatalf("review lost publisher identity: reads=%d err=%v", reads, err)
	}
	before := reads
	_, err = trees.AllocateReviewWorkspace(identity.Principal{}, folder, "external-review", strings.Repeat("a", 40))
	if !errors.Is(err, identity.ErrPrincipalRequired) || reads != before || engine.executed {
		t.Fatalf("unauthenticated allocation reached execution: reads=%d err=%v", reads, err)
	}
}
