package taskrouter

import (
	"context"
	"errors"
	"reflect"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: RouteTask must make zero Router calls for direct images, including
// naming, regardless of workspace count or single-image enhancement flags.
// Threat: incidental naming, workspace selection or large-count escalation
// allocates an agent instead of sending the original prompt to the image model.
// This service boundary is the narrowest layer proving invoker non-use.
func TestImageRoutingDirectNoRouter(t *testing.T) {
	for _, count := range []int{1, 2, 5, 25} {
		for _, enhance := range []bool{false, true} {
			if count > 1 && enhance {
				continue
			}
			calls := 0
			svc := NewService(func(context.Context, string, string) (string, error) {
				calls++
				return "", errors.New("must not invoke Router")
			})
			prompt := "  A café at sunrise\nwith blue chairs  "
			res, err := svc.RouteTask(context.Background(), TaskRouteOptions{Prompt: prompt, Intent: "image", VariantCount: count, EnhancePrompt: enhance, Project: &pebblestore.ProjectRecord{Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: "/repo/one"}, {Path: "/repo/two"}}}})
			if err != nil || calls != 0 || res.Agent != "image" || res.Tier != "direct" || res.Branch != "" || len(res.WorkspacesInvolved) != 0 || res.Mission != prompt || res.EnhancePrompt {
				t.Fatalf("direct contract: %+v calls=%d err=%v", res, calls, err)
			}
			if len(res.ImagePrompts) != count || len(res.Deliverables) != count {
				t.Fatalf("missing output mapping: %+v", res)
			}
			for _, got := range res.ImagePrompts {
				if got != prompt {
					t.Fatalf("prompt changed: %q", got)
				}
			}
		}
	}
}

// Purpose: only explicit multi-image opt-in may invoke Router; reject malformed,
// empty, duplicate and mismatched output mappings without returning partial work.
// RouteTask owns this contract; a deterministic invoker tests its response boundary.
func TestImageRoutingOptInValidation(t *testing.T) {
	for _, raw := range []string{`{"image_prompts":["red café","blue café"]}`, `{"image_prompts":["only one"]}`, `{"image_prompts":["ok"," "]}`, `{"image_prompts":["same","same"]}`, `{"image_prompts":["a","b","c"]}`, `not JSON`} {
		calls := 0
		svc := NewService(func(context.Context, string, string) (string, error) {
			calls++
			return raw, nil
		})
		res, err := svc.RouteTask(context.Background(), TaskRouteOptions{Prompt: "café", Agent: "image", VariantCount: 2, EnhancePrompt: true})
		if calls != 1 {
			t.Fatalf("expected one variant call, got %d", calls)
		}
		if raw == `{"image_prompts":["red café","blue café"]}` {
			if err != nil || !res.EnhancePrompt || !reflect.DeepEqual(res.ImagePrompts, []string{"red café", "blue café"}) || res.Agent != "image" {
				t.Fatalf("variant mapping: %+v %v", res, err)
			}
		} else if err == nil || len(res.ImagePrompts) != 0 {
			t.Fatalf("partial invalid result: %+v %v", res, err)
		}
	}
	if _, err := NewService().RouteTask(context.Background(), TaskRouteOptions{Prompt: "café", Agent: "image", VariantCount: 2, EnhancePrompt: true}); err == nil {
		t.Fatal("opt-in silently fell back without Router")
	}
}

// Purpose: image workspace exemption must follow strict agent/intent validation,
// and must never exempt a code contract from ambiguity rejection.
func TestImageRoutingDoesNotExemptSourceExecution(t *testing.T) {
	project := &pebblestore.ProjectRecord{Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: "/repo/one"}, {Path: "/repo/two"}}}
	for _, opts := range []TaskRouteOptions{{Agent: "coder", Intent: "image"}, {Agent: "coder", Intent: "code"}, {Agent: "swarm", Intent: "image", OutcomeType: "code_pr"}, {Agent: "image", Tier: "swarm"}, {Agent: "image", VariantCount: 26}} {
		opts.Prompt, opts.Project = "create image", project
		if _, err := NewService().RouteTask(context.Background(), opts); err == nil {
			t.Fatalf("invalid source/image contract accepted: %+v", opts)
		}
	}
}
