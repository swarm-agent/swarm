package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: exact retained candidate identity outranks recency. Threat: a
// newer successful sibling hides a failed child or is accidentally resumed.
// artifactV3ExactDraft is the narrowest selection boundary; compare the complete
// repository after both successful and rejected lookups to prove read-only use.
func TestArtifactV3RecoveryExactOlderCandidate(t *testing.T) {
	repository := pebblestore.ArtifactV3RepositoryProjection{ArtifactID: "artifact", OwnerSessionID: "parent", Drafts: map[string]pebblestore.ArtifactV3DraftProjection{}}
	for i, id := range []string{"failed", "ready"} {
		grant, _ := json.Marshal(tool.ArtifactV3AuthorGrant{ID: id, ArtifactID: "artifact", OwnerSessionID: "parent", TurnID: "turn", CandidateID: id})
		repository.Drafts[id] = pebblestore.ArtifactV3DraftProjection{GrantID: id, Grant: grant, EventSeq: uint64(i + 1), Sequence: 1, Status: id}
	}
	before, _ := json.Marshal(repository)
	got, err := artifactV3ExactDraft(repository, "turn", "failed")
	if err != nil || got.GrantID != "failed" {
		t.Fatalf("selected=%+v error=%v", got, err)
	}
	for _, ids := range [][2]string{{"", ""}, {"turn", ""}, {"", "failed"}, {"turn", "missing"}} {
		if _, err := artifactV3ExactDraft(repository, ids[0], ids[1]); err == nil {
			t.Fatalf("accepted ambiguous/unknown identity: %v", ids)
		}
	}
	after, _ := json.Marshal(repository)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("lookup changed siblings")
	}
}

// Requirement: a parent can recover an exact terminal Designer child's older
// failed draft without selecting or damaging a newer ready sibling. Threats are
// stale CAS, forged lineage, live-producer takeover and reuse of stale readiness.
// Real sessions, Git, projection mutations and AuthorService are the narrowest
// integration layer proving retained bytes, authority rejection and publication.
func TestArtifactV3RecoveryChildIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	db, err := pebblestore.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	for _, id := range []string{"parent", "child", "sibling"} {
		_, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: id, AccountScopeID: "account", UserID: "user", Title: "Recovery", WorkspacePath: root, WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "medium"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	child, _, err := sessions.Store().GetSession("child")
	if err != nil {
		t.Fatal(err)
	}
	child.Metadata = map[string]any{"parent_session_id": "parent", "lineage_kind": "delegated_subagent", "subagent": "designer"}
	if err := sessions.Store().UpdateSession(child); err != nil {
		t.Fatal(err)
	}
	setRun := func(session, run, status string) {
		t.Helper()
		_, err := sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: session, AccountScopeID: "account", UserID: "user", Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: run + status, PayloadHash: run + status, RunIntent: &pebblestore.V3SessionRunIntent{SessionID: session, AccountScopeID: "account", UserID: "user", RunID: run, Status: status}})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]string{{"child", "old"}, {"parent", "new"}} {
		setRun(pair[0], pair[1], pebblestore.V3RunIntentPendingExecutor)
		setRun(pair[0], pair[1], pebblestore.V3RunIntentRunning)
	}
	repos, work, evidence, err := artifactV3StorageRoots(filepath.Join(root, "data"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := pebblestore.NewArtifactV3Service(sessions.Store(), repos, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	renderer := &artifactV3ResumeRenderer{}
	adapter := newArtifactV3RuntimeAdapter(service, sessions.Store(), repos, evidence, pebblestore.ArtifactV3Limits{}, renderer)
	adapter.publish = func(identity.Principal, api.ArtifactV3Artifact, string, string) error { return nil }
	author := tool.NewArtifactV3AuthorService(work, adapter, adapter, adapter)
	p := tool.ArtifactV3AuthorPrincipal{AccountScopeID: "account", UserID: "user", ProducerSessionID: "parent", ProducerRunID: "new"}
	bind := func(g tool.ArtifactV3AuthorGrant, who tool.ArtifactV3AuthorPrincipal) tool.ArtifactV3AuthorGrant {
		t.Helper()
		g.ProducerSessionID, g.ProducerRunID = who.ProducerSessionID, who.ProducerRunID
		if _, err := adapter.LoadAuthorDraft(tool.WithArtifactV3AuthorRunContext(ctx, tool.ArtifactV3AuthorRunContext{Grant: g}), who, g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	initial, err := author.PrepareTurn(ctx, tool.ArtifactV3PrepareTurnRequest{AccountScopeID: "account", UserID: "user", OwnerSessionID: "parent", TaskCallID: "initial", Prompt: "Recovery", PolicyRevision: "policy", CandidateIndex: 1, Initial: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	initial = bind(initial, p)
	manifest, _ := json.Marshal(pebblestore.ArtifactV3Manifest{SchemaVersion: pebblestore.ArtifactV3ManifestVersion, Entrypoint: "index.html", Parts: []pebblestore.ArtifactV3Part{{ID: "hero", Label: "Hero", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#hero"}}}})
	for path, body := range map[string][]byte{"swarm-artifact.json": manifest, "index.html": []byte(`<html><body><main id="hero">Original</main></body></html>`), "styles/theme.css": []byte(`body{color:navy}`), "src/app.js": []byte(`export const value=1;`)} {
		if err := author.Create(ctx, p, initial, path, body); err != nil {
			t.Fatal(err)
		}
	}
	if gate, err := author.BuildPreview(ctx, p, initial); err != nil || !gate.Ready {
		t.Fatalf("initial gate: %+v %v", gate, err)
	}
	first, err := author.Finish(ctx, p, initial)
	if err != nil {
		t.Fatal(err)
	}
	request := tool.ArtifactV3PrepareTurnRequest{AccountScopeID: "account", UserID: "user", OwnerSessionID: "parent", TaskCallID: "wave", ArtifactID: initial.ArtifactID, BaseCommitOID: first.Revision.CommitOID, PolicyRevision: "policy", CandidateIndex: 1, TargetPartIDs: []string{"hero"}, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	failed, err := author.PrepareTurn(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	oldp := p
	oldp.ProducerSessionID, oldp.ProducerRunID = "child", "old"
	failed = bind(failed, oldp)
	renderer.fail = true
	if gate, err := author.BuildPreview(ctx, oldp, failed); err != nil || gate.Ready {
		t.Fatalf("failed gate: %+v %v", gate, err)
	}
	request.CandidateIndex = 2
	sibling, err := author.PrepareTurn(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	sp := p
	sp.ProducerSessionID, sp.ProducerRunID = "sibling", "sibling-run"
	sibling = bind(sibling, sp)
	renderer.fail = false
	if gate, err := author.BuildPreview(ctx, sp, sibling); err != nil || !gate.Ready {
		t.Fatalf("sibling gate: %+v %v", gate, err)
	}
	good, err := author.Finish(ctx, sp, sibling)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() pebblestore.ArtifactV3RepositoryProjection {
		t.Helper()
		r, ok, err := sessions.Store().GetArtifactV3Repository("account", "user", initial.ArtifactID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		return r
	}
	before := snapshot()
	locator, status, err := adapter.LocateArtifactV3ExactDraft(ctx, p, initial.ArtifactID, failed.TurnID, failed.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	summary, ok := status.(*api.ArtifactV3DraftSummary)
	if !ok || len(summary.Diagnostics) == 0 || summary.Diagnostics[0].Stage == "" || summary.Diagnostics[0].Code == "" || summary.Diagnostics[0].Message == "" {
		t.Fatalf("missing safe diagnostics: %+v", status)
	}
	var failedState tool.ArtifactV3AuthorDraft
	if err := json.Unmarshal(before.Drafts[failed.ID].State, &failedState); err != nil {
		t.Fatal(err)
	}
	expected := boundedArtifactV3Gate(failedState.Gate).Diagnostics
	if len(summary.Diagnostics) != len(expected) {
		t.Fatal("diagnostics lost")
	}
	for i, d := range expected {
		if summary.Diagnostics[i].Stage != d.Stage || summary.Diagnostics[i].Code != d.Code || summary.Diagnostics[i].Message != d.Message {
			t.Fatal("diagnostic details replaced")
		}
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), failed.ID) || strings.Contains(string(raw), "ProducerRunID") || strings.Contains(string(raw), "Project") {
		t.Fatal("private draft envelope leaked")
	}
	reject := func(who tool.ArtifactV3AuthorPrincipal, r tool.ArtifactV3DraftResumeRequest) {
		t.Helper()
		if _, err := adapter.ResumeArtifactV3DirectDraft(ctx, who, r); err == nil {
			t.Fatal("unauthorized/stale resume accepted")
		}
		if !reflect.DeepEqual(before, snapshot()) {
			t.Fatal("rejected resume mutated repository")
		}
	}
	reject(p, locator) // live old producer
	setRun("child", "old", pebblestore.V3RunIntentCompleted)
	stale := locator
	stale.ExpectedSequence++
	reject(p, stale)
	foreign := p
	foreign.AccountScopeID = "foreign"
	reject(foreign, locator)
	for _, parent := range []any{"foreign", []string{"parent"}} {
		child.Metadata["parent_session_id"] = parent
		if err := sessions.Store().UpdateSession(child); err != nil {
			t.Fatal(err)
		}
		reject(p, locator)
		if _, _, err := adapter.LocateArtifactV3ExactDraft(ctx, p, initial.ArtifactID, failed.TurnID, failed.CandidateID); err == nil {
			t.Fatal("invalid lineage status accepted")
		}
	}
	child.Metadata["parent_session_id"] = "parent"
	if err := sessions.Store().UpdateSession(child); err != nil {
		t.Fatal(err)
	}
	resumed, err := adapter.ResumeArtifactV3DirectDraft(ctx, p, locator)
	if err != nil {
		t.Fatal(err)
	}
	after := snapshot()
	if _, exists := after.Drafts[failed.ID]; exists {
		t.Fatal("old grant retained")
	}
	if !reflect.DeepEqual(before.Drafts[sibling.ID], after.Drafts[sibling.ID]) || after.HeadCommitOID != first.Revision.CommitOID {
		t.Fatal("resume changed sibling or selected head")
	}
	var oldState, nextState tool.ArtifactV3AuthorDraft
	if err := json.Unmarshal(before.Drafts[failed.ID].State, &oldState); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after.Drafts[resumed.ID].State, &nextState); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(oldState.Project, nextState.Project) || nextState.Gate != nil || len(nextState.History) == 0 || !reflect.DeepEqual(nextState.History[len(nextState.History)-1], *oldState.Gate) {
		t.Fatal("source/history/readiness changed incorrectly")
	}
	if _, err := author.Read(ctx, oldp, failed, "index.html", 0, 4096); err == nil {
		t.Fatal("old capability survived")
	}
	if _, err := author.Finish(ctx, p, resumed); err == nil {
		t.Fatal("published without rebuild")
	}
	if err := author.Edit(ctx, p, resumed, "index.html", []byte("Original"), []byte("Repaired"), false); err != nil {
		t.Fatal(err)
	}
	if gate, err := author.BuildPreview(ctx, p, resumed); err != nil || !gate.Ready {
		t.Fatalf("repair gate: %+v %v", gate, err)
	}
	repaired, err := author.Finish(ctx, p, resumed)
	if err != nil || repaired.Revision.CommitOID == "" {
		t.Fatalf("repair publication: %+v %v", repaired, err)
	}
	final := snapshot()
	if final.HeadCommitOID != first.Revision.CommitOID || !reflect.DeepEqual(final.Drafts[sibling.ID], before.Drafts[sibling.ID]) {
		t.Fatal("publication selected candidate or modified sibling")
	}
	candidate, found, err := sessions.Store().GetArtifactV3Candidate("account", "user", initial.ArtifactID, sibling.TurnID, sibling.CandidateID)
	if err != nil || !found || candidate.Status != "ready" || candidate.CommitOID != good.Revision.CommitOID {
		t.Fatalf("lost ready sibling: %+v %v", candidate, err)
	}
}
