// Package claudecode runs the locally installed, unmodified Claude Code CLI as a
// Swarm provider. Swarm never performs a Claude sign-in and never reads, stores,
// or forwards Claude credentials: the person signs in with `claude auth login`
// through Anthropic's own flow, and Swarm only starts the binary and asks it for
// its authentication status.
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ProviderID = "claudecode"

	// CLIPathEnv names an explicit Claude Code executable. When unset, `claude`
	// is resolved on the daemon's PATH.
	CLIPathEnv = "SWARM_CLAUDE_CODE_CLI_PATH"

	defaultCLIName       = "claude"
	authStatusTimeout    = 10 * time.Second
	versionTimeout       = 10 * time.Second
	statusCacheTTL       = 15 * time.Second
	maxCLIOutputBytes    = 64 * 1024
	minimumCLIVersionStr = "2.1.248"
)

// minimumCLIVersion is the first Claude Code release with --restricted, which
// the runner relies on to keep user/project settings (and their hooks) out of
// Swarm-driven turns.
var minimumCLIVersion = cliVersion{2, 1, 248}

// passthroughEnvKeys is the complete set of daemon environment variables handed
// to the CLI. Credential-bearing variables (ANTHROPIC_API_KEY,
// ANTHROPIC_AUTH_TOKEN, CLAUDE_CODE_OAUTH_TOKEN, ...) are deliberately absent:
// the CLI must authenticate from the sign-in the person completed themselves.
var passthroughEnvKeys = []string{
	"PATH",
	"HOME",
	"USER",
	"LOGNAME",
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"TZ",
	"TMPDIR",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_CACHE_HOME",
	"XDG_RUNTIME_DIR",
	"CLAUDE_CONFIG_DIR",
	"HTTPS_PROXY",
	"HTTP_PROXY",
	"NO_PROXY",
	"https_proxy",
	"http_proxy",
	"no_proxy",
	"NODE_EXTRA_CA_CERTS",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
}

type cliVersion [3]int

var cliVersionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

func parseCLIVersion(output string) (cliVersion, bool) {
	match := cliVersionPattern.FindStringSubmatch(output)
	if len(match) != 4 {
		return cliVersion{}, false
	}
	var out cliVersion
	for i := 0; i < 3; i++ {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return cliVersion{}, false
		}
		out[i] = value
	}
	return out, true
}

func (v cliVersion) atLeast(min cliVersion) bool {
	for i := 0; i < 3; i++ {
		if v[i] != min[i] {
			return v[i] > min[i]
		}
	}
	return true
}

func (v cliVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

// AuthStatus is the subset of `claude auth status` JSON that Swarm reads. It
// contains no credential material.
type AuthStatus struct {
	LoggedIn    bool   `json:"loggedIn"`
	AuthMethod  string `json:"authMethod"`
	APIProvider string `json:"apiProvider"`
	// SubscriptionType is the Claude plan (for example "max"), when reported.
	SubscriptionType string `json:"subscriptionType,omitempty"`
}

// CLIStatus is the observed local Claude Code installation state.
type CLIStatus struct {
	Path    string
	Version string
	Auth    AuthStatus
	Ready   bool
	Reason  string
}

// CLI locates and inspects the local Claude Code executable.
type CLI struct {
	lookupEnv func(string) (string, bool)
	lookPath  func(string) (string, error)
	run       func(ctx context.Context, path string, env []string, args ...string) ([]byte, error)
	now       func() time.Time

	mu       sync.Mutex
	cached   CLIStatus
	limits   *RateLimits
	cachedAt time.Time
}

func NewCLI() *CLI {
	return &CLI{
		lookupEnv: os.LookupEnv,
		lookPath:  exec.LookPath,
		run:       runCLICommand,
		now:       time.Now,
	}
}

// ResolvePath returns the absolute executable path for the configured CLI.
func (c *CLI) ResolvePath() (string, error) {
	name := defaultCLIName
	if configured, ok := c.lookupEnv(CLIPathEnv); ok && strings.TrimSpace(configured) != "" {
		name = strings.TrimSpace(configured)
	}
	path, err := c.lookPath(name)
	if err != nil {
		if name == defaultCLIName {
			return "", fmt.Errorf("Claude Code CLI %q was not found on the daemon PATH; install Claude Code or set %s", defaultCLIName, CLIPathEnv)
		}
		return "", fmt.Errorf("Claude Code CLI %s=%q is not executable: %w", CLIPathEnv, name, err)
	}
	return path, nil
}

// Env returns the exact environment for a CLI child process.
func (c *CLI) Env(extra ...string) []string {
	env := make([]string, 0, len(passthroughEnvKeys)+len(extra))
	for _, key := range passthroughEnvKeys {
		if value, ok := c.lookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env, extra...)
}

// Status reports whether the local CLI is installed, recent enough, and signed
// in. Results are cached briefly because provider listings poll Status.
func (c *CLI) Status(ctx context.Context) CLIStatus {
	c.mu.Lock()
	if !c.cachedAt.IsZero() && c.now().Sub(c.cachedAt) < statusCacheTTL {
		cached := c.cached
		c.mu.Unlock()
		return cached
	}
	c.mu.Unlock()

	status := c.probe(ctx)

	c.mu.Lock()
	c.cached = status
	c.cachedAt = c.now()
	c.mu.Unlock()
	return status
}

// Refresh discards the cached status and probes again.
func (c *CLI) Refresh(ctx context.Context) CLIStatus {
	c.mu.Lock()
	c.cachedAt = time.Time{}
	c.mu.Unlock()
	return c.Status(ctx)
}

func (c *CLI) probe(ctx context.Context) CLIStatus {
	path, err := c.ResolvePath()
	if err != nil {
		return CLIStatus{Reason: err.Error()}
	}
	status := CLIStatus{Path: path}
	env := c.Env()

	versionCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), versionTimeout)
	versionOut, err := c.run(versionCtx, path, env, "--version")
	cancel()
	if err != nil {
		status.Reason = fmt.Sprintf("Claude Code CLI version check failed: %v", err)
		return status
	}
	version, ok := parseCLIVersion(string(versionOut))
	if !ok {
		status.Reason = "Claude Code CLI returned an unrecognized version"
		return status
	}
	status.Version = version.String()
	if !version.atLeast(minimumCLIVersion) {
		status.Reason = fmt.Sprintf("Claude Code CLI %s is too old; Swarm requires %s or later (run `claude update`)", status.Version, minimumCLIVersionStr)
		return status
	}

	authCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), authStatusTimeout)
	authOut, authErr := c.run(authCtx, path, env, "auth", "status")
	cancel()
	auth, parseErr := parseAuthStatus(authOut)
	if parseErr != nil {
		if authErr != nil {
			status.Reason = fmt.Sprintf("Claude Code auth status check failed: %v", authErr)
		} else {
			status.Reason = fmt.Sprintf("Claude Code auth status was unreadable: %v", parseErr)
		}
		return status
	}
	status.Auth = auth
	if !auth.LoggedIn || strings.EqualFold(strings.TrimSpace(auth.AuthMethod), "none") || strings.TrimSpace(auth.AuthMethod) == "" {
		status.Reason = "Claude Code is not signed in for the daemon's user; run `claude auth login` as that user"
		return status
	}
	status.Ready = true
	status.Reason = fmt.Sprintf("Claude Code %s signed in (%s)", status.Version, authMethodLabel(auth.AuthMethod))
	return status
}

func parseAuthStatus(output []byte) (AuthStatus, error) {
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return AuthStatus{}, errors.New("empty output")
	}
	var status AuthStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return AuthStatus{}, err
	}
	return status, nil
}

func authMethodLabel(method string) string {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "claude.ai":
		return "Claude account"
	case "api_key", "api_key_helper":
		return "Claude Console API key configured in Claude Code"
	case "third_party":
		return "third-party inference provider configured in Claude Code"
	case "oauth_token":
		return "long-lived Claude token configured for Claude Code"
	default:
		return strings.TrimSpace(method)
	}
}

func runCLICommand(ctx context.Context, path string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Stdin = nil
	var stdout, stderr limitedBuffer
	stdout.limit = maxCLIOutputBytes
	stderr.limit = maxCLIOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		return stdout.Bytes(), ctx.Err()
	}
	return stdout.Bytes(), err
}

// limitedBuffer keeps at most limit bytes and silently discards the rest.
type limitedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			b.buf.Write(p[:remaining])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *limitedBuffer) String() string {
	return string(b.Bytes())
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}
