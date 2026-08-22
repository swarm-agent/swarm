package artifact

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"swarm/packages/swarmd/internal/appstorage"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const DefaultMaintenanceLimit = 100

// SessionResolver exposes only the durable session routing metadata required to
// resolve a workspace-scoped byte service. It deliberately has no mutation
// authority beyond acknowledging successful cleanup on its durable retry record.
type SessionResolver interface {
	GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error)
	GetSessionTombstone(sessionID string) (pebblestore.V3SessionTombstone, bool, error)
	ListSessions(limit int) ([]pebblestore.SessionSnapshot, error)
	ListPendingSessionArtifactCleanups(limit int) ([]pebblestore.V3SessionTombstone, error)
	MarkSessionArtifactCleanupComplete(sessionID string) error
	RepairSessionArtifactCollections(sessionID string) (pebblestore.SessionArtifactRepairReport, error)
}

// Registry resolves one private byte service per trusted workspace path and
// performs bounded restart/maintenance cleanup from durable session records.
type Registry struct {
	resolver SessionResolver
	limits   Limits
	mu       sync.Mutex
	services map[string]*Service
}

type MaintenanceReport struct {
	SessionsVisited     int
	DeletedSessions     int
	RemovedStaging      int
	RemovedBytes        int64
	CollectionsRepaired int
}

func NewRegistry(resolver SessionResolver, limits Limits) *Registry {
	return &Registry{resolver: resolver, limits: normalizeLimits(limits), services: make(map[string]*Service)}
}

func (r *Registry) ServiceForWorkspace(workspacePath string) (*Service, error) {
	if r == nil {
		return nil, errors.New("artifact registry is not configured")
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return nil, errors.New("workspace path is required")
	}
	key, err := workspaceServiceKey(workspacePath)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if service := r.services[key]; service != nil {
		return service, nil
	}
	service, err := New(workspacePath, r.limits)
	if err != nil {
		return nil, err
	}
	r.services[key] = service
	return service, nil
}

func (r *Registry) ServiceForSession(sessionID string) (*Service, error) {
	if r == nil || r.resolver == nil {
		return nil, errors.New("artifact registry session resolver is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if session, ok, err := r.resolver.GetSession(sessionID); err != nil {
		return nil, err
	} else if ok {
		return r.ServiceForWorkspace(session.WorkspacePath)
	}
	tombstone, ok, err := r.resolver.GetSessionTombstone(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok || strings.TrimSpace(tombstone.WorkspacePath) == "" {
		return nil, fmt.Errorf("session %q was not found", sessionID)
	}
	return r.ServiceForWorkspace(tombstone.WorkspacePath)
}

// ServiceForOwnedSession resolves bytes from the durable session workspace and,
// when supplied, verifies the trusted account/user envelope before returning a
// service. Artifact metadata can never redirect storage to a model-chosen path.
func (r *Registry) ServiceForOwnedSession(sessionID, accountScopeID, userID string) (*Service, pebblestore.SessionSnapshot, error) {
	if r == nil || r.resolver == nil {
		return nil, pebblestore.SessionSnapshot{}, errors.New("artifact registry session resolver is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, pebblestore.SessionSnapshot{}, errors.New("session id is required")
	}
	session, ok, err := r.resolver.GetSession(sessionID)
	if err != nil {
		return nil, pebblestore.SessionSnapshot{}, err
	}
	if ok {
		if accountScopeID != "" && session.AccountScopeID != strings.TrimSpace(accountScopeID) {
			return nil, pebblestore.SessionSnapshot{}, errors.New("artifact account scope does not own session")
		}
		if userID != "" && session.UserID != strings.TrimSpace(userID) {
			return nil, pebblestore.SessionSnapshot{}, errors.New("artifact user does not own session")
		}
		service, err := r.ServiceForWorkspace(session.WorkspacePath)
		return service, session, err
	}
	// Tombstones are intentionally available only to maintenance callers. A
	// lifecycle authority must never create or read artifacts for a deleted session.
	if accountScopeID != "" || userID != "" {
		return nil, pebblestore.SessionSnapshot{}, fmt.Errorf("session %q was not found", sessionID)
	}
	tombstone, ok, err := r.resolver.GetSessionTombstone(sessionID)
	if err != nil {
		return nil, pebblestore.SessionSnapshot{}, err
	}
	if !ok || strings.TrimSpace(tombstone.WorkspacePath) == "" {
		return nil, pebblestore.SessionSnapshot{}, fmt.Errorf("session %q was not found", sessionID)
	}
	service, err := r.ServiceForWorkspace(tombstone.WorkspacePath)
	return service, tombstone.Session, err
}

func (r *Registry) DeleteSession(sessionID, workspacePath string) error {
	service, err := r.ServiceForWorkspace(workspacePath)
	if err != nil {
		return err
	}
	return service.DeleteSession(sessionID)
}

func (r *Registry) RunMaintenance(limit int) (MaintenanceReport, error) {
	if r == nil || r.resolver == nil {
		return MaintenanceReport{}, errors.New("artifact registry session resolver is not configured")
	}
	if limit <= 0 || limit > DefaultMaintenanceLimit {
		limit = DefaultMaintenanceLimit
	}
	report := MaintenanceReport{}
	// Durable deletion retries take priority so a large active-session library
	// cannot starve bytes that no longer have live ownership metadata.
	tombstones, err := r.resolver.ListPendingSessionArtifactCleanups(limit)
	if err != nil {
		return report, err
	}
	for _, tombstone := range tombstones {
		if report.SessionsVisited >= limit {
			break
		}
		if !tombstone.Deleted || tombstone.Archived || !tombstone.ArtifactCleanupPending || strings.TrimSpace(tombstone.WorkspacePath) == "" {
			continue
		}
		if err := r.DeleteSession(tombstone.SessionID, tombstone.WorkspacePath); err != nil {
			return report, fmt.Errorf("delete artifact bytes for session %q: %w", tombstone.SessionID, err)
		}
		if err := r.resolver.MarkSessionArtifactCleanupComplete(tombstone.SessionID); err != nil {
			return report, fmt.Errorf("record artifact cleanup for session %q: %w", tombstone.SessionID, err)
		}
		report.SessionsVisited++
		report.DeletedSessions++
	}
	remaining := limit - report.SessionsVisited
	if remaining <= 0 {
		return report, nil
	}
	sessions, err := r.resolver.ListSessions(remaining)
	if err != nil {
		return report, err
	}
	for _, session := range sessions {
		if report.SessionsVisited >= limit {
			break
		}
		repaired, err := r.resolver.RepairSessionArtifactCollections(session.ID)
		if err != nil {
			return report, fmt.Errorf("repair artifact metadata for session %q: %w", session.ID, err)
		}
		report.CollectionsRepaired += repaired.CollectionsRepaired
		service, err := r.ServiceForWorkspace(session.WorkspacePath)
		if err != nil {
			return report, fmt.Errorf("resolve artifact service for session %q: %w", session.ID, err)
		}
		reconciled, err := service.Reconcile(session.ID)
		if err != nil {
			return report, fmt.Errorf("reconcile artifact session %q: %w", session.ID, err)
		}
		report.SessionsVisited++
		report.RemovedStaging += reconciled.RemovedStaging
		report.RemovedBytes += reconciled.RemovedBytes
	}
	return report, nil
}

func workspaceServiceKey(workspacePath string) (string, error) {
	return appstorage.WorkspaceIdentity(workspacePath)
}
