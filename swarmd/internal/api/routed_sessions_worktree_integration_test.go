package api

import (
	"errors"
	"regexp"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func (f *fakeWorktreeService) RollbackAllocation(_ worktreeruntime.Allocation) error { return nil }

type routedWorktreeServiceStub struct {
	fakeWorktreeService
	allocationErrs     []error
	allocationCalls    int
	allocationBranches []string
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
	s.allocationBranches = append(s.allocationBranches, branchName)
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

func TestAllocateRoutedSessionWorktreeIgnoresObsoleteDisabledConfig(t *testing.T) {
	stub := &routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
		config:     worktreeruntime.Config{Enabled: false, UseCurrentBranch: false, BaseBranch: "dev", BranchName: "router"},
		allocation: worktreeruntime.Allocation{WorkspacePath: "/managed/router-fix-api", RepoRoot: "/source/repo", BaseBranch: "dev", WorkspaceID: "router-fix-api"},
	}}
	server := &Server{worktrees: stub}
	allocation, _, err := server.allocateRoutedSessionWorktree(identity.Principal{UserID: "user", AccountScopeID: "account"}, pebblestore.SessionSnapshot{WorkspacePath: "/source/repo"}, "session-1", "Fix API")
	if err != nil {
		t.Fatalf("allocate routed worktree with obsolete disabled config: %v", err)
	}
	if stub.configReadCount != 1 || stub.allocationCalls != 1 || allocation.WorkspacePath != "/managed/router-fix-api" || stub.lastBaseBranch != "dev" || stub.lastBranchName != "router/fix-api" {
		t.Fatalf("allocation=%+v config reads=%d calls=%d base=%q branch=%q", allocation, stub.configReadCount, stub.allocationCalls, stub.lastBaseBranch, stub.lastBranchName)
	}
}

func TestAllocateRoutedSessionWorktreeRetriesOnlyOneTypedConflict(t *testing.T) {
	conflict := &worktreeruntime.RequestedWorktreeNameConflictError{WorktreeName: "router/fix-api", Cause: errors.New("taken")}
	tests := []struct {
		name      string
		errs      []error
		wantCalls int
		wantErr   bool
	}{
		{name: "typed conflict retries once", errs: []error{conflict, nil}, wantCalls: 2},
		{name: "second typed conflict returns", errs: []error{conflict, conflict}, wantCalls: 2, wantErr: true},
		{name: "non conflict never retries", errs: []error{errors.New("git unavailable")}, wantCalls: 1, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &routedWorktreeServiceStub{
				fakeWorktreeService: fakeWorktreeService{
					config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "router"},
					allocation: worktreeruntime.Allocation{WorkspacePath: "/managed/retry", RepoRoot: "/source/repo", WorkspaceID: "retry"},
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
			if len(stub.allocationBranches) == 0 || stub.allocationBranches[0] != "router/fix-api" {
				t.Fatalf("first allocation branches = %v, want unchanged router/fix-api", stub.allocationBranches)
			}
			if test.wantCalls == 2 {
				retryBranch := stub.allocationBranches[1]
				if matched, _ := regexp.MatchString(`^router/fix-api-[1-9][0-9]{4}$`, retryBranch); !matched {
					t.Fatalf("retry branch = %q, want five-digit identifier", retryBranch)
				}
				if !test.wantErr {
					if matched, _ := regexp.MatchString(`^Fix API [1-9][0-9]{4}$`, finalRequestedName); !matched {
						t.Fatalf("retry final requested name = %q, want five-digit identifier", finalRequestedName)
					}
				}
			}
			if stub.lastBaseBranch != "" {
				t.Fatalf("current-branch config passed base %q", stub.lastBaseBranch)
			}
		})
	}
}

func TestApplyRoutedSessionWorktreeDecisionAlwaysAllocatesAndUsesRequestedName(t *testing.T) {
	stub := &routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
		config:     worktreeruntime.Config{Enabled: false, UseCurrentBranch: false, BaseBranch: "dev", BranchName: "agent"},
		allocation: worktreeruntime.Allocation{WorkspacePath: "/managed/fix-api", RepoRoot: "/source/repo", BaseBranch: "dev", WorkspaceID: "fix-api"},
	}}
	server := &Server{worktrees: stub}
	candidate := pebblestore.SessionSnapshot{ID: "session-1", WorkspacePath: "/source/repo", WorkspaceName: "repo"}
	allocation, err := server.applyRoutedSessionWorktreeDecision(&candidate, identity.Principal{UserID: "user", AccountScopeID: "account"}, "session-1", "Fix API")
	if err != nil {
		t.Fatalf("mandatory lane decision: %v", err)
	}
	if stub.allocationCalls != 1 || candidate.WorkspacePath != "/source/repo" || candidate.WorktreeRootPath != allocation.WorkspacePath || !candidate.WorktreeEnabled || candidate.WorktreeBranch != "agent/fix-api" {
		t.Fatalf("mandatory allocation=%+v candidate=%+v calls=%d", allocation, candidate, stub.allocationCalls)
	}
}

func TestApplyRoutedSessionWorktreeDecisionUsesDeterministicFallbackWithoutRouter(t *testing.T) {
	stub := &routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
		config:     worktreeruntime.Config{UseCurrentBranch: true, BranchName: "agent"},
		allocation: worktreeruntime.Allocation{WorkspacePath: "/managed/fallback", RepoRoot: "/source/repo", WorkspaceID: "fallback"},
	}}
	server := &Server{worktrees: stub}
	candidate := pebblestore.SessionSnapshot{ID: "abcdef1234567890", WorkspacePath: "/source/repo", WorkspaceName: "repo"}
	if _, err := server.applyRoutedSessionWorktreeDecision(&candidate, identity.Principal{UserID: "user", AccountScopeID: "account"}, "abcdef1234567890", ""); err != nil {
		t.Fatalf("fallback decision: %v", err)
	}
	if stub.lastBranchName != "agent/session-abcdef123456" || candidate.Metadata["routed_worktree_original_name"] != "session-abcdef123456" {
		t.Fatalf("fallback branch=%q metadata=%+v", stub.lastBranchName, candidate.Metadata)
	}
}

func TestApplyRoutedSessionWorktreeAllocationPreservesSourceAndRecordsFacts(t *testing.T) {
	candidate := pebblestore.SessionSnapshot{ID: "session-1", WorkspacePath: "/source/repo", WorkspaceName: "repo", Metadata: map[string]any{"existing": true}}
	allocation := worktreeruntime.Allocation{WorkspacePath: "/managed/router-fix-api-12345", RepoRoot: "/source/repo", BaseBranch: "dev", BaseCommit: "abc123", BranchName: "router/fix-api-12345", WorkspaceID: "router-fix-api-12345"}
	applyRoutedSessionWorktreeAllocation(&candidate, "/source/repo", " Fix API ", "Fix API 12345", allocation)

	if candidate.WorkspacePath != "/source/repo" || !candidate.WorktreeEnabled || candidate.WorktreeRootPath != allocation.WorkspacePath || candidate.WorktreeBaseBranch != "dev" || candidate.WorktreeBranch != "router/fix-api-12345" {
		t.Fatalf("candidate worktree facts = %+v", candidate)
	}
	for _, legacyKey := range []string{"routed_worktree_final_requested_name", "routed_worktree_final_branch"} {
		if _, ok := candidate.Metadata[legacyKey]; ok {
			t.Fatalf("legacy metadata %q remains: %+v", legacyKey, candidate.Metadata)
		}
	}
	want := map[string]any{
		"swarm_v3_source_workspace_path":     "/source/repo",
		"swarm_v3_runtime_workspace_path":    allocation.WorkspacePath,
		"swarm_v3_mandatory_worktree":        true,
		"swarm_v3_worktree_owner_session_id": "session-1",
		"swarm_v3_worktree_base_commit":      "abc123",
		"routed_worktree_name":               "Fix API 12345",
		"routed_worktree_original_name":      "Fix API",
		"routed_worktree_requested_name":     "Fix API 12345",
		"routed_worktree_branch":             "router/fix-api-12345",
		"base_commit":                        "abc123",
		"existing":                           true,
	}
	for key, value := range want {
		if candidate.Metadata[key] != value {
			t.Fatalf("metadata[%q] = %#v, want %#v; metadata=%+v", key, candidate.Metadata[key], value, candidate.Metadata)
		}
	}
}
