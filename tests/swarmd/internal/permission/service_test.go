package permission

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestDefaultPolicyIncludesDangerousBashDeleteDenyRules(t *testing.T) {
	policy := DefaultPolicy()
	if len(policy.Rules) < 2 {
		t.Fatalf("expected default policy deny rules, got %d", len(policy.Rules))
	}
	patterns := make(map[string]PolicyRule, len(policy.Rules))
	for _, rule := range policy.Rules {
		patterns[rule.Pattern] = rule
	}
	for _, pattern := range []string{"rm -rf /", "rm -rf /*"} {
		rule, ok := patterns[pattern]
		if !ok {
			t.Fatalf("expected default rule for %q", pattern)
		}
		if rule.Kind != PolicyRuleKindPhrase || rule.Decision != PolicyDecisionDeny || rule.Tool != "bash" {
			t.Fatalf("unexpected rule for %q: %+v", pattern, rule)
		}
	}
}

func TestAuthorizeToolCallBlocksDangerousRecursiveDeletesInYoloMode(t *testing.T) {
	store, eventLog, permissionStore := openPermissionTestStore(t)
	defer func() {
		_ = store.Close()
	}()

	svc := NewService(permissionStore, eventLog, nil)
	cases := []struct {
		name    string
		command string
	}{
		{name: "root", command: "rm -rf /"},
		{name: "root_glob", command: "rm -rf /*"},
		{name: "workspace_dot", command: "rm -rf ."},
		{name: "workspace_glob", command: "rm -rf ./*"},
		{name: "parent", command: "rm -rf .."},
		{name: "home", command: "rm -rf ~"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := svc.AuthorizeToolCall(AuthorizationInput{
				SessionID:     "session_bash_block_" + tc.name,
				RunID:         "run_bash_block_" + tc.name,
				CallID:        "call_bash_block_" + tc.name,
				ToolName:      "bash",
				ToolArguments: `{"command":"` + tc.command + `"}`,
				Mode:          "yolo",
			})
			if err != nil {
				t.Fatalf("authorize dangerous bash %s: %v", tc.name, err)
			}
			if result.Decision != AuthorizationDeny {
				t.Fatalf("decision = %q, want %q", result.Decision, AuthorizationDeny)
			}
			if !strings.Contains(result.Reason, "dangerous recursive delete target is blocked") {
				t.Fatalf("expected dangerous delete reason, got %q", result.Reason)
			}
			if result.Record != nil {
				t.Fatalf("record present = true, want false")
			}
		})
	}
}

func TestPolicyRuleFromToolCallUsesBashExecutablePrefix(t *testing.T) {
	rule, ok := policyRuleFromToolCall("bash", `{"command":"sudo env DEBUG=1 uname -a --kernel-name --kernel-release"}`, PolicyDecisionAllow)
	if !ok {
		t.Fatalf("expected bash rule preview")
	}
	if rule.Kind != PolicyRuleKindBashPrefix {
		t.Fatalf("rule kind = %q, want %q", rule.Kind, PolicyRuleKindBashPrefix)
	}
	if rule.Pattern != "uname" {
		t.Fatalf("rule pattern = %q, want %q", rule.Pattern, "uname")
	}
	if preview := previewPolicyRule(rule); preview != "allow bash prefix: uname" {
		t.Fatalf("preview = %q, want %q", preview, "allow bash prefix: uname")
	}
}

func TestShouldApproveManageAgentMutation(t *testing.T) {
	cases := []struct {
		name      string
		arguments string
		want      bool
	}{
		{name: "empty_defaults_to_inspect", arguments: ``, want: false},
		{name: "inspect", arguments: `{"action":"inspect"}`, want: false},
		{name: "list", arguments: `{"action":"list"}`, want: false},
		{name: "get", arguments: `{"action":"get","agent":"demo"}`, want: false},
		{name: "read", arguments: `{"action":"read","agent":"demo"}`, want: false},
		{name: "activate_primary", arguments: `{"action":"activate_primary","agent":"demo"}`, want: false},
		{name: "set_active_subagent", arguments: `{"action":"set_active_subagent","purpose":"explorer","agent":"demo"}`, want: false},
		{name: "remove_active_subagent", arguments: `{"action":"remove_active_subagent","purpose":"explorer"}`, want: false},
		{name: "create", arguments: `{"action":"create","name":"demo"}`, want: true},
		{name: "update", arguments: `{"action":"update","agent":"demo"}`, want: true},
		{name: "delete", arguments: `{"action":"delete","agent":"demo"}`, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldApproveManageAgentMutation(tc.arguments); got != tc.want {
				t.Fatalf("ShouldApproveManageAgentMutation() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestAuthorizeToolCallManageAgentByAction(t *testing.T) {
	store, eventLog, permissionStore := openPermissionTestStore(t)
	defer func() {
		_ = store.Close()
	}()

	svc := NewService(permissionStore, eventLog, nil)
	cases := []struct {
		name            string
		arguments       string
		wantRequirement string
		wantDecision    AuthorizationDecision
		wantRecord      bool
	}{
		{name: "inspect", arguments: `{"action":"inspect"}`, wantRequirement: "manage_agent", wantDecision: AuthorizationApprove, wantRecord: false},
		{name: "list", arguments: `{"action":"list"}`, wantRequirement: "manage_agent", wantDecision: AuthorizationApprove, wantRecord: false},
		{name: "get", arguments: `{"action":"get","agent":"demo"}`, wantRequirement: "manage_agent", wantDecision: AuthorizationApprove, wantRecord: false},
		{name: "read", arguments: `{"action":"read","agent":"demo"}`, wantRequirement: "manage_agent", wantDecision: AuthorizationApprove, wantRecord: false},
		{name: "activate_primary", arguments: `{"action":"activate_primary","agent":"demo"}`, wantRequirement: "manage_agent", wantDecision: AuthorizationApprove, wantRecord: false},
		{name: "set_active_subagent", arguments: `{"action":"set_active_subagent","purpose":"explorer","agent":"demo"}`, wantRequirement: "manage_agent", wantDecision: AuthorizationApprove, wantRecord: false},
		{name: "remove_active_subagent", arguments: `{"action":"remove_active_subagent","purpose":"explorer"}`, wantRequirement: "manage_agent", wantDecision: AuthorizationApprove, wantRecord: false},
		{name: "create", arguments: `{"action":"create","agent":"demo"}`, wantRequirement: "agent_change", wantDecision: AuthorizationPending, wantRecord: true},
		{name: "update", arguments: `{"action":"update","agent":"demo"}`, wantRequirement: "agent_change", wantDecision: AuthorizationPending, wantRecord: true},
		{name: "delete", arguments: `{"action":"delete","agent":"demo"}`, wantRequirement: "agent_change", wantDecision: AuthorizationPending, wantRecord: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := svc.AuthorizeToolCall(AuthorizationInput{
				SessionID:     "session_manage_agent_requirement",
				RunID:         "run_" + tc.name,
				CallID:        "call_" + tc.name,
				ToolName:      "manage-agent",
				ToolArguments: tc.arguments,
				Mode:          "auto",
			})
			if err != nil {
				t.Fatalf("authorize manage-agent %s: %v", tc.name, err)
			}
			if result.Requirement != tc.wantRequirement {
				t.Fatalf("requirement = %q, want %q", result.Requirement, tc.wantRequirement)
			}
			if result.Decision != tc.wantDecision {
				t.Fatalf("decision = %q, want %q", result.Decision, tc.wantDecision)
			}
			if got := result.Record != nil; got != tc.wantRecord {
				t.Fatalf("record present = %t, want %t", got, tc.wantRecord)
			}
			if tc.wantRecord && result.Record.Requirement != tc.wantRequirement {
				t.Fatalf("record requirement = %q, want %q", result.Record.Requirement, tc.wantRequirement)
			}
		})
	}
}

func TestAuthorizeToolCallManageTodosAllowed(t *testing.T) {
	store, eventLog, permissionStore := openPermissionTestStore(t)
	defer func() {
		_ = store.Close()
	}()

	svc := NewService(permissionStore, eventLog, nil)
	result, err := svc.AuthorizeToolCall(AuthorizationInput{
		SessionID:     "session_manage_todos_allowed",
		RunID:         "run_manage_todos_allowed",
		CallID:        "call_manage_todos_allowed",
		ToolName:      "manage-todos",
		ToolArguments: `{"action":"create","text":"demo todo"}`,
		Mode:          "auto",
	})
	if err != nil {
		t.Fatalf("authorize manage-todos: %v", err)
	}
	if result.Requirement != "manage_todos" {
		t.Fatalf("requirement = %q, want %q", result.Requirement, "manage_todos")
	}
	if result.Decision != AuthorizationApprove {
		t.Fatalf("decision = %q, want %q", result.Decision, AuthorizationApprove)
	}
	if result.Record != nil {
		t.Fatalf("record present = true, want false")
	}

	pending, err := svc.ListPending("session_manage_todos_allowed", 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending permissions = %d, want 0", len(pending))
	}
}

func TestServiceWaitAndResolveAll(t *testing.T) {
	store, eventLog, permissionStore := openPermissionTestStore(t)
	defer func() {
		_ = store.Close()
	}()

	svc := NewService(permissionStore, eventLog, nil)
	sessionID := "session_wait"
	runID := "run_wait"

	created := make([]pebblestore.PermissionRecord, 0, 4)
	for i := 0; i < 4; i++ {
		record, err := svc.CreatePending(CreateInput{
			SessionID:     sessionID,
			RunID:         runID,
			CallID:        "call_" + string(rune('a'+i)),
			ToolName:      "bash",
			ToolArguments: `{"command":"echo test"}`,
			Requirement:   "bash",
			Mode:          "auto",
		})
		if err != nil {
			t.Fatalf("create pending %d: %v", i, err)
		}
		created = append(created, record)
	}

	results := make(chan pebblestore.PermissionRecord, len(created))
	errs := make(chan error, len(created))
	var wg sync.WaitGroup
	for _, record := range created {
		record := record
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			resolved, err := svc.WaitForResolution(ctx, sessionID, record.ID)
			if err != nil {
				errs <- err
				return
			}
			results <- resolved
		}()
	}

	resolved, err := svc.ResolveAll(sessionID, "approve", "bulk approve", 1000)
	if err != nil {
		t.Fatalf("resolve all: %v", err)
	}
	if len(resolved) != len(created) {
		t.Fatalf("expected %d resolved records, got %d", len(created), len(resolved))
	}

	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("wait failed: %v", err)
		}
	}
	countResolved := 0
	for result := range results {
		countResolved++
		if result.Status != pebblestore.PermissionStatusApproved {
			t.Fatalf("expected approved status, got %s", result.Status)
		}
	}
	if countResolved != len(created) {
		t.Fatalf("expected %d waiter results, got %d", len(created), countResolved)
	}

	pending, err := svc.ListPending(sessionID, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending permissions, got %d", len(pending))
	}
}

func TestServiceMultiSessionIsolation(t *testing.T) {
	store, eventLog, permissionStore := openPermissionTestStore(t)
	defer func() {
		_ = store.Close()
	}()

	svc := NewService(permissionStore, eventLog, nil)
	sessionA := "session_alpha"
	sessionB := "session_beta"

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			_, _ = svc.CreatePending(CreateInput{
				SessionID:     sessionA,
				RunID:         "run_a",
				CallID:        "call_a",
				ToolName:      "bash",
				ToolArguments: `{"command":"echo a"}`,
				Requirement:   "bash",
				Mode:          "auto",
			})
		}(i)
		go func(idx int) {
			defer wg.Done()
			_, _ = svc.CreatePending(CreateInput{
				SessionID:     sessionB,
				RunID:         "run_b",
				CallID:        "call_b",
				ToolName:      "write",
				ToolArguments: `{"path":"x","content":"y"}`,
				Requirement:   "write",
				Mode:          "plan",
			})
		}(i)
	}
	wg.Wait()

	if _, err := svc.ResolveAll(sessionA, "approve", "", 1000); err != nil {
		t.Fatalf("resolve all session A: %v", err)
	}

	pendingA, err := svc.ListPending(sessionA, 1000)
	if err != nil {
		t.Fatalf("list pending session A: %v", err)
	}
	pendingB, err := svc.ListPending(sessionB, 1000)
	if err != nil {
		t.Fatalf("list pending session B: %v", err)
	}
	if len(pendingA) != 0 {
		t.Fatalf("expected no pending permissions for session A, got %d", len(pendingA))
	}
	if len(pendingB) != 20 {
		t.Fatalf("expected 20 pending permissions for session B, got %d", len(pendingB))
	}
}

func TestServiceCancelRunPending(t *testing.T) {
	store, eventLog, permissionStore := openPermissionTestStore(t)
	defer func() {
		_ = store.Close()
	}()

	svc := NewService(permissionStore, eventLog, nil)
	sessionID := "session_cancel"
	runA := "run_a"
	runB := "run_b"

	runAIDs := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		record, err := svc.CreatePending(CreateInput{
			SessionID:     sessionID,
			RunID:         runA,
			CallID:        "call_a",
			ToolName:      "bash",
			ToolArguments: `{"command":"echo a"}`,
			Requirement:   "bash",
			Mode:          "auto",
		})
		if err != nil {
			t.Fatalf("create runA pending: %v", err)
		}
		runAIDs = append(runAIDs, record.ID)
	}
	for i := 0; i < 2; i++ {
		if _, err := svc.CreatePending(CreateInput{
			SessionID:     sessionID,
			RunID:         runB,
			CallID:        "call_b",
			ToolName:      "bash",
			ToolArguments: `{"command":"echo b"}`,
			Requirement:   "bash",
			Mode:          "auto",
		}); err != nil {
			t.Fatalf("create runB pending: %v", err)
		}
	}

	cancelled, err := svc.CancelRunPending(sessionID, runA, "run cancelled")
	if err != nil {
		t.Fatalf("cancel run pending: %v", err)
	}
	if len(cancelled) != 3 {
		t.Fatalf("expected 3 cancelled permissions, got %d", len(cancelled))
	}

	for _, id := range runAIDs {
		record, ok, err := permissionStore.GetPermission(sessionID, id)
		if err != nil {
			t.Fatalf("get permission %s: %v", id, err)
		}
		if !ok {
			t.Fatalf("permission %s not found", id)
		}
		if record.Status != pebblestore.PermissionStatusCancelled {
			t.Fatalf("expected cancelled status for %s, got %s", id, record.Status)
		}
	}

	if _, ok, err := permissionStore.GetRunWait(sessionID, runA); err != nil {
		t.Fatalf("get run wait state: %v", err)
	} else if ok {
		t.Fatalf("expected run wait state to be removed for %s", runA)
	}

	pending, err := svc.ListPending(sessionID, 100)
	if err != nil {
		t.Fatalf("list pending after cancel: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending permissions from runB, got %d", len(pending))
	}
}

func TestServiceReconcilePendingRuns(t *testing.T) {
	store, eventLog, permissionStore := openPermissionTestStore(t)
	defer func() {
		_ = store.Close()
	}()

	svc := NewService(permissionStore, eventLog, nil)
	sessionID := "session_reconcile"
	runA := "run_a"
	runB := "run_b"

	for _, runID := range []string{runA, runB} {
		if _, err := svc.CreatePending(CreateInput{
			SessionID:     sessionID,
			RunID:         runID,
			CallID:        "call_" + runID,
			ToolName:      "bash",
			ToolArguments: `{"command":"echo stale"}`,
			Requirement:   "bash",
			Mode:          "auto",
		}); err != nil {
			t.Fatalf("create pending for %s: %v", runID, err)
		}
	}

	if err := svc.ReconcilePendingRuns("daemon restarted"); err != nil {
		t.Fatalf("reconcile pending runs: %v", err)
	}

	pending, err := svc.ListPending(sessionID, 100)
	if err != nil {
		t.Fatalf("list pending after reconcile: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending permissions after reconcile, got %d", len(pending))
	}

	for _, runID := range []string{runA, runB} {
		if _, ok, err := permissionStore.GetRunWait(sessionID, runID); err != nil {
			t.Fatalf("get run wait %s: %v", runID, err)
		} else if ok {
			t.Fatalf("expected run wait %s to be removed", runID)
		}
		records, err := permissionStore.ListRunPermissions(sessionID, runID, 10)
		if err != nil {
			t.Fatalf("list run permissions %s: %v", runID, err)
		}
		if len(records) != 1 {
			t.Fatalf("expected 1 run permission for %s, got %d", runID, len(records))
		}
		if records[0].Status != pebblestore.PermissionStatusCancelled {
			t.Fatalf("expected reconciled status cancelled for %s, got %q", runID, records[0].Status)
		}
		if records[0].Reason != "daemon restarted" {
			t.Fatalf("expected reconcile reason for %s, got %q", runID, records[0].Reason)
		}
	}
}

func TestClassifyPermissionReason(t *testing.T) {
	cases := []struct {
		name      string
		reason    string
		wantKind  string
		wantChars int
	}{
		{name: "empty", reason: "", wantKind: "empty", wantChars: 0},
		{name: "default_approved", reason: "approved by user", wantKind: "default_approved", wantChars: len("approved by user")},
		{name: "default_denied", reason: "deny", wantKind: "default_denied", wantChars: len("deny")},
		{name: "default_cancelled", reason: "canceled", wantKind: "default_cancelled", wantChars: len("canceled")},
		{name: "custom", reason: "run only in docs dir", wantKind: "custom", wantChars: len("run only in docs dir")},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			kind, chars := classifyPermissionReason(tc.reason)
			if kind != tc.wantKind {
				t.Fatalf("expected kind %q, got %q", tc.wantKind, kind)
			}
			if chars != tc.wantChars {
				t.Fatalf("expected chars %d, got %d", tc.wantChars, chars)
			}
		})
	}
}

func openPermissionTestStore(t *testing.T) (*pebblestore.Store, *pebblestore.EventLog, *pebblestore.PermissionStore) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "perm-test.pebble")
	store, err := pebblestore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	return store, eventLog, pebblestore.NewPermissionStore(store)
}

func TestPermissionStoreSanitizesSensitiveFields(t *testing.T) {
	store, eventLog, permissionStore := openPermissionTestStore(t)
	defer func() {
		_ = store.Close()
	}()

	svc := NewService(permissionStore, eventLog, nil)
	svc.SetRetainToolOutputHistory(true)
	record, err := svc.CreatePending(CreateInput{
		SessionID:     "session_sensitive",
		RunID:         "run_sensitive",
		CallID:        "call_sensitive",
		ToolName:      "bash",
		ToolArguments: `{"api_key":"sk-secret-1234567890","command":"echo hi"}`,
		Requirement:   "bash",
		Mode:          "auto",
	})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}
	if strings.Contains(record.ToolArguments, "sk-secret") {
		t.Fatalf("tool arguments leaked secret: %s", record.ToolArguments)
	}

	stored, ok, err := permissionStore.GetPermission(record.SessionID, record.ID)
	if err != nil {
		t.Fatalf("get permission: %v", err)
	}
	if !ok {
		t.Fatal("expected stored permission record")
	}
	if strings.Contains(stored.ToolArguments, "sk-secret") {
		t.Fatalf("stored tool arguments leaked secret: %s", stored.ToolArguments)
	}

	updated, changed, err := svc.MarkToolCompleted(record.SessionID, record.RunID, record.CallID, 1, tool.Result{
		Output:     "full output with bearer sk-secret-1234567890",
		Error:      "Authorization: Bearer super-secret-token",
		DurationMS: 12,
	}, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("mark tool completed: %v", err)
	}
	if !changed {
		t.Fatal("expected permission record change")
	}
	if strings.Contains(updated.Output, "sk-secret") {
		t.Fatalf("output leaked secret: %s", updated.Output)
	}
	if strings.Contains(updated.Error, "super-secret-token") {
		t.Fatalf("error leaked secret: %s", updated.Error)
	}
	if !strings.Contains(updated.Output, "[redacted") {
		t.Fatalf("expected sanitized retained output, got %q", updated.Output)
	}
}
