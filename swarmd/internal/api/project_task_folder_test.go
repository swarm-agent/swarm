package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: captureFolderTaskBase/Delivery pin the folder's commit range, not
// untracked files or subsequent commits. Real Git is the narrowest layer that
// proves ancestry rejection and immutable result enumeration without providers.
func TestFolderTaskDelivery(t *testing.T) {
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "main")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "base")
	base := outboxPinGit(t, folder, "rev-parse", "HEAD")
	task := &store.ProjectTaskRecord{Agent: "designer", SessionID: "design", WorkspacePath: folder, SourceWorkspace: store.ProjectTaskSource{Path: folder}}
	if err := captureFolderTaskBase(task); err != nil {
		t.Fatal(err)
	}
	if task.BaseCommit != base || task.BaseBranch != "main" || task.WorktreeBranch != "" {
		t.Fatalf("base: %+v", task)
	}
	for _, name := range []string{"card.html", ".env"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outboxPinGit(t, folder, "add", "card.html")
	outboxPinGit(t, folder, "commit", "-q", "-m", "design")
	head := outboxPinGit(t, folder, "rev-parse", "HEAD")
	if err := captureFolderTaskDelivery(task); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "later")
	if err := captureFolderTaskDelivery(task); err != nil {
		t.Fatal(err)
	}
	state := inspectTaskGitState(*task, nil)
	if state.deliveryAssessment.State != "in_folder" || state.deliveryAssessment.SourceOID != head || len(state.deliveryAssessment.AllowedActions) != 0 {
		t.Fatalf("delivery: %+v", state.deliveryAssessment)
	}
	g, err := taskResultGit(context.Background(), folder, task.BaseCommit, state.deliveryAssessment.SourceOID)
	if err != nil || len(g.Commits) != 1 || g.Commits[0].SHA != head || len(g.Files) != 1 || g.Files[0] != "card.html" {
		t.Fatalf("result: %+v %v", g, err)
	}
	task.Integration = nil
	outboxPinGit(t, folder, "checkout", "-q", "-b", "other")
	if err := captureFolderTaskDelivery(task); err == nil || task.Integration != nil {
		t.Fatal("changed branch produced a receipt")
	}
}
