package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	modelruntime "swarm/packages/swarmd/internal/model"
	codexruntime "swarm/packages/swarmd/internal/provider/codex"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type Service struct {
	store                *pebblestore.SessionStore
	events               *pebblestore.EventLog
	mu                   sync.Mutex
	counter              atomic.Uint64
}

type CreateSessionOptions struct {
	SessionID      string
	UserID         string
	AccountScopeID string
	Title          string
	WorkspacePath  string
	WorkspaceName  string
	Mode           string
	Preference     *pebblestore.ModelPreference
	Worktree       *CreateSessionWorktree
	Metadata       map[string]any
}

type CreateSessionWorktree struct {
	RootPath    string
	BaseBranch  string
	BranchName  string
	WorkspaceID string
}

type SessionPreferenceUpdate struct {
	Provider    *string
	Model       *string
	Thinking    *string
	ServiceTier *string
	ContextMode *string
}

type SessionCodexConfigUpdate = SessionPreferenceUpdate

const (
	ModePlan = "plan"
	ModeAuto = "auto"

	planModeReentrySystemMessage = "Session mode changed to plan. The user explicitly re-entered plan mode; immediately follow plan-mode behavior for the next turn, use plan_manage to inspect or revise the active plan, and call exit_plan_mode only after presenting an actionable plan for approval."
	autoModeReentrySystemMessage = "Session mode changed to auto. The user explicitly exited plan mode; immediately follow auto-mode behavior for the next turn, do not call exit_plan_mode, and use plan_manage to inspect or revise any active plan."
)

func NewService(store *pebblestore.SessionStore, events *pebblestore.EventLog) *Service {
	return &Service{store: store, events: events}
}

func (s *Service) Store() *pebblestore.SessionStore {
	if s == nil {
		return nil
	}
	return s.store
}

func (s *Service) BeginExecutionEpoch(input pebblestore.BeginExecutionEpochInput) (pebblestore.BeginExecutionEpochResult, error) {
	if s == nil || s.store == nil {
		return pebblestore.BeginExecutionEpochResult{}, errors.New("session service is not configured")
	}
	return s.store.BeginExecutionEpoch(input)
}

func (s *Service) GetExecutionEpoch(sessionID, epochID string) (pebblestore.ExecutionEpoch, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ExecutionEpoch{}, false, errors.New("session service is not configured")
	}
	return s.store.GetExecutionEpoch(sessionID, epochID)
}

func (s *Service) ListExecutionEpochMessages(sessionID, epochID string, limit int) (pebblestore.ExecutionEpoch, []pebblestore.MessageSnapshot, error) {
	if s == nil || s.store == nil {
		return pebblestore.ExecutionEpoch{}, nil, errors.New("session service is not configured")
	}
	return s.store.ListExecutionEpochMessages(sessionID, epochID, limit)
}

func (s *Service) GetExecutionProviderLifecycleState(sessionID, epochID string) (pebblestore.ExecutionProviderLifecycleState, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ExecutionProviderLifecycleState{}, false, errors.New("session service is not configured")
	}
	return s.store.GetExecutionProviderLifecycleState(sessionID, epochID)
}

func (s *Service) PutExecutionProviderLifecycleState(state pebblestore.ExecutionProviderLifecycleState) error {
	if s == nil || s.store == nil {
		return errors.New("session service is not configured")
	}
	return s.store.PutExecutionProviderLifecycleState(state)
}

func (s *Service) GetActiveExecutionEpoch(sessionID string) (pebblestore.ExecutionEpoch, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ExecutionEpoch{}, false, errors.New("session service is not configured")
	}
	return s.store.GetActiveExecutionEpoch(sessionID)
}

func (s *Service) SealExecutionEpoch(input pebblestore.SealExecutionEpochInput) (pebblestore.ExecutionEpoch, error) {
	if s == nil || s.store == nil {
		return pebblestore.ExecutionEpoch{}, errors.New("session service is not configured")
	}
	return s.store.SealExecutionEpoch(input)
}

func (s *Service) RepairActiveExecutionEpoch(sessionID, epochID string) (pebblestore.ExecutionEpoch, error) {
	if s == nil || s.store == nil {
		return pebblestore.ExecutionEpoch{}, errors.New("session service is not configured")
	}
	return s.store.RepairActiveExecutionEpoch(sessionID, epochID)
}

func (s *Service) GetV3SessionRunIntent(sessionID, runID string) (pebblestore.V3SessionRunIntent, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.V3SessionRunIntent{}, false, errors.New("session service is not configured")
	}
	return s.store.GetV3SessionRunIntent(sessionID, runID)
}

func (s *Service) CreateSession(title, workspacePath, workspaceName string) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	return s.CreateSessionWithOptions(CreateSessionOptions{
		Title:         title,
		WorkspacePath: workspacePath,
		WorkspaceName: workspaceName,
	})
}

func (s *Service) CreateSessionWithOptions(options CreateSessionOptions) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	title := strings.TrimSpace(options.Title)
	workspacePath := strings.TrimSpace(options.WorkspacePath)
	workspaceName := strings.TrimSpace(options.WorkspaceName)
	if workspacePath == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("workspace path is required")
	}
	if workspaceName == "" {
		workspaceName = filepathBaseSafe(workspacePath)
	}
	if title == "" {
		title = "New Session"
	}
	preference, err := normalizeSessionPreferenceValue(normalizeSessionPreference(options.Preference))
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("normalize session preference: %w", err)
	}
	if strings.TrimSpace(preference.Provider) == "" || strings.TrimSpace(preference.Model) == "" || strings.TrimSpace(preference.Thinking) == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("session execution preference is required")
	}

	now := time.Now().UnixMilli()
	userID := strings.TrimSpace(options.UserID)
	accountScopeID := strings.TrimSpace(options.AccountScopeID)
	sessionID := strings.TrimSpace(options.SessionID)
	if sessionID == "" {
		sessionID = s.newSessionID(now)
	}
	session := pebblestore.SessionSnapshot{
		ID:                      sessionID,
		UserID:                  userID,
		AccountScopeID:          accountScopeID,
		WorkspacePath:           workspacePath,
		WorkspaceName:           workspaceName,
		TemporaryWorkspaceRoots: nil,
		Title:                   title,
		Mode:                    NormalizeMode(options.Mode),
		Preference:              preference,
		Metadata:                cloneSessionMetadataMap(options.Metadata),
		CreatedAt:               now,
		UpdatedAt:               now,
		MessageCount:            0,
		LastMessageAt:           0,
	}
	if options.Worktree != nil {
		rootPath := strings.TrimSpace(options.Worktree.RootPath)
		baseBranch := strings.TrimSpace(options.Worktree.BaseBranch)
		branchName := strings.TrimSpace(options.Worktree.BranchName)
		workspaceID := strings.TrimSpace(options.Worktree.WorkspaceID)
		if rootPath != "" {
			session.WorktreeEnabled = true
			session.WorktreeRootPath = rootPath
			session.WorktreeBaseBranch = baseBranch
			session.WorktreeBranch = branchName
		}
		if workspaceID != "" {
			if session.Metadata == nil {
				session.Metadata = make(map[string]any, 4)
			}
			session.Metadata["workspace_id"] = workspaceID
		}
	}
	if err := s.store.CreateSession(session); err != nil {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("persist session: %w", err)
	}

	payload, err := json.Marshal(session)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	stream := "session:" + session.ID
	env, err := s.events.Append(stream, "session.created", session.ID, payload, "", "")
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	return session, &env, nil
}

func (s *Service) DeleteSession(sessionID string) error {
	_, err := s.DeleteSessionWithEvent(sessionID)
	return err
}

func (s *Service) DeleteSessionWithEvent(sessionID string) (*pebblestore.EventEnvelope, error) {
	return s.tombstoneSessionWithEvent(sessionID, "deleted")
}

func (s *Service) DeleteSessionsWithEvents(sessionIDs []string) ([]*pebblestore.EventEnvelope, error) {
	return s.deleteSessionsWithEventsIfUnchanged(sessionIDs, nil)
}

// DeleteSessionsWithEventsIfUnchanged deletes the sessions only when every
// snapshot still has the UpdatedAt observed by the caller's preview. The check
// and durable delete run under the service mutation lock, so a concurrent
// session mutation cannot slip between them.
func (s *Service) DeleteSessionsWithEventsIfUnchanged(sessionIDs []string, expectedUpdatedAt map[string]int64) ([]*pebblestore.EventEnvelope, error) {
	if len(expectedUpdatedAt) == 0 {
		return nil, errors.New("expected session versions are required")
	}
	return s.deleteSessionsWithEventsIfUnchanged(sessionIDs, expectedUpdatedAt)
}

func (s *Service) deleteSessionsWithEventsIfUnchanged(sessionIDs []string, expectedUpdatedAt map[string]int64) ([]*pebblestore.EventEnvelope, error) {
	return s.tombstoneSessionsWithEventsExpected(sessionIDs, "deleted", expectedUpdatedAt)
}

func (s *Service) ArchiveSession(sessionID string) error {
	_, err := s.ArchiveSessionWithEvent(sessionID)
	return err
}

func (s *Service) ArchiveSessionWithEvent(sessionID string) (*pebblestore.EventEnvelope, error) {
	events, err := s.ArchiveSessionsWithEvents([]string{sessionID})
	if err != nil || len(events) == 0 {
		return nil, err
	}
	return events[0], nil
}

func (s *Service) ArchiveSessionsWithEvents(sessionIDs []string) ([]*pebblestore.EventEnvelope, error) {
	return s.tombstoneSessionsWithEvents(sessionIDs, "archived")
}

// ArchiveSessionsWithEventsIfUnchanged archives all sessions only when every
// snapshot still has the UpdatedAt observed by the caller. The version checks
// and durable batch mutation run under the service mutation lock.
func (s *Service) ArchiveSessionsWithEventsIfUnchanged(sessionIDs []string, expectedUpdatedAt map[string]int64) ([]*pebblestore.EventEnvelope, error) {
	if len(expectedUpdatedAt) == 0 {
		return nil, errors.New("expected session versions are required")
	}
	return s.tombstoneSessionsWithEventsExpected(sessionIDs, "archived", expectedUpdatedAt)
}

// ReactivateArchivedSessionsIfUnchanged restores the entire archived batch only
// when every tombstone still has the mutation version observed by the caller.
func (s *Service) ReactivateArchivedSessionsIfUnchanged(sessionIDs []string, expectedUpdatedAt map[string]int64) error {
	if s == nil || s.store == nil {
		return errors.New("session service is not configured")
	}
	if len(expectedUpdatedAt) == 0 {
		return errors.New("expected tombstone versions are required")
	}
	normalizedIDs := make([]string, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return errors.New("session id is required")
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		normalizedIDs = append(normalizedIDs, sessionID)
	}
	if len(normalizedIDs) == 0 {
		return errors.New("at least one session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sessionID := range normalizedIDs {
		if _, active, err := s.store.GetSession(sessionID); err != nil {
			return err
		} else if active {
			return fmt.Errorf("session %q is active, not archived", sessionID)
		}
		tombstone, ok, err := s.store.GetV3SessionTombstone(sessionID)
		if err != nil {
			return err
		}
		expected, hasExpected := expectedUpdatedAt[sessionID]
		if !ok || !tombstone.Archived || tombstone.Deleted || tombstone.Session.ID == "" {
			return fmt.Errorf("session %q is not an archived, restorable session", sessionID)
		}
		if !hasExpected || expected == 0 || tombstone.UpdatedAt != expected {
			return fmt.Errorf("session %q changed after unarchive preview", sessionID)
		}
		if lifecycle := tombstone.Session.Lifecycle; lifecycle != nil && lifecycle.Active {
			return fmt.Errorf("cannot unarchive session %q with active run state", sessionID)
		}
	}
	return s.store.ReactivateArchivedSessions(normalizedIDs, expectedUpdatedAt)
}

func (s *Service) tombstoneSessionWithEvent(sessionID, kind string) (*pebblestore.EventEnvelope, error) {
	events, err := s.tombstoneSessionsWithEvents([]string{sessionID}, kind)
	if err != nil || len(events) == 0 {
		return nil, err
	}
	return events[0], nil
}

func (s *Service) tombstoneSessionsWithEvents(sessionIDs []string, kind string) ([]*pebblestore.EventEnvelope, error) {
	return s.tombstoneSessionsWithEventsExpected(sessionIDs, kind, nil)
}

func (s *Service) tombstoneSessionsWithEventsExpected(sessionIDs []string, kind string, expectedUpdatedAt map[string]int64) ([]*pebblestore.EventEnvelope, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session service is not configured")
	}
	kind = strings.TrimSpace(strings.ToLower(kind))
	if kind != "deleted" && kind != "archived" {
		return nil, fmt.Errorf("unsupported session tombstone kind %q", kind)
	}
	normalizedIDs := make([]string, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return nil, errors.New("session id is required")
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		normalizedIDs = append(normalizedIDs, sessionID)
	}
	if len(normalizedIDs) == 0 {
		return nil, errors.New("at least one session id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	sessions := make([]pebblestore.SessionSnapshot, 0, len(normalizedIDs))
	for _, sessionID := range normalizedIDs {
		session, found, err := s.store.GetSession(sessionID)
		if err != nil {
			return nil, err
		}
		if found {
			if expectedUpdatedAt != nil {
				expected, exists := expectedUpdatedAt[sessionID]
				if !exists || session.UpdatedAt != expected {
					return nil, fmt.Errorf("session %q changed after mutation preview", sessionID)
				}
			}
			sessions = append(sessions, session)
		} else if kind == "deleted" {
			tombstone, tombstoneOK, tombstoneErr := s.store.GetV3SessionTombstone(sessionID)
			if tombstoneErr != nil {
				return nil, tombstoneErr
			}
			if tombstoneOK && tombstone.Archived && !tombstone.Deleted {
				if expectedUpdatedAt != nil {
					expected, exists := expectedUpdatedAt[sessionID]
					if !exists || tombstone.Session.UpdatedAt != expected {
						return nil, fmt.Errorf("session %q changed after mutation preview", sessionID)
					}
				}
				sessions = append(sessions, tombstone.Session)
			}
		}
	}
	if kind == "archived" {
		err := s.store.ArchiveSessions(normalizedIDs)
		if err != nil {
			return nil, err
		}
	} else {
		if err := s.store.DeleteSessions(normalizedIDs); err != nil {
			return nil, err
		}
	}
	if len(sessions) == 0 || s.events == nil {
		return nil, nil
	}
	eventType := "session.deleted"
	if kind == "archived" {
		eventType = "session.archived"
	}
	appends := make([]pebblestore.EventAppend, 0, len(sessions))
	for _, session := range sessions {
		payload, err := json.Marshal(session)
		if err != nil {
			return nil, err
		}
		appends = append(appends, pebblestore.EventAppend{Stream: "session:" + session.ID, EventType: eventType, EntityID: session.ID, Payload: payload, Source: "v3"})
	}
	envelopes, err := s.events.AppendBatch(appends)
	if err != nil {
		return nil, err
	}
	events := make([]*pebblestore.EventEnvelope, len(envelopes))
	for i := range envelopes {
		events[i] = &envelopes[i]
	}
	return events, nil
}

func (s *Service) SetWorktreeBranch(sessionID, branch string) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	branch = strings.TrimSpace(branch)
	if sessionID == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("session id is required")
	}
	if branch == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("branch is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	session.Mode = NormalizeMode(session.Mode)
	if !session.WorktreeEnabled {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q is not worktree-enabled", sessionID)
	}
	if strings.EqualFold(strings.TrimSpace(session.WorktreeBranch), branch) {
		return session, nil, nil
	}
	session.WorktreeBranch = branch
	session.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.UpdateSession(session); err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}

	if s.events == nil {
		return session, nil, nil
	}
	payload, err := json.Marshal(map[string]any{
		"session_id": sessionID,
		"branch":     branch,
		"updated_at": session.UpdatedAt,
	})
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.branch.updated", sessionID, payload, "", "")
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	return session, &env, nil
}

func (s *Service) UpdateMetadata(sessionID string, metadata map[string]any) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	return s.updateMetadata(sessionID, metadata, false)
}

func (s *Service) UpdateDerivedMetadata(sessionID string, metadata map[string]any) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	return s.updateMetadata(sessionID, metadata, true)
}

func (s *Service) updateMetadata(sessionID string, metadata map[string]any, preserveUpdatedAt bool) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("session id is required")
	}

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err = s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	cleanMetadata := cloneSessionMetadataMap(metadata)
	session.Metadata = cleanMetadata
	if !preserveUpdatedAt {
		session.UpdatedAt = time.Now().UnixMilli()
	}
	if err := s.store.UpdateSession(session); err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}

	if s.events == nil {
		return session, nil, nil
	}
	payload, err := json.Marshal(map[string]any{
		"session_id": sessionID,
		"metadata":   cleanMetadata,
		"updated_at": session.UpdatedAt,
	})
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.metadata.updated", sessionID, payload, "", "")
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	return session, &env, nil
}

func (s *Service) SetWorkspacePath(sessionID, workspacePath string) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	workspacePath = strings.TrimSpace(workspacePath)
	if sessionID == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("session id is required")
	}
	if workspacePath == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("workspace path is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	session.Mode = NormalizeMode(session.Mode)
	if strings.TrimSpace(session.WorkspacePath) == workspacePath {
		return session, nil, nil
	}

	session.WorkspacePath = workspacePath
	session.TemporaryWorkspaceRoots = nil
	session.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.UpdateSession(session); err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}

	if s.events == nil {
		return session, nil, nil
	}
	payload, err := json.Marshal(map[string]any{
		"session_id":     sessionID,
		"workspace_path": workspacePath,
		"updated_at":     session.UpdatedAt,
	})
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.workspace.updated", sessionID, payload, "", "")
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	return session, &env, nil
}

func (s *Service) AddTemporaryWorkspaceRoot(sessionID, root string) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	root = strings.TrimSpace(root)
	if sessionID == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("session id is required")
	}
	if root == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("workspace root is required")
	}

	resolvedRoot, err := normalizeSessionWorkspaceRoot(root)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	session.Mode = NormalizeMode(session.Mode)
	roots := append([]string(nil), session.TemporaryWorkspaceRoots...)
	roots = append(roots, resolvedRoot)
	session.TemporaryWorkspaceRoots = pebblestore.NormalizeSessionTemporaryWorkspaceRoots(session.WorkspacePath, roots)
	session.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.UpdateSession(session); err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	return session, nil, nil
}

func (s *Service) GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionSnapshot{}, false, errors.New("session id is required")
	}
	session, ok, err := s.store.GetSession(sessionID)
	if err != nil || !ok {
		return session, ok, err
	}
	session.Mode = NormalizeMode(session.Mode)
	return session, true, nil
}

func (s *Service) GetLifecycle(sessionID string) (pebblestore.SessionLifecycleSnapshot, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionLifecycleSnapshot{}, false, errors.New("session id is required")
	}
	return s.store.GetSessionLifecycle(sessionID)
}

func (s *Service) UpsertLifecycle(snapshot pebblestore.SessionLifecycleSnapshot) error {
	snapshot.SessionID = strings.TrimSpace(snapshot.SessionID)
	if snapshot.SessionID == "" {
		return errors.New("session id is required")
	}
	return s.store.UpsertSessionLifecycle(snapshot)
}

func (s *Service) ListActiveLifecycles(limit int) ([]pebblestore.SessionLifecycleSnapshot, error) {
	return s.store.ListActiveSessionLifecycles(limit)
}

func (s *Service) ListSessions(limit int) ([]pebblestore.SessionSnapshot, error) {
	sessions, err := s.store.ListSessions(limit)
	if err != nil {
		return nil, err
	}
	sessions = normalizeVisibleSessionList(sessions)
	return sessions, nil
}

func (s *Service) GetSessionLibraryMetric(sessionID string) (pebblestore.V3SessionLibraryMetric, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.V3SessionLibraryMetric{}, false, errors.New("session store is not configured")
	}
	return s.store.GetV3SessionLibraryMetric(sessionID)
}

func (s *Service) ListSessionsForAccount(accountScopeID string, limit int) ([]pebblestore.SessionSnapshot, error) {
	sessions, err := s.store.ListSessionsForAccount(accountScopeID, limit)
	if err != nil {
		return nil, err
	}
	sessions = normalizeVisibleSessionList(sessions)
	return sessions, nil
}

func (s *Service) ListSessionsForPath(path string, limit int) ([]pebblestore.SessionSnapshot, error) {
	sessions, err := s.store.ListSessionsForPath(path, limit)
	if err != nil {
		return nil, err
	}
	sessions = normalizeVisibleSessionList(sessions)
	return sessions, nil
}

func (s *Service) ListSessionsForAccountPath(accountScopeID, path string, limit int) ([]pebblestore.SessionSnapshot, error) {
	sessions, err := s.store.ListSessionsForAccountPath(accountScopeID, path, limit)
	if err != nil {
		return nil, err
	}
	sessions = normalizeVisibleSessionList(sessions)
	return sessions, nil
}

func (s *Service) ListSessionsForScope(scopePath string, limit int) ([]pebblestore.SessionSnapshot, error) {
	sessions, err := s.store.ListSessionsForScope(scopePath, limit)
	if err != nil {
		return nil, err
	}
	sessions = normalizeVisibleSessionList(sessions)
	return sessions, nil
}

func (s *Service) ListSessionsForAccountScope(accountScopeID, scopePath string, limit int) ([]pebblestore.SessionSnapshot, error) {
	sessions, err := s.store.ListSessionsForAccountScope(accountScopeID, scopePath, limit)
	if err != nil {
		return nil, err
	}
	sessions = normalizeVisibleSessionList(sessions)
	return sessions, nil
}

func (s *Service) ListSessionsForAccountWorkspaceBindings(accountScopeID, sourceWorkspaceID string, workspaceBindingIDs []string, fallbackScopePath string, limit int) ([]pebblestore.SessionSnapshot, error) {
	sessions, err := s.store.ListSessionsForAccountWorkspaceBindings(accountScopeID, sourceWorkspaceID, workspaceBindingIDs, fallbackScopePath, limit)
	if err != nil {
		return nil, err
	}
	sessions = normalizeVisibleSessionList(sessions)
	return sessions, nil
}

func normalizeVisibleSessionList(sessions []pebblestore.SessionSnapshot) []pebblestore.SessionSnapshot {
	visible := sessions[:0]
	for i := range sessions {
		sessions[i].Mode = NormalizeMode(sessions[i].Mode)
		if hidden, _ := sessions[i].Metadata["navigation_hidden"].(bool); hidden {
			continue
		}
		visible = append(visible, sessions[i])
	}
	return visible
}

func normalizeSessionListModes(sessions []pebblestore.SessionSnapshot) {
	for i := range sessions {
		sessions[i].Mode = NormalizeMode(sessions[i].Mode)
	}
}

func (s *Service) ListTopSessionsByWorkspace(workspacePaths []string, perWorkspaceLimit int) ([]pebblestore.WorkspaceSessionList, error) {
	groups, err := s.store.ListTopSessionsByWorkspace(workspacePaths, perWorkspaceLimit)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		for j := range groups[i].Sessions {
			groups[i].Sessions[j].Mode = NormalizeMode(groups[i].Sessions[j].Mode)
		}
	}
	return groups, nil
}

func (s *Service) GetSessionPreference(sessionID string) (pebblestore.ModelPreference, error) {
	session, ok, err := s.GetSession(sessionID)
	if err != nil {
		return pebblestore.ModelPreference{}, err
	}
	if !ok {
		return pebblestore.ModelPreference{}, fmt.Errorf("session %q not found", strings.TrimSpace(sessionID))
	}
	return normalizeStoredSessionPreference(session.Preference), nil
}

func (s *Service) SetSessionPreference(sessionID string, update SessionPreferenceUpdate) (pebblestore.ModelPreference, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.ModelPreference{}, nil, errors.New("session id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.ModelPreference{}, nil, err
	}
	if !ok {
		return pebblestore.ModelPreference{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	session.Mode = NormalizeMode(session.Mode)

	next := normalizeStoredSessionPreference(session.Preference)
	if update.Provider != nil {
		next.Provider = strings.ToLower(strings.TrimSpace(*update.Provider))
	}
	if update.Model != nil {
		next.Model = strings.TrimSpace(*update.Model)
	}
	if update.Thinking != nil {
		next.Thinking = strings.ToLower(strings.TrimSpace(*update.Thinking))
	}
	if update.ServiceTier != nil {
		next.ServiceTier = modelruntime.NormalizeServiceTierForProvider(next.Provider, *update.ServiceTier)
		if strings.TrimSpace(*update.ServiceTier) != "" && !strings.EqualFold(strings.TrimSpace(*update.ServiceTier), "standard") && next.ServiceTier == "" {
			return pebblestore.ModelPreference{}, nil, fmt.Errorf("invalid service tier %q", *update.ServiceTier)
		}
	}
	if update.ContextMode != nil {
		next.ContextMode = codexruntime.NormalizeContextMode(*update.ContextMode)
		if strings.TrimSpace(*update.ContextMode) != "" && next.ContextMode == "" {
			return pebblestore.ModelPreference{}, nil, fmt.Errorf("invalid codex context mode %q", *update.ContextMode)
		}
	}

	normalized, err := normalizeSessionPreferenceValue(next)
	if err != nil {
		return pebblestore.ModelPreference{}, nil, err
	}
	if sessionPreferenceEqual(session.Preference, normalized) {
		return normalized, nil, nil
	}

	session.Preference = normalized
	session.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.UpdateSession(session); err != nil {
		return pebblestore.ModelPreference{}, nil, err
	}

	payload, err := json.Marshal(map[string]any{
		"session_id": sessionID,
		"preference": normalized,
		"updated_at": session.UpdatedAt,
	})
	if err != nil {
		return pebblestore.ModelPreference{}, nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.preference.updated", sessionID, payload, "", "")
	if err != nil {
		return pebblestore.ModelPreference{}, nil, err
	}
	return normalized, &env, nil
}

func normalizeSessionPreference(input *pebblestore.ModelPreference) pebblestore.ModelPreference {
	if input == nil {
		return pebblestore.ModelPreference{}
	}
	return normalizeStoredSessionPreference(*input)
}

func normalizeStoredSessionPreference(pref pebblestore.ModelPreference) pebblestore.ModelPreference {
	pref.Provider = modelruntime.NormalizeProviderID(pref.Provider)
	pref.Model = strings.TrimSpace(pref.Model)
	pref.Thinking = strings.ToLower(strings.TrimSpace(pref.Thinking))
	pref.ServiceTier = modelruntime.NormalizeServiceTierForProvider(pref.Provider, pref.ServiceTier)
	pref.ContextMode = codexruntime.NormalizeContextMode(pref.ContextMode)
	if strings.TrimSpace(pref.Provider) == "" || strings.TrimSpace(pref.Model) == "" {
		pref.Provider = ""
		pref.Model = ""
		pref.Thinking = ""
		pref.ServiceTier = ""
		pref.ContextMode = ""
		pref.UpdatedAt = 0
	}
	return pref
}

func NormalizeSessionPreferenceValue(pref pebblestore.ModelPreference) (pebblestore.ModelPreference, error) {
	return normalizeSessionPreferenceValue(pref)
}

func normalizeSessionPreferenceValue(pref pebblestore.ModelPreference) (pebblestore.ModelPreference, error) {
	pref = normalizeStoredSessionPreference(pref)
	if pref.Provider == "" && pref.Model == "" {
		return pref, nil
	}
	if pref.Provider == "" {
		return pebblestore.ModelPreference{}, errors.New("session provider is required when model is set")
	}
	if pref.Model == "" {
		return pebblestore.ModelPreference{}, errors.New("session model is required when provider is set")
	}
	if pref.Thinking == "" {
		return pebblestore.ModelPreference{}, errors.New("session thinking is required when provider/model is set")
	}
	if !modelruntime.IsAllowedThinkingLevel(pref.Thinking) {
		return pebblestore.ModelPreference{}, fmt.Errorf("invalid thinking level %q", pref.Thinking)
	}
	pref.Thinking = modelruntime.NormalizeThinkingForProvider(pref.Provider, pref.Thinking)
	if !supportsCodexContextRuntime(pref.Provider, pref.Model) {
		pref.ContextMode = ""
	}
	pref.UpdatedAt = time.Now().UnixMilli()
	return pref, nil
}

func supportsCodexContextRuntime(provider, modelName string) bool {
	return strings.EqualFold(provider, "codex") && strings.EqualFold(modelName, "gpt-5.4")
}

func sessionPreferenceEqual(left, right pebblestore.ModelPreference) bool {
	left = normalizeStoredSessionPreference(left)
	right = normalizeStoredSessionPreference(right)
	return left.Provider == right.Provider && left.Model == right.Model && left.Thinking == right.Thinking && left.ServiceTier == right.ServiceTier && left.ContextMode == right.ContextMode
}

func (s *Service) GetMode(sessionID string) (string, error) {
	session, ok, err := s.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", strings.TrimSpace(sessionID))
	}
	return NormalizeMode(session.Mode), nil
}

func (s *Service) SetMode(sessionID, mode string) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("session id is required")
	}
	rawMode := strings.ToLower(strings.TrimSpace(mode))
	if !IsValidMode(rawMode) {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("invalid mode %q (expected %q or %q)", mode, ModePlan, ModeAuto)
	}
	mode = NormalizeMode(rawMode)

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err = s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	session.Mode = NormalizeMode(session.Mode)
	previousMode := session.Mode
	if previousMode == mode {
		return session, nil, nil
	}
	session.Mode = mode
	session.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.UpdateSession(session); err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}

	payload, err := json.Marshal(map[string]any{
		"session_id": sessionID,
		"mode":       mode,
		"updated_at": session.UpdatedAt,
	})
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.mode.updated", sessionID, payload, "", "")
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	if message, ok := modeTransitionSystemMessage(previousMode, mode); ok {
		if session, err = s.appendSystemMessageLocked(session, message, map[string]any{
			"source":        "session_mode_transition",
			"previous_mode": previousMode,
			"mode":          mode,
		}); err != nil {
			return pebblestore.SessionSnapshot{}, nil, err
		}
	}
	return session, &env, nil
}

func modeTransitionSystemMessage(previousMode, mode string) (string, bool) {
	previousMode = NormalizeMode(previousMode)
	mode = NormalizeMode(mode)
	if previousMode == mode {
		return "", false
	}
	if mode == ModePlan {
		return planModeReentrySystemMessage, true
	}
	if previousMode == ModePlan && mode == ModeAuto {
		return autoModeReentrySystemMessage, true
	}
	return "", false
}

func (s *Service) appendSystemMessageLocked(session pebblestore.SessionSnapshot, content string, metadata map[string]any) (pebblestore.SessionSnapshot, error) {
	sessionID := strings.TrimSpace(session.ID)
	content = strings.TrimSpace(content)
	if sessionID == "" {
		return pebblestore.SessionSnapshot{}, errors.New("session id is required")
	}
	if content == "" {
		return pebblestore.SessionSnapshot{}, errors.New("message content is required")
	}
	cleanMetadata := cloneSessionMetadataMap(metadata)
	payload := map[string]any{
		"session_id": sessionID,
		"role":       "system",
		"content":    content,
	}
	if len(cleanMetadata) > 0 {
		payload["metadata"] = cleanMetadata
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.message.appended", sessionID, payloadBytes, "", "")
	if err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	message := pebblestore.MessageSnapshot{
		ID:             fmt.Sprintf("msg_%020d", env.GlobalSeq),
		SessionID:      sessionID,
		UserID:         session.UserID,
		AccountScopeID: session.AccountScopeID,
		GlobalSeq:      env.GlobalSeq,
		Role:           "system",
		Content:        content,
		Metadata:       cleanMetadata,
		CreatedAt:      env.TsUnixMs,
	}
	if err := s.store.PutMessage(message); err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	session.MessageCount++
	session.UpdatedAt = env.TsUnixMs
	session.LastMessageAt = env.TsUnixMs
	if err := s.store.UpdateSession(session); err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	return session, nil
}

func (s *Service) GetCodexConfig(sessionID string) (pebblestore.ModelPreference, error) {
	session, ok, err := s.GetSession(sessionID)
	if err != nil {
		return pebblestore.ModelPreference{}, err
	}
	if !ok {
		return pebblestore.ModelPreference{}, fmt.Errorf("session %q not found", strings.TrimSpace(sessionID))
	}
	return normalizeStoredSessionPreference(session.Preference), nil
}

func (s *Service) SetCodexConfig(sessionID string, update SessionCodexConfigUpdate) (pebblestore.ModelPreference, *pebblestore.EventEnvelope, error) {
	return s.SetSessionPreference(sessionID, update)
}

func (s *Service) SetTitle(sessionID, title string) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("session id is required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return pebblestore.SessionSnapshot{}, nil, errors.New("title is required")
	}

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err = s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	session.Mode = NormalizeMode(session.Mode)
	if strings.TrimSpace(session.Title) == title {
		return session, nil, nil
	}
	session.Title = title
	session.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.UpdateSession(session); err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}

	if s.events == nil {
		return session, nil, nil
	}
	payload, err := json.Marshal(map[string]any{
		"session_id": sessionID,
		"title":      title,
		"updated_at": session.UpdatedAt,
	})
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.title.updated", sessionID, payload, "", "")
	if err != nil {
		return pebblestore.SessionSnapshot{}, nil, err
	}
	return session, &env, nil
}

func (s *Service) RecordTitleWarning(sessionID, stage, warning string) (*pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	stage = strings.ToLower(strings.TrimSpace(stage))
	warning = strings.TrimSpace(warning)
	if warning == "" {
		return nil, errors.New("warning is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok, err := s.store.GetSession(sessionID); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	if s.events == nil {
		return nil, nil
	}
	now := time.Now().UnixMilli()
	payload, err := json.Marshal(map[string]any{
		"session_id": sessionID,
		"stage":      stage,
		"warning":    warning,
		"updated_at": now,
	})
	if err != nil {
		return nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.title.warning", sessionID, payload, "", "")
	if err != nil {
		return nil, err
	}
	return &env, nil
}

func (s *Service) AppendMessage(sessionID, role, content string, metadata map[string]any) (pebblestore.MessageSnapshot, pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	role = strings.ToLower(strings.TrimSpace(role))
	content = strings.TrimSpace(content)
	if sessionID == "" {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, errors.New("session id is required")
	}
	if !isAllowedRole(role) {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, fmt.Errorf("invalid role %q", role)
	}
	if content == "" {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, errors.New("message content is required")
	}

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err = s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	session.Mode = NormalizeMode(session.Mode)

	cleanMetadata := cloneSessionMetadataMap(metadata)
	payload := map[string]any{
		"session_id": sessionID,
		"role":       role,
		"content":    content,
	}
	if len(cleanMetadata) > 0 {
		payload["metadata"] = cleanMetadata
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.message.appended", sessionID, payloadBytes, "", "")
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}

	message := pebblestore.MessageSnapshot{
		ID:             fmt.Sprintf("msg_%020d", env.GlobalSeq),
		SessionID:      sessionID,
		UserID:         session.UserID,
		AccountScopeID: session.AccountScopeID,
		GlobalSeq:      env.GlobalSeq,
		Role:           role,
		Content:        content,
		Metadata:       cleanMetadata,
		CreatedAt:      env.TsUnixMs,
	}
	if err := s.store.PutMessage(message); err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}

	session.MessageCount++
	session.UpdatedAt = env.TsUnixMs
	session.LastMessageAt = env.TsUnixMs
	if err := s.store.UpdateSession(session); err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}
	return message, session, &env, nil
}

func (s *Service) UpdateMessage(sessionID string, globalSeq uint64, content string) (pebblestore.MessageSnapshot, pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	content = strings.TrimSpace(content)
	if sessionID == "" {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, errors.New("session id is required")
	}
	if globalSeq == 0 {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, errors.New("message global seq is required")
	}
	if content == "" {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, errors.New("message content is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	session.Mode = NormalizeMode(session.Mode)

	message, ok, err := s.store.GetMessage(sessionID, globalSeq)
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, fmt.Errorf("message %d not found for session %q", globalSeq, sessionID)
	}
	if strings.TrimSpace(message.Content) == content {
		return message, session, nil, nil
	}

	message.Content = content
	payloadBytes, err := json.Marshal(map[string]any{
		"session_id": sessionID,
		"message":    message,
		"updated_at": time.Now().UnixMilli(),
	})
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.message.updated", sessionID, payloadBytes, "", "")
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}

	if err := s.store.PutMessage(message); err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}

	session.UpdatedAt = env.TsUnixMs
	session.LastMessageAt = env.TsUnixMs
	if err := s.store.UpdateSession(session); err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}
	return message, session, &env, nil
}

func cloneSessionMetadataMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = cloneSessionMetadataValue(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mapsEqualJSON(left, right map[string]any) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftBytes) == string(rightBytes)
}

func cloneSessionMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneSessionMetadataMap(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneSessionMetadataValue(item))
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneSessionMetadataMap(item))
		}
		return out
	default:
		return value
	}
}

func (s *Service) ListMessages(sessionID string, afterGlobalSeq uint64, limit int) ([]pebblestore.MessageSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	return s.store.ListMessages(sessionID, afterGlobalSeq, limit)
}

func (s *Service) RecordTurnUsage(sessionID string, usage pebblestore.SessionTurnUsageSnapshot) (pebblestore.SessionTurnUsageSnapshot, pebblestore.SessionUsageSummary, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, errors.New("session id is required")
	}
	usage.RunID = strings.TrimSpace(usage.RunID)
	if usage.RunID == "" {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, errors.New("run id is required")
	}
	usage.Provider = strings.ToLower(strings.TrimSpace(usage.Provider))
	usage.Model = strings.TrimSpace(usage.Model)
	usage.Source = strings.TrimSpace(usage.Source)
	normalizeTurnUsage(&usage)

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, err
	}
	if !ok {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, fmt.Errorf("session %q not found", sessionID)
	}

	now := time.Now().UnixMilli()
	previous, hadPrevious, err := s.store.GetTurnUsage(sessionID, usage.RunID)
	if err != nil {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, err
	}
	summary, hasSummary, err := s.store.GetUsageSummary(sessionID)
	if err != nil {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, err
	}
	if !hasSummary {
		summary = pebblestore.SessionUsageSummary{SessionID: sessionID}
	}

	if !hadPrevious {
		summary.TurnCount++
	}

	usage.SessionID = sessionID
	if strings.TrimSpace(usage.UserID) == "" {
		usage.UserID = session.UserID
	}
	if strings.TrimSpace(usage.AccountScopeID) == "" {
		usage.AccountScopeID = session.AccountScopeID
	}
	if usage.CreatedAt <= 0 {
		if hadPrevious && previous.CreatedAt > 0 {
			usage.CreatedAt = previous.CreatedAt
		} else {
			usage.CreatedAt = now
		}
	}
	usage.UpdatedAt = now

	if usage.ContextWindow > 0 {
		summary.ContextWindow = usage.ContextWindow
	} else if summary.ContextWindow > 0 {
		usage.ContextWindow = summary.ContextWindow
	}
	summary.SessionID = sessionID
	if strings.TrimSpace(summary.UserID) == "" {
		summary.UserID = usage.UserID
	}
	if strings.TrimSpace(summary.AccountScopeID) == "" {
		summary.AccountScopeID = usage.AccountScopeID
	}
	if usage.Provider != "" {
		summary.Provider = usage.Provider
	}
	if usage.Model != "" {
		summary.Model = usage.Model
	}
	if usage.Source != "" {
		summary.Source = usage.Source
	}
	if usage.ServiceTier != "" {
		summary.ServiceTier = usage.ServiceTier
	}
	summary.EstimatedCostUSD += usage.EstimatedCostUSD
	if hadPrevious {
		summary.EstimatedCostUSD -= previous.EstimatedCostUSD
		if summary.EstimatedCostUSD < 0 {
			summary.EstimatedCostUSD = 0
		}
	}
	summary.LastTransport = usage.Transport
	if usage.ConnectedViaWS != nil {
		summary.LastConnectedViaWS = boolPointer(*usage.ConnectedViaWS)
	} else {
		summary.LastConnectedViaWS = nil
	}
	summary.LastRunID = usage.RunID
	summary.UpdatedAt = now
	if hadPrevious {
		summary = pebblestore.ApplyProviderUsageSnapshotReplacementToSummary(summary, previous, usage)
	} else {
		summary = pebblestore.ApplyProviderUsageSnapshotToSummary(summary, usage)
	}
	normalizeUsageSummary(&summary)

	if err := s.store.PutTurnUsage(usage); err != nil {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, err
	}
	if err := s.store.PutUsageSummary(summary); err != nil {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, err
	}

	if s.events == nil {
		return usage, summary, nil, nil
	}
	payload, err := json.Marshal(map[string]any{
		"session_id":  sessionID,
		"run_id":      usage.RunID,
		"turn_usage":  usage,
		"usage_state": summary,
	})
	if err != nil {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.usage.recorded", sessionID, payload, "", "")
	if err != nil {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, err
	}
	return usage, summary, &env, nil
}

func (s *Service) ResetUsage(sessionID string, contextWindow int, provider, model, source string) (pebblestore.SessionUsageSummary, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionUsageSummary{}, nil, errors.New("session id is required")
	}
	if contextWindow < 0 {
		contextWindow = 0
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	source = strings.TrimSpace(source)

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionUsageSummary{}, nil, err
	}
	if !ok {
		return pebblestore.SessionUsageSummary{}, nil, fmt.Errorf("session %q not found", sessionID)
	}

	now := time.Now().UnixMilli()
	summary := pebblestore.SessionUsageSummary{
		SessionID:       sessionID,
		UserID:          session.UserID,
		AccountScopeID:  session.AccountScopeID,
		Provider:        provider,
		Model:           model,
		Source:          source,
		ContextWindow:   contextWindow,
		RemainingTokens: int64(contextWindow),
		UpdatedAt:       now,
	}
	normalizeUsageSummary(&summary)
	if err := s.store.ResetUsage(sessionID, summary); err != nil {
		return pebblestore.SessionUsageSummary{}, nil, err
	}

	if s.events == nil {
		return summary, nil, nil
	}
	payload, err := json.Marshal(map[string]any{
		"session_id":  sessionID,
		"usage_state": summary,
		"updated_at":  now,
	})
	if err != nil {
		return pebblestore.SessionUsageSummary{}, nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.usage.reset", sessionID, payload, "", "")
	if err != nil {
		return pebblestore.SessionUsageSummary{}, nil, err
	}
	return summary, &env, nil
}

func (s *Service) GetUsageSummary(sessionID string) (pebblestore.SessionUsageSummary, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionUsageSummary{}, false, errors.New("session id is required")
	}
	if _, ok, err := s.store.GetSession(sessionID); err != nil {
		return pebblestore.SessionUsageSummary{}, false, err
	} else if !ok {
		return pebblestore.SessionUsageSummary{}, false, fmt.Errorf("session %q not found", sessionID)
	}
	return s.store.GetUsageSummary(sessionID)
}

func (s *Service) ListTurnUsage(sessionID string, limit int) ([]pebblestore.SessionTurnUsageSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if _, ok, err := s.store.GetSession(sessionID); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	return s.store.ListTurnUsage(sessionID, limit)
}

type PlanSaveMetadata struct {
	UpdateSummary       string
	UpdateScope         string
	UpdateKind          string
	RevisionKind        string
	RestoredFromVersion int
	Checkpoint          bool
	Document            *pebblestore.SessionPlanDocument
}

type PlanPatchOptions struct {
	PlanID        string
	Title         string
	Status        string
	ApprovalState string
	Activate      *bool
	Patch         PlanPatch
	Document      *pebblestore.SessionPlanDocument
	DocumentPatch *PlanDocumentPatch
	Metadata      PlanSaveMetadata
}

func (s *Service) SavePlan(sessionID, planID, title, plan, status, approvalState string, activate bool) (pebblestore.SessionPlanSnapshot, *pebblestore.EventEnvelope, error) {
	return s.SavePlanWithMetadata(sessionID, planID, title, plan, status, approvalState, activate, PlanSaveMetadata{})
}

const (
	PlanRevisionKindDefinition = "definition"
	PlanRevisionKindExecution  = "execution"
)

func classifyPlanRevisionKind(metadata PlanSaveMetadata) string {
	kind := strings.ToLower(strings.TrimSpace(metadata.RevisionKind))
	switch kind {
	case PlanRevisionKindDefinition, "plan", "plan_definition", "whole_plan", "whole-plan":
		return PlanRevisionKindDefinition
	case PlanRevisionKindExecution, "checkpoint", "checkpoint_execution", "progress", "runtime", "snapshot":
		return PlanRevisionKindExecution
	}
	if metadata.Checkpoint {
		return PlanRevisionKindExecution
	}
	updateKind := strings.ToLower(strings.TrimSpace(metadata.UpdateKind))
	if updateKind == "" {
		return PlanRevisionKindDefinition
	}
	if isExecutionPlanUpdateKind(updateKind) {
		return PlanRevisionKindExecution
	}
	return PlanRevisionKindDefinition
}

func classifyPlanDocumentPatchRevisionKind(patch PlanDocumentPatch) string {
	operations := patch.Operations
	if len(operations) == 0 {
		operations = []PlanDocumentPatchOperation{{Operation: patch.Operation}}
	}
	for _, operation := range operations {
		if isExecutionPlanUpdateKind(strings.ToLower(strings.TrimSpace(operation.Operation))) {
			return PlanRevisionKindExecution
		}
	}
	return PlanRevisionKindDefinition
}

func isExecutionPlanUpdateKind(updateKind string) bool {
	return strings.Contains(updateKind, "checkpoint") || strings.Contains(updateKind, "execution") || strings.HasPrefix(updateKind, "start_") || strings.HasPrefix(updateKind, "continue_") || strings.HasPrefix(updateKind, "mark_") || strings.HasPrefix(updateKind, "accept_checkpoint") || updateKind == "pause_plan_run" || updateKind == "stop_plan_run" || updateKind == "resume_automatic" || updateKind == "resume_checkpointed"
}

func (s *Service) SavePlanWithMetadata(sessionID, planID, title, plan, status, approvalState string, activate bool, metadata PlanSaveMetadata) (pebblestore.SessionPlanSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	planID = strings.TrimSpace(planID)
	title = strings.TrimSpace(title)
	plan = strings.TrimSpace(plan)
	status = strings.ToLower(strings.TrimSpace(status))
	approvalState = strings.ToLower(strings.TrimSpace(approvalState))
	metadata.UpdateSummary = strings.TrimSpace(metadata.UpdateSummary)
	metadata.UpdateScope = strings.TrimSpace(metadata.UpdateScope)
	metadata.UpdateKind = strings.TrimSpace(metadata.UpdateKind)
	metadata.RevisionKind = classifyPlanRevisionKind(metadata)

	if sessionID == "" {
		return pebblestore.SessionPlanSnapshot{}, nil, errors.New("session id is required")
	}
	if title == "" {
		if metadata.Document != nil && strings.TrimSpace(metadata.Document.Title) != "" {
			title = strings.TrimSpace(metadata.Document.Title)
		} else {
			title = "Plan"
		}
	}
	if status == "" {
		status = "draft"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.SessionPlanSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}

	now := time.Now().UnixMilli()
	if planID == "" {
		if metadata.Document != nil && strings.TrimSpace(metadata.Document.ID) != "" {
			planID = strings.TrimSpace(metadata.Document.ID)
		} else {
			planID = s.newPlanID(now)
		}
	}

	existing, found, err := s.store.GetPlan(sessionID, planID)
	if err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	}
	record := pebblestore.SessionPlanSnapshot{
		ID:                  planID,
		SessionID:           sessionID,
		UserID:              session.UserID,
		AccountScopeID:      session.AccountScopeID,
		Title:               title,
		Plan:                plan,
		Status:              status,
		ApprovalState:       approvalState,
		Active:              false,
		CreatedAt:           now,
		UpdatedAt:           now,
		UpdateSummary:       metadata.UpdateSummary,
		UpdateScope:         metadata.UpdateScope,
		UpdateKind:          metadata.UpdateKind,
		RevisionKind:        metadata.RevisionKind,
		RestoredFromVersion: metadata.RestoredFromVersion,
		Checkpoint:          metadata.Checkpoint,
		Version:             1,
	}
	if found {
		if plan == "" && metadata.Document != nil {
			plan = existing.Plan
			record.Plan = existing.Plan
		}
		record.CreatedAt = existing.CreatedAt
		record.Document, err = NormalizePlanDocumentForSave(planID, title, metadata.Document, existing.Document)
		if err != nil {
			return pebblestore.SessionPlanSnapshot{}, nil, err
		}
		if record.UserID == "" {
			record.UserID = existing.UserID
		}
		if record.AccountScopeID == "" {
			record.AccountScopeID = existing.AccountScopeID
		}
		record.PriorTitle = existing.Title
		record.PriorPlan = existing.Plan
		record.DiffLines = BuildPlanDiffLines(existing.Plan, plan)
		if existing.Version <= 0 {
			existing.Version = 1
		}
		record.Version = existing.Version + 1
		record.ParentRevision = existing.Version
	}
	if !found {
		record.Document, err = NormalizePlanDocumentForSave(planID, title, metadata.Document, nil)
		if err != nil {
			return pebblestore.SessionPlanSnapshot{}, nil, err
		}
	}
	if record.Document != nil {
		record.Document.RevisionID = fmt.Sprintf("%s:v%d", planID, record.Version)
	}
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}

	if found {
		archived := existing
		archived.Active = false
		if archived.Version <= 0 {
			archived.Version = 1
		}
		if err := s.store.PutPlanWithArchivedRevision(record, archived); err != nil {
			return pebblestore.SessionPlanSnapshot{}, nil, err
		}
	} else if err := s.store.PutPlan(record); err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	}
	if activate {
		if err := s.store.SetActivePlan(sessionID, planID, now); err != nil {
			return pebblestore.SessionPlanSnapshot{}, nil, err
		}
		record.Active = true
	}

	payload, err := json.Marshal(map[string]any{
		"session_id":            sessionID,
		"plan_id":               planID,
		"title":                 record.Title,
		"status":                record.Status,
		"approval_state":        record.ApprovalState,
		"activate":              activate,
		"has_active_plan":       activate,
		"active_plan":           record,
		"updated_at":            now,
		"updated":               found,
		"version":               record.Version,
		"parent_revision":       record.ParentRevision,
		"update_summary":        record.UpdateSummary,
		"update_scope":          record.UpdateScope,
		"update_kind":           record.UpdateKind,
		"revision_kind":         record.RevisionKind,
		"restored_from_version": record.RestoredFromVersion,
		"checkpoint":            record.Checkpoint,
	})
	if err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.plan.saved", sessionID, payload, "", "")
	if err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	}
	return record, &env, nil
}

func (s *Service) PatchPlan(sessionID string, options PlanPatchOptions) (pebblestore.SessionPlanSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	planID := strings.TrimSpace(options.PlanID)
	if sessionID == "" {
		return pebblestore.SessionPlanSnapshot{}, nil, errors.New("session id is required")
	}
	if options.Patch.IsZero() && options.Document == nil && options.DocumentPatch == nil {
		return pebblestore.SessionPlanSnapshot{}, nil, errors.New("plan patch requires at least one edit field or document/document_patch")
	}
	var existing pebblestore.SessionPlanSnapshot
	var ok bool
	var err error
	if planID == "" || strings.EqualFold(planID, "active") {
		existing, ok, err = s.GetActivePlan(sessionID)
		if err != nil {
			return pebblestore.SessionPlanSnapshot{}, nil, err
		}
		if !ok {
			return pebblestore.SessionPlanSnapshot{}, nil, errors.New("plan_manage patch requires an active plan or plan_id")
		}
		planID = strings.TrimSpace(existing.ID)
	} else {
		existing, ok, err = s.GetPlan(sessionID, planID)
		if err != nil {
			return pebblestore.SessionPlanSnapshot{}, nil, err
		}
		if !ok {
			return pebblestore.SessionPlanSnapshot{}, nil, fmt.Errorf("plan %q not found", planID)
		}
	}
	patchedPlan := existing.Plan
	if !options.Patch.IsZero() {
		patchedPlan, err = ApplyPlanPatch(existing.Plan, options.Patch)
		if err != nil {
			return pebblestore.SessionPlanSnapshot{}, nil, err
		}
	}
	if patchedPlan == existing.Plan && options.Document == nil && options.DocumentPatch == nil {
		return pebblestore.SessionPlanSnapshot{}, nil, errors.New("plan patch produced no changes")
	}
	title := strings.TrimSpace(options.Title)
	if title == "" {
		title = strings.TrimSpace(existing.Title)
	}
	status := strings.TrimSpace(options.Status)
	if status == "" {
		status = strings.TrimSpace(existing.Status)
	}
	approvalState := strings.TrimSpace(options.ApprovalState)
	if approvalState == "" {
		approvalState = strings.TrimSpace(existing.ApprovalState)
	}
	activate := true
	if options.Activate != nil {
		activate = *options.Activate
	}
	metadata := options.Metadata
	if options.DocumentPatch != nil {
		if metadata.RevisionKind == "" {
			metadata.RevisionKind = classifyPlanDocumentPatchRevisionKind(*options.DocumentPatch)
		}
		document, err := ApplyPlanDocumentPatch(planID, title, existing.Document, *options.DocumentPatch)
		if err != nil {
			return pebblestore.SessionPlanSnapshot{}, nil, err
		}
		metadata.Document = document
	} else if options.Document != nil {
		metadata.Document = options.Document
	}
	return s.SavePlanWithMetadata(sessionID, planID, title, patchedPlan, status, approvalState, activate, metadata)
}

func (s *Service) ListPlans(sessionID string, limit int) ([]pebblestore.SessionPlanSnapshot, string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, "", errors.New("session id is required")
	}
	if _, ok, err := s.store.GetSession(sessionID); err != nil {
		return nil, "", err
	} else if !ok {
		return nil, "", fmt.Errorf("session %q not found", sessionID)
	}

	plans, err := s.store.ListPlans(sessionID, limit)
	if err != nil {
		return nil, "", err
	}
	activeID := ""
	active, ok, err := s.store.GetActivePlan(sessionID)
	if err != nil {
		return nil, "", err
	}
	if ok {
		activeID = strings.TrimSpace(active.PlanID)
	}
	for i := range plans {
		plans[i].Active = strings.EqualFold(strings.TrimSpace(plans[i].ID), activeID)
	}
	return plans, activeID, nil
}

func (s *Service) ListPlanRevisions(sessionID, planID string, limit int) ([]pebblestore.SessionPlanSnapshot, error) {
	return s.ListPlanRevisionsByKind(sessionID, planID, limit, "")
}

func (s *Service) ListPlanRevisionsByKind(sessionID, planID string, limit int, revisionKind string) ([]pebblestore.SessionPlanSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	planID = strings.TrimSpace(planID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if planID == "" {
		return nil, errors.New("plan id is required")
	}
	if _, ok, err := s.store.GetSession(sessionID); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	revisions, err := s.store.ListPlanRevisions(sessionID, planID, limit)
	if err != nil || strings.TrimSpace(revisionKind) == "" {
		return revisions, err
	}
	want := strings.ToLower(strings.TrimSpace(revisionKind))
	out := make([]pebblestore.SessionPlanSnapshot, 0, len(revisions))
	for _, revision := range revisions {
		if revision.RevisionKind == "" {
			revision.RevisionKind = classifyPlanRevisionKind(PlanSaveMetadata{UpdateKind: revision.UpdateKind, Checkpoint: revision.Checkpoint})
		}
		if strings.EqualFold(revision.RevisionKind, want) {
			out = append(out, revision)
		}
	}
	return out, nil
}

func (s *Service) GetPlanRevision(sessionID, planID string, version int) (pebblestore.SessionPlanSnapshot, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	planID = strings.TrimSpace(planID)
	if sessionID == "" {
		return pebblestore.SessionPlanSnapshot{}, false, errors.New("session id is required")
	}
	if planID == "" {
		return pebblestore.SessionPlanSnapshot{}, false, errors.New("plan id is required")
	}
	if version <= 0 {
		return pebblestore.SessionPlanSnapshot{}, false, errors.New("plan revision version is required")
	}
	if _, ok, err := s.store.GetSession(sessionID); err != nil {
		return pebblestore.SessionPlanSnapshot{}, false, err
	} else if !ok {
		return pebblestore.SessionPlanSnapshot{}, false, fmt.Errorf("session %q not found", sessionID)
	}
	return s.store.GetPlanRevision(sessionID, planID, version)
}

func (s *Service) GetPlan(sessionID, planID string) (pebblestore.SessionPlanSnapshot, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	planID = strings.TrimSpace(planID)
	if sessionID == "" {
		return pebblestore.SessionPlanSnapshot{}, false, errors.New("session id is required")
	}
	if planID == "" {
		return pebblestore.SessionPlanSnapshot{}, false, errors.New("plan id is required")
	}
	if _, ok, err := s.store.GetSession(sessionID); err != nil {
		return pebblestore.SessionPlanSnapshot{}, false, err
	} else if !ok {
		return pebblestore.SessionPlanSnapshot{}, false, fmt.Errorf("session %q not found", sessionID)
	}
	plan, ok, err := s.store.GetPlan(sessionID, planID)
	if err != nil || !ok {
		return plan, ok, err
	}
	active, hasActive, err := s.store.GetActivePlan(sessionID)
	if err != nil {
		return pebblestore.SessionPlanSnapshot{}, false, err
	}
	plan.Active = hasActive && strings.EqualFold(strings.TrimSpace(active.PlanID), strings.TrimSpace(plan.ID))
	return plan, true, nil
}

func (s *Service) GetActivePlan(sessionID string) (pebblestore.SessionPlanSnapshot, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionPlanSnapshot{}, false, errors.New("session id is required")
	}
	if _, ok, err := s.store.GetSession(sessionID); err != nil {
		return pebblestore.SessionPlanSnapshot{}, false, err
	} else if !ok {
		return pebblestore.SessionPlanSnapshot{}, false, fmt.Errorf("session %q not found", sessionID)
	}
	active, ok, err := s.store.GetActivePlan(sessionID)
	if err != nil || !ok {
		return pebblestore.SessionPlanSnapshot{}, ok, err
	}
	plan, found, err := s.store.GetPlan(sessionID, active.PlanID)
	if err != nil || !found {
		return pebblestore.SessionPlanSnapshot{}, found, err
	}
	plan.Active = true
	return plan, true, nil
}

func (s *Service) SetActivePlan(sessionID, planID string) (pebblestore.SessionPlanSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	planID = strings.TrimSpace(planID)
	if sessionID == "" {
		return pebblestore.SessionPlanSnapshot{}, nil, errors.New("session id is required")
	}
	if planID == "" {
		return pebblestore.SessionPlanSnapshot{}, nil, errors.New("plan id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok, err := s.store.GetSession(sessionID); err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	} else if !ok {
		return pebblestore.SessionPlanSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	record, ok, err := s.store.GetPlan(sessionID, planID)
	if err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.SessionPlanSnapshot{}, nil, fmt.Errorf("plan %q not found", planID)
	}

	now := time.Now().UnixMilli()
	if err := s.store.SetActivePlan(sessionID, planID, now); err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	}
	record.Active = true

	payload, err := json.Marshal(map[string]any{
		"session_id": sessionID,
		"plan_id":    planID,
		"updated_at": now,
	})
	if err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	}
	stream := "session:" + sessionID
	env, err := s.events.Append(stream, "session.plan.active", sessionID, payload, "", "")
	if err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	}
	return record, &env, nil
}

type StartNewPlanOptions struct {
	Override bool
	Document *pebblestore.SessionPlanDocument
}

func (s *Service) StartNewPlan(sessionID, title string, options ...StartNewPlanOptions) (pebblestore.SessionPlanSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionPlanSnapshot{}, nil, errors.New("session id is required")
	}
	var opts StartNewPlanOptions
	if len(options) > 0 {
		opts = options[0]
	}
	allowOverride := opts.Override
	if !allowOverride {
		active, ok, err := s.GetActivePlan(sessionID)
		if err != nil {
			return pebblestore.SessionPlanSnapshot{}, nil, err
		}
		if ok {
			return pebblestore.SessionPlanSnapshot{}, nil, fmt.Errorf("session already has active plan %q; update the current plan instead, or call plan_manage new with override=true to intentionally create a replacement plan", active.ID)
		}
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New Plan"
	}
	plan := "# " + title + "\n\n- [ ] next step\n"
	return s.SavePlanWithMetadata(sessionID, "", title, plan, "draft", "draft", true, PlanSaveMetadata{Document: opts.Document})
}

func NewSessionID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("generate session id: %v", err))
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	raw := hex.EncodeToString(buf)
	return fmt.Sprintf("%s-%s-%s-%s-%s", raw[:8], raw[8:12], raw[12:16], raw[16:20], raw[20:32])
}

func (s *Service) newSessionID(_ int64) string {
	return NewSessionID()
}

func (s *Service) newPlanID(nowMs int64) string {
	seq := s.counter.Add(1)
	return fmt.Sprintf("plan_%d_%06d", nowMs, seq)
}

func isAllowedRole(role string) bool {
	switch role {
	case "user", "assistant", "system", "tool", "reasoning":
		return true
	default:
		return false
	}
}

func NormalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ModeAuto:
		return ModeAuto
	default:
		return ModePlan
	}
}

func IsValidMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ModePlan, ModeAuto:
		return true
	default:
		return false
	}
}

func filepathBaseSafe(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "workspace"
	}
	last := path
	lastSlash := strings.LastIndex(path, "/")
	if lastSlash >= 0 && lastSlash < len(path)-1 {
		last = path[lastSlash+1:]
	}
	lastBackslash := strings.LastIndex(last, "\\")
	if lastBackslash >= 0 && lastBackslash < len(last)-1 {
		last = last[lastBackslash+1:]
	}
	last = strings.TrimSpace(last)
	if last == "" || last == "." || last == "/" {
		return "workspace"
	}
	return last
}

func normalizeTurnUsage(usage *pebblestore.SessionTurnUsageSnapshot) {
	if usage == nil {
		return
	}
	usage.Transport = strings.ToLower(strings.TrimSpace(usage.Transport))
	if usage.Transport != "" && usage.ConnectedViaWS == nil {
		connected := usage.Transport == "websocket"
		usage.ConnectedViaWS = boolPointer(connected)
	}
	if usage.Transport == "" {
		usage.ConnectedViaWS = nil
	}
	if usage.InputTokens < 0 {
		usage.InputTokens = 0
	}
	if usage.OutputTokens < 0 {
		usage.OutputTokens = 0
	}
	if usage.ThinkingTokens < 0 {
		usage.ThinkingTokens = 0
	}
	if usage.CacheReadTokens < 0 {
		usage.CacheReadTokens = 0
	}
	if usage.CacheWriteTokens < 0 {
		usage.CacheWriteTokens = 0
	}
	if usage.TotalTokens < 0 {
		usage.TotalTokens = 0
	}
	if usage.Steps < 0 {
		usage.Steps = 0
	}
	if usage.ContextWindow < 0 {
		usage.ContextWindow = 0
	}
}

func boolPointer(value bool) *bool {
	out := value
	return &out
}

func normalizeUsageSummary(summary *pebblestore.SessionUsageSummary) {
	if summary == nil {
		return
	}
	summary.LastTransport = strings.ToLower(strings.TrimSpace(summary.LastTransport))
	if summary.LastTransport != "" && summary.LastConnectedViaWS == nil {
		summary.LastConnectedViaWS = boolPointer(summary.LastTransport == "websocket")
	}
	if summary.LastTransport == "" {
		summary.LastConnectedViaWS = nil
	}
	if summary.TurnCount < 0 {
		summary.TurnCount = 0
	}
	if summary.InputTokens < 0 {
		summary.InputTokens = 0
	}
	if summary.OutputTokens < 0 {
		summary.OutputTokens = 0
	}
	if summary.ThinkingTokens < 0 {
		summary.ThinkingTokens = 0
	}
	if summary.CacheReadTokens < 0 {
		summary.CacheReadTokens = 0
	}
	if summary.CacheWriteTokens < 0 {
		summary.CacheWriteTokens = 0
	}
	if summary.TotalTokens < 0 {
		summary.TotalTokens = 0
	}
	if summary.ContextWindow < 0 {
		summary.ContextWindow = 0
	}
	if summary.RemainingTokens < 0 {
		summary.RemainingTokens = 0
	}
}

func normalizeSessionWorkspaceRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root %q: %w", root, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs, nil
	}
	return resolved, nil
}
