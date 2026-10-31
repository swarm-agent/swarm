package tool

import (
	"encoding/json"
	"strings"
)

// A catalog call that is refused or fails still answers in the envelope, on
// both surfaces and from a linked machine alike: state refused (the call was
// not accepted: a missing or wrong input, a rule, a record that does not
// exist) or error (it could not run: a service or machine unavailable), the
// reason in data, and next_action naming the fix when the reason names one.
// The Go error a handler returns keeps its plain reason; the envelope is what
// a client or the model is shown (Swarm Control's isError text, the
// Orchestrator's tool output).

// catalogErrorMarkers are reasons that say the call could not run, not that
// it was refused.
var catalogErrorMarkers = []string{"unavailable", "unreachable", "timed out", "deadline exceeded", "not configured", "could not reach", "connection refused", "tool failed", "internal error"}

// CatalogFailureState is refused or error for a failed call's reason. It
// reads the reason only, which both surfaces show alike, so the Orchestrator
// and Swarm Control always give the same state.
func CatalogFailureState(reason string) string {
	lower := strings.ToLower(reason)
	for _, marker := range catalogErrorMarkers {
		if strings.Contains(lower, marker) {
			return EnvelopeStateError
		}
	}
	return EnvelopeStateRefused
}

// catalogFailureFix is the fix a reason names after its last "; " (the
// catalog's reasons end with what to call), or "".
func catalogFailureFix(reason string) string {
	cut := strings.LastIndex(reason, "; ")
	if cut < 0 {
		return ""
	}
	fix := strings.TrimSpace(reason[cut+2:])
	if fix == "" || len(fix) > 300 {
		return ""
	}
	return fix
}

// FailureEnvelope is a failed catalog call's answer: the envelope with state
// refused or error and the reason in data. machine is where it ran (the home
// machine's name, or the linked machine's); an answer from a linked machine
// is untrusted.
func FailureEnvelope(toolName, action, machine string, linked bool, err error, reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" && err != nil {
		reason = strings.TrimSpace(err.Error())
	}
	parts := EnvelopeParts{Tool: toolName, Action: action, Machine: machine, State: CatalogFailureState(reason),
		Data: map[string]any{"reason": reason}, NextAction: catalogFailureFix(reason), Untrusted: linked}
	out, marshalErr := json.Marshal(NewEnvelope(parts))
	if marshalErr != nil {
		return reason
	}
	return string(out)
}

// IsFailureEnvelope reports whether text is a catalog failure's envelope.
func IsFailureEnvelope(text string) bool {
	var envelope struct {
		Tool  string `json:"tool"`
		State string `json:"state"`
	}
	if !strings.HasPrefix(strings.TrimSpace(text), "{") || json.Unmarshal([]byte(text), &envelope) != nil {
		return false
	}
	if _, ok := CatalogToolNamed(envelope.Tool); !ok {
		return false
	}
	return envelope.State == EnvelopeStateRefused || envelope.State == EnvelopeStateError
}

// catalogCallFailure is the Orchestrator's answer to a failed catalog call:
// its failure envelope, with the action and machine the call named.
func (r *Runtime) catalogCallFailure(call Call, err error) (string, bool) {
	catalog, ok := CatalogToolNamed(strings.TrimSpace(call.Name))
	if !ok || err == nil {
		return "", false
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(call.Arguments), &args)
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if action == "" {
		action = "list"
	}
	machine := strings.TrimSpace(asString(args["machine"]))
	linked := machine != ""
	if !linked {
		machine = r.homeMachineName()
	}
	return FailureEnvelope(catalog.Name, action, machine, linked, err, err.Error()), true
}
