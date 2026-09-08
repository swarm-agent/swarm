package tool

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func narrationExampleArgs(t *testing.T) map[string]any {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal([]byte(artifactNarrationCreateExample), &args); err != nil {
		t.Fatal(err)
	}
	return args
}

func narrationTestRuntime(t *testing.T, builder ArtifactV3Builder, previewer ArtifactV3Previewer) (*Runtime, *directArtifactV3RepoFake, WorkspaceScope, context.Context) {
	t.Helper()
	repo := &directArtifactV3RepoFake{}
	runtime := NewRuntime(1)
	runtime.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), repo, builder, previewer))
	scope := WorkspaceScope{SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1"}}
	ctx, cancel := context.WithTimeout(WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "session-1", RunID: "run-narration"}), 10*time.Second)
	t.Cleanup(cancel)
	return runtime, repo, scope, ctx
}

// Requirement: the model-visible example must execute through registered create,
// producing stable, independently labeled narration/context/direction locators in
// one native project. Threat: schema-only examples or an alternate writer bypass
// principal/build/preview authority. This tool integration layer uses the real
// author service and asserts submitted project, exact references and replay state;
// hermetic build/browser/repository doubles are not installed-runtime evidence.
func TestNarrationPlanExampleNativePublication(t *testing.T) {
	builder, previewer := &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}
	runtime, repo, scope, ctx := narrationTestRuntime(t, builder, previewer)
	schema := manageArtifactDefinition().Parameters["properties"].(map[string]any)["narration_plan"].(map[string]any)
	if !strings.Contains(schema["description"].(string), artifactNarrationCreateExample) || schema["additionalProperties"] != false {
		t.Fatal("missing executable closed-schema example")
	}
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "example", Name: "manage_artifact", Arguments: artifactNarrationCreateExample})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 1 || len(repo.submits) != 1 || builder.calls != 1 || previewer.calls != 1 || len(repo.selected) != 0 {
		t.Fatal("native initial build/preview/submit sequence not preserved")
	}
	if repo.turns[0].AccountScopeID != "account-1" || repo.turns[0].UserID != "user-1" || repo.turns[0].OwnerSessionID != scope.SessionID {
		t.Fatal("trusted ownership lost")
	}
	var manifest pebblestore.ArtifactV3Manifest
	if err := json.Unmarshal(repo.submits[0].Project[pebblestore.ArtifactV3ManifestFilename], &manifest); err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"context-opening", "narration-opening", "visual-opening", "music-opening", "context-resolve", "narration-resolve"}
	wantLabels := []string{"Scene — Opening", "Narration — Opening", "Visual direction — Opening", "Music direction — Opening", "Scene — Resolve", "Narration — Resolve"}
	if !reflect.DeepEqual(artifactV3ManifestPartIDs(manifest.Parts), wantIDs) {
		t.Fatalf("parts=%#v", manifest.Parts)
	}
	for i, part := range manifest.Parts {
		if part.Label != wantLabels[i] || part.Locator.Kind != "selector" || part.Locator.Path != "index.html" || part.Locator.Value != "#"+wantIDs[i] {
			t.Fatalf("part=%#v", part)
		}
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	ref := result["reference"].(map[string]any)
	if ref["session_id"] != scope.SessionID || ref["artifact_id"] != "artifact-direct" || ref["revision_ref"] != "revision-"+strings.Repeat("a", 40) || strings.Contains(output, "collection_id") || strings.Contains(output, "variant_id") {
		t.Fatalf("wrong native reference: %s", output)
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "replay", Name: "manage_artifact", Arguments: artifactNarrationCreateExample})
	if err != nil || len(repo.submits) != 1 || builder.calls != 1 || previewer.calls != 1 {
		t.Fatalf("replay mutated state: %v", err)
	}
}

// Requirement: untrusted plain text stays literal, and changing text/title/order
// never changes scene-derived identity. Threat: tag/attribute injection, selector
// escape and accidentally nested edit targets. A parsed DOM proves actual text
// and element boundaries more narrowly than a browser or source-string snapshot.
func TestNarrationPlanEscapedSeparateStableParts(t *testing.T) {
	args := narrationExampleArgs(t)
	plan := args["narration_plan"].(map[string]any)
	scene := plan["scenes"].([]any)[0].(map[string]any)
	payload := `</p><script>alert("x")</script><img src=x onerror=alert(1)> & "quoted"`
	plan["title"], scene["title"], scene["narration"], scene["visual_direction"], scene["music_direction"] = payload, payload, payload, payload, payload
	body, parts, err := renderArtifactNarrationPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	// The code-owned body is balanced XML-compatible markup; parse that exact
	// subtree with the standard library (not a second HTML-parser dependency).
	start := strings.Index(body, "<body>")
	end := strings.Index(body, "</body>") + len("</body>")
	decoder := xml.NewDecoder(strings.NewReader(body[start:end]))
	byID := map[string]string{}
	var stack []string
	active := ""
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch node := token.(type) {
		case xml.StartElement:
			if node.Name.Local == "script" || node.Name.Local == "img" {
				t.Fatal("injected element")
			}
			id := ""
			for _, attr := range node.Attr {
				if strings.HasPrefix(attr.Name.Local, "on") {
					t.Fatal("injected handler")
				}
				if attr.Name.Local == "id" {
					id = attr.Value
				}
			}
			if id != "" {
				if _, exists := byID[id]; exists || active != "" {
					t.Fatal("duplicate or nested Part")
				}
				byID[id], active = "", id
			}
			stack = append(stack, id)
		case xml.CharData:
			if active != "" {
				byID[active] += string(node)
			}
		case xml.EndElement:
			if stack[len(stack)-1] != "" {
				active = ""
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(byID) != len(parts) {
		t.Fatal("markup and Parts diverged")
	}
	for _, part := range parts {
		text, exists := byID[part.ID]
		if !exists {
			t.Fatalf("missing %s", part.ID)
		}
		if strings.HasSuffix(part.ID, "-opening") && strings.Count(text, payload) != 1 {
			t.Fatalf("literal text did not round trip for %s: %s", part.ID, text)
		}
	}
	if parts[1].Label != "Narration — "+payload {
		t.Fatal("plain text label was HTML-encoded or truncated")
	}
	scene["title"], scene["narration"] = "Renamed", "Rewritten voice"
	_, revised, err := renderArtifactNarrationPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	for i := range parts {
		if revised[i].ID != parts[i].ID || revised[i].Selector != parts[i].Selector {
			t.Fatal("text edit changed identity")
		}
	}
	scenes := plan["scenes"].([]any)
	plan["scenes"] = []any{scenes[1], scenes[0]}
	_, reordered, err := renderArtifactNarrationPlan(plan)
	if err != nil || reordered[0].ID != "context-resolve" || reordered[3].ID != "narration-opening" {
		t.Fatalf("reorder renumbered IDs: %v %#v", err, reordered)
	}
}

// Requirement: invalid/conflicting structured input fails before allocating any
// native turn, publication, or selection. Threat: unknown options/markup/duplicate
// IDs, oversized input, null coercion and foreign run identity. Exercise the
// execution boundary directly as well as registered-schema validation: both must
// fail closed, even when model schema validation is absent.
func TestNarrationPlanRejectsInvalidWithoutPublication(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"null-plan":    func(a map[string]any) { a["narration_plan"] = nil },
		"string-plan":  func(a map[string]any) { a["narration_plan"] = "{}" },
		"unknown-plan": func(a map[string]any) { a["narration_plan"].(map[string]any)["html"] = "<script>" },
		"no-scenes":    func(a map[string]any) { a["narration_plan"].(map[string]any)["scenes"] = []any{} },
		"many-scenes":  func(a map[string]any) { a["narration_plan"].(map[string]any)["scenes"] = make([]any, 9) },
		"blank-title":  func(a map[string]any) { a["narration_plan"].(map[string]any)["title"] = " " },
		"long-title":   func(a map[string]any) { a["narration_plan"].(map[string]any)["title"] = strings.Repeat("x", 161) },
		"non-html":     func(a map[string]any) { a["media_type"] = "text/plain" },
		"other-action": func(a map[string]any) { a["action"] = "read_v3" },
	}
	for _, field := range []string{"content", "parts", "entries", "initial_parts", "content_base64", "output_requirements"} {
		mutations["conflict-"+field] = func(a map[string]any) { a[field] = nil }
	}
	for name, value := range map[string]any{"empty": "", "spaces": " ", "uppercase": "Opening", "selector": "x.y", "markup": `x\" onclick=alert(1)`, "long": strings.Repeat("a", 49), "duplicate": "resolve", "null": nil} {
		mutations["id-"+name] = func(a map[string]any) {
			a["narration_plan"].(map[string]any)["scenes"].([]any)[0].(map[string]any)["id"] = value
		}
	}
	for name, value := range map[string]any{"blank": " ", "null": nil, "long": strings.Repeat("x", 8001), "utf8": string([]byte{0xff}), "nul": "a\x00b", "object": map[string]any{"html": "x"}} {
		mutations["narration-"+name] = func(a map[string]any) {
			a["narration_plan"].(map[string]any)["scenes"].([]any)[0].(map[string]any)["narration"] = value
		}
	}
	for _, field := range []string{"visual_direction", "music_direction", "unknown"} {
		mutations["scene-"+field] = func(a map[string]any) {
			a["narration_plan"].(map[string]any)["scenes"].([]any)[0].(map[string]any)[field] = " "
		}
	}
	mutations["total-limit"] = func(a map[string]any) {
		var scenes []any
		for i := 0; i < 8; i++ {
			scenes = append(scenes, map[string]any{"id": fmt.Sprintf("scene-%d", i), "title": "Scene", "narration": strings.Repeat("x", 8000), "visual_direction": strings.Repeat("x", 8000), "music_direction": strings.Repeat("x", 8000)})
		}
		a["narration_plan"].(map[string]any)["scenes"] = scenes
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			builder, previewer := &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}
			runtime, repo, scope, ctx := narrationTestRuntime(t, builder, previewer)
			args := narrationExampleArgs(t)
			mutate(args)
			if _, err := runtime.executeManageArtifact(ctx, scope, name, args); err == nil {
				t.Fatal("expected rejection")
			}
			if len(repo.turns)+len(repo.submits)+len(repo.selected)+builder.calls+previewer.calls != 0 {
				t.Fatal("invalid input allocated native state")
			}
		})
	}
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprintf("identity-foreign-%t", foreign), func(t *testing.T) {
			runtime, repo, scope, _ := narrationTestRuntime(t, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
			ctx := context.Background()
			if foreign {
				ctx = WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "foreign-session", RunID: "run-foreign"})
			}
			if _, err := runtime.executeManageArtifact(ctx, scope, "identity", narrationExampleArgs(t)); err == nil {
				t.Fatal("untrusted run accepted")
			}
			if len(repo.turns)+len(repo.submits)+len(repo.selected) != 0 {
				t.Fatal("identity rejection mutated state")
			}
		})
	}
}

type narrationPreviewFailure struct{}

func (*narrationPreviewFailure) Preview(context.Context, ArtifactV3PreviewRequest) (ArtifactV3PreviewResult, error) {
	return ArtifactV3PreviewResult{}, errors.New("injected preview failure")
}

// Requirement: structured rendering must never shortcut build/browser gates or
// record failed output as a ready replay. The real author orchestration with an
// injected failure is the narrowest proof of zero publication/head mutation.
func TestNarrationPlanPublicationGatesFailClosed(t *testing.T) {
	for _, stage := range []string{"build", "preview"} {
		t.Run(stage, func(t *testing.T) {
			builder := &artifactV3BuilderFake{failFirst: stage == "build"}
			var previewer ArtifactV3Previewer = &artifactV3PreviewerFake{}
			if stage == "preview" {
				previewer = &narrationPreviewFailure{}
			}
			runtime, repo, scope, ctx := narrationTestRuntime(t, builder, previewer)
			if _, err := runtime.executeManageArtifact(ctx, scope, "gate", narrationExampleArgs(t)); err == nil {
				t.Fatal("failed gate accepted")
			}
			if len(repo.turns) != 1 || len(repo.submits) != 0 || len(repo.selected) != 0 || len(runtime.directArtifactV3ByRun) != 0 {
				t.Fatal("failed gate published or cached ready state")
			}
		})
	}
}

// Requirement: every allowed scene, including the maximum eight with all optional
// directions, must survive native manifest limits and later HTML Part discovery.
// Threat: silent truncation loses narration targets; an ID derived from position
// makes a later complete revision incompatible. The author/tool layer proves the
// real submitted manifest count and exact discovery locators, without a browser.
func TestNarrationPlanSceneBoundsAndRevisionDiscovery(t *testing.T) {
	for _, count := range []int{1, artifactNarrationMaxScenes} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			runtime, repo, scope, ctx := narrationTestRuntime(t, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
			args := narrationExampleArgs(t)
			var scenes []any
			for i := 0; i < count; i++ {
				scenes = append(scenes, map[string]any{"id": fmt.Sprintf("scene-%d", i), "title": "A scene", "narration": "  A line.\nAnother line.  ", "visual_direction": "Visual only", "music_direction": "Music only"})
			}
			args["narration_plan"].(map[string]any)["scenes"] = scenes
			encoded, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "bounds", Name: "manage_artifact", Arguments: string(encoded)}); err != nil {
				t.Fatal(err)
			}
			var manifest pebblestore.ArtifactV3Manifest
			if err := json.Unmarshal(repo.submits[0].Project[pebblestore.ArtifactV3ManifestFilename], &manifest); err != nil {
				t.Fatal(err)
			}
			body := repo.submits[0].Project["index.html"]
			derived := deriveArtifactHTMLParts(body, "text/html")
			if len(manifest.Parts) != count*4 || len(derived) != count*4 {
				t.Fatalf("Parts truncated: manifest=%d derived=%d", len(manifest.Parts), len(derived))
			}
			for i, part := range manifest.Parts {
				if part.ID != derived[i].ID || part.Locator.Value != derived[i].Selector {
					t.Fatal("later revision discovery changed identity")
				}
			}
			if strings.Count(string(body), "  A line.\nAnother line.  ") != count {
				t.Fatal("plain narration whitespace changed")
			}
		})
	}
}

type narrationRevisionRepoFake struct{ *directArtifactV3RepoFake }

func (f narrationRevisionRepoFake) ReadArtifactV3DirectRevision(_ context.Context, account, user, session, artifact, revision string) (map[string][]byte, []pebblestore.ArtifactV3Part, error) {
	if account != "account-1" || user != "user-1" || session != "session-1" || artifact != "artifact-direct" || revision != "revision-"+strings.Repeat("a", 40) || len(f.submits) == 0 {
		return nil, nil, errors.New("unknown exact narration base")
	}
	project := artifactV3Clone(f.submits[0].Project)
	var manifest pebblestore.ArtifactV3Manifest
	if err := json.Unmarshal(project[pebblestore.ArtifactV3ManifestFilename], &manifest); err != nil {
		return nil, nil, err
	}
	return project, manifest.Parts, nil
}

// Requirement: automatically rendered multi-scene narration can be read and revised
// through the existing exact-base native path for one or several narration Parts.
// Threat: a direction target leaks into intent, unselected text/IDs disappear, or
// revising a draft implicitly selects head. Exercise the registered tools and real
// author orchestration with hermetic publication doubles; no installed runtime.
func TestNarrationPlanSingleAndMultiTargetRevision(t *testing.T) {
	for _, targets := range [][]string{{"narration-opening"}, {"narration-opening", "narration-resolve"}} {
		t.Run(strings.Join(targets, "+"), func(t *testing.T) {
			runtime, repo, scope, ctx := narrationTestRuntime(t, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
			runtime.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), narrationRevisionRepoFake{repo}, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))
			call := func(id string, args map[string]any) (map[string]any, error) {
				encoded, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: id, Name: "manage_artifact", Arguments: string(encoded)})
				if err != nil {
					return nil, err
				}
				var result map[string]any
				if err := json.Unmarshal([]byte(output), &result); err != nil {
					t.Fatal(err)
				}
				payload, ok := result["artifact_v3"].(map[string]any)
				if !ok {
					t.Fatal("registered tool omitted native payload")
				}
				return payload, nil
			}
			created, err := call("create-narration", narrationExampleArgs(t))
			if err != nil {
				t.Fatal(err)
			}
			createdRef := created["reference"].(map[string]any)
			reference := map[string]any{"session_id": createdRef["session_id"], "artifact_id": createdRef["artifact_id"], "revision_ref": createdRef["revision_ref"]}
			read, err := call("read-narration", map[string]any{"action": "read_v3", "artifact_v3_reference": reference})
			if err != nil {
				t.Fatal(err)
			}
			original := read["content"].(string)
			revised := strings.Replace(original, "Start with one idea.", "Begin with an idea.", 1)
			if len(targets) == 2 {
				revised = strings.Replace(revised, "What will you build?", "What comes next?", 1)
			}
			args := map[string]any{"action": "revise_v3", "artifact_v3_reference": reference, "target_part_ids": targets, "content": revised}
			result, err := call("revise-narration", args)
			if err != nil {
				t.Fatal(err)
			}
			if len(repo.turns) != 2 || len(repo.submits) != 2 || len(repo.selected) != 0 || repo.submits[1].Initial || repo.submits[1].BaseCommitOID != strings.Repeat("a", 40) || !reflect.DeepEqual(repo.turns[1].TargetPartIDs, targets) {
				t.Fatal("exact target/base or explicit head authority lost")
			}
			if result["status"] != "awaiting_selection" || result["base_revision_ref"] != created["reference"].(map[string]any)["revision_ref"] {
				t.Fatal("wrong candidate status/base")
			}
			if string(repo.submits[0].Project["index.html"]) != original || string(repo.submits[1].Project["index.html"]) != revised {
				t.Fatal("base or candidate bytes changed")
			}
			if !reflect.DeepEqual(repo.submits[0].Project[pebblestore.ArtifactV3ManifestFilename], repo.submits[1].Project[pebblestore.ArtifactV3ManifestFilename]) {
				t.Fatal("revision changed Part identity/labels")
			}
			// One unknown member rejects the entire target set before allocating a turn.
			args["target_part_ids"] = []string{"narration-opening", "narration-missing"}
			if _, err := call("invalid-narration-target", args); err == nil {
				t.Fatal("unknown target accepted")
			}
			if len(repo.turns) != 2 || len(repo.submits) != 2 || len(repo.selected) != 0 {
				t.Fatal("invalid target partially mutated native state")
			}
		})
	}
}
