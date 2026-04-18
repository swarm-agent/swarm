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
	codexOAuthAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	codexOAuthRedirectURL  = "http://localhost:1455/auth/callback"
	codexOAuthScopes       = "openid profile email offline_access"
	codexOAuthListenHost   = "127.0.0.1:1455"
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
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("client_id", clientID)
	values.Set("code", strings.TrimSpace(code))
	values.Set("redirect_uri", codexOAuthRedirectURL)
	values.Set("code_verifier", strings.TrimSpace(codeVerifier))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return OAuthTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 20 * time.Second}
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
		return OAuthTokens{}, fmt.Errorf("oauth token exchange failed status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
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
