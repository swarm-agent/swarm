package run

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestRunTurnStoresAssistantTextBeforeToolAsSeparateTimelineMessage(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "assistant-timeline.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "Assistant timeline",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Mode:          sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider: "fake",
			Model:    "fake-model",
			Thinking: "off",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	providers := registry.New()
	runner := &timelineProviderRunner{}
	providers.RegisterRunner(runner)
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	svc := NewService(sessions, modelSvc, providers, tool.NewRuntime(1), nil, nil, nil, eventLog)

	var storedEvents []StreamEvent
	result, err := svc.RunTurnStreaming(context.Background(), session.ID, RunRequest{Prompt: "do it"}, RunStartMeta{}, func(event StreamEvent) {
		if event.Type == StreamEventMessageStored && event.Message != nil {
			storedEvents = append(storedEvents, event)
		}
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if result.ToolCallCount != 1 {
		t.Fatalf("tool call count = %d, want 1", result.ToolCallCount)
	}

	messages, err := sessions.ListMessages(session.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	roles := make([]string, 0, len(messages))
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
		contents = append(contents, message.Content)
	}
	wantRoles := []string{"user", "assistant", "tool", "assistant"}
	if strings.Join(roles, ",") != strings.Join(wantRoles, ",") {
		t.Fatalf("stored roles = %v, want %v; contents = %v", roles, wantRoles, contents)
	}
	if messages[1].Content != "preface before tool" {
		t.Fatalf("pre-tool assistant content = %q", messages[1].Content)
	}
	if !strings.Contains(messages[2].Content, "tool ok") {
		t.Fatalf("tool content = %q, want tool result", messages[2].Content)
	}
	if messages[3].Content != "final after tool" {
		t.Fatalf("final assistant content = %q", messages[3].Content)
	}

	storedRoles := make([]string, 0, len(storedEvents))
	for _, event := range storedEvents {
		storedRoles = append(storedRoles, event.Message.Role)
	}
	if strings.Join(storedRoles, ",") != strings.Join(wantRoles, ",") {
		t.Fatalf("stored event roles = %v, want %v", storedRoles, wantRoles)
	}

	if got := runner.calls; got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
	if len(runner.inputs) != 2 {
		t.Fatalf("captured provider input count = %d, want 2", len(runner.inputs))
	}
	secondInput := runner.inputs[1]
	if len(secondInput) < 4 {
		t.Fatalf("second provider input length = %d, want at least 4: %#v", len(secondInput), secondInput)
	}
	wantInputKinds := []string{"user", "assistant", "function_call", "function_call_output"}
	if got := inputKinds(secondInput[:4]); !reflect.DeepEqual(got, wantInputKinds) {
		t.Fatalf("second provider input kinds = %v, want %v; input = %#v", got, wantInputKinds, secondInput)
	}
	if got := inputText(secondInput[1]); got != "preface before tool" {
		t.Fatalf("second provider assistant input text = %q, want preface before tool; input = %#v", got, secondInput[1])
	}
}

type timelineProviderRunner struct {
	calls  int
	inputs [][]map[string]any
}

func (r *timelineProviderRunner) ID() string { return "fake" }

func (r *timelineProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *timelineProviderRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	r.inputs = append(r.inputs, cloneProviderInput(req.Input))
	if r.calls == 1 {
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "preface before tool"})
		}
		return provideriface.Response{
			Text: "preface before tool",
			FunctionCalls: []provideriface.FunctionCall{{
				CallID:    "call_1",
				Name:      "bash",
				Arguments: `{"command":"printf tool ok"}`,
			}},
		}, nil
	}
	if onEvent != nil {
		onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "final after tool"})
	}
	return provideriface.Response{Text: "final after tool"}, nil
}

func cloneProviderInput(input []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(input))
	for _, item := range input {
		clone := make(map[string]any, len(item))
		for key, value := range item {
			clone[key] = value
		}
		out = append(out, clone)
	}
	return out
}

func inputKinds(input []map[string]any) []string {
	kinds := make([]string, 0, len(input))
	for _, item := range input {
		if role, ok := item["role"].(string); ok && strings.TrimSpace(role) != "" {
			kinds = append(kinds, strings.TrimSpace(role))
			continue
		}
		if itemType, ok := item["type"].(string); ok {
			kinds = append(kinds, strings.TrimSpace(itemType))
			continue
		}
		kinds = append(kinds, "")
	}
	return kinds
}

func inputText(item map[string]any) string {
	contents, ok := item["content"].([]map[string]any)
	if !ok || len(contents) == 0 {
		return ""
	}
	text, _ := contents[0]["text"].(string)
	return strings.TrimSpace(text)
}

func TestRunTurnStoresRepeatedAssistantToolSegmentsInTimelineOrder(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "assistant-multistep-timeline.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "Assistant multistep timeline",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Mode:          sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider: "fake-multistep",
			Model:    "fake-model",
			Thinking: "off",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	providers := registry.New()
	runner := &multiStepTimelineProviderRunner{}
	providers.RegisterRunner(runner)
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	svc := NewService(sessions, modelSvc, providers, tool.NewRuntime(1), nil, nil, nil, eventLog)

	result, err := svc.RunTurnStreaming(context.Background(), session.ID, RunRequest{Prompt: "do it twice"}, RunStartMeta{}, func(StreamEvent) {})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if result.ToolCallCount != 2 {
		t.Fatalf("tool call count = %d, want 2", result.ToolCallCount)
	}

	messages, err := sessions.ListMessages(session.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	roles := make([]string, 0, len(messages))
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
		contents = append(contents, message.Content)
	}
	wantRoles := []string{"user", "assistant", "tool", "assistant", "tool", "assistant"}
	if strings.Join(roles, ",") != strings.Join(wantRoles, ",") {
		t.Fatalf("stored roles = %v, want %v; contents = %v", roles, wantRoles, contents)
	}
	if messages[1].Content != "preface before first tool" {
		t.Fatalf("first assistant content = %q", messages[1].Content)
	}
	if messages[3].Content != "bridge before second tool" {
		t.Fatalf("second assistant content = %q", messages[3].Content)
	}
	if messages[5].Content != "final after second tool" {
		t.Fatalf("final assistant content = %q", messages[5].Content)
	}

	if got := runner.calls; got != 3 {
		t.Fatalf("provider calls = %d, want 3", got)
	}
	if len(runner.inputs) != 3 {
		t.Fatalf("captured provider input count = %d, want 3", len(runner.inputs))
	}
	if got, want := inputKinds(runner.inputs[1]), []string{"user", "assistant", "function_call", "function_call_output"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second provider input kinds = %v, want %v; input = %#v", got, want, runner.inputs[1])
	}
	wantThirdInputKinds := []string{"user", "assistant", "function_call", "function_call_output", "assistant", "function_call", "function_call_output"}
	if got := inputKinds(runner.inputs[2]); !reflect.DeepEqual(got, wantThirdInputKinds) {
		t.Fatalf("third provider input kinds = %v, want %v; input = %#v", got, wantThirdInputKinds, runner.inputs[2])
	}
	if got := inputText(runner.inputs[2][1]); got != "preface before first tool" {
		t.Fatalf("third provider first assistant input text = %q", got)
	}
	if got := inputText(runner.inputs[2][4]); got != "bridge before second tool" {
		t.Fatalf("third provider second assistant input text = %q", got)
	}
}

type multiStepTimelineProviderRunner struct {
	calls  int
	inputs [][]map[string]any
}

func (r *multiStepTimelineProviderRunner) ID() string { return "fake-multistep" }

func (r *multiStepTimelineProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *multiStepTimelineProviderRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	r.inputs = append(r.inputs, cloneProviderInput(req.Input))
	switch r.calls {
	case 1:
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "preface before first tool"})
		}
		return provideriface.Response{
			Text: "preface before first tool",
			FunctionCalls: []provideriface.FunctionCall{{
				CallID:    "call_1",
				Name:      "bash",
				Arguments: `{"command":"printf tool one"}`,
			}},
		}, nil
	case 2:
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "bridge before second tool"})
		}
		return provideriface.Response{
			Text: "bridge before second tool",
			FunctionCalls: []provideriface.FunctionCall{{
				CallID:    "call_2",
				Name:      "bash",
				Arguments: `{"command":"printf tool two"}`,
			}},
		}, nil
	default:
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "final after second tool"})
		}
		return provideriface.Response{Text: "final after second tool"}, nil
	}
}
