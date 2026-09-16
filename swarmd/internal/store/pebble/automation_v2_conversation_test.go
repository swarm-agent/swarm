package pebblestore

import (
	"testing"
)

// Requirement: occurrence transcripts remain immutable user-input targets even
// after execution ends; ordinary chats and scheduler-owned assistant output work.
// Threat: free-form turns or metadata removal bypass automation isolation.
// Authority: ApplyV3SessionMutation -> guardAutomationSessionMutation. A real
// temporary store proves rejection publishes no event rather than an HTTP status.
func TestAutomationV2OccurrenceConversationIsolation(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	for _, id := range []string{"occurrence", "ordinary"} {
		metadata := map[string]any{}
		if id == "occurrence" {
			metadata["automation_v2_occurrence_id"] = "slot"
		}
		if err := s.CreateSession(SessionSnapshot{ID: id, UserID: "user", AccountScopeID: "account", Metadata: metadata}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		id, role string
		denied   bool
	}{{"occurrence", "user", true}, {"occurrence", "assistant", false}, {"ordinary", "user", false}} {
		before, err := s.readV3SessionSequence(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		key := tc.id + tc.role
		_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: tc.id, UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationAppendMessage, ClientRequestID: key, PayloadHash: key, Message: &MessageSnapshot{ID: key, Role: tc.role, Content: "bounded fixture"}})
		if (err != nil) != tc.denied {
			t.Fatalf("%s/%s denial=%v: %v", tc.id, tc.role, tc.denied, err)
		}
		after, err := s.readV3SessionSequence(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if tc.denied && after != before {
			t.Fatal("denied message published an event")
		}
		if !tc.denied && after != before+1 {
			t.Fatal("allowed message missing")
		}
	}
	current, _, err := s.GetSession("occurrence")
	if err != nil {
		t.Fatal(err)
	}
	current.Metadata = map[string]any{}
	before, _ := s.readV3SessionSequence(current.ID)
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: current.ID, UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationUpdateMetadata, ClientRequestID: "remove-attribution", PayloadHash: "remove-attribution", Session: &current})
	if err == nil {
		t.Fatal("occurrence attribution removed")
	}
	after, _ := s.readV3SessionSequence(current.ID)
	if before != after {
		t.Fatal("rejected attribution edit published an event")
	}
}
