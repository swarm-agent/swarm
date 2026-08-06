package pebblestore

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var mediaStagingPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0, 'I', 'H', 'D', 'R'}

func openMediaStagingTestStore(t *testing.T) (*Store, *MediaStagingStore) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "staging.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewMediaStagingStore(store)
}

func putMediaStagingTestRecord(t *testing.T, staging *MediaStagingStore, account, key string, now int64) MediaStagingRecord {
	t.Helper()
	record, replayed, err := staging.Put(PutMediaStagingInput{
		AccountScopeID: account, IdempotencyKey: key, DeclaredMIMEType: "image/png",
		FileName: "capture.png", TTL: time.Hour, NowUnixMs: now, Reader: bytes.NewReader(mediaStagingPNG),
	})
	if err != nil || replayed {
		t.Fatalf("put staging record=%+v replayed=%v err=%v", record, replayed, err)
	}
	return record
}

func TestMediaStagingPutReplayIntegrityAndAccountBoundary(t *testing.T) {
	_, staging := openMediaStagingTestStore(t)
	record := putMediaStagingTestRecord(t, staging, "account-a", "upload-1", 1000)
	if record.ID == "" || strings.Contains(record.ID, record.DigestSHA256) || record.ExpiresAt != 1000+time.Hour.Milliseconds() {
		t.Fatalf("unexpected opaque record: %+v", record)
	}
	replayed, wasReplay, err := staging.Put(PutMediaStagingInput{
		AccountScopeID: "account-a", IdempotencyKey: "upload-1", DeclaredMIMEType: "image/png",
		FileName: "capture.png", TTL: time.Hour, NowUnixMs: 2000, Reader: bytes.NewReader(mediaStagingPNG),
	})
	if err != nil || !wasReplay || replayed.ID != record.ID || replayed.CreatedAt != record.CreatedAt {
		t.Fatalf("replay record=%+v replayed=%v err=%v", replayed, wasReplay, err)
	}
	if _, _, err := staging.Put(PutMediaStagingInput{
		AccountScopeID: "account-a", IdempotencyKey: "upload-1", DeclaredMIMEType: "image/png",
		FileName: "different.png", TTL: time.Hour, Reader: bytes.NewReader(mediaStagingPNG),
	}); !errors.Is(err, ErrMediaStagingConflict) {
		t.Fatalf("idempotency conflict error=%v", err)
	}
	if _, ok, err := staging.Get("account-b", record.ID); !errors.Is(err, ErrMediaStagingAccountDenied) || ok {
		t.Fatalf("cross-account get ok=%v err=%v", ok, err)
	}
	readRecord, payload, err := staging.Read("account-a", record.ID, 2000)
	if err != nil || readRecord.ID != record.ID || !bytes.Equal(payload, mediaStagingPNG) {
		t.Fatalf("read record=%+v payload=%x err=%v", readRecord, payload, err)
	}
}

func TestMediaStagingRejectedWritesAreNotPersisted(t *testing.T) {
	_, staging := openMediaStagingTestStore(t)
	tests := []PutMediaStagingInput{
		{AccountScopeID: "account", IdempotencyKey: "empty", DeclaredMIMEType: "image/png", Reader: bytes.NewReader(nil)},
		{AccountScopeID: "account", IdempotencyKey: "spoof", DeclaredMIMEType: "image/jpeg", Reader: bytes.NewReader(mediaStagingPNG)},
		{AccountScopeID: "account", IdempotencyKey: "large", DeclaredMIMEType: "image/png", MaxBytes: 4, Reader: bytes.NewReader(mediaStagingPNG)},
		{AccountScopeID: "account", IdempotencyKey: "ttl", DeclaredMIMEType: "image/png", TTL: MediaStagingMaximumTTL + time.Second, Reader: bytes.NewReader(mediaStagingPNG)},
	}
	for _, input := range tests {
		if _, _, err := staging.Put(input); err == nil {
			t.Fatalf("expected rejected write for %q", input.IdempotencyKey)
		}
	}
	count := 0
	if err := staging.store.IteratePrefix(MediaStagingRecordPrefix("account"), 10, func(_ string, _ []byte) error { count++; return nil }); err != nil {
		t.Fatalf("list records: %v", err)
	}
	if count != 0 {
		t.Fatalf("rejected writes persisted %d records", count)
	}
}

func TestMediaStagingQuotaDeleteAndExpiry(t *testing.T) {
	_, staging := openMediaStagingTestStore(t)
	first, _, err := staging.Put(PutMediaStagingInput{
		AccountScopeID: "account", IdempotencyKey: "first", DeclaredMIMEType: "image/png",
		MaxCount: 1, QuotaAssets: 1, NowUnixMs: 1000, TTL: time.Second, Reader: bytes.NewReader(mediaStagingPNG),
	})
	if err != nil {
		t.Fatalf("put first: %v", err)
	}
	if _, _, err := staging.Put(PutMediaStagingInput{
		AccountScopeID: "account", IdempotencyKey: "second", DeclaredMIMEType: "image/png",
		MaxCount: 1, QuotaAssets: 1, Reader: bytes.NewReader(mediaStagingPNG),
	}); err == nil {
		t.Fatal("expected count quota rejection")
	}
	if _, _, err := staging.Expire("account", first.ID, 1500); !errors.Is(err, ErrMediaStagingNotConsumable) {
		t.Fatalf("early expiry error=%v", err)
	}
	due, err := staging.ListExpired(2000, 10)
	if err != nil || len(due) != 1 || due[0].StagingID != first.ID {
		t.Fatalf("due=%+v err=%v", due, err)
	}
	expired, replayed, err := staging.Expire("account", first.ID, 2000)
	if err != nil || replayed || expired.State != MediaStagingStateExpired {
		t.Fatalf("expire record=%+v replayed=%v err=%v", expired, replayed, err)
	}
	if _, _, err := staging.Read("account", first.ID, 2000); !errors.Is(err, ErrMediaStagingNotConsumable) {
		t.Fatalf("read expired error=%v", err)
	}
	if _, replayed, err := staging.Expire("account", first.ID, 3000); err != nil || !replayed {
		t.Fatalf("expire replayed=%v err=%v", replayed, err)
	}

	second := putMediaStagingTestRecord(t, staging, "account", "delete", 3000)
	deleted, replayed, err := staging.Delete("account", second.ID, 4000)
	if err != nil || replayed || deleted.State != MediaStagingStateDeleted {
		t.Fatalf("delete record=%+v replayed=%v err=%v", deleted, replayed, err)
	}
}

func TestMediaStagingAtomicBindAndConsume(t *testing.T) {
	_, staging := openMediaStagingTestStore(t)
	first := putMediaStagingTestRecord(t, staging, "account", "one", 1000)
	second := putMediaStagingTestRecord(t, staging, "account", "two", 1000)
	if _, _, err := staging.Bind(BindMediaStagingInput{
		AccountScopeID: "account", SessionID: "session", NowUnixMs: 2000,
		Bindings: []MediaStagingBinding{
			{StagingID: first.ID, AuthorityAssetID: "media-one", DigestSHA256: first.DigestSHA256},
			{StagingID: second.ID, AuthorityAssetID: "media-two", DigestSHA256: strings.Repeat("0", 64)},
		},
	}); !errors.Is(err, ErrMediaStagingIntegrity) {
		t.Fatalf("failed bind error=%v", err)
	}
	if _, _, err := staging.Read("account", first.ID, 2000); err != nil {
		t.Fatalf("failed bind consumed first record: %v", err)
	}

	bindings := []MediaStagingBinding{
		{StagingID: first.ID, AuthorityAssetID: "media-one", DigestSHA256: first.DigestSHA256},
		{StagingID: second.ID, AuthorityAssetID: "media-two", DigestSHA256: second.DigestSHA256},
	}
	bound, replayed, err := staging.Bind(BindMediaStagingInput{AccountScopeID: "account", SessionID: "session", NowUnixMs: 2000, Bindings: bindings})
	if err != nil || replayed || len(bound) != 2 || bound[0].State != MediaStagingStateBound {
		t.Fatalf("bind records=%+v replayed=%v err=%v", bound, replayed, err)
	}
	if _, _, err := staging.Read("account", first.ID, 2000); !errors.Is(err, ErrMediaStagingNotConsumable) {
		t.Fatalf("bound bytes remained readable: %v", err)
	}
	if _, replayed, err := staging.Bind(BindMediaStagingInput{AccountScopeID: "account", SessionID: "session", Bindings: bindings}); err != nil || !replayed {
		t.Fatalf("bind replay replayed=%v err=%v", replayed, err)
	}
	if _, _, err := staging.Delete("account", first.ID, 3000); !errors.Is(err, ErrMediaStagingAlreadyBound) {
		t.Fatalf("bound delete error=%v", err)
	}
}
