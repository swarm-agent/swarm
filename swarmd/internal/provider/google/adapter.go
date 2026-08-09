package google

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/defaults"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const googleModelsVerificationURL = "https://generativelanguage.googleapis.com/v1beta/models"

const maxGoogleVerificationResponseBytes = 64 << 10

type Adapter struct {
	authStore  *pebblestore.AuthStore
	httpClient *http.Client
}

func NewAdapter(authStore *pebblestore.AuthStore) *Adapter {
	return &Adapter{
		authStore:  authStore,
		httpClient: newGoogleVerificationClient(),
	}
}

func newGoogleVerificationClient() *http.Client {
	return &http.Client{
		Timeout: 6 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (a *Adapter) ID() string {
	return "google"
}

func (a *Adapter) Status(ctx context.Context) (provideriface.Status, error) {
	providerDefaults := defaults.MustLookup("google")
	principal, principalOK := identity.PrincipalFromContext(ctx)
	if !principalOK {
		return provideriface.Status{
			ID:              "google",
			Ready:           false,
			Reason:          "product identity is required",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     googleAuthMethods(),
		}, nil
	}
	record, ok, err := a.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "google")
	if err != nil {
		return provideriface.Status{}, err
	}
	if !ok || !recordReady(record) {
		return provideriface.Status{
			ID:              "google",
			Ready:           false,
			Reason:          "missing google auth",
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     googleAuthMethods(),
		}, nil
	}
	return provideriface.Status{
		ID:              "google",
		Ready:           true,
		DefaultModel:    providerDefaults.PrimaryModel,
		DefaultThinking: providerDefaults.PrimaryThinking,
		AuthMethods:     googleAuthMethods(),
	}, nil
}

func (a *Adapter) VerifyCredential(ctx context.Context, credential provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	apiKey := strings.TrimSpace(credential.APIKey)
	if apiKey == "" {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, errors.New("google api verification requires api_key")
	}

	endpoint, err := url.Parse(googleModelsVerificationURL)
	if err != nil {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, errors.New("google api verification endpoint is invalid")
	}
	query := endpoint.Query()
	query.Set("pageSize", "1")
	endpoint.RawQuery = query.Encode()

	verifyCtx := ctx
	if verifyCtx == nil {
		verifyCtx = context.Background()
	}
	req, err := http.NewRequestWithContext(verifyCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, sanitizeGoogleError("create google api verification request", err)
	}
	// Keep the key out of the URL so it cannot leak through ordinary URL logging.
	req.Header.Set(googleAPIKeyHeader, apiKey)

	client := a.httpClient
	if client == nil {
		client = newGoogleVerificationClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, sanitizeGoogleError("google api verification request failed", err)
	}
	defer resp.Body.Close()

	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxGoogleVerificationResponseBytes)); err != nil {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, sanitizeGoogleError("read google api verification response", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, fmt.Errorf("google api verification failed status=%d", resp.StatusCode)
	}

	return provideriface.AuthVerification{
		Connected: true,
		Method:    "api",
		Message:   "Google API key verified via Gemini models.list",
	}, nil
}

func recordReady(record pebblestore.AuthCredentialRecord) bool {
	return strings.TrimSpace(record.APIKey) != ""
}

func googleAuthMethods() []provideriface.AuthMethod {
	return []provideriface.AuthMethod{
		{
			ID:             "api",
			Label:          "API key",
			CredentialType: "api",
			Description:    "Use a Google API key.",
		},
	}
}
