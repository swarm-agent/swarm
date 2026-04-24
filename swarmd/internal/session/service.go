package session

import (
	"context"
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
	hosted               HostedSessionSync
	localSwarmIDResolver func() string
	mu                   sync.Mutex
	counter              atomic.Uint64
}

type CreateSessionOptions struct {
	SessionID     string
	Title         string
	WorkspacePath string
	WorkspaceName string
	Mode          string
	Preference    *pebblestore.ModelPreference
	Worktree      *CreateSessionWorktree
	Metadata      map[string]any
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
)

func NewService(store *pebblestore.SessionStore, events *pebblestore.EventLog) *Service {
	return &Service{store: store, events: events}
}

func (s *Service) SetHostedSync(sync HostedSessionSync) {
	if s == nil {
		return
	}
	s.hosted = sync
}

func (s *Service) SetLocalSwarmIDResolver(resolver func() string) {
	if s == nil {
		return
	}
	s.localSwarmIDResolver = resolver
}

func (s *Service) hostedDescriptor(metadata map[string]any) (HostedSessionDescriptor, bool) {
	if s == nil {
		return HostedSessionDescriptor{}, false
	}
	localSwarmID := ""
	if s.localSwarmIDResolver != nil {
		localSwarmID = strings.TrimSpace(s.localSwarmIDResolver())
	}
	return HostedSessionFromMetadataForLocal(metadata, localSwarmID)
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
	sessionID := strings.TrimSpace(options.SessionID)
	if sessionID == "" {
		sessionID = s.newSessionID(now)
	}
	session := pebblestore.SessionSnapshot{
		ID:                      sessionID,
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

func (s *Service) StoreMirroredSession(session pebblestore.SessionSnapshot) (pebblestore.SessionSnapshot, error) {
	session.ID = strings.TrimSpace(session.ID)
	session.WorkspacePath = strings.TrimSpace(session.WorkspacePath)
	session.WorkspaceName = strings.TrimSpace(session.WorkspaceName)
	session.Title = strings.TrimSpace(session.Title)
	session.Mode = NormalizeMode(session.Mode)
	session.Metadata = cloneSessionMetadataMap(session.Metadata)
	if session.ID == "" {
		return pebblestore.SessionSnapshot{}, errors.New("session id is required")
	}
	if session.WorkspacePath == "" {
		return pebblestore.SessionSnapshot{}, errors.New("workspace path is required")
	}
	if session.WorkspaceName == "" {
		session.WorkspaceName = filepathBaseSafe(session.WorkspacePath)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok, err := s.store.GetSession(session.ID); err != nil {
		return pebblestore.SessionSnapshot{}, err
	} else if ok {
		session = preserveHostedMirroredSession(existing, session)
		if err := s.store.UpdateSession(session); err != nil {
			return pebblestore.SessionSnapshot{}, err
		}
	} else {
		session = adaptHostedSessionForRuntime(session)
		if err := s.store.CreateSession(session); err != nil {
			return pebblestore.SessionSnapshot{}, err
		}
	}
	return session, nil
}

func (s *Service) SyncHostedMirrorOpenState(sessionID string, source pebblestore.SessionSnapshot) (pebblestore.SessionSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionSnapshot{}, errors.New("session id is required")
	}
	source.ID = strings.TrimSpace(source.ID)
	if source.ID != "" && !strings.EqualFold(source.ID, sessionID) {
		return pebblestore.SessionSnapshot{}, fmt.Errorf("session %q does not match sync source %q", sessionID, source.ID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	if !ok {
		return pebblestore.SessionSnapshot{}, fmt.Errorf("session %q not found", sessionID)
	}

	next := session
	if mode := NormalizeMode(source.Mode); mode != "" {
		next.Mode = mode
	}
	if preference, prefErr := normalizeSessionPreferenceValue(normalizeSessionPreference(&source.Preference)); prefErr == nil && strings.TrimSpace(preference.Provider) != "" && strings.TrimSpace(preference.Model) != "" && strings.TrimSpace(preference.Thinking) != "" {
		next.Preference = preference
	}
	next.WorktreeEnabled = source.WorktreeEnabled
	if next.WorktreeEnabled {
		next.WorktreeRootPath = strings.TrimSpace(source.WorktreeRootPath)
		next.WorktreeBaseBranch = strings.TrimSpace(source.WorktreeBaseBranch)
		next.WorktreeBranch = strings.TrimSpace(source.WorktreeBranch)
	} else {
		next.WorktreeRootPath = ""
		next.WorktreeBaseBranch = ""
		next.WorktreeBranch = ""
	}
	if descriptor, hosted := HostedSessionFromMetadata(next.Metadata); hosted {
		runtimeWorkspacePath := strings.TrimSpace(source.WorkspacePath)
		if runtimeWorkspacePath == "" {
			if sourceDescriptor, ok := HostedSessionFromMetadata(source.Metadata); ok {
				runtimeWorkspacePath = strings.TrimSpace(sourceDescriptor.RuntimeWorkspacePath)
			}
		}
		if runtimeWorkspacePath != "" {
			descriptor.RuntimeWorkspacePath = runtimeWorkspacePath
			next.Metadata = descriptor.WithMetadata(next.Metadata)
		}
	}
	updatedAt := time.Now().UnixMilli()
	if source.UpdatedAt > updatedAt {
		updatedAt = source.UpdatedAt
	}
	if session.UpdatedAt > updatedAt {
		updatedAt = session.UpdatedAt
	}
	next.UpdatedAt = updatedAt

	if err := s.store.UpdateSession(next); err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	return next, nil
}

func (s *Service) StoreMirroredMessage(session pebblestore.SessionSnapshot, message pebblestore.MessageSnapshot) (pebblestore.SessionSnapshot, error) {
	mirrored, err := s.StoreMirroredSession(session)
	if err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	message.Metadata = cloneSessionMetadataMap(message.Metadata)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.PutMessage(message); err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	return mirrored, nil
}

func (s *Service) StoreMirroredLifecycle(snapshot pebblestore.SessionLifecycleSnapshot) error {
	snapshot.SessionID = strings.TrimSpace(snapshot.SessionID)
	if snapshot.SessionID == "" {
		return errors.New("session id is required")
	}
	return s.store.UpsertSessionLifecycle(snapshot)
}

func (s *Service) DeleteSession(sessionID string) error {
	if s == nil || s.store == nil {
		return errors.New("session service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.DeleteSession(sessionID)
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
	if s.hosted != nil {
		if descriptor, hosted := s.hostedDescriptor(session.Metadata); hosted {
			updated, err := s.hosted.UpdateMetadata(context.Background(), descriptor, sessionID, metadata)
			if err != nil {
				return pebblestore.SessionSnapshot{}, nil, err
			}
			updated = adaptHostedSessionForRuntime(updated)
			mirrored, mirrorErr := s.StoreMirroredSession(updated)
			if mirrorErr != nil {
				return pebblestore.SessionSnapshot{}, nil, mirrorErr
			}
			return mirrored, nil, nil
		}
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
	session.UpdatedAt = time.Now().UnixMilli()
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
	session, ok, err := s.store.GetSession(snapshot.SessionID)
	if err != nil {
		return err
	}
	if ok && s.hosted != nil {
		if descriptor, hosted := s.hostedDescriptor(session.Metadata); hosted {
			if err := s.hosted.UpsertLifecycle(context.Background(), descriptor, snapshot); err != nil {
				return err
			}
		}
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
	for i := range sessions {
		sessions[i].Mode = NormalizeMode(sessions[i].Mode)
	}
	return sessions, nil
}

func (s *Service) ListSessionsForPath(path string, limit int) ([]pebblestore.SessionSnapshot, error) {
	sessions, err := s.store.ListSessionsForPath(path, limit)
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		sessions[i].Mode = NormalizeMode(sessions[i].Mode)
	}
	return sessions, nil
}

func (s *Service) ListSessionsForScope(scopePath string, limit int) ([]pebblestore.SessionSnapshot, error) {
	sessions, err := s.store.ListSessionsForScope(scopePath, limit)
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		sessions[i].Mode = NormalizeMode(sessions[i].Mode)
	}
	return sessions, nil
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
		next.ServiceTier = codexruntime.NormalizeServiceTier(*update.ServiceTier)
		if strings.TrimSpace(*update.ServiceTier) != "" && next.ServiceTier == "" {
			return pebblestore.ModelPreference{}, nil, fmt.Errorf("invalid codex service tier %q", *update.ServiceTier)
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
	pref.ServiceTier = codexruntime.NormalizeServiceTier(pref.ServiceTier)
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
	if !supportsCodexRuntime(pref.Provider, pref.Model) {
		pref.ServiceTier = ""
	}
	if !supportsCodexContextRuntime(pref.Provider, pref.Model) {
		pref.ContextMode = ""
	}
	pref.UpdatedAt = time.Now().UnixMilli()
	return pref, nil
}

func supportsCodexRuntime(provider, modelName string) bool {
	return strings.EqualFold(provider, "codex") && (strings.EqualFold(modelName, "gpt-5.4") || strings.EqualFold(modelName, "gpt-5.5"))
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
	if s.hosted != nil {
		if descriptor, hosted := s.hostedDescriptor(session.Metadata); hosted {
			updated, err := s.hosted.SetMode(context.Background(), descriptor, sessionID, mode)
			if err != nil {
				return pebblestore.SessionSnapshot{}, nil, err
			}
			updated = adaptHostedSessionForRuntime(updated)
			mirrored, mirrorErr := s.StoreMirroredSession(updated)
			if mirrorErr != nil {
				return pebblestore.SessionSnapshot{}, nil, mirrorErr
			}
			return mirrored, nil, nil
		}
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
	if session.Mode == mode {
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
	return session, &env, nil
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
	if s.hosted != nil {
		if descriptor, hosted := s.hostedDescriptor(session.Metadata); hosted {
			updated, err := s.hosted.SetTitle(context.Background(), descriptor, sessionID, title)
			if err != nil {
				return pebblestore.SessionSnapshot{}, nil, err
			}
			updated = adaptHostedSessionForRuntime(updated)
			mirrored, mirrorErr := s.StoreMirroredSession(updated)
			if mirrorErr != nil {
				return pebblestore.SessionSnapshot{}, nil, mirrorErr
			}
			return mirrored, nil, nil
		}
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
	if s.hosted != nil {
		if descriptor, hosted := s.hostedDescriptor(session.Metadata); hosted {
			message, updated, err := s.hosted.AppendMessage(context.Background(), descriptor, sessionID, role, content, metadata)
			if err != nil {
				return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
			}
			updated = adaptHostedSessionForRuntime(updated)
			mirrored, mirrorErr := s.StoreMirroredMessage(updated, message)
			if mirrorErr != nil {
				return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, mirrorErr
			}
			return message, mirrored, nil, nil
		}
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
		ID:        fmt.Sprintf("msg_%020d", env.GlobalSeq),
		SessionID: sessionID,
		GlobalSeq: env.GlobalSeq,
		Role:      role,
		Content:   content,
		Metadata:  cleanMetadata,
		CreatedAt: env.TsUnixMs,
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

func preserveHostedMirroredSession(existing, incoming pebblestore.SessionSnapshot) pebblestore.SessionSnapshot {
	if descriptor, hosted := HostedSessionFromMetadata(existing.Metadata); hosted {
		if _, incomingHosted := HostedSessionFromMetadata(incoming.Metadata); !incomingHosted {
			incoming.Metadata = descriptor.WithMetadata(incoming.Metadata)
		}
	}
	return adaptHostedSessionForRuntime(incoming)
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

	if _, ok, err := s.store.GetSession(sessionID); err != nil {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, err
	} else if !ok {
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

	if hadPrevious {
		summary = applyTurnUsageDelta(summary, previous, -1)
	} else {
		summary.TurnCount++
	}

	usage.SessionID = sessionID
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
	summary = applyTurnUsageDelta(summary, usage, 1)
	summary.SessionID = sessionID
	if usage.Provider != "" {
		summary.Provider = usage.Provider
	}
	if usage.Model != "" {
		summary.Model = usage.Model
	}
	if usage.Source != "" {
		summary.Source = usage.Source
	}
	summary.LastTransport = usage.Transport
	if usage.ConnectedViaWS != nil {
		summary.LastConnectedViaWS = boolPointer(*usage.ConnectedViaWS)
	} else {
		summary.LastConnectedViaWS = nil
	}
	summary.LastRunID = usage.RunID
	summary.UpdatedAt = now
	if summary.ContextWindow > 0 {
		usedForRemaining := remainingUsageTokens(usage, summary)
		remaining := int64(summary.ContextWindow) - usedForRemaining
		if remaining < 0 {
			remaining = 0
		}
		summary.RemainingTokens = remaining
	} else {
		summary.RemainingTokens = 0
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

	if _, ok, err := s.store.GetSession(sessionID); err != nil {
		return pebblestore.SessionUsageSummary{}, nil, err
	} else if !ok {
		return pebblestore.SessionUsageSummary{}, nil, fmt.Errorf("session %q not found", sessionID)
	}

	now := time.Now().UnixMilli()
	summary := pebblestore.SessionUsageSummary{
		SessionID:       sessionID,
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

func (s *Service) SavePlan(sessionID, planID, title, plan, status, approvalState string, activate bool) (pebblestore.SessionPlanSnapshot, *pebblestore.EventEnvelope, error) {
	sessionID = strings.TrimSpace(sessionID)
	planID = strings.TrimSpace(planID)
	title = strings.TrimSpace(title)
	plan = strings.TrimSpace(plan)
	status = strings.ToLower(strings.TrimSpace(status))
	approvalState = strings.ToLower(strings.TrimSpace(approvalState))

	if sessionID == "" {
		return pebblestore.SessionPlanSnapshot{}, nil, errors.New("session id is required")
	}
	if title == "" {
		title = "Plan"
	}
	if status == "" {
		status = "draft"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok, err := s.store.GetSession(sessionID); err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	} else if !ok {
		return pebblestore.SessionPlanSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}

	now := time.Now().UnixMilli()
	if planID == "" {
		planID = s.newPlanID(now)
	}

	existing, found, err := s.store.GetPlan(sessionID, planID)
	if err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	}
	record := pebblestore.SessionPlanSnapshot{
		ID:            planID,
		SessionID:     sessionID,
		Title:         title,
		Plan:          plan,
		Status:        status,
		ApprovalState: approvalState,
		Active:        false,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if found {
		record.CreatedAt = existing.CreatedAt
		record.PriorTitle = existing.Title
		record.PriorPlan = existing.Plan
		record.DiffLines = BuildPlanDiffLines(existing.Plan, plan)
	}
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}

	if err := s.store.PutPlan(record); err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	}
	if activate {
		if err := s.store.SetActivePlan(sessionID, planID, now); err != nil {
			return pebblestore.SessionPlanSnapshot{}, nil, err
		}
		record.Active = true
	}

	payload, err := json.Marshal(map[string]any{
		"session_id":     sessionID,
		"plan_id":        planID,
		"title":          record.Title,
		"status":         record.Status,
		"approval_state": record.ApprovalState,
		"activate":       activate,
		"updated_at":     now,
		"updated":        found,
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
	record.UpdatedAt = now
	if err := s.store.PutPlan(record); err != nil {
		return pebblestore.SessionPlanSnapshot{}, nil, err
	}

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

func (s *Service) StartNewPlan(sessionID, title string) (pebblestore.SessionPlanSnapshot, *pebblestore.EventEnvelope, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New Plan"
	}
	plan := "# " + title + "\n\n- [ ] next step\n"
	return s.SavePlan(sessionID, "", title, plan, "draft", "draft", true)
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

func applyTurnUsageDelta(summary pebblestore.SessionUsageSummary, usage pebblestore.SessionTurnUsageSnapshot, sign int64) pebblestore.SessionUsageSummary {
	summary.InputTokens += sign * usage.InputTokens
	summary.OutputTokens += sign * usage.OutputTokens
	summary.ThinkingTokens += sign * usage.ThinkingTokens
	summary.CacheReadTokens += sign * usage.CacheReadTokens
	summary.CacheWriteTokens += sign * usage.CacheWriteTokens
	summary.TotalTokens += sign * usage.TotalTokens
	return summary
}

func remainingUsageTokens(usage pebblestore.SessionTurnUsageSnapshot, summary pebblestore.SessionUsageSummary) int64 {
	source := strings.ToLower(strings.TrimSpace(usage.Source))
	used := usage.TotalTokens
	if source == "google_api_usage" {
		// Gemini context occupancy should be API-sourced only. If total is omitted,
		// fall back to API prompt tokens (still API-reported) and never to session sums.
		if used <= 0 && usage.InputTokens > 0 {
			used = usage.InputTokens
		}
	} else if source == "copilot_session_usage" {
		// Copilot session.usage_info reports current conversation occupancy via CurrentTokens.
		// Persist that snapshot in TotalTokens and do not fall back to accumulated session totals.
		if used < 0 {
			used = 0
		}
		return used
	} else if used <= 0 {
		used = summary.TotalTokens
	}
	if used < 0 {
		return 0
	}
	return used
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
