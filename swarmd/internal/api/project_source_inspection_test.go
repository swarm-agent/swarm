package api

import "testing"

// Purpose: inspect_source calls a committed source ready even when the
// folder holds files Git does not track (a .env left out of the first
// commit, say): agents work from HEAD in their own worktrees. A change to
// a tracked file still makes the source not clean.
// Threat: the Orchestrator refusing to launch in a folder the person just
// readied, over a file that was deliberately left out of the commit.
func TestSourceCleanlinessIgnoresUntrackedFiles(t *testing.T) {
	clean, untracked := sourceCleanliness("?? .env\n?? notes/\n")
	if !clean || len(untracked) != 2 || untracked[0] != ".env" {
		t.Fatalf("clean=%v untracked=%v", clean, untracked)
	}
	clean, untracked = sourceCleanliness(" M notes.txt\n?? .env\n")
	if clean || len(untracked) != 1 {
		t.Fatalf("clean=%v untracked=%v", clean, untracked)
	}
	if clean, untracked := sourceCleanliness(""); !clean || len(untracked) != 0 {
		t.Fatalf("empty status: clean=%v untracked=%v", clean, untracked)
	}
}
