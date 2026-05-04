package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	defaultDaemonURL        = "http://127.0.0.1:7781"
	localTransportSocketEnv = "SWARMD_LOCAL_TRANSPORT_SOCKET"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "health":
		err = cmdHealth(os.Args[2:])
	case "auth":
		err = cmdAuth(os.Args[2:])
	case "model":
		err = cmdModel(os.Args[2:])
	case "workspace":
		err = cmdWorkspace(os.Args[2:])
	case "context":
		err = cmdContext(os.Args[2:])
	case "session":
		err = cmdSession(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "swarmctl error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("Usage:")
	fmt.Println("  swarmctl health [--addr URL]")
	fmt.Println("  swarmctl auth codex status [--addr URL]")
	fmt.Println("  swarmctl auth codex login [--method auto|code] [--label NAME] [--addr URL]")
	fmt.Println("  swarmctl auth codex remote [--label NAME] [--addr URL]")
	fmt.Println("  swarmctl auth codex set --api-key KEY [--label NAME] [--addr URL]")
	fmt.Println("  swarmctl auth attach rotate [--addr URL]  # requires SWARMD_TOKEN")
	fmt.Println("  swarmctl model get [--addr URL]")
	fmt.Println("  swarmctl model set --provider codex --model gpt-5.4 --thinking xhigh [--addr URL]")
	fmt.Println("  swarmctl model catalog get --provider codex [--model MODEL] [--limit N] [--addr URL]")
	fmt.Println("  swarmctl workspace resolve [--cwd PATH] [--addr URL]")
	fmt.Println("  swarmctl context sources [--cwd PATH] [--addr URL]")
	fmt.Println("  swarmctl session list [--limit N] [--addr URL]")
	fmt.Println("  swarmctl session create [--title TEXT] [--workspace-path PATH] [--workspace-name NAME] [--addr URL]")
	fmt.Println("  swarmctl session get --id SESSION_ID [--addr URL]")
	fmt.Println("  swarmctl session messages --id SESSION_ID [--after-seq N] [--limit N] [--addr URL]")
	fmt.Println("  swarmctl session send --id SESSION_ID --role user --content TEXT [--addr URL]")
	fmt.Println("  swarmctl session run --id SESSION_ID --prompt TEXT [--addr URL]")
	fmt.Println("")
	fmt.Println("Auth token for protected APIs can be passed with env: SWARMD_TOKEN=<token>")
}

func cmdHealth(args []string) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return getJSON(*addr+"/healthz", nil)
}

func cmdAuth(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: swarmctl auth <codex|attach> ...")
	}

	switch args[0] {
	case "codex":
		if len(args) < 2 {
			return fmt.Errorf("usage: swarmctl auth codex <status|login|remote|set>")
		}
		switch args[1] {
		case "status":
			fs := flag.NewFlagSet("auth codex status", flag.ContinueOnError)
			addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			return getJSON(*addr+"/v1/auth/codex", nil)
		case "remote":
			fs := flag.NewFlagSet("auth codex remote", flag.ContinueOnError)
			addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
			label := fs.String("label", "", "optional credential label")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			return cmdAuthCodexLogin(*addr, "code", false, *label)
		case "login":
			fs := flag.NewFlagSet("auth codex login", flag.ContinueOnError)
			addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
			method := fs.String("method", "auto", "login method: auto or code")
			label := fs.String("label", "", "optional credential label")
			noOpen := fs.Bool("no-open", false, "do not attempt to open browser automatically")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			return cmdAuthCodexLogin(*addr, *method, !*noOpen, *label)
		case "set":
			fs := flag.NewFlagSet("auth codex set", flag.ContinueOnError)
			addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
			apiKey := fs.String("api-key", "", "codex api key")
			label := fs.String("label", "", "optional credential label")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			key := strings.TrimSpace(*apiKey)
			if key == "" {
				key = strings.TrimSpace(os.Getenv("CODEX_API_KEY"))
			}
			if key == "" {
				return fmt.Errorf("api key is required via --api-key or CODEX_API_KEY")
			}
			var credential authCredentialStatus
			if err := postJSON(*addr+"/v1/auth/credentials", map[string]any{
				"provider": "codex",
				"type":     "api",
				"label":    strings.TrimSpace(*label),
				"api_key":  key,
				"active":   true,
			}, &credential); err != nil {
				return wrapAttachTokenHint(err)
			}
			if strings.TrimSpace(credential.Label) != "" {
				fmt.Printf("Codex API key saved as %q.\n", strings.TrimSpace(credential.Label))
				return nil
			}
			fmt.Println("Codex API key saved.")
			return nil
		default:
			return fmt.Errorf("usage: swarmctl auth codex <status|login|remote|set>")
		}
	case "attach":
		if len(args) < 2 {
			return fmt.Errorf("usage: swarmctl auth attach <rotate>")
		}
		switch args[1] {
		case "rotate":
			fs := flag.NewFlagSet("auth attach rotate", flag.ContinueOnError)
			addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			return postJSON(*addr+"/v1/auth/attach/rotate", map[string]string{}, nil)
		default:
			return fmt.Errorf("usage: swarmctl auth attach <rotate>")
		}
	default:
		return fmt.Errorf("usage: swarmctl auth <codex|attach> ...")
	}
}

func cmdModel(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: swarmctl model <get|set>")
	}

	switch args[0] {
	case "get":
		fs := flag.NewFlagSet("model get", flag.ContinueOnError)
		addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return getJSON(*addr+"/v1/model", nil)
	case "set":
		fs := flag.NewFlagSet("model set", flag.ContinueOnError)
		addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
		provider := fs.String("provider", "codex", "provider id")
		model := fs.String("model", "gpt-5.4", "model id")
		thinking := fs.String("thinking", "xhigh", "thinking level")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		payload := map[string]string{
			"provider": *provider,
			"model":    *model,
			"thinking": *thinking,
		}
		return postJSON(*addr+"/v1/model", payload, nil)
	case "catalog":
		if len(args) < 2 {
			return fmt.Errorf("usage: swarmctl model catalog <get>")
		}
		switch args[1] {
		case "get":
			fs := flag.NewFlagSet("model catalog get", flag.ContinueOnError)
			addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
			provider := fs.String("provider", "codex", "provider id")
			model := fs.String("model", "", "model id")
			limit := fs.Int("limit", 500, "max records when listing catalog")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			q := url.Values{}
			q.Set("provider", *provider)
			if strings.TrimSpace(*model) != "" {
				q.Set("model", strings.TrimSpace(*model))
			} else {
				q.Set("limit", fmt.Sprintf("%d", *limit))
			}
			return getJSON(*addr+"/v1/model/catalog?"+q.Encode(), nil)
		default:
			return fmt.Errorf("usage: swarmctl model catalog <get>")
		}
	default:
		return fmt.Errorf("usage: swarmctl model <get|set>")
	}
}

func cmdWorkspace(args []string) error {
	if len(args) < 1 || args[0] != "resolve" {
		return fmt.Errorf("usage: swarmctl workspace resolve [--cwd PATH]")
	}
	fs := flag.NewFlagSet("workspace resolve", flag.ContinueOnError)
	addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
	cwd := fs.String("cwd", "", "directory to bind")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	path := *addr + "/v1/workspace/resolve"
	if strings.TrimSpace(*cwd) != "" {
		path = path + "?cwd=" + url.QueryEscape(*cwd)
	}
	return getJSON(path, nil)
}

func cmdContext(args []string) error {
	if len(args) < 1 || args[0] != "sources" {
		return fmt.Errorf("usage: swarmctl context sources [--cwd PATH]")
	}
	fs := flag.NewFlagSet("context sources", flag.ContinueOnError)
	addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
	cwd := fs.String("cwd", "", "directory to scan")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	path := *addr + "/v1/context/sources"
	if strings.TrimSpace(*cwd) != "" {
		path += "?cwd=" + url.QueryEscape(*cwd)
	}
	return getJSON(path, nil)
}

func cmdSession(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: swarmctl session <list|create|get|messages|send|run>")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("session list", flag.ContinueOnError)
		addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
		limit := fs.Int("limit", 100, "max sessions")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return getJSON(fmt.Sprintf("%s/v1/sessions?limit=%d", *addr, *limit), nil)
	case "create":
		fs := flag.NewFlagSet("session create", flag.ContinueOnError)
		addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
		title := fs.String("title", "New Session", "session title")
		workspacePath := fs.String("workspace-path", "", "workspace path override")
		workspaceName := fs.String("workspace-name", "", "workspace name override")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		payload := map[string]string{
			"title":          *title,
			"workspace_path": *workspacePath,
			"workspace_name": *workspaceName,
		}
		return postJSON(*addr+"/v1/sessions", payload, nil)
	case "get":
		fs := flag.NewFlagSet("session get", flag.ContinueOnError)
		addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
		id := fs.String("id", "", "session id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*id) == "" {
			return fmt.Errorf("--id is required")
		}
		return getJSON(*addr+"/v1/sessions/"+url.PathEscape(*id), nil)
	case "messages":
		fs := flag.NewFlagSet("session messages", flag.ContinueOnError)
		addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
		id := fs.String("id", "", "session id")
		after := fs.Uint64("after-seq", 0, "return messages after global sequence")
		limit := fs.Int("limit", 500, "max messages")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*id) == "" {
			return fmt.Errorf("--id is required")
		}
		q := url.Values{}
		if *after > 0 {
			q.Set("after_seq", fmt.Sprintf("%d", *after))
		}
		q.Set("limit", fmt.Sprintf("%d", *limit))
		return getJSON(*addr+"/v1/sessions/"+url.PathEscape(*id)+"/messages?"+q.Encode(), nil)
	case "send":
		fs := flag.NewFlagSet("session send", flag.ContinueOnError)
		addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
		id := fs.String("id", "", "session id")
		role := fs.String("role", "user", "message role")
		content := fs.String("content", "", "message content")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*id) == "" {
			return fmt.Errorf("--id is required")
		}
		if strings.TrimSpace(*content) == "" {
			return fmt.Errorf("--content is required")
		}
		payload := map[string]string{
			"role":    *role,
			"content": *content,
		}
		return postJSON(*addr+"/v1/sessions/"+url.PathEscape(*id)+"/messages", payload, nil)
	case "run":
		fs := flag.NewFlagSet("session run", flag.ContinueOnError)
		addr := fs.String("addr", defaultDaemonURL, "daemon base URL")
		id := fs.String("id", "", "session id")
		prompt := fs.String("prompt", "", "prompt to send to codex")
		instructions := fs.String("instructions", "", "override run instructions")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*id) == "" {
			return fmt.Errorf("--id is required")
		}
		if strings.TrimSpace(*prompt) == "" {
			return fmt.Errorf("--prompt is required")
		}
		payload := map[string]any{
			"prompt":       *prompt,
			"instructions": *instructions,
		}
		return postJSON(*addr+"/v1/sessions/"+url.PathEscape(*id)+"/run", payload, nil)
	default:
		return fmt.Errorf("usage: swarmctl session <list|create|get|messages|send|run>")
	}
}

func cmdAuthCodexLogin(addr, method string, openBrowser bool, label string) error {
	method = strings.ToLower(strings.TrimSpace(method))
	if method == "" {
		method = "auto"
	}
	if method != "auto" && method != "code" {
		return fmt.Errorf("unsupported login method %q (expected auto or code)", method)
	}

	var session codexOAuthSessionStatus
	if err := postJSON(addr+"/v1/auth/codex/oauth/start", map[string]any{
		"provider": "codex",
		"label":    strings.TrimSpace(label),
		"active":   true,
		"method":   "manual",
	}, &session); err != nil {
		return wrapAttachTokenHint(err)
	}

	authURL := strings.TrimSpace(session.AuthURL)
	if authURL == "" {
		return errors.New("codex oauth start returned empty auth_url")
	}
	fmt.Printf("Open this URL to login:\n%s\n", authURL)

	if openBrowser {
		if err := tryOpenBrowser(authURL); err != nil {
			fmt.Fprintf(os.Stderr, "swarmctl warning: failed to open browser automatically: %v\n", err)
		}
	}

	switch method {
	case "auto":
		fmt.Println("Waiting for OAuth callback on localhost:1455 (timeout 6m)...")
		callbackInput, err := waitForLocalCodexOAuthCallback(context.Background(), oauthStateFromAuthURL(authURL))
		if err != nil {
			return err
		}
		if err := postJSON(addr+"/v1/auth/codex/oauth/complete", map[string]any{
			"session_id":     strings.TrimSpace(session.SessionID),
			"callback_input": callbackInput,
		}, &session); err != nil {
			return wrapAttachTokenHint(err)
		}
	case "code":
		fmt.Println("After login, paste the full callback URL or just the authorization code, then press Enter:")
		reader := bufio.NewReader(os.Stdin)
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if err := postJSON(addr+"/v1/auth/codex/oauth/complete", map[string]any{
			"session_id":     strings.TrimSpace(session.SessionID),
			"callback_input": strings.TrimSpace(line),
		}, &session); err != nil {
			return wrapAttachTokenHint(err)
		}
	}

	switch strings.ToLower(strings.TrimSpace(session.Status)) {
	case "success":
	case "error":
		errText := strings.TrimSpace(session.Error)
		if errText == "" {
			errText = "codex oauth login failed"
		}
		return errors.New(errText)
	default:
		return fmt.Errorf("unexpected oauth status %q", session.Status)
	}

	if savedLabel := strings.TrimSpace(session.Label); savedLabel != "" {
		fmt.Printf("Codex OAuth login saved as %q.\n", savedLabel)
		return nil
	}
	if session.Credential != nil && strings.TrimSpace(session.Credential.Label) != "" {
		fmt.Printf("Codex OAuth login saved as %q.\n", strings.TrimSpace(session.Credential.Label))
		return nil
	}
	fmt.Println("Codex OAuth login saved.")
	return nil
}

type authCredentialStatus struct {
	Label  string `json:"label"`
	Active bool   `json:"active"`
}

type codexOAuthSessionStatus struct {
	SessionID  string                `json:"session_id"`
	Label      string                `json:"label"`
	AuthURL    string                `json:"auth_url"`
	Status     string                `json:"status"`
	Error      string                `json:"error"`
	Credential *authCredentialStatus `json:"credential"`
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

func wrapAttachTokenHint(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "status=401") {
		return fmt.Errorf("%w (set SWARMD_TOKEN first via an existing authenticated local/admin flow before retrying)", err)
	}
	return err
}

func oauthStateFromAuthURL(authURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(authURL))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("state"))
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
		return [][]string{
			{"open", targetURL},
		}
	default:
		return [][]string{
			{"xdg-open", targetURL},
			{"open", targetURL},
		}
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

func callbackSuccessHTML() string {
	return `<!doctype html><html><head><title>Codex Connected</title></head><body style="font-family:system-ui;background:#0b0b0b;color:#e5e7eb;display:flex;align-items:center;justify-content:center;height:100vh;"><div style="text-align:center"><h1 style="color:#22c55e">Connected to Codex</h1><p>You can close this window and return to the terminal.</p></div></body></html>`
}

func callbackErrorHTML(message string) string {
	escaped := strings.ReplaceAll(message, "<", "&lt;")
	escaped = strings.ReplaceAll(escaped, ">", "&gt;")
	return `<!doctype html><html><head><title>Codex Login Failed</title></head><body style="font-family:system-ui;background:#0b0b0b;color:#e5e7eb;display:flex;align-items:center;justify-content:center;height:100vh;"><div style="text-align:center"><h1 style="color:#ef4444">Codex Login Failed</h1><p>` + escaped + `</p></div></body></html>`
}

func getJSON(rawURL string, out any) error {
	client := localTransportHTTPClient()
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	reqURL := requestURL(rawURL)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	addAttachTokenHeader(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return printOrDecode(resp, out)
}

func postJSON(rawURL string, input any, out any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}

	client := localTransportHTTPClient()
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	reqURL := requestURL(rawURL)
	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	addAttachTokenHeader(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return printOrDecode(resp, out)
}

func localTransportHTTPClient() *http.Client {
	socketPath := strings.TrimSpace(os.Getenv(localTransportSocketEnv))
	if socketPath == "" {
		return nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimSpace(socketPath))
	}
	return &http.Client{Timeout: 120 * time.Second, Transport: transport}
}

func requestURL(rawURL string) string {
	if localTransportHTTPClient() == nil {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil {
		return rawURL
	}
	parsed.Scheme = "http"
	parsed.Host = "swarm-local-transport"
	return parsed.String()
}

func defaultLocalTransportSocketPath() string {
	lane := strings.TrimSpace(os.Getenv("SWARM_LANE"))
	if lane == "" {
		lane = "main"
	}
	if dataDir := strings.TrimSpace(os.Getenv("DATA_DIR")); dataDir != "" {
		return filepath.Join(dataDir, "local-transport", "api.sock")
	}
	dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "swarmd", lane, "local-transport", "api.sock")
}

func addAttachTokenHeader(req *http.Request) {
	if req == nil {
		return
	}
	if strings.TrimSpace(os.Getenv(localTransportSocketEnv)) == "" {
		if socketPath := strings.TrimSpace(defaultLocalTransportSocketPath()); socketPath != "" {
			if info, err := os.Stat(socketPath); err == nil && !info.IsDir() {
				_ = os.Setenv(localTransportSocketEnv, socketPath)
			}
		}
	}
	token := strings.TrimSpace(os.Getenv("SWARMD_TOKEN"))
	if token == "" {
		return
	}
	req.Header.Set("X-Swarm-Token", token)
}

func printOrDecode(resp *http.Response, out any) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if out == nil {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err == nil {
			_, _ = pretty.WriteTo(os.Stdout)
			_, _ = os.Stdout.WriteString("\n")
			return nil
		}
		_, _ = os.Stdout.Write(body)
		_, _ = os.Stdout.WriteString("\n")
		return nil
	}

	return json.Unmarshal(body, out)
}
