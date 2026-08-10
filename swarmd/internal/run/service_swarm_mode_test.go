package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"swarm/packages/swarmd/internal/swarmmode"
)

type fakeSwarmRouter struct {
	mu             sync.Mutex
	expansionCalls []swarmmode.GroupExpansionRequest
	refineCalls    []swarmmode.RefinementRequest
	failIdentity   string
	malformed      bool
	duplicate      bool
}

func (f *fakeSwarmRouter) OneShot(_ context.Context, _ string, payload any, _ map[string]any, identity string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if identity == f.failIdentity {
		return "", fmt.Errorf("synthetic Router failure")
	}
	switch request := payload.(type) {
	case swarmmode.GroupExpansionRequest:
		f.expansionCalls = append(f.expansionCalls, request)
		if f.malformed {
			return `{"themes":`, nil
		}
		result := swarmmode.GroupExpansionResult{Themes: make([]swarmmode.IndexedTheme, request.Count)}
		for offset := 0; offset < request.Count; offset++ {
			index := request.StartIndex + offset
			theme := "theme-" + strconv.Itoa(index)
			if len(request.Themes) != 0 {
				theme = request.Themes[offset]
			}
			if f.duplicate {
				theme = "duplicate"
			}
			result.Themes[offset] = swarmmode.IndexedTheme{Index: index, Theme: theme}
		}
		raw, _ := json.Marshal(result)
		return string(raw), nil
	case swarmmode.RefinementRequest:
		f.refineCalls = append(f.refineCalls, request)
		raw, _ := json.Marshal(swarmmode.RefinementResult{Index: request.Index, Prompt: "specialization-" + strconv.Itoa(request.Index) + " for " + request.Theme})
		return string(raw), nil
	default:
		return "", fmt.Errorf("unexpected Router payload %T", payload)
	}
}

func swarmPipelineRequest(count int) swarmmode.ToolRequest {
	return swarmmode.ToolRequest{Prompt: "Build independent variants", AgentType: swarmmode.AgentTypeDesigner, Count: count, OutputContract: "Create one reusable artifact", OwnedScopeTemplate: "web/variants/variant-{index}.tsx"}
}

func TestRunSwarmRouterPipelineBatchingAndOrdering(t *testing.T) {
	for _, count := range []int{1, 10, 11, 100} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			fake := &fakeSwarmRouter{}
			themes, refined, err := runSwarmRouterPipeline(context.Background(), swarmPipelineRequest(count), fake, nil)
			if err != nil {
				t.Fatalf("runSwarmRouterPipeline(): %v", err)
			}
			wantGroups := (count + swarmmode.RouterGroupSize - 1) / swarmmode.RouterGroupSize
			if len(fake.expansionCalls) != wantGroups || len(fake.refineCalls) != count {
				t.Fatalf("calls = %d expansion, %d refinement; want %d, %d", len(fake.expansionCalls), len(fake.refineCalls), wantGroups, count)
			}
			if len(themes) != count || len(refined) != count {
				t.Fatalf("results = %d themes, %d refined; want %d each", len(themes), len(refined), count)
			}
			for i := 0; i < count; i++ {
				if themes[i].Index != i+1 || refined[i].Index != i+1 {
					t.Fatalf("result %d ordering = theme %d refine %d", i, themes[i].Index, refined[i].Index)
				}
			}
		})
	}
}

func TestRunSwarmRouterPipelinePreservesSuppliedThemes(t *testing.T) {
	request := swarmPipelineRequest(11)
	request.Themes = make([]string, request.Count)
	for i := range request.Themes {
		request.Themes[i] = fmt.Sprintf("seed-%02d", i+1)
	}
	fake := &fakeSwarmRouter{}
	themes, _, err := runSwarmRouterPipeline(context.Background(), request, fake, nil)
	if err != nil {
		t.Fatalf("runSwarmRouterPipeline(): %v", err)
	}
	for i, item := range themes {
		if item.Theme != request.Themes[i] {
			t.Fatalf("theme %d = %q, want %q", i+1, item.Theme, request.Themes[i])
		}
	}
}

func TestRunSwarmRouterPipelineFailsClosedByStage(t *testing.T) {
	for _, test := range []struct {
		name       string
		fake       *fakeSwarmRouter
		want       string
		refinement int
	}{
		{name: "group failure", fake: &fakeSwarmRouter{failIdentity: "expand:2"}, want: "expansion stage group 2", refinement: 0},
		{name: "malformed", fake: &fakeSwarmRouter{malformed: true}, want: "expansion stage group 1", refinement: 0},
		{name: "duplicate", fake: &fakeSwarmRouter{duplicate: true}, want: "expansion stage group 1", refinement: 0},
		{name: "refinement", fake: &fakeSwarmRouter{failIdentity: "refine:7"}, want: "refinement stage item 7", refinement: 11},
	} {
		t.Run(test.name, func(t *testing.T) {
			themes, refined, err := runSwarmRouterPipeline(context.Background(), swarmPipelineRequest(11), test.fake, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want stage %q", err, test.want)
			}
			if themes != nil || refined != nil {
				t.Fatalf("failed pipeline returned partial final results")
			}
			if len(test.fake.refineCalls) != test.refinement {
				t.Fatalf("refinement calls = %d, want %d", len(test.fake.refineCalls), test.refinement)
			}
		})
	}
}

func TestBuildSwarmTaskArgumentsDesignerTargetsAndCoderIsolation(t *testing.T) {
	designer := swarmPipelineRequest(3)
	refined := []swarmmode.RefinementResult{{Index: 1, Prompt: "one"}, {Index: 2, Prompt: "two"}, {Index: 3, Prompt: "three"}}
	parsed, err := buildSwarmTaskArguments(designer, refined)
	if err != nil {
		t.Fatalf("build designer arguments: %v", err)
	}
	seen := map[string]bool{}
	for i, launch := range parsed.Launches {
		if len(launch.OwnedScope) != 1 || seen[launch.OwnedScope[0]] {
			t.Fatalf("designer launch %d has non-unique target %#v", i, launch.OwnedScope)
		}
		seen[launch.OwnedScope[0]] = true
	}

	coder := designer
	coder.AgentType = swarmmode.AgentTypeCoder
	coder.OwnedScopeTemplate = ""
	parsed, err = buildSwarmTaskArguments(coder, refined)
	if err != nil {
		t.Fatalf("build coder arguments: %v", err)
	}
	for i, launch := range parsed.Launches {
		if len(launch.OwnedScope) != 1 || launch.OwnedScope[0] != "." {
			t.Fatalf("coder launch %d scope = %#v, want canonical isolated-worktree scope", i, launch.OwnedScope)
		}
	}
}

func TestValidateApprovedSwarmModeArgumentsBindsExactRequest(t *testing.T) {
	request := swarmPipelineRequest(2)
	parsed, err := buildSwarmTaskArguments(request, nil)
	if err != nil {
		t.Fatal(err)
	}
	taskManifest := taskLaunchManifest{PathID: taskLaunchPermissionPathID, LaunchCount: request.Count, Launches: make([]taskLaunchManifestRow, request.Count)}
	for i, launch := range parsed.Launches {
		taskManifest.Launches[i] = taskLaunchManifestRow{RequestedSubagentType: launch.RequestedSubagentType, MetaPrompt: launch.MetaPrompt}
	}
	taskDigest, err := taskLaunchManifestDigest(taskManifest)
	if err != nil {
		t.Fatal(err)
	}
	taskManifest.ManifestHash = taskDigest
	manifest := swarmModePermissionManifest{PathID: swarmModePermissionPathID, Tool: "swarm_mode", Request: request, LaunchCount: request.Count, RouterGroupCount: 1, TaskManifest: taskManifest}
	digest, err := swarmModeManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"manifest_hash": taskDigest, "manifest": taskManifest, "swarm_manifest_hash": digest, "swarm_request": request})
	if err := validateApprovedSwarmModeArguments(request, string(raw)); err != nil {
		t.Fatalf("validate approved request: %v", err)
	}
	changed := request
	changed.Prompt = "Different brief"
	if err := validateApprovedSwarmModeArguments(changed, string(raw)); err == nil {
		t.Fatal("changed request unexpectedly matched approved manifest")
	}
}

func TestSwarmModeManifestDigestIsStableAndExact(t *testing.T) {
	manifest := swarmModePermissionManifest{PathID: swarmModePermissionPathID, Tool: "swarm_mode", Request: swarmPipelineRequest(10), LaunchCount: 10, RouterGroupCount: 1}
	first, err := swarmModeManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := swarmModeManifestDigest(manifest)
	if err != nil || first != second {
		t.Fatalf("stable digest = %q, %q, %v", first, second, err)
	}
	manifest.LaunchCount = 11
	third, err := swarmModeManifestDigest(manifest)
	if err != nil || third == first {
		t.Fatalf("changed final count did not change digest")
	}
}
