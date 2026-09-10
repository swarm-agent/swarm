package run

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: selectRecoveryPaths must keep inspection bounded beyond 100 registered
// lanes while exact lookup still passes through authorizedRecoveryCandidates.
// Callback assertions prove foreign paths never reach content reads; this is the
// narrow consumer boundary, not a substitute for Git/path or storage CAS tests.
func TestRecoveryDiscoveryBoundsAndExactOwnership(t *testing.T) {
	root := t.TempDir()
	paths := make([]string, 125)
	for i := range paths {
		paths[i] = filepath.Join(root, fmt.Sprintf("lane-%03d", i))
	}
	page, truncated, err := selectRecoveryPaths(paths, "")
	if err != nil || !truncated || len(page) != 100 {
		t.Fatalf("page=%d truncated=%v err=%v", len(page), truncated, err)
	}
	target := paths[124]
	selected, truncated, err := selectRecoveryPaths(paths, target)
	if err != nil || truncated || len(selected) != 1 || selected[0] != target {
		t.Fatalf("exact=%v truncated=%v err=%v", selected, truncated, err)
	}
	reads := 0
	for _, foreign := range []bool{true, false} {
		rows, err := authorizedRecoveryCandidates(selected, func(path string) ([]pebblestore.WorktreeOwnership, error) {
			if foreign {
				return nil, pebblestore.ErrWorktreeRecoveryConflict
			}
			return []pebblestore.WorktreeOwnership{{Path: path, OwnerSessionID: "owner", Revision: 9}}, nil
		}, func(path string) (worktreeruntime.RecoveryIdentity, error) {
			reads++
			return worktreeruntime.RecoveryIdentity{HEAD: "head", Fingerprint: "fp"}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if foreign && (reads != 0 || len(rows) != 0) {
			t.Fatal("foreign content inspected or disclosed")
		}
		if !foreign && (reads != 1 || len(rows) != 1 || rows[0].OwnershipRevision != 9 || rows[0].Fingerprint != "fp") {
			t.Fatalf("owned result=%+v", rows)
		}
	}
	for _, bad := range []string{"relative", root + "/../escape"} {
		if _, _, err := selectRecoveryPaths(paths, bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	absent, _, err := selectRecoveryPaths(paths, filepath.Join(root, "absent"))
	if err != nil || len(absent) != 0 {
		t.Fatalf("absent=%v err=%v", absent, err)
	}
}

// Purpose: an unreadable owned lane must not terminate the inventory or provide
// usable recovery evidence. The narrow callback layer injects inspection failure
// and proves a later lane still supplies its independent exact evidence.
func TestRecoveryDiscoveryInspectionDiagnostic(t *testing.T) {
	rows, err := authorizedRecoveryCandidates([]string{"broken", "healthy"}, func(path string) ([]pebblestore.WorktreeOwnership, error) {
		return []pebblestore.WorktreeOwnership{{Path: path, OwnerSessionID: "owner", Revision: 1}}, nil
	}, func(path string) (worktreeruntime.RecoveryIdentity, error) {
		if path == "broken" {
			return worktreeruntime.RecoveryIdentity{}, errors.New("private error detail")
		}
		return worktreeruntime.RecoveryIdentity{HEAD: "head", Fingerprint: "fp"}, nil
	})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows[0].Diagnostic == "" || rows[0].HEAD != "" || rows[0].Fingerprint != "" || rows[1].Fingerprint != "fp" {
		t.Fatalf("rows=%+v", rows)
	}
}

// Purpose: discovery exact paths cannot silently normalize malformed selectors
// into a broad inventory. Exercise the actual tool argument parser.
func TestRecoveryDiscoveryExactArgument(t *testing.T) {
	for _, value := range []string{`null`, `7`, `""`, `" /lane"`} {
		if _, err := parseManageWorkspaceArguments(`{"action":"discover_worktrees","worktree_path":` + value + `}`); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	args, err := parseManageWorkspaceArguments(`{"action":"discover_worktrees","worktree_path":"/lane","workspace_id":"workspace","workspace_generation":1}`)
	if err != nil || args.WorktreePath != "/lane" {
		t.Fatalf("args=%+v err=%v", args, err)
	}
}
