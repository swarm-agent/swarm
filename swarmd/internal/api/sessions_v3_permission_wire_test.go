package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the qualification runner must consume the actual V3 allow_once wire
// contract, not a boolean saved_rule fixture. handleSessionV3PrimaryPermissionResolve,
// Service.ResolveWithPolicyAndArguments/resolveLocked and the Pebble permission
// store own this boundary. In-process HTTP serialization is the narrowest layer
// proving explicit null, original executor arguments and no persistent policy.
// A foreign account cannot mutate the record; a racing deny remains denied.
// No provider, executor, tool invocation, task approval or deployment is started.
func TestSessionsV3PrimaryAllowOnceWireContract(t *testing.T) {
	server, sessionSvc, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "permission-wire-create", "permission wire", t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	principal := testPrincipal()
	beforePolicy, err := permissionSvc.CurrentPolicyForAccount(principal.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	callArgs := `{"action":"list_sources","project_id":"fixture-project"}`
	pending, err := permissionSvc.CreatePending(permission.CreateInput{SessionID: created.ID, RunID: "wire-run", Step: 1, CallID: "wire-call", ToolName: "manage_projects", ToolArguments: `{"display_summary":"source discovery"}`, ToolCallArguments: callArgs, Requirement: "tool", Mode: sessionruntime.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	storedBefore, err := permissionSvc.ListPermissions(created.ID, 10)
	if err != nil {
		t.Fatal(err)
	}

	foreign := principal
	foreign.AccountScopeID = "foreign-account"
	foreign.UserID = "foreign-user"
	request := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/permissions/"+pending.ID+"/resolve", bytes.NewBufferString(`{"action":"allow_once","reason":"exact fixture call"}`))
	}
	denied := httptest.NewRecorder()
	server.handleSessionV3PrimaryPermissionResolve(denied, request(), foreign, created.ID, pending.ID)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("foreign status = %d, want 404", denied.Code)
	}
	storedAfter, err := permissionSvc.ListPermissions(created.ID, 10)
	if err != nil || !reflect.DeepEqual(storedBefore, storedAfter) {
		t.Fatalf("foreign resolution changed permission: err=%v", err)
	}

	resolve := func() pebblestore.PermissionRecord {
		t.Helper()
		response := httptest.NewRecorder()
		server.handleSessionV3PrimaryPermissionResolve(response, request(), principal, created.ID, pending.ID)
		if response.Code != http.StatusOK {
			t.Fatalf("resolve status = %d: %s", response.Code, response.Body.String())
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if raw, present := envelope["saved_rule"]; !present || string(raw) != "null" {
			t.Fatalf("saved_rule = %s present=%t, want explicit null", raw, present)
		}
		if string(envelope["ok"]) != "true" || string(envelope["session_id"]) != `"`+created.ID+`"` {
			t.Fatal("invalid resolution envelope identity")
		}
		var record pebblestore.PermissionRecord
		if err := json.Unmarshal(envelope["permission"], &record); err != nil {
			t.Fatal(err)
		}
		if record.ID != pending.ID || record.SessionID != created.ID || record.RunID != pending.RunID || record.CallID != pending.CallID || record.ToolName != "manage_projects" || record.ToolCallArguments != callArgs || record.ToolArguments != pending.ToolArguments {
			t.Fatalf("resolution changed canonical call: %+v", record)
		}
		return record
	}
	approved := resolve()
	if approved.Status != pebblestore.PermissionStatusApproved || approved.Decision != "allow_once" || approved.ApprovedArguments != "" {
		t.Fatalf("allow_once direct response = %+v", approved)
	}
	stored, err := permissionSvc.ListPermissions(created.ID, 10)
	if err != nil || len(stored) != 1 || stored[0].ApprovedArguments != "{}" || stored[0].ToolCallArguments != callArgs || stored[0].Decision != "allow_once" || stored[0].Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("stored allow_once normalization = %+v err=%v", stored, err)
	}
	// A replay reads the normalized stored record, not the pre-persistence value.
	replayed := resolve()
	if replayed.Status != approved.Status || replayed.Decision != approved.Decision || replayed.ApprovedArguments != "{}" || replayed.ResolvedAt != approved.ResolvedAt {
		t.Fatalf("replay changed decision or resolution time: %+v", replayed)
	}

	// Actual service race: a deny wins before the HTTP allow_once arrives.
	pending, err = permissionSvc.CreatePending(permission.CreateInput{SessionID: created.ID, RunID: "wire-run", Step: 2, CallID: "race-call", ToolName: "manage_projects", ToolArguments: callArgs, ToolCallArguments: callArgs, Requirement: "tool", Mode: sessionruntime.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := permissionSvc.ResolveWithPolicyAndArguments(created.ID, pending.ID, "deny", "race winner", ""); err != nil {
		t.Fatal(err)
	}
	raced := resolve()
	if raced.Status != pebblestore.PermissionStatusDenied || raced.Decision != "deny" || raced.ApprovedArguments != "{}" {
		t.Fatalf("racing deny was overwritten: %+v", raced)
	}
	afterPolicy, err := permissionSvc.CurrentPolicyForAccount(principal.AccountScopeID)
	if err != nil || !reflect.DeepEqual(beforePolicy, afterPolicy) {
		t.Fatalf("allow_once or race persisted policy: err=%v", err)
	}
	intents, err := sessionSvc.ListSessionRunIntents(created.ID, 0, 10)
	if err != nil || len(intents) != 0 {
		t.Fatalf("permission resolution started execution: intents=%+v err=%v", intents, err)
	}
}
