package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/provider/defaults"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const maxBodyBytes = 8 * 1024

type Adapter struct {
	authStore  *pebblestore.AuthStore
	httpClient *http.Client
	manager    *Manager
}

func NewAdapter(authStore *pebblestore.AuthStore) *Adapter {
	return NewAdapterWithManager(authStore, NewManager(authStore))
}

func NewAdapterWithManager(authStore *pebblestore.AuthStore, manager *Manager) *Adapter {
	if manager == nil {
		manager = NewManager(authStore)
	}
	return &Adapter{
		authStore: authStore,
		httpClient: &http.Client{
			Timeout: 6 * time.Second,
		},
		manager: manager,
	}
}

func (a *Adapter) ID() string {
	return "copilot"
}

func (a *Adapter) Status(ctx context.Context) (provideriface.Status, error) {
	providerDefaults := defaults.MustLookup("copilot")
	if a == nil || a.authStore == nil {
		return provideriface.Status{
			ID:          "copilot",
			Ready:       false,
			Reason:      "copilot auth store is not configured",
			AuthMethods: copilotAuthMethods(),
		}, nil
	}

	record, ok, err := a.authStore.GetActiveCredential("copilot")
	if err != nil {
		return provideriface.Status{}, err
	}
	if !ok {
		return provideriface.Status{
			ID:          "copilot",
			Ready:       false,
			Reason:      "no active Copilot auth source selected",
			AuthMethods: copilotAuthMethods(),
		}, nil
	}

	if !copilotCredentialReady(record) {
		return provideriface.Status{
			ID:              "copilot",
			Ready:           false,
			Reason:          missingCredentialMaterialMessage(record),
			DefaultModel:    providerDefaults.PrimaryModel,
			DefaultThinking: providerDefaults.PrimaryThinking,
			AuthMethods:     copilotAuthMethods(),
		}, nil
	}

	ready, reason := copilotAuthManagerStatus(record)
	if strings.TrimSpace(reason) == "" {
		if ready {
			reason = configuredCredentialMessage(record)
		} else {
			reason = unauthenticatedCredentialMessage(record)
		}
	}
	return provideriface.Status{
		ID:              "copilot",
		Ready:           ready,
		Reason:          reason,
		DefaultModel:    providerDefaults.PrimaryModel,
		DefaultThinking: providerDefaults.PrimaryThinking,
		AuthMethods:     copilotAuthMethods(),
	}, nil
}

func activeCredentialName(record pebblestore.AuthCredentialRecord) string {
	if label := strings.TrimSpace(record.Label); label != "" {
		return label
	}
	if id := strings.TrimSpace(record.ID); id != "" {
		return id
	}
	return "copilot"
}

func copilotAuthManagerStatus(record pebblestore.AuthCredentialRecord) (bool, string) {
	if record.Connection != nil {
		reason := strings.TrimSpace(record.Connection.Message)
		if record.Connection.Connected {
			if reason == "" {
				reason = configuredCredentialMessage(record)
			}
			return true, reason
		}
		if reason == "" {
			reason = unauthenticatedCredentialMessage(record)
		}
		return false, reason
	}

	if copilotStoredCredentialAuth(record) {
		return true, configuredCredentialMessage(record)
	}
	return false, unauthenticatedCredentialMessage(record)
}

func copilotCredentialReady(record pebblestore.AuthCredentialRecord) bool {
	switch strings.ToLower(strings.TrimSpace(record.Type)) {
	case pebblestore.AuthTypeOAuth:
		return strings.TrimSpace(record.AccessToken) != ""
	case pebblestore.AuthTypeCLI, pebblestore.AuthTypeGH:
		return true
	default:
		return strings.TrimSpace(record.APIKey) != ""
	}
}

func copilotStoredCredentialAuth(record pebblestore.AuthCredentialRecord) bool {
	switch strings.ToLower(strings.TrimSpace(record.Type)) {
	case pebblestore.AuthTypeOAuth, pebblestore.AuthTypeAPI, "":
		return true
	default:
		return false
	}
}

func configuredCredentialMessage(record pebblestore.AuthCredentialRecord) string {
	credential := activeCredentialName(record)
	switch strings.ToLower(strings.TrimSpace(record.Type)) {
	case pebblestore.AuthTypeOAuth:
		return fmt.Sprintf("active Copilot OAuth credential %q is configured in the auth manager. New Copilot runs use this Swarm credential until changed in /auth.", credential)
	default:
		return fmt.Sprintf("active Copilot token credential %q is configured in the auth manager. New Copilot runs use this Swarm credential until changed in /auth.", credential)
	}
}

func missingCredentialMaterialMessage(record pebblestore.AuthCredentialRecord) string {
	credential := activeCredentialName(record)
	switch strings.ToLower(strings.TrimSpace(record.Type)) {
	case pebblestore.AuthTypeOAuth:
		return fmt.Sprintf("active Copilot OAuth credential %q is missing access_token", credential)
	default:
		return fmt.Sprintf("active Copilot token credential %q is missing GitHub token", credential)
	}
}

func unauthenticatedCredentialMessage(record pebblestore.AuthCredentialRecord) string {
	credential := activeCredentialName(record)
	switch strings.ToLower(strings.TrimSpace(record.Type)) {
	case pebblestore.AuthTypeCLI:
		return fmt.Sprintf("active Copilot auth source %q is saved in the auth manager but still requires `copilot login`", credential)
	case pebblestore.AuthTypeGH:
		return fmt.Sprintf("active Copilot auth source %q is saved in the auth manager but still requires `gh auth`", credential)
	default:
		return fmt.Sprintf("active Copilot auth source %q is saved in the auth manager but has not been verified yet", credential)
	}
}

func (a *Adapter) VerifyCredential(ctx context.Context, credential provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	authType := strings.ToLower(strings.TrimSpace(credential.Type))
	if authType == "" {
		authType = pebblestore.AuthTypeAPI
	}

	var token string
	switch authType {
	case pebblestore.AuthTypeOAuth:
		token = strings.TrimSpace(credential.AccessToken)
		if token == "" {
			return provideriface.AuthVerification{Connected: false, Method: "oauth"}, errors.New("copilot oauth verification requires access_token")
		}
	case pebblestore.AuthTypeAPI:
		token = strings.TrimSpace(credential.APIKey)
		if token == "" {
			return provideriface.AuthVerification{Connected: false, Method: "api"}, errors.New("copilot token verification requires a GitHub token")
		}
	case pebblestore.AuthTypeCLI, pebblestore.AuthTypeGH:
		status, err := a.manager.GetAuthStatusForCredential(ctxOrBackground(ctx), credential)
		if err != nil {
			return provideriface.AuthVerification{Connected: false, Method: authType}, err
		}
		if !status.IsAuthenticated {
			message := strings.TrimSpace(status.StatusMessage)
			if message == "" {
				switch authType {
				case pebblestore.AuthTypeCLI:
					message = "Copilot CLI is not authenticated; run `copilot login`"
				default:
					message = "GitHub CLI is not authenticated; run `gh auth login`"
				}
			}
			return provideriface.AuthVerification{Connected: false, Method: authType, Message: message}, errors.New(message)
		}
		message := fmt.Sprintf("Copilot auth verified for %s", strings.TrimSpace(status.Login))
		if strings.TrimSpace(status.Login) == "" {
			message = "Copilot auth verified"
		}
		return provideriface.AuthVerification{
			Connected: true,
			Method:    authType,
			Message:   message,
		}, nil
	default:
		return provideriface.AuthVerification{Connected: false, Method: authType}, fmt.Errorf("unsupported copilot auth type %q", authType)
	}

	login, err := a.validateGitHubToken(ctxOrBackground(ctx), token)
	if err != nil {
		return provideriface.AuthVerification{Connected: false, Method: authType}, err
	}

	return provideriface.AuthVerification{
		Connected: true,
		Method:    authType,
		Message:   fmt.Sprintf("GitHub token verified for %s", login),
	}, nil
}

func (a *Adapter) validateGitHubToken(ctx context.Context, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("github token is empty")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "swarmd-copilot-auth/2")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github token verification failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &payload)
		message := strings.TrimSpace(payload.Message)
		if message == "" {
			message = strings.TrimSpace(string(body))
		}
		if message == "" {
			message = "unknown error"
		}
		return "", fmt.Errorf("github token verification failed status=%d: %s", resp.StatusCode, message)
	}

	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return "", fmt.Errorf("decode github user response: %w", err)
	}
	login := strings.TrimSpace(user.Login)
	if login == "" {
		login = "github-user"
	}
	return login, nil
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

func copilotAuthMethods() []provideriface.AuthMethod {
	return []provideriface.AuthMethod{
		{
			ID:             "cli",
			Label:          "Copilot CLI login",
			CredentialType: pebblestore.AuthTypeCLI,
			Description:    "Use the local `copilot login` session as the active Swarm Copilot auth source.",
		},
		{
			ID:             "gh",
			Label:          "GitHub CLI auth",
			CredentialType: pebblestore.AuthTypeGH,
			Description:    "Resolve the active Swarm Copilot auth source from `gh auth token`.",
		},
		{
			ID:             "token",
			Label:          "GitHub token",
			CredentialType: pebblestore.AuthTypeAPI,
			Description:    "Store a direct GitHub token for the active Swarm Copilot auth source.",
		},
	}
}
