package pebblestore

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// Requirement: SaveDraft/ApplySessionMutation atomically retain ordered wave
// membership across draft failure and genesis. Threat: title-based merging,
// foreign/stale allocation or hydration moving a head. Real Pebble/Git is the
// narrowest layer proving both durable index and unchanged postconditions.
func TestArtifactV3GenerationDurability(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "wave-owner")
	service, err := NewArtifactV3Service(sessions, t.TempDir(), ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "wave-owner"}
	save := func(member ArtifactV3GenerationMember, id, status string, sequence uint64) error {
		grant, _ := json.Marshal(map[string]any{"Generation": member, "ArtifactID": member.ArtifactID, "OwnerSessionID": owner.SessionID, "TurnID": member.TurnID, "CandidateID": member.CandidateID, "BaseCommitOID": member.BaseCommitOID, "SourceProjectionSeq": member.SourceProjectionSeq})
		_, err := service.SaveDraft(owner, member.ArtifactID, id+status, "same title", ArtifactV3DraftProjection{GrantID: id, Grant: grant, Status: status, ExpiresAt: 2000000000000}, sequence)
		return err
	}
	a := ArtifactV3GenerationMember{WaveID: "wave-a", Index: 1, Count: 2, ArtifactID: "a", TurnID: "turn-a", CandidateID: "candidate-a"}
	b := a
	b.Index = 2
	b.ArtifactID = "b"
	b.TurnID = "turn-b"
	b.CandidateID = "candidate-b"
	c := a
	c.WaveID = "wave-b"
	c.Count = 1
	c.ArtifactID = "c"
	c.TurnID = "turn-c"
	c.CandidateID = "candidate-c"
	for _, m := range []ArtifactV3GenerationMember{a, b, c} {
		if err := save(m, m.ArtifactID, "creating", 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := save(b, "b", "error", 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	created, err := service.Create(ctx, ArtifactV3CreateInput{Owner: owner, ArtifactID: "a", TransactionID: "genesis", Project: artifactV3TestProject(t, "Title", "text"), Build: preparedArtifactV3Evidence("build"), Preview: preparedArtifactV3Evidence("preview")})
	if err != nil {
		t.Fatal(err)
	}
	sessions = NewSessionStore(store)
	before, _, _ := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, "a")
	members, err := sessions.GetArtifactV3Generation(owner.AccountScopeID, owner.UserID, owner.SessionID, "a", "wave-a")
	if err != nil || !reflect.DeepEqual(members, []ArtifactV3GenerationMember{a, b}) {
		t.Fatalf("members=%+v err=%v", members, err)
	}
	failed, _, _ := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, "b")
	if failed.DraftStatus != "error" {
		t.Fatal("failed sibling lost")
	}
	if len(before.Generations) != 1 || before.HeadCommitOID != created.Repository.HeadCommitOID {
		t.Fatal("publication lost membership")
	}
	for _, args := range [][3]string{{"foreign", owner.SessionID, "wave-a"}, {owner.UserID, "foreign", "wave-a"}, {owner.UserID, owner.SessionID, "wave-b"}} {
		if _, err := sessions.GetArtifactV3Generation(owner.AccountScopeID, args[0], args[1], "a", args[2]); err == nil {
			t.Fatal("foreign context accepted")
		}
	}
	otherBefore, _, _ := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, "c")
	bad := c
	bad.WaveID = "wave-a"
	bad.Count = 2
	if err := save(bad, "collision", "creating", 0); err == nil {
		t.Fatal("occupied slot accepted")
	}
	bad = a
	bad.WaveID = "stale"
	bad.BaseCommitOID = gitOID("f")
	bad.SourceProjectionSeq = before.EventSeq
	if err := save(bad, "stale", "creating", 0); err == nil {
		t.Fatal("stale base accepted")
	}
	otherAfter, _, _ := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, "c")
	if !reflect.DeepEqual(otherBefore, otherAfter) {
		t.Fatal("collision partially mutated sibling")
	}
	after, _, _ := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, "a")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reads or rejection changed anchor")
	}
	// A corrupted accelerator must not invent a foreign or stale sibling.
	corrupt := b
	corrupt.ArtifactID = "missing"
	raw, _ := json.Marshal(corrupt)
	if err := store.db.Set([]byte(artifactV3GenerationKey(owner.AccountScopeID, owner.SessionID, "wave-a", 2)), raw, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.GetArtifactV3Generation(owner.AccountScopeID, owner.UserID, owner.SessionID, "a", "wave-a"); err == nil {
		t.Fatal("corrupt membership accepted")
	}
}
