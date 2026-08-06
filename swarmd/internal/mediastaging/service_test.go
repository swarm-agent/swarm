package mediastaging

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestServiceUsesAccountScopedStoreWithoutSessionAuthority(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "media-staging.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := NewService(pebblestore.NewMediaStagingStore(store))
	payload := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0, 'I', 'H', 'D', 'R'}
	record, replayed, err := service.Put(pebblestore.PutMediaStagingInput{
		AccountScopeID: "account", IdempotencyKey: "upload", DeclaredMIMEType: "image/png", Reader: bytes.NewReader(payload),
	})
	if err != nil || replayed || record.ID == "" || record.BoundSessionID != "" || record.AuthorityAssetID != "" {
		t.Fatalf("put record=%+v replayed=%v err=%v", record, replayed, err)
	}
	got, ok, err := service.Get("account", record.ID)
	if err != nil || !ok || got.ID != record.ID {
		t.Fatalf("get record=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestNilServiceFailsClosed(t *testing.T) {
	var service *Service
	if _, _, err := service.Get("account", "staging"); err == nil {
		t.Fatal("nil service unexpectedly succeeded")
	}
	if _, err := service.CleanupExpired(1, 1); err == nil {
		t.Fatal("nil cleanup unexpectedly succeeded")
	}
}

func TestCleanupExpiredIsBoundedAndReportsSafeRaces(t *testing.T) {
	store := &cleanupStore{
		expired: []pebblestore.MediaStagingExpiry{
			{AccountScopeID: "account", StagingID: "expired"},
			{AccountScopeID: "account", StagingID: "bound"},
			{AccountScopeID: "account", StagingID: "terminal"},
			{AccountScopeID: "account", StagingID: "gone"},
		},
		expireErrors: map[string]error{
			"bound":    pebblestore.ErrMediaStagingAlreadyBound,
			"terminal": pebblestore.ErrMediaStagingNotConsumable,
			"gone":     pebblestore.ErrMediaStagingNotFound,
		},
	}
	report, err := NewService(store).CleanupExpired(2000, 4)
	if err != nil {
		t.Fatalf("cleanup expired: %v", err)
	}
	if store.listLimit != 4 || report.Candidates != 4 || report.Expired != 1 || report.Bound != 1 || report.AlreadyTerminal != 1 || report.NotFound != 1 || !report.More {
		t.Fatalf("limit=%d report=%+v", store.listLimit, report)
	}
	if _, err := NewService(store).CleanupExpired(2000, MaximumCleanupLimit+1); err == nil {
		t.Fatal("unbounded cleanup unexpectedly succeeded")
	}
}

func TestCleanupAbandonedPreflightsAccountAndProtectsBoundRecords(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "media-staging-cleanup.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := NewService(pebblestore.NewMediaStagingStore(store))
	put := func(account, key string) pebblestore.MediaStagingRecord {
		record, _, putErr := service.Put(pebblestore.PutMediaStagingInput{
			AccountScopeID: account, IdempotencyKey: key, DeclaredMIMEType: "image/png", NowUnixMs: 1000,
			TTL: time.Hour, Reader: bytes.NewReader([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0, 'I', 'H', 'D', 'R'}),
		})
		if putErr != nil {
			t.Fatalf("put %s: %v", key, putErr)
		}
		return record
	}
	owned := put("account-a", "owned")
	foreign := put("account-b", "foreign")
	if _, err := service.CleanupAbandoned("account-a", []string{owned.ID, foreign.ID}, 2000); !errors.Is(err, pebblestore.ErrMediaStagingAccountDenied) {
		t.Fatalf("cross-account cleanup error=%v", err)
	}
	if record, ok, getErr := service.Get("account-a", owned.ID); getErr != nil || !ok || record.State != pebblestore.MediaStagingStateStaged {
		t.Fatalf("preflight partially deleted owned record=%+v ok=%v err=%v", record, ok, getErr)
	}

	bound := put("account-a", "bound")
	if _, _, bindErr := service.Bind(pebblestore.BindMediaStagingInput{
		AccountScopeID: "account-a", SessionID: "session", NowUnixMs: 2000,
		Bindings: []pebblestore.MediaStagingBinding{{StagingID: bound.ID, AuthorityAssetID: "asset", DigestSHA256: bound.DigestSHA256}},
	}); bindErr != nil {
		t.Fatalf("bind: %v", bindErr)
	}
	report, err := service.CleanupAbandoned("account-a", []string{owned.ID, bound.ID}, 3000)
	if err != nil || report.Deleted != 1 || report.Bound != 1 {
		t.Fatalf("cleanup report=%+v err=%v", report, err)
	}
	if record, ok, getErr := service.Get("account-a", bound.ID); getErr != nil || !ok || record.State != pebblestore.MediaStagingStateBound {
		t.Fatalf("bound authority changed record=%+v ok=%v err=%v", record, ok, getErr)
	}
}

type cleanupStore struct {
	expired      []pebblestore.MediaStagingExpiry
	expireErrors map[string]error
	listLimit    int
}

func (store *cleanupStore) Put(pebblestore.PutMediaStagingInput) (pebblestore.MediaStagingRecord, bool, error) {
	return pebblestore.MediaStagingRecord{}, false, nil
}
func (store *cleanupStore) Get(string, string) (pebblestore.MediaStagingRecord, bool, error) {
	return pebblestore.MediaStagingRecord{}, true, nil
}
func (store *cleanupStore) Read(string, string, int64) (pebblestore.MediaStagingRecord, []byte, error) {
	return pebblestore.MediaStagingRecord{}, nil, nil
}
func (store *cleanupStore) Delete(string, string, int64) (pebblestore.MediaStagingRecord, bool, error) {
	return pebblestore.MediaStagingRecord{}, false, nil
}
func (store *cleanupStore) Expire(_ string, stagingID string, _ int64) (pebblestore.MediaStagingRecord, bool, error) {
	return pebblestore.MediaStagingRecord{}, false, store.expireErrors[stagingID]
}
func (store *cleanupStore) ListExpired(_ int64, limit int) ([]pebblestore.MediaStagingExpiry, error) {
	store.listLimit = limit
	return store.expired, nil
}
func (store *cleanupStore) Bind(pebblestore.BindMediaStagingInput) ([]pebblestore.MediaStagingRecord, bool, error) {
	return nil, false, nil
}
