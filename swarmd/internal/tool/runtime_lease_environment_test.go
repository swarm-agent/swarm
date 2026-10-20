package tool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/sandbox"
)

// leaseEngine answers the container engine calls a lease makes, as a ready
// Docker would.
type leaseEngine struct{}

func (leaseEngine) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(joined, "image inspect"):
		return []byte("sha256:img\n"), nil
	case strings.HasPrefix(joined, "info"):
		return []byte(`{"runc":{}}`), nil
	case strings.HasPrefix(joined, "container inspect") && strings.Contains(joined, "{{json .State}}"):
		return []byte(`{"Running":true,"StartedAt":"first"}`), nil
	}
	return nil, nil
}

func (e leaseEngine) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return e.Run(ctx, name, args...)
}

func (leaseEngine) RunWithIO(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) error {
	return nil
}

func installLeaseManager(t *testing.T, limit sandbox.LeaseLimit) string {
	t.Helper()
	project := t.TempDir()
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{
		Mode: sandbox.ModeRequired, Backend: sandbox.BackendDocker, Memory: "1g",
		UID: 10001, GID: 10001, StateDir: filepath.Join(t.TempDir(), "state"),
	}, leaseEngine{})
	if err != nil {
		t.Fatal(err)
	}
	m.SetLayout(sandbox.Layout{Projects: func() ([]string, error) { return []string{project}, nil }})
	m.SetLeasePolicy(limit, nil)
	old := sandbox.Leases()
	sandbox.SetLeases(m)
	t.Cleanup(func() { sandbox.SetLeases(old) })
	return project
}

func maxLeases(n int) sandbox.LeaseLimit { return func(string) (int, error) { return n, nil } }

func takeLeaseFor(t *testing.T, scope WorkspaceScope) string {
	t.Helper()
	out, err := executeLeaseEnvironment(context.Background(), scope, map[string]any{"action": "lease"})
	if err != nil {
		t.Fatal(err)
	}
	var taken struct {
		Lease struct {
			ID string `json:"id"`
		} `json:"lease"`
	}
	if err := json.Unmarshal([]byte(out), &taken); err != nil || taken.Lease.ID == "" {
		t.Fatalf("lease output %s: %v", out, err)
	}
	return taken.Lease.ID
}

// Purpose: an agent may take leases itself with lease_environment; bash then
// uses its only lease, must name one when it holds several, and never runs in
// another agent's lease.
func TestBashRunsInTheAgentsLease(t *testing.T) {
	project := installLeaseManager(t, maxLeases(3))
	scope := WorkspaceScope{PrimaryPath: project, SessionID: "session-1"}
	first := takeLeaseFor(t, scope)
	if id, err := bashLease(context.Background(), scope, map[string]any{}, time.Second, nil); err != nil || id != first {
		t.Fatalf("only lease not used: %q %v", id, err)
	}
	other, err := bashLease(context.Background(), WorkspaceScope{PrimaryPath: project, SessionID: "session-2"}, map[string]any{}, time.Second, nil)
	if err != nil || other == "" || other == first {
		t.Fatalf("another agent: %q %v", other, err)
	}
	takeLeaseFor(t, scope)
	if _, err := bashLease(context.Background(), scope, map[string]any{}, time.Second, nil); err == nil || !strings.Contains(err.Error(), "pass environment") {
		t.Fatalf("two leases without a choice: %v", err)
	}
	if id, err := bashLease(context.Background(), scope, map[string]any{"environment": first}, time.Second, nil); err != nil || id != first {
		t.Fatalf("named lease: %q %v", id, err)
	}
	listed, err := executeLeaseEnvironment(context.Background(), scope, map[string]any{"action": "list"})
	if err != nil || !strings.Contains(listed, `"max_leases":3`) || !strings.Contains(listed, `"in_use":3`) {
		t.Fatalf("list: %s %v", listed, err)
	}
	if _, err := executeLeaseEnvironment(context.Background(), scope, map[string]any{"action": "release", "lease_id": first}); err != nil {
		t.Fatal(err)
	}
	if got := sandbox.Leases().OwnerLeases("session-1"); len(got) != 1 {
		t.Fatalf("after release: %v", got)
	}
}

// Purpose: in a folder whose environment is set up for leases, an agent's
// first bash command takes a lease by itself and later commands stay in it;
// when every lease is in use the next agent waits for one to free up, and
// gets a clear error when none does in time.
func TestBashTakesALeaseByItself(t *testing.T) {
	old := leaseWaitPoll
	leaseWaitPoll = 10 * time.Millisecond
	defer func() { leaseWaitPoll = old }()
	project := installLeaseManager(t, maxLeases(1))
	a := WorkspaceScope{PrimaryPath: project, SessionID: "agent-a"}
	b := WorkspaceScope{PrimaryPath: project, SessionID: "agent-b"}
	first, err := bashLease(context.Background(), a, map[string]any{}, time.Second, nil)
	if err != nil || first == "" {
		t.Fatalf("first command took no lease: %q %v", first, err)
	}
	if again, err := bashLease(context.Background(), a, map[string]any{}, time.Second, nil); err != nil || again != first {
		t.Fatalf("second command left the lease: %q %v", again, err)
	}
	var waited []string
	if _, err := bashLease(context.Background(), b, map[string]any{}, 50*time.Millisecond, func(s string) { waited = append(waited, s) }); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("full folder: %v", err)
	}
	if len(waited) != 1 || !strings.Contains(waited[0], "1 of 1 in use") {
		t.Fatalf("wait not announced once: %q", waited)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = sandbox.Leases().ReleaseOwner(context.Background(), "agent-a")
	}()
	got, err := bashLease(context.Background(), b, map[string]any{}, 5*time.Second, nil)
	if err != nil || got == "" || got == first {
		t.Fatalf("waiting agent did not get the freed lease: %q %v", got, err)
	}
}

// Purpose: a folder without an environment set up for leases runs bash as
// before, and one set up for leases whose environment is not ready is refused
// rather than run on the host without its environment and secrets.
func TestAutomaticLeaseOnlyWhereSetUp(t *testing.T) {
	project := installLeaseManager(t, maxLeases(0))
	scope := WorkspaceScope{PrimaryPath: project, SessionID: "agent"}
	if id, err := bashLease(context.Background(), scope, map[string]any{}, time.Second, nil); err != nil || id != "" {
		t.Fatalf("folder without leases: %q %v", id, err)
	}
	sandbox.Leases().SetLeasePolicy(func(string) (int, error) { return 0, errors.New("the environment is building") }, nil)
	if _, err := bashLease(context.Background(), scope, map[string]any{}, time.Second, nil); err == nil || !strings.Contains(err.Error(), "building") {
		t.Fatalf("not-ready environment: %v", err)
	}
}

// Purpose: a review never runs in a lease, and naming a lease where leases
// are unavailable is refused rather than run on the host.
func TestLeaseRefusals(t *testing.T) {
	old := sandbox.Leases()
	sandbox.SetLeases(nil)
	defer sandbox.SetLeases(old)
	if _, err := bashLease(context.Background(), WorkspaceScope{SessionID: "s"}, map[string]any{"environment": "x"}, time.Second, nil); err == nil {
		t.Fatal("a named lease without leases was accepted")
	}
	if _, err := bashLease(context.Background(), WorkspaceScope{SessionID: "s", ReadOnlyBash: true}, map[string]any{"environment": "x"}, time.Second, nil); err == nil {
		t.Fatal("a review ran in a lease")
	}
}
