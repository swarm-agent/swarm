package exa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type Adapter struct {
	authStore  *pebblestore.AuthStore
	httpClient *http.Client
}

func NewAdapter(authStore *pebblestore.AuthStore) *Adapter {
	return &Adapter{
		authStore: authStore,
		httpClient: &http.Client{
			Timeout: 6 * time.Second,
		},
	}
}

func (a *Adapter) ID() string {
	return "exa"
}

func (a *Adapter) Status(ctx context.Context) (provideriface.Status, error) {
	if a == nil || a.authStore == nil {
		return provideriface.Status{
			ID:          "exa",
			Ready:       false,
			Reason:      "missing exa auth store",
			RunReason:   "search-only provider (no model runner)",
			AuthMethods: exaAuthMethods(),
		}, nil
	}
	principal, principalOK := identity.PrincipalFromContext(ctx)
	if !principalOK {
		return provideriface.Status{
			ID:          "exa",
			Ready:       false,
			Reason:      "product identity is required",
			RunReason:   "search-only provider (no model runner)",
			AuthMethods: exaAuthMethods(),
		}, nil
	}
	record, ok, err := a.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "exa")
	if err != nil {
		return provideriface.Status{}, err
	}
	apiKey := ""
	if ok {
		apiKey = strings.TrimSpace(record.APIKey)
	}
	if apiKey != "" {
		return provideriface.Status{
			ID:          "exa",
			Ready:       true,
			RunReason:   "search-only provider (no model runner)",
			AuthMethods: exaAuthMethods(),
		}, nil
	}
	return provideriface.Status{
		ID:          "exa",
		Ready:       false,
		Reason:      "missing active Exa API key; add one in Settings > Providers or run /auth key exa <api_key>",
		RunReason:   "search-only provider (no model runner)",
		AuthMethods: exaAuthMethods(),
	}, nil
}

func (a *Adapter) VerifyCredential(ctx context.Context, credential provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	authType := strings.ToLower(strings.TrimSpace(credential.Type))
	if authType == "" {
		authType = "api"
	}
	if authType != "api" {
		return provideriface.AuthVerification{
			Connected: false,
			Method:    authType,
		}, errors.New("exa credential verification supports only api key auth")
	}

	apiKey := strings.TrimSpace(credential.APIKey)
	if apiKey == "" {
		return provideriface.AuthVerification{
			Connected: false,
			Method:    "api",
		}, errors.New("exa api verification requires api_key")
	}

	verifyCtx := ctx
	if verifyCtx == nil {
		verifyCtx = context.Background()
	}
	payload := map[string]any{
		"query":      "swarm exa auth probe",
		"numResults": 1,
		"type":       "fast",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return provideriface.AuthVerification{
			Connected: false,
			Method:    "api",
		}, fmt.Errorf("marshal exa verify payload: %w", err)
	}
	req, err := http.NewRequestWithContext(verifyCtx, http.MethodPost, "https://api.exa.ai/search", bytes.NewReader(body))
	if err != nil {
		return provideriface.AuthVerification{
			Connected: false,
			Method:    "api",
		}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)

	client := a.httpClient
	if client == nil {
		client = &http.Client{Timeout: 6 * time.Second}
	}
	providerdiagnostics.LogRequest("exa", "verify.search", req, body)
	resp, err := client.Do(req)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "exa", "verify.search", err)
		return provideriface.AuthVerification{
			Connected: false,
			Method:    "api",
		}, fmt.Errorf("exa api verification failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	providerdiagnostics.LogResponse("exa", "verify.search", resp, raw)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "exa", "verify.search", err)
		return provideriface.AuthVerification{
			Connected: false,
			Method:    "api",
		}, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		msg := exaVerifyErrorMessage(raw)
		if msg == "" {
			msg = "unknown error"
		}
		return provideriface.AuthVerification{
			Connected: false,
			Method:    "api",
		}, fmt.Errorf("exa api verification failed status=%d: %s", resp.StatusCode, msg)
	}

	return provideriface.AuthVerification{
		Connected: true,
		Method:    "api",
		Message:   "Exa API key verified via /search",
	}, nil
}

func exaVerifyErrorMessage(raw []byte) string {
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return ""
	}
	var payload struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil {
		msg := strings.TrimSpace(payload.Message)
		if msg != "" {
			return msg
		}
		msg = strings.TrimSpace(payload.Error)
		if msg != "" {
			return msg
		}
	}
	return body
}

func exaAuthMethods() []provideriface.AuthMethod {
	return []provideriface.AuthMethod{
		{
			ID:             "api",
			Label:          "API key",
			CredentialType: "api",
			Description:    "Adding and activating an Exa API key opts in to Exa-hosted web search and page retrieval. Queries and selected URLs are sent to Exa, results return to the agent/model context, and your browser profile is not used.",
		},
	}
}
