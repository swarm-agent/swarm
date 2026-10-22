package pebblestore

import (
	"fmt"
	"strings"
)

// SessionAccessMetadataKey holds what the person let one session's agent do:
// {"tasks": "free"|"ask", "machine": "none"|"ask"|"full"}. The person's app
// sets it (a key with swarm:approve, or the owner) when it starts the session
// or later from its shield; the metadata routes refuse it, and the agent can
// read it but never change it. A session without it keeps Swarm's ordinary
// behavior: task cards follow the permission policy and the agent has no
// access to the person's machine.
const SessionAccessMetadataKey = "swarm_v3_session_access"

const (
	// SessionTasksFree: the agent delegates code and audit tasks that start
	// at once, with nothing to approve. Their work still reaches the
	// person's branch only through the person's merge.
	SessionTasksFree = "free"
	// SessionTasksAsk: task cards wait for the person's Approve.
	SessionTasksAsk = "ask"

	// SessionMachineNone: no access to the person's machine; the agent may
	// request it when the person asks for something that needs it.
	SessionMachineNone = "none"
	// SessionMachineAsk: each command on the person's machine waits for
	// the person to run or deny it.
	SessionMachineAsk = "ask"
	// SessionMachineFull: commands on the person's machine run as the
	// person without asking.
	SessionMachineFull = "full"
)

// SessionAccess is a session's stored access. Empty fields mean the setting
// was never chosen.
type SessionAccess struct {
	Tasks   string `json:"tasks,omitempty"`
	Machine string `json:"machine,omitempty"`
}

// NormalizeSessionAccess trims and checks an access choice. An empty field
// leaves that setting unchanged.
func NormalizeSessionAccess(access SessionAccess) (SessionAccess, error) {
	access.Tasks = strings.ToLower(strings.TrimSpace(access.Tasks))
	access.Machine = strings.ToLower(strings.TrimSpace(access.Machine))
	switch access.Tasks {
	case "", SessionTasksFree, SessionTasksAsk:
	default:
		return SessionAccess{}, fmt.Errorf("tasks must be %q or %q", SessionTasksFree, SessionTasksAsk)
	}
	switch access.Machine {
	case "", SessionMachineNone, SessionMachineAsk, SessionMachineFull:
	default:
		return SessionAccess{}, fmt.Errorf("machine must be %q, %q or %q", SessionMachineNone, SessionMachineAsk, SessionMachineFull)
	}
	return access, nil
}

// MergeSessionAccess applies a change: each non-empty field replaces the
// stored one.
func MergeSessionAccess(current, change SessionAccess) SessionAccess {
	if change.Tasks != "" {
		current.Tasks = change.Tasks
	}
	if change.Machine != "" {
		current.Machine = change.Machine
	}
	return current
}

// SessionAccessFromMetadata reads a session's stored access; anything
// malformed reads as never chosen.
func SessionAccessFromMetadata(metadata map[string]any) SessionAccess {
	var access SessionAccess
	switch raw := metadata[SessionAccessMetadataKey].(type) {
	case map[string]any:
		access.Tasks, _ = raw["tasks"].(string)
		access.Machine, _ = raw["machine"].(string)
	case map[string]string:
		access.Tasks, access.Machine = raw["tasks"], raw["machine"]
	case SessionAccess:
		access = raw
	default:
		return SessionAccess{}
	}
	normalized, err := NormalizeSessionAccess(access)
	if err != nil {
		return SessionAccess{}
	}
	return normalized
}

// SessionAccessMetadata is the stored form of access.
func SessionAccessMetadata(access SessionAccess) map[string]any {
	out := map[string]any{}
	if access.Tasks != "" {
		out["tasks"] = access.Tasks
	}
	if access.Machine != "" {
		out["machine"] = access.Machine
	}
	return out
}

// SessionTasksStartFreely reports whether the person let the session's agent
// start delegated tasks without approval.
func SessionTasksStartFreely(session SessionSnapshot) bool {
	return SessionAccessFromMetadata(session.Metadata).Tasks == SessionTasksFree
}
