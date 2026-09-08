package pebblestore

import (
	"context"
	"reflect"
	"testing"
)

// Requirement: whole-project message admission authenticates exact owner/head
// even when optional target IDs are absent or empty. Threat: callers treating
// nil Parts as missing identity, or accepting whole-project references without
// ownership checks. Real temporary Git/Pebble admission is the narrowest layer
// proving the server-derived reference consumed by task binding; no provider.
func TestNativeWholeArtifactMessageSelectionAuthority(t *testing.T) {
	sessions := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, sessions, "whole-selection")
	service, err := NewArtifactV3Service(sessions, t.TempDir(), ArtifactV3Limits{MaxFiles: 256, MaxParts: 256})
	if err != nil {
		t.Fatal(err)
	}
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "whole-selection"}
	created, err := service.Create(context.Background(), ArtifactV3CreateInput{Owner: owner, ArtifactID: "whole-artifact", TransactionID: "genesis", Project: artifactV3TestProject(t, "Starter", "free"), Build: preparedArtifactV3Evidence("build"), Preview: preparedArtifactV3Evidence("preview")})
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range []*[]string{nil, new([]string)} {
		in := SessionArtifactSelectionReference{SessionID: owner.SessionID, ArtifactID: created.Repository.ArtifactID, RevisionRef: "revision-" + created.Revision.CommitOID, TargetPartIDs: ids, Action: "use", CommitOID: "forged", ProjectionSeq: 999}
		got, err := sessions.ValidateSessionArtifactMessageSelections(owner.AccountScopeID, owner.UserID, []SessionArtifactSelectionReference{in})
		if err != nil || len(got) != 1 {
			t.Fatalf("whole selection: %+v %v", got, err)
		}
		if got[0].TargetPartIDs != nil || got[0].CommitOID != created.Revision.CommitOID || got[0].ProjectionSeq != created.Repository.EventSeq || got[0].SessionID != owner.SessionID || got[0].RevisionRef != in.RevisionRef {
			t.Fatalf("server-derived whole selection lost: %+v", got[0])
		}
		if out, err := sessions.ValidateSessionArtifactMessageSelections("foreign-account", owner.UserID, []SessionArtifactSelectionReference{in}); err == nil || out != nil {
			t.Fatalf("foreign whole selection admitted: %+v %v", out, err)
		}
		after, ok, err := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, in.ArtifactID)
		if err != nil || !ok || !reflect.DeepEqual(after, *created.Repository) {
			t.Fatal("selection changed artifact authority")
		}
	}
}
