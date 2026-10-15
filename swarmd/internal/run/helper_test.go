package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"sync"
	"testing"
	"time"
)

// Purpose: ResolveAgentToolContract must reject forged helper tool grants,
// including recursive delegation and delivery, at the runtime authority boundary.
// This layer proves the executable contract, not provider-backed fan-out.
func TestHelperToolBoundary(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	p := agentruntime.HelperAgentProfileForParent(pebblestore.AgentProfile{})
	for _, name := range []string{"write", "edit", "git_commit", "deliver", "manage_sessions", "manage_artifact", "task"} {
		p.ToolContract.Tools[name] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(true)}
	}
	resolved, _, _, err := svc.ResolveAgentToolContract(p)
	if err != nil {
		t.Fatal(err)
	}
	for name, state := range resolved.Tools {
		allowed := name == "read" || name == "search" || name == "find" || name == "list" || name == "bash"
		if state.Enabled != allowed {
			t.Fatalf("%s enabled=%v", name, state.Enabled)
		}
	}
	coder, _, _, err := svc.ResolveAgentToolContract(agentruntime.WorkerCoderAgentProfileForParent(pebblestore.AgentProfile{}))
	if err != nil || !coder.Tools["task"].Enabled {
		t.Fatalf("coder task: %+v %v", coder.Tools["task"], err)
	}
}

// Purpose: preparation must retain the exact job checkout and attenuate a
// trusted Finder profile. Real durable child creation proves the session
// boundary, without pretending to run a provider-backed conversation.
func TestHelperChildWorktree(t *testing.T) {
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, ok, err := svc.sessions.GetSession(id)
	if err != nil || !ok {
		t.Fatal(err)
	}
	parent.Metadata = map[string]any{}
	parent.Metadata["agent_profile"] = agentruntime.WorkerCoderAgentProfileForParent(pebblestore.AgentProfile{})
	parent.WorktreeEnabled = true
	parent.WorktreeRootPath = parent.WorkspacePath
	profile := agentruntime.FinderAgentProfileForParent(pebblestore.AgentProfile{})
	child, err := svc.prepareDelegatedSubagentLaunchWithProfile(parent, "auto", taskLaunchPrepared{LogicalTaskID: "helper-test", RequestedSubagent: "finder", MetaPrompt: "inspect", LaunchIndex: 1}, "inspect", "", &profile, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if child.SubagentProfile.Name != agentruntime.HelperAgentID || child.ChildSession.WorkspacePath != parent.WorktreeRootPath {
		t.Fatalf("wrong child: %+v", child.ChildSession)
	}
	helper := child.ChildSession
	helper.Metadata["agent_profile"] = child.SubagentProfile
	if err := validateHelperTask(helper, taskCallArguments{}); err == nil {
		t.Fatal("recursive helper accepted")
	}
}

// helperBarrierRunner adds a concurrency barrier to the handoff recording
// provider. No response can finish until all three requests are in flight.
type helperBarrierRunner struct {
	taskDesignerRecordingRunner
	mu      sync.Mutex
	arrived int
	ready   chan struct{}
}

func (r *helperBarrierRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *helperBarrierRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, event func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.mu.Lock()
	r.arrived++
	n := r.arrived
	r.requests = append(r.requests, req)
	if n == 3 {
		close(r.ready)
	}
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return provideriface.Response{}, ctx.Err()
	case <-r.ready:
	}
	text := fmt.Sprintf("helper-result-%d", n)
	if event != nil {
		event(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: text})
	}
	return provideriface.Response{Text: text}, nil
}

// Purpose: executeTaskToolWithParsed must fan out three real durable helper
// sessions concurrently and return every report to their Coder parent. A
// barrier, rather than timing or sleeps, makes serialized execution fail. The
// recording provider isolates orchestration; this is not a live-model test.
func TestHelperParallelResults(t *testing.T) {
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	if _, _, err := svc.sessions.UpdateMetadata(id, map[string]any{"agent_profile": agentruntime.WorkerCoderAgentProfileForParent(pebblestore.AgentProfile{})}); err != nil {
		t.Fatal(err)
	}
	parent, ok, err := svc.sessions.GetSession(id)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := svc.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{ClientRequestID: "helper-parent", PayloadHash: "helper-parent", SessionID: id, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, Kind: sessionruntime.SessionMutationCreateSession, Session: &parent}); err != nil {
		t.Fatal(err)
	}
	runner := &helperBarrierRunner{ready: make(chan struct{})}
	providers := registry.New()
	providers.RegisterRunner(runner)
	svc.providers = providers
	launches := []any{}
	for i := 1; i <= 3; i++ {
		launches = append(launches, map[string]any{"subagent_type": "finder", "meta_prompt": fmt.Sprintf("Inspect question %d", i)})
	}
	parsed, err := parseTaskCallArguments(mustJSON(t, map[string]any{"prompt": "Inspect three independent questions", "launches": launches}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, SessionID: id, AccountScopeSource: identity.AccountScopeSourceSession}
	output, err := svc.executeTaskToolWithParsed(ctx, id, sessionruntime.ModeAuto, 1, tool.Call{CallID: "parallel-helpers", Name: "task"}, nil, taskExecutionRequest{Parsed: parsed, ParsedProvided: true, Principal: principal})
	if err != nil {
		t.Fatalf("parallel task: %v; %s", err, output)
	}
	for i := 1; i <= 3; i++ {
		if !strings.Contains(output, fmt.Sprintf("helper-result-%d", i)) {
			t.Fatalf("missing result %d: %s", i, output)
		}
	}
	var result struct {
		Launches []struct {
			ChildSessionID string `json:"child_session_id"`
		}
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Launches) != 3 {
		t.Fatalf("expected three children: %s", output)
	}
	seen := map[string]bool{}
	for _, launch := range result.Launches {
		child, ok, err := svc.sessions.GetSession(launch.ChildSessionID)
		if err != nil || !ok || seen[child.ID] {
			t.Fatalf("invalid child: %+v %v", launch, err)
		}
		seen[child.ID] = true
		profile, err := sessionV3AgentProfileFromMetadataMap(child.Metadata)
		if err != nil || profile.Name != agentruntime.HelperAgentID || child.WorkspacePath != parent.WorkspacePath {
			t.Fatalf("wrong helper: %+v %v", child, err)
		}
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.arrived != 3 {
		t.Fatalf("requests=%d", runner.arrived)
	}
}
