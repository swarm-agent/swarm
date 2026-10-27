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
	// The person's browser (their Chrome, through their app's extension): a
	// lease the person grants for a while, scoped to sites.
	// SessionBrowserNone: no lease; the agent may ask with request_browser_access.
	SessionBrowserNone = "none"
	// SessionBrowserAsk: every action in the browser waits for the person.
	SessionBrowserAsk = "ask"
	// SessionBrowserConfirm: reading, moving and typing run; actions the
	// app judges critical (paying, publishing, keys, account settings) wait.
	SessionBrowserConfirm = "confirm"
	// SessionBrowserFull: everything runs; the person watches.
	SessionBrowserFull = "full"
)

// SessionAccess is a session's stored access. Empty fields mean the setting
// was never chosen. The browser lease carries the sites it covers (hosts,
// space-separated) and when it ends (Unix ms); a lease past its end counts
// as none.
type SessionAccess struct {
	Fleet        map[string]string `json:"fleet,omitempty"`
	Tasks        string            `json:"tasks,omitempty"`
	Machine      string            `json:"machine,omitempty"`
	Browser      string            `json:"browser,omitempty"`
	BrowserSites string            `json:"browser_sites,omitempty"`
	BrowserUntil int64             `json:"browser_until,omitempty"`
}

// BrowserLeaseLive reports whether the access holds a browser lease that
// has not ended at now (Unix ms).
func (a SessionAccess) BrowserLeaseLive(now int64) bool {
	return a.Browser != "" && a.Browser != SessionBrowserNone && a.BrowserUntil > now
}

// NormalizeSessionAccess trims and checks an access choice. An empty field
// leaves that setting unchanged.
func NormalizeSessionAccess(access SessionAccess) (SessionAccess, error) {
	if access.Fleet != nil {
		fleet := make(map[string]string, len(access.Fleet))
		for name, level := range access.Fleet {
			if name == "" || strings.TrimSpace(name) != name {
				return SessionAccess{}, fmt.Errorf("fleet machine name must be nonempty without surrounding whitespace")
			}
			switch level {
			case SessionMachineNone, SessionMachineAsk, SessionMachineFull:
			default:
				return SessionAccess{}, fmt.Errorf("fleet access for %q must be none, ask or full", name)
			}
			fleet[name] = level
		}
		access.Fleet = fleet
	}
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
	access.Browser = strings.ToLower(strings.TrimSpace(access.Browser))
	access.BrowserSites = strings.Join(strings.Fields(strings.ToLower(access.BrowserSites)), " ")
	switch access.Browser {
	case "", SessionBrowserNone, SessionBrowserAsk, SessionBrowserConfirm, SessionBrowserFull:
	default:
		return SessionAccess{}, fmt.Errorf("browser must be %q, %q, %q or %q", SessionBrowserNone, SessionBrowserAsk, SessionBrowserConfirm, SessionBrowserFull)
	}
	if access.Browser == SessionBrowserNone {
		access.BrowserSites, access.BrowserUntil = "", 0
	}
	return access, nil
}

// MergeSessionAccess applies a change: each non-empty field replaces the
// stored one.
func MergeSessionAccess(current, change SessionAccess) SessionAccess {
	if change.Fleet != nil {
		current.Fleet = make(map[string]string, len(change.Fleet))
		for name, level := range change.Fleet {
			current.Fleet[name] = level
		}
	}
	if change.Tasks != "" {
		current.Tasks = change.Tasks
	}
	if change.Machine != "" {
		current.Machine = change.Machine
	}
	// A browser choice replaces the whole lease: level, sites and end.
	if change.Browser != "" {
		current.Browser, current.BrowserSites, current.BrowserUntil = change.Browser, change.BrowserSites, change.BrowserUntil
	}
	return current
}

// SessionAccessFromMetadata reads a session's stored access; anything
// malformed reads as never chosen.
func SessionAccessFromMetadata(metadata map[string]any) SessionAccess {
	var access SessionAccess
	switch raw := metadata[SessionAccessMetadataKey].(type) {
	case map[string]any:
		switch fleet := raw["fleet"].(type) {
		case map[string]string:
			access.Fleet = fleet
		case map[string]any:
			access.Fleet = make(map[string]string, len(fleet))
			for name, value := range fleet {
				access.Fleet[name], _ = value.(string)
			}
		}
		access.Tasks, _ = raw["tasks"].(string)
		access.Machine, _ = raw["machine"].(string)
		access.Browser, _ = raw["browser"].(string)
		access.BrowserSites, _ = raw["browser_sites"].(string)
		switch until := raw["browser_until"].(type) {
		case float64:
			access.BrowserUntil = int64(until)
		case int64:
			access.BrowserUntil = until
		case int:
			access.BrowserUntil = int64(until)
		}
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
	if access.Fleet != nil {
		fleet := make(map[string]any, len(access.Fleet))
		for name, level := range access.Fleet {
			fleet[name] = level
		}
		out["fleet"] = fleet
	}
	if access.Tasks != "" {
		out["tasks"] = access.Tasks
	}
	if access.Machine != "" {
		out["machine"] = access.Machine
	}
	if access.Browser != "" {
		out["browser"] = access.Browser
		if access.BrowserSites != "" {
			out["browser_sites"] = access.BrowserSites
		}
		if access.BrowserUntil > 0 {
			out["browser_until"] = access.BrowserUntil
		}
	}
	return out
}

// SessionTasksStartFreely reports whether the person let the session's agent
// start delegated tasks without approval.
func SessionTasksStartFreely(session SessionSnapshot) bool {
	return SessionAccessFromMetadata(session.Metadata).Tasks == SessionTasksFree
}

// Empty reports whether no access choices were supplied.
func (a SessionAccess) Empty() bool {
	return a.Tasks == "" && a.Machine == "" && a.Browser == "" && a.BrowserSites == "" && a.BrowserUntil == 0 && a.Fleet == nil
}
