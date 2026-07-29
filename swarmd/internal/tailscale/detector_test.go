package tailscale

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const testStatusJSON = `{"BackendState":"Running","Self":{"DNSName":"swarm.tailnet.ts.net.","Online":true,"UserID":42},"User":{"42":{"ID":42,"LoginName":"owner@example.test"}}}`
const testServeJSON = `{"TCP":{"443":{"HTTPS":true}},"Web":{"swarm.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:5555"}}}}}`
const testFunnelOffJSON = `{}`

type scriptedRunner struct {
	mu      sync.Mutex
	outputs map[string][]byte
	errors  map[string]error
	calls   [][]string
	gate    <-chan struct{}
	entered chan<- struct{}
}

func (r *scriptedRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	gate := r.gate
	entered := r.entered
	r.mu.Unlock()
	if gate != nil {
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	key := commandKey(args)
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{name}, args...))
	output := append([]byte(nil), r.outputs[key]...)
	err := r.errors[key]
	r.mu.Unlock()
	return output, err
}

func (r *scriptedRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func commandKey(args []string) string {
	for len(args) > 0 && strings.HasPrefix(args[0], "--socket=") {
		args = args[1:]
	}
	key := ""
	for index, arg := range args {
		if index > 0 {
			key += " "
		}
		key += arg
	}
	return key
}

func newTestDetector(t *testing.T, runner Runner, now func() time.Time) *Detector {
	t.Helper()
	detector, err := NewDetector(Config{
		Listener: Listener{Host: "127.0.0.1", Port: 5555},
		Runner:   runner,
		Now:      now,
	})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	return detector
}

func successfulRunner() *scriptedRunner {
	return &scriptedRunner{outputs: map[string][]byte{
		"status --json":        []byte(testStatusJSON),
		"serve status --json":  []byte(testServeJSON),
		"funnel status --json": []byte(testFunnelOffJSON),
	}, errors: map[string]error{}}
}

func TestNormalizeHTTPSOrigin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "https://Node.Tailnet.TS.NET./", want: "https://node.tailnet.ts.net", ok: true},
		{input: "https://node.tailnet.ts.net:443", want: "https://node.tailnet.ts.net", ok: true},
		{input: "http://node.tailnet.ts.net", ok: false},
		{input: "https://node.tailnet.ts.net:8443", ok: false},
		{input: "https://user@node.tailnet.ts.net", ok: false},
		{input: "https://node.tailnet.ts.net/path", ok: false},
		{input: "https://node.tailnet.ts.net?query=yes", ok: false},
		{input: "https://node.example.com", ok: false},
		{input: "https://ts.net", ok: false},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := NormalizeHTTPSOrigin(test.input)
			if (err == nil) != test.ok {
				t.Fatalf("NormalizeHTTPSOrigin(%q) error = %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("NormalizeHTTPSOrigin(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestDetectorOwnerMetadataRequiresExactSelfUser(t *testing.T) {
	tests := []struct {
		name       string
		statusJSON string
		wantOwner  string
	}{
		{name: "exact self user", statusJSON: testStatusJSON, wantOwner: "owner@example.test"},
		{name: "missing self user id", statusJSON: `{"BackendState":"Running","Self":{"DNSName":"swarm.tailnet.ts.net.","Online":true},"User":{"42":{"ID":42,"LoginName":"owner@example.test"}}}`},
		{name: "missing user profile", statusJSON: `{"BackendState":"Running","Self":{"DNSName":"swarm.tailnet.ts.net.","Online":true,"UserID":42},"User":{}}`},
		{name: "mismatched profile id", statusJSON: `{"BackendState":"Running","Self":{"DNSName":"swarm.tailnet.ts.net.","Online":true,"UserID":42},"User":{"42":{"ID":7,"LoginName":"owner@example.test"}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := successfulRunner()
			runner.outputs["status --json"] = []byte(test.statusJSON)
			snapshot, err := newTestDetector(t, runner, time.Now).Snapshot(context.Background(), RequireFresh)
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if snapshot.OwnerLogin != test.wantOwner {
				t.Fatalf("owner login = %q, want %q", snapshot.OwnerLogin, test.wantOwner)
			}
		})
	}
}

func TestDetectorVerifiesExactDesktopRouteAndSocketInvocation(t *testing.T) {
	runner := successfulRunner()
	detector, err := NewDetector(Config{
		Listener:   Listener{Host: "::1", Port: 5555},
		Runner:     runner,
		SocketPath: "/run/tailscale/tailscaled.sock",
	})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}

	snapshot, err := detector.Snapshot(context.Background(), RequireFresh)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	route, ok := snapshot.RouteForOrigin("https://swarm.tailnet.ts.net")
	if !ok || route.Classification != ClassificationVerifiedSwarmDesktop {
		t.Fatalf("route = %#v, found = %v", route, ok)
	}
	if snapshot.Remediation != "tailscale serve --bg http://127.0.0.1:5555" {
		t.Fatalf("remediation = %q", snapshot.Remediation)
	}

	runner.mu.Lock()
	calls := append([][]string(nil), runner.calls...)
	runner.mu.Unlock()
	want := [][]string{
		{"tailscale", "--socket=/run/tailscale/tailscaled.sock", "status", "--json"},
		{"tailscale", "--socket=/run/tailscale/tailscaled.sock", "serve", "status", "--json"},
		{"tailscale", "--socket=/run/tailscale/tailscaled.sock", "funnel", "status", "--json"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDetectorClassifications(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		serve      string
		funnel     string
		want       Classification
		wantOrigin string
	}{
		{name: "wrong target", serve: `{"TCP":{"443":{"HTTPS":true}},"Web":{"swarm.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7781"}}}}}`, funnel: `{}`, want: ClassificationWrongTarget},
		{name: "funnel enabled", serve: `{"TCP":{"443":{"HTTPS":true}},"Web":{"swarm.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:5555"}}},"AllowFunnel":{"swarm.tailnet.ts.net:443":true}}`, funnel: `{"AllowFunnel":{"swarm.tailnet.ts.net:443":true}}`, want: ClassificationFunnelEnabled},
		{name: "funnel disagreement", serve: testServeJSON, funnel: `{"AllowFunnel":{"swarm.tailnet.ts.net:443":true}}`, want: ClassificationFunnelEnabled},
		{name: "unsupported handler", serve: `{"TCP":{"443":{"HTTPS":true}},"Web":{"swarm.tailnet.ts.net:443":{"Handlers":{"/":{"Text":"hello"}}}}}`, funnel: `{}`, want: ClassificationUnsupportedHandler},
		{name: "extra handler field", serve: `{"TCP":{"443":{"HTTPS":true}},"Web":{"swarm.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:5555","AcceptAppCaps":["example/cap"]}}}}}`, funnel: `{}`, want: ClassificationUnsupportedHandler},
		{name: "incompatible tcp", serve: `{"TCP":{"443":{"HTTP":true}},"Web":{"swarm.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:5555"}}}}}`, funnel: `{}`, want: ClassificationIncompatible},
		{name: "different authority", serve: `{"TCP":{"443":{"HTTPS":true}},"Web":{"other.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:5555"}}}}}`, funnel: `{}`, want: ClassificationInvalid, wantOrigin: "https://other.tailnet.ts.net"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := successfulRunner()
			runner.outputs["serve status --json"] = []byte(test.serve)
			runner.outputs["funnel status --json"] = []byte(test.funnel)
			detector := newTestDetector(t, runner, time.Now)
			snapshot, err := detector.Snapshot(context.Background(), RequireFresh)
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			origin := test.wantOrigin
			if origin == "" {
				origin = "https://swarm.tailnet.ts.net"
			}
			route, ok := snapshot.RouteForOrigin(origin)
			if !ok || route.Classification != test.want {
				t.Fatalf("route = %#v, found = %v, want %q", route, ok, test.want)
			}
		})
	}
}

func TestDetectorRejectsMalformedAndIncompatibleSchemas(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		command string
		output  string
	}{
		{name: "status array", command: "status --json", output: `[]`},
		{name: "status missing self", command: "status --json", output: `{}`},
		{name: "serve malformed", command: "serve status --json", output: `{`},
		{name: "serve invalid tcp", command: "serve status --json", output: `{"TCP":{"not-a-port":{"HTTPS":true}}}`},
		{name: "serve services unsupported", command: "serve status --json", output: `{"Services":{"svc:other":{}}}`},
		{name: "serve malformed web", command: "serve status --json", output: `{"Web":{"not-an-authority":{"Handlers":{}}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := successfulRunner()
			runner.outputs[test.command] = []byte(test.output)
			detector := newTestDetector(t, runner, time.Now)
			_, err := detector.Snapshot(context.Background(), RequireFresh)
			var schemaErr *SchemaError
			if !errors.As(err, &schemaErr) {
				t.Fatalf("error = %T %v, want SchemaError", err, err)
			}
		})
	}
}

func TestDetectorCacheFreshnessInvalidationAndNoStaleOnError(t *testing.T) {
	runner := successfulRunner()
	now := time.Unix(100, 0)
	detector := newTestDetector(t, runner, func() time.Time { return now })

	if _, err := detector.Snapshot(context.Background(), UseCache); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	if _, err := detector.Snapshot(context.Background(), UseCache); err != nil {
		t.Fatalf("cached Snapshot: %v", err)
	}
	if got := runner.callCount(); got != 3 {
		t.Fatalf("cached call count = %d, want 3", got)
	}

	now = now.Add(maximumCacheAge)
	if _, err := detector.Snapshot(context.Background(), UseCache); err != nil {
		t.Fatalf("boundary Snapshot: %v", err)
	}
	if got := runner.callCount(); got != 3 {
		t.Fatalf("boundary call count = %d, want 3", got)
	}

	if _, err := detector.Snapshot(context.Background(), RequireFresh); err != nil {
		t.Fatalf("fresh Snapshot: %v", err)
	}
	if got := runner.callCount(); got != 6 {
		t.Fatalf("fresh bypass call count = %d, want 6", got)
	}

	detector.Invalidate()
	runner.mu.Lock()
	runner.errors["status --json"] = errors.New("offline")
	runner.mu.Unlock()
	if _, err := detector.Snapshot(context.Background(), UseCache); err == nil {
		t.Fatal("Snapshot succeeded with stale data after refresh error")
	}
	if got := runner.callCount(); got != 7 {
		t.Fatalf("failed refresh call count = %d, want 7", got)
	}
}

func TestDetectorInvalidationDuringRefreshPreventsRecache(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	runner := successfulRunner()
	runner.mu.Lock()
	runner.gate = gate
	runner.entered = entered
	runner.mu.Unlock()
	detector := newTestDetector(t, runner, time.Now)

	result := make(chan error, 1)
	go func() {
		_, err := detector.Snapshot(context.Background(), UseCache)
		result <- err
	}()
	<-entered
	detector.Invalidate()
	close(gate)
	if err := <-result; err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	runner.mu.Lock()
	runner.gate = nil
	runner.entered = nil
	runner.mu.Unlock()
	if _, err := detector.Snapshot(context.Background(), UseCache); err != nil {
		t.Fatalf("Snapshot after invalidation: %v", err)
	}
	if got := runner.callCount(); got != 6 {
		t.Fatalf("call count = %d, want 6", got)
	}
}

func TestDetectorCoalescesConcurrentRefreshes(t *testing.T) {
	gate := make(chan struct{})
	runner := successfulRunner()
	runner.mu.Lock()
	runner.gate = gate
	runner.mu.Unlock()
	detector := newTestDetector(t, runner, time.Now)

	const callers = 8
	var ready sync.WaitGroup
	ready.Add(callers)
	start := make(chan struct{})
	errorsOut := make(chan error, callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			_, err := detector.Snapshot(context.Background(), RequireFresh)
			errorsOut <- err
		}()
	}
	ready.Wait()
	close(start)
	deadline := time.Now().Add(time.Second)
	for runner.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	close(gate)
	for range callers {
		if err := <-errorsOut; err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
	}
	if got := runner.callCount(); got != 3 {
		t.Fatalf("coalesced call count = %d, want 3", got)
	}
}

func TestDetectorReturnsCommandErrorAndStops(t *testing.T) {
	runner := successfulRunner()
	runner.outputs["serve status --json"] = []byte("permission denied")
	runner.errors["serve status --json"] = errors.New("exit status 1")
	detector := newTestDetector(t, runner, time.Now)

	_, err := detector.Snapshot(context.Background(), RequireFresh)
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("error = %T %v, want CommandError", err, err)
	}
	if commandErr.Output != "permission denied" {
		t.Fatalf("output = %q", commandErr.Output)
	}
	if got := runner.callCount(); got != 2 {
		t.Fatalf("call count = %d, want 2", got)
	}
}
