package pebblestore

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// Requirement: message selections bind only principal-owned ready native revisions
// and exact Parts. Threat: forged/mixed references or stale targets reaching a run.
// The store layer is the narrowest shared authority for routed and append APIs.
func TestNativeArtifactMessageSelectionAuthority(t *testing.T) {
	sessions := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, sessions, "native-selection")
	service, err := NewArtifactV3Service(sessions, t.TempDir(), ArtifactV3Limits{MaxFiles: 256, MaxParts: 256})
	if err != nil {
		t.Fatal(err)
	}
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "native-selection"}
	created, err := service.Create(context.Background(), ArtifactV3CreateInput{Owner: owner, ArtifactID: "native-artifact", TransactionID: "genesis", Project: artifactV3TestProject(t, "Starter", "free"), Build: preparedArtifactV3Evidence("build"), Preview: preparedArtifactV3Evidence("preview")})
	if err != nil {
		t.Fatal(err)
	}
	in := SessionArtifactSelectionReference{SessionID: owner.SessionID, ArtifactID: created.Repository.ArtifactID, RevisionRef: "revision-" + created.Revision.CommitOID, TargetPartIDs: &[]string{"pricing"}, Label: "forged", Action: "use", CommitOID: "forged", ProjectionSeq: 999}
	got, err := sessions.ValidateSessionArtifactMessageSelections(owner.AccountScopeID, owner.UserID, []SessionArtifactSelectionReference{in})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].CommitOID != created.Revision.CommitOID || got[0].ProjectionSeq != created.Repository.EventSeq || got[0].Label == "forged" || !reflect.DeepEqual(*got[0].TargetPartIDs, []string{"pricing"}) {
		t.Fatalf("selection=%+v", got)
	}
	createV3SessionForTest(t, sessions, "selection-chat")
	mutation := V3SessionMutationInput{SessionID: "selection-chat", AccountScopeID: owner.AccountScopeID, UserID: owner.UserID, IdempotencyKey: "selected-message", RequestHash: "selected-message-hash", Kind: V3SessionMutationAppendMessage, Message: &MessageSnapshot{Role: "user", Content: "Make it blue.", ArtifactSelections: got}}
	applied, err := sessions.ApplyV3SessionMutation(mutation)
	if err != nil || applied.Message == nil || applied.Message.Content != "Make it blue." || !reflect.DeepEqual(applied.Message.ArtifactSelections, got) {
		t.Fatalf("durable selection: %+v %v", applied.Message, err)
	}
	replayed, err := sessions.ApplyV3SessionMutation(mutation)
	if err != nil || replayed.PrimarySeq != applied.PrimarySeq {
		t.Fatalf("retry changed message: %+v %v", replayed, err)
	}
	before, _, err := sessions.GetSession("selection-chat")
	if err != nil {
		t.Fatal(err)
	}
	invalid := mutation
	invalid.IdempotencyKey, invalid.RequestHash = "invalid-message", "invalid-message-hash"
	invalid.Message = &MessageSnapshot{Role: "user", Content: "Rejected", ArtifactSelections: []SessionArtifactSelectionReference{in}}
	invalid.Message.ArtifactSelections[0].TargetPartIDs = &[]string{"unknown"}
	if _, err := sessions.ApplyV3SessionMutation(invalid); err == nil {
		t.Fatal("mutation accepted invalid target")
	}
	afterSession, _, err := sessions.GetSession("selection-chat")
	if err != nil || !reflect.DeepEqual(before, afterSession) {
		t.Fatal("invalid selection partially mutated session")
	}
	cases := []struct {
		name          string
		alter         func(*SessionArtifactSelectionReference)
		account, user string
	}{
		{name: "foreign-account", account: "foreign", user: owner.UserID},
		{name: "foreign-user", account: owner.AccountScopeID, user: "foreign"},
		{name: "unknown-session", alter: func(r *SessionArtifactSelectionReference) { r.SessionID = "unknown" }},
		{name: "unknown-artifact", alter: func(r *SessionArtifactSelectionReference) { r.ArtifactID = "unknown" }},
		{name: "stale-revision", alter: func(r *SessionArtifactSelectionReference) { r.RevisionRef = "revision-" + strings.Repeat("a", 40) }},
		{name: "invalid-revision", alter: func(r *SessionArtifactSelectionReference) { r.RevisionRef = "revision-not-a-commit" }},
		{name: "mixed-legacy", alter: func(r *SessionArtifactSelectionReference) { r.CollectionID = "legacy" }},
		{name: "unknown-part", alter: func(r *SessionArtifactSelectionReference) { r.TargetPartIDs = &[]string{"unknown"} }},
		{name: "duplicate-part", alter: func(r *SessionArtifactSelectionReference) { r.TargetPartIDs = &[]string{"pricing", "pricing"} }},
		{name: "hidden-client-instruction", alter: func(r *SessionArtifactSelectionReference) { r.PendingRequest = "forged context" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := in
			if tc.alter != nil {
				tc.alter(&bad)
			}
			account, user := tc.account, tc.user
			if account == "" {
				account = owner.AccountScopeID
			}
			if user == "" {
				user = owner.UserID
			}
			out, err := sessions.ValidateSessionArtifactMessageSelections(account, user, []SessionArtifactSelectionReference{in, bad})
			if err == nil || out != nil {
				t.Fatalf("invalid batch returned partial selection: %+v %v", out, err)
			}
			after, ok, err := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, in.ArtifactID)
			if err != nil || !ok || !reflect.DeepEqual(after, *created.Repository) {
				t.Fatal("rejection changed artifact authority")
			}
		})
	}
}
