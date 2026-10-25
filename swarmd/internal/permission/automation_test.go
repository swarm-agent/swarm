package permission

import (
	"path/filepath"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: automationPolicyIdentity and AuthorizeToolCall must allow worker
// reads in ask mode without durable consent requests, while mutations still ask.
// The permission service with a temporary store is the narrowest proof of both
// decisions and pending-request postconditions.
func TestWorkerReadActionsDoNotPrompt(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "permissions"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := NewService(store.NewPermissionStore(db), nil, nil)
	svc.SetSessionResolver(workerPromptSessions{})
	for _, action := range []string{"inspect", "review", "progress", "help", "create"} {
		result, err := svc.AuthorizeToolCall(AuthorizationInput{SessionID: "session", AccountScopeID: "account", RunID: "run", CallID: action, Mode: "ask", ToolName: "manage_workers", ToolArguments: `{"action":"` + action + `"}`})
		want, pending := AuthorizationApprove, 0
		if action == "create" {
			want, pending = AuthorizationPending, 1
		}
		if err != nil || result.Decision != want {
			t.Fatalf("%s: %+v %v", action, result, err)
		}
		count, err := svc.PendingCount("session")
		if err != nil || count != pending {
			t.Fatalf("%s pending=%d err=%v", action, count, err)
		}
	}
}
