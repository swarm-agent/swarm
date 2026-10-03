package api

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: direct video status belongs to generation, not chat-session routing.
// A warning plus no session/output must not invent a terminal failure on reads.
// syncTaskSessionState is the list/detail reconciliation boundary; a temporary
// store reload is the narrowest layer proving persisted and presented agreement.
func TestDirectVideoSessionSyncPreservesGenerationStatus(t *testing.T) {
	_, db, p := setupDirectMediaTestServer(t)
	project := &pebblestore.ProjectRecord{ID: "video-status-project", AccountID: p.AccountScopeID, Name: "Video status"}
	if err := db.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"pending", "queued", "in_progress", "needs_review", "completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			task := &pebblestore.ProjectTaskRecord{
				ID: "video-" + status, ProjectID: project.ID, AccountID: p.AccountScopeID,
				Title: "Video", Agent: "video", Status: status, RouterAlert: "Routing warning",
			}
			if status == "failed" {
				task.LastError = "Provider rejected generation"
			}
			if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
				t.Fatal(err)
			}
			loaded, ok, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
			if err != nil || !ok {
				t.Fatalf("reload: ok=%v err=%v", ok, err)
			}
			syncTaskSessionState(loaded, db)
			if loaded.Status != status || loaded.LastError != task.LastError || loaded.RouterAlert != task.RouterAlert {
				t.Fatalf("generation state changed by session reconciliation: %+v", loaded)
			}
		})
	}
	// Preserve the missing-session routing failure contract for agent execution.
	code := &pebblestore.ProjectTaskRecord{Agent: "coder", Status: "in_progress", RouterAlert: "Routing failed"}
	syncTaskSessionState(code, db)
	if code.Status != "failed" || code.LastError != code.RouterAlert {
		t.Fatalf("agent routing failure hidden: %+v", code)
	}
}
