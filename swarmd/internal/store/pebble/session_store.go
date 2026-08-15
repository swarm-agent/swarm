package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"

	"swarm/packages/swarmd/internal/privacy"
)

const (
	SessionModelProfileSourceSaved         = "saved"
	SessionModelProfileSourceTemporary     = "temporary"
	SessionModelProfileSourceSwarmSettings = "swarm_settings"
)

// SessionModelProfileSnapshot is the immutable model-selection contract bound
// to a session. Action is always the resolved Action-mode selection. Plan is
// present only when Plan mode was enabled when the snapshot was captured.
// Explicit favorite identity is copied when applicable. Swarm defaults instead
// carry direct Action/Plan selections with source "swarm_settings".
type SessionModelProfileSnapshot struct {
	Source             string                 `json:"source"`
	UseAccountDefault  bool                   `json:"use_account_default,omitempty"`
	ActionFavoriteID   string                 `json:"action_favorite_id,omitempty"`
	ActionFavoriteName string                 `json:"action_favorite_name,omitempty"`
	Action             ModelProfileSelection  `json:"action"`
	PlanFavoriteID     string                 `json:"plan_favorite_id,omitempty"`
	PlanFavoriteName   string                 `json:"plan_favorite_name,omitempty"`
	Plan               *ModelProfileSelection `json:"plan,omitempty"`
	AppliedAt          int64                  `json:"applied_at"`
}

// CloneSessionModelProfileSnapshot returns a deep copy suitable for crossing a
// persistence, event, or service boundary without sharing mutable selections.
func CloneSessionModelProfileSnapshot(profile *SessionModelProfileSnapshot) *SessionModelProfileSnapshot {
	if profile == nil {
		return nil
	}
	cloned := *profile
	cloned.Plan = CloneModelProfileSelection(profile.Plan)
	return &cloned
}

// CloneModelProfileSelection returns a copy of a resolved session selection.
func CloneModelProfileSelection(selection *ModelProfileSelection) *ModelProfileSelection {
	if selection == nil {
		return nil
	}
	cloned := *selection
	return &cloned
}

type SessionSnapshot struct {
	ID                      string                       `json:"id"`
	UserID                  string                       `json:"user_id,omitempty"`
	AccountScopeID          string                       `json:"account_scope_id,omitempty"`
	WorkspacePath           string                       `json:"workspace_path"`
	WorkspaceName           string                       `json:"workspace_name"`
	TemporaryWorkspaceRoots []string                     `json:"temporary_workspace_roots,omitempty"`
	Title                   string                       `json:"title"`
	Mode                    string                       `json:"mode"`
	Preference              ModelPreference              `json:"preference,omitempty"`
	ModelProfile            *SessionModelProfileSnapshot `json:"model_profile,omitempty"`
	WorktreeEnabled         bool                         `json:"worktree_enabled,omitempty"`
	WorktreeRootPath        string                       `json:"worktree_root_path,omitempty"`
	WorktreeBaseBranch      string                       `json:"worktree_base_branch,omitempty"`
	WorktreeBranch          string                       `json:"worktree_branch,omitempty"`
	Metadata                map[string]any               `json:"metadata,omitempty"`
	CreatedAt               int64                        `json:"created_at"`
	UpdatedAt               int64                        `json:"updated_at"`
	MessageCount            int                          `json:"message_count"`
	LastMessageAt           int64                        `json:"last_message_at"`
	Lifecycle               *SessionLifecycleSnapshot    `json:"lifecycle,omitempty"`
}

type SessionLifecycleSnapshot struct {
	SessionID      string `json:"session_id"`
	UserID         string `json:"user_id,omitempty"`
	AccountScopeID string `json:"account_scope_id,omitempty"`
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

type V3SessionTombstone struct {
	SessionID              string          `json:"session_id"`
	UserID                 string          `json:"user_id,omitempty"`
	AccountScopeID         string          `json:"account_scope_id,omitempty"`
	WorkspacePath          string          `json:"workspace_path,omitempty"`
	Kind                   string          `json:"kind"`
	Deleted                bool            `json:"deleted,omitempty"`
	Archived               bool            `json:"archived,omitempty"`
	Hidden                 bool            `json:"hidden,omitempty"`
	EndpointSeq            uint64          `json:"endpoint_seq"`
	EventSeq               uint64          `json:"event_seq"`
	UpdatedAt              int64           `json:"updated_at"`
	ArtifactCleanupPending bool            `json:"artifact_cleanup_pending,omitempty"`
	Session                SessionSnapshot `json:"session,omitempty"`
}

type MessageSnapshot struct {
	ID                 string                              `json:"id"`
	SessionID          string                              `json:"session_id"`
	UserID             string                              `json:"user_id,omitempty"`
	AccountScopeID     string                              `json:"account_scope_id,omitempty"`
	GlobalSeq          uint64                              `json:"global_seq"`
	Role               string                              `json:"role"`
	Content            string                              `json:"content"`
	Metadata           map[string]any                      `json:"metadata,omitempty"`
	Media              []SessionMediaReference             `json:"media,omitempty"`
	VideoAttachments   []SessionVideoAttachmentReference   `json:"video_attachments,omitempty"`
	ArtifactSelections []SessionArtifactSelectionReference `json:"artifact_selections,omitempty"`
	CreatedAt          int64                               `json:"created_at"`
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
	ID                  string               `json:"id"`
	SessionID           string               `json:"session_id"`
	UserID              string               `json:"user_id,omitempty"`
	AccountScopeID      string               `json:"account_scope_id,omitempty"`
	Title               string               `json:"title"`
	Plan                string               `json:"plan"`
	Document            *SessionPlanDocument `json:"document,omitempty"`
	Status              string               `json:"status"`
	ApprovalState       string               `json:"approval_state"`
	Active              bool                 `json:"active"`
	CreatedAt           int64                `json:"created_at"`
	UpdatedAt           int64                `json:"updated_at"`
	PriorTitle          string               `json:"prior_title,omitempty"`
	PriorPlan           string               `json:"prior_plan,omitempty"`
	DiffLines           []string             `json:"diff_lines,omitempty"`
	UpdateSummary       string               `json:"update_summary,omitempty"`
	UpdateScope         string               `json:"update_scope,omitempty"`
	UpdateKind          string               `json:"update_kind,omitempty"`
	RevisionKind        string               `json:"revision_kind,omitempty"`
	RestoredFromVersion int                  `json:"restored_from_version,omitempty"`
	Version             int                  `json:"version,omitempty"`
	ParentRevision      int                  `json:"parent_revision,omitempty"`
	Checkpoint          bool                 `json:"checkpoint,omitempty"`
}

type SessionPlanDocument struct {
	ID              string                     `json:"id"`
	Title           string                     `json:"title"`
	Status          string                     `json:"status,omitempty"`
	SchemaVersion   string                     `json:"schema_version,omitempty"`
	RevisionID      string                     `json:"revision_id,omitempty"`
	Info            SessionPlanInfo            `json:"info,omitempty"`
	ExecutionPolicy SessionPlanExecutionPolicy `json:"execution_policy,omitempty"`
	// ExecutionOrigin distinguishes lightweight auto-session work from approved
	// full-plan execution without relying on conversation history.
	ExecutionOrigin     string                         `json:"execution_origin,omitempty"`
	ExecutionState      *SessionPlanExecutionState     `json:"execution_state,omitempty"`
	Artifacts           []SessionPlanArtifactReference `json:"artifacts,omitempty"`
	Checkpoints         []SessionPlanCheckpoint        `json:"checkpoints,omitempty"`
	OriginalCheckpoints []SessionPlanCheckpoint        `json:"original_checkpoints,omitempty"`
	ActiveCheckpointID  string                         `json:"active_checkpoint_id,omitempty"`
	RenderedText        string                         `json:"rendered_text,omitempty"`
	DisplayText         string                         `json:"display_text,omitempty"`
}

// SessionPlanExecutionPolicy is plan-level policy. It is intentionally stored
// with the plan document so checkpoint execution can be resumed without reading
// chat history.
type SessionPlanExecutionPolicy struct {
	Mode                     string `json:"mode,omitempty"`
	Shape                    string `json:"shape,omitempty"`
	FollowupCheckpointPolicy string `json:"followup_checkpoint_policy,omitempty"`
}

// SessionPlanExecutionState stores the active execution linkage for the plan.
type SessionPlanExecutionState struct {
	Status           string `json:"status,omitempty"`
	ActiveAttemptID  string `json:"active_attempt_id,omitempty"`
	ParentSessionID  string `json:"parent_session_id,omitempty"`
	CurrentSessionID string `json:"current_session_id,omitempty"`
	CurrentRunID     string `json:"current_run_id,omitempty"`
	LastCheckpointID string `json:"last_checkpoint_id,omitempty"`
	LastAttemptID    string `json:"last_attempt_id,omitempty"`
	LastOutcome      string `json:"last_outcome,omitempty"`
	StartedAt        int64  `json:"started_at,omitempty"`
	UpdatedAt        int64  `json:"updated_at,omitempty"`
	CompletedAt      int64  `json:"completed_at,omitempty"`
}

type SessionPlanInfo struct {
	Goal               string   `json:"goal,omitempty"`
	Scope              string   `json:"scope,omitempty"`
	Context            string   `json:"context,omitempty"`
	Decisions          []string `json:"decisions,omitempty"`
	Constraints        []string `json:"constraints,omitempty"`
	Assumptions        []string `json:"assumptions,omitempty"`
	OpenQuestions      []string `json:"open_questions,omitempty"`
	RelevantFiles      []string `json:"relevant_files,omitempty"`
	SuccessCriteria    []string `json:"success_criteria,omitempty"`
	ValidationStrategy string   `json:"validation_strategy,omitempty"`
}

// SessionPlanArtifactReference identifies a portable workspace artifact that a
// checkpoint may selectively read or must deliver to the user. Contents are
// never embedded in the durable plan document.
type SessionPlanArtifactReference struct {
	Path        string `json:"path"`
	Role        string `json:"role,omitempty"`
	Description string `json:"description,omitempty"`
	MediaType   string `json:"media_type,omitempty"`
}

type SessionPlanCheckpoint struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status,omitempty"`
	Objective string `json:"objective,omitempty"`
	// Tasks is accepted as legacy compatibility input. Subtasks is the canonical
	// durable execution checklist once a document is normalized or mutated.
	Tasks              []string                             `json:"tasks,omitempty"`
	Subtasks           []SessionPlanSubtask                 `json:"subtasks,omitempty"`
	ActiveSubtaskID    string                               `json:"active_subtask_id,omitempty"`
	AcceptanceCriteria []string                             `json:"acceptance_criteria,omitempty"`
	Artifacts          []SessionPlanArtifactReference       `json:"artifacts,omitempty"`
	SourceMessageID    string                               `json:"source_message_id,omitempty"`
	Notes              string                               `json:"notes,omitempty"`
	Report             string                               `json:"report,omitempty"`
	Result             string                               `json:"result,omitempty"`
	ChangedFiles       []string                             `json:"changed_files,omitempty"`
	Validation         []string                             `json:"validation,omitempty"`
	AttemptID          string                               `json:"attempt_id,omitempty"`
	RunID              string                               `json:"run_id,omitempty"`
	SessionID          string                               `json:"session_id,omitempty"`
	StartedAt          int64                                `json:"started_at,omitempty"`
	CompletedAt        int64                                `json:"completed_at,omitempty"`
	Review             *SessionPlanCheckpointReview         `json:"review,omitempty"`
	Recommendation     *SessionPlanCheckpointRecommendation `json:"recommendation,omitempty"`
	Handoff            *SessionPlanCheckpointHandoff        `json:"handoff,omitempty"`
	Attempts           []SessionPlanCheckpointAttempt       `json:"attempts,omitempty"`
	Order              int                                  `json:"order,omitempty"`
}

type SessionPlanSubtask struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status,omitempty"`
	Notes       string `json:"notes,omitempty"`
	Result      string `json:"result,omitempty"`
	StartedAt   int64  `json:"started_at,omitempty"`
	CompletedAt int64  `json:"completed_at,omitempty"`
	Order       int    `json:"order,omitempty"`
}

type SessionPlanCheckpointAttempt struct {
	ID              string   `json:"id"`
	CheckpointID    string   `json:"checkpoint_id,omitempty"`
	Status          string   `json:"status,omitempty"`
	Outcome         string   `json:"outcome,omitempty"`
	RunID           string   `json:"run_id,omitempty"`
	SessionID       string   `json:"session_id,omitempty"`
	ParentSessionID string   `json:"parent_session_id,omitempty"`
	StartedAt       int64    `json:"started_at,omitempty"`
	CompletedAt     int64    `json:"completed_at,omitempty"`
	Report          string   `json:"report,omitempty"`
	Result          string   `json:"result,omitempty"`
	ChangedFiles    []string `json:"changed_files,omitempty"`
	Validation      []string `json:"validation,omitempty"`
}

type SessionPlanCheckpointReview struct {
	Status       string `json:"status,omitempty"`
	ReviewerID   string `json:"reviewer_id,omitempty"`
	ReviewerType string `json:"reviewer_type,omitempty"`
	Result       string `json:"result,omitempty"`
	Notes        string `json:"notes,omitempty"`
	ReviewedAt   int64  `json:"reviewed_at,omitempty"`
}

// SessionPlanCheckpointRecommendation is the single explicit final-review
// recommendation emitted by a checkpoint terminal handoff.
type SessionPlanCheckpointRecommendation struct {
	Decision    string `json:"decision,omitempty"`
	Action      string `json:"action,omitempty"`
	Reason      string `json:"reason,omitempty"`
	ActionState string `json:"action_state,omitempty"`
}

// SessionPlanCheckpointHandoff stores only the concise author-authored fields.
// Full terminal evidence remains canonical on SessionPlanCheckpoint and is
// joined into PlanFinalHandoff only when a lifecycle message is projected.
type SessionPlanCheckpointHandoff struct {
	Title              string                              `json:"title,omitempty"`
	Overview           string                              `json:"overview"`
	ImpactBullets      []string                            `json:"impact_bullets,omitempty"`
	CopyableCodeBlocks []PlanFinalHandoffCopyableCodeBlock `json:"copyable_code_blocks,omitempty"`
	SuggestedPrompts   []PlanFinalHandoffSuggestedPrompt   `json:"suggested_prompts,omitempty"`
	PullRequestURL     string                              `json:"pull_request_url,omitempty"`
}

// PlanFinalHandoffCopyableCodeBlock is display-only text that clients render in
// a code block with an explicit copy affordance. It is never executed directly.
type PlanFinalHandoffCopyableCodeBlock struct {
	Label    string `json:"label,omitempty"`
	Language string `json:"language,omitempty"`
	Code     string `json:"code"`
}

// PlanFinalHandoffSuggestedPrompt is inert chat input. Clients may send Prompt
// through the ordinary V3 user-message path; it is never a direct operation.
type PlanFinalHandoffSuggestedPrompt struct {
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
}

// PlanFinalHandoff is the versioned client projection persisted in lifecycle
// message metadata. Details is a lossless copy of the checkpoint evidence.
type PlanFinalHandoff struct {
	SchemaVersion      int                                  `json:"schema_version"`
	Title              string                               `json:"title"`
	Overview           string                               `json:"overview"`
	ImpactBullets      []string                             `json:"impact_bullets,omitempty"`
	CopyableCodeBlocks []PlanFinalHandoffCopyableCodeBlock  `json:"copyable_code_blocks,omitempty"`
	Recommendation     *SessionPlanCheckpointRecommendation `json:"recommendation,omitempty"`
	SuggestedPrompts   []PlanFinalHandoffSuggestedPrompt    `json:"suggested_prompts,omitempty"`
	PullRequestURL     string                               `json:"pull_request_url,omitempty"`
	Artifacts          []PlanFinalHandoffArtifact           `json:"artifacts,omitempty"`
	Details            PlanFinalHandoffDetails              `json:"details"`
}

// PlanFinalHandoffArtifact is a safe client-facing descriptor for one declared
// deliverable. It intentionally omits the workspace-relative path; clients use
// ID with the authenticated session artifact route instead.
type PlanFinalHandoffArtifact struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Filename    string `json:"filename"`
	MediaType   string `json:"media_type"`
	Kind        string `json:"kind"`
	Previewable bool   `json:"previewable"`
}

// PlanFinalHandoffDetails keeps the complete terminal evidence available to
// clients without expanding it in the default handoff presentation.
type PlanFinalHandoffDetails struct {
	Report       string   `json:"report,omitempty"`
	Result       string   `json:"result,omitempty"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	Validation   []string `json:"validation,omitempty"`
}

type SessionPlanActive struct {
	SessionID      string `json:"session_id"`
	UserID         string `json:"user_id,omitempty"`
	AccountScopeID string `json:"account_scope_id,omitempty"`
	PlanID         string `json:"plan_id"`
	UpdatedAt      int64  `json:"updated_at"`
}

type SessionStore struct {
	store *Store
}

func NewSessionStore(store *Store) *SessionStore {
	return &SessionStore{store: store}
}

func (s *SessionStore) CreateSession(session SessionSnapshot) error {
	session = normalizeSessionOwnership(session)
	if err := validateCanonicalSessionID(session.ID); err != nil {
		return err
	}
	payload, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal session %q: %w", session.ID, err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeySession(session.ID)), payload, nil); err != nil {
		return err
	}
	if session.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySessionByAccount(session.AccountScopeID, session.ID)), []byte(session.ID), nil); err != nil {
			return err
		}
	}
	if err := replaceSessionRecentIndexInBatch(batch, nil, &session); err != nil {
		return err
	}
	if err := replaceSessionReviewAutoArchiveDueInBatch(batch, nil, &session); err != nil {
		return err
	}
	if err := s.replaceV3SessionSearchIndexInBatch(batch, s.store.db, session, false, nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

func (s *SessionStore) CreateSessionForAccount(session SessionSnapshot, userID, accountScopeID string) error {
	session.UserID = strings.TrimSpace(userID)
	session.AccountScopeID = strings.TrimSpace(accountScopeID)
	if session.AccountScopeID == "" {
		return errors.New("account scope id is required")
	}
	return s.CreateSession(session)
}

func (s *SessionStore) UpdateSessionForAccount(session SessionSnapshot, userID, accountScopeID string) error {
	session.UserID = strings.TrimSpace(userID)
	session.AccountScopeID = strings.TrimSpace(accountScopeID)
	if session.AccountScopeID == "" {
		return errors.New("account scope id is required")
	}
	return s.UpdateSession(session)
}

func (s *SessionStore) UpdateSession(session SessionSnapshot) error {
	session = normalizeSessionOwnership(session)
	if err := validateCanonicalSessionID(session.ID); err != nil {
		return err
	}
	payload, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal session %q: %w", session.ID, err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	var previous *SessionSnapshot
	if existing, ok, err := s.GetSession(session.ID); err != nil {
		return err
	} else if ok {
		previous = &existing
		if existing.AccountScopeID != "" && existing.AccountScopeID != session.AccountScopeID {
			if err := batch.Delete([]byte(KeySessionByAccount(existing.AccountScopeID, session.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
				return err
			}
		}
	}
	if err := batch.Set([]byte(KeySession(session.ID)), payload, nil); err != nil {
		return err
	}
	if session.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySessionByAccount(session.AccountScopeID, session.ID)), []byte(session.ID), nil); err != nil {
			return err
		}
	}
	if err := replaceSessionRecentIndexInBatch(batch, previous, &session); err != nil {
		return err
	}
	if err := replaceSessionReviewAutoArchiveDueInBatch(batch, previous, &session); err != nil {
		return err
	}
	if previous == nil || v3SessionSearchMetadataChanged(*previous, session) {
		if err := s.replaceV3SessionSearchIndexInBatch(batch, s.store.db, session, false, nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

func (s *SessionStore) DeleteSession(sessionID string) error {
	return s.DeleteSessions([]string{sessionID})
}

func (s *SessionStore) DeleteSessions(sessionIDs []string) error {
	return s.tombstoneSessions(sessionIDs, "deleted")
}

func (s *SessionStore) ArchiveSession(sessionID string) error {
	return s.tombstoneSession(sessionID, "archived")
}

func (s *SessionStore) ArchiveSessions(sessionIDs []string) error {
	return s.tombstoneSessions(sessionIDs, "archived")
}

// ReactivateArchivedSessions restores a complete archived-session batch in one
// durable commit. Callers must preflight tombstone versions while holding their
// service mutation lock; this store method repeats the expected-version check
// under the per-session mutation locks immediately before writing.
func (s *SessionStore) ReactivateArchivedSessions(sessionIDs []string, expectedUpdatedAt map[string]int64) error {
	if s == nil || s.store == nil {
		return errors.New("session store is not configured")
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
	if len(expectedUpdatedAt) == 0 {
		return errors.New("expected tombstone versions are required")
	}
	unlockSessions := s.store.sessionMutations.lockSessions(normalizedIDs...)
	defer unlockSessions()
	s.store.sessionMutations.libraryRepairMu.RLock()
	defer s.store.sessionMutations.libraryRepairMu.RUnlock()

	tombstones := make([]V3SessionTombstone, 0, len(normalizedIDs))
	for _, sessionID := range normalizedIDs {
		if _, active, err := s.GetSession(sessionID); err != nil {
			return err
		} else if active {
			return fmt.Errorf("session %q is not archived", sessionID)
		}
		tombstone, ok, err := s.GetV3SessionTombstone(sessionID)
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
		tombstones = append(tombstones, tombstone)
	}

	reservedOutbox, err := s.store.sessionMutations.reserveOutbox(s.store, len(tombstones))
	if err != nil {
		return err
	}
	reservationCommitted := false
	defer func() {
		if !reservationCommitted {
			s.store.sessionMutations.abandonOutbox(reservedOutbox)
		}
	}()
	batch := s.store.NewBatch()
	defer batch.Close()
	for i, tombstone := range tombstones {
		session := normalizeSessionForReactivation(normalizeSessionOwnership(tombstone.Session))
		currentSeq, err := s.readV3SessionSequence(session.ID)
		if err != nil {
			return err
		}
		seq, endpointSeq, now := currentSeq+1, reservedOutbox[i], time.Now().UnixMilli()
		payload, err := json.Marshal(v3SessionEventReplayPayload{SessionID: session.ID, Seq: seq, Kind: V3SessionMutationReactivateSession, Session: &session})
		if err != nil {
			return err
		}
		event := V3SessionEvent{ID: fmt.Sprintf("v3evt_%s_%020d", session.ID, seq), SessionID: session.ID, Seq: seq, EventType: "session.reactivated", Payload: payload, TsUnixMs: now}
		projection := V3SessionProjection{SessionID: session.ID, LastEventSeq: seq, ProjectionHighWatermarkSeq: seq, UpdatedAt: now}
		eventPayload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		projectionPayload, err := json.Marshal(projection)
		if err != nil {
			return err
		}
		outbox := V3RealtimeOutboxRecord{EndpointSeq: endpointSeq, EndpointCursor: V3RealtimeOutboxCursor(endpointSeq), SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, Membership: newV3RealtimeOutboxMembershipFromSession(session, now), Event: event, Projection: projection, CreatedAt: now}
		outboxPayload, err := json.Marshal(outbox)
		if err != nil {
			return err
		}
		outboxRef, err := marshalV3RealtimeOutboxReference(outbox)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyV3SessionSequence(session.ID)), uint64ToBytes(seq), nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyV3SessionEvent(session.ID, seq)), eventPayload, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyV3SessionProjection(session.ID)), projectionPayload, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyV3RealtimeOutbox(endpointSeq)), outboxPayload, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyV3RealtimeOutboxBySessionEndpoint(session.ID, endpointSeq)), outboxRef, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyV3RealtimeOutboxBySessionSeq(session.ID, seq)), outboxRef, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyV3RealtimeOutboxByAuthScope(session.AccountScopeID, session.UserID, endpointSeq)), outboxRef, nil); err != nil {
			return err
		}
		if err := s.setSessionInBatch(batch, session); err != nil {
			return err
		}
		if session.Lifecycle != nil {
			lifecyclePayload, err := json.Marshal(session.Lifecycle)
			if err != nil {
				return err
			}
			if err := batch.Set([]byte(KeySessionLifecycle(session.ID)), lifecyclePayload, nil); err != nil {
				return err
			}
			if session.AccountScopeID != "" {
				if err := batch.Set([]byte(KeySessionLifecycleByAccount(session.AccountScopeID, session.ID)), []byte(session.ID), nil); err != nil {
					return err
				}
			}
		}
		if err := removeV3SessionTombstoneInBatch(batch, tombstone); err != nil {
			return err
		}
		if err := s.transitionV3SessionSearchLifecycleInBatch(batch, s.store.db, session, false); err != nil {
			return err
		}
		if err := s.updateV3SessionLibraryMetricInBatch(batch, session, nil, false, false); err != nil {
			return err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	reservationCommitted = true
	if err := s.store.sessionMutations.commitOutbox(s.store, reservedOutbox); err != nil {
		return err
	}
	v3SuccessfulBatchOperations.Add(1)
	return nil
}

func (s *SessionStore) tombstoneSession(sessionID, kind string) error {
	return s.tombstoneSessions([]string{sessionID}, kind)
}

func (s *SessionStore) tombstoneSessions(sessionIDs []string, kind string) error {
	if s == nil || s.store == nil {
		return errors.New("session store is not configured")
	}
	kind = strings.TrimSpace(strings.ToLower(kind))
	if kind != "deleted" && kind != "archived" {
		return fmt.Errorf("unsupported session tombstone kind %q", kind)
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
	unlockSessions := s.store.sessionMutations.lockSessions(normalizedIDs...)
	defer unlockSessions()
	s.store.sessionMutations.libraryRepairMu.RLock()
	defer s.store.sessionMutations.libraryRepairMu.RUnlock()

	existingByID := make(map[string]SessionSnapshot, len(normalizedIDs))
	for _, sessionID := range normalizedIDs {
		if loaded, ok, err := s.GetSession(sessionID); err != nil {
			return err
		} else if ok {
			existingByID[sessionID] = loaded
		} else if tombstone, tombstoneOK, tombstoneErr := s.GetV3SessionTombstone(sessionID); tombstoneErr != nil {
			return tombstoneErr
		} else if tombstoneOK && tombstone.Archived && !tombstone.Deleted {
			existingByID[sessionID] = tombstone.Session
		}
	}

	writeCount := 0
	for _, sessionID := range normalizedIDs {
		if existingByID[sessionID].ID != "" {
			writeCount++
		}
	}
	var reservedOutbox []uint64
	reservationCommitted := false
	if writeCount > 0 {
		var err error
		reservedOutbox, err = s.store.sessionMutations.reserveOutbox(s.store, writeCount)
		if err != nil {
			return err
		}
		defer func() {
			if !reservationCommitted {
				s.store.sessionMutations.abandonOutbox(reservedOutbox)
			}
		}()
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	outboxIndex := 0
	for _, sessionID := range normalizedIDs {
		existing := existingByID[sessionID]
		if existing.ID != "" {
			if kind == "deleted" {
				if err := s.purgeSessionContentInBatch(batch, existing); err != nil {
					return err
				}
				if previousTombstone, ok, tombstoneErr := s.GetV3SessionTombstone(sessionID); tombstoneErr != nil {
					return tombstoneErr
				} else if ok {
					if err := removeV3SessionTombstoneInBatch(batch, previousTombstone); err != nil {
						return err
					}
				}
			}
			currentSeq, err := s.readV3SessionSequence(sessionID)
			if err != nil {
				return err
			}
			seq := currentSeq + 1
			endpointSeq := reservedOutbox[outboxIndex]
			outboxIndex++
			now := time.Now().UnixMilli()
			tombstone := V3SessionTombstone{
				SessionID:              existing.ID,
				UserID:                 existing.UserID,
				AccountScopeID:         existing.AccountScopeID,
				WorkspacePath:          existing.WorkspacePath,
				Kind:                   kind,
				Deleted:                kind == "deleted",
				Archived:               kind == "archived",
				EndpointSeq:            endpointSeq,
				EventSeq:               seq,
				UpdatedAt:              now,
				ArtifactCleanupPending: kind == "deleted",
			}
			// Archived sessions remain restorable. Deleted sessions retain only
			// routing/ordering fields plus the bounded artifact-cleanup retry bit.
			if kind == "archived" {
				tombstone.Session = existing
			}
			mutationKind := V3SessionMutationDeleteSession
			eventType := "session.deleted"
			if kind == "archived" {
				mutationKind = V3SessionMutationArchiveSession
				eventType = "session.archived"
			}
			var replaySession *SessionSnapshot
			if kind == "archived" {
				replaySession = &existing
			}
			payload, err := json.Marshal(v3SessionEventReplayPayload{SessionID: sessionID, Seq: seq, Kind: mutationKind, Session: replaySession, Tombstone: &tombstone})
			if err != nil {
				return fmt.Errorf("marshal v3 session %s payload %q: %w", kind, sessionID, err)
			}
			event := V3SessionEvent{ID: fmt.Sprintf("v3evt_%s_%020d", sessionID, seq), SessionID: sessionID, Seq: seq, EventType: eventType, Payload: payload, TsUnixMs: now}
			projection := V3SessionProjection{SessionID: sessionID, LastEventSeq: seq, ProjectionHighWatermarkSeq: seq, UpdatedAt: now}
			eventPayload, err := json.Marshal(event)
			if err != nil {
				return fmt.Errorf("marshal v3 session %s event %q: %w", kind, sessionID, err)
			}
			projectionPayload, err := json.Marshal(projection)
			if err != nil {
				return fmt.Errorf("marshal v3 session %s projection %q: %w", kind, sessionID, err)
			}
			realtimeOutbox := V3RealtimeOutboxRecord{EndpointSeq: endpointSeq, EndpointCursor: V3RealtimeOutboxCursor(endpointSeq), SessionID: sessionID, UserID: existing.UserID, AccountScopeID: existing.AccountScopeID, Membership: newV3RealtimeOutboxMembershipFromTombstone(tombstone, now), Event: event, Projection: projection, CreatedAt: now}
			realtimeOutboxPayload, err := json.Marshal(realtimeOutbox)
			if err != nil {
				return fmt.Errorf("marshal v3 session %s outbox %q: %w", kind, sessionID, err)
			}
			realtimeOutboxReferencePayload, err := marshalV3RealtimeOutboxReference(realtimeOutbox)
			if err != nil {
				return fmt.Errorf("marshal v3 session %s outbox reference %q: %w", kind, sessionID, err)
			}
			if err := batch.Set([]byte(KeyV3SessionSequence(sessionID)), uint64ToBytes(seq), nil); err != nil {
				return err
			}
			if err := batch.Set([]byte(KeyV3SessionEvent(sessionID, seq)), eventPayload, nil); err != nil {
				return err
			}
			if err := batch.Set([]byte(KeyV3RealtimeOutbox(endpointSeq)), realtimeOutboxPayload, nil); err != nil {
				return err
			}
			if err := batch.Set([]byte(KeyV3RealtimeOutboxBySessionEndpoint(sessionID, endpointSeq)), realtimeOutboxReferencePayload, nil); err != nil {
				return err
			}
			if err := batch.Set([]byte(KeyV3RealtimeOutboxBySessionSeq(sessionID, seq)), realtimeOutboxReferencePayload, nil); err != nil {
				return err
			}
			if err := batch.Set([]byte(KeyV3RealtimeOutboxByAuthScope(existing.AccountScopeID, existing.UserID, endpointSeq)), realtimeOutboxReferencePayload, nil); err != nil {
				return err
			}
			if err := batch.Set([]byte(KeyV3SessionProjection(sessionID)), projectionPayload, nil); err != nil {
				return err
			}
			if err := setV3SessionTombstoneInBatch(batch, tombstone); err != nil {
				return err
			}
			if kind == "archived" {
				if err := s.transitionV3SessionSearchLifecycleInBatch(batch, s.store.db, existing, true); err != nil {
					return err
				}
				if err := s.updateV3SessionLibraryMetricInBatch(batch, existing, nil, true, false); err != nil {
					return err
				}
			} else {
				if err := removeV3SessionSearchIndexInBatch(batch, s.store.db, sessionID); err != nil {
					return err
				}
				if err := s.updateV3SessionLibraryMetricInBatch(batch, existing, nil, false, true); err != nil {
					return err
				}
			}
		}
		if err := batch.Delete([]byte(KeySession(sessionID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return err
		}
		if existing.AccountScopeID != "" {
			if err := batch.Delete([]byte(KeySessionByAccount(existing.AccountScopeID, sessionID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
				return err
			}
			if err := batch.Delete([]byte(KeySessionLifecycleByAccount(existing.AccountScopeID, sessionID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
				return err
			}
			if kind == "deleted" {
				if err := batch.Delete([]byte(KeySessionPlanActiveByAccount(existing.AccountScopeID, sessionID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
					return err
				}
			}
		}
		if err := batch.Delete([]byte(KeySessionLifecycle(sessionID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return err
		}
		if kind == "deleted" {
			if err := batch.Delete([]byte(KeySessionPlanActive(sessionID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
				return err
			}
		}
		if existing.ID != "" {
			if err := replaceSessionRecentIndexInBatch(batch, &existing, nil); err != nil {
				return err
			}
			if err := replaceSessionReviewAutoArchiveDueInBatch(batch, &existing, nil); err != nil {
				return err
			}
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	reservationCommitted = true
	if err := s.store.sessionMutations.commitOutbox(s.store, reservedOutbox); err != nil {
		return err
	}
	v3SuccessfulBatchOperations.Add(1)
	return nil
}

func deletePrefixInBatch(batch *pebble.Batch, prefix string) error {
	if strings.TrimSpace(prefix) == "" {
		return nil
	}
	return batch.DeleteRange([]byte(prefix), []byte(prefix+"\xff"), nil)
}

// purgeSessionContentInBatch removes reclaimable session-owned data while the
// caller retains and replaces the minimal tombstone/event/projection/outbox
// records needed for durable V3 removal replay.
func (s *SessionStore) purgeSessionContentInBatch(batch *pebble.Batch, session SessionSnapshot) error {
	for _, prefix := range []string{
		MessagePrefix(session.ID), MessageByAccountPrefix(session.AccountScopeID, session.ID),
		SessionPlanPrefix(session.ID), SessionPlanRevisionPrefix(session.ID, ""), SessionPlanByAccountPrefix(session.AccountScopeID, session.ID),
		SessionTurnUsagePrefix(session.ID), SessionTurnUsageByAccountPrefix(session.AccountScopeID, session.ID),
		PermissionPrefix(session.ID), PermissionPendingPrefix(session.ID), RunWaitPrefix(session.ID), RunPermissionPrefix(session.ID, ""),
		V3SessionEventPrefix(session.ID), V3SessionMessagePrefix(session.ID), V3SessionRunIntentPrefix(session.ID),
		ExecutionEpochPrefix(session.ID), ExecutionEpochOrdinalPrefix(session.ID), ExecutionEpochBoundaryPrefix(session.ID), ExecutionProviderLifecycleStatePrefix(session.ID),
		V3SessionIdempotencyPrefix(session.AccountScopeID, session.ID), V3RealtimeOutboxBySessionEndpointPrefix(session.ID), V3RealtimeOutboxBySessionSeqPrefix(session.ID),
		SessionMediaAssetPrefix(session.AccountScopeID, session.ID), SessionMediaBlobPrefix(session.AccountScopeID, session.ID),
		TranscriptionAttachmentPrefix(session.AccountScopeID, session.ID), TranscriptionJobPrefix(session.AccountScopeID, session.ID), NormalizedTranscriptPrefix(session.AccountScopeID, session.ID),
		SessionArtifactCollectionPrefix(session.AccountScopeID, session.ID), SessionArtifactCollectionStatusSessionPrefix(session.AccountScopeID, session.ID),
		SessionArtifactVariantSessionPrefix(session.AccountScopeID, session.ID), SessionArtifactVariantStatusSessionPrefix(session.AccountScopeID, session.ID), SessionArtifactVariantDigestSessionPrefix(session.AccountScopeID, session.ID), SessionArtifactVariantLineageSessionPrefix(session.AccountScopeID, session.ID),
	} {
		if err := deletePrefixInBatch(batch, prefix); err != nil {
			return err
		}
	}
	for _, key := range []string{
		KeySessionUsageSummary(session.ID), KeySessionUsageSummaryByAccount(session.AccountScopeID, session.ID),
		KeyV3SessionSequence(session.ID), KeyV3SessionProjection(session.ID), KeyV3SessionRunIntentActive(session.ID), KeyExecutionEpochActive(session.ID), KeyExecutionEpochLatest(session.ID),
	} {
		if err := batch.Delete([]byte(key), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return err
		}
	}
	return nil
}

func normalizeV3SessionTombstone(tombstone V3SessionTombstone) V3SessionTombstone {
	tombstone.SessionID = strings.TrimSpace(tombstone.SessionID)
	tombstone.UserID = strings.TrimSpace(tombstone.UserID)
	tombstone.AccountScopeID = strings.TrimSpace(tombstone.AccountScopeID)
	tombstone.WorkspacePath = strings.TrimSpace(tombstone.WorkspacePath)
	tombstone.Kind = strings.TrimSpace(strings.ToLower(tombstone.Kind))
	if tombstone.Kind == "" {
		switch {
		case tombstone.Deleted:
			tombstone.Kind = "deleted"
		case tombstone.Archived:
			tombstone.Kind = "archived"
		case tombstone.Hidden:
			tombstone.Kind = "hidden"
		default:
			tombstone.Kind = "changed"
		}
	}
	tombstone.Session = normalizeSessionOwnership(tombstone.Session)
	if tombstone.Session.ID != "" {
		if tombstone.UserID == "" {
			tombstone.UserID = tombstone.Session.UserID
		}
		if tombstone.AccountScopeID == "" {
			tombstone.AccountScopeID = tombstone.Session.AccountScopeID
		}
		if tombstone.WorkspacePath == "" {
			tombstone.WorkspacePath = tombstone.Session.WorkspacePath
		}
	}
	return tombstone
}

func setV3SessionTombstoneInBatch(batch *pebble.Batch, tombstone V3SessionTombstone) error {
	tombstone = normalizeV3SessionTombstone(tombstone)
	if tombstone.SessionID == "" {
		return errors.New("session tombstone session id is required")
	}
	payload, err := json.Marshal(tombstone)
	if err != nil {
		return fmt.Errorf("marshal v3 session tombstone %q: %w", tombstone.SessionID, err)
	}
	if err := batch.Set([]byte(KeyV3SessionTombstone(tombstone.SessionID)), payload, nil); err != nil {
		return err
	}
	if tombstone.ArtifactCleanupPending {
		if err := batch.Set([]byte(KeyV3SessionArtifactCleanupPending(tombstone.SessionID)), payload, nil); err != nil {
			return err
		}
	} else if err := batch.Delete([]byte(KeyV3SessionArtifactCleanupPending(tombstone.SessionID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
		return err
	}
	if tombstone.AccountScopeID != "" {
		if err := batch.Set([]byte(KeyV3SessionTombstoneByAccount(tombstone.AccountScopeID, tombstone.SessionID)), payload, nil); err != nil {
			return err
		}
	}
	if tombstone.AccountScopeID != "" && tombstone.UserID != "" {
		if err := batch.Set([]byte(KeyV3SessionTombstoneByAccountUser(tombstone.AccountScopeID, tombstone.UserID, tombstone.UpdatedAt, tombstone.SessionID)), payload, nil); err != nil {
			return err
		}
		if tombstone.WorkspacePath != "" {
			workspacePath := tombstone.WorkspacePath
			if normalized, err := normalizeSessionPath(workspacePath); err == nil {
				workspacePath = normalized
			}
			if err := batch.Set([]byte(KeyV3SessionTombstoneByAccountUserWorkspace(tombstone.AccountScopeID, tombstone.UserID, workspacePath, tombstone.UpdatedAt, tombstone.SessionID)), payload, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeV3SessionTombstoneInBatch(batch *pebble.Batch, tombstone V3SessionTombstone) error {
	tombstone = normalizeV3SessionTombstone(tombstone)
	if tombstone.SessionID == "" {
		return nil
	}
	deleteKey := func(key string) error {
		if strings.TrimSpace(key) == "" {
			return nil
		}
		if err := batch.Delete([]byte(key), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return err
		}
		return nil
	}
	if err := deleteKey(KeyV3SessionTombstone(tombstone.SessionID)); err != nil {
		return err
	}
	if err := deleteKey(KeyV3SessionArtifactCleanupPending(tombstone.SessionID)); err != nil {
		return err
	}
	if tombstone.AccountScopeID != "" {
		if err := deleteKey(KeyV3SessionTombstoneByAccount(tombstone.AccountScopeID, tombstone.SessionID)); err != nil {
			return err
		}
	}
	if tombstone.AccountScopeID != "" && tombstone.UserID != "" {
		if err := deleteKey(KeyV3SessionTombstoneByAccountUser(tombstone.AccountScopeID, tombstone.UserID, tombstone.UpdatedAt, tombstone.SessionID)); err != nil {
			return err
		}
		if tombstone.WorkspacePath != "" {
			workspacePath := tombstone.WorkspacePath
			if normalized, err := normalizeSessionPath(workspacePath); err == nil {
				workspacePath = normalized
			}
			if err := deleteKey(KeyV3SessionTombstoneByAccountUserWorkspace(tombstone.AccountScopeID, tombstone.UserID, workspacePath, tombstone.UpdatedAt, tombstone.SessionID)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SessionStore) GetV3SessionTombstone(sessionID string) (V3SessionTombstone, bool, error) {
	return getV3SessionTombstoneFromReader(s.store.db, sessionID)
}

func (s *SessionStore) MarkV3SessionArtifactCleanupComplete(sessionID string) error {
	if s == nil || s.store == nil {
		return errors.New("session store is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	unlock := s.store.sessionMutations.lockSessions(sessionID)
	defer unlock()
	tombstone, ok, err := getV3SessionTombstoneFromReader(s.store.db, sessionID)
	if err != nil || !ok {
		return err
	}
	if !tombstone.Deleted || tombstone.Archived {
		return errors.New("artifact cleanup can only complete for a deleted session")
	}
	if !tombstone.ArtifactCleanupPending {
		return nil
	}
	previous := tombstone
	tombstone.ArtifactCleanupPending = false
	batch := s.store.NewBatch()
	defer batch.Close()
	// Replace all tombstone indexes in one batch so the pending cleanup index
	// cannot diverge from the durable replay tombstone.
	if err := removeV3SessionTombstoneInBatch(batch, previous); err != nil {
		return err
	}
	if err := setV3SessionTombstoneInBatch(batch, tombstone); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

func getV3SessionTombstoneFromReader(reader pebble.Reader, sessionID string) (V3SessionTombstone, bool, error) {
	var tombstone V3SessionTombstone
	ok, err := getJSONFromReader(reader, KeyV3SessionTombstone(strings.TrimSpace(sessionID)), &tombstone)
	if err != nil || !ok {
		return V3SessionTombstone{}, ok, err
	}
	return normalizeV3SessionTombstone(tombstone), true, nil
}

func (s *SessionStore) ListPendingV3SessionArtifactCleanups(limit int) ([]V3SessionTombstone, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	out := make([]V3SessionTombstone, 0, limit)
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: V3SessionArtifactCleanupPendingPrefix(), Limit: limit}, func(_ string, value []byte) (bool, error) {
		var tombstone V3SessionTombstone
		if err := json.Unmarshal(value, &tombstone); err != nil {
			return false, err
		}
		tombstone = normalizeV3SessionTombstone(tombstone)
		if tombstone.Deleted && !tombstone.Archived && tombstone.ArtifactCleanupPending {
			out = append(out, tombstone)
		}
		return len(out) < limit, nil
	})
	return out, err
}

func (s *SessionStore) ListV3SessionTombstonesForAccount(accountScopeID string, limit int) ([]V3SessionTombstone, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if limit <= 0 {
		limit = 500
	}
	prefix := V3SessionTombstonePrefix()
	if accountScopeID != "" {
		prefix = V3SessionTombstoneByAccountPrefix(accountScopeID)
	}
	out := make([]V3SessionTombstone, 0)
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: prefix, Limit: limit}, func(_ string, value []byte) (bool, error) {
		var tombstone V3SessionTombstone
		if err := json.Unmarshal(value, &tombstone); err != nil {
			return false, err
		}
		tombstone = normalizeV3SessionTombstone(tombstone)
		if accountScopeID != "" && tombstone.AccountScopeID != accountScopeID {
			return true, nil
		}
		out = append(out, tombstone)
		return len(out) < limit, nil
	})
	return out, err
}

func (s *SessionStore) GetSession(sessionID string) (SessionSnapshot, bool, error) {
	return s.getSessionFromReader(s.store.db, sessionID)
}

func (s *SessionStore) getSessionFromReader(reader pebble.Reader, sessionID string) (SessionSnapshot, bool, error) {
	var session SessionSnapshot
	ok, err := getJSONFromReader(reader, KeySession(sessionID), &session)
	if err != nil {
		return SessionSnapshot{}, false, err
	}
	if !ok {
		return SessionSnapshot{}, false, nil
	}
	session = normalizeSessionOwnership(session)
	session.TemporaryWorkspaceRoots = NormalizeSessionTemporaryWorkspaceRoots(session.WorkspacePath, session.TemporaryWorkspaceRoots)
	session.Metadata = cloneSessionMetadataMap(session.Metadata)
	session.ModelProfile = CloneSessionModelProfileSnapshot(session.ModelProfile)
	if lifecycle, lifecycleOK, err := getSessionLifecycleFromReader(reader, session.ID); err != nil {
		return SessionSnapshot{}, false, err
	} else if lifecycleOK {
		session.Lifecycle = &lifecycle
	}
	return session, true, nil
}

func (s *SessionStore) ListSessions(limit int) ([]SessionSnapshot, error) {
	return s.listSessions(limit, nil)
}

func (s *SessionStore) ListSessionsForAccount(accountScopeID string, limit int) ([]SessionSnapshot, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	return s.listSessionsForAccount(accountScopeID, limit, nil)
}

func (s *SessionStore) ListSessionsForAccountUser(accountScopeID, userID string, limit int) ([]SessionSnapshot, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("user id is required")
	}
	return s.listSessionsForAccount(accountScopeID, limit, func(session SessionSnapshot) bool {
		return strings.TrimSpace(session.UserID) == userID
	})
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

func (s *SessionStore) ListSessionsForAccountPath(accountScopeID, path string, limit int) ([]SessionSnapshot, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	normalizedPath, err := normalizeSessionPath(path)
	if err != nil {
		return nil, err
	}
	return s.listSessionsForAccount(accountScopeID, limit, func(session SessionSnapshot) bool {
		normalizedWorkspacePath, err := normalizeSessionPath(session.WorkspacePath)
		if err != nil {
			return false
		}
		return normalizedWorkspacePath == normalizedPath
	})
}

func (s *SessionStore) ListSessionsForAccountScope(accountScopeID, scopePath string, limit int) ([]SessionSnapshot, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	normalizedScope, err := normalizeSessionPath(scopePath)
	if err != nil {
		return nil, err
	}
	return s.listSessionsForAccount(accountScopeID, limit, func(session SessionSnapshot) bool {
		return pathInScope(session.WorkspacePath, normalizedScope)
	})
}

func (s *SessionStore) ListSessionsForAccountWorkspaceBindings(accountScopeID, sourceWorkspaceID string, workspaceBindingIDs []string, fallbackScopePath string, limit int) ([]SessionSnapshot, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	sourceWorkspaceID = strings.TrimSpace(sourceWorkspaceID)
	bindingSet := make(map[string]struct{}, len(workspaceBindingIDs))
	for _, bindingID := range workspaceBindingIDs {
		if bindingID = strings.TrimSpace(bindingID); bindingID != "" {
			bindingSet[bindingID] = struct{}{}
		}
	}
	if sourceWorkspaceID == "" && len(bindingSet) == 0 {
		return s.ListSessionsForAccountScope(accountScopeID, fallbackScopePath, limit)
	}
	if limit <= 0 {
		limit = 100
	}
	fallbackScopePath = strings.TrimSpace(fallbackScopePath)
	normalizedFallbackScope := ""
	if fallbackScopePath != "" {
		normalized, err := normalizeSessionPath(fallbackScopePath)
		if err != nil {
			return nil, err
		}
		normalizedFallbackScope = normalized
	}
	out := make([]SessionSnapshot, 0, limit)
	const iterateAll = int(^uint(0) >> 1)
	err := s.store.IteratePrefix(SessionByAccountPrefix(accountScopeID), iterateAll, func(_ string, value []byte) error {
		sessionID := strings.TrimSpace(string(value))
		if sessionID == "" {
			return nil
		}
		session, ok, err := s.GetSession(sessionID)
		if err != nil {
			return err
		}
		if !ok || strings.TrimSpace(session.AccountScopeID) != accountScopeID {
			return nil
		}
		include := sourceWorkspaceID != "" && strings.TrimSpace(sessionMetadataString(session.Metadata, "swarm_v3_source_workspace_id")) == sourceWorkspaceID
		if !include && len(bindingSet) > 0 {
			bindingID := strings.TrimSpace(sessionMetadataString(session.Metadata, "swarm_v3_workspace_binding_id"))
			if bindingID == "" {
				bindingID = strings.TrimSpace(sessionMetadataString(session.Metadata, "local_workspace_binding_id"))
			}
			_, include = bindingSet[bindingID]
		}
		if !include && normalizedFallbackScope != "" {
			include = sessionMatchesWorkspaceScope(session, normalizedFallbackScope)
		}
		if !include {
			return nil
		}
		if err := s.appendSessionListItem(&out, session); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return finalizeSessionList(out, limit), nil
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
		session = normalizeSessionOwnership(session)
		matchedWorkspacePath := ""
		for _, candidate := range order {
			// Worktree sessions are physically rooted outside the source workspace; group them by binding/source identity when present.
			if !sessionMatchesWorkspaceScope(session, candidate) {
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
		sessions = selectTopParentSessionsWithChildren(sessions, perWorkspaceLimit)
		out = append(out, WorkspaceSessionList{
			WorkspacePath: normalizedTargets[normalized],
			Sessions:      sessions,
		})
	}
	return out, nil
}

func selectTopParentSessionsWithChildren(sessions []SessionSnapshot, parentLimit int) []SessionSnapshot {
	if parentLimit <= 0 || len(sessions) == 0 {
		return sessions
	}

	selectedSessionIDs := make(map[string]struct{}, parentLimit)
	selected := make([]SessionSnapshot, 0, parentLimit)
	children := make([]SessionSnapshot, 0)
	for _, session := range sessions {
		sessionID := strings.TrimSpace(session.ID)
		parentSessionID := sessionMetadataString(session.Metadata, "parent_session_id")
		if parentSessionID == "" || parentSessionID == sessionID {
			if len(selected) < parentLimit {
				selected = append(selected, session)
				selectedSessionIDs[sessionID] = struct{}{}
			}
			continue
		}
		children = append(children, session)
	}

	if len(children) == 0 {
		return selected
	}
	for {
		added := false
		remaining := children[:0]
		for _, session := range children {
			sessionID := strings.TrimSpace(session.ID)
			parentSessionID := sessionMetadataString(session.Metadata, "parent_session_id")
			if _, ok := selectedSessionIDs[parentSessionID]; ok {
				selected = append(selected, session)
				selectedSessionIDs[sessionID] = struct{}{}
				added = true
				continue
			}
			remaining = append(remaining, session)
		}
		children = remaining
		if !added || len(children) == 0 {
			break
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].UpdatedAt == selected[j].UpdatedAt {
			return selected[i].ID < selected[j].ID
		}
		return selected[i].UpdatedAt > selected[j].UpdatedAt
	})
	return selected
}

func sessionMetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
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
		session = normalizeSessionOwnership(session)
		if include != nil && !include(session) {
			return nil
		}
		if err := s.appendSessionListItem(&out, session); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return finalizeSessionList(out, limit), nil
}

func (s *SessionStore) listSessionsForAccount(accountScopeID string, limit int, include func(SessionSnapshot) bool) ([]SessionSnapshot, error) {
	if limit <= 0 {
		limit = 100
	}
	out := make([]SessionSnapshot, 0, limit)
	const iterateAll = int(^uint(0) >> 1)
	err := s.store.IteratePrefix(SessionByAccountPrefix(accountScopeID), iterateAll, func(_ string, value []byte) error {
		sessionID := strings.TrimSpace(string(value))
		if sessionID == "" {
			return nil
		}
		session, ok, err := s.GetSession(sessionID)
		if err != nil {
			return err
		}
		if !ok || strings.TrimSpace(session.AccountScopeID) != accountScopeID {
			return nil
		}
		if include != nil && !include(session) {
			return nil
		}
		if err := s.appendSessionListItem(&out, session); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return finalizeSessionList(out, limit), nil
}

func (s *SessionStore) appendSessionListItem(out *[]SessionSnapshot, session SessionSnapshot) error {
	session.TemporaryWorkspaceRoots = NormalizeSessionTemporaryWorkspaceRoots(session.WorkspacePath, session.TemporaryWorkspaceRoots)
	session.Metadata = cloneSessionMetadataMap(session.Metadata)
	lifecycle, err := s.loadSessionLifecycle(session.ID)
	if err != nil {
		return err
	}
	session.Lifecycle = lifecycle
	*out = append(*out, session)
	return nil
}

func finalizeSessionList(out []SessionSnapshot, limit int) []SessionSnapshot {
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *SessionStore) UpsertSessionLifecycle(snapshot SessionLifecycleSnapshot) error {
	snapshot.SessionID = strings.TrimSpace(snapshot.SessionID)
	snapshot.UserID = strings.TrimSpace(snapshot.UserID)
	snapshot.AccountScopeID = strings.TrimSpace(snapshot.AccountScopeID)
	snapshot.RunID = strings.TrimSpace(snapshot.RunID)
	snapshot.Phase = strings.TrimSpace(snapshot.Phase)
	snapshot.StopReason = strings.TrimSpace(snapshot.StopReason)
	snapshot.Error = strings.TrimSpace(snapshot.Error)
	snapshot.OwnerTransport = strings.TrimSpace(snapshot.OwnerTransport)
	if snapshot.SessionID == "" {
		return errors.New("session lifecycle session id is required")
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal session lifecycle %q: %w", snapshot.SessionID, err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeySessionLifecycle(snapshot.SessionID)), payload, nil); err != nil {
		return err
	}
	if snapshot.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySessionLifecycleByAccount(snapshot.AccountScopeID, snapshot.SessionID)), []byte(snapshot.SessionID), nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

func (s *SessionStore) GetSessionLifecycle(sessionID string) (SessionLifecycleSnapshot, bool, error) {
	return getSessionLifecycleFromReader(s.store.db, sessionID)
}

func getSessionLifecycleFromReader(reader pebble.Reader, sessionID string) (SessionLifecycleSnapshot, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionLifecycleSnapshot{}, false, errors.New("session lifecycle session id is required")
	}
	var snapshot SessionLifecycleSnapshot
	ok, err := getJSONFromReader(reader, KeySessionLifecycle(sessionID), &snapshot)
	if err != nil {
		return SessionLifecycleSnapshot{}, false, err
	}
	if !ok {
		return SessionLifecycleSnapshot{}, false, nil
	}
	snapshot.SessionID = sessionID
	snapshot.UserID = strings.TrimSpace(snapshot.UserID)
	snapshot.AccountScopeID = strings.TrimSpace(snapshot.AccountScopeID)
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
		snapshot.UserID = strings.TrimSpace(snapshot.UserID)
		snapshot.AccountScopeID = strings.TrimSpace(snapshot.AccountScopeID)
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
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal message %q/%d: %w", message.SessionID, message.GlobalSeq, err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyMessage(message.SessionID, message.GlobalSeq)), payload, nil); err != nil {
		return err
	}
	if message.AccountScopeID != "" {
		if err := batch.Set([]byte(KeyMessageByAccount(message.AccountScopeID, message.SessionID, message.GlobalSeq)), []byte(message.ID), nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
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
	message.SessionID = strings.TrimSpace(message.SessionID)
	message.UserID = strings.TrimSpace(message.UserID)
	message.AccountScopeID = strings.TrimSpace(message.AccountScopeID)
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
	return s.putPlanWithArchivedRevision(plan, nil)
}

func (s *SessionStore) PutPlanWithArchivedRevision(plan, archived SessionPlanSnapshot) error {
	return s.putPlanWithArchivedRevision(plan, &archived)
}

func (s *SessionStore) putPlanWithArchivedRevision(plan SessionPlanSnapshot, archived *SessionPlanSnapshot) error {
	plan.UserID = strings.TrimSpace(plan.UserID)
	plan.AccountScopeID = strings.TrimSpace(plan.AccountScopeID)
	payload, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("marshal plan %q/%q: %w", plan.SessionID, plan.ID, err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if archived != nil {
		archive := *archived
		archive.UserID = strings.TrimSpace(archive.UserID)
		archive.AccountScopeID = strings.TrimSpace(archive.AccountScopeID)
		archivePayload, err := json.Marshal(archive)
		if err != nil {
			return fmt.Errorf("marshal plan revision %q/%q/%d: %w", archive.SessionID, archive.ID, archive.Version, err)
		}
		if err := batch.Set([]byte(KeySessionPlanRevision(archive.SessionID, archive.ID, archive.Version)), archivePayload, nil); err != nil {
			return err
		}
	}
	if plan.Version <= 0 {
		plan.Version = 1
	}
	planRevisionPayload, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("marshal plan revision %q/%q/%d: %w", plan.SessionID, plan.ID, plan.Version, err)
	}
	if err := batch.Set([]byte(KeySessionPlanRevision(plan.SessionID, plan.ID, plan.Version)), planRevisionPayload, nil); err != nil {
		return err
	}
	payload = planRevisionPayload
	if err := batch.Set([]byte(KeySessionPlan(plan.SessionID, plan.ID)), payload, nil); err != nil {
		return err
	}
	if plan.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySessionPlanByAccount(plan.AccountScopeID, plan.SessionID, plan.ID)), []byte(plan.ID), nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
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
	plan.UserID = strings.TrimSpace(plan.UserID)
	plan.AccountScopeID = strings.TrimSpace(plan.AccountScopeID)
	return plan, true, nil
}

func (s *SessionStore) GetPlanRevision(sessionID, planID string, version int) (SessionPlanSnapshot, bool, error) {
	var plan SessionPlanSnapshot
	ok, err := s.store.GetJSON(KeySessionPlanRevision(sessionID, planID, version), &plan)
	if err != nil {
		return SessionPlanSnapshot{}, false, err
	}
	if !ok {
		return SessionPlanSnapshot{}, false, nil
	}
	plan.UserID = strings.TrimSpace(plan.UserID)
	plan.AccountScopeID = strings.TrimSpace(plan.AccountScopeID)
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

func (s *SessionStore) ListPlanRevisions(sessionID, planID string, limit int) ([]SessionPlanSnapshot, error) {
	if limit <= 0 {
		limit = 200
	}
	out := make([]SessionPlanSnapshot, 0, limit)
	err := s.store.IteratePrefix(SessionPlanRevisionPrefix(sessionID, planID), 20000, func(_ string, value []byte) error {
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
		if out[i].Version == out[j].Version {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].Version > out[j].Version
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
	active.UserID = strings.TrimSpace(active.UserID)
	active.AccountScopeID = strings.TrimSpace(active.AccountScopeID)
	return active, true, nil
}

func (s *SessionStore) SetActivePlan(sessionID, planID string, updatedAt int64) error {
	active := SessionPlanActive{
		SessionID: sessionID,
		PlanID:    planID,
		UpdatedAt: updatedAt,
	}
	if session, ok, err := s.GetSession(sessionID); err != nil {
		return err
	} else if ok {
		active.UserID = strings.TrimSpace(session.UserID)
		active.AccountScopeID = strings.TrimSpace(session.AccountScopeID)
	}
	payload, err := json.Marshal(active)
	if err != nil {
		return fmt.Errorf("marshal active plan %q: %w", sessionID, err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeySessionPlanActive(sessionID)), payload, nil); err != nil {
		return err
	}
	if active.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySessionPlanActiveByAccount(active.AccountScopeID, sessionID)), payload, nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
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

func normalizeSessionOwnership(session SessionSnapshot) SessionSnapshot {
	session.ID = strings.TrimSpace(session.ID)
	session.UserID = strings.TrimSpace(session.UserID)
	session.AccountScopeID = strings.TrimSpace(session.AccountScopeID)
	session.WorkspacePath = strings.TrimSpace(session.WorkspacePath)
	session.WorkspaceName = strings.TrimSpace(session.WorkspaceName)
	session.WorktreeRootPath = strings.TrimSpace(session.WorktreeRootPath)
	session.WorktreeBaseBranch = strings.TrimSpace(session.WorktreeBaseBranch)
	session.WorktreeBranch = strings.TrimSpace(session.WorktreeBranch)
	if session.WorktreeEnabled {
		if session.WorktreeRootPath == "" || session.WorktreeBranch == "" {
			session.WorktreeEnabled = false
			session.WorktreeRootPath = ""
			session.WorktreeBaseBranch = ""
			session.WorktreeBranch = ""
		}
	} else {
		session.WorktreeRootPath = ""
		session.WorktreeBaseBranch = ""
	}
	return session
}

func sanitizeMessageSnapshot(message MessageSnapshot) MessageSnapshot {
	message.SessionID = strings.TrimSpace(message.SessionID)
	message.UserID = strings.TrimSpace(message.UserID)
	message.AccountScopeID = strings.TrimSpace(message.AccountScopeID)
	message.Content = privacy.SanitizeText(message.Content)
	message.Metadata = sanitizeMessageMetadata(message.Metadata)
	message.Media = normalizeSessionMediaReferences(message.Media)
	message.VideoAttachments = normalizeSessionVideoAttachmentReferences(message.VideoAttachments)
	message.ArtifactSelections = normalizeSessionArtifactSelectionReferences(message.ArtifactSelections)
	return message
}

func sanitizeMessageMetadata(input map[string]any) map[string]any {
	return privacy.SanitizeMap(input)
}

func normalizeSessionVideoAttachmentReferences(input []SessionVideoAttachmentReference) []SessionVideoAttachmentReference {
	if len(input) == 0 {
		return nil
	}
	out := make([]SessionVideoAttachmentReference, 0, len(input))
	for _, ref := range input {
		ref.Ref = strings.TrimSpace(ref.Ref)
		ref.Name = strings.TrimSpace(ref.Name)
		ref.MIMEType = strings.ToLower(strings.TrimSpace(ref.MIMEType))
		ref.SourceFingerprint = strings.ToLower(strings.TrimSpace(ref.SourceFingerprint))
		out = append(out, ref)
	}
	return out
}

func normalizeSessionArtifactSelectionReferences(input []SessionArtifactSelectionReference) []SessionArtifactSelectionReference {
	if len(input) == 0 {
		return nil
	}
	out := make([]SessionArtifactSelectionReference, 0, len(input))
	for _, ref := range input {
		ref.SessionID = strings.TrimSpace(ref.SessionID)
		ref.CollectionID = strings.TrimSpace(ref.CollectionID)
		ref.VariantID = strings.TrimSpace(ref.VariantID)
		ref.Label = strings.TrimSpace(ref.Label)
		ref.Description = strings.TrimSpace(ref.Description)
		ref.Action = strings.ToLower(strings.TrimSpace(ref.Action))
		out = append(out, ref)
	}
	return out
}
