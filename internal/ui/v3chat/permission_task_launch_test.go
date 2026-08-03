package v3chat

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
)

func taskLaunchTestPermission() client.PermissionRecord {
	return client.PermissionRecord{
		ID:            "permission-task",
		SessionID:     "session-parent",
		RunID:         "run-parent",
		CallID:        "call-task",
		ToolName:      "functions.task",
		Requirement:   "task_launch",
		Mode:          "auto",
		Status:        "pending",
		ToolArguments: `{"path_id":"permission.task_launch.v1","description":"Inspect two independent areas","prompt":"Return findings with evidence.","launch_count":2,"resolved_agent_name":"finder","approved_arguments":{"action":"spawn","manifest_hash":"exact-wave"},"launches":[{"description":"Backend map","requested_subagent_type":"finder","resolved_agent_name":"finder","meta_prompt":"Inspect backend architecture.","deliverable":"Backend evidence","owned_scope":["swarmd/internal/**"]},{"description":"TUI map","requested_subagent_type":"finder","resolved_agent_name":"finder","meta_prompt":"Inspect TUI permission handling.","deliverable":"TUI evidence","owned_scope":["internal/ui/**"]}]}`,
	}
}

func TestTaskLaunchPermissionDrawsBoundedModal(t *testing.T) {
	permission := taskLaunchTestPermission()
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: permission.SessionID}, PendingPermissions: []client.PermissionRecord{permission}}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(92, 24)

	page.Draw(screen)
	screen.Show()
	drawn := simulationText(screen, 92, 24)
	for _, want := range []string{"LAUNCH 2 SUBAGENTS?", "Bounded exact-wave review", "Backend map", "TUI map", "Enter Launch", "Esc Deny"} {
		if !strings.Contains(drawn, want) {
			t.Fatalf("task launch modal missing %q:\n%s", want, drawn)
		}
	}
	if strings.Contains(drawn, "manifest_hash") || strings.Contains(drawn, "approved_arguments") {
		t.Fatalf("task launch modal leaked canonical envelope fields:\n%s", drawn)
	}
}

func TestTaskLaunchPermissionForwardsCanonicalApprovedArguments(t *testing.T) {
	permission := taskLaunchTestPermission()
	if got := taskLaunchApprovedArguments(permission); !strings.Contains(got, `"manifest_hash":"exact-wave"`) {
		t.Fatalf("approved arguments = %q, want exact canonical wave", got)
	}
}

func TestApprovedTaskLaunchPermissionTransitionsToCanonicalTaskStream(t *testing.T) {
	permission := taskLaunchTestPermission()
	permission.Status = "approved"
	permission.Decision = "allow_once"
	state := NewState()
	state.Session.ID = permission.SessionID
	state.Permissions.Records = []PermissionTimelineItem{{Record: permission, GlobalSeq: 1}}
	state.Tools[permission.CallID] = ToolTimelineItem{
		ID:        permission.CallID,
		CallID:    permission.CallID,
		GlobalSeq: 2,
		Name:      "task",
		Status:    "running",
		TaskStream: &TaskStreamState{
			PathID:        "tool.task.stream.v2",
			Status:        "running",
			LaunchCount:   1,
			LaunchOrder:   []string{"child-1"},
			LaunchesByKey: map[string]map[string]any{"child-1": {"launch_index": 1, "subagent": "finder", "assignment_label": "Test one Finder delegation", "status": "running", "current_tool": "search"}},
		},
	}

	rows := NewPage(nil, testPageStyles()).renderRows(state, 100, testPageStyles())
	var rendered strings.Builder
	for _, row := range rows {
		rendered.WriteString(row.text)
		rendered.WriteByte('\n')
	}
	text := rendered.String()
	for _, want := range []string{"SUBAGENT STREAM", "Test one Finder delegation", "@finder", "current: search"} {
		if !strings.Contains(text, want) {
			t.Fatalf("approved task launch did not transition to canonical stream %q:\n%s", want, text)
		}
	}
	for _, stale := range []string{"Launch 2 subagents", "Resolved · Approved once", "RESOLVED"} {
		if strings.Contains(text, stale) {
			t.Fatalf("approved task launch kept stale permission content %q:\n%s", stale, text)
		}
	}
}

func TestResolvedTaskLaunchPermissionRemainsUntilCanonicalTaskStreamArrives(t *testing.T) {
	permission := taskLaunchTestPermission()
	permission.Status = "approved"
	permission.Decision = "allow_once"
	state := NewState()
	state.Session.ID = permission.SessionID
	state.Permissions.Records = []PermissionTimelineItem{{Record: permission, GlobalSeq: 1}}

	rows := NewPage(nil, testPageStyles()).renderRows(state, 100, testPageStyles())
	var rendered strings.Builder
	for _, row := range rows {
		rendered.WriteString(row.text)
		rendered.WriteByte('\n')
	}
	if text := rendered.String(); !strings.Contains(text, "Resolved · Approved once") || !strings.Contains(text, "RESOLVED") {
		t.Fatalf("resolved task permission disappeared before the task stream arrived:\n%s", text)
	}
}

func TestTaskLaunchPermissionTakesFocusOverOtherPendingPermission(t *testing.T) {
	taskPermission := taskLaunchTestPermission()
	ordinary := client.PermissionRecord{ID: "permission-read", SessionID: taskPermission.SessionID, ToolName: "functions.read", Requirement: "tool", Mode: "auto", Status: "pending", ToolArguments: `{"path":"README.md"}`}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: taskPermission.SessionID}, PendingPermissions: []client.PermissionRecord{ordinary, taskPermission}}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	page.mu.Lock()
	page.permissionIndex = 0
	page.mu.Unlock()

	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	page.mu.Lock()
	selected := page.permissionIndex
	page.mu.Unlock()
	permissions := SelectPendingPermissions(store.Snapshot())
	if selected < 0 || selected >= len(permissions) || !isTaskLaunchPermission(permissions[selected]) {
		t.Fatalf("selected permission index = %d, want task launch focus", selected)
	}
}
