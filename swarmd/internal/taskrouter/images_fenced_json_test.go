package taskrouter

import (
	"context"
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: RouteTask/routeImages must accept bare JSON or exactly one complete
// JSON/unlabelled Markdown fence, retaining requested slot order and prompt bytes.
// Threat: Router formatting causes a backtick parse failure before generation;
// loose extraction instead accepts prose, incomplete fences or trailing payloads.
// This deterministic service-boundary test is the narrowest layer proving both
// normalization and ValidateImagePrompts/distinctness without any image provider.
func TestImageRoutingFencedJSON(t *testing.T) {
	valid := `{"image_prompts":["red café","blue café"]}`
	fenced := "```json\n" + valid + "\n```"
	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{name: "bare", raw: valid, want: []string{"red café", "blue café"}},
		{name: "bare whitespace", raw: " \r\n" + valid + "\r\n\t", want: []string{"red café", "blue café"}},
		{name: "JSON fence", raw: fenced, want: []string{"red café", "blue café"}},
		{name: "unlabelled fence", raw: "```\n" + valid + "\n```", want: []string{"red café", "blue café"}},
		{name: "CRLF and whitespace", raw: " \r\n\t```json \r\n  " + valid + " \r\n\t``` \r\n", want: []string{"red café", "blue café"}},
		{name: "prompt bytes preserved", raw: "```json\n{\"image_prompts\":[\"  red café  \",\"blue ``` café\"]}\n```", want: []string{"  red café  ", "blue ``` café"}},
		{name: "malformed JSON", raw: "```json\n{\"image_prompts\":[}\n```"},
		{name: "incomplete JSON", raw: "```json\n{\"image_prompts\":[\"red\",\"blue\"]\n```"},
		{name: "missing closing fence", raw: "```json\n" + valid},
		{name: "missing opening newline", raw: "```json " + valid + "```"},
		{name: "inline closing fence", raw: "```json\n" + valid + "```"},
		{name: "wrong fence label", raw: "```javascript\n" + valid + "\n```"},
		{name: "extra fence marker", raw: "````json\n" + valid + "\n````"},
		{name: "leading prose", raw: "Here are the variants:\n" + fenced},
		{name: "trailing prose", raw: fenced + "\nEnjoy!"},
		{name: "inline trailing prose", raw: fenced + " Enjoy!"},
		{name: "trailing JSON", raw: fenced + "\n{}"},
		{name: "bare trailing JSON", raw: valid + " {}"},
		{name: "payload inside fence", raw: "```json\n" + valid + "\n{}\n```"},
		{name: "multiple fences", raw: fenced + "\n" + fenced},
		{name: "empty fence", raw: "```json\n\n```"},
		{name: "too few prompts", raw: "```json\n{\"image_prompts\":[\"red\"]}\n```"},
		{name: "too many prompts", raw: "```json\n{\"image_prompts\":[\"red\",\"blue\",\"green\"]}\n```"},
		{name: "empty prompt", raw: "```json\n{\"image_prompts\":[\"red\",\" \\t\"]}\n```"},
		{name: "duplicate prompts", raw: "```json\n{\"image_prompts\":[\"red\",\"red\"]}\n```"},
		{name: "trimmed duplicate prompts", raw: "```json\n{\"image_prompts\":[\" red\",\"red \"]}\n```"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			svc := NewService(func(context.Context, string, string) (string, error) {
				calls++
				return tc.raw, nil
			})
			res, err := svc.RouteTask(context.Background(), TaskRouteOptions{Prompt: "café", Agent: "image", VariantCount: 2, EnhancePrompt: true})
			if calls != 1 {
				t.Fatalf("expected exactly one Router call, got %d", calls)
			}
			if tc.want == nil {
				if err == nil || !reflect.DeepEqual(res, pebblestore.TaskRouteResult{}) {
					t.Fatalf("invalid response returned partial/fallback work: %+v err=%v", res, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(res.ImagePrompts, tc.want) || res.VariantCount != 2 || len(res.Deliverables) != 2 || !res.EnhancePrompt || res.Agent != "image" || res.Mission != "café" {
				t.Fatalf("variant contract: %+v err=%v", res, err)
			}
			for i, suffix := range []string{"(Image 1)", "(Image 2)"} {
				if !strings.HasSuffix(res.Deliverables[i].Title, suffix) {
					t.Fatalf("slot order changed: %+v", res.Deliverables)
				}
			}
		})
	}
}
