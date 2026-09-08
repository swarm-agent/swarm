package pebblestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// Requirement: OpenTurn must reject malformed/unknown focus before durable
// mutation, and ResolveSelectedSource must authenticate exact selected identity
// without recovery writes. This store-layer test observes projections directly;
// it prevents stale or foreign source handoffs and partial preflight state.
func TestArtifactV3RemixSourcePreflight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sessions := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, sessions, "remix-source")
	service, err := NewArtifactV3Service(sessions, t.TempDir(), ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "remix-source"}
	created, err := service.Create(ctx, ArtifactV3CreateInput{Owner: owner, ArtifactID: "remix", TransactionID: "genesis", Project: artifactV3TestProject(t, "Fictional", "free"), Build: preparedArtifactV3Evidence("build"), Preview: preparedArtifactV3Evidence("preview")})
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, "remix")
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.ResolveSelectedSource(owner, "remix", created.Repository.HeadCommitOID, before.EventSeq)
	if err != nil || source.CommitOID != before.HeadCommitOID || source.RevisionRef != "revision-"+source.CommitOID || source.SessionID != owner.SessionID {
		t.Fatalf("source=%+v error=%v", source, err)
	}
	if _, err := service.ResolveSelectedSource(owner, "remix", gitOID("f"), before.EventSeq); !errors.Is(err, ErrArtifactV3Conflict) {
		t.Fatalf("stale: %v", err)
	}
	foreign := owner
	foreign.AccountScopeID = "foreign"
	if _, err := service.ResolveSelectedSource(foreign, "remix", "", 0); err == nil {
		t.Fatal("foreign source accepted")
	}
	for _, input := range []ArtifactV3OpenTurnInput{
		{RevisionIntent: ArtifactV3RevisionWholeProject, TargetPartIDs: []string{"pricing"}},
		{RevisionIntent: ArtifactV3RevisionFocusedParts},
		{RevisionIntent: ArtifactV3RevisionFocusedParts, TargetPartIDs: []string{"missing"}},
		{RevisionIntent: "unknown"},
		{TargetPartIDs: []string{"pricing", "pricing"}},
	} {
		input.Owner, input.ArtifactID, input.TurnID, input.ExpectedHead = owner, "remix", "rejected", before.HeadCommitOID
		if _, err := service.OpenTurn(ctx, input); !errors.Is(err, ErrArtifactV3Invalid) {
			t.Fatalf("input=%+v err=%v", input, err)
		}
	}
	after, _, err := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, "remix")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("preflight mutated repository: %v", err)
	}
	if _, found, err := sessions.GetArtifactV3Turn(owner.AccountScopeID, owner.UserID, "remix", "rejected"); err != nil || found {
		t.Fatalf("partial turn: %v %v", found, err)
	}
}

// Requirement: three immutable sibling candidates never select themselves;
// a focused selection can seed a whole-project round without losing assets or
// allowing an old sibling to overwrite the new head. Service/Git integration is
// the narrowest layer proving exact parentage, stored intent and selection CAS.
func TestArtifactV3RemixThreeCandidatesRepeat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sessions := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, sessions, "remix-rounds")
	service, err := NewArtifactV3Service(sessions, t.TempDir(), ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "remix-rounds"}
	project := artifactV3TestProject(t, "Fictional", "free")
	var manifest ArtifactV3Manifest
	if err := json.Unmarshal(project.Files[ArtifactV3ManifestFilename], &manifest); err != nil {
		t.Fatal(err)
	}
	project.Files["pages/main.html"] = project.Files["index.html"]
	delete(project.Files, "index.html")
	manifest.Entrypoint = "pages/main.html"
	for i := range manifest.Parts {
		if manifest.Parts[i].Locator.Path == "index.html" {
			manifest.Parts[i].Locator.Path = manifest.Entrypoint
		}
		for j, name := range manifest.Parts[i].Locator.Paths {
			if name == "index.html" {
				manifest.Parts[i].Locator.Paths[j] = manifest.Entrypoint
			}
		}
	}
	manifest.Parts = append(manifest.Parts, ArtifactV3Part{ID: "retired", Label: "Retired concept", Locator: ArtifactV3Locator{Kind: "file", Path: "pages/main.html"}})
	project.Files[ArtifactV3ManifestFilename], err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	originalManifest := append([]byte(nil), project.Files[ArtifactV3ManifestFilename]...)
	project.Files["assets/brand.txt"] = []byte("fictional brand asset")
	created, err := service.Create(ctx, ArtifactV3CreateInput{Owner: owner, ArtifactID: "rounds", TransactionID: "genesis", Project: project, Build: preparedArtifactV3Evidence("build"), Preview: preparedArtifactV3Evidence("preview")})
	if err != nil {
		t.Fatal(err)
	}
	head := created.Repository.HeadCommitOID
	repository, err := service.open(ctx, owner, "rounds")
	if err != nil {
		t.Fatal(err)
	}
	for round, intent := range []string{ArtifactV3RevisionFocusedParts, ArtifactV3RevisionWholeProject, ArtifactV3RevisionFocusedParts} {
		turnID := fmt.Sprintf("round-%d", round)
		var targets []string
		if intent == ArtifactV3RevisionFocusedParts {
			targets = []string{"pricing"}
		}
		if _, err := service.OpenTurn(ctx, ArtifactV3OpenTurnInput{Owner: owner, ArtifactID: "rounds", TurnID: turnID, ExpectedHead: head, RevisionIntent: intent, TargetPartIDs: targets}); err != nil {
			t.Fatal(err)
		}
		var selectedOID string
		for candidate := 0; candidate < 3; candidate++ {
			id := fmt.Sprintf("candidate-%d-%d", round, candidate)
			next := ArtifactV3Project{Files: map[string][]byte{}}
			for name := range project.Files {
				body, err := repository.ReadFile(ctx, head, name)
				if err != nil {
					t.Fatal(err)
				}
				next.Files[name] = body
			}
			next.Files["pages/main.html"] = append(next.Files["pages/main.html"], []byte("<!--"+id+"-->")...)
			if round == 1 {
				var revised ArtifactV3Manifest
				if err := json.Unmarshal(next.Files[ArtifactV3ManifestFilename], &revised); err != nil {
					t.Fatal(err)
				}
				var parts []ArtifactV3Part
				for _, part := range revised.Parts {
					if part.ID == "retired" {
						continue
					}
					if part.ID == "pricing" {
						part.Locator = ArtifactV3Locator{Kind: "file", Path: "pages/main.html"}
					}
					parts = append(parts, part)
				}
				revised.Parts = append(parts, ArtifactV3Part{ID: "brand", Label: "Brand", Locator: ArtifactV3Locator{Kind: "semantic", Value: "Brand assets", Paths: []string{"assets/brand.txt"}}})
				next.Files[ArtifactV3ManifestFilename], err = json.Marshal(revised)
				if err != nil {
					t.Fatal(err)
				}
			}
			if round == 1 && candidate == 0 {
				before, _, err := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, "rounds")
				if err != nil {
					t.Fatal(err)
				}
				bad := ArtifactV3Project{Files: map[string][]byte{}}
				for name, body := range next.Files {
					bad.Files[name] = body
				}
				var policy ArtifactV3Manifest
				if err := json.Unmarshal(bad.Files[ArtifactV3ManifestFilename], &policy); err != nil {
					t.Fatal(err)
				}
				policy.Entrypoint = "assets/brand.txt"
				bad.Files[ArtifactV3ManifestFilename], err = json.Marshal(policy)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.SubmitCandidate(ctx, ArtifactV3SubmitCandidateInput{Owner: owner, ArtifactID: "rounds", TurnID: turnID, CandidateID: "policy-override", TransactionID: "policy-override", ExpectedHead: head, Project: bad}); !errors.Is(err, ErrArtifactV3Invalid) {
					t.Fatalf("non-Part policy override: %v", err)
				}
				after, _, err := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, "rounds")
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("policy rejection mutated projection: %v", err)
				}
				ref, err := repository.CandidateRef(turnID, "policy-override")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := repository.ref(ctx, ref); !errors.Is(err, ErrArtifactV3NotFound) {
					t.Fatalf("policy rejection created ref: %v", err)
				}
			}
			oid, err := repository.commitProject(ctx, next, []string{head}, id)
			if err != nil {
				t.Fatal(err)
			}
			evidence := ArtifactV3EvidenceProjection{Status: "succeeded", CommitOID: oid, DigestSHA256: "fixture-evidence", Reference: id}
			result, err := service.SubmitCandidate(ctx, ArtifactV3SubmitCandidateInput{Owner: owner, ArtifactID: "rounds", TurnID: turnID, CandidateID: id, TransactionID: id, ExpectedHead: head, Project: next, Message: id, Build: evidence, Preview: evidence})
			if err != nil {
				t.Fatal(err)
			}
			if result.Turn.RevisionIntent != intent || !reflect.DeepEqual(result.Revision.ParentCommitOIDs, []string{head}) {
				t.Fatalf("candidate lineage: %+v", result)
			}
			if candidate == 1 {
				selectedOID = oid
			}
		}
		if current, err := repository.Head(ctx); err != nil || current != head {
			t.Fatalf("candidate moved head: %s %v", current, err)
		}
		// Read-only discovery supplies actual candidate IDs and turn CAS for explicit selection.
		source, err := service.ResolveSelectedSource(owner, "rounds", head, 0)
		if err != nil || len(source.Candidates) != 3 || len(source.Turns) != 1 || source.Turns[0].EventSeq == 0 || source.Turns[0].TurnID != turnID {
			t.Fatalf("selection handoff: %+v %v", source, err)
		}
		_, err = service.Select(ctx, ArtifactV3SelectInput{Owner: owner, ArtifactID: "rounds", TurnID: turnID, CandidateID: fmt.Sprintf("candidate-%d-1", round), TransactionID: "select-" + turnID, ExpectedHead: head})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Select(ctx, ArtifactV3SelectInput{Owner: owner, ArtifactID: "rounds", TurnID: turnID, CandidateID: fmt.Sprintf("candidate-%d-0", round), TransactionID: "stale-" + turnID, ExpectedHead: head}); err == nil {
			t.Fatal("old sibling selected")
		}
		oldManifest, err := repository.ReadFile(ctx, created.Repository.HeadCommitOID, ArtifactV3ManifestFilename)
		if err != nil || string(oldManifest) != string(originalManifest) {
			t.Fatalf("old manifest mutated: %v", err)
		}
		asset, err := repository.ReadFile(ctx, selectedOID, "assets/brand.txt")
		if err != nil || string(asset) != string(project.Files["assets/brand.txt"]) {
			t.Fatalf("asset changed: %v", err)
		}
		selectedManifest, err := repository.ReadFile(ctx, selectedOID, ArtifactV3ManifestFilename)
		if err != nil {
			t.Fatal(err)
		}
		var selected ArtifactV3Manifest
		if err := json.Unmarshal(selectedManifest, &selected); err != nil {
			t.Fatal(err)
		}
		if round > 0 {
			seen := map[string]ArtifactV3Part{}
			for _, part := range selected.Parts {
				seen[part.ID] = part
			}
			if _, ok := seen["retired"]; ok {
				t.Fatal("retired Part survived")
			}
			if seen["brand"].Locator.Kind != "semantic" || seen["pricing"].Locator.Kind != "file" {
				t.Fatalf("structural reconciliation lost: %+v", selected)
			}
		}
		head = selectedOID
		if current, err := repository.Head(ctx); err != nil || current != head {
			t.Fatalf("stale selection mutated head: %s %v", current, err)
		}
	}
}
