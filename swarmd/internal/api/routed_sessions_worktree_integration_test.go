package api

import (
	"errors"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func (f *fakeWorktreeService) RollbackAllocation(_ worktreeruntime.Allocation) error { return nil }

type routedWorktreeServiceStub struct {
	fakeWorktreeService
	allocationErrs  []error
	allocationCalls int
}

func (s *routedWorktreeServiceStub) AllocateDetachedWorkspace(workspacePath, nameSeed string) (worktreeruntime.Allocation, error) {
	s.lastWorkspace = workspacePath
	s.lastNameSeed = nameSeed
	call := s.allocationCalls
	s.allocationCalls++
	if call < len(s.allocationErrs) && s.allocationErrs[call] != nil {
		return worktreeruntime.Allocation{}, s.allocationErrs[call]
	}
	return s.fakeWorktreeService.AllocateDetachedWorkspace(workspacePath, nameSeed)
}

func (s *routedWorktreeServiceStub) AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	s.lastBaseBranch = baseBranch
	s.lastBranchName = branchName
	allocation, err := s.AllocateDetachedWorkspace(workspacePath, nameSeed)
	if err != nil {
		return worktreeruntime.Allocation{}, err
	}
	if baseBranch != "" {
		allocation.BaseBranch = baseBranch
	}
	allocation.BranchName = branchName
	return allocation, nil
}

func (s *routedWorktreeServiceStub) AllocateDetachedWorkspaceRequestedForPrincipal(_ identity.Principal, workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	return s.AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName)
}

func TestAllocateRoutedSessionWorktreeUsesConfiguredPrefixAndBase(t *testing.T) {
	stub := &routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
		config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: false, BaseBranch: "dev", BranchName: "router/<id>"},
		allocation: worktreeruntime.Allocation{WorkspacePath: "/managed/router-fix-api", RepoRoot: "/source/repo", BaseBranch: "dev", BranchName: "router/fix-api", WorkspaceID: "router-fix-api"},
	}}
	server := &Server{worktrees: stub}
	allocation, finalRequestedName, err := server.allocateRoutedSessionWorktree(identity.Principal{UserID: "user", AccountScopeID: "account"}, pebblestore.SessionSnapshot{WorkspacePath: "/source/repo"}, "session-1", " Fix API ")
	if err != nil {
		t.Fatalf("allocateRoutedSessionWorktree: %v", err)
	}
	if stub.configReadCount != 1 || stub.allocationCalls != 1 || stub.lastWorkspace != "/source/repo" || stub.lastNameSeed != "session-1" || stub.lastBaseBranch != "dev" || stub.lastBranchName != "router/fix-api" {
		t.Fatalf("allocation calls/config = %+v", stub)
	}
	if allocation.BranchName != "router/fix-api" || finalRequestedName != "Fix API" {
		t.Fatalf("allocation = %+v final requested name=%q", allocation, finalRequestedName)
	}
}

func TestAllocateRoutedSessionWorktreeRejectsDisabledConfig(t *testing.T) {
	stub := &routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{config: worktreeruntime.Config{Enabled: false}}}
	server := &Server{worktrees: stub}
	_, _, err := server.allocateRoutedSessionWorktree(identity.Principal{UserID: "user", AccountScopeID: "account"}, pebblestore.SessionSnapshot{WorkspacePath: "/source/repo"}, "session-1", "Fix API")
	if err == nil || stub.allocationCalls != 0 {
		t.Fatalf("disabled config error=%v allocation calls=%d", err, stub.allocationCalls)
	}
}

func TestAllocateRoutedSessionWorktreeRetriesOnlyOneTypedConflict(t *testing.T) {
	conflict := &worktreeruntime.RequestedWorktreeNameConflictError{WorktreeName: "router/fix-api", Cause: errors.New("taken")}
	tests := []struct {
		name       string
		errs       []error
		wantCalls  int
		wantBranch string
		wantErr    bool
	}{
		{name: "typed conflict retries once", errs: []error{conflict, nil}, wantCalls: 2, wantBranch: "router/fix-api-1"},
		{name: "second typed conflict returns", errs: []error{conflict, conflict}, wantCalls: 2, wantErr: true},
		{name: "non conflict never retries", errs: []error{errors.New("git unavailable")}, wantCalls: 1, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &routedWorktreeServiceStub{
				fakeWorktreeService: fakeWorktreeService{
					config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "router"},
					allocation: worktreeruntime.Allocation{WorkspacePath: "/managed/retry", RepoRoot: "/source/repo", BranchName: test.wantBranch, WorkspaceID: "retry"},
				},
				allocationErrs: test.errs,
			}
			server := &Server{worktrees: stub}
			_, finalRequestedName, err := server.allocateRoutedSessionWorktree(identity.Principal{UserID: "user", AccountScopeID: "account"}, pebblestore.SessionSnapshot{WorkspacePath: "/source/repo"}, "session-1", "Fix API")
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr=%t", err, test.wantErr)
			}
			if stub.allocationCalls != test.wantCalls {
				t.Fatalf("allocation calls = %d, want %d", stub.allocationCalls, test.wantCalls)
			}
			if test.wantBranch != "" && stub.lastBranchName != test.wantBranch {
				t.Fatalf("retry branch = %q, want %q", stub.lastBranchName, test.wantBranch)
			}
			if !test.wantErr && finalRequestedName != "Fix API (1)" {
				t.Fatalf("retry final requested name = %q", finalRequestedName)
			}
			if stub.lastBaseBranch != "" {
				t.Fatalf("current-branch config passed base %q", stub.lastBaseBranch)
			}
		})
	}
}

func TestApplyRoutedSessionWorktreeDecisionOnAllocatesAndAppliesCandidate(t *testing.T) {
	stub := &routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
		config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: false, BaseBranch: "dev", BranchName: "router"},
		allocation: worktreeruntime.Allocation{WorkspacePath: "/managed/router-fix-api", RepoRoot: "/source/repo", BaseBranch: "dev", WorkspaceID: "router-fix-api"},
	}}
	server := &Server{worktrees: stub}
	candidate := pebblestore.SessionSnapshot{WorkspacePath: "/source/repo", WorkspaceName: "repo"}
	routerName := "Fix API"
	allocation, err := server.applyRoutedSessionWorktreeDecision(&candidate, identity.Principal{UserID: "user", AccountScopeID: "account"}, "session-1", true, &routerName)
	if err != nil {
		t.Fatalf("on decision: %v", err)
	}
	if stub.allocationCalls != 1 || candidate.WorkspacePath != allocation.WorkspacePath || !candidate.WorktreeEnabled || candidate.WorktreeBranch != "router/fix-api" {
		t.Fatalf("on decision allocation=%+v candidate=%+v calls=%d", allocation, candidate, stub.allocationCalls)
	}
}

func TestApplyRoutedSessionWorktreeDecisionRequiresRouterName(t *testing.T) {
	stub := &routedWorktreeServiceStub{}
	server := &Server{worktrees: stub}
	candidate := pebblestore.SessionSnapshot{WorkspacePath: "/source/repo"}
	if _, err := server.applyRoutedSessionWorktreeDecision(&candidate, identity.Principal{UserID: "user", AccountScopeID: "account"}, "session-1", true, nil); err == nil {
		t.Fatal("expected missing Router name to fail")
	}
	if stub.configReadCount != 0 || stub.allocationCalls != 0 {
		t.Fatalf("missing name read config=%d allocated=%d", stub.configReadCount, stub.allocationCalls)
	}
}

func TestApplyRoutedSessionWorktreeDecisionOffDoesNotAllocate(t *testing.T) {
	stub := &routedWorktreeServiceStub{}
	server := &Server{worktrees: stub}
	candidate := pebblestore.SessionSnapshot{WorkspacePath: "/source/repo", WorkspaceName: "repo"}
	if _, err := server.applyRoutedSessionWorktreeDecision(&candidate, identity.Principal{UserID: "user", AccountScopeID: "account"}, "session-1", false, nil); err != nil {
		t.Fatalf("off decision: %v", err)
	}
	if stub.configReadCount != 0 || stub.allocationCalls != 0 || candidate.WorktreeEnabled {
		t.Fatalf("off decision config=%d allocation=%d candidate=%+v", stub.configReadCount, stub.allocationCalls, candidate)
	}
	if candidate.Metadata["swarm_v3_source_workspace_path"] != "/source/repo" || candidate.Metadata["swarm_v3_runtime_workspace_path"] != "/source/repo" {
		t.Fatalf("off decision metadata = %+v", candidate.Metadata)
	}
}

func TestApplyRoutedSessionWorktreeAllocationPreservesSourceAndRecordsFacts(t *testing.T) {
	candidate := pebblestore.SessionSnapshot{WorkspacePath: "/source/repo", WorkspaceName: "repo", Metadata: map[string]any{"existing": true}}
	allocation := worktreeruntime.Allocation{WorkspacePath: "/managed/router-fix-api-1", RepoRoot: "/source/repo", BaseBranch: "dev", BaseCommit: "abc123", BranchName: "router/fix-api-1", WorkspaceID: "router-fix-api-1"}
	applyRoutedSessionWorktreeAllocation(&candidate, "/source/repo", " Fix API ", "Fix API (1)", allocation)

	if candidate.WorkspacePath != allocation.WorkspacePath || !candidate.WorktreeEnabled || candidate.WorktreeRootPath != allocation.WorkspacePath || candidate.WorktreeBaseBranch != "dev" || candidate.WorktreeBranch != "router/fix-api-1" {
		t.Fatalf("candidate worktree facts = %+v", candidate)
	}
	want := map[string]any{
		"swarm_v3_source_workspace_path":      "/source/repo",
		"swarm_v3_runtime_workspace_path":     allocation.WorkspacePath,
		"routed_worktree_original_name":       "Fix API",
		"routed_worktree_requested_name":       "Fix API (1)",
		"routed_worktree_final_requested_name": "Fix API (1)",
		"routed_worktree_branch":               "router/fix-api-1",
		"routed_worktree_final_branch":         "router/fix-api-1",
		"workspace_id":                         "router-fix-api-1",
		"base_commit":                          "abc123",
		"existing":                             true,
	}
	for key, value := range want {
		if candidate.Metadata[key] != value {
			t.Fatalf("metadata[%q] = %#v, want %#v; metadata=%+v", key, candidate.Metadata[key], value, candidate.Metadata)
		}
	}
}
