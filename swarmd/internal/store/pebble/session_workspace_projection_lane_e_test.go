package pebblestore

import "testing"

// TestLaneE_E2E045DuplicateWorkspaceGrantReplayConverges covers
// E2E-045/REQ-V3-004: replayed or duplicated grant facts cannot duplicate
// hydrated authority or the path-free usage projection.
func TestLaneE_E2E045DuplicateWorkspaceGrantReplayConverges(t *testing.T) {
	available := true
	session := SessionSnapshot{WorkspaceGrants: []WorkspaceGrant{
		{Kind: WorkspaceGrantPrimary, WorkspaceID: "ws-primary", WorkspaceGeneration: 7, Path: "/workspace", Name: "Primary", Available: &available},
		{Kind: WorkspaceGrantTemporary, Path: "/external", Available: &available},
		{Kind: WorkspaceGrantPrimary, WorkspaceID: "ws-primary", WorkspaceGeneration: 7, Path: "/workspace", Name: "Primary", Available: &available},
		{Kind: WorkspaceGrantTemporary, Path: "/external", Available: &available},
	}}

	grants := NormalizeSessionWorkspaceGrants(session)
	if len(grants) != 2 {
		t.Fatalf("normalized grants = %+v, want one primary and one temporary", grants)
	}
	if grants[0].Kind != WorkspaceGrantPrimary || grants[1].Kind != WorkspaceGrantTemporary {
		t.Fatalf("normalized grant order = %+v", grants)
	}
	usage := WorkspaceUsageFromGrants(grants)
	if len(usage) != 1 || usage[0].WorkspaceID != "ws-primary" || usage[0].WorkspaceGeneration != 7 {
		t.Fatalf("workspace usage = %+v, want one path-free primary identity", usage)
	}
}

// TestLaneE_E2E045HydrationOrderMismatchConverges covers the hydration side of
// E2E-045: equivalent grants received in a different order normalize to the
// same deterministic projection without elevating anonymous temporary paths.
func TestLaneE_E2E045HydrationOrderMismatchConverges(t *testing.T) {
	available := true
	forward := SessionSnapshot{WorkspaceGrants: []WorkspaceGrant{
		{Kind: WorkspaceGrantTemporary, Path: "/z", Available: &available},
		{Kind: WorkspaceGrantPrimary, WorkspaceID: "ws-b", WorkspaceGeneration: 2, Path: "/b", Name: "B", Available: &available},
		{Kind: WorkspaceGrantWorktree, WorkspaceID: "ws-a", WorkspaceGeneration: 1, Path: "/a", Name: "A", Available: &available},
	}}
	reverse := SessionSnapshot{WorkspaceGrants: []WorkspaceGrant{forward.WorkspaceGrants[2], forward.WorkspaceGrants[1], forward.WorkspaceGrants[0]}}

	left := NormalizeSessionWorkspaceGrants(forward)
	right := NormalizeSessionWorkspaceGrants(reverse)
	if len(left) != len(right) {
		t.Fatalf("normalized lengths differ: left=%+v right=%+v", left, right)
	}
	for i := range left {
		if left[i].Kind != right[i].Kind || left[i].WorkspaceID != right[i].WorkspaceID || left[i].Path != right[i].Path {
			t.Fatalf("normalized hydration mismatch at %d: left=%+v right=%+v", i, left, right)
		}
	}
	leftUsage, rightUsage := WorkspaceUsageFromGrants(left), WorkspaceUsageFromGrants(right)
	if len(leftUsage) != 2 || len(rightUsage) != 2 {
		t.Fatalf("anonymous temporary grant leaked into usage: left=%+v right=%+v", leftUsage, rightUsage)
	}
	for i := range leftUsage {
		if leftUsage[i] != rightUsage[i] {
			t.Fatalf("usage mismatch at %d: left=%+v right=%+v", i, leftUsage, rightUsage)
		}
	}
}
