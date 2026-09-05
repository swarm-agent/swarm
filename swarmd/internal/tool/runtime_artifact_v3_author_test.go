package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type artifactV3AuthorRepoFake struct {
	base    map[string][]byte
	submits []ArtifactV3SubmitRequest
}

func (f *artifactV3AuthorRepoFake) MaterializeBase(_ context.Context, _, _, destination string) error {
	for path, body := range f.base {
		full := filepath.Join(destination, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(full, body, 0o600); err != nil {
			return err
		}
	}
	return nil
}
func (f *artifactV3AuthorRepoFake) SubmitProject(_ context.Context, request ArtifactV3SubmitRequest) (ArtifactV3Revision, error) {
	f.submits = append(f.submits, request)
	return ArtifactV3Revision{CommitOID: strings.Repeat("a", 40), TreeOID: strings.Repeat("b", 40), ManifestBlobOID: strings.Repeat("c", 40)}, nil
}

type artifactV3BuilderFake struct {
	calls     int
	failFirst bool
}

func (f *artifactV3BuilderFake) Build(_ context.Context, request ArtifactV3BuildRequest) (ArtifactV3BuildResult, error) {
	f.calls++
	if f.failFirst && f.calls == 1 {
		return ArtifactV3BuildResult{Status: "failed", Diagnostics: []ArtifactV3Diagnostic{{Stage: "build", Code: "compile", Message: "compile failed", Path: "src/app.js", Line: 2}}}, nil
	}
	return ArtifactV3BuildResult{ID: "build", Status: "succeeded", OutputFiles: request.Project}, nil
}

type artifactV3PreviewerFake struct{ calls int }

func (f *artifactV3PreviewerFake) Preview(_ context.Context, _ ArtifactV3PreviewRequest) (ArtifactV3PreviewResult, error) {
	f.calls++
	return ArtifactV3PreviewResult{ID: "preview", Status: "valid", EvidenceDigests: []string{"pixels"}, Diagnostics: []ArtifactV3Diagnostic{{Stage: "locator", Code: "resolved", Message: "pricing resolved"}}}, nil
}

func fullArtifactV3Grant() ArtifactV3AuthorGrant {
	return ArtifactV3AuthorGrant{ID: "grant", ArtifactID: "artifact", OwnerSessionID: "owner", ProducerSessionID: "child", ProducerRunID: "run", TurnID: "turn", CandidateID: "candidate", BaseCommitOID: strings.Repeat("d", 40), PolicyRevision: "policy", AllowedActions: []string{artifactV3ActionInspect, artifactV3ActionList, artifactV3ActionRead, artifactV3ActionCreate, artifactV3ActionEdit, artifactV3ActionRename, artifactV3ActionDelete, artifactV3ActionDiff, artifactV3ActionBuild, artifactV3ActionFinish}, TargetPartIDs: []string{"pricing"}, LockedPaths: []string{"assets/locked.svg"}, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Limits: ArtifactV3AuthorLimits{MaxFileBytes: 4096, MaxTreeBytes: 32768, MaxFiles: 64}}
}
func artifactV3Principal() ArtifactV3AuthorPrincipal {
	return ArtifactV3AuthorPrincipal{AccountScopeID: "account", UserID: "user", ProducerSessionID: "child", ProducerRunID: "run"}
}

// Requirement: a targeted V3 turn receives the whole exact tree and may repair
// shared files outside the target before one complete candidate is submitted.
// Threat: V2-style target enforcement could reject cross-project coherence or
// submit independently authored bytes without a successful browser gate.
func TestArtifactV3AuthorWholeProjectRepairAndFinish(t *testing.T) {
	repo := &artifactV3AuthorRepoFake{base: map[string][]byte{"swarm-artifact.json": []byte(`{"schema_version":"swarm.artifact/v3","entrypoint":"index.html","parts":[]}`), "index.html": []byte(`<button data-plan="team">Team</button>`), "styles/theme.css": []byte(`:root{--accent:#fff}`), "src/plans.js": []byte(`team:29`), "assets/locked.svg": []byte(`<svg/>`)}}
	builder := &artifactV3BuilderFake{failFirst: true}
	previewer := &artifactV3PreviewerFake{}
	service := NewArtifactV3AuthorService(t.TempDir(), repo, builder, previewer)
	grant := fullArtifactV3Grant()
	principal := artifactV3Principal()
	ctx := context.Background()
	inspected, err := service.Inspect(ctx, principal, grant)
	if err != nil || len(inspected.Files) != 5 || inspected.TargetPartIDs[0] != "pricing" {
		t.Fatalf("inspect=%+v err=%v", inspected, err)
	}
	if err = service.Edit(ctx, principal, grant, "index.html", []byte(`data-plan="team"`), []byte(`data-plan="studio"`), false); err != nil {
		t.Fatal(err)
	}
	if err = service.Edit(ctx, principal, grant, "styles/theme.css", []byte(`#fff`), []byte(`#123456`), false); err != nil {
		t.Fatal(err)
	}
	if err = service.Edit(ctx, principal, grant, "src/plans.js", []byte(`team:29`), []byte(`studio:39`), false); err != nil {
		t.Fatal(err)
	}
	if err = service.Create(ctx, principal, grant, "src/new.js", []byte(`export const ready=true`)); err != nil {
		t.Fatal(err)
	}
	if err = service.Rename(ctx, principal, grant, "src/new.js", "src/shared.js"); err != nil {
		t.Fatal(err)
	}
	if err = service.Delete(ctx, principal, grant, "src/shared.js"); err != nil {
		t.Fatal(err)
	}
	if err = service.Edit(ctx, principal, grant, "assets/locked.svg", []byte("svg"), []byte("bad"), false); !errors.Is(err, ErrArtifactV3AuthorLocked) {
		t.Fatalf("locked edit=%v", err)
	}
	diff, err := service.Diff(ctx, principal, grant)
	if err != nil || len(diff.Changes) != 3 {
		t.Fatalf("diff=%+v err=%v", diff, err)
	}
	gate, err := service.BuildPreview(ctx, principal, grant)
	if err != nil || gate.Ready || len(gate.Diagnostics) == 0 {
		t.Fatalf("first gate=%+v err=%v", gate, err)
	}
	if _, err = service.Finish(ctx, principal, grant); !errors.Is(err, ErrArtifactV3AuthorNotReady) {
		t.Fatalf("finish before repair=%v", err)
	}
	gate, err = service.BuildPreview(ctx, principal, grant)
	if err != nil || !gate.Ready || previewer.calls != 1 {
		t.Fatalf("repaired gate=%+v err=%v", gate, err)
	}
	finished, err := service.Finish(ctx, principal, grant)
	if err != nil || finished.Revision.CommitOID == "" || len(repo.submits) != 1 {
		t.Fatalf("finish=%+v submits=%d err=%v", finished, len(repo.submits), err)
	}
	if got := string(repo.submits[0].Project["styles/theme.css"]); !strings.Contains(got, "#123456") {
		t.Fatalf("whole tree not submitted: %s", got)
	}
	if _, err = service.Finish(ctx, principal, grant); err != nil || len(repo.submits) != 1 {
		t.Fatalf("finish replay submits=%d err=%v", len(repo.submits), err)
	}
}

// Requirement: every filesystem operation is rooted in the private turn and
// every mutation invalidates prior build evidence. Threat: traversal, symlinks,
// caller-supplied destinations, or stale success evidence could escape the turn
// or commit bytes that were never previewed.
func TestArtifactV3AuthorContainmentCapabilityAndStaleGate(t *testing.T) {
	repo := &artifactV3AuthorRepoFake{base: map[string][]byte{"swarm-artifact.json": []byte(`{}`), "index.html": []byte("ok")}}
	service := NewArtifactV3AuthorService(t.TempDir(), repo, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
	grant := fullArtifactV3Grant()
	principal := artifactV3Principal()
	ctx := context.Background()
	if err := service.Create(ctx, principal, grant, "../escape", []byte("bad")); !errors.Is(err, ErrArtifactV3AuthorInvalid) {
		t.Fatalf("traversal=%v", err)
	}
	other := principal
	other.ProducerSessionID = "other"
	if _, err := service.Inspect(ctx, other, grant); !errors.Is(err, ErrArtifactV3AuthorUnauthorized) {
		t.Fatalf("foreign principal=%v", err)
	}
	gate, err := service.BuildPreview(ctx, principal, grant)
	if err != nil || !gate.Ready {
		t.Fatalf("gate=%+v err=%v", gate, err)
	}
	if err = service.Edit(ctx, principal, grant, "index.html", []byte("ok"), []byte("changed"), false); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Finish(ctx, principal, grant); !errors.Is(err, ErrArtifactV3AuthorNotReady) {
		t.Fatalf("stale gate=%v", err)
	}
	definition := artifactV3AuthorDefinition()
	properties := definition.Parameters["properties"].(map[string]any)
	for _, forbidden := range []string{"destination", "repository_id", "ref", "base_commit_oid", "policy", "build_command", "output_path", "parts", "part_id"} {
		if _, ok := properties[forbidden]; ok {
			t.Fatalf("schema exposes %q", forbidden)
		}
	}
}

func TestArtifactV3AuthorRuntimeRequiresTrustedContext(t *testing.T) {
	runtime := NewRuntime(1)
	runtime.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), &artifactV3AuthorRepoFake{}, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))
	scope := WorkspaceScope{SessionID: "child", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}}
	if _, err := runtime.executeArtifactV3Author(context.Background(), scope, "call", map[string]any{"action": artifactV3ActionInspect}); err == nil || !strings.Contains(err.Error(), "trusted context") {
		t.Fatalf("missing context=%v", err)
	}
	grant := fullArtifactV3Grant()
	grant.Initial = true
	grant.BaseCommitOID = ""
	ctx := WithArtifactV3AuthorRunContext(context.Background(), ArtifactV3AuthorRunContext{Grant: grant})
	if _, err := runtime.executeArtifactV3Author(ctx, scope, "call", map[string]any{"action": artifactV3ActionInspect, "destination": "bad"}); err == nil || !strings.Contains(err.Error(), "caller-authored") {
		t.Fatalf("redirection=%v", err)
	}
	otherScope := scope
	otherScope.SessionID = "other"
	if _, err := runtime.executeArtifactV3Author(ctx, otherScope, "call", map[string]any{"action": artifactV3ActionInspect}); err == nil || !strings.Contains(err.Error(), "producer") {
		t.Fatalf("producer mismatch=%v", err)
	}
	found := false
	for _, definition := range runtime.Definitions() {
		if definition.Name == "artifact_v3_author" {
			found = true
		}
	}
	if !found {
		t.Fatal("artifact_v3_author is not registered")
	}
}

// Requirement: first-use inspect_context must teach the canonical manifest without
// disclosing private turn paths or bypassing capability and publication gates.
// Threat: a drifting example causes repeated rejection; guidance could leak the
// private workspace or accidentally initialize/publish a project. This service
// test exercises Inspect and the real ValidateArtifactV3Project validator, the
// narrowest layer proving the returned example's schema and inspect postconditions.
func TestArtifactV3AuthorInspectManifestGuidance(t *testing.T) {
	ctx := context.Background()
	repo := &artifactV3AuthorRepoFake{}
	service := NewArtifactV3AuthorService(t.TempDir(), repo, nil, nil)
	grant := fullArtifactV3Grant()
	grant.Initial = true
	grant.BaseCommitOID = ""
	principal := artifactV3Principal()

	foreign := principal
	foreign.ProducerRunID = "foreign-run"
	denied, err := service.Inspect(ctx, foreign, grant)
	if !errors.Is(err, ErrArtifactV3AuthorUnauthorized) || denied.ManifestVersion != "" || len(service.turns) != 0 {
		t.Fatalf("unauthorized inspect returned guidance or created state: %+v, %v", denied, err)
	}
	inspected, err := service.Inspect(ctx, principal, grant)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.ManifestFilename != pebblestore.ArtifactV3ManifestFilename || inspected.ManifestVersion != pebblestore.ArtifactV3ManifestVersion {
		t.Fatalf("noncanonical manifest guidance: %+v", inspected)
	}
	body, err := json.Marshal(inspected.ManifestExample)
	if err != nil {
		t.Fatal(err)
	}
	project := pebblestore.ArtifactV3Project{Files: map[string][]byte{
		inspected.ManifestFilename: body,
		"index.html":               []byte(`<!doctype html><html><body><main id="main">Example</main></body></html>`),
	}}
	manifest, err := pebblestore.ValidateArtifactV3Project(project, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatalf("offered manifest example rejected by canonical validator: %v", err)
	}
	if manifest.SchemaVersion != inspected.ManifestVersion || len(manifest.Parts) != 1 || manifest.Parts[0].Locator.Kind != "selector" || manifest.Parts[0].Locator.Value != "#main" {
		t.Fatalf("example lacks canonical selector part: %+v", manifest)
	}
	delete(project.Files, "index.html")
	if _, err := pebblestore.ValidateArtifactV3Project(project, pebblestore.ArtifactV3Limits{}); !errors.Is(err, pebblestore.ErrArtifactV3Invalid) {
		t.Fatalf("example without entrypoint accepted: %v", err)
	}
	payload, err := json.Marshal(inspected)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{service.root, service.turns[artifactV3WorkspaceKey(grant)].root} {
		if strings.Contains(string(payload), private) {
			t.Fatal("inspect leaked private filesystem path")
		}
	}
	if len(inspected.Files) != 0 || inspected.LatestGate != nil {
		t.Fatalf("guidance created files or gate: %+v", inspected)
	}
	if _, err := service.Finish(ctx, principal, grant); !errors.Is(err, ErrArtifactV3AuthorNotReady) || len(repo.submits) != 0 {
		t.Fatalf("guidance bypassed publication gates: %v, submits=%d", err, len(repo.submits))
	}
}
