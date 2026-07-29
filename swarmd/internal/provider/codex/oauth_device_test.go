package codex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestDeviceAuthorizationAcceptsAliasAndBoundsInterval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usercode" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["client_id"] != clientID {
			t.Fatalf("client_id = %q", request["client_id"])
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_auth_id": "private-id",
			"usercode":       "ABCD-EFGH",
			"interval":       "999",
		})
	}))
	defer server.Close()

	authorization, err := requestDeviceAuthorization(context.Background(), server.Client(), deviceAuthEndpoints{
		userCodeURL: server.URL + "/usercode", verificationURL: "https://example.test/device",
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorization.UserCode != "ABCD-EFGH" || authorization.VerificationURL != "https://example.test/device" {
		t.Fatalf("authorization = %#v", authorization)
	}
	if authorization.interval != codexDevicePollMaximum {
		t.Fatalf("interval = %s", authorization.interval)
	}
}

func TestRequestDeviceAuthorizationReportsDisabled(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	_, err := requestDeviceAuthorization(context.Background(), server.Client(), deviceAuthEndpoints{userCodeURL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "unavailable or disabled") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompleteDeviceAuthorizationPollsAndExchanges(t *testing.T) {
	verifier := "test-device-verifier"
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/poll":
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"authorization_code": "authorization-code",
				"code_challenge":     oauthCodeChallenge(verifier),
				"code_verifier":      verifier,
			})
		case "/token":
			body, _ := io.ReadAll(r.Body)
			values, _ := url.ParseQuery(string(body))
			if values.Get("redirect_uri") != "https://example.test/deviceauth/callback" || values.Get("code_verifier") != verifier {
				t.Fatalf("exchange values = %v", values)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access", "refresh_token": "refresh", "expires_in": 3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tokens, err := completeDeviceAuthorizationWithSleep(context.Background(), server.Client(), deviceAuthEndpoints{
		pollURL: server.URL + "/poll", tokenURL: server.URL + "/token", redirectURL: "https://example.test/deviceauth/callback",
	}, DeviceAuthorization{
		UserCode: "ABCD-EFGH", deviceAuthID: "private-id", interval: time.Second, ExpiresAt: time.Now().Add(time.Minute),
	}, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access" || tokens.RefreshToken != "refresh" || polls.Load() != 2 {
		t.Fatalf("tokens = %#v, polls = %d", tokens, polls.Load())
	}
}
