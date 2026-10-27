package permission

import (
	"path/filepath"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: AuthorizeToolCall must distinguish definition reads from writes in
// ask mode; the service/store layer proves actual pending consent records.
func TestCustomAgentPermissions(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "permissions"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(store.NewPermissionStore(db), nil, nil)
	s.SetSessionResolver(workerPromptSessions{})
	for i, action := range []string{"list", "get", "define", "disable"} {
		result, err := s.AuthorizeToolCall(AuthorizationInput{SessionID: "session", AccountScopeID: "account", RunID: "run", CallID: action, Mode: "ask", ToolName: "manage_custom_agents", ToolArguments: `{"action":"` + action + `"}`})
		want := AuthorizationApprove
		if i >= 2 {
			want = AuthorizationPending
		}
		if err != nil || result.Decision != want {
			t.Fatalf("%s: %+v %v", action, result, err)
		}
		count, err := s.PendingCount("session")
		expected := 0
		if i >= 2 {
			expected = i - 1
		}
		if err != nil || count != expected {
			t.Fatalf("pending %d %v", count, err)
		}
	}
}
