package flow

import (
	"reflect"
	"testing"
)

func TestAssignmentCommandIdempotencyKeyUsesExplicitFields(t *testing.T) {
	command := AssignmentCommand{
		CommandID: " command-1 ",
		FlowID:    " flow-1 ",
		Revision:  3,
		Assignment: Assignment{
			FlowID:   "other-flow",
			Revision: 2,
		},
	}

	key := command.IdempotencyKey()
	if key.FlowID != "flow-1" || key.Revision != 3 || key.CommandID != "command-1" {
		t.Fatalf("key = %#v, want explicit flow/revision/command", key)
	}
	if err := command.ValidateIdempotencyKey(); err != nil {
		t.Fatalf("ValidateIdempotencyKey: %v", err)
	}
}

func TestAssignmentCommandIdempotencyKeyFallsBackToAssignment(t *testing.T) {
	command := AssignmentCommand{
		CommandID: "command-1",
		Assignment: Assignment{
			FlowID:   "flow-1",
			Revision: 7,
		},
	}

	key := command.IdempotencyKey()
	if key.FlowID != "flow-1" || key.Revision != 7 || key.CommandID != "command-1" {
		t.Fatalf("key = %#v, want assignment flow/revision fallback", key)
	}
}

func TestAssignmentCommandValidateIdempotencyKeyRequiresAllParts(t *testing.T) {
	for name, command := range map[string]AssignmentCommand{
		"flow_id":    {CommandID: "command-1", Revision: 1},
		"revision":   {CommandID: "command-1", FlowID: "flow-1"},
		"command_id": {FlowID: "flow-1", Revision: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := command.ValidateIdempotencyKey(); err == nil {
				t.Fatal("ValidateIdempotencyKey succeeded, want error")
			}
		})
	}
}

func TestFlowAssignmentDoesNotExposeRequestTimeToolOverrides(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(Assignment{}),
		reflect.TypeOf(AgentSelection{}),
		reflect.TypeOf(AssignmentCommand{}),
	} {
		for _, field := range []string{"ToolScope", "ToolContract", "AllowTools", "DenyTools", "BashPrefixes"} {
			if _, ok := typ.FieldByName(field); ok {
				t.Fatalf("%s exposes request-time tool override field %s", typ.Name(), field)
			}
		}
	}
}
