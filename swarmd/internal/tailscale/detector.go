// Package tailscale inspects the local Tailscale daemon's read-only status.
// It never mutates Tailscale Serve or Funnel configuration.
package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	defaultCommandTimeout = 2 * time.Second
	maximumCacheAge       = 5 * time.Second
)

// Classification describes whether a Tailscale Serve route is safe for the
// Swarm desktop listener.
type Classification string

const (
	ClassificationVerifiedSwarmDesktop Classification = "verified_swarm_desktop"
	ClassificationWrongTarget          Classification = "wrong_target"
	ClassificationFunnelEnabled        Classification = "funnel_enabled"
	ClassificationUnsupportedHandler   Classification = "unsupported_handler"
	ClassificationInvalid              Classification = "invalid"
	ClassificationUnconfigured         Classification = "unconfigured"
	ClassificationIncompatible         Classification = "incompatible"
	ClassificationUnavailable          Classification = "unavailable"
)

// RefreshMode controls whether a successful cached snapshot may be returned.
type RefreshMode uint8

const (
	UseCache RefreshMode = iota
	RequireFresh
)

// Runner executes one read-only Tailscale command. Implementations must honor
// context cancellation.
type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(context.Context, string, ...string) ([]byte, error)

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, path, args...).CombinedOutput()
}

// Listener is the effective Swarm desktop HTTP listener.
type Listener struct {
	Host string
	Port int
}

// Config configures a Detector.
type Config struct {
	Listener       Listener
	Runner         Runner
	Binary         string
	SocketPath     string
	CommandTimeout time.Duration
	CacheTTL       time.Duration
	Now            func() time.Time
}

// Route is one Serve web authority and its current security classification.
type Route struct {
	Origin         string         `json:"origin,omitempty"`
	Authority      string         `json:"authority"`
	ProxyTarget    string         `json:"proxy_target,omitempty"`
	Classification Classification `json:"classification"`
	Reason         string         `json:"reason,omitempty"`
}

// Snapshot is a successful, mutually consistent read of Tailscale status,
// Serve status, and Funnel status.
type Snapshot struct {
	DetectedAt  time.Time `json:"detected_at"`
	SelfDNSName string    `json:"self_dns_name"`
	SelfOrigin  string    `json:"self_origin"`
	Routes      []Route   `json:"routes"`
	Remediation string    `json:"remediation,omitempty"`
}

// RouteForOrigin returns the route for an exact normalized HTTPS origin.
func (s Snapshot) RouteForOrigin(raw string) (Route, bool) {
	origin, err := NormalizeHTTPSOrigin(raw)
	if err != nil {
		return Route{}, false
	}
	for _, route := range s.Routes {
		if route.Origin == origin {
			return route, true
		}
	}
	return Route{}, false
}

// CommandError reports a failed read-only Tailscale invocation.
type CommandError struct {
	Args   []string
	Output string
	Err    error
}

func (e *CommandError) Error() string {
	command := strings.Join(append([]string{"tailscale"}, e.Args...), " ")
	if e.Output != "" {
		return fmt.Sprintf("%s failed: %s", command, e.Output)
	}
	return fmt.Sprintf("%s failed: %v", command, e.Err)
}

func (e *CommandError) Unwrap() error { return e.Err }

// SchemaError reports JSON that cannot be safely interpreted as the expected
// Tailscale status schema.
type SchemaError struct {
	Command string
	Err     error
}

func (e *SchemaError) Error() string {
	return fmt.Sprintf("parse tailscale %s schema: %v", e.Command, e.Err)
}

func (e *SchemaError) Unwrap() error { return e.Err }

// Detector performs and caches read-only Tailscale inspection.
type Detector struct {
	listener       Listener
	runner         Runner
	binary         string
	socketPath     string
	commandTimeout time.Duration
	cacheTTL       time.Duration
	now            func() time.Time

	mu         sync.RWMutex
	cached     Snapshot
	cachedAt   time.Time
	hasCache   bool
	generation uint64
	group      singleflight.Group
}

// NewDetector returns a read-only detector for the effective desktop listener.
func NewDetector(cfg Config) (*Detector, error) {
	listener, err := normalizeListener(cfg.Listener)
	if err != nil {
		return nil, err
	}
	if cfg.Runner == nil {
		cfg.Runner = execRunner{}
	}
	if strings.TrimSpace(cfg.Binary) == "" {
		cfg.Binary = "tailscale"
	} else if strings.TrimSpace(cfg.Binary) != "tailscale" {
		return nil, errors.New("detector binary must be tailscale")
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = strings.TrimSpace(os.Getenv("TS_SOCKET"))
	} else {
		cfg.SocketPath = strings.TrimSpace(cfg.SocketPath)
	}
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = defaultCommandTimeout
	}
	if cfg.CacheTTL <= 0 || cfg.CacheTTL > maximumCacheAge {
		cfg.CacheTTL = maximumCacheAge
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Detector{
		listener:       listener,
		runner:         cfg.Runner,
		binary:         "tailscale",
		socketPath:     cfg.SocketPath,
		commandTimeout: cfg.CommandTimeout,
		cacheTTL:       cfg.CacheTTL,
		now:            cfg.Now,
	}, nil
}

// Invalidate discards a successful cached snapshot. Callers should invalidate
// before status reads and before or after any related policy mutation.
func (d *Detector) Invalidate() {
	d.mu.Lock()
	d.clearCacheLocked()
	d.generation++
	d.mu.Unlock()
}

func (d *Detector) clearCache() {
	d.mu.Lock()
	d.clearCacheLocked()
	d.mu.Unlock()
}

func (d *Detector) clearCacheLocked() {
	d.cached = Snapshot{}
	d.cachedAt = time.Time{}
	d.hasCache = false
}

// Snapshot returns current status. Concurrent refreshes are coalesced. Failed
// refreshes never return an older successful snapshot. RequireFresh bypasses
// the cache but may join a refresh already in progress.
func (d *Detector) Snapshot(ctx context.Context, mode RefreshMode) (Snapshot, error) {
	if mode == RequireFresh {
		d.clearCache()
	}
	if mode == UseCache {
		if snapshot, ok := d.loadCache(); ok {
			return snapshot, nil
		}
	}

	generation := d.cacheGeneration()
	result := d.group.DoChan("snapshot", func() (any, error) {
		snapshot, err := d.detect(context.WithoutCancel(ctx))
		if err != nil {
			return Snapshot{}, err
		}
		d.storeCache(snapshot, generation)
		return snapshot, nil
	})

	select {
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case call := <-result:
		if call.Err != nil {
			return Snapshot{}, call.Err
		}
		return call.Val.(Snapshot), nil
	}
}

func (d *Detector) loadCache() (Snapshot, bool) {
	now := d.now()
	d.mu.RLock()
	defer d.mu.RUnlock()
	if !d.hasCache || now.Sub(d.cachedAt) < 0 || now.Sub(d.cachedAt) > d.cacheTTL {
		return Snapshot{}, false
	}
	return cloneSnapshot(d.cached), true
}

func (d *Detector) cacheGeneration() uint64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.generation
}

func (d *Detector) storeCache(snapshot Snapshot, generation uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.generation != generation {
		return
	}
	d.cached = cloneSnapshot(snapshot)
	d.cachedAt = d.now()
	d.hasCache = true
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Routes = append([]Route(nil), snapshot.Routes...)
	return snapshot
}

func (d *Detector) detect(ctx context.Context) (Snapshot, error) {
	statusOutput, err := d.run(ctx, "status", "--json")
	if err != nil {
		return Snapshot{}, err
	}
	serveOutput, err := d.run(ctx, "serve", "status", "--json")
	if err != nil {
		return Snapshot{}, err
	}
	funnelOutput, err := d.run(ctx, "funnel", "status", "--json")
	if err != nil {
		return Snapshot{}, err
	}

	status, err := parseStatus(statusOutput)
	if err != nil {
		return Snapshot{}, err
	}
	serve, err := parseServeConfig("serve status", serveOutput)
	if err != nil {
		return Snapshot{}, err
	}
	funnel, err := parseServeConfig("funnel status", funnelOutput)
	if err != nil {
		return Snapshot{}, err
	}

	selfDNS, err := normalizeDNSName(status.Self.DNSName)
	if err != nil {
		return Snapshot{}, &SchemaError{Command: "status", Err: fmt.Errorf("Self.DNSName: %w", err)}
	}
	selfOrigin := "https://" + selfDNS
	snapshot := Snapshot{
		DetectedAt:  d.now().UTC(),
		SelfDNSName: selfDNS,
		SelfOrigin:  selfOrigin,
		Remediation: d.remediation(),
	}
	snapshot.Routes = classifyRoutes(selfDNS, d.listener, serve, funnel)
	return snapshot, nil
}

func (d *Detector) run(ctx context.Context, args ...string) ([]byte, error) {
	commandArgs := append([]string(nil), args...)
	if d.socketPath != "" {
		commandArgs = append([]string{"--socket=" + d.socketPath}, commandArgs...)
	}
	commandCtx, cancel := context.WithTimeout(ctx, d.commandTimeout)
	defer cancel()
	output, err := d.runner.Run(commandCtx, d.binary, commandArgs...)
	if err != nil {
		message := strings.TrimSpace(string(output))
		return nil, &CommandError{Args: commandArgs, Output: message, Err: err}
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return nil, &SchemaError{Command: strings.Join(args, " "), Err: errors.New("empty JSON output")}
	}
	return output, nil
}

func (d *Detector) remediation() string {
	host := d.listener.Host
	if isLoopbackAlias(host) {
		host = "127.0.0.1"
	}
	return "tailscale serve --bg http://" + net.JoinHostPort(host, strconv.Itoa(d.listener.Port))
}

func normalizeListener(listener Listener) (Listener, error) {
	listener.Host = strings.TrimSpace(strings.TrimSuffix(listener.Host, "."))
	if listener.Host == "" {
		return Listener{}, errors.New("desktop listener host is required")
	}
	if listener.Port < 1 || listener.Port > 65535 {
		return Listener{}, fmt.Errorf("desktop listener port %d is invalid", listener.Port)
	}
	if strings.EqualFold(listener.Host, "localhost") {
		listener.Host = "localhost"
		return listener, nil
	}
	ip := net.ParseIP(listener.Host)
	if ip == nil {
		return Listener{}, fmt.Errorf("desktop listener host %q is not an IP address or localhost", listener.Host)
	}
	listener.Host = ip.String()
	return listener, nil
}

// NormalizeHTTPSOrigin validates and canonicalizes an exact HTTPS .ts.net
// origin. Paths, credentials, query strings, fragments, and non-443 ports are
// rejected.
func NormalizeHTTPSOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid origin: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Opaque != "" {
		return "", errors.New("origin must use https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("origin must not contain credentials, a query, or a fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("origin must not contain a non-root path")
	}
	if parsed.RawPath != "" && parsed.RawPath != "/" {
		return "", errors.New("origin must not contain an encoded non-root path")
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return "", errors.New("origin must use HTTPS port 443")
	}
	host, err := normalizeDNSName(parsed.Hostname())
	if err != nil {
		return "", err
	}
	return "https://" + host, nil
}

func normalizeDNSName(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(raw, ".")))
	if len(host) == 0 || len(host) > 253 || host == "ts.net" || !strings.HasSuffix(host, ".ts.net") {
		return "", errors.New("host must be a DNS name below ts.net")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("host contains an invalid DNS label")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", errors.New("host contains an invalid DNS character")
			}
		}
	}
	return host, nil
}

// ClassifyRoutes classifies parsed Serve and Funnel configuration JSON for a
// known Self DNS name without executing Tailscale. It is useful to callers that
// already own a consistent status snapshot.
func ClassifyRoutes(selfDNS string, listener Listener, serveJSON, funnelJSON []byte) ([]Route, error) {
	listener, err := normalizeListener(listener)
	if err != nil {
		return nil, err
	}
	selfDNS, err = normalizeDNSName(selfDNS)
	if err != nil {
		return nil, err
	}
	serve, err := parseServeConfig("serve status", serveJSON)
	if err != nil {
		return nil, err
	}
	funnel, err := parseServeConfig("funnel status", funnelJSON)
	if err != nil {
		return nil, err
	}
	return classifyRoutes(selfDNS, listener, serve, funnel), nil
}

func classifyRoutes(selfDNS string, listener Listener, serve, funnel serveConfig) []Route {
	authorities := make([]string, 0, len(serve.Web)+1)
	for authority := range serve.Web {
		authorities = append(authorities, authority)
	}
	sort.Strings(authorities)

	expectedAuthority := net.JoinHostPort(selfDNS, "443")
	foundExpected := false
	routes := make([]Route, 0, len(authorities)+1)
	for _, authority := range authorities {
		if normalizedAuthority(authority) == expectedAuthority {
			foundExpected = true
		}
		routes = append(routes, classifyRoute(authority, selfDNS, listener, serve, funnel))
	}
	if !foundExpected {
		routes = append(routes, Route{
			Origin:         "https://" + selfDNS,
			Authority:      expectedAuthority,
			Classification: ClassificationUnconfigured,
			Reason:         "no Serve web route is configured for this node's HTTPS origin",
		})
	}
	return routes
}

func classifyRoute(authority, selfDNS string, listener Listener, serve, funnel serveConfig) Route {
	route := Route{Authority: authority}
	host, port, err := splitAuthority(authority)
	if err != nil {
		route.Classification = ClassificationInvalid
		route.Reason = err.Error()
		return route
	}
	route.Origin = "https://" + host
	if port != 443 || host != selfDNS {
		route.Classification = ClassificationInvalid
		route.Reason = "authority is not the exact HTTPS origin of the local Tailscale node"
		return route
	}

	serveFunnel := serve.AllowFunnel[authority] || serve.AllowFunnel[normalizedAuthority(authority)]
	funnelFunnel := funnel.AllowFunnel[authority] || funnel.AllowFunnel[normalizedAuthority(authority)]
	if serveFunnel != funnelFunnel {
		route.Classification = ClassificationFunnelEnabled
		route.Reason = "Funnel may be enabled because Serve and Funnel status disagree"
		return route
	}
	if serveFunnel {
		route.Classification = ClassificationFunnelEnabled
		route.Reason = "Funnel is enabled for this authority"
		return route
	}

	tcp := serve.TCP[strconv.Itoa(port)]
	if tcp == nil || !tcp.HTTPS || tcp.HTTP || tcp.TCPForward != "" || tcp.TerminateTLS != "" || tcp.ProxyProtocol != 0 {
		route.Classification = ClassificationIncompatible
		route.Reason = "Serve TCP state is not an exact HTTPS web listener"
		return route
	}
	web := serve.Web[authority]
	if web == nil {
		web = serve.Web[normalizedAuthority(authority)]
	}
	if web == nil || web.Handlers == nil || len(web.Handlers) != 1 {
		route.Classification = ClassificationUnsupportedHandler
		route.Reason = "Serve authority must contain exactly one root handler"
		return route
	}
	handler := web.Handlers["/"]
	if handler == nil || !handler.proxyOnly() {
		route.Classification = ClassificationUnsupportedHandler
		route.Reason = "Serve root handler must be an HTTP proxy and contain no other handler behavior"
		return route
	}
	route.ProxyTarget = handler.Proxy
	if !proxyTargetsListener(handler.Proxy, listener) {
		route.Classification = ClassificationWrongTarget
		route.Reason = "Serve proxy does not target the effective Swarm desktop loopback listener"
		return route
	}
	route.Classification = ClassificationVerifiedSwarmDesktop
	return route
}

func splitAuthority(raw string) (string, int, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return "", 0, errors.New("Serve authority must contain an explicit host and port")
	}
	host, err = normalizeDNSName(host)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errors.New("Serve authority contains an invalid port")
	}
	return host, port, nil
}

func normalizedAuthority(raw string) string {
	host, port, err := splitAuthority(raw)
	if err != nil {
		return raw
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func proxyTargetsListener(raw string, listener Listener) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "http" || parsed.Opaque != "" || parsed.User != nil {
		return false
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || (parsed.Path != "" && parsed.Path != "/") {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port != listener.Port {
		return false
	}
	targetHost := strings.TrimSuffix(strings.TrimSpace(parsed.Hostname()), ".")
	return isLoopbackAlias(listener.Host) && isLoopbackAlias(targetHost)
}

func isLoopbackAlias(host string) bool {
	if strings.EqualFold(strings.TrimSpace(strings.TrimSuffix(host, ".")), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func parseStatus(output []byte) (statusWire, error) {
	var status statusWire
	if err := unmarshalJSONObject(output, &status); err != nil {
		return statusWire{}, &SchemaError{Command: "status", Err: err}
	}
	if status.Self == nil {
		return statusWire{}, &SchemaError{Command: "status", Err: errors.New("missing Self object")}
	}
	return status, nil
}

func parseServeConfig(command string, output []byte) (serveConfig, error) {
	var config serveConfig
	if err := unmarshalJSONObject(output, &config); err != nil {
		return serveConfig{}, &SchemaError{Command: command, Err: err}
	}
	if config.TCP == nil {
		config.TCP = map[string]*tcpHandler{}
	}
	if config.Web == nil {
		config.Web = map[string]*webServer{}
	}
	if config.AllowFunnel == nil {
		config.AllowFunnel = map[string]bool{}
	}
	if containsConfiguredJSON(config.Services) || containsConfiguredJSON(config.Foreground) {
		return serveConfig{}, &SchemaError{Command: command, Err: errors.New("Services and Foreground Serve configurations are unsupported")}
	}
	for port, handler := range config.TCP {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 || handler == nil {
			return serveConfig{}, &SchemaError{Command: command, Err: fmt.Errorf("invalid TCP port handler %q", port)}
		}
	}
	for authority, web := range config.Web {
		if _, _, err := splitAuthority(authority); err != nil {
			return serveConfig{}, &SchemaError{Command: command, Err: fmt.Errorf("invalid Web authority %q: %w", authority, err)}
		}
		if web == nil || web.Handlers == nil {
			return serveConfig{}, &SchemaError{Command: command, Err: fmt.Errorf("invalid Web handler map for %q", authority)}
		}
	}
	for authority := range config.AllowFunnel {
		if _, _, err := splitAuthority(authority); err != nil {
			return serveConfig{}, &SchemaError{Command: command, Err: fmt.Errorf("invalid AllowFunnel authority %q: %w", authority, err)}
		}
	}
	return config, nil
}

func unmarshalJSONObject(output []byte, target any) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(output, &object); err != nil {
		return err
	}
	if object == nil {
		return errors.New("expected a JSON object")
	}
	if err := json.Unmarshal(output, target); err != nil {
		return err
	}
	return nil
}

type statusWire struct {
	BackendState string    `json:"BackendState"`
	Self         *selfWire `json:"Self"`
}

type selfWire struct {
	DNSName string `json:"DNSName"`
	Online  bool   `json:"Online"`
}

type serveConfig struct {
	TCP         map[string]*tcpHandler `json:"TCP"`
	Web         map[string]*webServer  `json:"Web"`
	AllowFunnel map[string]bool        `json:"AllowFunnel"`
	Services    json.RawMessage        `json:"Services"`
	Foreground  json.RawMessage        `json:"Foreground"`
}

func containsConfiguredJSON(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	return value != "" && value != "null" && value != "{}"
}

type tcpHandler struct {
	HTTPS         bool   `json:"HTTPS"`
	HTTP          bool   `json:"HTTP"`
	TCPForward    string `json:"TCPForward"`
	TerminateTLS  string `json:"TerminateTLS"`
	ProxyProtocol int    `json:"ProxyProtocol"`
}

type webServer struct {
	Handlers map[string]*httpHandler `json:"Handlers"`
}

type httpHandler struct {
	Proxy         string            `json:"Proxy"`
	Path          string            `json:"Path"`
	Text          string            `json:"Text"`
	Redirect      string            `json:"Redirect"`
	AcceptAppCaps []json.RawMessage `json:"AcceptAppCaps"`
	fieldCount    int
}

func (h *httpHandler) UnmarshalJSON(data []byte) error {
	type alias httpHandler
	var decoded alias
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("handler must be an object")
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*h = httpHandler(decoded)
	h.fieldCount = len(fields)
	return nil
}

func (h *httpHandler) proxyOnly() bool {
	return h != nil && h.fieldCount == 1 && strings.TrimSpace(h.Proxy) != ""
}
