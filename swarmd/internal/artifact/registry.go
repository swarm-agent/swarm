package artifact

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/artifactgit"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const DefaultMaintenanceLimit = 100

type SessionResolver interface {
	GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error)
	GetSessionTombstone(sessionID string) (pebblestore.V3SessionTombstone, bool, error)
	ListSessions(limit int) ([]pebblestore.SessionSnapshot, error)
	ListPendingSessionArtifactCleanups(limit int) ([]pebblestore.V3SessionTombstone, error)
	MarkSessionArtifactCleanupComplete(sessionID string) error
	RepairSessionArtifactCollections(sessionID string) (pebblestore.SessionArtifactRepairReport, error)
}

// Registry authenticates session ownership and opens daemon-owned bare Git
// repositories. It has no workspace-derived artifact byte service.
type Registry struct {
	resolver       SessionResolver
	limits         Limits
	mu             sync.Mutex
	repositories   map[string]*artifactgit.Repository
	repositoryRoot string
	repositoryErr  error
}

type MaintenanceReport struct {
	SessionsVisited     int
	DeletedSessions     int
	CollectionsRepaired int
}

func NewRegistry(resolver SessionResolver, limits Limits) *Registry {
	root, err := appstorage.DataDir("artifact-repositories")
	return &Registry{resolver: resolver, limits: normalizeLimits(limits), repositories: make(map[string]*artifactgit.Repository), repositoryRoot: root, repositoryErr: err}
}

func (r *Registry) OwnedSession(sessionID, accountScopeID, userID string) (pebblestore.SessionSnapshot, error) {
	if r == nil || r.resolver == nil {
		return pebblestore.SessionSnapshot{}, errors.New("artifact registry session resolver is not configured")
	}
	session, ok, err := r.resolver.GetSession(strings.TrimSpace(sessionID))
	if err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	if !ok {
		return pebblestore.SessionSnapshot{}, fmt.Errorf("session %q was not found", sessionID)
	}
	if session.AccountScopeID != strings.TrimSpace(accountScopeID) || session.UserID != strings.TrimSpace(userID) {
		return pebblestore.SessionSnapshot{}, errors.New("artifact session ownership does not match")
	}
	return session, nil
}

// VerifyGitPrerequisite checks native Git and private repository creation without
// leaving a probe repository or cached handle in the runtime registry.
func (r *Registry) VerifyGitPrerequisite(ctx context.Context) error {
	const probeID = "startup-probe"
	repo, err := r.Repository(ctx, probeID)
	if err != nil {
		return err
	}
	if err = repo.Delete(); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.repositories, probeID)
	r.mu.Unlock()
	return nil
}

func (r *Registry) Repository(ctx context.Context, repositoryID string) (*artifactgit.Repository, error) {
	if r == nil {
		return nil, errors.New("artifact Git repository root is not configured")
	}
	if r.repositoryErr != nil {
		return nil, fmt.Errorf("resolve artifact Git repository root: %w", r.repositoryErr)
	}
	if strings.TrimSpace(r.repositoryRoot) == "" {
		return nil, errors.New("artifact Git repository root is not configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if repo := r.repositories[repositoryID]; repo != nil {
		return repo, nil
	}
	limits := artifactgit.Limits{MaxBlobBytes: r.limits.MaxVideoArtifactBytes, MaxCompositionBytes: r.limits.MaxSessionBytes, MaxParts: pebblestore.SessionArtifactMaxParts}
	repo, err := artifactgit.Open(ctx, r.repositoryRoot, repositoryID, limits)
	if err != nil {
		return nil, err
	}
	r.repositories[repositoryID] = repo
	return repo, nil
}

// RunMaintenance applies exact Git ref/repository cleanup captured before a
// session projection is purged, then repairs bounded Pebble projections. Cleanup
// is artifact-identity based and never derives a byte location from a workspace.
func (r *Registry) RunMaintenance(limit int) (MaintenanceReport, error) {
	if r == nil || r.resolver == nil {
		return MaintenanceReport{}, errors.New("artifact registry session resolver is not configured")
	}
	if limit <= 0 || limit > DefaultMaintenanceLimit {
		limit = DefaultMaintenanceLimit
	}
	report := MaintenanceReport{}
	tombstones, err := r.resolver.ListPendingSessionArtifactCleanups(limit)
	if err != nil {
		return report, err
	}
	for _, tombstone := range tombstones {
		if report.SessionsVisited >= limit {
			break
		}
		if !tombstone.Deleted || tombstone.Archived || !tombstone.ArtifactCleanupPending {
			continue
		}
		if len(tombstone.ArtifactGitCleanup) == 0 {
			return report, errors.New("artifact cleanup tombstone is missing exact Git identities")
		}
		deletedRepositories := make(map[string]struct{})
		for _, cleanup := range tombstone.ArtifactGitCleanup {
			if _, deleted := deletedRepositories[cleanup.RepositoryID]; deleted {
				continue
			}
			repo, openErr := r.Repository(context.Background(), cleanup.RepositoryID)
			if openErr != nil {
				return report, openErr
			}
			if cleanup.DeleteRepository {
				if cleanup.CandidateRef != "" {
					return report, errors.New("artifact cleanup cannot delete a root repository through a candidate ref")
				}
				if deleteErr := repo.Delete(); deleteErr != nil {
					return report, deleteErr
				}
				r.mu.Lock()
				delete(r.repositories, cleanup.RepositoryID)
				r.mu.Unlock()
				deletedRepositories[cleanup.RepositoryID] = struct{}{}
				continue
			}
			if cleanup.CandidateRef == "" || cleanup.ExpectedCommit == "" {
				return report, errors.New("artifact candidate cleanup identity is incomplete")
			}
			if deleteErr := repo.DeleteCandidate(context.Background(), cleanup.CandidateRef, cleanup.ExpectedCommit); deleteErr != nil {
				return report, deleteErr
			}
		}
		if err := r.resolver.MarkSessionArtifactCleanupComplete(tombstone.SessionID); err != nil {
			return report, err
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
		repaired, err := r.resolver.RepairSessionArtifactCollections(session.ID)
		if err != nil {
			return report, err
		}
		report.SessionsVisited++
		report.CollectionsRepaired += repaired.CollectionsRepaired
	}
	return report, nil
}
