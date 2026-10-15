package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: exercise automatic reviewer admission through the real HTTP worker
// route, Git worktree allocation and Pebble, not a replacement MCP handler.
// A BLOCK must reach the coder through the real follow-up acceptance route.
func TestReviewPipelineChain(t *testing.T) {
	s, db, h := setupWorkerAPITestServer(t)
	workspaceID := setupWorkerAPIExecution(t, s, db)
	s.workspace = workspace.NewService(store.NewWorkspaceStore(db))
	ids := store.NewIdentityStore(db)
	if _, err := ids.PutCurrentSelection(store.CurrentSelectionRecord{UserID: "user-test"}); err != nil {
		t.Fatal(err)
	}
	s.identitySessions = identity.NewSessionService(ids, store.NewIdentitySessionStore(db))
	s.signals = store.NewSignalStore(db)
	w, r := settledWorkerJobForMCP(t, s, h, workspaceID, "pipeline-chain")
	c, err := s.pipelineCall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	refs, err := c.jobRefs(controlMCPJobWorker, w.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := c.jobView(controlMCPJobWorker, w.ID, r.ID, 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	result := controlMCPJobResultJSON(view)
	dir := controlMCPString(result, "workspace_path")
	if dir == "" {
		t.Fatalf("missing worktree: %v", view)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("git: %v %s", e, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, "change"), []byte("change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "change")
	git("commit", "-m", "change")
	result["head"] = git("rev-parse", "HEAD")
	rule := publish.Rule{Folder: c.jobFolder(refs, result), Pipeline: true, Auto: true, RequireReview: true, Mode: "land"}
	ready, err := c.advancePipeline(w.ID, r.ID, refs, rule, result)
	if err != nil || ready {
		t.Fatalf("automatic reviewer admission: ready=%v err=%v", ready, err)
	}
	var state ReviewPipeline
	if _, err = db.GetJSON(pipelineKey("acct-test", controlMCPJobID(controlMCPJobWorker, w.ID, r.ID)), &state); err != nil {
		t.Fatal(err)
	}
	if state.Next != "review_started" || state.ReviewerJobID == "" {
		t.Fatalf("review not admitted: %+v", state)
	}
}
