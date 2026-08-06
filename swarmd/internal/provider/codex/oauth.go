package codex

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
	"strings"
	"sync"
	"time"
)

const (
	codexOAuthAuthorizeURL     = "https://auth.openai.com/oauth/authorize"
	codexOAuthRedirectURL      = "http://localhost:1455/auth/callback"
	codexDeviceRedirectURL     = "https://auth.openai.com/deviceauth/callback"
	codexDeviceUserCodeURL     = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	codexDeviceTokenURL        = "https://auth.openai.com/api/accounts/deviceauth/token"
	codexDeviceVerificationURL = "https://auth.openai.com/codex/device"
	codexOAuthScopes           = "openid profile email offline_access"
	codexOAuthListenHost       = "127.0.0.1:1455"
	codexDeviceAuthWindow      = 15 * time.Minute
	codexDevicePollDefault     = 5 * time.Second
	codexDevicePollMinimum     = 2 * time.Second
	codexDevicePollMaximum     = 10 * time.Second
)

type OAuthLogin struct {
	AuthURL      string
	CodeVerifier string
	State        string
}

type OAuthTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

// DeviceAuthorization contains the user-visible portion of an in-flight device
// login. Its protocol credentials remain private so API responses cannot expose
// the device auth ID or PKCE verifier.
type DeviceAuthorization struct {
	VerificationURL string
	UserCode        string
	ExpiresAt       time.Time

	deviceAuthID string
	interval     time.Duration
}

type deviceAuthEndpoints struct {
	userCodeURL     string
	pollURL         string
	verificationURL string
	tokenURL        string
	redirectURL     string
}

var defaultDeviceAuthEndpoints = deviceAuthEndpoints{
	userCodeURL:     codexDeviceUserCodeURL,
	pollURL:         codexDeviceTokenURL,
	verificationURL: codexDeviceVerificationURL,
	tokenURL:        tokenURL,
	redirectURL:     codexDeviceRedirectURL,
}

// RequestDeviceAuthorization starts OpenAI's device authorization protocol.
// A 404 is a policy signal and is reported explicitly so callers can present
// browser/manual login as a real fallback rather than pretending device login
// succeeded.
func RequestDeviceAuthorization(ctx context.Context) (DeviceAuthorization, error) {
	return requestDeviceAuthorization(ctx, &http.Client{Timeout: 20 * time.Second}, defaultDeviceAuthEndpoints)
}

func requestDeviceAuthorization(ctx context.Context, client *http.Client, endpoints deviceAuthEndpoints) (DeviceAuthorization, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(struct {
		ClientID string `json:"client_id"`
	}{ClientID: clientID})
	if err != nil {
		return DeviceAuthorization{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoints.userCodeURL, strings.NewReader(string(payload)))
	if err != nil {
		return DeviceAuthorization{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return DeviceAuthorization{}, fmt.Errorf("request codex device code: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return DeviceAuthorization{}, fmt.Errorf("read codex device code response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return DeviceAuthorization{}, errors.New("codex device authorization is unavailable or disabled; use browser or manual login")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DeviceAuthorization{}, fmt.Errorf("codex device code request failed status=%d", resp.StatusCode)
	}

	var decoded struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		UserCodeAlt  string          `json:"usercode"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return DeviceAuthorization{}, fmt.Errorf("decode codex device code response: %w", err)
	}
	userCode := strings.TrimSpace(decoded.UserCode)
	if userCode == "" {
		userCode = strings.TrimSpace(decoded.UserCodeAlt)
	}
	if strings.TrimSpace(decoded.DeviceAuthID) == "" || userCode == "" {
		return DeviceAuthorization{}, errors.New("codex device code response missing device_auth_id or user_code")
	}
	return DeviceAuthorization{
		VerificationURL: endpoints.verificationURL,
		UserCode:        userCode,
		ExpiresAt:       time.Now().Add(codexDeviceAuthWindow),
		deviceAuthID:    strings.TrimSpace(decoded.DeviceAuthID),
		interval:        parseDevicePollInterval(decoded.Interval),
	}, nil
}

// CompleteDeviceAuthorization polls the private OpenAI device endpoint until
// approval or expiry, then exchanges the issued code using the returned PKCE
// verifier and the device callback URI.
func CompleteDeviceAuthorization(ctx context.Context, authorization DeviceAuthorization) (OAuthTokens, error) {
	return completeDeviceAuthorization(ctx, &http.Client{Timeout: 20 * time.Second}, defaultDeviceAuthEndpoints, authorization)
}

func completeDeviceAuthorization(ctx context.Context, client *http.Client, endpoints deviceAuthEndpoints, authorization DeviceAuthorization) (OAuthTokens, error) {
	return completeDeviceAuthorizationWithSleep(ctx, client, endpoints, authorization, func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	})
}

func completeDeviceAuthorizationWithSleep(ctx context.Context, client *http.Client, endpoints deviceAuthEndpoints, authorization DeviceAuthorization, sleep func(context.Context, time.Duration) error) (OAuthTokens, error) {
	if sleep == nil {
		return OAuthTokens{}, errors.New("device authorization sleep function is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := authorization.ExpiresAt
	if deadline.IsZero() || deadline.After(time.Now().Add(codexDeviceAuthWindow)) {
		deadline = time.Now().Add(codexDeviceAuthWindow)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	interval := sanitizeDevicePollInterval(authorization.interval)
	for {
		payload, err := json.Marshal(struct {
			DeviceAuthID string `json:"device_auth_id"`
			UserCode     string `json:"user_code"`
		}{DeviceAuthID: authorization.deviceAuthID, UserCode: authorization.UserCode})
		if err != nil {
			return OAuthTokens{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoints.pollURL, strings.NewReader(string(payload)))
		if err != nil {
			return OAuthTokens{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return OAuthTokens{}, errors.New("codex device authorization expired after 15 minutes")
			}
			return OAuthTokens{}, fmt.Errorf("poll codex device authorization: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			return OAuthTokens{}, fmt.Errorf("read codex device authorization response: %w", readErr)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var approved struct {
				AuthorizationCode string `json:"authorization_code"`
				CodeChallenge     string `json:"code_challenge"`
				CodeVerifier      string `json:"code_verifier"`
			}
			if err := json.Unmarshal(body, &approved); err != nil {
				return OAuthTokens{}, fmt.Errorf("decode codex device authorization response: %w", err)
			}
			if strings.TrimSpace(approved.AuthorizationCode) == "" || strings.TrimSpace(approved.CodeChallenge) == "" || strings.TrimSpace(approved.CodeVerifier) == "" {
				return OAuthTokens{}, errors.New("codex device authorization response missing authorization code or PKCE values")
			}
			if oauthCodeChallenge(approved.CodeVerifier) != strings.TrimSpace(approved.CodeChallenge) {
				return OAuthTokens{}, errors.New("codex device authorization PKCE challenge mismatch")
			}
			return exchangeOAuthCode(ctx, client, endpoints.tokenURL, endpoints.redirectURL, approved.AuthorizationCode, approved.CodeVerifier)
		}
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			return OAuthTokens{}, fmt.Errorf("codex device authorization failed status=%d", resp.StatusCode)
		}

		if err := sleep(ctx, interval); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return OAuthTokens{}, errors.New("codex device authorization expired after 15 minutes")
			}
			return OAuthTokens{}, err
		}
	}
}

func parseDevicePollInterval(raw json.RawMessage) time.Duration {
	if len(raw) == 0 || string(raw) == "null" {
		return codexDevicePollDefault
	}
	var seconds int64
	if err := json.Unmarshal(raw, &seconds); err != nil {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return codexDevicePollDefault
		}
		parsed, parseErr := time.ParseDuration(strings.TrimSpace(text) + "s")
		if parseErr != nil {
			return codexDevicePollDefault
		}
		return sanitizeDevicePollInterval(parsed)
	}
	return sanitizeDevicePollInterval(time.Duration(seconds) * time.Second)
}

func sanitizeDevicePollInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return codexDevicePollDefault
	}
	if interval < codexDevicePollMinimum {
		return codexDevicePollMinimum
	}
	if interval > codexDevicePollMaximum {
		return codexDevicePollMaximum
	}
	return interval
}

func StartOAuthLogin() (OAuthLogin, error) {
	codeVerifier, err := generateOAuthCodeVerifier()
	if err != nil {
		return OAuthLogin{}, err
	}
	state, err := randomOAuthState()
	if err != nil {
		return OAuthLogin{}, err
	}
	return OAuthLogin{
		AuthURL:      buildOAuthAuthURL(oauthCodeChallenge(codeVerifier), state),
		CodeVerifier: codeVerifier,
		State:        state,
	}, nil
}

func WaitForOAuthCallback(ctx context.Context, codeVerifier, expectedState string) (OAuthTokens, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}

	type result struct {
		tokens OAuthTokens
		err    error
	}

	listener, err := net.Listen("tcp", codexOAuthListenHost)
	if err != nil {
		return OAuthTokens{}, fmt.Errorf("start callback server: %w", err)
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

		tokens, err := ExchangeOAuthCode(r.Context(), code, codeVerifier)
		if err != nil {
			fmt.Fprint(w, callbackErrorHTML("Token exchange failed"))
			complete(result{err: err})
			return
		}
		fmt.Fprint(w, callbackSuccessHTML())
		complete(result{tokens: tokens})
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
		return OAuthTokens{}, out.err
	}
	return out.tokens, nil
}

func ExchangeOAuthCode(ctx context.Context, code, codeVerifier string) (OAuthTokens, error) {
	return exchangeOAuthCode(ctx, &http.Client{Timeout: 20 * time.Second}, tokenURL, codexOAuthRedirectURL, code, codeVerifier)
}

func exchangeOAuthCode(ctx context.Context, client *http.Client, endpoint, redirectURI, code, codeVerifier string) (OAuthTokens, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("client_id", clientID)
	values.Set("code", strings.TrimSpace(code))
	values.Set("redirect_uri", strings.TrimSpace(redirectURI))
	values.Set("code_verifier", strings.TrimSpace(codeVerifier))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return OAuthTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return OAuthTokens{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return OAuthTokens{}, err
	}
	if resp.StatusCode >= 400 {
		return OAuthTokens{}, fmt.Errorf("oauth token exchange failed status=%d", resp.StatusCode)
	}

	var decoded struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return OAuthTokens{}, fmt.Errorf("decode oauth token response: %w", err)
	}
	if strings.TrimSpace(decoded.AccessToken) == "" || strings.TrimSpace(decoded.RefreshToken) == "" {
		return OAuthTokens{}, errors.New("oauth token response missing access_token or refresh_token")
	}

	expiresAt := time.Now().Add(time.Duration(decoded.ExpiresIn) * time.Second).Add(-5 * time.Minute).UnixMilli()
	return OAuthTokens{
		AccessToken:  decoded.AccessToken,
		RefreshToken: decoded.RefreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}

func ParseOAuthCallbackInput(input string) (string, string) {
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

func ExtractAccountID(token string) string {
	return extractAccountIDFromToken(token)
}

func buildOAuthAuthURL(codeChallengeValue, state string) string {
	authURL, err := url.Parse(codexOAuthAuthorizeURL)
	if err != nil {
		return codexOAuthAuthorizeURL + "?client_id=" + url.QueryEscape(clientID)
	}
	query := authURL.Query()
	query.Set("client_id", clientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", codexOAuthRedirectURL)
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

func generateOAuthCodeVerifier() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func oauthCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomOAuthState() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func callbackSuccessHTML() string {
	return `<!doctype html><html><head><title>Codex Connected</title></head><body style="font-family:system-ui;background:#0b0b0b;color:#e5e7eb;display:flex;align-items:center;justify-content:center;height:100vh;"><div style="text-align:center"><h1 style="color:#22c55e">Connected to Codex</h1><p>You can close this window and return to settings.</p></div></body></html>`
}

func callbackErrorHTML(message string) string {
	escaped := strings.ReplaceAll(message, "<", "&lt;")
	escaped = strings.ReplaceAll(escaped, ">", "&gt;")
	return `<!doctype html><html><head><title>Codex Login Failed</title></head><body style="font-family:system-ui;background:#0b0b0b;color:#e5e7eb;display:flex;align-items:center;justify-content:center;height:100vh;"><div style="text-align:center"><h1 style="color:#ef4444">Codex Login Failed</h1><p>` + escaped + `</p></div></body></html>`
}
