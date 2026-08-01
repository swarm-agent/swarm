package mediastaging

import (
	"errors"

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

func (s *Service) Bind(input pebblestore.BindMediaStagingInput) ([]pebblestore.MediaStagingRecord, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, errors.New("media staging service is not configured")
	}
	return s.store.Bind(input)
}
