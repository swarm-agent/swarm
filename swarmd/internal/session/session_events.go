package session

import (
	"context"
	"errors"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type SessionMutationInput = pebblestore.V3SessionMutationInput
type SessionMutationResult = pebblestore.V3SessionMutationResult
type SessionEvent = pebblestore.V3SessionEvent
type SessionProjection = pebblestore.V3SessionProjection
type SessionRunIntent = pebblestore.V3SessionRunIntent
type SessionRunState = pebblestore.V3SessionRunState
type SessionRunStateRepairResult = pebblestore.V3SessionRunStateRepairResult
type SessionReplay = pebblestore.V3SessionReplay
type RealtimeOutboxRecord = pebblestore.V3RealtimeOutboxRecord
type SessionTombstone = pebblestore.V3SessionTombstone
type ArtifactMutation = pebblestore.V3ArtifactMutation
type ArtifactProjection = pebblestore.V3ArtifactProjection
type ArtifactCollection = pebblestore.SessionArtifactCollection
type ArtifactVariant = pebblestore.SessionArtifactVariant
type ArtifactSelectionReference = pebblestore.SessionArtifactSelectionReference
type ArtifactChain = pebblestore.SessionArtifactChain
type VideoProjectSnapshot = pebblestore.VideoProjectSnapshot
type VideoProjectRevisionSnapshot = pebblestore.VideoProjectRevisionSnapshot
type VideoRenderJobSnapshot = pebblestore.VideoRenderJobSnapshot
type VideoProjectTimeline = pebblestore.VideoProjectTimeline
type VideoTimelineClip = pebblestore.VideoTimelineClip
type VideoProjectMutation = pebblestore.V3VideoProjectMutation
type VideoProjectProjection = pebblestore.V3VideoProjectProjection

type SessionIdempotencyRecord = pebblestore.V3SessionIdempotencyRecord

const (
	SessionMutationCreateSession              = pebblestore.V3SessionMutationCreateSession
	SessionMutationAppendMessage              = pebblestore.V3SessionMutationAppendMessage
	SessionMutationUpsertLifecycle            = pebblestore.V3SessionMutationUpsertLifecycle
	SessionMutationRecordRunIntent            = pebblestore.V3SessionMutationRecordRunIntent
	SessionMutationRecordDiagnostic           = pebblestore.V3SessionMutationRecordDiagnostic
	SessionMutationRecordUsage                = pebblestore.V3SessionMutationRecordUsage
	SessionMutationUpdateMode                 = pebblestore.V3SessionMutationUpdateMode
	SessionMutationUpdatePreference           = pebblestore.V3SessionMutationUpdatePreference
	SessionMutationUpdateMetadata             = pebblestore.V3SessionMutationUpdateMetadata
	SessionMutationUpdateSettings             = pebblestore.V3SessionMutationUpdateSettings
	SessionMutationUpdateModelProfile         = pebblestore.V3SessionMutationUpdateModelProfile
	SessionMutationUpdateTitle                = pebblestore.V3SessionMutationUpdateTitle
	SessionMutationSavePlan                   = pebblestore.V3SessionMutationSavePlan
	SessionMutationAcceptPlan                 = pebblestore.V3SessionMutationAcceptPlan
	SessionMutationCommitCheckpointBoundary   = pebblestore.V3SessionMutationCommitCheckpointBoundary
	SessionMutationArchiveSession             = pebblestore.V3SessionMutationArchiveSession
	SessionMutationCreateArtifact             = pebblestore.V3SessionMutationCreateArtifact
	SessionMutationUpdateArtifact             = pebblestore.V3SessionMutationUpdateArtifact
	SessionMutationFinalizeArtifact           = pebblestore.V3SessionMutationFinalizeArtifact
	SessionMutationFailArtifact               = pebblestore.V3SessionMutationFailArtifact
	SessionMutationUnavailableArtifact        = pebblestore.V3SessionMutationUnavailableArtifact
	SessionMutationSelectArtifact             = pebblestore.V3SessionMutationSelectArtifact
	SessionMutationDeleteArtifactVariant      = pebblestore.V3SessionMutationDeleteArtifactVariant
	SessionMutationDeleteArtifactCollection   = pebblestore.V3SessionMutationDeleteArtifactCollection
	SessionMutationCreateVideoProject         = pebblestore.V3SessionMutationCreateVideoProject
	SessionMutationUpdateVideoProject         = pebblestore.V3SessionMutationUpdateVideoProject
	SessionMutationCreateVideoProjectRevision = pebblestore.V3SessionMutationCreateVideoProjectRevision
	SessionMutationCreateVideoRenderJob       = pebblestore.V3SessionMutationCreateVideoRenderJob
	SessionMutationUpdateVideoRenderJob       = pebblestore.V3SessionMutationUpdateVideoRenderJob

	RunIntentPendingExecutor = pebblestore.V3RunIntentPendingExecutor
	RunIntentRunning         = pebblestore.V3RunIntentRunning
	RunIntentCompleted       = pebblestore.V3RunIntentCompleted
	RunIntentFailed          = pebblestore.V3RunIntentFailed
	RunIntentCancelled       = pebblestore.V3RunIntentCancelled
	RunIntentExpired         = pebblestore.V3RunIntentExpired
	RunIntentInterrupted     = pebblestore.V3RunIntentInterrupted
	RunIntentDispatchBlocked = pebblestore.V3RunIntentDispatchBlocked
)

var ErrSessionIdempotencyConflict = pebblestore.ErrV3IdempotencyConflict

func (s *Service) ApplySessionMutation(input SessionMutationInput) (SessionMutationResult, error) {
	if s == nil || s.store == nil {
		return SessionMutationResult{}, errors.New("session store is not configured")
	}
	return s.store.ApplyV3SessionMutation(input)
}

func (s *Service) GetSessionArtifactCollection(accountScopeID, sessionID, collectionID string) (ArtifactCollection, bool, error) {
	if s == nil || s.store == nil {
		return ArtifactCollection{}, false, errors.New("session store is not configured")
	}
	return s.store.GetSessionArtifactCollection(accountScopeID, sessionID, collectionID)
}

func (s *Service) ProjectSessionArtifactVariantChain(accountScopeID, userID string, variant ArtifactVariant) (ArtifactVariant, ArtifactChain, error) {
	if s == nil || s.store == nil {
		return ArtifactVariant{}, ArtifactChain{}, errors.New("session store is not configured")
	}
	return s.store.ProjectSessionArtifactVariantChain(accountScopeID, userID, variant)
}

func (s *Service) GetSessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) (ArtifactVariant, bool, error) {
	if s == nil || s.store == nil {
		return ArtifactVariant{}, false, errors.New("session store is not configured")
	}
	return s.store.GetSessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID)
}

func (s *Service) GetSessionArtifactVariantByID(accountScopeID, sessionID, variantID string) (ArtifactVariant, bool, error) {
	if s == nil || s.store == nil {
		return ArtifactVariant{}, false, errors.New("session store is not configured")
	}
	return s.store.GetSessionArtifactVariantByID(accountScopeID, sessionID, variantID)
}

func (s *Service) ValidateSessionArtifactMessageSelections(accountScopeID, userID string, selections []ArtifactSelectionReference) ([]ArtifactSelectionReference, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ValidateSessionArtifactMessageSelections(accountScopeID, userID, selections)
}

func (s *Service) GetVideoProject(accountScopeID, sessionID, projectID string) (VideoProjectSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return VideoProjectSnapshot{}, false, errors.New("session store is not configured")
	}
	return s.store.GetVideoProject(accountScopeID, sessionID, projectID)
}

func (s *Service) CreateVideoProject(input pebblestore.CreateVideoProjectInput) (VideoProjectSnapshot, *VideoProjectRevisionSnapshot, error) {
	if s == nil || s.store == nil {
		return VideoProjectSnapshot{}, nil, errors.New("session store is not configured")
	}
	return s.store.CreateVideoProject(input)
}

func (s *Service) ListVideoProjects(accountScopeID, sessionID string, limit int) ([]VideoProjectSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListVideoProjects(accountScopeID, sessionID, limit)
}

func (s *Service) CreateVideoProjectRevision(input pebblestore.CreateVideoProjectRevisionInput) (VideoProjectRevisionSnapshot, VideoProjectSnapshot, error) {
	if s == nil || s.store == nil {
		return VideoProjectRevisionSnapshot{}, VideoProjectSnapshot{}, errors.New("session store is not configured")
	}
	return s.store.CreateVideoProjectRevision(input)
}

func (s *Service) GetVideoProjectRevision(accountScopeID, sessionID, projectID, revisionID string) (VideoProjectRevisionSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return VideoProjectRevisionSnapshot{}, false, errors.New("session store is not configured")
	}
	return s.store.GetVideoProjectRevision(accountScopeID, sessionID, projectID, revisionID)
}

func (s *Service) ListVideoProjectRevisions(accountScopeID, sessionID, projectID string, limit int) ([]VideoProjectRevisionSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListVideoProjectRevisions(accountScopeID, sessionID, projectID, limit)
}

func (s *Service) CreateVideoRenderJob(input pebblestore.CreateVideoRenderJobInput) (VideoRenderJobSnapshot, error) {
	if s == nil || s.store == nil {
		return VideoRenderJobSnapshot{}, errors.New("session store is not configured")
	}
	return s.store.CreateVideoRenderJob(input)
}

func (s *Service) GetVideoRenderJob(accountScopeID, sessionID, jobID string) (VideoRenderJobSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return VideoRenderJobSnapshot{}, false, errors.New("session store is not configured")
	}
	return s.store.GetVideoRenderJob(accountScopeID, sessionID, jobID)
}

func (s *Service) UpdateVideoRenderJob(input pebblestore.UpdateVideoRenderJobInput) (VideoRenderJobSnapshot, error) {
	if s == nil || s.store == nil {
		return VideoRenderJobSnapshot{}, errors.New("session store is not configured")
	}
	return s.store.UpdateVideoRenderJob(input)
}

func (s *Service) ListVideoRenderJobs(accountScopeID, sessionID, projectID string, limit int) ([]VideoRenderJobSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListVideoRenderJobs(accountScopeID, sessionID, projectID, limit)
}

func (s *Service) ListSessionArtifactCollections(accountScopeID, sessionID, status string, limit int) ([]ArtifactCollection, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListSessionArtifactCollections(accountScopeID, sessionID, status, limit)
}

func (s *Service) ListAllSessionArtifactCollections(accountScopeID, sessionID, status string) ([]ArtifactCollection, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListAllSessionArtifactCollections(accountScopeID, sessionID, status)
}

func (s *Service) ListSessionArtifactVariants(accountScopeID, sessionID, collectionID string, limit int) ([]ArtifactVariant, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListSessionArtifactVariants(accountScopeID, sessionID, collectionID, limit)
}

func (s *Service) SearchSessionArtifactCatalog(accountScopeID, userID string, options pebblestore.SessionArtifactCatalogOptions) (pebblestore.SessionArtifactCatalogPage, error) {
	if s == nil || s.store == nil {
		return pebblestore.SessionArtifactCatalogPage{}, errors.New("session store is not configured")
	}
	return s.store.SearchSessionArtifactCatalog(accountScopeID, userID, options)
}

func (s *Service) ListSessionArtifactVariantsByLineage(accountScopeID, sessionID, dimension, value string, limit int) ([]ArtifactVariant, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListSessionArtifactVariantsByLineage(accountScopeID, sessionID, dimension, value, limit)
}

func (s *Service) ListSessionEvents(sessionID string, afterSeq uint64, limit int) ([]SessionEvent, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionEvents(sessionID, afterSeq, limit)
}

func (s *Service) ListSessionEventsBefore(sessionID string, beforeSeq uint64, limit int) ([]SessionEvent, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionEventsBefore(sessionID, beforeSeq, limit)
}

func (s *Service) ListSessionMessages(sessionID string, afterSeq uint64, limit int) ([]pebblestore.MessageSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionMessages(sessionID, afterSeq, limit)
}

func (s *Service) ListSessionMessageTail(sessionID string, limit int) ([]pebblestore.MessageSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionMessageTail(sessionID, limit)
}

func (s *Service) ListSessionMessagesBefore(sessionID string, beforeSeq uint64, limit int) ([]pebblestore.MessageSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionMessagesBefore(sessionID, beforeSeq, limit)
}

func (s *Service) GetSessionProjection(sessionID string) (SessionProjection, bool, error) {
	if s == nil || s.store == nil {
		return SessionProjection{}, false, errors.New("session store is not configured")
	}
	return s.store.GetV3SessionProjection(sessionID)
}

func (s *Service) HydrateSessionSnapshot(sessionID string, messageLimit, eventLimit int) (pebblestore.V3SessionHydration, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.V3SessionHydration{}, false, errors.New("session store is not configured")
	}
	return s.store.HydrateV3SessionSnapshot(sessionID, messageLimit, eventLimit)
}

func (s *Service) BuildSessionWorkset(options pebblestore.V3SessionWorksetOptions) (pebblestore.V3SessionWorksetResult, error) {
	if s == nil || s.store == nil {
		return pebblestore.V3SessionWorksetResult{}, errors.New("session store is not configured")
	}
	return s.store.BuildV3SessionWorkset(options)
}

func (s *Service) GetSessionTombstone(sessionID string) (pebblestore.V3SessionTombstone, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.V3SessionTombstone{}, false, errors.New("session store is not configured")
	}
	return s.store.GetV3SessionTombstone(sessionID)
}

func (s *Service) MarkSessionArtifactCleanupComplete(sessionID string) error {
	if s == nil || s.store == nil {
		return errors.New("session store is not configured")
	}
	return s.store.MarkV3SessionArtifactCleanupComplete(sessionID)
}

func (s *Service) SearchSessions(options pebblestore.V3SessionSearchOptions) (pebblestore.V3SessionSearchResult, error) {
	if s == nil || s.store == nil {
		return pebblestore.V3SessionSearchResult{}, errors.New("session store is not configured")
	}
	return s.store.SearchV3Sessions(options)
}

func (s *Service) BuildSyncSnapshot(options pebblestore.V3SyncSnapshotOptions) (pebblestore.V3SyncSnapshotResult, error) {
	if s == nil || s.store == nil {
		return pebblestore.V3SyncSnapshotResult{}, errors.New("session store is not configured")
	}
	return s.store.BuildV3SyncSnapshot(options)
}

func (s *Service) BuildSyncSnapshotWithContext(ctx context.Context, options pebblestore.V3SyncSnapshotOptions) (pebblestore.V3SyncSnapshotResult, error) {
	if s == nil || s.store == nil {
		return pebblestore.V3SyncSnapshotResult{}, errors.New("session store is not configured")
	}
	return s.store.BuildV3SyncSnapshotWithContext(ctx, options)
}

func (s *Service) ReplaySessionEvents(sessionID string, afterSeq uint64, limit int) (SessionReplay, error) {
	if s == nil || s.store == nil {
		return SessionReplay{}, errors.New("session store is not configured")
	}
	return s.store.ReplayV3SessionEvents(sessionID, afterSeq, limit)
}
func (s *Service) ListRealtimeOutboxAfter(afterEndpointSeq uint64, limit int) ([]RealtimeOutboxRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3RealtimeOutboxAfter(afterEndpointSeq, limit)
}

func (s *Service) ListRealtimeOutboxForAuthScopeAfter(accountScopeID, userID string, afterEndpointSeq uint64, limit int) ([]RealtimeOutboxRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3RealtimeOutboxForAuthScopeAfter(accountScopeID, userID, afterEndpointSeq, limit)
}

func (s *Service) ListRealtimeOutboxForSessionAfterEndpoint(sessionID string, afterEndpointSeq uint64, limit int) ([]RealtimeOutboxRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3RealtimeOutboxForSessionAfterEndpoint(sessionID, afterEndpointSeq, limit)
}

func (s *Service) ListSessionTombstonesForAccount(accountScopeID string, limit int) ([]SessionTombstone, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionTombstonesForAccount(accountScopeID, limit)
}

func (s *Service) ListPendingSessionArtifactCleanups(limit int) ([]SessionTombstone, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListPendingV3SessionArtifactCleanups(limit)
}

func (s *Service) RepairSessionArtifactCollections(sessionID string) (pebblestore.SessionArtifactRepairReport, error) {
	if s == nil || s.store == nil {
		return pebblestore.SessionArtifactRepairReport{}, errors.New("session store is not configured")
	}
	return s.store.RepairSessionArtifactCollections(sessionID)
}

func (s *Service) ListRealtimeOutboxForSessionsAfterEndpoint(sessionIDs []string, afterEndpointSeq uint64, limit int) ([]RealtimeOutboxRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3RealtimeOutboxForSessionsAfterEndpoint(sessionIDs, afterEndpointSeq, limit)
}

func (s *Service) LastRealtimeOutboxForSessionAtOrBeforeEndpoint(sessionID string, endpointSeq uint64) (RealtimeOutboxRecord, bool, error) {
	if s == nil || s.store == nil {
		return RealtimeOutboxRecord{}, false, errors.New("session store is not configured")
	}
	return s.store.LastV3RealtimeOutboxForSessionAtOrBeforeEndpoint(sessionID, endpointSeq)
}

func (s *Service) CurrentRealtimeOutboxRevision() (uint64, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("session store is not configured")
	}
	return s.store.CurrentV3RealtimeOutboxRevision()
}

func (s *Service) OldestRetainedRealtimeOutboxRevision() (uint64, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("session store is not configured")
	}
	return s.store.OldestRetainedV3RealtimeEndpointSeq()
}

func (s *Service) PutSessionMaintenanceState(state pebblestore.V3SessionMaintenanceState) error {
	if s == nil || s.store == nil {
		return errors.New("session store is not configured")
	}
	return s.store.PutV3SessionMaintenanceState(state)
}

func (s *Service) RunSessionRetentionPass(ctx context.Context, now time.Time, policy pebblestore.V3SessionRetentionPolicy) (pebblestore.V3SessionMaintenanceResult, error) {
	if s == nil || s.store == nil {
		return pebblestore.V3SessionMaintenanceResult{}, errors.New("session store is not configured")
	}
	return s.store.RunV3SessionRetentionPass(ctx, now, policy)
}

func (s *Service) RunSessionSearchMigrationPass(ctx context.Context, now time.Time, maxSessions int) (pebblestore.V3SessionSearchMigrationResult, error) {
	if s == nil || s.store == nil {
		return pebblestore.V3SessionSearchMigrationResult{}, errors.New("session store is not configured")
	}
	return s.store.RunV3SessionSearchMigrationPass(ctx, now, maxSessions)
}

func (s *Service) CurrentRealtimeOutboxCursor() (string, error) {
	if s == nil || s.store == nil {
		return "", errors.New("session store is not configured")
	}
	return s.store.CurrentV3RealtimeOutboxCursor()
}

func (s *Service) ListRealtimeOutboxForSessionAfterSeq(sessionID string, afterSeq uint64, limit int) ([]RealtimeOutboxRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3RealtimeOutboxForSessionAfterSeq(sessionID, afterSeq, limit)
}

func (s *Service) GetSessionRunIntent(sessionID, runID string) (SessionRunIntent, bool, error) {
	if s == nil || s.store == nil {
		return SessionRunIntent{}, false, errors.New("session store is not configured")
	}
	return s.store.GetV3SessionRunIntent(sessionID, runID)
}

func (s *Service) GetSessionRunState(sessionID string) (SessionRunState, bool, error) {
	if s == nil || s.store == nil {
		return SessionRunState{}, false, errors.New("session store is not configured")
	}
	return s.store.GetV3SessionRunState(sessionID)
}

func (s *Service) GetSessionActiveRunIntent(sessionID string) (SessionRunIntent, bool, error) {
	if s == nil || s.store == nil {
		return SessionRunIntent{}, false, errors.New("session store is not configured")
	}
	return s.store.GetV3SessionActiveRunIntent(sessionID)
}

func (s *Service) ListActiveSessionRunStates(accountScopeID string, limit int) ([]SessionRunState, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3ActiveSessionRunStates(accountScopeID, limit)
}

func (s *Service) EnsureSessionRunStateIndex() error {
	if s == nil || s.store == nil {
		return errors.New("session store is not configured")
	}
	return s.store.EnsureV3RunStateIndex()
}

func (s *Service) RepairSessionRunStatesFromLegacyRunIntents(accountScopeID string, limit int) (SessionRunStateRepairResult, error) {
	if s == nil || s.store == nil {
		return SessionRunStateRepairResult{}, errors.New("session store is not configured")
	}
	return s.store.RepairV3SessionRunStatesFromLegacyRunIntents(accountScopeID, limit)
}

func (s *Service) ListSessionRunIntents(sessionID string, afterSeq uint64, limit int) ([]SessionRunIntent, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionRunIntents(sessionID, afterSeq, limit)
}

func (s *Service) ListSessionRunIntentsByStatus(status string, limit int) ([]SessionRunIntent, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionRunIntentsByStatus(status, limit)
}

func (s *Service) ListRecoverableSessionRunIntents(staleRunningBeforeUnixMs int64, limit int) ([]SessionRunIntent, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionRecoverableRunIntents(staleRunningBeforeUnixMs, limit)
}
