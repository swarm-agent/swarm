package api

import (
	"encoding/json"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAITaskLifecyclePayloadIncludesManagedTitleAndTerminalFields(t *testing.T) {
	item := pebblestore.WorkspaceTodoItem{
		ID: "task-1", AccountScopeID: "account-1", UserID: "user-1",
		WorkspaceID: "workspace-1", WorkspacePath: "/workspace",
		Text: "request title", AIDisplayTitle: "Prepared title",
		AIState: pebblestore.WorkspaceTodoAIStateCompleted, AIStateVersion: 4,
		ManagedSessionID: "session-1", FinalRunID: "run-1", AIResult: "done",
		CreatedAt: 1, UpdatedAt: 2, CompletedAt: 2,
	}
	payload := newSessionsV3AITaskLifecyclePayload(item)
	if payload.TaskID != item.ID || payload.RequestTitle != item.Text || payload.DisplayTitle != item.AIDisplayTitle {
		t.Fatalf("identity/title payload = %+v", payload)
	}
	if payload.State != pebblestore.WorkspaceTodoAIStateCompleted || payload.Version != 4 || payload.ManagedSessionID != "session-1" || payload.ManagedRunID != "run-1" || payload.Result != "done" {
		t.Fatalf("terminal payload = %+v", payload)
	}
}

func TestAITaskLifecycleResourceRoundTripsThroughCanonicalRealtimeContract(t *testing.T) {
	payload := newSessionsV3AITaskLifecyclePayload(pebblestore.WorkspaceTodoItem{
		ID: "task-replay", AccountScopeID: "account", UserID: "user", WorkspaceID: "workspace", WorkspacePath: "/workspace",
		Text: "request", AIDisplayTitle: "Prepared title", AIState: pebblestore.WorkspaceTodoAIStateCompleted, AIStateVersion: 4,
		ManagedSessionID: "session", FinalRunID: "run", AIResult: "done", CreatedAt: 1, UpdatedAt: 2, CompletedAt: 2,
	})
	message := V3RealtimeMessage{
		Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion,
		Kind: V3RealtimeKindAITaskResource, EndpointCursor: "opaque-cursor", AITask: &payload,
	}
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded V3RealtimeMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateV3RealtimeOutboundServerMessage(decoded); err != nil {
		t.Fatalf("task lifecycle resource rejected by canonical realtime contract: %v", err)
	}
	if decoded.AITask == nil || decoded.AITask.DisplayTitle != "Prepared title" || decoded.AITask.State != pebblestore.WorkspaceTodoAIStateCompleted || decoded.AITask.Result != "done" {
		t.Fatalf("replayed task lifecycle payload=%#v", decoded.AITask)
	}
}

func TestAITaskResourceIsAcceptedByRealtimeWorkset(t *testing.T) {
	if !v3RealtimeWorksetResourceAllowed("tasks") {
		t.Fatal("tasks must be a V3 realtime workset resource")
	}
	workset := v3RealtimeWorksetSubscription{Resources: []string{"tasks"}}
	record := pebblestore.V3RealtimeOutboxRecord{Event: pebblestore.V3SessionEvent{EventType: v3AITaskLifecycleEventType}}
	if !v3RealtimeWorksetIncludesRecordResource(workset, record) {
		t.Fatal("task lifecycle record must be routed only to tasks resource subscribers")
	}
}
