package tool

import (
	"encoding/json"
	"errors"
	"testing"
)

// Purpose: every catalog answer, a refusal or failure included, is the
// envelope: state refused (not accepted) or error (could not run), the
// reason in data, next_action the fix the reason names, untrusted when it
// came from a linked machine; the Go error keeps its plain reason. Threat:
// the parity defect where a linked machine's refusal reached the client as
// plain text outside the envelope. Authority: FailureEnvelope,
// CatalogFailureState, IsFailureEnvelope and Runtime.catalogCallFailure
// (ExecuteBatch's catalog failure answer); TestCatalogParityOrchestratorAndControl
// proves both surfaces give the identical envelope.
func TestCatalogFailureEnvelope(t *testing.T) {
	decode := func(raw string) Envelope {
		t.Helper()
		var envelope Envelope
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			t.Fatalf("not an envelope: %s", raw)
		}
		if err := CheckEnvelope(raw); err != nil {
			t.Fatalf("contract: %v: %s", err, raw)
		}
		return envelope
	}
	linked := "linked machine box refused get_task: the machine answered HTTP 400: project_id is required for get_task; manage_projects action=list lists your projects and their ids"
	raw := FailureEnvelope("manage_projects", "get_task", "box", true, errors.New(linked), linked)
	e := decode(raw)
	data, _ := e.Data.(map[string]any)
	if e.State != EnvelopeStateRefused || e.Machine != "box" || !e.UntrustedContent || data["reason"] != linked || e.NextAction != "manage_projects action=list lists your projects and their ids" {
		t.Fatalf("linked refusal: %s", raw)
	}
	if !IsFailureEnvelope(raw) {
		t.Fatal("a failure envelope was not recognised")
	}
	// Could not run: error, here (the home's name), no fix to name.
	raw = FailureEnvelope("manage_fleet", "status", "", false, nil, "box is unreachable: dial tcp: connection refused")
	if e := decode(raw); e.State != EnvelopeStateError || e.Machine != HomeMachineFallback || e.UntrustedContent || e.NextAction != "" {
		t.Fatalf("unreachable: %s", raw)
	}
	// An answer that is not a failure, or not a catalog tool's, is not one.
	for _, text := range []string{`{"tool":"manage_projects","action":"get_task","machine":"home","state":"ok","data":{}}`, `{"tool":"bash","state":"error"}`, "plain text", ""} {
		if IsFailureEnvelope(text) {
			t.Fatalf("%q read as a failure envelope", text)
		}
	}

	// The Orchestrator's runtime answers a failed catalog call with it; any
	// other tool's failure is untouched.
	r := NewRuntime(1)
	defer r.Close()
	out, ok := r.catalogCallFailure(Call{Name: "manage_projects", Arguments: `{"action":"Get_Task","machine":"box"}`}, errors.New(linked))
	if e := decode(out); !ok || e.Action != "get_task" || e.Machine != "box" || e.State != EnvelopeStateRefused || !e.UntrustedContent {
		t.Fatalf("Orchestrator failure: %s", out)
	}
	if _, ok := r.catalogCallFailure(Call{Name: "bash", Arguments: `{}`}, errors.New("exit 1")); ok {
		t.Fatal("a non-catalog tool's failure was enveloped")
	}
	if _, ok := r.catalogCallFailure(Call{Name: "manage_projects"}, nil); ok {
		t.Fatal("a call without an error was enveloped")
	}
}
