package mediastaging

import (
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Store is the persistence contract used by pre-session API and cleanup
// surfaces. It deliberately exposes no session creation or model selection.
type Store interface {
	Put(pebblestore.PutMediaStagingInput) (pebblestore.MediaStagingRecord, bool, error)
	Get(accountScopeID, stagingID string) (pebblestore.MediaStagingRecord, bool, error)
	Read(accountScopeID, stagingID string, nowUnixMs int64) (pebblestore.MediaStagingRecord, []byte, error)
	Delete(accountScopeID, stagingID string, nowUnixMs int64) (pebblestore.MediaStagingRecord, bool, error)
	Expire(accountScopeID, stagingID string, nowUnixMs int64) (pebblestore.MediaStagingRecord, bool, error)
	ListExpired(nowUnixMs int64, limit int) ([]pebblestore.MediaStagingExpiry, error)
	Bind(pebblestore.BindMediaStagingInput) ([]pebblestore.MediaStagingRecord, bool, error)
}

const (
	DefaultCleanupLimit = 100
	MaximumCleanupLimit = 1000
)

// CleanupReport accounts for every bounded cleanup candidate. Race counters are
// explicit so callers can distinguish safe no-ops from actual cleanup failures.
type CleanupReport struct {
	Limit           int
	Candidates      int
	Expired         int
	Deleted         int
	AlreadyTerminal int
	Bound           int
	NotFound        int
	Failed          int
	More            bool
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) Put(input pebblestore.PutMediaStagingInput) (pebblestore.MediaStagingRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.MediaStagingRecord{}, false, errors.New("media staging service is not configured")
	}
	return s.store.Put(input)
}

func (s *Service) Get(accountScopeID, stagingID string) (pebblestore.MediaStagingRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.MediaStagingRecord{}, false, errors.New("media staging service is not configured")
	}
	return s.store.Get(accountScopeID, stagingID)
}

func (s *Service) Read(accountScopeID, stagingID string, nowUnixMs int64) (pebblestore.MediaStagingRecord, []byte, error) {
	if s == nil || s.store == nil {
		return pebblestore.MediaStagingRecord{}, nil, errors.New("media staging service is not configured")
	}
	return s.store.Read(accountScopeID, stagingID, nowUnixMs)
}

func (s *Service) Delete(accountScopeID, stagingID string, nowUnixMs int64) (pebblestore.MediaStagingRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.MediaStagingRecord{}, false, errors.New("media staging service is not configured")
	}
	return s.store.Delete(accountScopeID, stagingID, nowUnixMs)
}

func (s *Service) Expire(accountScopeID, stagingID string, nowUnixMs int64) (pebblestore.MediaStagingRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.MediaStagingRecord{}, false, errors.New("media staging service is not configured")
	}
	return s.store.Expire(accountScopeID, stagingID, nowUnixMs)
}

func (s *Service) ListExpired(nowUnixMs int64, limit int) ([]pebblestore.MediaStagingExpiry, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("media staging service is not configured")
	}
	return s.store.ListExpired(nowUnixMs, limit)
}

// CleanupExpired expires at most limit due staging records. Candidates can be
// bound or terminal by a concurrent request after listing; those races are safe
// no-ops and are reported rather than treated as successful expiry.
func (s *Service) CleanupExpired(nowUnixMs int64, limit int) (CleanupReport, error) {
	report := CleanupReport{Limit: limit}
	if s == nil || s.store == nil {
		return report, errors.New("media staging service is not configured")
	}
	if limit <= 0 || limit > MaximumCleanupLimit {
		return report, fmt.Errorf("media staging cleanup limit must be between 1 and %d", MaximumCleanupLimit)
	}
	candidates, err := s.store.ListExpired(nowUnixMs, limit)
	if err != nil {
		return report, fmt.Errorf("list expired media staging records: %w", err)
	}
	if len(candidates) > limit {
		return report, fmt.Errorf("media staging expiry scan returned %d candidates for limit %d", len(candidates), limit)
	}
	report.Candidates = len(candidates)
	report.More = len(candidates) == limit
	var failures []error
	for _, candidate := range candidates {
		_, replayed, expireErr := s.store.Expire(candidate.AccountScopeID, candidate.StagingID, nowUnixMs)
		switch {
		case expireErr == nil && replayed:
			report.AlreadyTerminal++
		case expireErr == nil:
			report.Expired++
		case errors.Is(expireErr, pebblestore.ErrMediaStagingAlreadyBound):
			report.Bound++
		case errors.Is(expireErr, pebblestore.ErrMediaStagingNotFound):
			report.NotFound++
		case errors.Is(expireErr, pebblestore.ErrMediaStagingNotConsumable):
			report.AlreadyTerminal++
		default:
			report.Failed++
			failures = append(failures, fmt.Errorf("expire media staging record %q: %w", candidate.StagingID, expireErr))
		}
	}
	return report, errors.Join(failures...)
}

// CleanupAbandoned deletes a bounded set of unbound staging records belonging
// to one authenticated account. It preflights the complete set before deletion,
// so a cross-account ID fails closed without partially cleaning the request.
func (s *Service) CleanupAbandoned(accountScopeID string, stagingIDs []string, nowUnixMs int64) (CleanupReport, error) {
	report := CleanupReport{Limit: MaximumCleanupLimit, Candidates: len(stagingIDs)}
	if s == nil || s.store == nil {
		return report, errors.New("media staging service is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return report, errors.New("media staging cleanup account scope is required")
	}
	if len(stagingIDs) > MaximumCleanupLimit {
		return report, fmt.Errorf("media staging cleanup count exceeds %d", MaximumCleanupLimit)
	}

	ids := make([]string, 0, len(stagingIDs))
	seen := make(map[string]struct{}, len(stagingIDs))
	for _, stagingID := range stagingIDs {
		stagingID = strings.TrimSpace(stagingID)
		if stagingID == "" {
			return report, errors.New("media staging cleanup ID is required")
		}
		if _, duplicate := seen[stagingID]; duplicate {
			return report, fmt.Errorf("duplicate media staging cleanup ID %q", stagingID)
		}
		seen[stagingID] = struct{}{}
		if _, _, err := s.store.Get(accountScopeID, stagingID); err != nil {
			return report, fmt.Errorf("authorize media staging cleanup ID %q: %w", stagingID, err)
		}
		ids = append(ids, stagingID)
	}

	var failures []error
	for _, stagingID := range ids {
		_, replayed, deleteErr := s.store.Delete(accountScopeID, stagingID, nowUnixMs)
		switch {
		case deleteErr == nil && replayed:
			report.AlreadyTerminal++
		case deleteErr == nil:
			report.Deleted++
		case errors.Is(deleteErr, pebblestore.ErrMediaStagingAlreadyBound):
			report.Bound++
		case errors.Is(deleteErr, pebblestore.ErrMediaStagingNotFound):
			report.NotFound++
		case errors.Is(deleteErr, pebblestore.ErrMediaStagingNotConsumable):
			report.AlreadyTerminal++
		case errors.Is(deleteErr, pebblestore.ErrMediaStagingAccountDenied):
			report.Failed++
			failures = append(failures, fmt.Errorf("media staging ownership changed for %q: %w", stagingID, deleteErr))
		default:
			report.Failed++
			failures = append(failures, fmt.Errorf("delete abandoned media staging record %q: %w", stagingID, deleteErr))
		}
	}
	return report, errors.Join(failures...)
}

func (s *Service) Bind(input pebblestore.BindMediaStagingInput) ([]pebblestore.MediaStagingRecord, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, errors.New("media staging service is not configured")
	}
	return s.store.Bind(input)
}
