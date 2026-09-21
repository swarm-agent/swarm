package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// DeploymentStore manages persistence of instantiated Deployment records in Pebble.
// Persists runtime metadata (container_id, provider_resource_id, endpoints, ports, status, health)
// while strictly verifying that NO secrets, credentials, or private keys are stored.
// Strictly scoped to AccountScopeID and WorkspaceID.
type DeploymentStore struct {
	store      *Store
	leases     *LeaseStore
	operations *EnvironmentOperationStore
	mu         sync.Mutex
}

// NewDeploymentStore creates a new DeploymentStore backed by the given Pebble Store.
func NewDeploymentStore(store *Store) *DeploymentStore {
	return &DeploymentStore{
		store:      store,
		leases:     NewLeaseStore(store),
		operations: NewEnvironmentOperationStore(store),
	}
}

// Operations returns the underlying EnvironmentOperationStore.
func (s *DeploymentStore) Operations() *EnvironmentOperationStore {
	if s == nil {
		return nil
	}
	return s.operations
}

// Leases returns the underlying LeaseStore.
func (s *DeploymentStore) Leases() *LeaseStore {
	if s == nil {
		return nil
	}
	return s.leases
}

// AcquireLease atomically acquires an exclusive lease for a deployment.
func (s *DeploymentStore) AcquireLease(lease environments.DeploymentLease) (environments.DeploymentLease, error) {
	if s == nil || s.leases == nil {
		return environments.DeploymentLease{}, errors.New("deployment store is not configured")
	}
	return s.leases.AcquireLease(lease)
}

// ReleaseLease atomically releases an active lease.
func (s *DeploymentStore) ReleaseLease(accountScopeID, workspaceID, leaseID string, reason string) (environments.DeploymentLease, error) {
	if s == nil || s.leases == nil {
		return environments.DeploymentLease{}, errors.New("deployment store is not configured")
	}
	return s.leases.ReleaseLease(accountScopeID, workspaceID, leaseID, reason)
}

// GetActiveLease retrieves the current active lease for a deployment.
func (s *DeploymentStore) GetActiveLease(accountScopeID, workspaceID, deploymentID string) (environments.DeploymentLease, bool, error) {
	if s == nil || s.leases == nil {
		return environments.DeploymentLease{}, false, errors.New("deployment store is not configured")
	}
	return s.leases.GetActiveLease(accountScopeID, workspaceID, deploymentID)
}

// Get retrieves a Deployment by its account scope, workspace id, and deployment id.
func (s *DeploymentStore) Get(accountScopeID, workspaceID, deploymentID string) (environments.Deployment, bool, error) {
	if s == nil || s.store == nil {
		return environments.Deployment{}, false, errors.New("deployment store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	deploymentID = strings.TrimSpace(deploymentID)
	if accountScopeID == "" || workspaceID == "" || deploymentID == "" {
		return environments.Deployment{}, false, errors.New("account scope id, workspace id, and deployment id are required")
	}

	key := KeyDeploymentForAccount(accountScopeID, workspaceID, deploymentID)
	var dep environments.Deployment
	ok, err := s.store.GetJSON(key, &dep)
	if err != nil || !ok {
		return environments.Deployment{}, ok, err
	}

	// Enforce strict isolation
	if dep.AccountScopeID != accountScopeID || dep.WorkspaceID != workspaceID {
		return environments.Deployment{}, false, nil
	}

	return dep, true, nil
}

// List returns all deployments for an account scope and workspace, sorted by updated timestamp descending.
func (s *DeploymentStore) List(accountScopeID, workspaceID string, limit int) ([]environments.Deployment, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("deployment store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	if accountScopeID == "" || workspaceID == "" {
		return nil, errors.New("account scope id and workspace id are required")
	}
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}

	deployments := make([]environments.Deployment, 0)
	prefix := DeploymentPrefixForAccount(accountScopeID, workspaceID)

	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		var dep environments.Deployment
		if err := json.Unmarshal(value, &dep); err != nil {
			return fmt.Errorf("unmarshal deployment: %w", err)
		}
		if dep.AccountScopeID == accountScopeID && dep.WorkspaceID == workspaceID {
			deployments = append(deployments, dep)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(deployments, func(i, j int) bool {
		if deployments[i].UpdatedAt == deployments[j].UpdatedAt {
			return deployments[i].ID < deployments[j].ID
		}
		return deployments[i].UpdatedAt > deployments[j].UpdatedAt
	})

	return deployments, nil
}

// ListByEnvironment returns deployments for a specific environment definition in a workspace.
func (s *DeploymentStore) ListByEnvironment(accountScopeID, workspaceID, environmentID string, limit int) ([]environments.Deployment, error) {
	all, err := s.List(accountScopeID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	environmentID = strings.TrimSpace(environmentID)
	filtered := make([]environments.Deployment, 0, len(all))
	for _, dep := range all {
		if dep.EnvironmentID == environmentID {
			filtered = append(filtered, dep)
		}
	}
	return filtered, nil
}

// Save persists a Deployment, ensuring domain validation, secret verification, and proper timestamps.
func (s *DeploymentStore) Save(dep environments.Deployment) (environments.Deployment, error) {
	if s == nil || s.store == nil {
		return environments.Deployment{}, errors.New("deployment store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := dep.Validate(); err != nil {
		return environments.Deployment{}, fmt.Errorf("validate deployment: %w", err)
	}

	// Serialize and verify that no credentials or secrets exist in the payload
	raw, err := json.Marshal(dep)
	if err != nil {
		return environments.Deployment{}, fmt.Errorf("marshal deployment: %w", err)
	}
	if err := environments.AssertNoSecretsRaw(raw); err != nil {
		return environments.Deployment{}, fmt.Errorf("security check failed: %w", err)
	}

	now := time.Now().UnixMilli()
	current, found, err := s.Get(dep.AccountScopeID, dep.WorkspaceID, dep.ID)
	if err != nil {
		return environments.Deployment{}, err
	}
	if found {
		dep.CreatedAt = current.CreatedAt
		if dep.Lifecycle.CreatedAt <= 0 {
			dep.Lifecycle.CreatedAt = current.Lifecycle.CreatedAt
		}
	} else {
		if dep.CreatedAt <= 0 {
			dep.CreatedAt = now
		}
		if dep.Lifecycle.CreatedAt <= 0 {
			dep.Lifecycle.CreatedAt = now
		}
	}
	dep.UpdatedAt = now

	if err := s.persistDeploymentAtomic(dep, false); err != nil {
		return environments.Deployment{}, fmt.Errorf("persist deployment atomic: %w", err)
	}

	return dep, nil
}

// UpdateStatus updates the status, health, and error message of an existing deployment.
func (s *DeploymentStore) UpdateStatus(accountScopeID, workspaceID, deploymentID string, status environments.DeploymentStatus, health environments.HealthStatus, errMsg string) (environments.Deployment, error) {
	if s == nil || s.store == nil {
		return environments.Deployment{}, errors.New("deployment store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dep, found, err := s.Get(accountScopeID, workspaceID, deploymentID)
	if err != nil {
		return environments.Deployment{}, err
	}
	if !found {
		return environments.Deployment{}, fmt.Errorf("deployment %q not found", deploymentID)
	}

	now := time.Now().UnixMilli()
	dep.Status = status
	if health != "" {
		dep.Health = health
	}
	dep.ErrorMessage = errMsg
	dep.UpdatedAt = now
	dep.Lifecycle.LastActiveAt = now

	switch status {
	case environments.DeploymentStatusStarting, environments.DeploymentStatusProvisioning:
		if dep.Lifecycle.StartedAt <= 0 {
			dep.Lifecycle.StartedAt = now
		}
	case environments.DeploymentStatusRunning, environments.DeploymentStatusReady, environments.DeploymentStatusBusy:
		if dep.Lifecycle.ReadyAt <= 0 {
			dep.Lifecycle.ReadyAt = now
		}
	case environments.DeploymentStatusStopping, environments.DeploymentStatusStopped:
		if dep.Lifecycle.StoppedAt <= 0 {
			dep.Lifecycle.StoppedAt = now
		}
	case environments.DeploymentStatusTerminated:
		if dep.Lifecycle.TerminatedAt <= 0 {
			dep.Lifecycle.TerminatedAt = now
		}
	}

	if err := dep.Validate(); err != nil {
		return environments.Deployment{}, fmt.Errorf("validate deployment: %w", err)
	}

	if err := s.persistDeploymentAtomic(dep, false); err != nil {
		return environments.Deployment{}, fmt.Errorf("persist deployment atomic: %w", err)
	}

	return dep, nil
}

// UpdateRuntime updates the runtime metadata of an existing deployment.
func (s *DeploymentStore) UpdateRuntime(accountScopeID, workspaceID, deploymentID string, runtime environments.RuntimeMetadata) (environments.Deployment, error) {
	if s == nil || s.store == nil {
		return environments.Deployment{}, errors.New("deployment store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dep, found, err := s.Get(accountScopeID, workspaceID, deploymentID)
	if err != nil {
		return environments.Deployment{}, err
	}
	if !found {
		return environments.Deployment{}, fmt.Errorf("deployment %q not found", deploymentID)
	}

	// Verify no secrets in runtime metadata
	raw, err := json.Marshal(runtime)
	if err != nil {
		return environments.Deployment{}, fmt.Errorf("marshal runtime metadata: %w", err)
	}
	if err := environments.AssertNoSecretsRaw(raw); err != nil {
		return environments.Deployment{}, fmt.Errorf("security check failed in runtime metadata: %w", err)
	}

	now := time.Now().UnixMilli()
	dep.Runtime = runtime
	dep.UpdatedAt = now
	dep.Lifecycle.LastActiveAt = now

	if err := dep.Validate(); err != nil {
		return environments.Deployment{}, fmt.Errorf("validate deployment: %w", err)
	}

	if err := s.persistDeploymentAtomic(dep, false); err != nil {
		return environments.Deployment{}, fmt.Errorf("persist deployment runtime atomic: %w", err)
	}

	return dep, nil
}

// Delete removes a Deployment record by account scope, workspace id, and deployment id.
func (s *DeploymentStore) Delete(accountScopeID, workspaceID, deploymentID string) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("deployment store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	deploymentID = strings.TrimSpace(deploymentID)
	if accountScopeID == "" || workspaceID == "" || deploymentID == "" {
		return false, errors.New("account scope id, workspace id, and deployment id are required")
	}

	dep, found, err := s.Get(accountScopeID, workspaceID, deploymentID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}

	if err := s.persistDeploymentAtomic(dep, true); err != nil {
		return false, fmt.Errorf("delete deployment atomic: %w", err)
	}

	return true, nil
}

// persistDeploymentAtomic updates a deployment and recalculates summary active deployment counts
// within a single atomic Pebble batch using the private environment participant.
func (s *DeploymentStore) persistDeploymentAtomic(dep environments.Deployment, isDelete bool) error {
	var mutationToPublish *environmentRealtimeMutation
	defer func() {
		if mutationToPublish != nil {
			s.store.publishEnvironmentRealtime(mutationToPublish)
		}
	}()

	s.store.environmentsMu.Lock()
	defer s.store.environmentsMu.Unlock()

	key := KeyDeploymentForAccount(dep.AccountScopeID, dep.WorkspaceID, dep.ID)
	mutation := &environmentRealtimeMutation{
		accountScopeID: dep.AccountScopeID,
		workspaceID:    dep.WorkspaceID,
		writes:         make(map[string][]byte),
	}

	if isDelete {
		mutation.delete(key)
	} else {
		raw, err := json.Marshal(dep)
		if err != nil {
			return fmt.Errorf("marshal deployment: %w", err)
		}
		mutation.putBytes(key, raw)
	}

	// Update summary active deployments count atomically
	var summary environments.EnvironmentSummary
	if s.operations != nil {
		sSummary, err := s.operations.getSummaryLocked(dep.AccountScopeID, dep.WorkspaceID)
		if err == nil {
			summary = sSummary
		} else {
			summary = environments.EnvironmentSummary{
				AccountScopeID: dep.AccountScopeID,
				WorkspaceID:    dep.WorkspaceID,
				Revision:       1,
				UpdatedAt:      time.Now().UnixMilli(),
			}
		}
	} else {
		summary = environments.EnvironmentSummary{
			AccountScopeID: dep.AccountScopeID,
			WorkspaceID:    dep.WorkspaceID,
			Revision:       1,
			UpdatedAt:      time.Now().UnixMilli(),
		}
	}

	activeCount := 0
	depPrefix := DeploymentPrefixForAccount(dep.AccountScopeID, dep.WorkspaceID)
	_ = s.store.IteratePrefix(depPrefix, 10000, func(k string, value []byte) error {
		if k == key {
			if !isDelete && dep.IsActive() {
				activeCount++
			}
			return nil
		}
		var d environments.Deployment
		if err := json.Unmarshal(value, &d); err == nil {
			if d.AccountScopeID == dep.AccountScopeID && d.WorkspaceID == dep.WorkspaceID && d.IsActive() {
				activeCount++
			}
		}
		return nil
	})
	if !isDelete {
		var existing environments.Deployment
		if ok, _ := s.store.GetJSON(key, &existing); !ok && dep.IsActive() {
			activeCount++
		}
	}

	summary.ActiveDeployments = activeCount
	summary.Revision++
	summary.UpdatedAt = time.Now().UnixMilli()

	summaryRaw, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal summary: %w", err)
	}
	summaryKey := KeyEnvironmentSummaryForAccount(dep.AccountScopeID, dep.WorkspaceID)
	mutation.putBytes(summaryKey, summaryRaw)

	metaPayload, _ := json.Marshal(map[string]any{
		"account_scope_id": dep.AccountScopeID,
		"workspace_id":    dep.WorkspaceID,
		"summary_revision": summary.Revision,
	})
	mutation.eventPayload = metaPayload

	if err := s.store.commitEnvironmentRealtime(mutation); err != nil {
		return err
	}
	mutationToPublish = mutation
	return nil
}
