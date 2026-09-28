package session

import (
	"context"
	"errors"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// CreateWorker creates an account-owned stable worker in the idle lifecycle state.
// Workers exist independently of authoring sessions or plans.
func (s *Service) CreateWorker(_ context.Context, account, user string, req pebblestore.CreateWorkerRequest) (pebblestore.WorkerRecord, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkerRecord{}, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.WorkerRecord{}, errors.New("worker store not configured")
	}
	return ws.CreateWorker(account, user, req, ValidateExecutablePlanDocument)
}

// GetWorker fetches a worker by ID or attached automation ID within the account scope.
func (s *Service) GetWorker(account, workerID string) (pebblestore.WorkerRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkerRecord{}, false, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.WorkerRecord{}, false, errors.New("worker store not configured")
	}
	return ws.GetWorker(account, workerID)
}

// ListWorkers returns a paginated list of workers for the given account.
func (s *Service) ListWorkers(account string, query pebblestore.ListWorkersQuery) (pebblestore.ListWorkersResult, error) {
	if s == nil || s.store == nil {
		return pebblestore.ListWorkersResult{}, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.ListWorkersResult{}, errors.New("worker store not configured")
	}
	return ws.ListWorkers(account, query)
}

// UpdateWorker updates a worker's definition guarded by optimistic revision check.
// Updates to active scheduled workers are rejected until checkpoint 2 safe stop controls exist.
func (s *Service) UpdateWorker(account, user, workerID string, expectedRevision uint64, req pebblestore.UpdateWorkerRequest) (pebblestore.WorkerRecord, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkerRecord{}, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.WorkerRecord{}, errors.New("worker store not configured")
	}
	return ws.UpdateWorker(account, user, workerID, expectedRevision, req, ValidateExecutablePlanDocument)
}

// DeleteWorker tombstones a worker, recording the event and revision in history.
func (s *Service) DeleteWorker(account, user, workerID string, expectedRevision uint64) error {
	if s == nil || s.store == nil {
		return errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return errors.New("worker store not configured")
	}
	return ws.DeleteWorker(account, user, workerID, expectedRevision)
}

// GetWorkerHistory retrieves revision history for a worker with pagination.
func (s *Service) GetWorkerHistory(account, workerID string, limit int, after string) ([]pebblestore.WorkerRevisionRecord, string, error) {
	if s == nil || s.store == nil {
		return nil, "", errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return nil, "", errors.New("worker store not configured")
	}
	return ws.ListWorkerRevisions(account, workerID, limit, after)
}

// GetWorkerRevision retrieves a specific pinned revision snapshot of a worker.
func (s *Service) GetWorkerRevision(account, workerID string, revision uint64) (pebblestore.WorkerRevisionRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkerRevisionRecord{}, false, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.WorkerRevisionRecord{}, false, errors.New("worker store not configured")
	}
	return ws.GetWorkerRevision(account, workerID, revision)
}

// AttachWorkerAutomation attaches a new automation definition with an executable plan to the worker.
func (s *Service) AttachWorkerAutomation(account, user, workerID string, expectedWorkerRevision uint64, auto pebblestore.WorkerAutomationDefinition) (pebblestore.WorkerRecord, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkerRecord{}, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.WorkerRecord{}, errors.New("worker store not configured")
	}
	return ws.AttachWorkerAutomation(account, user, workerID, expectedWorkerRevision, auto, ValidateExecutablePlanDocument)
}

// UpdateWorkerAutomation updates an existing attached automation definition.
func (s *Service) UpdateWorkerAutomation(account, user, workerID, automationID string, expectedWorkerRevision uint64, auto pebblestore.WorkerAutomationDefinition) (pebblestore.WorkerRecord, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkerRecord{}, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.WorkerRecord{}, errors.New("worker store not configured")
	}
	return ws.UpdateWorkerAutomation(account, user, workerID, automationID, expectedWorkerRevision, auto, ValidateExecutablePlanDocument)
}

// RemoveWorkerAutomation detaches an automation definition from the worker.
func (s *Service) RemoveWorkerAutomation(account, user, workerID, automationID string, expectedWorkerRevision uint64) (pebblestore.WorkerRecord, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkerRecord{}, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.WorkerRecord{}, errors.New("worker store not configured")
	}
	return ws.RemoveWorkerAutomation(account, user, workerID, automationID, expectedWorkerRevision)
}

// ValidatePortableWorkerDefinition parses and strictly validates a JSON worker document.
func (s *Service) ValidatePortableWorkerDefinition(data []byte) (pebblestore.PortableWorkerDefinition, error) {
	return pebblestore.ValidatePortableWorkerDefinition(data, ValidateExecutablePlanDocument)
}

// ExportWorker exports the complete portable worker definition as indented JSON.
func (s *Service) ExportWorker(account, workerID string) (pebblestore.PortableWorkerDefinition, []byte, error) {
	if s == nil || s.store == nil {
		return pebblestore.PortableWorkerDefinition{}, nil, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.PortableWorkerDefinition{}, nil, errors.New("worker store not configured")
	}
	return ws.ExportWorker(account, workerID)
}

// ImportWorkerAsNew allocates a fresh identity and imports the portable definition into the idle state.
func (s *Service) ImportWorkerAsNew(account, user string, data []byte, idempotencyKey ...string) (pebblestore.WorkerRecord, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkerRecord{}, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.WorkerRecord{}, errors.New("worker store not configured")
	}
	return ws.ImportWorkerAsNew(account, user, data, ValidateExecutablePlanDocument, idempotencyKey...)
}

// ImportWorkerUpdate strictly validates and updates an existing target worker with expected revision.
func (s *Service) ImportWorkerUpdate(account, user, workerID string, expectedRevision uint64, data []byte) (pebblestore.WorkerRecord, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkerRecord{}, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.WorkerRecord{}, errors.New("worker store not configured")
	}
	return ws.ImportWorkerUpdate(account, user, workerID, expectedRevision, data, ValidateExecutablePlanDocument)
}

// RecordWorkerRun links a run occurrence to a worker and its pinned revision.
func (s *Service) RecordWorkerRun(account string, run pebblestore.WorkerRunRecord) (pebblestore.WorkerRunRecord, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkerRunRecord{}, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.WorkerRunRecord{}, errors.New("worker store not configured")
	}
	return ws.RecordWorkerRun(account, run)
}

// GetWorkerRun retrieves a worker run record by worker ID and run ID.
func (s *Service) GetWorkerRun(account, workerID, runID string) (pebblestore.WorkerRunRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkerRunRecord{}, false, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.WorkerRunRecord{}, false, errors.New("worker store not configured")
	}
	return ws.GetWorkerRun(account, workerID, runID)
}

// ListWorkerRuns retrieves paginated run records for a worker.
func (s *Service) ListWorkerRuns(account, workerID string, limit int, after string) ([]pebblestore.WorkerRunRecord, string, error) {
	if s == nil || s.store == nil {
		return nil, "", errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return nil, "", errors.New("worker store not configured")
	}
	return ws.ListWorkerRuns(account, workerID, limit, after)
}

// MigrateLegacyAutomationsV2 idempotently migrates accepted legacy AutomationV2Records into durable WorkerRecords.
func (s *Service) MigrateLegacyAutomationsV2(account string) (pebblestore.WorkerMigrationSummary, error) {
	if s == nil || s.store == nil {
		return pebblestore.WorkerMigrationSummary{}, errors.New("session service not configured")
	}
	ws := s.store.WorkerStore()
	if ws == nil {
		return pebblestore.WorkerMigrationSummary{}, errors.New("worker store not configured")
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return pebblestore.WorkerMigrationSummary{}, errors.New("account is required")
	}
	return ws.MigrateLegacyAutomationsV2(account)
}
