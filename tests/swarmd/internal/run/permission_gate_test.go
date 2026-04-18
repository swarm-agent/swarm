package run

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type gateOutcome struct {
	results        []tool.Result
	approvedCalls  []tool.Call
	approvedIdx    []int
	approvedMask   []bool
	feedback       []PermissionFeedback
	err            error
	sessionID      string
	expectedCalls  int
	expectedDenied int
}

func TestPermissionRequirementMatrix(t *testing.T) {
	cases := []struct {
		name            string
		mode            string
		toolName        string
		wantRequirement string
		wantApproval    bool
	}{
		{name: "plan_read", mode: sessionruntime.ModePlan, toolName: "read", wantRequirement: "read", wantApproval: false},
		{name: "plan_list", mode: sessionruntime.ModePlan, toolName: "list", wantRequirement: "list", wantApproval: false},
		{name: "plan_skill_use", mode: sessionruntime.ModePlan, toolName: "skill-use", wantRequirement: "skill_use", wantApproval: false},
		{name: "plan_plan_manage", mode: sessionruntime.ModePlan, toolName: "plan_manage", wantRequirement: "plan_manage", wantApproval: false},
		{name: "plan_task", mode: sessionruntime.ModePlan, toolName: "task", wantRequirement: "task_launch", wantApproval: true},
		{name: "plan_manage_agent", mode: sessionruntime.ModePlan, toolName: "manage-agent", wantRequirement: "manage_agent", wantApproval: false},
		{name: "plan_manage_todos", mode: sessionruntime.ModePlan, toolName: "manage-todos", wantRequirement: "manage_todos", wantApproval: false},
		{name: "plan_manage_theme", mode: sessionruntime.ModePlan, toolName: "manage-theme", wantRequirement: "manage_theme", wantApproval: false},
		{name: "plan_write", mode: sessionruntime.ModePlan, toolName: "write", wantRequirement: "write", wantApproval: false},
		{name: "plan_edit", mode: sessionruntime.ModePlan, toolName: "edit", wantRequirement: "edit", wantApproval: false},
		{name: "plan_bash", mode: sessionruntime.ModePlan, toolName: "bash", wantRequirement: "bash", wantApproval: true},
		{name: "plan_ask_user", mode: sessionruntime.ModePlan, toolName: "ask-user", wantRequirement: "ask_user", wantApproval: true},
		{name: "plan_exit_plan_mode", mode: sessionruntime.ModePlan, toolName: "exit_plan_mode", wantRequirement: "exit_plan_mode", wantApproval: true},
		{name: "auto_write", mode: sessionruntime.ModeAuto, toolName: "write", wantRequirement: "write", wantApproval: false},
		{name: "auto_edit", mode: sessionruntime.ModeAuto, toolName: "edit", wantRequirement: "edit", wantApproval: false},
		{name: "auto_bash", mode: sessionruntime.ModeAuto, toolName: "bash", wantRequirement: "bash", wantApproval: true},
		{name: "auto_task", mode: sessionruntime.ModeAuto, toolName: "task", wantRequirement: "task_launch", wantApproval: true},
		{name: "auto_manage_agent", mode: sessionruntime.ModeAuto, toolName: "manage-agent", wantRequirement: "manage_agent", wantApproval: false},
		{name: "auto_manage_todos", mode: sessionruntime.ModeAuto, toolName: "manage-todos", wantRequirement: "manage_todos", wantApproval: false},
		{name: "auto_manage_theme", mode: sessionruntime.ModeAuto, toolName: "manage-theme", wantRequirement: "manage_theme", wantApproval: false},
		{name: "auto_ask_user", mode: sessionruntime.ModeAuto, toolName: "ask-user", wantRequirement: "ask_user", wantApproval: true},
		{name: "auto_exit_plan_mode", mode: sessionruntime.ModeAuto, toolName: "exit_plan_mode", wantRequirement: "exit_plan_mode", wantApproval: true},
		{name: "auto_custom", mode: sessionruntime.ModeAuto, toolName: "dangerous", wantRequirement: "dangerous", wantApproval: true},
		{name: "yolo_write", mode: sessionruntime.ModeYolo, toolName: "write", wantRequirement: "write", wantApproval: false},
		{name: "yolo_edit", mode: sessionruntime.ModeYolo, toolName: "edit", wantRequirement: "edit", wantApproval: false},
		{name: "yolo_bash", mode: sessionruntime.ModeYolo, toolName: "bash", wantRequirement: "bash", wantApproval: false},
		{name: "yolo_task", mode: sessionruntime.ModeYolo, toolName: "task", wantRequirement: "task_launch", wantApproval: true},
		{name: "yolo_manage_agent", mode: sessionruntime.ModeYolo, toolName: "manage-agent", wantRequirement: "manage_agent", wantApproval: false},
		{name: "yolo_manage_todos", mode: sessionruntime.ModeYolo, toolName: "manage-todos", wantRequirement: "manage_todos", wantApproval: false},
		{name: "yolo_manage_theme", mode: sessionruntime.ModeYolo, toolName: "manage-theme", wantRequirement: "manage_theme", wantApproval: false},
		{name: "yolo_ask_user", mode: sessionruntime.ModeYolo, toolName: "ask-user", wantRequirement: "ask_user", wantApproval: true},
		{name: "yolo_exit_plan_mode", mode: sessionruntime.ModeYolo, toolName: "exit_plan_mode", wantRequirement: "exit_plan_mode", wantApproval: true},
		{name: "yolo_custom", mode: sessionruntime.ModeYolo, toolName: "dangerous", wantRequirement: "dangerous", wantApproval: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			requirement, needsApproval := permissionRequirement(tc.mode, tc.toolName, "")
			if requirement != tc.wantRequirement {
				t.Fatalf("expected requirement %q, got %q", tc.wantRequirement, requirement)
			}
			if needsApproval != tc.wantApproval {
				t.Fatalf("expected needsApproval=%t, got %t", tc.wantApproval, needsApproval)
			}
		})
	}
}

func TestGateToolCallsPlanModeApproveAndDeny(t *testing.T) {
	permSvc, store := openRunPermissionService(t)
	defer func() {
		_ = store.Close()
	}()

	svc := &Service{permissions: permSvc}
	sessionID := "session_plan_gate"
	runID := "run_plan_gate"
	emit := func(StreamEvent) {}
	calls := []tool.Call{
		{CallID: "read_1", Name: "read", Arguments: `{"path":"README.md"}`},
		{CallID: "write_1", Name: "write", Arguments: `{"path":"out.txt","content":"x"}`},
		{CallID: "bash_1", Name: "bash", Arguments: `{"command":"echo test"}`},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan gateOutcome, 1)
	go func() {
		results, approvedCalls, approvedIdx, approvedMask, feedback, err := svc.gateToolCalls(ctx, sessionID, runID, 1, sessionruntime.ModePlan, calls, emit)
		done <- gateOutcome{
			results:       results,
			approvedCalls: approvedCalls,
			approvedIdx:   approvedIdx,
			approvedMask:  approvedMask,
			feedback:      feedback,
			err:           err,
		}
	}()

	pending := waitForPendingCount(t, permSvc, sessionID, 1, 3*time.Second)
	idsByTool := map[string]string{}
	for _, record := range pending {
		idsByTool[record.ToolName] = record.ID
	}
	if idsByTool["bash"] == "" {
		t.Fatalf("expected only bash pending record, got %+v", pending)
	}
	if idsByTool["write"] != "" {
		t.Fatalf("did not expect write pending record, got %+v", pending)
	}

	if _, err := permSvc.Resolve(sessionID, idsByTool["bash"], permission.DecisionDeny, "bash denied"); err != nil {
		t.Fatalf("deny bash: %v", err)
	}

	outcome := <-done
	if outcome.err != nil {
		t.Fatalf("gate tool calls failed: %v", outcome.err)
	}
	if len(outcome.results) != len(calls) {
		t.Fatalf("expected %d results, got %d", len(calls), len(outcome.results))
	}
	if len(outcome.approvedCalls) != 1 {
		t.Fatalf("expected 1 approved call (read), got %d", len(outcome.approvedCalls))
	}
	if len(outcome.approvedMask) != len(calls) {
		t.Fatalf("expected approved mask length %d, got %d", len(calls), len(outcome.approvedMask))
	}
	if !outcome.approvedMask[0] || outcome.approvedMask[1] || outcome.approvedMask[2] {
		t.Fatalf("unexpected approved mask: %+v", outcome.approvedMask)
	}
	if outcome.results[1].Error != "write is unavailable in plan mode" {
		t.Fatalf("expected blocked write error, got %q", outcome.results[1].Error)
	}
	if outcome.results[2].Error != "permission denied" {
		t.Fatalf("expected denied bash error, got %q", outcome.results[2].Error)
	}
	if len(outcome.feedback) != 0 {
		t.Fatalf("expected no permission feedback note, got %d", len(outcome.feedback))
	}

	pendingAfter, err := permSvc.ListPending(sessionID, 10)
	if err != nil {
		t.Fatalf("list pending after decisions: %v", err)
	}
	if len(pendingAfter) != 0 {
		t.Fatalf("expected no pending permissions, got %d", len(pendingAfter))
	}
}

func TestGateToolCallsAutoAndYoloModes(t *testing.T) {
	t.Run("auto", func(t *testing.T) {
		permSvc, store := openRunPermissionService(t)
		defer func() {
			_ = store.Close()
		}()
		svc := &Service{permissions: permSvc}
		emit := func(StreamEvent) {}

		sessionID := "session_auto_gate"
		runID := "run_auto_gate"
		calls := []tool.Call{
			{CallID: "write_1", Name: "write", Arguments: `{"path":"out.txt","content":"x"}`},
			{CallID: "bash_1", Name: "bash", Arguments: `{"command":"echo auto"}`},
			{CallID: "custom_1", Name: "dangerous", Arguments: `{"value":"1"}`},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		done := make(chan gateOutcome, 1)
		go func() {
			results, approvedCalls, approvedIdx, approvedMask, feedback, err := svc.gateToolCalls(ctx, sessionID, runID, 1, sessionruntime.ModeAuto, calls, emit)
			done <- gateOutcome{
				results:       results,
				approvedCalls: approvedCalls,
				approvedIdx:   approvedIdx,
				approvedMask:  approvedMask,
				feedback:      feedback,
				err:           err,
			}
		}()

		pending := waitForPendingCount(t, permSvc, sessionID, 2, 3*time.Second)
		idsByTool := map[string]string{}
		for _, record := range pending {
			idsByTool[record.ToolName] = record.ID
		}
		if idsByTool["bash"] == "" || idsByTool["dangerous"] == "" {
			t.Fatalf("expected bash and dangerous pending records, got %+v", pending)
		}

		if _, err := permSvc.Resolve(sessionID, idsByTool["bash"], permission.DecisionApprove, "bash allowed"); err != nil {
			t.Fatalf("approve bash: %v", err)
		}
		if _, err := permSvc.Resolve(sessionID, idsByTool["dangerous"], permission.DecisionDeny, "custom denied"); err != nil {
			t.Fatalf("deny custom: %v", err)
		}

		outcome := <-done
		if outcome.err != nil {
			t.Fatalf("gate tool calls failed: %v", outcome.err)
		}
		if len(outcome.approvedCalls) != 2 {
			t.Fatalf("expected 2 approved calls, got %d", len(outcome.approvedCalls))
		}
		if !outcome.approvedMask[0] || !outcome.approvedMask[1] || outcome.approvedMask[2] {
			t.Fatalf("unexpected approved mask: %+v", outcome.approvedMask)
		}
		if outcome.results[2].Error != "permission denied" {
			t.Fatalf("expected denied custom tool, got %q", outcome.results[2].Error)
		}
		if len(outcome.feedback) != 1 {
			t.Fatalf("expected 1 permission feedback note, got %d", len(outcome.feedback))
		}
		if outcome.feedback[0].CallID != "bash_1" || outcome.feedback[0].ToolName != "bash" {
			t.Fatalf("unexpected feedback target: %+v", outcome.feedback[0])
		}
		if outcome.feedback[0].Message != "bash allowed" {
			t.Fatalf("expected feedback message %q, got %q", "bash allowed", outcome.feedback[0].Message)
		}
	})

	t.Run("yolo", func(t *testing.T) {
		permSvc, store := openRunPermissionService(t)
		defer func() {
			_ = store.Close()
		}()
		svc := &Service{permissions: permSvc}

		calls := []tool.Call{
			{CallID: "write_1", Name: "write", Arguments: `{"path":"out.txt","content":"x"}`},
			{CallID: "bash_1", Name: "bash", Arguments: `{"command":"echo yolo"}`},
			{CallID: "custom_1", Name: "dangerous", Arguments: `{"value":"1"}`},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		results, approvedCalls, approvedIdx, approvedMask, feedback, err := svc.gateToolCalls(ctx, "session_yolo_gate", "run_yolo_gate", 1, sessionruntime.ModeYolo, calls, func(StreamEvent) {})
		if err != nil {
			t.Fatalf("gate tool calls failed: %v", err)
		}
		if len(results) != len(calls) {
			t.Fatalf("expected %d results, got %d", len(calls), len(results))
		}
		if len(approvedCalls) != len(calls) {
			t.Fatalf("expected all calls approved in yolo, got %d/%d", len(approvedCalls), len(calls))
		}
		if len(approvedIdx) != len(calls) || len(approvedMask) != len(calls) {
			t.Fatalf("expected approved index/mask sizes %d, got %d/%d", len(calls), len(approvedIdx), len(approvedMask))
		}
		for i, ok := range approvedMask {
			if !ok {
				t.Fatalf("expected call %d to be approved in yolo mode", i)
			}
		}
		if len(feedback) != 0 {
			t.Fatalf("expected no permission feedback in yolo mode, got %d", len(feedback))
		}
		pending, err := permSvc.ListPending("session_yolo_gate", 10)
		if err != nil {
			t.Fatalf("list pending: %v", err)
		}
		if len(pending) != 0 {
			t.Fatalf("expected no pending permissions in yolo mode, got %d", len(pending))
		}
	})
}

func TestGateToolCallsTortureMultiSessionResolveAll(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping torture test in short mode")
	}

	permSvc, store := openRunPermissionService(t)
	defer func() {
		_ = store.Close()
	}()

	svc := &Service{permissions: permSvc}
	emit := func(StreamEvent) {}

	const (
		sessionCount   = 20
		callsPerTurn   = 12
		waitTimeout    = 10 * time.Second
		resolveTimeout = 15 * time.Second
	)

	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()

	outcomes := make(chan gateOutcome, sessionCount)
	sessionIDs := make([]string, 0, sessionCount)
	for i := 0; i < sessionCount; i++ {
		sessionID := fmt.Sprintf("session_torture_%02d", i)
		sessionIDs = append(sessionIDs, sessionID)
		runID := fmt.Sprintf("run_torture_%02d", i)
		calls := make([]tool.Call, 0, callsPerTurn)
		for j := 0; j < callsPerTurn; j++ {
			calls = append(calls, tool.Call{
				CallID:    fmt.Sprintf("call_%02d_%02d", i, j),
				Name:      "bash",
				Arguments: `{"command":"echo torture"}`,
			})
		}
		go func(currentSessionID, currentRunID string, currentCalls []tool.Call) {
			results, approvedCalls, approvedIdx, approvedMask, feedback, err := svc.gateToolCalls(ctx, currentSessionID, currentRunID, 1, sessionruntime.ModeAuto, currentCalls, emit)
			outcomes <- gateOutcome{
				results:        results,
				approvedCalls:  approvedCalls,
				approvedIdx:    approvedIdx,
				approvedMask:   approvedMask,
				feedback:       feedback,
				err:            err,
				sessionID:      currentSessionID,
				expectedCalls:  len(currentCalls),
				expectedDenied: 0,
			}
		}(sessionID, runID, calls)
	}

	for _, sessionID := range sessionIDs {
		waitForPendingCount(t, permSvc, sessionID, callsPerTurn, waitTimeout)
	}

	var wg sync.WaitGroup
	resolveErrors := make(chan error, sessionCount)
	for _, sessionID := range sessionIDs {
		wg.Add(1)
		go func(currentSessionID string) {
			defer wg.Done()
			resolved, err := permSvc.ResolveAll(currentSessionID, permission.DecisionApprove, "torture approve", 5000)
			if err != nil {
				resolveErrors <- fmt.Errorf("resolve all for %s: %w", currentSessionID, err)
				return
			}
			if len(resolved) != callsPerTurn {
				resolveErrors <- fmt.Errorf("resolve all for %s returned %d records, expected %d", currentSessionID, len(resolved), callsPerTurn)
			}
		}(sessionID)
	}
	wg.Wait()
	close(resolveErrors)
	for err := range resolveErrors {
		if err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < sessionCount; i++ {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Fatalf("gate tool calls failed for %s: %v", outcome.sessionID, outcome.err)
		}
		if len(outcome.results) != outcome.expectedCalls {
			t.Fatalf("unexpected results count for %s: got %d want %d", outcome.sessionID, len(outcome.results), outcome.expectedCalls)
		}
		if len(outcome.approvedCalls) != outcome.expectedCalls {
			t.Fatalf("expected all calls approved for %s, got %d/%d", outcome.sessionID, len(outcome.approvedCalls), outcome.expectedCalls)
		}
		if len(outcome.approvedIdx) != outcome.expectedCalls || len(outcome.approvedMask) != outcome.expectedCalls {
			t.Fatalf("expected approved index/mask sizes %d for %s, got %d/%d", outcome.expectedCalls, outcome.sessionID, len(outcome.approvedIdx), len(outcome.approvedMask))
		}
		for idx, approved := range outcome.approvedMask {
			if !approved {
				t.Fatalf("expected call %d approved for %s", idx, outcome.sessionID)
			}
		}
		if len(outcome.feedback) != outcome.expectedCalls {
			t.Fatalf("expected %d feedback notes for %s, got %d", outcome.expectedCalls, outcome.sessionID, len(outcome.feedback))
		}
	}

	for _, sessionID := range sessionIDs {
		pending, err := permSvc.ListPending(sessionID, 10)
		if err != nil {
			t.Fatalf("list pending for %s: %v", sessionID, err)
		}
		if len(pending) != 0 {
			t.Fatalf("expected no pending permissions for %s, got %d", sessionID, len(pending))
		}
	}
}

func openRunPermissionService(t *testing.T) (*permission.Service, *pebblestore.Store) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "run-perm-test.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	permStore := pebblestore.NewPermissionStore(store)
	return permission.NewService(permStore, eventLog, nil), store
}

func waitForPendingCount(t *testing.T, svc *permission.Service, sessionID string, expected int, timeout time.Duration) []pebblestore.PermissionRecord {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		pending, err := svc.ListPending(sessionID, 5000)
		if err == nil && len(pending) == expected {
			return pending
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("waiting for pending count %d in %s failed: %v", expected, sessionID, err)
			}
			t.Fatalf("waiting for pending count %d in %s timed out, got %d", expected, sessionID, len(pending))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPermissionArgumentsForTaskUseStructuredLaunchManifest(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "task-launch-manifest.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	parentSession, _, err := sessionSvc.CreateSession("parent", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}

	svc := &Service{sessions: sessionSvc, agents: agentSvc}
	raw := svc.permissionArgumentsForCall(parentSession.ID, sessionruntime.ModePlan, tool.Call{
		CallID:    "task_manifest_1",
		Name:      "task",
		Arguments: `{"description":"Inspect repo","prompt":"Find the key files and summarize.","allow_bash":true,"report_max_chars":2400,"launches":[{"subagent_type":"explorer","meta_prompt":"map repository structure"},{"subagent_type":"memory","meta_prompt":"extract concise findings"}]}`,
	})

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode manifest payload: %v", err)
	}
	if got := strings.TrimSpace(mapString(payload, "path_id")); got != taskLaunchPermissionPathID {
		t.Fatalf("path_id = %q, want %q", got, taskLaunchPermissionPathID)
	}
	if got := strings.TrimSpace(mapString(payload, "goal")); got != "Inspect repo" {
		t.Fatalf("goal = %q, want Inspect repo", got)
	}
	if got := strings.TrimSpace(mapString(payload, "description")); got != "Inspect repo" {
		t.Fatalf("description = %q, want Inspect repo", got)
	}
	if got := strings.TrimSpace(mapString(payload, "subagent_type")); got != "explorer" {
		t.Fatalf("subagent_type = %q, want explorer", got)
	}
	if got := strings.TrimSpace(mapString(payload, "resolved_agent_name")); got != "explorer" {
		t.Fatalf("resolved_agent_name = %q, want explorer", got)
	}
	if got := strings.TrimSpace(mapString(payload, "parent_mode")); got != sessionruntime.ModePlan {
		t.Fatalf("parent_mode = %q, want %q", got, sessionruntime.ModePlan)
	}
	if got := strings.TrimSpace(mapString(payload, "effective_child_mode")); got != sessionruntime.ModeAuto {
		t.Fatalf("effective_child_mode = %q, want %q", got, sessionruntime.ModeAuto)
	}
	if got := strings.TrimSpace(mapString(payload, "prompt")); got != "Find the key files and summarize." {
		t.Fatalf("prompt = %q", got)
	}
	if got := mapInt(payload, "launch_count"); got != 2 {
		t.Fatalf("launch_count = %d, want 2", got)
	}
	if !mapBool(payload, "allow_bash") {
		t.Fatalf("expected allow_bash=true in manifest")
	}
	if got := strings.TrimSpace(mapString(payload, "effective_prompt")); got != "" {
		t.Fatalf("effective_prompt should be omitted, got %q", got)
	}
	launches, ok := payload["launches"].([]any)
	if !ok || len(launches) != 2 {
		t.Fatalf("expected two launch entries, got %#v", payload["launches"])
	}
	launch0, ok := launches[0].(map[string]any)
	if !ok {
		t.Fatalf("launch[0] entry type = %T", launches[0])
	}
	if got := strings.TrimSpace(mapString(launch0, "requested_subagent_type")); got != "explorer" {
		t.Fatalf("launch[0] requested_subagent_type = %q", got)
	}
	if got := strings.TrimSpace(mapString(launch0, "meta_prompt")); got != "map repository structure" {
		t.Fatalf("launch[0] meta_prompt = %q", got)
	}
	if got := strings.TrimSpace(mapString(launch0, "raw_prompt")); got != "" {
		t.Fatalf("launch[0] raw_prompt should be omitted, got %q", got)
	}
	if got := strings.TrimSpace(mapString(launch0, "effective_prompt")); got != "" {
		t.Fatalf("launch[0] effective_prompt should be omitted, got %q", got)
	}
	if got := strings.TrimSpace(mapString(launch0, "target_workspace_path")); got != parentSession.WorkspacePath {
		t.Fatalf("launch[0] target_workspace_path = %q, want %q", got, parentSession.WorkspacePath)
	}
	launch1, ok := launches[1].(map[string]any)
	if !ok {
		t.Fatalf("launch[1] entry type = %T", launches[1])
	}
	if got := strings.TrimSpace(mapString(launch1, "requested_subagent_type")); got != "memory" {
		t.Fatalf("launch[1] requested_subagent_type = %q", got)
	}
	if got := strings.TrimSpace(mapString(launch1, "meta_prompt")); got != "extract concise findings" {
		t.Fatalf("launch[1] meta_prompt = %q", got)
	}
	parent, ok := payload["parent"].(map[string]any)
	if !ok {
		t.Fatalf("expected parent block in manifest")
	}
	if got := strings.TrimSpace(mapString(parent, "session_id")); got != parentSession.ID {
		t.Fatalf("parent session_id = %q, want %q", got, parentSession.ID)
	}
	if got := strings.TrimSpace(mapString(parent, "workspace_path")); got != parentSession.WorkspacePath {
		t.Fatalf("parent workspace_path = %q, want %q", got, parentSession.WorkspacePath)
	}
}

func TestPermissionRequirement_BypassPermissionsSuffix(t *testing.T) {
	cases := []struct {
		toolName        string
		arguments       string
		wantRequirement string
		wantApproval    bool
	}{
		{toolName: "bash", wantRequirement: "bash", wantApproval: false},
		{toolName: "task", wantRequirement: "task_launch", wantApproval: true},
		{toolName: "manage-agent", arguments: `{"action":"create"}`, wantRequirement: "agent_change", wantApproval: true},
		{toolName: "manage-agent", arguments: `{"action":"inspect"}`, wantRequirement: "manage_agent", wantApproval: false},
		{toolName: "manage-agent", arguments: `{"action":"activate_primary"}`, wantRequirement: "manage_agent", wantApproval: false},
		{toolName: "manage-todos", wantRequirement: "manage_todos", wantApproval: false},
		{toolName: "manage-theme", wantRequirement: "manage_theme", wantApproval: false},
		{toolName: "dangerous", wantRequirement: "dangerous", wantApproval: false},
		{toolName: "exit_plan_mode", wantRequirement: "exit_plan_mode", wantApproval: true},
	}

	for _, tc := range cases {
		requirement, needsApproval := permissionRequirement("auto+bypass_permissions", tc.toolName, tc.arguments)
		if requirement != tc.wantRequirement {
			t.Fatalf("tool %s: requirement = %q, want %q", tc.toolName, requirement, tc.wantRequirement)
		}
		if needsApproval != tc.wantApproval {
			t.Fatalf("tool %s: needsApproval = %t, want %t", tc.toolName, needsApproval, tc.wantApproval)
		}
	}
}

func TestTaskAlwaysRequiresApprovalUnderBypassPermissions(t *testing.T) {
	permSvc, store := openRunPermissionService(t)
	defer func() {
		_ = store.Close()
	}()
	permSvc.SetBypassPermissions(true)

	result, err := permSvc.AuthorizeToolCall(permission.AuthorizationInput{
		SessionID:     "session_task_bypass",
		RunID:         "run_task_bypass",
		CallID:        "task_bypass_call",
		ToolName:      "task",
		ToolArguments: `{"description":"Inspect repo","prompt":"Map the codebase.","launches":[{"subagent_type":"explorer","meta_prompt":"map repository structure"}]}`,
		Mode:          sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize task with bypass: %v", err)
	}
	if result.Requirement != "task_launch" {
		t.Fatalf("requirement = %q, want %q", result.Requirement, "task_launch")
	}
	if result.Decision != permission.AuthorizationPending {
		t.Fatalf("decision = %q, want %q", result.Decision, permission.AuthorizationPending)
	}

	pending, err := permSvc.ListPending("session_task_bypass", 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending permissions = %d, want 1", len(pending))
	}
	if pending[0].Requirement != "task_launch" {
		t.Fatalf("pending requirement = %q, want %q", pending[0].Requirement, "task_launch")
	}
}

func TestManageThemeBypassPermissionsSkipsApproval(t *testing.T) {
	permSvc, store := openRunPermissionService(t)
	defer func() {
		_ = store.Close()
	}()
	permSvc.SetBypassPermissions(true)

	result, err := permSvc.AuthorizeToolCall(permission.AuthorizationInput{
		SessionID:     "session_manage_theme_bypass",
		RunID:         "run_manage_theme_bypass",
		CallID:        "manage_theme_bypass_call",
		ToolName:      "manage-theme",
		ToolArguments: `{"action":"set","theme_id":"midnight"}`,
		Mode:          sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize manage-theme with bypass: %v", err)
	}
	if result.Requirement != "manage_theme" {
		t.Fatalf("requirement = %q, want %q", result.Requirement, "manage_theme")
	}
	if result.Decision != permission.AuthorizationApprove {
		t.Fatalf("decision = %q, want %q", result.Decision, permission.AuthorizationApprove)
	}

	pending, err := permSvc.ListPending("session_manage_theme_bypass", 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending permissions = %d, want 0", len(pending))
	}
}

func TestManageAgentAlwaysRequiresApprovalUnderBypassPermissions(t *testing.T) {
	permSvc, store := openRunPermissionService(t)
	defer func() {
		_ = store.Close()
	}()
	permSvc.SetBypassPermissions(true)

	result, err := permSvc.AuthorizeToolCall(permission.AuthorizationInput{
		SessionID:     "session_manage_agent_bypass",
		RunID:         "run_manage_agent_bypass",
		CallID:        "manage_agent_bypass_call",
		ToolName:      "manage-agent",
		ToolArguments: `{"action":"create","name":"demo-agent","content":{"name":"demo-agent","mode":"subagent","description":"Demo","prompt":"Help","execution_setting":"read"}}`,
		Mode:          sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize manage-agent with bypass: %v", err)
	}
	if result.Requirement != "agent_change" {
		t.Fatalf("requirement = %q, want %q", result.Requirement, "agent_change")
	}
	if result.Decision != permission.AuthorizationPending {
		t.Fatalf("decision = %q, want %q", result.Decision, permission.AuthorizationPending)
	}

	pending, err := permSvc.ListPending("session_manage_agent_bypass", 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending permissions = %d, want 1", len(pending))
	}
	if pending[0].Requirement != "agent_change" {
		t.Fatalf("pending requirement = %q, want %q", pending[0].Requirement, "agent_change")
	}
}

func TestManageAgentInspectBypassPermissionsSkipsApproval(t *testing.T) {
	permSvc, store := openRunPermissionService(t)
	defer func() {
		_ = store.Close()
	}()
	permSvc.SetBypassPermissions(true)

	result, err := permSvc.AuthorizeToolCall(permission.AuthorizationInput{
		SessionID:     "session_manage_agent_inspect_bypass",
		RunID:         "run_manage_agent_inspect_bypass",
		CallID:        "manage_agent_inspect_bypass_call",
		ToolName:      "manage-agent",
		ToolArguments: `{"action":"inspect"}`,
		Mode:          sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize manage-agent inspect with bypass: %v", err)
	}
	if result.Requirement != "manage_agent" {
		t.Fatalf("requirement = %q, want %q", result.Requirement, "manage_agent")
	}
	if result.Decision != permission.AuthorizationApprove {
		t.Fatalf("decision = %q, want %q", result.Decision, permission.AuthorizationApprove)
	}

	pending, err := permSvc.ListPending("session_manage_agent_inspect_bypass", 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending permissions = %d, want 0", len(pending))
	}
}
