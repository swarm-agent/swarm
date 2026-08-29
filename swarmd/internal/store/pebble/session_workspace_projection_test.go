package pebblestore

import "testing"

func TestNormalizeSessionWorkspaceGrantsPreservesHistoricalAvailability(t *testing.T) {
	unavailable := false
	session := SessionSnapshot{
		WorkspacePath: "/workspace/current",
		WorkspaceName: "current",
		WorkspaceGrants: []WorkspaceGrant{{
			Kind: WorkspaceGrantPrimary, WorkspaceID: "ws-old", WorkspaceGeneration: 7,
			Path: "/workspace/old", Name: "old", Available: &unavailable,
		}},
	}
	grants := NormalizeSessionWorkspaceGrants(session)
	if len(grants) != 1 || grants[0].WorkspaceID != "ws-old" || grants[0].Available == nil || *grants[0].Available {
		t.Fatalf("grants = %#v", grants)
	}
	usage := WorkspaceUsageFromGrants(grants)
	if len(usage) != 1 || usage[0].WorkspaceGeneration != 7 || usage[0].Available == nil || *usage[0].Available {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestNormalizeSessionWorkspaceGrantsBackfillsLegacyRoots(t *testing.T) {
	session := SessionSnapshot{
		WorkspacePath: "/workspace/primary", WorkspaceName: "primary",
		WorktreeEnabled: true, WorktreeRootPath: "/workspace/worktree", WorktreeBranch: "agent/test",
		TemporaryWorkspaceRoots: []string{"/workspace/linked"},
	}
	grants := NormalizeSessionWorkspaceGrants(session)
	if len(grants) != 3 {
		t.Fatalf("grants = %#v", grants)
	}
	if grants[0].Kind != WorkspaceGrantPrimary || grants[1].Kind != WorkspaceGrantWorktree || grants[2].Kind != WorkspaceGrantTemporary {
		t.Fatalf("grant kinds = %#v", grants)
	}
	for _, grant := range grants {
		if grant.Available == nil || !*grant.Available {
			t.Fatalf("legacy live grant availability = %#v", grants)
		}
	}
	if usage := WorkspaceUsageFromGrants(grants); len(usage) != 0 {
		t.Fatalf("anonymous legacy roots must not invent stable usage identities: %#v", usage)
	}
}
