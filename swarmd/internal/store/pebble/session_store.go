package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"

	"swarm/packages/swarmd/internal/privacy"
)

type SessionSnapshot struct {
	ID                      string                    `json:"id"`
	WorkspacePath           string                    `json:"workspace_path"`
	WorkspaceName           string                    `json:"workspace_name"`
	TemporaryWorkspaceRoots []string                  `json:"temporary_workspace_roots,omitempty"`
	Title                   string                    `json:"title"`
	Mode                    string                    `json:"mode"`
	Preference              ModelPreference           `json:"preference,omitempty"`
	WorktreeEnabled         bool                      `json:"worktree_enabled,omitempty"`
	WorktreeRootPath        string                    `json:"worktree_root_path,omitempty"`
	WorktreeBaseBranch      string                    `json:"worktree_base_branch,omitempty"`
	WorktreeBranch          string                    `json:"worktree_branch,omitempty"`
	Metadata                map[string]any            `json:"metadata,omitempty"`
	CreatedAt               int64                     `json:"created_at"`
	UpdatedAt               int64                     `json:"updated_at"`
	MessageCount            int                       `json:"message_count"`
	LastMessageAt           int64                     `json:"last_message_at"`
	Lifecycle               *SessionLifecycleSnapshot `json:"lifecycle,omitempty"`
}

type SessionLifecycleSnapshot struct {
	SessionID      string `json:"session_id"`
	RunID          string `json:"run_id,omitempty"`
	Active         bool   `json:"active"`
	Phase          string `json:"phase,omitempty"`
	StartedAt      int64  `json:"started_at,omitempty"`
	EndedAt        int64  `json:"ended_at,omitempty"`
	UpdatedAt      int64  `json:"updated_at,omitempty"`
	Generation     uint64 `json:"generation,omitempty"`
	StopReason     string `json:"stop_reason,omitempty"`
	Error          string `json:"error,omitempty"`
	OwnerTransport string `json:"owner_transport,omitempty"`
}

type MessageSnapshot struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	GlobalSeq uint64         `json:"global_seq"`
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt int64          `json:"created_at"`
}

type SessionCodexConfig struct {
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	ServiceTier string `json:"service_tier,omitempty"`
	ContextMode string `json:"context_mode,omitempty"`
	UpdatedAt   int64  `json:"updated_at,omitempty"`
}

type SessionPlanSnapshot struct {
	ID            string   `json:"id"`
	SessionID     string   `json:"session_id"`
	Title         string   `json:"title"`
	Plan          string   `json:"plan"`
	Status        string   `json:"status"`
	ApprovalState string   `json:"approval_state"`
	Active        bool     `json:"active"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
	PriorTitle    string   `json:"prior_title,omitempty"`
	PriorPlan     string   `json:"prior_plan,omitempty"`
	DiffLines     []string `json:"diff_lines,omitempty"`
}

type SessionPlanActive struct {
	SessionID string `json:"session_id"`
	PlanID    string `json:"plan_id"`
	UpdatedAt int64  `json:"updated_at"`
}

type SessionStore struct {
	store *Store
}

func NewSessionStore(store *Store) *SessionStore {
	return &SessionStore{store: store}
}

func (s *SessionStore) CreateSession(session SessionSnapshot) error {
	return s.store.PutJSON(KeySession(session.ID), session)
}

func (s *SessionStore) UpdateSession(session SessionSnapshot) error {
	return s.store.PutJSON(KeySession(session.ID), session)
}

func (s *SessionStore) DeleteSession(sessionID string) error {
	if s == nil || s.store == nil {
		return errors.New("session store is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Delete([]byte(KeySession(sessionID)), nil); err != nil {
		return err
	}
	if err := batch.Delete([]byte(KeySessionLifecycle(sessionID)), nil); err != nil {
		return err
	}
	if err := batch.Delete([]byte(KeySessionPlanActive(sessionID)), nil); err != nil {
		return err
	}
	return batch.Commit(nil)
}

func (s *SessionStore) GetSession(sessionID string) (SessionSnapshot, bool, error) {
	var session SessionSnapshot
	ok, err := s.store.GetJSON(KeySession(sessionID), &session)
	if err != nil {
		return SessionSnapshot{}, false, err
	}
	if !ok {
		return SessionSnapshot{}, false, nil
	}
	session.TemporaryWorkspaceRoots = NormalizeSessionTemporaryWorkspaceRoots(session.WorkspacePath, session.TemporaryWorkspaceRoots)
	session.Metadata = cloneSessionMetadataMap(session.Metadata)
	session.Lifecycle, err = s.loadSessionLifecycle(session.ID)
	if err != nil {
		return SessionSnapshot{}, false, err
	}
	return session, true, nil
}

func (s *SessionStore) ListSessions(limit int) ([]SessionSnapshot, error) {
	return s.listSessions(limit, nil)
}

func (s *SessionStore) ListSessionsForPath(path string, limit int) ([]SessionSnapshot, error) {
	normalizedPath, err := normalizeSessionPath(path)
	if err != nil {
		return nil, err
	}
	return s.listSessions(limit, func(session SessionSnapshot) bool {
		normalizedWorkspacePath, err := normalizeSessionPath(session.WorkspacePath)
		if err != nil {
			return false
		}
		return normalizedWorkspacePath == normalizedPath
	})
}

func (s *SessionStore) ListSessionsForScope(scopePath string, limit int) ([]SessionSnapshot, error) {
	normalizedScope, err := normalizeSessionPath(scopePath)
	if err != nil {
		return nil, err
	}
	return s.listSessions(limit, func(session SessionSnapshot) bool {
		return pathInScope(session.WorkspacePath, normalizedScope)
	})
}

type WorkspaceSessionList struct {
	WorkspacePath string            `json:"workspace_path"`
	Sessions      []SessionSnapshot `json:"sessions"`
}

func (s *SessionStore) ListTopSessionsByWorkspace(workspacePaths []string, perWorkspaceLimit int) ([]WorkspaceSessionList, error) {
	if perWorkspaceLimit <= 0 {
		perWorkspaceLimit = 25
	}

	normalizedTargets := make(map[string]string, len(workspacePaths))
	order := make([]string, 0, len(workspacePaths))
	for _, raw := range workspacePaths {
		normalized, err := normalizeSessionPath(raw)
		if err != nil {
			return nil, err
		}
		if _, exists := normalizedTargets[normalized]; exists {
			continue
		}
		normalizedTargets[normalized] = strings.TrimSpace(raw)
		order = append(order, normalized)
	}

	groups := make(map[string][]SessionSnapshot, len(order))
	for _, normalized := range order {
		groups[normalized] = nil
	}

	const iterateAll = int(^uint(0) >> 1)
	err := s.store.IteratePrefix(SessionPrefix(), iterateAll, func(_ string, value []byte) error {
		var session SessionSnapshot
		if err := json.Unmarshal(value, &session); err != nil {
			return err
		}
		if strings.TrimSpace(session.ID) == "" {
			return nil
		}
		normalizedWorkspacePath, err := normalizeSessionPath(session.WorkspacePath)
		if err != nil {
			return nil
		}
		matchedWorkspacePath := ""
		for _, candidate := range order {
			// Worktree sessions live under child paths; group them under the nearest workspace root.
			if !normalizedPathInScope(normalizedWorkspacePath, candidate) {
				continue
			}
			if len(candidate) > len(matchedWorkspacePath) {
				matchedWorkspacePath = candidate
			}
		}
		if matchedWorkspacePath == "" {
			return nil
		}
		session.TemporaryWorkspaceRoots = NormalizeSessionTemporaryWorkspaceRoots(session.WorkspacePath, session.TemporaryWorkspaceRoots)
		session.Metadata = cloneSessionMetadataMap(session.Metadata)
		lifecycle, err := s.loadSessionLifecycle(session.ID)
		if err != nil {
			return err
		}
		session.Lifecycle = lifecycle
		groups[matchedWorkspacePath] = append(groups[matchedWorkspacePath], session)
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make([]WorkspaceSessionList, 0, len(order))
	for _, normalized := range order {
		sessions := groups[normalized]
		sort.Slice(sessions, func(i, j int) bool {
			if sessions[i].UpdatedAt == sessions[j].UpdatedAt {
				return sessions[i].ID < sessions[j].ID
			}
			return sessions[i].UpdatedAt > sessions[j].UpdatedAt
		})
		if len(sessions) > perWorkspaceLimit {
			sessions = sessions[:perWorkspaceLimit]
		}
		out = append(out, WorkspaceSessionList{
			WorkspacePath: normalizedTargets[normalized],
			Sessions:      sessions,
		})
	}
	return out, nil
}

func (s *SessionStore) listSessions(limit int, include func(SessionSnapshot) bool) ([]SessionSnapshot, error) {
	if limit <= 0 {
		limit = 100
	}
	out := make([]SessionSnapshot, 0, limit)
	const iterateAll = int(^uint(0) >> 1)
	err := s.store.IteratePrefix(SessionPrefix(), iterateAll, func(_ string, value []byte) error {
		var session SessionSnapshot
		if err := json.Unmarshal(value, &session); err != nil {
			return err
		}
		if strings.TrimSpace(session.ID) == "" {
			return nil
		}
		if include != nil && !include(session) {
			return nil
		}
		session.TemporaryWorkspaceRoots = NormalizeSessionTemporaryWorkspaceRoots(session.WorkspacePath, session.TemporaryWorkspaceRoots)
		session.Metadata = cloneSessionMetadataMap(session.Metadata)
		lifecycle, err := s.loadSessionLifecycle(session.ID)
		if err != nil {
			return err
		}
		session.Lifecycle = lifecycle
		out = append(out, session)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SessionStore) UpsertSessionLifecycle(snapshot SessionLifecycleSnapshot) error {
	snapshot.SessionID = strings.TrimSpace(snapshot.SessionID)
	snapshot.RunID = strings.TrimSpace(snapshot.RunID)
	snapshot.Phase = strings.TrimSpace(snapshot.Phase)
	snapshot.StopReason = strings.TrimSpace(snapshot.StopReason)
	snapshot.Error = strings.TrimSpace(snapshot.Error)
	snapshot.OwnerTransport = strings.TrimSpace(snapshot.OwnerTransport)
	if snapshot.SessionID == "" {
		return errors.New("session lifecycle session id is required")
	}
	return s.store.PutJSON(KeySessionLifecycle(snapshot.SessionID), snapshot)
}

func (s *SessionStore) GetSessionLifecycle(sessionID string) (SessionLifecycleSnapshot, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionLifecycleSnapshot{}, false, errors.New("session lifecycle session id is required")
	}
	var snapshot SessionLifecycleSnapshot
	ok, err := s.store.GetJSON(KeySessionLifecycle(sessionID), &snapshot)
	if err != nil {
		return SessionLifecycleSnapshot{}, false, err
	}
	if !ok {
		return SessionLifecycleSnapshot{}, false, nil
	}
	snapshot.SessionID = sessionID
	snapshot.RunID = strings.TrimSpace(snapshot.RunID)
	snapshot.Phase = strings.TrimSpace(snapshot.Phase)
	snapshot.StopReason = strings.TrimSpace(snapshot.StopReason)
	snapshot.Error = strings.TrimSpace(snapshot.Error)
	snapshot.OwnerTransport = strings.TrimSpace(snapshot.OwnerTransport)
	return snapshot, true, nil
}

func (s *SessionStore) ListActiveSessionLifecycles(limit int) ([]SessionLifecycleSnapshot, error) {
	if limit <= 0 {
		limit = 1000
	}
	out := make([]SessionLifecycleSnapshot, 0, limit)
	err := s.store.IteratePrefix(SessionLifecyclePrefix(), limit, func(_ string, value []byte) error {
		var snapshot SessionLifecycleSnapshot
		if err := json.Unmarshal(value, &snapshot); err != nil {
			return err
		}
		snapshot.SessionID = strings.TrimSpace(snapshot.SessionID)
		snapshot.RunID = strings.TrimSpace(snapshot.RunID)
		snapshot.Phase = strings.TrimSpace(snapshot.Phase)
		snapshot.StopReason = strings.TrimSpace(snapshot.StopReason)
		snapshot.Error = strings.TrimSpace(snapshot.Error)
		snapshot.OwnerTransport = strings.TrimSpace(snapshot.OwnerTransport)
		if !snapshot.Active || snapshot.SessionID == "" {
			return nil
		}
		out = append(out, snapshot)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SessionStore) loadSessionLifecycle(sessionID string) (*SessionLifecycleSnapshot, error) {
	snapshot, ok, err := s.GetSessionLifecycle(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	copy := snapshot
	return &copy, nil
}

func normalizeSessionPath(input string) (string, error) {
	target := strings.TrimSpace(input)
	if target == "" {
		return "", errors.New("workspace path is required")
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs, nil
	}
	return resolved, nil
}

func pathInScope(workspacePath, scopePath string) bool {
	workspacePath = strings.TrimSpace(workspacePath)
	scopePath = strings.TrimSpace(scopePath)
	if workspacePath == "" || scopePath == "" {
		return false
	}

	normalizedSessionPath, err := normalizeSessionPath(workspacePath)
	if err != nil {
		return false
	}
	return normalizedPathInScope(normalizedSessionPath, scopePath)
}

func normalizedPathInScope(normalizedSessionPath, scopePath string) bool {
	if normalizedSessionPath == scopePath {
		return true
	}

	rel, err := filepath.Rel(scopePath, normalizedSessionPath)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func NormalizeSessionTemporaryWorkspaceRoots(workspacePath string, roots []string) []string {
	workspacePath = strings.TrimSpace(workspacePath)
	if len(roots) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(roots))
	out := make([]string, 0, len(roots))
	for _, raw := range roots {
		root := strings.TrimSpace(raw)
		if root == "" || root == workspacePath {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *SessionStore) PutMessage(message MessageSnapshot) error {
	message = sanitizeMessageSnapshot(message)
	return s.store.PutJSON(KeyMessage(message.SessionID, message.GlobalSeq), message)
}

func (s *SessionStore) GetMessage(sessionID string, globalSeq uint64) (MessageSnapshot, bool, error) {
	var message MessageSnapshot
	ok, err := s.store.GetJSON(KeyMessage(sessionID, globalSeq), &message)
	if err != nil {
		return MessageSnapshot{}, false, err
	}
	if !ok {
		return MessageSnapshot{}, false, nil
	}
	message.Metadata = sanitizeMessageMetadata(message.Metadata)
	return message, true, nil
}

func (s *SessionStore) ListMessages(sessionID string, afterGlobalSeq uint64, limit int) ([]MessageSnapshot, error) {
	if limit <= 0 {
		limit = 500
	}
	if afterGlobalSeq == 0 {
		return s.listLatestMessages(sessionID, limit)
	}
	out := make([]MessageSnapshot, 0, limit)
	err := s.store.IteratePrefix(MessagePrefix(sessionID), 100000, func(_ string, value []byte) error {
		var message MessageSnapshot
		if err := json.Unmarshal(value, &message); err != nil {
			return err
		}
		if message.GlobalSeq <= afterGlobalSeq {
			return nil
		}
		if len(out) < limit {
			out = append(out, message)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *SessionStore) listLatestMessages(sessionID string, limit int) ([]MessageSnapshot, error) {
	prefix := MessagePrefix(sessionID)
	iter, err := s.store.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return nil, fmt.Errorf("create latest message iterator: %w", err)
	}
	defer iter.Close()

	out := make([]MessageSnapshot, 0, limit)
	for ok := iter.Last(); ok; ok = iter.Prev() {
		var message MessageSnapshot
		if err := json.Unmarshal(iter.Value(), &message); err != nil {
			return nil, err
		}
		out = append(out, message)
		if len(out) >= limit {
			break
		}
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterate latest messages %q: %w", sessionID, err)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *SessionStore) PutPlan(plan SessionPlanSnapshot) error {
	return s.store.PutJSON(KeySessionPlan(plan.SessionID, plan.ID), plan)
}

func (s *SessionStore) GetPlan(sessionID, planID string) (SessionPlanSnapshot, bool, error) {
	var plan SessionPlanSnapshot
	ok, err := s.store.GetJSON(KeySessionPlan(sessionID, planID), &plan)
	if err != nil {
		return SessionPlanSnapshot{}, false, err
	}
	if !ok {
		return SessionPlanSnapshot{}, false, nil
	}
	return plan, true, nil
}

func (s *SessionStore) ListPlans(sessionID string, limit int) ([]SessionPlanSnapshot, error) {
	if limit <= 0 {
		limit = 200
	}
	out := make([]SessionPlanSnapshot, 0, limit)
	err := s.store.IteratePrefix(SessionPlanPrefix(sessionID), 20000, func(_ string, value []byte) error {
		var plan SessionPlanSnapshot
		if err := json.Unmarshal(value, &plan); err != nil {
			return err
		}
		if strings.TrimSpace(plan.ID) == "" || strings.TrimSpace(plan.SessionID) == "" {
			return nil
		}
		out = append(out, plan)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SessionStore) GetActivePlan(sessionID string) (SessionPlanActive, bool, error) {
	var active SessionPlanActive
	ok, err := s.store.GetJSON(KeySessionPlanActive(sessionID), &active)
	if err != nil {
		return SessionPlanActive{}, false, err
	}
	if !ok {
		return SessionPlanActive{}, false, nil
	}
	return active, true, nil
}

func (s *SessionStore) SetActivePlan(sessionID, planID string, updatedAt int64) error {
	return s.store.PutJSON(KeySessionPlanActive(sessionID), SessionPlanActive{
		SessionID: sessionID,
		PlanID:    planID,
		UpdatedAt: updatedAt,
	})
}

func cloneSessionMetadataMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneSessionMetadataValue(value)
	}
	return out
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
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneSessionMetadataMap(item))
		}
		return out
	default:
		return value
	}
}

func sanitizeMessageSnapshot(message MessageSnapshot) MessageSnapshot {
	message.Content = privacy.SanitizeText(message.Content)
	message.Metadata = sanitizeMessageMetadata(message.Metadata)
	return message
}

func sanitizeMessageMetadata(input map[string]any) map[string]any {
	return privacy.SanitizeMap(input)
}
