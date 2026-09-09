package pebblestore

import (
	"encoding/json"
	"sort"
)

// MemorySelection is a deterministic next-request preview, not a claim about an
// earlier provider request. The complete serialized payload is budgeted.
type MemorySelection struct {
	Revision       int64            `json:"revision"`
	TokenMethod    string           `json:"token_method"`
	Budget         int              `json:"budget"`
	InjectedTokens int              `json:"injected_tokens"`
	Entries        []MemoryEntry    `json:"entries"`
	Omitted        []MemoryOmission `json:"omitted"`
	Payload        string           `json:"payload"`
}
type MemoryOmission struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

func SelectMemory(d MemoryDocument, workspaceID, sessionID string) MemorySelection {
	out := MemorySelection{Revision: d.Revision, TokenMethod: "utf8_bytes_upper_bound", Budget: d.Settings.InjectionTokens, Entries: []MemoryEntry{}, Omitted: []MemoryOmission{}}
	entries := append([]MemoryEntry(nil), d.Entries...)
	rank := func(e MemoryEntry) int {
		if e.Kind == "rule" {
			return 0
		}
		if e.Pinned {
			return 1
		}
		if e.Kind == "orientation" {
			return 2
		}
		return 3
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := rank(entries[i]), rank(entries[j])
		if a != b {
			return a < b
		}
		return entries[i].ID < entries[j].ID
	})
	for _, e := range entries {
		reason := ""
		switch {
		case !d.Settings.ReadEnabled:
			reason = "reading disabled"
		case memoryEntryDenied(d, e):
			reason = "excluded or forgotten"
		case e.SessionID != "" && e.SessionID != sessionID:
			reason = "different session"
		case e.WorkspaceID != "" && e.WorkspaceID != workspaceID && e.Kind != "orientation":
			reason = "different workspace"
		}
		if reason == "" {
			candidate := append(append([]MemoryEntry(nil), out.Entries...), e)
			payload, _ := json.Marshal(struct {
				Revision int64         `json:"revision"`
				Entries  []MemoryEntry `json:"entries"`
			}{d.Revision, candidate})
			if len(payload) > out.Budget {
				reason = "injection budget"
			} else {
				out.Entries = candidate
				out.Payload = string(payload)
				out.InjectedTokens = len(payload)
			}
		}
		if reason != "" {
			out.Omitted = append(out.Omitted, MemoryOmission{e.ID, reason})
		}
	}
	return out
}

// SelectionForSession authenticates the session before deriving its scope. Empty
// session selects only account-wide guidance and cross-workspace orientation.
func (s *MemoryStore) SelectionForSession(account, user, sessionID string) (MemorySelection, error) {
	workspaceID := ""
	if sessionID != "" {
		sess, ok, err := NewSessionStore(s.store).GetSession(sessionID)
		if err != nil {
			return MemorySelection{}, err
		}
		if !ok || sess.AccountScopeID != account || sess.UserID != user {
			return MemorySelection{}, ErrMemoryPolicy
		}
		workspaceID = sessionMetadataString(sess.Metadata, "swarm_v3_source_workspace_id")
	}
	d, err := s.GetForAccount(account)
	if err != nil {
		return MemorySelection{}, err
	}
	return SelectMemory(d, workspaceID, sessionID), nil
}
