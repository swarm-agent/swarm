package tool

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

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

func installLeaseManager(t *testing.T, max int) string {
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
	m.SetLeasePolicy(func(string) (int, error) { return max, nil }, nil)
	old := sandbox.Leases()
	sandbox.SetLeases(m)
	t.Cleanup(func() { sandbox.SetLeases(old) })
	return project
}

// Purpose: an agent leases its folder's environment with lease_environment;
// bash then picks its only lease by itself, must name one when it holds
// several, and runs as before when it holds none.
func TestBashRunsInTheAgentsLease(t *testing.T) {
	project := installLeaseManager(t, 3)
	scope := WorkspaceScope{PrimaryPath: project, SessionID: "session-1"}
	if id, err := bashLease(scope, map[string]any{}); err != nil || id != "" {
		t.Fatalf("no lease: %q %v", id, err)
	}
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
	if id, err := bashLease(scope, map[string]any{}); err != nil || id != taken.Lease.ID {
		t.Fatalf("only lease not used: %q %v", id, err)
	}
	if id, err := bashLease(WorkspaceScope{PrimaryPath: project, SessionID: "session-2"}, map[string]any{}); err != nil || id != "" {
		t.Fatalf("another agent got the lease: %q %v", id, err)
	}
	if _, err := executeLeaseEnvironment(context.Background(), scope, map[string]any{"action": "lease"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bashLease(scope, map[string]any{}); err == nil || !strings.Contains(err.Error(), "pass environment") {
		t.Fatalf("two leases without a choice: %v", err)
	}
	if id, err := bashLease(scope, map[string]any{"environment": taken.Lease.ID}); err != nil || id != taken.Lease.ID {
		t.Fatalf("named lease: %q %v", id, err)
	}
	listed, err := executeLeaseEnvironment(context.Background(), scope, map[string]any{"action": "list"})
	if err != nil || !strings.Contains(listed, `"max_leases":3`) || !strings.Contains(listed, `"in_use":2`) {
		t.Fatalf("list: %s %v", listed, err)
	}
	if _, err := executeLeaseEnvironment(context.Background(), scope, map[string]any{"action": "release", "lease_id": taken.Lease.ID}); err != nil {
		t.Fatal(err)
	}
	if got := sandbox.Leases().OwnerLeases("session-1"); len(got) != 1 {
		t.Fatalf("after release: %v", got)
	}
}

// Purpose: a review never runs in a lease, and naming a lease where leases
// are unavailable is refused rather than run on the host.
func TestLeaseRefusals(t *testing.T) {
	old := sandbox.Leases()
	sandbox.SetLeases(nil)
	defer sandbox.SetLeases(old)
	if _, err := bashLease(WorkspaceScope{SessionID: "s"}, map[string]any{"environment": "x"}); err == nil {
		t.Fatal("a named lease without leases was accepted")
	}
	if _, err := bashLease(WorkspaceScope{SessionID: "s", ReadOnlyBash: true}, map[string]any{"environment": "x"}); err == nil {
		t.Fatal("a review ran in a lease")
	}
}
