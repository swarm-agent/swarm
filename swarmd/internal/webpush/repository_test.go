package webpush

import (
	"context"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPebbleRepositoryPersistsVAPIDPairWithoutJSONExposure(t *testing.T) {
	store := openTestStore(t)
	repository, err := NewPebbleRepository(store)
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.EnsureVAPIDKeyPair(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.EnsureVAPIDKeyPair(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.PrivateKey == "" || first.PublicKey == "" {
		t.Fatalf("unexpected stable key pair: %#v %#v", first, second)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"public_key":"`+first.PublicKey+`"}` {
		t.Fatalf("private key leaked through JSON: %s", encoded)
	}
}

func TestPebbleRepositoryIsolatesAccountsAndHidesCapabilities(t *testing.T) {
	store := openTestStore(t)
	repository, _ := NewPebbleRepository(store)
	input := validSubscriptionInput(t, "https://push.example.test/a")
	created, changed, err := repository.UpsertSubscription(context.Background(), "account-a", input)
	if err != nil || !changed {
		t.Fatalf("upsert: changed=%v err=%v", changed, err)
	}
	if created.ID == "" {
		t.Fatal("subscription id is empty")
	}
	accountA, err := repository.ListStoredSubscriptions(context.Background(), "account-a", 10)
	if err != nil || len(accountA) != 1 {
		t.Fatalf("account A list: records=%d err=%v", len(accountA), err)
	}
	accountB, err := repository.ListStoredSubscriptions(context.Background(), "account-b", 10)
	if err != nil || len(accountB) != 0 {
		t.Fatalf("account B list: records=%d err=%v", len(accountB), err)
	}
	encoded, err := json.Marshal(accountA[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"id":"`+created.ID+`","created_at":`+itoa(created.CreatedAt)+`,"updated_at":`+itoa(created.UpdatedAt)+`}` {
		t.Fatalf("capability leaked through JSON: %s", encoded)
	}
	removed, err := repository.DeleteSubscription(context.Background(), "account-b", created.ID)
	if err != nil || removed {
		t.Fatalf("cross-account delete: removed=%v err=%v", removed, err)
	}
}

func TestPebbleRepositoryRejectsInvalidSubscription(t *testing.T) {
	store := openTestStore(t)
	repository, _ := NewPebbleRepository(store)
	_, _, err := repository.UpsertSubscription(context.Background(), "account-a", SubscriptionInput{
		Endpoint: "http://push.example.test/a",
		Keys:     SubscriptionKeys{Auth: "bad", P256DH: "bad"},
	})
	if err == nil {
		t.Fatal("expected invalid subscription error")
	}
}

func validSubscriptionInput(t *testing.T, endpoint string) SubscriptionInput {
	t.Helper()
	private, x, y, err := elliptic.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = private
	p256dh := elliptic.Marshal(elliptic.P256(), x, y)
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return SubscriptionInput{Endpoint: endpoint, Keys: SubscriptionKeys{
		Auth: base64.RawURLEncoding.EncodeToString(auth), P256DH: base64.RawURLEncoding.EncodeToString(p256dh),
	}}
}

func openTestStore(t *testing.T) *pebblestore.Store {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "secret.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for value > 0 {
		i--
		b[i] = byte('0' + value%10)
		value /= 10
	}
	return string(b[i:])
}
