package google

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type googleAdapterRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn googleAdapterRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestGoogleVerifyCredentialUsesNonGenerationModelsEndpoint(t *testing.T) {
	adapter := NewAdapter(nil)
	adapter.httpClient = &http.Client{Transport: googleAdapterRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("method = %q, want GET", req.Method)
		}
		if got := req.URL.Scheme + "://" + req.URL.Host + req.URL.Path; got != googleModelsVerificationURL {
			t.Fatalf("verification URL = %q, want %q", got, googleModelsVerificationURL)
		}
		if got := req.URL.Query().Get("pageSize"); got != "1" {
			t.Fatalf("pageSize = %q, want 1", got)
		}
		if req.URL.Query().Has("key") {
			t.Fatal("Google API key must not be placed in the verification URL")
		}
		if got := req.Header.Get(googleAPIKeyHeader); got != "test-google-key" {
			t.Fatalf("Google API key header = %q, want submitted key", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"models":[{"name":"models/gemini-test"}]}`)),
			Request:    req,
		}, nil
	})}

	verification, err := adapter.VerifyCredential(context.Background(), provideriface.AuthCredential{APIKey: " test-google-key "})
	if err != nil {
		t.Fatalf("VerifyCredential: %v", err)
	}
	if !verification.Connected || verification.Method != "api" || verification.Message != "Google API key verified via Gemini models.list" {
		t.Fatalf("verification = %+v", verification)
	}
}

func TestGoogleVerifyCredentialRejectsMissingKeyAndProviderErrors(t *testing.T) {
	adapter := NewAdapter(nil)
	verification, err := adapter.VerifyCredential(context.Background(), provideriface.AuthCredential{})
	if err == nil || verification.Connected || !strings.Contains(err.Error(), "requires api_key") {
		t.Fatalf("missing-key result = %+v, err = %v", verification, err)
	}

	adapter.httpClient = &http.Client{Transport: googleAdapterRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"forbidden"}}`)),
			Request:    req,
		}, nil
	})}
	verification, err = adapter.VerifyCredential(context.Background(), provideriface.AuthCredential{APIKey: "invalid-key"})
	if err == nil || verification.Connected || !strings.Contains(err.Error(), "status=403") {
		t.Fatalf("provider-error result = %+v, err = %v", verification, err)
	}
}

func TestGoogleVerifyCredentialSanitizesTransportErrors(t *testing.T) {
	adapter := NewAdapter(nil)
	adapter.httpClient = &http.Client{Transport: googleAdapterRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("request failed at https://example.invalid?key=secret-value")
	})}

	verification, err := adapter.VerifyCredential(context.Background(), provideriface.AuthCredential{APIKey: "secret-value"})
	if err == nil || verification.Connected {
		t.Fatalf("transport-error result = %+v, err = %v", verification, err)
	}
	if strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "key=[redacted]") {
		t.Fatalf("transport error was not sanitized: %v", err)
	}
}

func TestGoogleVerificationClientRefusesRedirects(t *testing.T) {
	client := newGoogleVerificationClient()
	if client.CheckRedirect == nil {
		t.Fatal("verification client must define a redirect policy")
	}
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v, want ErrUseLastResponse", err)
	}
}
