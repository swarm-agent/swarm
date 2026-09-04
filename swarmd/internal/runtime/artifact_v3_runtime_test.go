package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/htmlcapture"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type artifactV3RuntimeRenderer struct{}

func (artifactV3RuntimeRenderer) Capture(_ context.Context, request htmlcapture.Request) ([]htmlcapture.Result, error) {
	if request.Entry == "" || len(request.Files) == 0 || request.ViewportWidth != 1440 || request.ViewportHeight != 900 {
		return nil, htmlcapture.NewError("capture_invalid", "invalid capture")
	}
	return []htmlcapture.Result{{StateID: "default", PNG: []byte("real-renderer-evidence")}}, nil
}

// Requirement: the production bridge must connect managed whole-tree authoring,
// exact Git/Pebble state, API reads, and restart recovery without V2 records.
func TestArtifactV3RuntimeAdapterProductionPathAndRecovery(t *testing.T) {
	root := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(root, "session.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	_, _, err = sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "artifact-v3-runtime", AccountScopeID: "account", UserID: "user", Title: "Artifact V3", WorkspacePath: root, WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot, workspaceRoot, evidenceRoot, err := artifactV3StorageRoots(filepath.Join(root, "data"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := pebblestore.NewArtifactV3Service(sessions.Store(), repositoryRoot, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	adapter := newArtifactV3RuntimeAdapter(service, sessions.Store(), repositoryRoot, evidenceRoot, pebblestore.ArtifactV3Limits{}, artifactV3RuntimeRenderer{})
	adapter.publish = func(identity.Principal, api.ArtifactV3Artifact, string, string) error { return nil }
	author := tool.NewArtifactV3AuthorService(workspaceRoot, adapter, adapter, adapter)
	grant, err := author.PrepareTurn(context.Background(), tool.ArtifactV3PrepareTurnRequest{AccountScopeID: "account", UserID: "user", OwnerSessionID: "artifact-v3-runtime", TaskCallID: "call", Prompt: "create", PolicyRevision: "policy", CandidateIndex: 1, Initial: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	principal := tool.ArtifactV3AuthorPrincipal{AccountScopeID: "account", UserID: "user", ProducerSessionID: "child", ProducerRunID: "run"}
	grant.ProducerSessionID = "child"
	grant.ProducerRunID = "run"
	manifest, _ := json.Marshal(pebblestore.ArtifactV3Manifest{SchemaVersion: pebblestore.ArtifactV3ManifestVersion, Entrypoint: "index.html", Parts: []pebblestore.ArtifactV3Part{{ID: "hero", Label: "Hero", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#hero"}}}})
	if err := author.Create(context.Background(), principal, grant, "swarm-artifact.json", manifest); err != nil {
		t.Fatal(err)
	}
	if err := author.Create(context.Background(), principal, grant, "index.html", []byte(`<!doctype html><html><head><link rel="stylesheet" href="styles/theme.css"></head><body><main id="hero">Artifact V3</main><script type="module" src="src/app.js"></script></body></html>`)); err != nil {
		t.Fatal(err)
	}
	if err := author.Create(context.Background(), principal, grant, "styles/theme.css", []byte(`body{color:navy}`)); err != nil {
		t.Fatal(err)
	}
	if err := author.Create(context.Background(), principal, grant, "src/app.js", []byte(`document.body.dataset.ready="true"`)); err != nil {
		t.Fatal(err)
	}
	gate, err := author.BuildPreview(context.Background(), principal, grant)
	if err != nil || !gate.Ready || len(gate.Preview.EvidenceDigests) != 1 {
		t.Fatalf("gate=%+v err=%v", gate, err)
	}
	finished, err := author.Finish(context.Background(), principal, grant)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := adapter.GetArtifact(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Head == nil || artifact.Head.CommitOID != finished.Revision.CommitOID || artifact.Head.Build == nil || artifact.Head.Build.Status != "succeeded" || artifact.Head.Validation == nil || artifact.Head.Validation.Status != "valid" {
		t.Fatalf("artifact=%+v finish=%+v", artifact, finished)
	}
	followup := tool.ArtifactV3PrepareTurnRequest{AccountScopeID: "account", UserID: "user", OwnerSessionID: "artifact-v3-runtime", TaskCallID: "followup", ArtifactID: grant.ArtifactID, BaseCommitOID: artifact.Head.CommitOID, ProjectionSeq: artifact.Revision, PolicyRevision: "policy", CandidateIndex: 1, Initial: false, TargetPartIDs: []string{"hero"}, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	preparedFollowup, err := adapter.PrepareArtifactV3Turn(context.Background(), followup)
	if err != nil {
		t.Fatalf("valid manifest target was rejected: %v", err)
	}
	followPrincipal := tool.ArtifactV3AuthorPrincipal{AccountScopeID: "account", UserID: "user", ProducerSessionID: "child", ProducerRunID: "repair-run"}
	preparedFollowup.ProducerSessionID, preparedFollowup.ProducerRunID = "child", "repair-run"
	if _, err := author.Inspect(context.Background(), followPrincipal, preparedFollowup); err != nil {
		t.Fatalf("materialize direct repair base: %v", err)
	}
	if err := author.Edit(context.Background(), followPrincipal, preparedFollowup, "index.html", []byte("Artifact V3"), []byte("Artifact V3 repaired"), false); err != nil {
		t.Fatal(err)
	}
	if gate, err := author.BuildPreview(context.Background(), followPrincipal, preparedFollowup); err != nil || !gate.Ready {
		t.Fatalf("repair gate=%+v err=%v", gate, err)
	}
	repair, err := author.Finish(context.Background(), followPrincipal, preparedFollowup)
	if err != nil {
		t.Fatal(err)
	}
	beforeSelect, err := adapter.GetArtifact(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID)
	if err != nil || beforeSelect.Head.CommitOID != finished.Revision.CommitOID {
		t.Fatalf("repair candidate moved head before selection: artifact=%+v err=%v", beforeSelect, err)
	}
	selected, err := adapter.SelectArtifactV3DirectHead(context.Background(), "account", "user", "artifact-v3-runtime", grant.ArtifactID, preparedFollowup.TurnID, preparedFollowup.CandidateID)
	if err != nil || selected.CommitOID != repair.Revision.CommitOID {
		t.Fatalf("direct repair selection=%+v repair=%+v err=%v", selected, repair, err)
	}
	artifact, err = adapter.GetArtifact(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID)
	if err != nil || artifact.Head.CommitOID != repair.Revision.CommitOID || len(artifact.Head.Parents) != 1 || artifact.Head.Parents[0] != finished.Revision.CommitOID {
		t.Fatalf("selected direct repair lineage artifact=%+v err=%v", artifact, err)
	}
	followup.TaskCallID, followup.TargetPartIDs, followup.BaseCommitOID, followup.ProjectionSeq = "unknown-target", []string{"missing"}, artifact.Head.CommitOID, artifact.Revision
	if _, err := adapter.PrepareArtifactV3Turn(context.Background(), followup); !errors.Is(err, pebblestore.ErrArtifactV3Invalid) {
		t.Fatalf("unknown manifest target error=%v", err)
	}
	preview, err := adapter.OpenPreview(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID, artifact.Head.RevisionRef, "", "")
	if err != nil || !strings.Contains(string(preview.Body), "Artifact V3") || !strings.Contains(string(preview.Body), "preview/files/styles/theme.css") {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	asset, err := adapter.OpenPreview(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID, artifact.Head.RevisionRef, "styles/theme.css", "")
	if err != nil || !strings.HasPrefix(asset.MediaType, "text/css") || !strings.Contains(string(asset.Body), "navy") {
		t.Fatalf("asset=%+v err=%v", asset, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := pebblestore.Open(filepath.Join(root, "session.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restartedService, err := pebblestore.NewArtifactV3Service(pebblestore.NewSessionStore(reopened), repositoryRoot, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	restarted := newArtifactV3RuntimeAdapter(restartedService, pebblestore.NewSessionStore(reopened), repositoryRoot, evidenceRoot, pebblestore.ArtifactV3Limits{}, artifactV3RuntimeRenderer{})
	if err := recoverArtifactV3Repositories(context.Background(), restarted); err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.GetArtifact(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID)
	if err != nil || recovered.Head.CommitOID != repair.Revision.CommitOID {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	evidence, err := restarted.ReadArtifactV3PreviewEvidence(context.Background(), "account", "user", "artifact-v3-runtime", grant.ArtifactID, artifact.Head.RevisionRef)
	if err != nil || string(evidence) != "real-renderer-evidence" {
		t.Fatalf("preview evidence=%q err=%v", evidence, err)
	}
	if _, err := restarted.ReadArtifactV3PreviewEvidence(context.Background(), "account", "foreign", "artifact-v3-runtime", grant.ArtifactID, artifact.Head.RevisionRef); err == nil {
		t.Fatal("foreign owner read Artifact V3 preview evidence")
	}
	if entries, err := os.ReadDir(repositoryRoot); err != nil {
		t.Fatalf("repos=%v err=%v", entries, err)
	} else {
		var repositories int
		for _, entry := range entries {
			if entry.IsDir() && strings.HasSuffix(entry.Name(), ".git") {
				repositories++
			}
		}
		if repositories != 1 {
			t.Fatalf("repos=%v", entries)
		}
	}
}

// Requirement: build and Git finish must share one strict manifest authority.
// The regression threat is a permissive preview accepting shorthand selectors
// or presentation metadata that strict commit validation rejects later.
func TestArtifactV3RuntimeBuildRejectsManifestThatFinishWouldReject(t *testing.T) {
	adapter := newArtifactV3RuntimeAdapter(nil, nil, t.TempDir(), t.TempDir(), pebblestore.ArtifactV3Limits{}, artifactV3RuntimeRenderer{})
	project := map[string][]byte{
		"swarm-artifact.json": []byte(`{"schema_version":"swarm.artifact/v3","entrypoint":"index.html","title":"Legacy shorthand","parts":[{"id":"hero","selector":"#hero"}]}`),
		"index.html":          []byte(`<!doctype html><html><body><main id="hero">Hero</main></body></html>`),
	}
	build, err := adapter.Build(context.Background(), tool.ArtifactV3BuildRequest{ArtifactID: "artifact", TurnID: "turn", Attempt: 1, Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "failed" || len(build.Diagnostics) != 1 || build.Diagnostics[0].Code != "manifest_invalid" {
		t.Fatalf("build=%+v", build)
	}
	if !strings.Contains(build.Diagnostics[0].Message, "label") || !strings.Contains(build.Diagnostics[0].Message, "locator") {
		t.Fatalf("diagnostic is not actionable: %+v", build.Diagnostics[0])
	}
}

type artifactV3SafeRendererFailure struct{}

func (artifactV3SafeRendererFailure) Capture(context.Context, htmlcapture.Request) ([]htmlcapture.Result, error) {
	return nil, htmlcapture.NewError("capture_viewport_overflow", "capture document overflows the required viewport")
}

// Requirement: safe renderer diagnostics must reach the authoring turn so the
// AI repairs the requested artifact instead of publishing a diagnostic probe.
func TestArtifactV3RuntimePreviewPreservesSafeRendererDiagnostic(t *testing.T) {
	adapter := newArtifactV3RuntimeAdapter(nil, nil, t.TempDir(), t.TempDir(), pebblestore.ArtifactV3Limits{}, artifactV3SafeRendererFailure{})
	project := map[string][]byte{
		"swarm-artifact.json": []byte(`{"schema_version":"swarm.artifact/v3","entrypoint":"index.html","parts":[{"id":"hero","label":"Hero","locator":{"kind":"selector","path":"index.html","value":"#hero"}}]}`),
		"index.html":          []byte(`<!doctype html><html><body><main id="hero">Hero</main></body></html>`),
	}
	build, err := adapter.Build(context.Background(), tool.ArtifactV3BuildRequest{ArtifactID: "artifact", TurnID: "turn", Attempt: 1, Project: project})
	if err != nil || build.Status != "succeeded" {
		t.Fatalf("build=%+v err=%v", build, err)
	}
	preview, err := adapter.Preview(context.Background(), tool.ArtifactV3PreviewRequest{ArtifactID: "artifact", TurnID: "turn", Attempt: 1, Build: build})
	if err != nil || preview.Status != "failed" || len(preview.Diagnostics) != 1 || preview.Diagnostics[0].Code != "capture_viewport_overflow" || !strings.Contains(preview.Diagnostics[0].Message, "overflows") {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
}

// Requirement: source bytes alone are never readiness evidence; a browser
// preview adapter is mandatory before a candidate can be finished.
func TestArtifactV3RuntimeAdapterFailsClosedWithoutBrowser(t *testing.T) {
	root := t.TempDir()
	adapter := newArtifactV3RuntimeAdapter(nil, nil, root, root, pebblestore.ArtifactV3Limits{}, nil)
	project := map[string][]byte{"swarm-artifact.json": []byte(`{"schema_version":"swarm.artifact/v3","entrypoint":"index.html","parts":[]}`), "index.html": []byte(`<!doctype html><html><body>ok</body></html>`)}
	build, err := adapter.Build(context.Background(), tool.ArtifactV3BuildRequest{ArtifactID: "artifact", TurnID: "turn", Attempt: 1, Project: project})
	if err != nil || build.Status != "succeeded" {
		t.Fatalf("build=%+v err=%v", build, err)
	}
	preview, err := adapter.Preview(context.Background(), tool.ArtifactV3PreviewRequest{ArtifactID: "artifact", TurnID: "turn", Attempt: 1, Build: build})
	if err != nil || preview.Status != "failed" || len(preview.EvidenceDigests) != 0 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
}
