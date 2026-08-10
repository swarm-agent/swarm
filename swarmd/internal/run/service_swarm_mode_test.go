package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/swarmmode"
)

type fakeSwarmRouter struct {
	mu       sync.Mutex
	calls    []string
	requests []any
	failAt   string
}

func (f *fakeSwarmRouter) OneShot(_ context.Context, _ string, payload any, _ map[string]any, identity string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, identity)
	f.requests = append(f.requests, payload)
	if identity == f.failAt {
		return "", fmt.Errorf("synthetic Router failure")
	}
	switch request := payload.(type) {
	case swarmmode.RoundOneRequest:
		result := swarmmode.RoundOneResult{Themes: make([]swarmmode.IndexedTheme, request.Count)}
		for i := range result.Themes {
			theme := fmt.Sprintf("theme-%d", i+1)
			if len(request.Themes) > 0 {
				theme = request.Themes[i]
			}
			result.Themes[i] = swarmmode.IndexedTheme{Index: i + 1, Theme: theme}
		}
		raw, _ := json.Marshal(result)
		return string(raw), nil
	case swarmmode.RoundTwoRequest:
		result := swarmmode.RoundTwoResult{Prompts: make([]swarmmode.RefinedPrompt, len(request.Themes))}
		for i, theme := range request.Themes {
			result.Prompts[i] = swarmmode.RefinedPrompt{Index: i + 1, Prompt: fmt.Sprintf("specialized worker %d for %s", i+1, theme.Theme)}
		}
		raw, _ := json.Marshal(result)
		return string(raw), nil
	default:
		return "", fmt.Errorf("unexpected Router payload %T", payload)
	}
}

func swarmPipelineRequest(count int) swarmmode.ToolRequest {
	return swarmmode.ToolRequest{
		Prompt: "Build independent variants", AgentType: swarmmode.AgentTypeDesigner,
		Count: count, OutputContract: "Create one reusable artifact",
		OwnedScopeTemplate: "web/variants/variant-{index}.tsx",
	}
}

func TestRunSwarmRouterPipelineUsesExactlyTwoSequentialRounds(t *testing.T) {
	fake := &fakeSwarmRouter{}
	progress := make([]string, 0, 4)
	themes, refined, err := runSwarmRouterPipeline(context.Background(), swarmPipelineRequest(20), fake, func(round int, state, _ string) {
		progress = append(progress, fmt.Sprintf("%d:%s", round, state))
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(fake.calls, ","), "hydrate:1,hydrate:2"; got != want {
		t.Fatalf("Router calls = %q, want %q", got, want)
	}
	if len(themes) != 20 || len(refined) != 20 {
		t.Fatalf("results = %d themes, %d prompts", len(themes), len(refined))
	}
	if got, want := strings.Join(progress, ","), "1:running,1:completed,2:running,2:completed"; got != want {
		t.Fatalf("progress = %q, want %q", got, want)
	}
}

func TestRunSwarmRouterPipelineStopsBeforeRoundTwoOnFailure(t *testing.T) {
	fake := &fakeSwarmRouter{failAt: "hydrate:1"}
	if _, _, err := runSwarmRouterPipeline(context.Background(), swarmPipelineRequest(20), fake, nil); err == nil || !strings.Contains(err.Error(), "round 1") {
		t.Fatalf("error = %v", err)
	}
	if got := strings.Join(fake.calls, ","); got != "hydrate:1" {
		t.Fatalf("calls = %q", got)
	}
}

func TestBuildSwarmTaskArgumentsUsesHydratedPromptsAndCanonicalScopes(t *testing.T) {
	request := swarmPipelineRequest(3)
	refined := []swarmmode.RefinedPrompt{{Index: 1, Prompt: "one"}, {Index: 2, Prompt: "two"}, {Index: 3, Prompt: "three"}}
	parsed, err := buildSwarmTaskArguments(request, refined)
	if err != nil {
		t.Fatal(err)
	}
	for i, launch := range parsed.Launches {
		if launch.MetaPrompt != refined[i].Prompt {
			t.Fatalf("launch %d prompt = %q", i, launch.MetaPrompt)
		}
		want := fmt.Sprintf("web/variants/variant-%d.tsx", i+1)
		if len(launch.OwnedScope) != 1 || launch.OwnedScope[0] != want {
			t.Fatalf("launch %d scope = %#v, want %q", i, launch.OwnedScope, want)
		}
	}

	request.AgentType = swarmmode.AgentTypeCoder
	request.OwnedScopeTemplate = ""
	parsed, err = buildSwarmTaskArguments(request, refined)
	if err != nil {
		t.Fatal(err)
	}
	for i, launch := range parsed.Launches {
		if len(launch.OwnedScope) != 1 || launch.OwnedScope[0] != "." {
			t.Fatalf("Coder launch %d scope = %#v", i, launch.OwnedScope)
		}
	}
}

func TestCanonicalTaskLaunchHelperRunsConcurrentlyAndWaitsForEveryWorker(t *testing.T) {
	var started atomic.Int32
	release := make(chan struct{})
	done := make(chan struct{})
	var results []int
	var errs []error
	go func() {
		results, errs = executeTaskLaunchesInParallel(context.Background(), 3, func(_ context.Context, index int) (int, error) {
			started.Add(1)
			<-release
			return index + 1, nil
		})
		close(done)
	}()

	deadline := time.After(time.Second)
	for started.Load() != 3 {
		select {
		case <-deadline:
			t.Fatalf("workers started = %d, want 3 concurrent launches", started.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case <-done:
		t.Fatal("parallel helper returned before workers completed")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("parallel helper did not return after every worker completed")
	}
	if got := fmt.Sprint(results); got != "[1 2 3]" || len(errs) != 3 {
		t.Fatalf("parallel results = %s errors=%d", got, len(errs))
	}
}

func TestEmitSwarmHydrationProgressIsVisibleAndBounded(t *testing.T) {
	var events []StreamEvent
	emitSwarmHydrationProgress(func(event StreamEvent) { events = append(events, event) }, 2, "call-swarm", 1, "running", "Router is hydrating themes")
	if len(events) != 1 || events[0].Type != StreamEventToolDelta || events[0].ToolName != "swarm_mode" {
		t.Fatalf("events = %+v", events)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[0].Output), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["path_id"] != swarmModeHydrationPathID || payload["round"] != float64(1) || payload["rounds"] != float64(2) {
		t.Fatalf("payload = %+v", payload)
	}
}
