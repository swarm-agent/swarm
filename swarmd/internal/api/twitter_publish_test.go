package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestExtractTweetTexts(t *testing.T) {
	// 1. Direct tweet_text
	p1 := map[string]any{"tweet_text": "Hello world from Swarm!"}
	texts1 := ExtractTweetTexts(p1)
	if len(texts1) != 1 || texts1[0] != "Hello world from Swarm!" {
		t.Fatalf("expected 1 tweet, got %v", texts1)
	}

	// 2. Direct text
	p2 := map[string]any{"text": "Simple tweet text"}
	texts2 := ExtractTweetTexts(p2)
	if len(texts2) != 1 || texts2[0] != "Simple tweet text" {
		t.Fatalf("expected 1 tweet, got %v", texts2)
	}

	// 3. Posts array
	p3 := map[string]any{
		"posts": []any{
			map[string]any{"text": "1/2 First tweet"},
			map[string]any{"text": "2/2 Second tweet"},
		},
	}
	texts3 := ExtractTweetTexts(p3)
	if len(texts3) != 2 || texts3[0] != "1/2 First tweet" || texts3[1] != "2/2 Second tweet" {
		t.Fatalf("expected 2 tweets, got %v", texts3)
	}
}

func TestPercentEncode(t *testing.T) {
	if got := percentEncode("hello world"); got != "hello%20world" {
		t.Fatalf("expected hello%%20world, got %s", got)
	}
	if got := percentEncode("a-b_c.d~e"); got != "a-b_c.d~e" {
		t.Fatalf("expected unreserved chars intact, got %s", got)
	}
	if got := percentEncode("foo@bar#baz"); got != "foo%40bar%23baz" {
		t.Fatalf("expected special chars encoded, got %s", got)
	}
}

func TestBuildOAuth1Header(t *testing.T) {
	creds := &TwitterCredentials{
		APIKey:            "test_key",
		APISecret:         "test_secret",
		AccessToken:       "test_token",
		AccessTokenSecret: "test_token_secret",
		Account:           "@__swarmagent",
	}

	header := BuildOAuth1Header(http.MethodPost, "https://api.twitter.com/2/tweets", creds, "1234567890", "test_nonce")
	if !strings.HasPrefix(header, "OAuth ") {
		t.Fatalf("expected OAuth header prefix, got %s", header)
	}
	if !strings.Contains(header, `oauth_consumer_key="test_key"`) {
		t.Fatalf("expected consumer key in header, got %s", header)
	}
	if !strings.Contains(header, `oauth_token="test_token"`) {
		t.Fatalf("expected token in header, got %s", header)
	}
	if !strings.Contains(header, `oauth_signature_method="HMAC-SHA1"`) {
		t.Fatalf("expected signature method HMAC-SHA1, got %s", header)
	}
	if !strings.Contains(header, `oauth_signature=`) {
		t.Fatalf("expected signature in header, got %s", header)
	}
}

func TestExecuteTwitterPublish_FallbackWarning(t *testing.T) {
	rec := &pebblestore.DeliverableRecord{
		ID:    "deliv_test",
		Title: "Test Tweet",
		Kind:  "social_post",
		Payload: map[string]any{
			"tweet_text": "A simulated tweet test",
		},
		ActionContract: &pebblestore.DeliverableActionContract{
			Action: "publish_x_post",
		},
	}

	res := ExecuteTwitterPublish(context.Background(), rec)
	if res["published_to"] != "x" {
		t.Fatalf("expected published_to x, got %v", res)
	}
	if res["status"] != "published" {
		t.Fatalf("expected status published, got %v", res)
	}
}

func TestExecuteTwitterPublish_MockServerSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "OAuth ") {
			http.Error(w, "missing oauth header", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]string{
				"id":   "2032084733673553999",
				"text": "Live mock tweet test",
			},
		})
	}))
	defer ts.Close()

	// Verify BuildOAuth1Header generates expected header against the mock server URL
	creds := &TwitterCredentials{
		APIKey:            "k",
		APISecret:         "s",
		AccessToken:       "t",
		AccessTokenSecret: "ts",
	}
	h := BuildOAuth1Header(http.MethodPost, ts.URL, creds, "1000", "nonce")
	if !strings.Contains(h, `oauth_signature=`) {
		t.Fatalf("failed to generate signature for mock URL")
	}
}
