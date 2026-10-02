package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type publicationTransport func(*http.Request) (*http.Response, error)

func (f publicationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func publicationCredentials(t *testing.T) {
	t.Helper()
	for _, key := range []string{"TWITTER_API_KEY", "TWITTER_API_SECRET", "TWITTER_ACCESS_TOKEN", "TWITTER_ACCESS_TOKEN_SECRET"} {
		t.Setenv(key, "test-only")
	}
	t.Setenv("TWITTER_ACCOUNT", "example")
	t.Setenv("TWITTER_ACCOUNT_SCOPE_ID", "owner")
}

// Purpose: executeTwitterPublish owns response validation and incremental receipt
// persistence. Injecting HTTP responses at its actual transport boundary proves
// failures cannot become success or leak provider bodies; no live X calls occur.
func TestPublicationTwitterOutcomes(t *testing.T) {
	publicationCredentials(t)
	for _, tc := range []struct {
		name, body, want string
		code             int
		transportError   bool
	}{
		{"success", `{"data":{"id":"123"}}`, "published", 201, false},
		{"rejected", `private-response`, "publication_failed", 403, false},
		{"server", `private-response`, "reconciliation_required", 503, false},
		{"malformed", `private-response`, "reconciliation_required", 201, false},
		{"missing", `{"data":{}}`, "reconciliation_required", 201, false},
		{"nonnumeric", `{"data":{"id":"bad/id"}}`, "reconciliation_required", 201, false},
		{"zero", `{"data":{"id":"0"}}`, "reconciliation_required", 201, false},
		{"transport", "", "reconciliation_required", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, saves := 0, 0
			client := &http.Client{Timeout: time.Second, Transport: publicationTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != twitterPostTweetsURL || !strings.HasPrefix(r.Header.Get("Authorization"), "OAuth ") {
					t.Error("wrong request")
				}
				if tc.transportError {
					return nil, errors.New("private-response")
				}
				return &http.Response{StatusCode: tc.code, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			rec := &pebblestore.DeliverableRecord{AccountID: "owner", PublicationClaim: "claim", Payload: map[string]any{"text": "post"}, ActionContract: &pebblestore.DeliverableActionContract{TargetSecretRef: "env:TWITTER"}}
			result := executeTwitterPublish(context.Background(), rec, client, func(m map[string]any) error {
				saves++
				if m["tweet_id"] != "123" {
					t.Error("missing receipt")
				}
				return nil
			})
			encoded, _ := json.Marshal(result)
			if result["status"] != tc.want || calls != 1 || strings.Contains(string(encoded), "private-response") {
				t.Fatalf("outcome: %s calls=%d", encoded, calls)
			}
			if (tc.want == "published") != (saves == 1) {
				t.Fatalf("saves=%d", saves)
			}
		})
	}
}

// Purpose: credential/account/payload validation must happen before HTTP; a
// persisted first receipt must survive a second-post failure or sink failure.
// executeTwitterPublish is the narrowest boundary that owns this ordering.
func TestPublicationTwitterPreflightAndPartial(t *testing.T) {
	publicationCredentials(t)
	calls := 0
	client := &http.Client{Transport: publicationTransport(func(*http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("lost response")
		}
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{"data":{"id":"123"}}`))}, nil
	})}
	rec := &pebblestore.DeliverableRecord{AccountID: "other", PublicationClaim: "claim", Payload: map[string]any{"text": "post"}, ActionContract: &pebblestore.DeliverableActionContract{TargetSecretRef: "env:TWITTER"}}
	save := func(map[string]any) error { return nil }
	if got := executeTwitterPublish(context.Background(), rec, client, save); got["status"] != "publication_failed" || calls != 0 {
		t.Fatal(got)
	}
	rec.AccountID = "owner"
	t.Setenv("TWITTER_API_KEY", "")
	if got := executeTwitterPublish(context.Background(), rec, client, save); got["status"] != "publication_failed" || calls != 0 {
		t.Fatal(got)
	}
	t.Setenv("TWITTER_API_KEY", "test-only")
	rec.Payload = map[string]any{"posts": []any{"post", " "}}
	if got := executeTwitterPublish(context.Background(), rec, client, save); got["status"] != "publication_failed" || calls != 0 {
		t.Fatal(got)
	}
	rec.Payload = map[string]any{"posts": []any{"first", "second"}}
	saves := 0
	got := executeTwitterPublish(context.Background(), rec, client, func(map[string]any) error { saves++; return nil })
	if got["status"] != "reconciliation_required" || got["post_count"] != 1 || saves != 1 || calls != 2 {
		t.Fatal(got, saves, calls)
	}
	calls = 0
	got = executeTwitterPublish(context.Background(), rec, client, func(map[string]any) error { return errors.New("disk unavailable") })
	if got["status"] != "reconciliation_required" || calls != 1 {
		t.Fatal(got, calls)
	}
}

// Purpose: approveDeliverablePublication must claim in real Pebble before webhook
// I/O, persist rejection/ambiguity and return successful receipts without repost.
// TLS loopback plus the production approval service tests the smallest complete
// store/network boundary, including cross-account rejection and no side effect.
func TestPublicationWebhookApproval(t *testing.T) {
	for _, code := range []int{204, 403, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls atomic.Int32
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(code) }))
			defer ts.Close()
			client := ts.Client()
			client.Timeout = time.Second
			db, err := pebblestore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := pebblestore.NewSessionStore(db)
			rec := pebblestore.DeliverableRecord{Title: "webhook", ActionContract: &pebblestore.DeliverableActionContract{Action: "execute_webhook", TargetURL: ts.URL}}
			if err := s.PutDeliverable("owner", &rec); err != nil {
				t.Fatal(err)
			}
			if _, err := approveDeliverablePublicationWithClient(context.Background(), s, "other", rec.ID, "reviewer", client); err == nil || calls.Load() != 0 {
				t.Fatal("cross-account side effect")
			}
			got, err := approveDeliverablePublicationWithClient(context.Background(), s, "owner", rec.ID, "reviewer", client)
			if err != nil {
				t.Fatal(err)
			}
			want := "published"
			if code == 403 {
				want = "publication_failed"
			}
			if code == 503 {
				want = "reconciliation_required"
			}
			if got.Status != want || calls.Load() != 1 {
				t.Fatal(got.Status, calls.Load())
			}
			again, err := approveDeliverablePublicationWithClient(context.Background(), s, "owner", rec.ID, "reviewer", client)
			if calls.Load() != 1 || (code == 204 && (err != nil || again.Status != "published")) || (code != 204 && err == nil) {
				t.Fatal("unsafe replay", err)
			}
		})
	}
}

// Purpose: handleDeliverables must reject missing/non-user principals before
// touching the store or network. A nil-service server makes reaching either
// boundary observable as an incorrect 503 instead of the required auth denial.
func TestPublicationApprovalRequiresUser(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodPost, DeliverablesPath+"/post/approve", nil)
	w := httptest.NewRecorder()
	s.handleDeliverables(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
	p := accountTestPrincipal()
	p.Type = "application"
	r = r.WithContext(identity.ContextWithPrincipal(r.Context(), p))
	w = httptest.NewRecorder()
	s.handleDeliverables(w, r)
	if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
}

// Purpose: missing webhook targets and transport ambiguity must never publish.
// executeDeliverableWebhook owns these classifications and must redact errors.
func TestPublicationWebhookInvalidAndAmbiguous(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: publicationTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("private-token") })}
	rec := &pebblestore.DeliverableRecord{ActionContract: &pebblestore.DeliverableActionContract{}}
	got := executeDeliverableWebhook(context.Background(), rec, client)
	if got["status"] != "publication_failed" || calls != 0 {
		t.Fatal(got)
	}
	rec.ActionContract.TargetURL = "https://example.invalid/hook"
	got = executeDeliverableWebhook(context.Background(), rec, client)
	encoded, _ := json.Marshal(got)
	if got["status"] != "reconciliation_required" || calls != 1 || strings.Contains(string(encoded), "private-token") {
		t.Fatal(got)
	}
}
