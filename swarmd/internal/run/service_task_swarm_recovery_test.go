package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: configuredTaskSwarmRouter.Hydrate and hydrateTaskSwarmLaunches must
// recover malformed model JSON once without changing the brief or admitting a
// partial wave. Fake streaming providers prove the orchestration gate directly,
// without model credentials, child processes, rendering, or ambient session state.
type swarmRecoveryRunner struct {
	requests  []provideriface.Request
	deadlines []time.Time
	respond   func(context.Context, int, func(provideriface.StreamEvent)) (provideriface.Response, error)
}

func (r *swarmRecoveryRunner) ID() string { return "test" }
func (r *swarmRecoveryRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *swarmRecoveryRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, emit func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.requests = append(r.requests, req)
	deadline, _ := ctx.Deadline()
	r.deadlines = append(r.deadlines, deadline)
	return r.respond(ctx, len(r.requests), emit)
}

func swarmRecoveryFixture(t *testing.T) (taskCallArguments, taskSwarmHydrationRequest, string) {
	t.Helper()
	// Quotes, backslashes, Unicode, and a long multi-line passage must not be
	// shortened to make Router JSON easier to generate.
	brief := "Keep all eight narration passages exactly.\n" + strings.Repeat("Narration: \"Build together\" — path\\shape.\n", 100)
	encoded, _ := json.Marshal(map[string]any{
		"mode": "swarm", "agent_type": "designer", "count": 5,
		"prompt": brief, "description": "Narration motion studies",
		"themes":              []string{"quiet", "spatial", "type", "crystal", "grid"},
		"output_contract":     "Five complete animations, not storyboards",
		"output_requirements": map[string]any{"preset": "landscape_video"},
		"animation_profile":   map[string]any{"profile": "motion_ui"},
		"iteration_controls":  map[string]any{"change": []string{"motion treatment"}, "preserve": []string{"exact narration"}, "exclude": []string{"new claims"}},
	})
	parsed, err := parseTaskCallArguments(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request, err := buildTaskSwarmHydrationRequest(parsed, parsed.Launches)
	if err != nil {
		t.Fatal(err)
	}
	result := taskSwarmHydrationResult{GroupTitle: "Narration Motion Studies"}
	for i := 1; i <= 5; i++ {
		result.Deltas = append(result.Deltas, taskSwarmHydratedDelta{Index: i, Title: fmt.Sprintf("Motion %d", i), Theme: fmt.Sprintf("specialization %d", i), Role: fmt.Sprintf("Execute approach %d", i), Constraints: []string{}, Deliverable: fmt.Sprintf("Complete animation %d", i)})
	}
	data, _ := json.Marshal(result)
	return parsed, request, string(data)
}

func TestTaskSwarmRouterRegeneratesMalformedJSONBeforeWholeWaveAdmission(t *testing.T) {
	parsed, request, valid := swarmRecoveryFixture(t)
	malformed := `{"group_title"t:"do not forward this failed output"}`
	if _, err := decodeTaskSwarmHydrationResultForRequest(malformed, request); err == nil || !strings.Contains(err.Error(), "invalid character 't' after object key") {
		t.Fatalf("regression fixture: %v", err)
	}
	runner := &swarmRecoveryRunner{respond: func(_ context.Context, n int, emit func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if n == 1 {
			return provideriface.Response{Text: malformed}, nil
		}
		// Stream-only response exercises the same bounded decoder path.
		emit(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: valid[:50]})
		emit(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: valid[50:]})
		return provideriface.Response{}, nil
	}}
	router := &configuredTaskSwarmRouter{runner: runner, parentID: "test-parent", callID: "test-call"}
	launches, err := hydrateTaskSwarmLaunches(context.Background(), router, pebblestore.SessionSnapshot{ID: "test-parent"}, parsed, request, parsed.Launches, 1, "test-call", nil)
	if err != nil || len(launches) != 5 {
		t.Fatalf("whole wave: %d, %v", len(launches), err)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("attempts = %d", len(runner.requests))
	}
	for i, req := range runner.requests {
		if req.ToolChoice != "none" || len(req.Tools) != 0 || req.ToolInvoker != nil || req.NativeContinuationAllowed || !req.ForceFreshProviderContext {
			t.Fatal("Router gained tools or continuation")
		}
		if !reflect.DeepEqual(req.Input, runner.requests[0].Input) {
			t.Fatal("retry changed the authoritative request")
		}
		if strings.Contains(req.Instructions, "do not forward this failed output") {
			t.Fatal("untrusted response entered regeneration instructions")
		}
		if runner.deadlines[i].IsZero() || !runner.deadlines[i].Equal(runner.deadlines[0]) {
			t.Fatal("retry reset the shared deadline")
		}
	}
	if runner.requests[0].ProviderLineageID == runner.requests[1].ProviderLineageID || runner.requests[0].ProviderCacheKey == runner.requests[1].ProviderCacheKey || runner.requests[0].SessionAffinityKey == runner.requests[1].SessionAffinityKey {
		t.Fatal("regeneration reused failed provider lineage")
	}
	for i, launch := range launches {
		if !strings.Contains(launch.MetaPrompt, request.Prompt) || !strings.Contains(launch.MetaPrompt, request.OutputContract) {
			t.Fatal("brief or output contract shortened")
		}
		for _, text := range []string{request.Items[i].Theme, "exact narration", "motion treatment", "new claims", "artifact_v3_author", "build_preview", "finish_turn exactly once"} {
			if !strings.Contains(launch.MetaPrompt, text) {
				t.Fatalf("worker %d lost %q", i+1, text)
			}
		}
		if launch.SourceArguments["swarm_theme"] != request.Items[i].Theme || launch.StreamKey != fmt.Sprintf("swarm:%d", i+1) {
			t.Fatalf("worker identity/theme drift: %#v", launch)
		}
		if !reflect.DeepEqual(launch.OutputRequirements, request.OutputRequirements) || !reflect.DeepEqual(launch.AnimationProfile, request.AnimationProfile) {
			t.Fatal("output contract drift")
		}
		if strings.Contains(launch.MetaPrompt, "call artifact_v2_author") || strings.Contains(launch.MetaPrompt, "output mode: managed Artifact V2") {
			t.Fatal("native Designer received retired authoring instructions")
		}
	}
}

// Purpose: persistent malformed/semantic output must exhaust exactly two calls
// with zero admission and no mutation of the caller's pending launch specs.
func TestTaskSwarmRouterPersistentInvalidOutputLeavesWaveUntouched(t *testing.T) {
	_, _, valid := swarmRecoveryFixture(t)
	cases := map[string]string{
		"syntax":          `{"group_title"t:1}`,
		"empty":           "",
		"unknown":         strings.Replace(valid, `"deltas":`, `"unknown":true,"deltas":`, 1),
		"trailing":        valid + `{}`,
		"missing workers": `{"group_title":"Narration Motion Studies","deltas":[]}`,
		"duplicate index": strings.Replace(valid, `"index":2`, `"index":1`, 1),
		"incomplete":      strings.Replace(valid, `"role":"Execute approach 1"`, `"role":""`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			parsed, request, _ := swarmRecoveryFixture(t)
			before, _ := json.Marshal(parsed.Launches)
			runner := &swarmRecoveryRunner{respond: func(context.Context, int, func(provideriface.StreamEvent)) (provideriface.Response, error) {
				return provideriface.Response{Text: raw}, nil
			}}
			launches, err := hydrateTaskSwarmLaunches(context.Background(), &configuredTaskSwarmRouter{runner: runner}, pebblestore.SessionSnapshot{}, parsed, request, parsed.Launches, 1, "test-call", nil)
			after, _ := json.Marshal(parsed.Launches)
			if err == nil || !strings.Contains(err.Error(), "after 2 validated attempts") || launches != nil || len(runner.requests) != 2 || string(before) != string(after) {
				t.Fatalf("invalid wave escaped: calls=%d err=%v changed=%v", len(runner.requests), err, string(before) != string(after))
			}
		})
	}
}

// Purpose: the regeneration allowance is only for bounded output validation,
// not transport/security/resource failures; even late success after cancellation
// cannot admit workers. Test both streamed and final-only output caps.
func TestTaskSwarmRouterDoesNotRetryTerminalFailures(t *testing.T) {
	_, request, valid := swarmRecoveryFixture(t)
	for _, kind := range []string{"provider", "cancelled", "stream limit", "final limit", "stream tool", "final tool"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := &swarmRecoveryRunner{respond: func(_ context.Context, _ int, emit func(provideriface.StreamEvent)) (provideriface.Response, error) {
				switch kind {
				case "provider":
					return provideriface.Response{}, errors.New("provider unavailable")
				case "cancelled":
					cancel()
				case "stream limit":
					emit(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: strings.Repeat("x", taskSwarmRouterMaxOutputRunes+1)})
				case "final limit":
					return provideriface.Response{Text: strings.Repeat("界", taskSwarmRouterMaxOutputRunes+1)}, nil
				case "stream tool":
					emit(provideriface.StreamEvent{Type: provideriface.StreamEventToolCallStarted, ToolName: "forbidden"})
				case "final tool":
					return provideriface.Response{Text: valid, FunctionCalls: []provideriface.FunctionCall{{Name: "forbidden"}}}, nil
				}
				return provideriface.Response{Text: valid}, nil
			}}
			result, err := (&configuredTaskSwarmRouter{runner: runner}).Hydrate(ctx, request)
			if err == nil || len(result.Deltas) != 0 || len(runner.requests) != 1 {
				t.Fatalf("terminal failure retried/admitted: %v %d", err, len(runner.requests))
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &swarmRecoveryRunner{}
	if _, err := (&configuredTaskSwarmRouter{runner: runner}).Hydrate(ctx, request); !errors.Is(err, context.Canceled) || len(runner.requests) != 0 {
		t.Fatalf("cancelled request started: %v", err)
	}
}

// Purpose: valid first output must retain one-call cost; V3 source identity must
// survive into the generated worker contract without conversion to legacy IDs.
func TestTaskSwarmRouterValidFirstAttemptPreservesNativeSource(t *testing.T) {
	_, request, valid := swarmRecoveryFixture(t)
	request.ArtifactV3Source = &taskArtifactV3Source{SessionID: "test-source", ArtifactID: "test-artifact", CommitOID: strings.Repeat("a", 40), ProjectionSeq: 7, TargetPartIDs: []string{"narration-opening"}}
	runner := &swarmRecoveryRunner{respond: func(context.Context, int, func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return provideriface.Response{Text: valid}, nil
	}}
	result, err := (&configuredTaskSwarmRouter{runner: runner}).Hydrate(context.Background(), request)
	if err != nil || len(runner.requests) != 1 {
		t.Fatalf("first attempt: %v", err)
	}
	request.SectionTargets = []*taskSwarmSectionTarget{{ID: "narration-opening", Label: "Opening", Kind: "semantic"}}
	prompt, err := composeTaskSwarmChildPrompt(request, request.Items[0], result.Deltas[0])
	encoded, _ := json.Marshal(request.ArtifactV3Source)
	if err != nil || !strings.Contains(prompt, string(encoded)) || !strings.Contains(prompt, "managed native Artifact V3") || strings.Contains(prompt, "use artifact_v2_author") {
		t.Fatalf("native source contract: %v", err)
	}
}

// Purpose: even a post-hydration composition failure must not publish earlier
// assignments; count mismatch must reject before any provider call.
func TestTaskSwarmRouterCompositionFailureIsAtomic(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		parsed, request, valid := swarmRecoveryFixture(t)
		before, _ := json.Marshal(parsed.Launches)
		if mismatch {
			request.Items = request.Items[:4]
		} else {
			request.Items[4].Index = 99
		}
		runner := &swarmRecoveryRunner{respond: func(context.Context, int, func(provideriface.StreamEvent)) (provideriface.Response, error) {
			return provideriface.Response{Text: valid}, nil
		}}
		launches, err := hydrateTaskSwarmLaunches(context.Background(), &configuredTaskSwarmRouter{runner: runner}, pebblestore.SessionSnapshot{}, parsed, request, parsed.Launches, 1, "test-call", nil)
		after, _ := json.Marshal(parsed.Launches)
		if err == nil || launches != nil || string(before) != string(after) {
			t.Fatal("partial composition escaped")
		}
		wantCalls := 1
		if mismatch {
			wantCalls = 0
		}
		if len(runner.requests) != wantCalls {
			t.Fatalf("provider calls = %d, want %d", len(runner.requests), wantCalls)
		}
	}
}
