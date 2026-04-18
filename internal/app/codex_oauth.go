package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

const (
	codexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexOAuthAuthURL  = "https://auth.openai.com/oauth/authorize"
	codexOAuthTokenURL = "https://auth.openai.com/oauth/token"
	codexOAuthScopes   = "openid profile email offline_access"
	codexOAuthRedirect = "http://localhost:1455/auth/callback"
)

type codexOAuthTokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

func (a *App) beginCodexCodeLogin(login *ui.AuthModalLogin) error {
	provider := "codex"
	label := ""
	active := true
	openBrowser := true
	if login != nil {
		if trimmed := strings.ToLower(strings.TrimSpace(login.Provider)); trimmed != "" {
			provider = trimmed
		}
		label = strings.TrimSpace(login.Label)
		active = login.Active
		openBrowser = login.OpenBrowser
	}
	if provider != "codex" {
		return fmt.Errorf("interactive code login is only supported for codex")
	}

	codeVerifier, err := generateCodeVerifier()
	if err != nil {
		return err
	}
	state, err := randomState()
	if err != nil {
		return err
	}
	authURL := buildCodexAuthURL(codeChallenge(codeVerifier), state)
	a.codexPending = &codexCodeLoginState{
		Provider:     provider,
		Label:        label,
		Active:       active,
		CodeVerifier: codeVerifier,
		State:        state,
		AuthURL:      authURL,
	}

	status := "Remote login selected. Press Enter to copy the full auth URL, then paste the callback URL or code after sign-in."
	a.home.StartAuthModalCodexCallbackPrompt(status, authURL)
	if openBrowser {
		if err := tryOpenBrowser(authURL); err != nil {
			a.showToast(ui.ToastWarning, fmt.Sprintf("could not open browser automatically: %v", err))
		}
	}
	return nil
}

func (a *App) completeProviderLogin(login *ui.AuthModalLogin) {
	if a.home == nil {
		return
	}
	pending := a.codexPending
	if pending == nil {
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalError("no pending codex login; start login again")
		return
	}
	callbackInput := ""
	provider := strings.ToLower(strings.TrimSpace(pending.Provider))
	label := strings.TrimSpace(pending.Label)
	active := pending.Active
	if login != nil {
		callbackInput = strings.TrimSpace(login.CallbackInput)
		if trimmed := strings.ToLower(strings.TrimSpace(login.Provider)); trimmed != "" {
			provider = trimmed
		}
		if trimmed := strings.TrimSpace(login.Label); trimmed != "" {
			label = trimmed
		}
		active = login.Active
	}
	if provider != "codex" {
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalError(fmt.Sprintf("unsupported provider for callback login: %s", provider))
		return
	}
	if callbackInput == "" {
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalError("callback URL or code is required")
		return
	}
	code, callbackState := parseOAuthCallbackInput(callbackInput)
	if code == "" {
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalError("authorization code is required")
		return
	}
	if callbackState != "" && pending.State != "" && callbackState != pending.State {
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalError("oauth state mismatch")
		return
	}

	a.home.SetAuthModalLoading(true)
	tokenCtx, tokenCancel := context.WithTimeout(context.Background(), 45*time.Second)
	tokens, err := exchangeCodexCodeForTokens(tokenCtx, code, pending.CodeVerifier)
	tokenCancel()
	if err != nil {
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalError(fmt.Sprintf("oauth token exchange failed: %v", err))
		return
	}

	upsertCtx, upsertCancel := context.WithTimeout(context.Background(), 6*time.Second)
	record, err := a.api.UpsertAuthCredential(upsertCtx, client.AuthCredentialUpsertRequest{
		Provider:     provider,
		Type:         "oauth",
		Label:        label,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    tokens.ExpiresAt,
		AccountID:    extractAccountID(tokens.AccessToken),
		Active:       active,
	})
	upsertCancel()
	if err != nil {
		a.home.SetAuthModalLoading(false)
		a.home.SetAuthModalError(fmt.Sprintf("save oauth credential failed: %v", err))
		return
	}

	a.codexPending = nil
	_, toast := codexLoginSuccessMessages(provider, label, record.Active)
	a.home.HideAuthModal()
	if strings.TrimSpace(toast) != "" {
		a.showToast(ui.ToastSuccess, toast)
	}
	a.notifyAuthAutoDefaults(record.AutoDefaults)
	a.queueReload(false)
}

func (a *App) applyAuthDefaultsAfterLogin(ctx context.Context, provider, authType string) (*client.AutoDefaultsStatus, error) {
	if a == nil || a.api == nil {
		return nil, errors.New("auth api unavailable")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	authType = strings.ToLower(strings.TrimSpace(authType))
	if provider == "" {
		return nil, nil
	}
	list, err := a.api.ListAuthCredentials(ctx, provider, "", 200)
	if err != nil {
		return nil, err
	}
	var fallback *client.AutoDefaultsStatus
	for _, record := range list.Records {
		if !strings.EqualFold(strings.TrimSpace(record.Provider), provider) {
			continue
		}
		if authType != "" && !strings.EqualFold(strings.TrimSpace(record.AuthType), authType) {
			continue
		}
		if fallback == nil {
			fallback = record.AutoDefaults
		}
		if record.Active {
			return record.AutoDefaults, nil
		}
	}
	return fallback, nil
}

func codexLoginSuccessMessages(provider, label string, active bool) (string, string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "codex"
	}
	label = strings.TrimSpace(label)

	status := "OAuth login saved"
	if label != "" {
		status = fmt.Sprintf("OAuth login saved as %q", label)
	}

	subject := provider
	if label != "" {
		subject = fmt.Sprintf("%s/%s", provider, label)
	}
	toast := fmt.Sprintf("credential saved for %s", subject)
	if active {
		toast += " and set active"
	} else {
		toast += " (not active; existing active credential is unchanged)"
	}
	return status, toast
}

func exchangeCodexCodeForTokens(ctx context.Context, code, codeVerifier string) (codexOAuthTokenSet, error) {
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("client_id", codexOAuthClientID)
	values.Set("code", strings.TrimSpace(code))
	values.Set("redirect_uri", codexOAuthRedirect)
	values.Set("code_verifier", strings.TrimSpace(codeVerifier))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexOAuthTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return codexOAuthTokenSet{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return codexOAuthTokenSet{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return codexOAuthTokenSet{}, err
	}
	if resp.StatusCode >= 400 {
		return codexOAuthTokenSet{}, fmt.Errorf("oauth token exchange failed status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return codexOAuthTokenSet{}, fmt.Errorf("decode oauth token response: %w", err)
	}
	if strings.TrimSpace(decoded.AccessToken) == "" || strings.TrimSpace(decoded.RefreshToken) == "" {
		return codexOAuthTokenSet{}, errors.New("oauth token response missing access_token or refresh_token")
	}

	expiresAt := time.Now().Add(time.Duration(decoded.ExpiresIn) * time.Second).Add(-5 * time.Minute).UnixMilli()
	return codexOAuthTokenSet{
		AccessToken:  decoded.AccessToken,
		RefreshToken: decoded.RefreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}

func waitForLocalCodexOAuthCallback(ctx context.Context, expectedState string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 6*time.Minute)
		defer cancel()
	}

	type result struct {
		callbackInput string
		err           error
	}

	listener, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		return "", fmt.Errorf("start callback server: %w", err)
	}
	defer listener.Close()

	resultCh := make(chan result, 1)
	done := make(chan struct{})
	var once sync.Once
	complete := func(res result) {
		once.Do(func() {
			resultCh <- res
			close(done)
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
			http.Error(w, "callback already received", http.StatusConflict)
			return
		default:
		}

		query := r.URL.Query()
		if oauthErr := strings.TrimSpace(query.Get("error")); oauthErr != "" {
			fmt.Fprint(w, callbackErrorHTML("OAuth error: "+oauthErr))
			complete(result{err: fmt.Errorf("oauth error: %s", oauthErr)})
			return
		}

		code := strings.TrimSpace(query.Get("code"))
		if code == "" {
			fmt.Fprint(w, callbackErrorHTML("Missing authorization code"))
			complete(result{err: errors.New("missing authorization code in callback")})
			return
		}
		state := strings.TrimSpace(query.Get("state"))
		if expectedState != "" && state != expectedState {
			fmt.Fprint(w, callbackErrorHTML("State mismatch"))
			complete(result{err: errors.New("oauth state mismatch")})
			return
		}

		fmt.Fprint(w, callbackSuccessHTML())
		complete(result{callbackInput: "http://" + r.Host + r.URL.RequestURI()})
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErrCh := make(chan error, 1)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			serveErrCh <- serveErr
		}
		close(serveErrCh)
	}()

	var out result
	select {
	case out = <-resultCh:
	case serveErr := <-serveErrCh:
		if serveErr != nil {
			out.err = serveErr
		} else {
			out.err = errors.New("oauth callback server stopped unexpectedly")
		}
	case <-ctx.Done():
		out.err = ctx.Err()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			out.err = errors.New("oauth timeout waiting for callback")
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	if out.err != nil {
		return "", out.err
	}
	return out.callbackInput, nil
}

func oauthStateFromAuthURL(authURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(authURL))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("state"))
}

func buildCodexAuthURL(codeChallengeValue, state string) string {
	authURL, err := url.Parse(codexOAuthAuthURL)
	if err != nil {
		fallback := codexOAuthAuthURL + "?client_id=" + url.QueryEscape(codexOAuthClientID)
		return fallback
	}
	query := authURL.Query()
	query.Set("client_id", codexOAuthClientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", codexOAuthRedirect)
	query.Set("scope", codexOAuthScopes)
	query.Set("code_challenge", codeChallengeValue)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	query.Set("originator", "swarm")
	authURL.RawQuery = query.Encode()
	return authURL.String()
}

func generateCodeVerifier() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func codeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomState() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func parseOAuthCallbackInput(input string) (string, string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", ""
	}

	candidates := []string{trimmed}
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		if strings.HasPrefix(trimmed, "localhost:") || strings.HasPrefix(trimmed, "127.0.0.1:") || strings.HasPrefix(trimmed, "[::1]:") {
			candidates = append(candidates, "http://"+trimmed)
		}
		if strings.HasPrefix(trimmed, "/") {
			candidates = append(candidates, "http://localhost:1455"+trimmed)
		}
		if strings.HasPrefix(trimmed, "?") {
			candidates = append(candidates, "http://localhost:1455/auth/callback"+trimmed)
		}
		if strings.Contains(trimmed, "code=") || strings.Contains(trimmed, "state=") {
			query := trimmed
			if strings.HasPrefix(query, "?") {
				query = query[1:]
			}
			candidates = append(candidates, "http://localhost:1455/auth/callback?"+query)
		}
		if strings.Contains(trimmed, "/auth/callback") {
			candidates = append(candidates, "http://"+trimmed)
		}
	}

	for _, candidate := range candidates {
		parsed, err := url.Parse(candidate)
		if err != nil {
			continue
		}
		code := strings.TrimSpace(parsed.Query().Get("code"))
		state := strings.TrimSpace(parsed.Query().Get("state"))
		if code != "" || state != "" {
			return code, state
		}
	}
	return trimmed, ""
}

func extractAccountID(accessToken string) string {
	parts := strings.Split(strings.TrimSpace(accessToken), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	if account, ok := claims["chatgpt_account_id"].(string); ok && strings.TrimSpace(account) != "" {
		return strings.TrimSpace(account)
	}
	if authClaims, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if account, ok := authClaims["chatgpt_account_id"].(string); ok && strings.TrimSpace(account) != "" {
			return strings.TrimSpace(account)
		}
	}
	if orgs, ok := claims["organizations"].([]any); ok && len(orgs) > 0 {
		if first, ok := orgs[0].(map[string]any); ok {
			if account, ok := first["id"].(string); ok && strings.TrimSpace(account) != "" {
				return strings.TrimSpace(account)
			}
		}
	}
	return ""
}

func callbackSuccessHTML() string {
	return `<!doctype html><html><head><title>Codex Connected</title></head><body style="font-family:system-ui;background:#0b0b0b;color:#e5e7eb;display:flex;align-items:center;justify-content:center;height:100vh;"><div style="text-align:center"><h1 style="color:#22c55e">Connected to Codex</h1><p>You can close this window and return to the terminal.</p></div></body></html>`
}

func callbackErrorHTML(message string) string {
	escaped := strings.ReplaceAll(message, "<", "&lt;")
	escaped = strings.ReplaceAll(escaped, ">", "&gt;")
	return `<!doctype html><html><head><title>Codex Login Failed</title></head><body style="font-family:system-ui;background:#0b0b0b;color:#e5e7eb;display:flex;align-items:center;justify-content:center;height:100vh;"><div style="text-align:center"><h1 style="color:#ef4444">Codex Login Failed</h1><p>` + escaped + `</p></div></body></html>`
}

func tryOpenBrowser(targetURL string) error {
	commands := browserOpenCommands(targetURL)
	var lastErr error
	for _, command := range commands {
		if len(command) == 0 {
			continue
		}
		cmd := exec.Command(command[0], command[1:]...)
		if err := cmd.Start(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("no browser open command available")
	}
	return lastErr
}

func browserOpenCommands(targetURL string) [][]string {
	if isWSL() {
		return [][]string{
			{"wslview", targetURL},
			{"rundll32.exe", "url.dll,FileProtocolHandler", targetURL},
			{"cmd.exe", "/c", "start", "", targetURL},
			{"xdg-open", targetURL},
		}
	}

	switch runtime.GOOS {
	case "windows":
		return [][]string{
			{"rundll32", "url.dll,FileProtocolHandler", targetURL},
			{"cmd", "/c", "start", "", targetURL},
		}
	case "darwin":
		return [][]string{{"open", targetURL}}
	default:
		return [][]string{{"xdg-open", targetURL}, {"open", targetURL}}
	}
}

func isWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if strings.TrimSpace(os.Getenv("WSL_DISTRO_NAME")) != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv("WSL_INTEROP")) != "" {
		return true
	}
	return fileContainsMicrosoft("/proc/sys/kernel/osrelease") || fileContainsMicrosoft("/proc/version")
}

func fileContainsMicrosoft(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), "microsoft")
}
