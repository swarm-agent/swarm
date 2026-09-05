package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type directArtifactV3RepoFake struct {
	artifactV3AuthorRepoFake
	turns    []ArtifactV3PrepareTurnRequest
	selected []string
}

func (f *directArtifactV3RepoFake) PrepareArtifactV3Turn(_ context.Context, request ArtifactV3PrepareTurnRequest) (ArtifactV3AuthorGrant, error) {
	f.turns = append(f.turns, request)
	index := len(f.turns)
	candidateIndex := request.CandidateIndex
	if candidateIndex < 1 {
		candidateIndex = 1
	}
	return ArtifactV3AuthorGrant{
		ID: fmt.Sprintf("direct-grant-%d", index), ArtifactID: "artifact-direct", OwnerSessionID: request.OwnerSessionID,
		TurnID: "turn-" + request.TaskCallID, CandidateID: "candidate-" + request.TaskCallID + "-" + fmt.Sprint(candidateIndex), PolicyRevision: request.PolicyRevision,
		BaseCommitOID: request.BaseCommitOID, Initial: request.Initial, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
		AllowedActions: []string{artifactV3ActionInspect, artifactV3ActionList, artifactV3ActionRead, artifactV3ActionCreate, artifactV3ActionEdit, artifactV3ActionRename, artifactV3ActionDelete, artifactV3ActionDiff, artifactV3ActionBuild, artifactV3ActionFinish},
	}, nil
}

func (f *directArtifactV3RepoFake) FailArtifactV3Turn(context.Context, ArtifactV3TurnFailure) error {
	return nil
}

func (f *directArtifactV3RepoFake) MaterializeBase(_ context.Context, _, _, destination string) error {
	if len(f.submits) == 0 {
		return errors.New("missing direct Artifact V3 base")
	}
	for path, body := range f.submits[0].Project {
		target := filepath.Join(destination, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, body, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (f *directArtifactV3RepoFake) ReadArtifactV3DirectRevision(_ context.Context, _, _, _, _, _ string) (map[string][]byte, []pebblestore.ArtifactV3Part, error) {
	if len(f.submits) == 0 {
		return nil, nil, errors.New("missing direct Artifact V3 revision")
	}
	return artifactV3Clone(f.submits[len(f.submits)-1].Project), []pebblestore.ArtifactV3Part{{ID: "hero", Label: "Hero", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#hero"}}, {ID: "pricing", Label: "Pricing", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#pricing"}}, {ID: "footer", Label: "Footer", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#footer"}}}, nil
}

func (f *directArtifactV3RepoFake) SelectArtifactV3DirectHead(_ context.Context, _, _, _, _, turnID, candidateID string) (ArtifactV3Revision, error) {
	f.selected = append(f.selected, turnID+":"+candidateID)
	return ArtifactV3Revision{CommitOID: "selected-repair-commit", TreeOID: "selected-repair-tree", ManifestBlobOID: "selected-repair-manifest"}, nil
}

// Requirement: ordinary primary Swarm can publish one complete HTML Artifact V3
// without Designer delegation or any V1/V2 artifact authority. The narrow tool
// layer proves schema exposure, stable Part derivation, the real author
// build/preview/finish sequence, and native exact revision output.
// Threat: removing create from the schema made providers repeatedly substitute
// generate_image, while restoring the retired V1/V2 writer would create a second
// current managed-artifact authority.
func TestManageArtifactCreateUsesDirectArtifactV3HTMLPath(t *testing.T) {
	repository := &directArtifactV3RepoFake{artifactV3AuthorRepoFake: artifactV3AuthorRepoFake{}}
	author := NewArtifactV3AuthorService(t.TempDir(), repository, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
	runtime := NewRuntime(1)
	runtime.SetArtifactV3AuthorService(author)
	scope := WorkspaceScope{SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1"}}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "session-1", RunID: "run-1"})
	html := `<!doctype html><html><body><main id="hero">Product hero</main><section id="pricing">Team $29</section><footer id="footer">Get started</footer></body></html>`
	requestedParts := []map[string]any{{"id": "hero", "label": "Hero", "kind": "semantic"}, {"id": "pricing", "label": "Pricing", "kind": "semantic"}, {"id": "footer", "label": "Footer", "kind": "semantic"}}
	arguments, err := json.Marshal(map[string]any{"action": "create", "filename": "index.html", "media_type": "text/html", "content": html, "collection_name": "Static product page", "parts": requestedParts})
	if err != nil {
		t.Fatal(err)
	}
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "direct-html", Name: "manage_artifact", Arguments: string(arguments)})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.submits) != 1 || !repository.submits[0].Initial || len(repository.submits[0].Project) != 2 {
		t.Fatalf("submits=%#v", repository.submits)
	}
	manifest := string(repository.submits[0].Project["swarm-artifact.json"])
	for _, expected := range []string{`"schema_version":"swarm.artifact/v3"`, `"id":"hero"`, `"id":"pricing"`, `"id":"footer"`} {
		if !strings.Contains(manifest, expected) {
			t.Fatalf("manifest missing %s: %s", expected, manifest)
		}
	}
	if !strings.Contains(output, `"artifact_v3"`) || !strings.Contains(output, `"part_count":3`) || !strings.Contains(output, `"revision_kind":"initial"`) || strings.Contains(output, "collection_id") || strings.Contains(output, "variant_id") {
		t.Fatalf("output=%s", output)
	}
	secondArguments, err := json.Marshal(map[string]any{"action": "create", "filename": "index.html", "media_type": "text/html", "content": strings.ReplaceAll(html, "Product hero", "Revised hero"), "collection_name": "Static product page", "parts": requestedParts})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "second-direct-html", Name: "manage_artifact", Arguments: string(secondArguments)})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.submits) != 2 || repository.submits[1].Initial || repository.submits[1].BaseCommitOID == "" || len(repository.selected) != 1 || !strings.Contains(second, `"commit_oid":"selected-repair-commit"`) || !strings.Contains(second, `"revision_kind":"visual_repair"`) || !strings.Contains(second, `"media_inspect_reference":{"artifact_id":"artifact-direct","revision_ref":"revision-selected-repair-commit"`) {
		t.Fatalf("changed same-run HTML did not create and select one native repair: submits=%#v selected=%#v output=%s", repository.submits, repository.selected, second)
	}
	third, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "third-direct-html", Name: "manage_artifact", Arguments: string(secondArguments)})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.submits) != 2 || len(repository.selected) != 1 || !strings.Contains(third, `"idempotent_replay":true`) || !strings.Contains(third, `do not recreate it`) {
		t.Fatalf("exact same-run replay was not explicit and idempotent: submits=%d selected=%d output=%s", len(repository.submits), len(repository.selected), third)
	}
	changedPartArguments, err := json.Marshal(map[string]any{"action": "create", "filename": "index.html", "media_type": "text/html", "content": strings.ReplaceAll(strings.ReplaceAll(html, "Product hero", "Another hero"), `id="hero"`, `id="hero-renamed"`), "collection_name": "Static product page", "parts": []map[string]any{{"id": "hero-renamed", "label": "Hero", "kind": "semantic"}, {"id": "pricing", "label": "Pricing", "kind": "semantic"}, {"id": "footer", "label": "Footer", "kind": "semantic"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "renamed-part", Name: "manage_artifact", Arguments: string(changedPartArguments)}); err == nil || !strings.Contains(err.Error(), "preserve the prior Artifact V3 stable Part IDs") {
		t.Fatalf("same-run repair changed stable Part identity: %v", err)
	}

	definition := manageArtifactDefinition()
	properties := definition.Parameters["properties"].(map[string]any)
	actions := properties["action"].(map[string]any)["enum"].([]string)
	found := false
	for _, action := range actions {
		if action == "create" {
			found = true
		}
	}
	if !found {
		t.Fatal("manage_artifact schema does not expose direct Artifact V3 create")
	}
	if !strings.Contains(definition.Description, "Do not substitute generate_image") || !strings.Contains(definition.Description, "native Artifact V3") {
		t.Fatalf("manage_artifact description does not distinguish HTML V3 creation from image generation: %s", definition.Description)
	}
}

// Requirement: createDirectArtifactV3HTML must reject invalid media and unresolved
// region references before allocating an author turn or publishing any revision.
// This tool-layer test keeps those negative boundaries independent of Part count.
func TestManageArtifactCreateV3RejectsNonHTMLAndMissingStableParts(t *testing.T) {
	repository := &directArtifactV3RepoFake{}
	runtime := NewRuntime(1)
	runtime.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), repository, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))
	scope := WorkspaceScope{SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1"}}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "session-1", RunID: "run-1"})
	for name, arguments := range map[string]string{
		"non-html":               `{"action":"create","filename":"note.txt","media_type":"text/plain","content":"text"}`,
		"missing-part":           `{"action":"create","filename":"index.html","media_type":"text/html","content":"<!doctype html><html><body><div>no stable part</div></body></html>"}`,
		"requested-part-missing": `{"action":"create","filename":"index.html","media_type":"text/html","content":"<!doctype html><html><body><main id=\"hero\">Hero</main></body></html>","parts":[{"id":"pricing","label":"Pricing","kind":"semantic"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: name, Name: "manage_artifact", Arguments: arguments}); err == nil {
				t.Fatal("expected rejection")
			}
			if len(repository.turns) != 0 || len(repository.submits) != 0 || len(repository.selected) != 0 {
				t.Fatal("invalid input allocated or published an Artifact V3 turn")
			}
		})
	}
}

// Requirement: createDirectArtifactV3HTML derives a variable number of stable
// regions without requiring an explicit Parts array or a three-Part layout.
// Threat: a page wrapper or fourth section must not reject a valid whole project;
// explicit selection must still control IDs, labels, and order. This tool-layer
// test exercises the real author service with hermetic build/preview fakes and
// verifies the submitted manifest and unchanged HTML, not only a ready status.
func TestManageArtifactCreateV3FlexiblePartCounts(t *testing.T) {
	for _, count := range []int{1, 2, 3, 4, 8, 16} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("count-%d/explicit-%t", count, explicit), func(t *testing.T) {
				repository := &directArtifactV3RepoFake{}
				author := NewArtifactV3AuthorService(t.TempDir(), repository, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
				runtime := NewRuntime(1)
				runtime.SetArtifactV3AuthorService(author)
				scope := WorkspaceScope{SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1"}}
				ctx, cancel := context.WithTimeout(WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "session-1", RunID: "run-1"}), 10*time.Second)
				defer cancel()

				// Include a wrapper and navigation to reproduce the original failure.
				var html strings.Builder
				html.WriteString(`<!doctype html><html><body><main id="page"><nav id="navigation">Navigation</nav>`)
				for index := 0; index < count; index++ {
					fmt.Fprintf(&html, `<section id="part-%d">Section %d</section>`, index, index)
				}
				html.WriteString(`</main></body></html>`)
				args := map[string]any{"action": "create", "filename": "index.html", "media_type": "text/html", "content": html.String()}
				wantIDs := []string{"page", "navigation"}
				wantLabels := []string{"Page", "Navigation"}
				if explicit {
					wantIDs, wantLabels = nil, nil
					requested := make([]map[string]any, 0, count)
					for index := count - 1; index >= 0; index-- {
						id, label := fmt.Sprintf("part-%d", index), fmt.Sprintf("Selected %d", index)
						requested = append(requested, map[string]any{"id": id, "label": label, "kind": "semantic"})
						wantIDs, wantLabels = append(wantIDs, id), append(wantLabels, label)
					}
					args["parts"] = requested
				} else {
					for index := 0; index < count; index++ {
						wantIDs = append(wantIDs, fmt.Sprintf("part-%d", index))
						wantLabels = append(wantLabels, fmt.Sprintf("Part %d", index))
					}
				}
				arguments, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "flexible-parts", Name: "manage_artifact", Arguments: string(arguments)})
				if err != nil {
					t.Fatal(err)
				}
				if len(repository.turns) != 1 || len(repository.submits) != 1 || !repository.submits[0].Initial {
					t.Fatalf("expected one initial publication: turns=%d submits=%#v", len(repository.turns), repository.submits)
				}
				project := repository.submits[0].Project
				if string(project["index.html"]) != html.String() {
					t.Fatal("Part discovery rewrote the authored HTML")
				}
				var manifest pebblestore.ArtifactV3Manifest
				if err := json.Unmarshal(project[pebblestore.ArtifactV3ManifestFilename], &manifest); err != nil {
					t.Fatal(err)
				}
				if len(manifest.Parts) != len(wantIDs) {
					t.Fatalf("parts=%#v want IDs=%v", manifest.Parts, wantIDs)
				}
				for index, part := range manifest.Parts {
					wantLocator := pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#" + wantIDs[index]}
					if part.ID != wantIDs[index] || part.Label != wantLabels[index] || !reflect.DeepEqual(part.Locator, wantLocator) {
						t.Fatalf("part[%d]=%#v want ID=%s label=%s locator=%#v", index, part, wantIDs[index], wantLabels[index], wantLocator)
					}
				}
				if !strings.Contains(output, fmt.Sprintf(`"part_count":%d`, len(wantIDs))) {
					t.Fatalf("output did not report derived count: %s", output)
				}
			})
		}
	}
}

// Requirement: the advertised motion_ui profile reaches the immutable native
// project and sequential Parts retain bounded midpoint samples. Reject override,
// missing-profile and invalid-time inputs before allocating any turn.
func TestManageArtifactCreateV3TemporalContract(t *testing.T) {
	for _, invalid := range []string{"", "override", "missing-profile", "invalid-time"} {
		t.Run("case-"+invalid, func(t *testing.T) {
			repository := &directArtifactV3RepoFake{}
			runtime := NewRuntime(1)
			runtime.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), repository, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))
			scope := WorkspaceScope{SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1"}}
			ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "session-1", RunID: "run-1"})
			args := map[string]any{"action": "create", "media_type": "text/html", "content": `<html><body><section id="scene">A scene</section></body></html>`, "animation_profile": map[string]any{"profile": "motion_ui"}, "parts": []map[string]any{{"id": "scene", "label": "Scene", "kind": "temporal", "start_ms": 0, "end_ms": 4000}}}
			switch invalid {
			case "override":
				args["animation_profile"] = map[string]any{"profile": "motion_ui", "network_allowed": true}
			case "missing-profile":
				delete(args, "animation_profile")
			case "invalid-time":
				args["parts"] = []map[string]any{{"id": "scene", "kind": "temporal", "start_ms": 4000, "end_ms": 4000}}
			}
			encoded, _ := json.Marshal(args)
			_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "temporal", Name: "manage_artifact", Arguments: string(encoded)})
			if invalid != "" {
				if err == nil || len(repository.turns) != 0 || len(repository.submits) != 0 {
					t.Fatalf("invalid input mutated publication: err=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var manifest pebblestore.ArtifactV3Manifest
			if err := json.Unmarshal(repository.submits[0].Project[pebblestore.ArtifactV3ManifestFilename], &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.AnimationProfile == nil || manifest.AnimationProfile.ProfileID != "motion_ui" || manifest.AnimationProfile.Budgets.NetworkAllowed || len(manifest.Parts) != 1 || manifest.Parts[0].CaptureTimeMS == nil || *manifest.Parts[0].CaptureTimeMS != 2000 || manifest.Parts[0].Locator.Value != "#scene" {
				t.Fatalf("temporal contract lost: %#v", manifest)
			}
		})
	}
}
