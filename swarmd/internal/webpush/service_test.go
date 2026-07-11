package webpush

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	pushlib "github.com/SherClockHolmes/webpush-go"
)

func TestServiceSendDeliversAndRemovesExpiredSubscriptions(t *testing.T) {
	repository := &memoryRepository{
		pair: VAPIDKeyPair{PublicKey: "public", PrivateKey: "private"},
		records: []StoredSubscription{
			{Subscription: Subscription{ID: "wps_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, AccountScopeID: "account-a", Endpoint: "https://push.example.test/ok", Auth: "auth", P256DH: "p256dh"},
			{Subscription: Subscription{ID: "wps_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, AccountScopeID: "account-a", Endpoint: "https://push.example.test/gone", Auth: "auth", P256DH: "p256dh"},
		},
	}
	sender := senderFunc(func(_ context.Context, _ []byte, subscription *pushlib.Subscription, _ *pushlib.Options) (*http.Response, error) {
		status := http.StatusCreated
		if strings.HasSuffix(subscription.Endpoint, "/gone") {
			status = http.StatusGone
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("response"))}, nil
	})
	service, err := NewServiceWithSender(repository, "mailto:push@example.test", sender)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Send(context.Background(), "account-a", Payload{Title: "Permission requested"}, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempted != 2 || result.Delivered != 1 || result.Removed != 1 || len(repository.deleted) != 1 {
		t.Fatalf("unexpected result: %#v deleted=%v", result, repository.deleted)
	}
}

func TestServiceSendReturnsObservablePartialErrors(t *testing.T) {
	repository := &memoryRepository{
		pair: VAPIDKeyPair{PublicKey: "public", PrivateKey: "private"},
		records: []StoredSubscription{{
			Subscription: Subscription{ID: "wps_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, AccountScopeID: "account-a",
			Endpoint: "https://push.example.test/fail", Auth: "auth", P256DH: "p256dh",
		}},
	}
	service, err := NewServiceWithSender(repository, "push@example.test", senderFunc(func(context.Context, []byte, *pushlib.Subscription, *pushlib.Options) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader("limited"))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Send(context.Background(), "account-a", Payload{Title: "Test"}, SendOptions{})
	if err == nil || len(result.Errors) != 1 || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("expected observable HTTP error, result=%#v err=%v", result, err)
	}
}

type senderFunc func(context.Context, []byte, *pushlib.Subscription, *pushlib.Options) (*http.Response, error)

func (f senderFunc) SendNotificationWithContext(ctx context.Context, payload []byte, subscription *pushlib.Subscription, options *pushlib.Options) (*http.Response, error) {
	return f(ctx, payload, subscription, options)
}

type memoryRepository struct {
	pair    VAPIDKeyPair
	records []StoredSubscription
	deleted []string
}

func (m *memoryRepository) EnsureVAPIDKeyPair(context.Context) (VAPIDKeyPair, error) {
	return m.pair, nil
}
func (m *memoryRepository) UpsertSubscription(context.Context, string, SubscriptionInput) (Subscription, bool, error) {
	return Subscription{}, false, nil
}
func (m *memoryRepository) ListSubscriptions(_ context.Context, account string, limit int) ([]Subscription, error) {
	stored, err := m.ListStoredSubscriptions(context.Background(), account, limit)
	out := make([]Subscription, 0, len(stored))
	for _, record := range stored {
		out = append(out, record.Subscription)
	}
	return out, err
}
func (m *memoryRepository) ListStoredSubscriptions(_ context.Context, account string, _ int) ([]StoredSubscription, error) {
	out := make([]StoredSubscription, 0, len(m.records))
	for _, record := range m.records {
		if record.AccountScopeID == account {
			out = append(out, record)
		}
	}
	return out, nil
}
func (m *memoryRepository) DeleteSubscription(_ context.Context, account, id string) (bool, error) {
	for _, record := range m.records {
		if record.AccountScopeID == account && record.ID == id {
			m.deleted = append(m.deleted, id)
			return true, nil
		}
	}
	return false, nil
}
