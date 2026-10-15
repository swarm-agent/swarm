package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// Purpose: memoryFromMeminfo and NewManager own resource limits. These hermetic
// unit tests prevent small-host underprovisioning, malformed overrides, and
// accidental changes to the effective status/spec without requiring Docker.
func TestSandboxMemory(t *testing.T) {
	for _, tc := range []struct {
		info string
		want uint64
	}{
		{"MemTotal: 2097152 kB\n", 2 * gib},
		{"MemTotal: 33554432 kB\n", 32 * gib},
		{"MemTotal: 33556480 kB\n", 33556480 * 1024},
	} {
		got, err := memoryFromMeminfo(strings.NewReader(tc.info))
		if err != nil || got != tc.want {
			t.Fatalf("got %d, %v; want %d", got, err, tc.want)
		}
	}
	for _, info := range []string{"", "MemTotal: broken kB", "MemTotal: 0 kB"} {
		if _, err := memoryFromMeminfo(strings.NewReader(info)); err == nil {
			t.Fatal("accepted invalid memory info")
		}
	}
	for _, value := range []string{"", "1g", "1.5G", "1024m", "1073741824", "2gb"} {
		if err := ValidateMemory(value); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"0", "512m", "-1g", "1GiB", "bogus", "1g --privileged", "999999999999999999999g"} {
		if _, err := NewManager(context.Background(), Config{Mode: ModeOff, Memory: value, UID: 10001}, readyRunner()); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	m := newTestManager(t, ModeOff, readyRunner(), Config{Memory: "6g"})
	if m.Status().Memory != "6g" {
		t.Fatal(m.Status())
	}
	hash := m.specHash("/project", nil, "")
	m.cfg.Memory = "8g"
	if m.specHash("/project", nil, "") == hash {
		t.Fatal("memory missing from spec")
	}
}

type deathRunner struct {
	*fakeRunner
	after  containerState
	states int
}

func (r *deathRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 3 && args[0] == "container" && args[1] == "inspect" && args[3] == "{{json .State}}" {
		r.states++
		return json.Marshal(r.after)
	}
	return r.fakeRunner.Run(ctx, name, args...)
}
func (r *deathRunner) RunWithIO(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) error {
	return errors.New("exec transport lost")
}

// Purpose: Manager.Exec must distinguish container death from command failure by
// engine State, not error text. A scripted engine exercises the actual supervised
// boundary and verifies invalidation without replaying a potentially mutating command.
func TestExecSandboxDeath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after containerState
		died  bool
	}{
		{"oom", containerState{OOMKilled: true, StartedAt: "first"}, true},
		{"stopped", containerState{StartedAt: "first"}, true},
		{"restarted", containerState{Running: true, StartedAt: "second"}, true},
		{"command failure", containerState{Running: true, StartedAt: "first"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &deathRunner{fakeRunner: readyRunner(), after: tc.after}
			m, err := NewManager(context.Background(), Config{Mode: ModeRequired, UID: 10001, Memory: "6g"}, r)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			r.out["container inspect"] = m.specHash(root, []string{root}, "") + " true first"
			_, err = m.Exec(context.Background(), Scope{Root: root}, ExecRequest{Command: []string{"false"}, WorkingDir: root, Timeout: time.Second})
			var died *ErrSandboxDied
			if errors.As(err, &died) != tc.died {
				t.Fatalf("wrong classification: %v", err)
			}
			if tc.died {
				if died.OOMKilled != tc.after.OOMKilled || died.Memory != "6g" || !m.boxes[ContainerName(root)].invalid {
					t.Fatalf("lost death evidence: %+v", died)
				}
			}
			if r.states != 1 {
				t.Fatalf("unexpected command replay/state calls: %d", r.states)
			}
		})
	}
}
