package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

// SessionCompanionMetadataKey marks the one V3 session that is an account
// user's Companion. It is protected session metadata: only the Companion
// service sets it, and generic metadata updates cannot add or remove it.
const SessionCompanionMetadataKey = "swarm_v3_companion"

var ErrCompanionConflict = errors.New("companion identity changed; reload and retry")

// CompanionIdentity is the durable owner record of an account user's
// Companion. It holds no conversation content: history and events stay in the
// canonical V3 session named by SessionID. Generation advances only when the
// user explicitly deletes their Companion, so a new Companion never reuses a
// deleted session ID and nothing from older chats is imported implicitly.
type CompanionIdentity struct {
	AccountScopeID string `json:"account_scope_id"`
	UserID         string `json:"user_id"`
	SessionID      string `json:"session_id"`
	Generation     uint64 `json:"generation"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

// CompanionSessionID is the deterministic session ID for one generation of an
// account user's Companion. Every concurrent or retried get-or-create of the
// same generation names the same session, so the canonical create mutation's
// idempotency makes duplicates impossible.
func CompanionSessionID(account, user string, generation uint64) string {
	b, _ := json.Marshal([]any{"companion", strings.TrimSpace(account), strings.TrimSpace(user), generation})
	sum := sha256.Sum256(b)
	return "companion_" + hex.EncodeToString(sum[:16])
}

func companionKey(account, user string) []byte {
	b, _ := json.Marshal([]string{account, user})
	h := sha256.Sum256(b)
	return []byte("companion_identity:" + hex.EncodeToString(h[:]))
}

func validCompanionPrincipal(account, user string) bool {
	return strings.TrimSpace(account) != "" && strings.TrimSpace(user) != ""
}

// GetCompanion reads the account user's Companion identity.
func (s *SessionStore) GetCompanion(account, user string) (CompanionIdentity, bool, error) {
	var record CompanionIdentity
	if !validCompanionPrincipal(account, user) {
		return record, false, errors.New("companion principal required")
	}
	b, closer, err := s.store.db.Get(companionKey(strings.TrimSpace(account), strings.TrimSpace(user)))
	if errors.Is(err, pebble.ErrNotFound) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	defer closer.Close()
	if err := json.Unmarshal(b, &record); err != nil {
		return CompanionIdentity{}, false, fmt.Errorf("decode companion identity: %w", err)
	}
	return record, true, nil
}

// EnsureCompanion returns the account user's Companion identity, creating
// generation 1 the first time. It is idempotent and serialized, so
// concurrent callers always receive the same identity.
func (s *SessionStore) EnsureCompanion(account, user string, now int64) (CompanionIdentity, bool, error) {
	account, user = strings.TrimSpace(account), strings.TrimSpace(user)
	if !validCompanionPrincipal(account, user) {
		return CompanionIdentity{}, false, errors.New("companion principal required")
	}
	s.store.companionMu.Lock()
	defer s.store.companionMu.Unlock()
	current, found, err := s.GetCompanion(account, user)
	if err != nil || found {
		return current, false, err
	}
	record := CompanionIdentity{AccountScopeID: account, UserID: user, Generation: 1, CreatedAt: now, UpdatedAt: now}
	record.SessionID = CompanionSessionID(account, user, record.Generation)
	if err := s.putCompanion(record); err != nil {
		return CompanionIdentity{}, false, err
	}
	return record, true, nil
}

// RetireCompanion advances the identity to a new generation after the user
// explicitly deleted the Companion session expectedSessionID. The next
// EnsureCompanion caller then gets a fresh, empty Companion. A stale
// expectedSessionID is refused so a retried delete cannot retire the
// replacement.
func (s *SessionStore) RetireCompanion(account, user, expectedSessionID string, now int64) (CompanionIdentity, error) {
	account, user = strings.TrimSpace(account), strings.TrimSpace(user)
	if !validCompanionPrincipal(account, user) || strings.TrimSpace(expectedSessionID) == "" {
		return CompanionIdentity{}, errors.New("companion principal and session id required")
	}
	s.store.companionMu.Lock()
	defer s.store.companionMu.Unlock()
	current, found, err := s.GetCompanion(account, user)
	if err != nil {
		return CompanionIdentity{}, err
	}
	if !found || current.SessionID != strings.TrimSpace(expectedSessionID) {
		return CompanionIdentity{}, ErrCompanionConflict
	}
	current.Generation++
	current.SessionID = CompanionSessionID(account, user, current.Generation)
	current.UpdatedAt = now
	if err := s.putCompanion(current); err != nil {
		return CompanionIdentity{}, err
	}
	return current, nil
}

// IsCurrentCompanion reports whether session is the account user's current
// Companion: it carries the protected Companion marker, belongs to them, and
// their durable identity names it. The marker alone is not enough, so a
// deleted generation or a copied snapshot never qualifies.
func (s *SessionStore) IsCurrentCompanion(session SessionSnapshot, account, user string) (bool, error) {
	account, user = strings.TrimSpace(account), strings.TrimSpace(user)
	if _, marked := session.Metadata[SessionCompanionMetadataKey]; !marked || !validCompanionPrincipal(account, user) || session.AccountScopeID != account || session.UserID != user {
		return false, nil
	}
	record, found, err := s.GetCompanion(account, user)
	if err != nil || !found {
		return false, err
	}
	return record.SessionID == session.ID, nil
}

// CheckoutFreeCompanion reports whether session is marked as a Companion and
// carries no source checkout: no workspace, worktree, grants or project. Such a
// Companion is a conversation with runtime identity only; it has no implicit
// working directory or repository authority. Callers must still confirm it
// with ValidateCheckoutFreeCompanion before treating it as authorized.
func CheckoutFreeCompanion(session SessionSnapshot) bool {
	if _, marked := session.Metadata[SessionCompanionMetadataKey]; !marked {
		return false
	}
	return strings.TrimSpace(session.WorkspacePath) == "" && ProjectConversationID(session) == ""
}

// ValidateCheckoutFreeCompanion keeps a checkout-free Companion's conversation
// authority separate from every filesystem grant, like a project
// conversation: it must be the principal's current Companion and carry no
// workspace, worktree, grant or task metadata.
func (s *SessionStore) ValidateCheckoutFreeCompanion(session SessionSnapshot, account, user string) error {
	if !CheckoutFreeCompanion(session) {
		return errors.New("session is not a checkout-free Companion")
	}
	current, err := s.IsCurrentCompanion(session, account, user)
	if err != nil {
		return err
	}
	if !current {
		return errors.New("companion ownership mismatch")
	}
	if session.WorkspaceName != "" || session.WorktreeEnabled || session.WorktreeRootPath != "" || session.WorktreeBranch != "" || session.WorktreeBaseBranch != "" || len(session.WorkspaceGrants) != 0 || len(session.TemporaryWorkspaceRoots) != 0 {
		return errors.New("checkout-free Companion cannot carry filesystem authority")
	}
	for _, key := range []string{"project_id", "task_id", "task_attempt_id", "parent_session_id", "workspace_path", "worktree_root_path", "worktree_enabled"} {
		if value, supplied := session.Metadata[key]; supplied && value != nil && value != "" {
			return errors.New("checkout-free Companion cannot carry task or workspace metadata")
		}
	}
	return nil
}

// companionWatchesProject reports whether parent may watch the local tasks of
// projectID as the account user's Companion. The Companion is not a project
// conversation: it follows the tasks it delegates in any of the account's
// projects with the same watches a project Orchestrator uses.
func (s *SessionStore) companionWatchesProject(parent SessionSnapshot, account, user, projectID string) (bool, error) {
	if strings.TrimSpace(projectID) == "" {
		return false, nil
	}
	companion, err := s.IsCurrentCompanion(parent, account, user)
	if err != nil || !companion {
		return false, err
	}
	project, found, err := s.GetProject(account, projectID)
	if err != nil {
		return false, err
	}
	return found && project != nil, nil
}

func (s *SessionStore) putCompanion(record CompanionIdentity) error {
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.store.db.Set(companionKey(record.AccountScopeID, record.UserID), b, pebble.Sync)
}
