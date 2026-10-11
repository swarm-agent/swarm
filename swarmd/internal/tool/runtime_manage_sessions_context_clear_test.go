package tool

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: clearing a conversation's context must mean the agent cannot read
// the cleared history back through its own tools. Before the fix the project
// orchestrator answered "what word did I ask you to remember?" after a clear
// by calling manage_sessions read_messages on its own session. Its own
// session's read_messages (tail/before/after), scoped search and durable_log
// search must stop at the clear boundary; the same reads from another session
// (and the person/UI history) still see everything.
// Owner: Runtime.selfReadContextBoundary over the durable execution epochs
// written by the canonical session.context.clear mutation. The tool runtime
// over the real session service and Pebble store is the narrowest layer that
// joins the agent's scope, the tool read path and the durable boundary.
func TestManageSessionsSelfReadStopsAtContextClear(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "clear.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	store := pebblestore.NewSessionStore(db)
	sessions := sessionruntime.NewService(store, events)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	if err := store.PutProject("account", &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	conversation := pebblestore.SessionSnapshot{ID: "conversation", AccountScopeID: "account", UserID: "user", Title: "Chat", Metadata: map[string]any{
		"swarm_v3_project_id": "project", "project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator",
	}}
	apply := func(key, kind string) pebblestore.V3SessionMutationInput {
		return pebblestore.V3SessionMutationInput{SessionID: conversation.ID, UserID: "user", AccountScopeID: "account", ClientRequestID: key, PayloadHash: key, Kind: kind}
	}
	create := apply("create", pebblestore.V3SessionMutationCreateSession)
	create.Session = &conversation
	if _, err := store.ApplyV3SessionMutation(create); err != nil {
		t.Fatal(err)
	}
	appendMessage := func(id, role, content string) {
		t.Helper()
		input := apply(id, pebblestore.V3SessionMutationAppendMessage)
		input.Message = &pebblestore.MessageSnapshot{ID: id, SessionID: conversation.ID, Role: role, Content: content}
		if _, err := store.ApplyV3SessionMutation(input); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage("remember", "user", "Remember the word PELICAN.")
	appendMessage("ack", "assistant", "Noted: PELICAN.")
	projection, _, err := store.GetV3SessionProjection(conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	clear := apply("clear", pebblestore.V3SessionMutationClearContext)
	clear.ExpectedLastEventSeq = &projection.LastEventSeq
	if _, err := store.ApplyV3SessionMutation(clear); err != nil {
		t.Fatal(err)
	}
	appendMessage("ask", "user", "What word did I ask you to remember?")

	runtime := &Runtime{sessions: sessions}
	call := func(scope WorkspaceScope, args map[string]any) string {
		t.Helper()
		out, err := runtime.executeManageSessions(context.Background(), scope, args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	reads := []map[string]any{
		{"action": "read_messages", "session_id": conversation.ID, "limit": 50},
		{"action": "read_messages", "session_id": conversation.ID, "mode": "before", "before_seq": 1000, "limit": 50},
		{"action": "read_messages", "session_id": conversation.ID, "mode": "after", "after_seq": 0, "limit": 50},
		{"action": "read_messages", "session_id": conversation.ID, "mode": "around", "around_seq": 2, "limit": 50},
		{"action": "search", "session_id": conversation.ID, "query": "pelican"},
		{"action": "search", "search_mode": "durable_log", "session_id": conversation.ID, "query": "pelican"},
	}

	self := WorkspaceScope{Principal: principal, SessionID: conversation.ID}
	for _, args := range reads {
		if out := call(self, args); strings.Contains(strings.ToLower(out), "pelican") {
			t.Fatalf("own session %v leaked cleared history: %s", args, out)
		}
	}
	if out := call(self, reads[0]); !strings.Contains(out, "What word did I ask you to remember?") {
		t.Fatalf("own session lost messages after the clear: %s", out)
	}

	other := WorkspaceScope{Principal: principal, SessionID: "another-session"}
	for _, args := range reads[:3] {
		if out := call(other, args); !strings.Contains(out, "PELICAN") {
			t.Fatalf("read from another session %v lost history: %s", args, out)
		}
	}
	if out := call(other, reads[5]); !strings.Contains(out, "PELICAN") {
		t.Fatalf("durable_log from another session lost history: %s", out)
	}
	if _, history, err := store.ListExecutionEpochMessages(conversation.ID, "epoch-00000000000000000001", 0); err != nil || len(history) != 2 {
		t.Fatalf("durable history changed: %+v %v", history, err)
	}
}
