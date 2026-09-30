package taskrouter

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Purpose: NameTask and RouteTask must reject malformed naming rather than
// persist a prompt prefix; the service is the narrowest layer observing Router
// output validation, Unicode handling, and unchanged direct-media contracts.
func TestManualTaskNamingValidation(t *testing.T) {
	prompt := "ok can you fix the sidebar jumping when I resize the window and keep the existing keyboard shortcuts intact please"
	for name, raw := range map[string]string{
		"empty": `{"title":" "}`, "malformed": `not json`,
		"multiline": `{"title":"Fix sidebar\njumps"}`,
		"overlong": `{"title":"` + strings.Repeat("界", 81) + `"}`,
		"filler": `{"title":"Can you fix the sidebar"}`,
		"echo": `{"title":"ok can you fix the sidebar jumping"}`,
		"missing": `{}`, "wrong type": `{"title":123}`,
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			s := NewService(func(context.Context, string, string) (string, error) { calls++; return raw, nil })
			if title, err := s.NameTask(context.Background(), prompt, ""); err == nil || title != "" || calls != 1 {
				t.Fatalf("title=%q err=%v calls=%d", title, err, calls)
			}
			if strings.HasPrefix(raw, "{") {
				full := strings.TrimSuffix(raw, "}") + `,"agent":"coder"}`
				router := NewService(func(context.Context, string, string) (string, error) { return full, nil })
				if _, err := router.RouteTask(context.Background(), TaskRouteOptions{Prompt: prompt, Agent: "coder"}); err == nil {
					t.Fatal("full Router accepted invalid title")
				}
			}
		})
	}
	for _, title := range []string{"Fix sidebar layout jumps", "修复侧栏布局跳动", strings.Repeat("界", 80)} {
		s := NewService(func(_ context.Context, _, input string) (string, error) {
			var payload map[string]string
			if err := json.Unmarshal([]byte(input), &payload); err != nil || payload["prompt"] != prompt {
				t.Fatalf("original instructions changed: %s %v", input, err)
			}
			return `{"title":"` + title + `"}`, nil
		})
		if got, err := s.NameTask(context.Background(), prompt, ""); err != nil || got != title {
			t.Fatalf("title=%q err=%v want=%q", got, err, title)
		}
	}
	failure := errors.New("Router unavailable")
	s := NewService(func(context.Context, string, string) (string, error) { return "", failure })
	if _, err := s.NameTask(context.Background(), prompt, ""); !errors.Is(err, failure) { t.Fatal(err) }
	if got, err := s.NameTask(context.Background(), prompt, "My custom title"); err != nil || got != "My custom title" { t.Fatalf("custom: %q %v", got, err) }
	if _, err := NewService().NameTask(context.Background(), prompt, ""); err == nil { t.Fatal("missing Router silently accepted") }
}

// Purpose: naming direct media must make exactly one invocation and change only
// Title, never enhance prompts or accept model-generated routing/settings.
func TestManualTaskNamingDirectMediaPreservesContract(t *testing.T) {
	for _, opts := range []TaskRouteOptions{
		{Prompt: "Please generate a gentle rain soundtrack for concentration", Intent: "sound"},
		{Prompt: "Please generate a continuous shot of a misty forest at sunrise", Intent: "video", VideoType: "single"},
	} {
		base, err := NewService().RouteTask(context.Background(), opts)
		if err != nil { t.Fatal(err) }
		calls := 0
		s := NewService(func(context.Context, string, string) (string, error) {
			calls++
			return `{"title":"Create calming nature media","agent":"coder","mission":"rewritten","scenes":[{}],"soundtrack":"changed"}`, nil
		})
		got, err := s.RouteTask(context.Background(), opts)
		if err != nil || calls != 1 { t.Fatalf("calls=%d err=%v", calls, err) }
		base.Title = "Create calming nature media"
		if !reflect.DeepEqual(got, base) { t.Fatalf("naming changed contract: %+v want %+v", got, base) }
	}
}

// Purpose: normal Router elaboration shares title validation while explicit
// user titles stay authoritative, even when generated title output is empty.
func TestManualTaskNamingFullRouter(t *testing.T) {
	for _, custom := range []string{"", "My chosen task title"} {
		calls := 0
		s := NewService(func(context.Context, string, string) (string, error) {
			calls++
			return `{"title":"Fix sidebar layout jumps","agent":"image","mission":"summary"}`, nil
		})
		opts := TaskRouteOptions{Title: custom, Prompt: "ok please fix the sidebar layout jumping on resize while preserving keyboard shortcuts", Agent: "coder"}
		got, err := s.RouteTask(context.Background(), opts)
		if err != nil || calls != 1 { t.Fatalf("%v calls=%d", err, calls) }
		want := "Fix sidebar layout jumps"
		if custom != "" { want = custom }
		if got.Title != want || got.Agent != "coder" || got.OutcomeType != "code_pr" { t.Fatalf("%+v", got) }
	}
}
