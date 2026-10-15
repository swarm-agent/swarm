package api

import (
	"context"

	"strings"
	"sync"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"

	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

type scopedToolProbeRunner struct {
	id             string
	allowedCommand string

	mu       sync.Mutex
	requests []provideriface.Request
}

func (r *scopedToolProbeRunner) ID() string {
	return r.id
}

func (r *scopedToolProbeRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *scopedToolProbeRunner) CreateResponseStreaming(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()

	if hasFunctionCallOutput(req.Input) {
		return provideriface.Response{Text: "tool-output:" + lastFunctionCallOutput(req.Input)}, nil
	}
	if requestContainsTool(req, "bash") {
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{
			CallID:    "bash_1",
			Name:      "bash",
			Arguments: `{"command":"` + r.allowedCommand + `","timeout_ms":1000}`,
		}}}, nil
	}
	return provideriface.Response{Text: "bash-unavailable-from-tools"}, nil
}

func (r *scopedToolProbeRunner) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = nil
}

type fakeAgentAPISwarmService struct {
	state swarmruntime.LocalState
	token string
}

func (f fakeAgentAPISwarmService) EnsureLocalState(input swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error) {
	return f.state, nil
}
func (f fakeAgentAPISwarmService) RenameLocalSwarm(input swarmruntime.RenameLocalSwarmInput) (swarmruntime.LocalState, error) {
	state := f.state
	state.Node.Name = strings.TrimSpace(input.Name)
	return state, nil
}
func (f fakeAgentAPISwarmService) ListGroupsForSwarm(string, int) ([]swarmruntime.GroupState, string, error) {
	return nil, "", nil
}
func (f fakeAgentAPISwarmService) UpsertGroup(input swarmruntime.UpsertGroupInput) (swarmruntime.Group, error) {
	return swarmruntime.Group{}, nil
}
func (f fakeAgentAPISwarmService) DeleteGroup(string) error { return nil }
func (f fakeAgentAPISwarmService) SetCurrentGroup(string, string) (swarmruntime.GroupState, error) {
	return swarmruntime.GroupState{}, nil
}

func (f fakeAgentAPISwarmService) UpsertGroupMember(input swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error) {
	return swarmruntime.GroupMember{}, nil
}
func (f fakeAgentAPISwarmService) RemoveGroupMember(input swarmruntime.RemoveGroupMemberInput) error {
	return nil
}

func requestContainsTool(req provideriface.Request, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, definition := range req.Tools {
		if strings.EqualFold(strings.TrimSpace(definition.Name), want) {
			return true
		}
	}
	return false
}

func hasFunctionCallOutput(input []map[string]any) bool {
	for _, item := range input {
		if strings.EqualFold(strings.TrimSpace(stringMapValue(item, "type")), "function_call_output") {
			return true
		}
	}
	return false
}

func lastFunctionCallOutput(input []map[string]any) string {
	for i := len(input) - 1; i >= 0; i-- {
		if !strings.EqualFold(strings.TrimSpace(stringMapValue(input[i], "type")), "function_call_output") {
			continue
		}
		return strings.TrimSpace(stringMapValue(input[i], "output"))
	}
	return ""
}

func stringMapValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return value
}
