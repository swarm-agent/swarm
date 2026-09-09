package pebblestore

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// Requirement: ready non-head and historical revisions remain immutable remix
// sources through head changes. OpenTurn/SubmitCandidate/Select and message
// admission are the authority; this hermetic Git/store test proves parentage,
// revision-specific Parts, explicit stale-head rejection and no rejected writes.
func TestArtifactV3ExactSourceSurvivesHeadSelection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sessions := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, sessions, "exact-source")
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "exact-source"}
	service, err := NewArtifactV3Service(sessions, t.TempDir(), ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	project := artifactV3TestProject(t, "Fictional", "free")
	created, err := service.Create(ctx, ArtifactV3CreateInput{Owner: owner, ArtifactID: "exact", TransactionID: "genesis", Project: project, Build: preparedArtifactV3Evidence("build"), Preview: preparedArtifactV3Evidence("preview")})
	if err != nil {
		t.Fatal(err)
	}
	base := created.Revision.CommitOID
	repo, err := service.open(ctx, owner, "exact")
	if err != nil {
		t.Fatal(err)
	}
	open := func(id, source string) {
		t.Helper()
		if _, err := service.OpenTurn(ctx, ArtifactV3OpenTurnInput{Owner: owner, ArtifactID: "exact", TurnID: id, ExpectedHead: source, TargetPartIDs: []string{"pricing"}}); err != nil {
			t.Fatal(err)
		}
	}
	submit := func(id, source string) string {
		t.Helper()
		next := artifactV3TestProject(t, "Fictional", id)
		oid, err := repo.commitProject(ctx, next, []string{source}, id)
		if err != nil {
			t.Fatal(err)
		}
		evidence := ArtifactV3EvidenceProjection{Status: "succeeded", CommitOID: oid, DigestSHA256: "fixture", Reference: id}
		got, err := service.SubmitCandidate(ctx, ArtifactV3SubmitCandidateInput{Owner: owner, ArtifactID: "exact", TurnID: id, CandidateID: id, TransactionID: id + "-commit", ExpectedHead: source, Project: next, Message: id, Build: evidence, Preview: evidence})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Revision.ParentCommitOIDs, []string{source}) {
			t.Fatalf("wrong lineage: %+v", got.Revision)
		}
		return oid
	}
	open("option", base)
	option := submit("option", base)
	open("remix", option)
	open("advance", base)
	advance := submit("advance", base)
	if _, err := service.Select(ctx, ArtifactV3SelectInput{Owner: owner, ArtifactID: "exact", TurnID: "advance", CandidateID: "advance", TransactionID: "accept-advance", ExpectedHead: base}); err != nil {
		t.Fatal(err)
	}
	remix := submit("remix", option)
	open("historical", base)
	for _, source := range []string{base, option, remix} {
		got, err := service.ResolveSelectedSource(owner, "exact", source, created.Repository.EventSeq)
		if err != nil || got.CommitOID != source {
			t.Fatalf("resolve %s: %+v %v", source, got, err)
		}
		ref, err := sessions.validateNativeArtifactMessageSelection(owner.AccountScopeID, owner.UserID, SessionArtifactSelectionReference{SessionID: owner.SessionID, ArtifactID: "exact", RevisionRef: "revision-" + source, Action: "use", TargetPartIDs: &[]string{"pricing"}})
		if err != nil || ref.CommitOID != source {
			t.Fatalf("admission: %+v %v", ref, err)
		}
	}
	before, _, _ := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, "exact")
	for _, input := range []ArtifactV3OpenTurnInput{
		{Owner: owner, ExpectedHead: option, TargetPartIDs: []string{"missing"}},
		{Owner: owner, ExpectedHead: gitOID("f")},
		{Owner: ArtifactV3Owner{AccountScopeID: "foreign", UserID: owner.UserID, SessionID: owner.SessionID}, ExpectedHead: option},
	} {
		input.ArtifactID, input.TurnID = "exact", "rejected"
		if _, err := service.OpenTurn(ctx, input); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	if _, found, _ := sessions.GetArtifactV3Turn(owner.AccountScopeID, owner.UserID, "exact", "rejected"); found {
		t.Fatal("partial turn")
	}
	if _, err := service.Select(ctx, ArtifactV3SelectInput{Owner: owner, ArtifactID: "exact", TurnID: "remix", CandidateID: "remix", TransactionID: "stale", ExpectedHead: base}); err == nil {
		t.Fatal("stale CAS accepted")
	}
	after, _, _ := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, "exact")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejection changed projection")
	}
	if head, err := repo.Head(ctx); err != nil || head != advance {
		t.Fatalf("head moved: %s %v", head, err)
	}
	if _, err := service.Select(ctx, ArtifactV3SelectInput{Owner: owner, ArtifactID: "exact", TurnID: "remix", CandidateID: "remix", TransactionID: "accept-remix", ExpectedHead: advance}); err != nil {
		t.Fatal(err)
	}
	if head, err := repo.Head(ctx); err != nil || head != remix {
		t.Fatalf("explicit acceptance: %s %v", head, err)
	}
}
