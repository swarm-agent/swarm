package mediastaging

import (
	"bytes"
	"path/filepath"
	"testing"

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
}
