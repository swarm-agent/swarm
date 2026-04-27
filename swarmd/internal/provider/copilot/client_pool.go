package copilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/github/copilot-sdk/go"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const copilotUtilityTimeout = 10 * time.Minute

type Manager struct {
	authStore      *pebblestore.AuthStore
	cliPath        string
	externalCLIURL string

	mu             sync.Mutex
	clients        map[string]*sdk.Client
	activeSessions atomic.Int64
}

type runtimeAuthBinding struct {
	CredentialID    string
	CredentialLabel string
	Method          string
	Token           string
	UseLoggedInUser bool
}

func NewManager(authStore *pebblestore.AuthStore) *Manager {
	return &Manager{
		authStore:      authStore,
		cliPath:        resolveCopilotCLIPath(),
		externalCLIURL: resolveCopilotCLIURL(),
		clients:        make(map[string]*sdk.Client),
	}
}

func (m *Manager) HasActiveSession() bool {
	if m == nil {
		return false
	}
	return m.activeSessions.Load() > 0
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	clients := make([]*sdk.Client, 0, len(m.clients))
	for _, client := range m.clients {
		if client != nil {
			clients = append(clients, client)
		}
	}
	m.clients = make(map[string]*sdk.Client)
	m.mu.Unlock()

	var errs []error
	for _, client := range clients {
		if err := client.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) getClient(ctx context.Context) (*sdk.Client, runtimeAuthBinding, error) {
	if m == nil {
		return nil, runtimeAuthBinding{}, errors.New("copilot manager is not configured")
	}

	binding, err := m.resolveActiveAuthBinding(ctx)
	if err != nil {
		return nil, runtimeAuthBinding{}, err
	}
	return m.getClientForBinding(ctx, binding)
}

func (m *Manager) getClientForBinding(ctx context.Context, binding runtimeAuthBinding) (*sdk.Client, runtimeAuthBinding, error) {
	if m == nil {
		return nil, runtimeAuthBinding{}, errors.New("copilot manager is not configured")
	}
	if strings.TrimSpace(m.externalCLIURL) != "" {
		return nil, runtimeAuthBinding{}, fmt.Errorf("COPILOT_SIDECAR_URL/COPILOT_CLI_URL external server mode is not supported for Swarm-managed Copilot credentials; unset it to use the active Swarm credential")
	}

	fingerprint := authBindingFingerprint(binding)

	m.mu.Lock()
	existing := m.clients[fingerprint]
	m.mu.Unlock()
	if existing != nil {
		if err := m.startClientForRequest(ctx, existing); err == nil {
			return existing, binding, nil
		}
		_ = existing.Stop()
		m.mu.Lock()
		delete(m.clients, fingerprint)
		m.mu.Unlock()
	}

	options := &sdk.ClientOptions{
		CLIPath:         strings.TrimSpace(m.cliPath),
		GitHubToken:     strings.TrimSpace(binding.Token),
		UseLoggedInUser: sdk.Bool(binding.UseLoggedInUser),
		AutoStart:       sdk.Bool(true),
		AutoRestart:     sdk.Bool(true),
		LogLevel:        "info",
	}
	client := sdk.NewClient(options)
	if err := m.startClientForRequest(ctx, client); err != nil {
		return nil, runtimeAuthBinding{}, fmt.Errorf("start copilot sdk client: %w", err)
	}

	m.mu.Lock()
	if existing := m.clients[fingerprint]; existing == nil {
		m.clients[fingerprint] = client
		m.mu.Unlock()
		return client, binding, nil
	} else {
		m.mu.Unlock()
		_ = client.Stop()
		if err := m.startClientForRequest(ctx, existing); err != nil {
			return nil, runtimeAuthBinding{}, fmt.Errorf("restart pooled copilot sdk client: %w", err)
		}
		return existing, binding, nil
	}
}

func (m *Manager) startClientForRequest(ctx context.Context, client *sdk.Client) error {
	if client == nil {
		return errors.New("copilot sdk client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	started := make(chan error, 1)
	go func() {
		started <- client.Start(context.Background())
	}()

	select {
	case err := <-started:
		return err
	case <-ctx.Done():
		client.ForceStop()
		return ctx.Err()
	}
}

func (m *Manager) resolveActiveAuthBinding(ctx context.Context) (runtimeAuthBinding, error) {
	if m == nil || m.authStore == nil {
		return runtimeAuthBinding{}, errors.New("copilot auth store is not configured")
	}

	record, ok, err := m.authStore.GetActiveCredential("copilot")
	if err != nil {
		return runtimeAuthBinding{}, fmt.Errorf("read active Copilot auth source: %w", err)
	}
	if !ok {
		record = pebblestore.AuthCredentialRecord{
			Provider: "copilot",
			Type:     pebblestore.AuthTypeCLI,
			Label:    "Copilot CLI login",
		}
	}
	return m.resolveCredentialBinding(ctx, record)
}

func (m *Manager) resolveCredentialBinding(ctx context.Context, record pebblestore.AuthCredentialRecord) (runtimeAuthBinding, error) {
	binding := runtimeAuthBinding{
		CredentialID:    strings.TrimSpace(record.ID),
		CredentialLabel: strings.TrimSpace(record.Label),
		Method:          strings.ToLower(strings.TrimSpace(record.Type)),
	}
	if binding.CredentialLabel == "" {
		binding.CredentialLabel = binding.CredentialID
	}
	if binding.Method == "" {
		binding.Method = "api"
	}

	switch binding.Method {
	case pebblestore.AuthTypeOAuth:
		binding.Token = strings.TrimSpace(record.AccessToken)
		if binding.Token == "" {
			return runtimeAuthBinding{}, errors.New("active Copilot OAuth credential is missing access_token")
		}
	case pebblestore.AuthTypeAPI:
		binding.Token = strings.TrimSpace(record.APIKey)
		if binding.Token == "" {
			return runtimeAuthBinding{}, errors.New("active Copilot token credential is missing GitHub token")
		}
	case pebblestore.AuthTypeCLI:
		binding.UseLoggedInUser = true
	case pebblestore.AuthTypeGH:
		token, err := resolveGitHubCLIToken(ctx)
		if err != nil {
			return runtimeAuthBinding{}, err
		}
		binding.Token = token
	default:
		return runtimeAuthBinding{}, fmt.Errorf("unsupported copilot auth type %q", binding.Method)
	}

	return binding, nil
}

func authBindingFingerprint(binding runtimeAuthBinding) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(binding.CredentialID),
		strings.TrimSpace(binding.Method),
		strings.TrimSpace(binding.Token),
		fmt.Sprintf("%t", binding.UseLoggedInUser),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func resolveGitHubCLIToken(ctx context.Context) (string, error) {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return "", errors.New("gh auth is selected but GitHub CLI (`gh`) is not installed")
	}

	cmd := exec.CommandContext(ctxOrBackground(ctx), ghPath, "auth", "token")
	output, runErr := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if runErr != nil {
		if text == "" {
			return "", fmt.Errorf("gh auth token failed: %w", runErr)
		}
		return "", fmt.Errorf("gh auth token failed: %s", text)
	}
	if text == "" {
		return "", errors.New("gh auth token returned an empty token")
	}
	return text, nil
}

func resolveCopilotCLIPath() string {
	if value := strings.TrimSpace(os.Getenv("COPILOT_CLI_PATH")); value != "" {
		return value
	}
	return "copilot"
}

func resolveCopilotCLIURL() string {
	for _, key := range []string{"COPILOT_SIDECAR_URL", "COPILOT_CLI_URL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func ensureTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), timeout)
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func inheritContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithCancel(context.Background())
	}
	return context.WithCancel(ctx)
}
