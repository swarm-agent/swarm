package runtime

import (
	"context"
	"encoding/json"
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
	if request.Entry == "" || len(request.Files) == 0 {
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
	preview, err := adapter.OpenPreview(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID, artifact.Head.RevisionRef, "")
	if err != nil || !strings.Contains(string(preview.Body), "Artifact V3") || !strings.Contains(string(preview.Body), "preview/files/styles/theme.css") {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	asset, err := adapter.OpenPreview(context.Background(), api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "artifact-v3-runtime", grant.ArtifactID, artifact.Head.RevisionRef, "styles/theme.css")
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
	if err != nil || recovered.Head.CommitOID != finished.Revision.CommitOID {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
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
