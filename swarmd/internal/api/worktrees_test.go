package api

import (
	"context"

	"net/http"

	"strings"

	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"

	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type fakeWorktreeService struct {
	config          worktreeruntime.Config
	allocation      worktreeruntime.Allocation
	managed         []worktreeruntime.ManagedWorktree
	managedErr      error
	prune           worktreeruntime.PruneResult
	attachBranch    *string
	allocationErr   error
	configReadCount int
	lastWorkspace   string
	lastNameSeed    string
	lastBaseBranch  string
	lastBranchName  string
	inspectStates   map[string]worktreeruntime.TaskWorkspaceState
}

func (f *fakeWorktreeService) GetConfig(workspacePath string) (worktreeruntime.Config, error) {
	f.configReadCount++
	cfg := f.config
	cfg.WorkspacePath = workspacePath
	return cfg, nil
}

func (f *fakeWorktreeService) GetConfigForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.Config, error) {
	return f.GetConfig(workspacePath)
}

// Keep the overview fixture aligned with the API's exact saved-root read contract.
// Account and path rejection are exercised against the real service in
// worktree.TestSavedWorkspaceConfigExactAccountRead, not this response-only fake.
func (f *fakeWorktreeService) GetConfigForSavedWorkspaceForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.Config, error) {
	return f.GetConfig(workspacePath)
}

func (f *fakeWorktreeService) SetConfig(workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (worktreeruntime.Config, *pebblestore.EventEnvelope, error) {
	f.config = worktreeruntime.Config{WorkspacePath: workspacePath, Enabled: enabled, UseCurrentBranch: useCurrentBranch, BaseBranch: baseBranch, BranchName: branchName}
	return f.config, nil, nil
}

func (f *fakeWorktreeService) SetConfigForPrincipal(principal identity.Principal, workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (worktreeruntime.Config, *pebblestore.EventEnvelope, error) {
	return f.SetConfig(workspacePath, enabled, useCurrentBranch, baseBranch, branchName)
}

func (f *fakeWorktreeService) AllocateDetachedWorkspace(workspacePath, nameSeed string) (worktreeruntime.Allocation, error) {
	f.lastWorkspace = workspacePath
	f.lastNameSeed = nameSeed
	if f.allocationErr != nil {
		return worktreeruntime.Allocation{}, f.allocationErr
	}
	allocation := f.allocation
	if strings.TrimSpace(allocation.RepoRoot) == "" {
		allocation.RepoRoot = workspacePath
	}
	if strings.TrimSpace(allocation.WorkspaceID) == "" {
		allocation.WorkspaceID = worktreeruntime.WorkspaceIdentityForSession(nameSeed)
	}
	if strings.TrimSpace(allocation.BranchName) != "" {
		if branchWorkspaceID, err := worktreeruntime.WorkspaceIdentityForRequestedBranch(allocation.BranchName); err == nil {
			allocation.WorkspaceID = branchWorkspaceID
		}
	}
	if strings.TrimSpace(allocation.WorkspacePath) == "" {
		allocation.WorkspacePath = strings.TrimRight(allocation.RepoRoot, "/") + "/.swarm/worktrees/" + allocation.WorkspaceID
	}
	if strings.TrimSpace(allocation.BaseBranch) == "" {
		allocation.BaseBranch = "main"
	}
	return allocation, nil
}

func (f *fakeWorktreeService) AllocateDetachedWorkspaceForPrincipal(principal identity.Principal, workspacePath, nameSeed string) (worktreeruntime.Allocation, error) {
	return f.AllocateDetachedWorkspace(workspacePath, nameSeed)
}

func (f *fakeWorktreeService) AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	f.lastBaseBranch = strings.TrimSpace(baseBranch)
	f.lastBranchName = strings.TrimSpace(branchName)
	allocation, err := f.AllocateDetachedWorkspace(workspacePath, nameSeed)
	if err != nil {
		return worktreeruntime.Allocation{}, err
	}
	if strings.TrimSpace(baseBranch) != "" {
		allocation.BaseBranch = strings.TrimSpace(baseBranch)
	}
	if strings.TrimSpace(branchName) != "" {
		allocation.BranchName = strings.TrimSpace(branchName)
		if branchWorkspaceID, err := worktreeruntime.WorkspaceIdentityForRequestedBranch(allocation.BranchName); err == nil {
			allocation.WorkspaceID = branchWorkspaceID
			allocation.WorkspacePath = strings.TrimRight(allocation.RepoRoot, "/") + "/.swarm/worktrees/" + allocation.WorkspaceID
		}
	}
	return allocation, nil
}

func (f *fakeWorktreeService) AllocateDetachedWorkspaceRequestedForPrincipal(principal identity.Principal, workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	return f.AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName)
}

func (f *fakeWorktreeService) AttachBranch(workspacePath, sessionID, title string) (string, error) {
	if f.attachBranch != nil {
		return *f.attachBranch, nil
	}
	return "agent/test", nil
}

func (f *fakeWorktreeService) ListManaged(workspacePath string) ([]worktreeruntime.ManagedWorktree, error) {
	return f.managed, f.managedErr
}

func (f *fakeWorktreeService) ListManagedForPrincipal(principal identity.Principal, workspacePath string) ([]worktreeruntime.ManagedWorktree, error) {
	return f.ListManaged(workspacePath)
}

func (f *fakeWorktreeService) PruneManaged(workspacePath string) (worktreeruntime.PruneResult, error) {
	return f.prune, nil
}

func (f *fakeWorktreeService) PruneManagedForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.PruneResult, error) {
	return f.PruneManaged(workspacePath)
}

func (f *fakeWorktreeService) InspectTaskWorkspace(workspacePath string) (worktreeruntime.TaskWorkspaceState, error) {
	if state, ok := f.inspectStates[workspacePath]; ok {
		return state, nil
	}
	branch := strings.TrimSpace(f.lastBranchName)
	if branch == "" {
		branch = strings.TrimSpace(f.allocation.BranchName)
	}
	return worktreeruntime.TaskWorkspaceState{WorkspacePath: workspacePath, BranchName: branch, Clean: true}, nil
}

func testPrincipal() identity.Principal {
	actor := testActorContext()
	principal, err := identity.PrincipalFromActor(actor)
	if err != nil {
		return identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
	}
	return principal
}

func testActorContext() identity.ActorContext {
	now := time.Unix(1_700_000_000, 0).UTC()
	user := pebblestore.UserRecord{ID: "user-1", Username: "test-user", AccountScopeID: "account-1", CreatedAt: now, UpdatedAt: now}
	account := pebblestore.AccountScopeRecord{ID: "account-1", Type: "personal", CreatedByUserID: "user-1", UserID: "user-1", Role: "owner", CreatedAt: now, UpdatedAt: now}
	accountUser := pebblestore.AccountUserRecord{ID: "acct-user-1", AccountScopeID: "account-1", UserID: "user-1", Status: "active", CreatedAt: now, UpdatedAt: now}
	selection := pebblestore.CurrentSelectionRecord{UserID: "user-1", TeamID: "team-1", CreatedAt: now, UpdatedAt: now}
	team := pebblestore.TeamRecord{ID: "team-1", AccountScopeID: "account-1", Name: "Test Team", Default: true, CreatedAt: now, UpdatedAt: now}
	membership := pebblestore.TeamMembershipRecord{TeamID: "team-1", UserID: "user-1", Role: "owner", CreatedAt: now, UpdatedAt: now}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1", AccountScopeSource: identity.AccountScopeSourceSession, User: user, AccountScope: account, AccountUser: accountUser, TokenExpires: now.Add(time.Hour)}
	return identity.ActorContext{Principal: principal, UserID: "user-1", AccountScopeID: "account-1", TeamID: "team-1", User: user, AccountScope: account, AccountUser: accountUser, Team: team, Membership: membership, Selection: selection, TokenExpires: now.Add(time.Hour)}
}

func withTestPrincipal(req *http.Request) *http.Request {
	actor := testActorContext()
	ctx := context.WithValue(req.Context(), productActorRequestContextKey, actor)
	ctx = context.WithValue(ctx, productPrincipalRequestContextKey, actor.Principal)
	ctx = identity.ContextWithPrincipal(ctx, actor.Principal)
	return req.WithContext(ctx)
}
