package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/google/uuid"
	"swarm-refactor/swarmtui/pkg/environments"
)

var (
	// ErrDeploymentLeaseHeld indicates that the deployment is currently leased by another active consumer.
	ErrDeploymentLeaseHeld = errors.New("deployment already has an active lease held by another consumer")

	// ErrDeploymentLeaseNotFound indicates that the requested lease ID was not found.
	ErrDeploymentLeaseNotFound = errors.New("deployment lease not found")
)

// LeaseStore manages persistence and atomic lifecycle of DeploymentLease records in Pebble.
// Strictly scoped to AccountScopeID and WorkspaceID.
type LeaseStore struct {
	store *Store
	mu    sync.Mutex
}

// NewLeaseStore creates a new LeaseStore backed by the given Pebble Store.
func NewLeaseStore(store *Store) *LeaseStore {
	return &LeaseStore{store: store}
}

// Get retrieves a DeploymentLease by account scope, workspace id, and lease id.
func (s *LeaseStore) Get(accountScopeID, workspaceID, leaseID string) (environments.DeploymentLease, bool, error) {
	if s == nil || s.store == nil {
		return environments.DeploymentLease{}, false, errors.New("lease store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	leaseID = strings.TrimSpace(leaseID)
	if accountScopeID == "" || workspaceID == "" || leaseID == "" {
		return environments.DeploymentLease{}, false, errors.New("account scope id, workspace id, and lease id are required")
	}

	key := KeyDeploymentLeaseForAccount(accountScopeID, workspaceID, leaseID)
	var lease environments.DeploymentLease
	ok, err := s.store.GetJSON(key, &lease)
	if err != nil || !ok {
		return environments.DeploymentLease{}, ok, err
	}

	// Enforce strict isolation
	if lease.AccountScopeID != accountScopeID || lease.WorkspaceID != workspaceID {
		return environments.DeploymentLease{}, false, nil
	}

	return lease, true, nil
}

// GetActiveLease retrieves the current active lease for a deployment, if any.
func (s *LeaseStore) GetActiveLease(accountScopeID, workspaceID, deploymentID string) (environments.DeploymentLease, bool, error) {
	if s == nil || s.store == nil {
		return environments.DeploymentLease{}, false, errors.New("lease store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	deploymentID = strings.TrimSpace(deploymentID)
	if accountScopeID == "" || workspaceID == "" || deploymentID == "" {
		return environments.DeploymentLease{}, false, errors.New("account scope id, workspace id, and deployment id are required")
	}

	activeKey := KeyDeploymentActiveLeaseForAccount(accountScopeID, workspaceID, deploymentID)
	activeLeaseIDBytes, ok, err := s.store.GetBytes(activeKey)
	if err != nil || !ok {
		return environments.DeploymentLease{}, ok, err
	}

	activeLeaseID := string(activeLeaseIDBytes)
	lease, found, err := s.Get(accountScopeID, workspaceID, activeLeaseID)
	if err != nil || !found {
		return environments.DeploymentLease{}, false, err
	}

	if !lease.Active {
		return environments.DeploymentLease{}, false, nil
	}

	return lease, true, nil
}

// AcquireLease atomically acquires an exclusive lease for a deployment.
// If an active lease is currently held (unexpired), AcquireLease returns ErrDeploymentLeaseHeld.
// If a prior lease expired, it is atomically transitioned to inactive with reason "expired".
func (s *LeaseStore) AcquireLease(lease environments.DeploymentLease) (environments.DeploymentLease, error) {
	if s == nil || s.store == nil {
		return environments.DeploymentLease{}, errors.New("lease store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	if lease.ID == "" {
		lease.ID = "lease_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if lease.AcquiredAt <= 0 {
		lease.AcquiredAt = now
	}
	lease.Active = true

	if err := lease.Validate(); err != nil {
		return environments.DeploymentLease{}, fmt.Errorf("validate lease: %w", err)
	}

	// Check if active lease already exists
	activeKey := KeyDeploymentActiveLeaseForAccount(lease.AccountScopeID, lease.WorkspaceID, lease.DeploymentID)
	existingActiveIDBytes, hasActive, err := s.store.GetBytes(activeKey)
	if err != nil {
		return environments.DeploymentLease{}, fmt.Errorf("check active lease: %w", err)
	}

	batch := s.store.NewBatch()
	defer batch.Close()

	if hasActive {
		existingID := string(existingActiveIDBytes)
		existingKey := KeyDeploymentLeaseForAccount(lease.AccountScopeID, lease.WorkspaceID, existingID)
		var existingLease environments.DeploymentLease
		found, err := s.store.GetJSON(existingKey, &existingLease)
		if err != nil {
			return environments.DeploymentLease{}, fmt.Errorf("read existing active lease: %w", err)
		}
		if found && existingLease.Active {
			if existingLease.IsHeld(now) {
				return environments.DeploymentLease{}, fmt.Errorf("%w: held by %s (%s)", ErrDeploymentLeaseHeld, existingLease.ConsumerID, existingLease.ConsumerType)
			}
			// Expired: expire it atomically in the batch
			existingLease.Active = false
			existingLease.ReleasedAt = now
			existingLease.ReleaseReason = "expired"
			expiredRaw, err := json.Marshal(existingLease)
			if err != nil {
				return environments.DeploymentLease{}, fmt.Errorf("marshal expired lease: %w", err)
			}
			if err := batch.Set([]byte(existingKey), expiredRaw, nil); err != nil {
				return environments.DeploymentLease{}, fmt.Errorf("batch set expired lease: %w", err)
			}
		}
	}

	// Persist the new lease
	leaseRaw, err := json.Marshal(lease)
	if err != nil {
		return environments.DeploymentLease{}, fmt.Errorf("marshal new lease: %w", err)
	}
	newLeaseKey := KeyDeploymentLeaseForAccount(lease.AccountScopeID, lease.WorkspaceID, lease.ID)
	if err := batch.Set([]byte(newLeaseKey), leaseRaw, nil); err != nil {
		return environments.DeploymentLease{}, fmt.Errorf("batch set new lease: %w", err)
	}

	// Update the active pointer
	if err := batch.Set([]byte(activeKey), []byte(lease.ID), nil); err != nil {
		return environments.DeploymentLease{}, fmt.Errorf("batch set active lease pointer: %w", err)
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return environments.DeploymentLease{}, fmt.Errorf("commit acquire lease batch: %w", err)
	}

	return lease, nil
}

// ReleaseLease atomically releases an active lease, marking it inactive and updating active pointers.
func (s *LeaseStore) ReleaseLease(accountScopeID, workspaceID, leaseID string, reason string) (environments.DeploymentLease, error) {
	if s == nil || s.store == nil {
		return environments.DeploymentLease{}, errors.New("lease store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	leaseID = strings.TrimSpace(leaseID)
	if accountScopeID == "" || workspaceID == "" || leaseID == "" {
		return environments.DeploymentLease{}, errors.New("account scope id, workspace id, and lease id are required")
	}

	lease, found, err := s.Get(accountScopeID, workspaceID, leaseID)
	if err != nil {
		return environments.DeploymentLease{}, err
	}
	if !found {
		return environments.DeploymentLease{}, ErrDeploymentLeaseNotFound
	}

	now := time.Now().UnixMilli()
	if !lease.Active {
		// Idempotent: already released
		return lease, nil
	}

	lease.Active = false
	lease.ReleasedAt = now
	if reason != "" {
		lease.ReleaseReason = strings.TrimSpace(reason)
	} else {
		lease.ReleaseReason = "released"
	}

	batch := s.store.NewBatch()
	defer batch.Close()

	leaseRaw, err := json.Marshal(lease)
	if err != nil {
		return environments.DeploymentLease{}, fmt.Errorf("marshal released lease: %w", err)
	}
	leaseKey := KeyDeploymentLeaseForAccount(accountScopeID, workspaceID, lease.ID)
	if err := batch.Set([]byte(leaseKey), leaseRaw, nil); err != nil {
		return environments.DeploymentLease{}, fmt.Errorf("batch set released lease: %w", err)
	}

	// If the active pointer points to this lease, delete it
	activeKey := KeyDeploymentActiveLeaseForAccount(accountScopeID, workspaceID, lease.DeploymentID)
	activeLeaseIDBytes, ok, err := s.store.GetBytes(activeKey)
	if err == nil && ok && string(activeLeaseIDBytes) == lease.ID {
		if err := batch.Delete([]byte(activeKey), nil); err != nil {
			return environments.DeploymentLease{}, fmt.Errorf("batch delete active lease pointer: %w", err)
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return environments.DeploymentLease{}, fmt.Errorf("commit release lease batch: %w", err)
	}

	return lease, nil
}

// List returns all leases (active and historical) for an account scope and workspace.
func (s *LeaseStore) List(accountScopeID, workspaceID string, limit int) ([]environments.DeploymentLease, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("lease store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	if accountScopeID == "" || workspaceID == "" {
		return nil, errors.New("account scope id and workspace id are required")
	}
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}

	leases := make([]environments.DeploymentLease, 0)
	prefix := DeploymentLeasePrefixForAccount(accountScopeID, workspaceID)

	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		var lease environments.DeploymentLease
		if err := json.Unmarshal(value, &lease); err != nil {
			return fmt.Errorf("unmarshal lease: %w", err)
		}
		if lease.AccountScopeID == accountScopeID && lease.WorkspaceID == workspaceID {
			leases = append(leases, lease)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(leases, func(i, j int) bool {
		if leases[i].AcquiredAt == leases[j].AcquiredAt {
			return leases[i].ID < leases[j].ID
		}
		return leases[i].AcquiredAt > leases[j].AcquiredAt
	})

	return leases, nil
}

// ListActive returns only currently active leases in a workspace.
func (s *LeaseStore) ListActive(accountScopeID, workspaceID string, limit int) ([]environments.DeploymentLease, error) {
	all, err := s.List(accountScopeID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	active := make([]environments.DeploymentLease, 0, len(all))
	for _, l := range all {
		if l.Active {
			active = append(active, l)
		}
	}
	return active, nil
}

// ListForDeployment returns all leases for a specific deployment.
func (s *LeaseStore) ListForDeployment(accountScopeID, workspaceID, deploymentID string, limit int) ([]environments.DeploymentLease, error) {
	all, err := s.List(accountScopeID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	deploymentID = strings.TrimSpace(deploymentID)
	filtered := make([]environments.DeploymentLease, 0, len(all))
	for _, l := range all {
		if l.DeploymentID == deploymentID {
			filtered = append(filtered, l)
		}
	}
	return filtered, nil
}

// ExpireStaleLeases iterates over active leases and expires any that have passed nowMillis.
// Returns the number of leases expired.
func (s *LeaseStore) ExpireStaleLeases(accountScopeID, workspaceID string, nowMillis int64) (int, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("lease store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	active, err := s.ListActive(accountScopeID, workspaceID, 10000)
	if err != nil {
		return 0, err
	}

	expiredCount := 0
	for _, l := range active {
		if l.IsExpired(nowMillis) {
			l.Active = false
			l.ReleasedAt = nowMillis
			l.ReleaseReason = "expired"

			batch := s.store.NewBatch()
			raw, err := json.Marshal(l)
			if err != nil {
				batch.Close()
				return expiredCount, fmt.Errorf("marshal expired lease: %w", err)
			}
			leaseKey := KeyDeploymentLeaseForAccount(accountScopeID, workspaceID, l.ID)
			_ = batch.Set([]byte(leaseKey), raw, nil)

			activeKey := KeyDeploymentActiveLeaseForAccount(accountScopeID, workspaceID, l.DeploymentID)
			activeLeaseIDBytes, ok, err := s.store.GetBytes(activeKey)
			if err == nil && ok && string(activeLeaseIDBytes) == l.ID {
				_ = batch.Delete([]byte(activeKey), nil)
			}
			if err := batch.Commit(pebble.Sync); err != nil {
				batch.Close()
				return expiredCount, fmt.Errorf("commit expired lease batch: %w", err)
			}
			batch.Close()
			expiredCount++
		}
	}

	return expiredCount, nil
}
