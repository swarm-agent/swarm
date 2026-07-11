package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type accountRoundTripFunc func(*http.Request) (*http.Response, error)

func (f accountRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAccountAPICallsUseOAuthAccountScopeAndTypedPayloads(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "auth.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authStore := pebblestore.NewAuthStore(store)
	if _, err := authStore.SetCodexOAuthForAccount("account-1", "access-token", "refresh-token", time.Now().Add(time.Hour).UnixMilli(), "chatgpt-account-1"); err != nil {
		t.Fatal(err)
	}

	var paths []string
	client := NewClient(authStore)
	client.httpClient = &http.Client{Transport: accountRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.Method+" "+req.URL.Path)
		if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := req.Header.Get(chatGPTAccountIDHeader); got != "chatgpt-account-1" {
			t.Errorf("account id = %q", got)
		}
		body := `{"plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_after_seconds":60,"reset_at":200}}}`
		switch req.URL.Path {
		case "/backend-api/wham/rate-limit-reset-credits":
			body = `{"credits":[{"id":"credit-1","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-01-01T00:00:00Z","expires_at":null,"title":"Reset","description":"One reset"}],"available_count":1}`
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			var payload ConsumeResetCreditRequest
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.CreditID != "credit-1" || payload.RedeemRequestID != "request-1" {
				t.Errorf("consume payload = %+v", payload)
			}
			body = `{"code":"reset","windows_reset":2}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"})

	usage, err := client.GetAccountUsage(ctx)
	if err != nil || usage.PlanType != "plus" || usage.RateLimit.PrimaryWindow.UsedPercent != 25 {
		t.Fatalf("usage = %+v, err = %v", usage, err)
	}
	credits, err := client.GetResetCredits(ctx)
	if err != nil || credits.AvailableCount != 1 || len(credits.Credits) != 1 {
		t.Fatalf("credits = %+v, err = %v", credits, err)
	}
	out, err := client.ConsumeResetCredit(ctx, ConsumeResetCreditRequest{CreditID: "credit-1", RedeemRequestID: "request-1"})
	if err != nil || out.Code != "reset" || out.WindowsReset != 2 {
		t.Fatalf("consume = %+v, err = %v", out, err)
	}
	want := []string{"GET /backend-api/wham/usage", "GET /backend-api/wham/rate-limit-reset-credits", "POST /backend-api/wham/rate-limit-reset-credits/consume"}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestAccountAPIRetriesOnceAfterOAuthRefresh(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "auth.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authStore := pebblestore.NewAuthStore(store)
	if _, err := authStore.SetCodexOAuthForAccount("account-1", "expired-access", "refresh-token", time.Now().Add(time.Hour).UnixMilli(), "chatgpt-account-1"); err != nil {
		t.Fatal(err)
	}

	var usageCalls, refreshCalls int
	client := NewClient(authStore)
	client.httpClient = &http.Client{Transport: accountRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		status := http.StatusOK
		switch req.URL.String() {
		case codexAccountUsageURL:
			usageCalls++
			if usageCalls == 1 {
				status = http.StatusUnauthorized
			} else {
				if got := req.Header.Get("Authorization"); got != "Bearer refreshed-access" {
					t.Errorf("retried authorization = %q", got)
				}
				body = `{"plan_type":"plus"}`
			}
		case tokenURL:
			refreshCalls++
			body = `{"access_token":"refreshed-access","refresh_token":"refreshed-token","expires_in":3600}`
		default:
			t.Fatalf("unexpected URL %s", req.URL)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"})
	usage, err := client.GetAccountUsage(ctx)
	if err != nil || usage.PlanType != "plus" || usageCalls != 2 || refreshCalls != 1 {
		t.Fatalf("usage=%+v usageCalls=%d refreshCalls=%d err=%v", usage, usageCalls, refreshCalls, err)
	}
}

func TestAccountAPIRejectsAPIKeyAuth(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "auth.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authStore := pebblestore.NewAuthStore(store)
	if _, err := authStore.SetCodexAPIKeyForAccount("account-1", "sk-test"); err != nil {
		t.Fatal(err)
	}
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"})
	_, err = NewClient(authStore).GetAccountUsage(ctx)
	if !errors.Is(err, ErrCodexAccountOAuthRequired) {
		t.Fatalf("error = %v", err)
	}
}
