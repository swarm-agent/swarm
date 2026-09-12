package pebblestore

import (
	"strings"
	"unicode/utf8"
)

// Creation provenance comes from the authenticated mutation actor, not kind or
// model-authored metadata. Existing unknown provenance remains unknown on edit.
func (s *MemoryStore) prepareMemoryMetadata(d MemoryDocument, m MemoryMutation, e *MemoryEntry, index int) error {
	switch e.Purpose {
	case "", "preference", "project_context", "operational_context", "recovery", "orientation":
	default:
		return ErrMemoryPolicy
	}
	if len(e.Subject) > 256 || !utf8.ValidString(e.Subject) || strings.ContainsRune(e.Subject, 0) {
		return ErrMemoryPolicy
	}
	origin := "user"
	if e.ID == MemoryWorkspaceMapID && m.Actor.ID == "workspace-map-compatibility" {
		origin = "system"
	}
	if m.Actor.Kind == "learned" {
		origin = "learned"
	}
	if index >= 0 {
		origin = d.Entries[index].Origin
	}
	if m.Operation == "restore" {
		origin = e.Origin
	}
	if e.Origin != "" && e.Origin != origin {
		return ErrMemoryPolicy
	}
	e.Origin = origin
	// Scope IDs are contextual labels. Source references, unlike labels, assert
	// evidence and must resolve to this authenticated user's owned session.
	for _, src := range e.Sources {
		if src.SessionID == "" {
			return ErrMemoryPolicy
		}
		sess, ok, err := NewSessionStore(s.store).GetSession(src.SessionID)
		if err != nil {
			return err
		}
		if !ok || sess.AccountScopeID != d.AccountScopeID || sess.UserID != m.Actor.ID {
			return ErrMemoryPolicy
		}
	}
	return nil
}
