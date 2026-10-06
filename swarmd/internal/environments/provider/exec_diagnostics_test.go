package provider

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// Purpose: SSHDockerProvider.Exec/executeSupervised must retain capped streams
// for nonzero exits, deadline/cancel and disconnect, without interpreting lost
// SSH access as a confirmed remote exit. An injected command boundary is the
// narrowest hermetic layer exercising SSH dispatch and cleanup evidence.
type diagnosticSSHRunner struct {
	mode   string
	cancel context.CancelFunc
}

func (r *diagnosticSSHRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.RunCombined(ctx, name, args...)
}
func (r *diagnosticSSHRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	if strings.Contains(strings.Join(args, " "), "swarm-cleanup") {
		return []byte("SWARM_CLEANUP:TERMINATED\n"), nil
	}
	return nil, nil
}
func (r *diagnosticSSHRunner) RunWithIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	_, _ = io.WriteString(stdout, "assertion passed\n"+strings.Repeat("x", 128))
	_, _ = io.WriteString(stderr, "failure detail\n")
	switch r.mode {
	case "cancel":
		r.cancel()
		<-ctx.Done()
		return ctx.Err()
	case "deadline":
		<-ctx.Done()
		return ctx.Err()
	case "failure", "disconnect":
		code := "7"
		if r.mode == "disconnect" {
			code = "255"
		}
		// Real local exit status only; no remote daemon or credentials needed.
		return exec.CommandContext(ctx, "sh", "-c", "exit "+code).Run()
	}
	return nil
}

func TestSSHExecDiagnosticOutcomes(t *testing.T) {
	for _, mode := range []string{"success", "failure", "deadline", "cancel", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r := &diagnosticSSHRunner{mode: mode, cancel: cancel}
			p := NewSSHDockerProvider(r)
			conn := &environments.Connection{Kind: environments.ConnectionKindSSH, SSH: &environments.SSHConfig{Host: "example.invalid", User: "tester"}}
			dep := &environments.Deployment{Runtime: environments.RuntimeMetadata{ContainerID: "owned"}}
			timeout := 500 * time.Millisecond
			if mode == "deadline" {
				timeout = 20 * time.Millisecond
			}
			res, err := p.Exec(ctx, conn, dep, ExecRequest{OperationID: "op_diagnostic", Command: []string{"true"}, Timeout: timeout, MaxOutput: 32})
			if res == nil || !strings.Contains(res.Stdout, "assertion passed") || res.Stderr != "failure detail\n" || len(res.Stdout) > 32 || !res.Truncated {
				t.Fatalf("diagnostics lost: %+v %v", res, err)
			}
			switch mode {
			case "success":
				if err != nil || res.ExitCode != 0 {
					t.Fatalf("success: %+v %v", res, err)
				}
			case "failure":
				if err != nil || res.ExitCode != 7 {
					t.Fatalf("nonzero: %+v %v", res, err)
				}
			case "deadline":
				if !errors.Is(err, ErrOperationTimedOut) || res.ExitCode != 124 {
					t.Fatalf("deadline: %+v %v", res, err)
				}
			case "cancel":
				if !errors.Is(err, ErrOperationCancelled) || res.ExitCode != 130 {
					t.Fatalf("cancel: %+v %v", res, err)
				}
			case "disconnect":
				if !errors.Is(err, ErrOperationNotConfirmed) || res.ExitCode != -1 {
					t.Fatalf("disconnect falsely confirmed: %+v %v", res, err)
				}
			}
		})
	}
}

// Purpose: SafeExecOutput is the durable/tool credential boundary. Redaction
// precedes truncation, including split credentials and UTF-8 byte boundaries;
// this unit layer proves safety without storing any real credential.
func TestSafeExecOutputBoundsAndRedaction(t *testing.T) {
	for _, limit := range []int{1, 16, 4096, 0, DefaultMaxOutputBytes + 1} {
		out, truncated := SafeExecOutput("assertion\nAuthorization: Bearer fixture-value\npassword=fixture-value\n"+strings.Repeat("界", 32), limit, nil)
		if strings.Contains(out, "fixture-value") {
			t.Fatal("credential persisted")
		}
		if limit > 0 && limit <= DefaultMaxOutputBytes && len(out) > limit {
			t.Fatal("limit exceeded")
		}
		if limit == 1 && !truncated {
			t.Fatal("missing truncation flag")
		}
	}
	out, _ := SafeExecOutput("prefix known-fixture", 4096, map[string]string{"KEY": "known-fixture-value"})
	if strings.Contains(out, "known-fixture") {
		t.Fatal("partial environment value leaked")
	}
}

// Purpose: cleanupOperation must respect deadline while another cleanup holds
// the same process fence. Channel-lock contention is the narrowest layer proving
// cancellation cannot wait indefinitely behind a disconnected cleanup transport.
func TestExecCleanupLockDeadline(t *testing.T) {
	lock := getCleanupLock("owned:op_lock_deadline")
	lock <- struct{}{}
	defer func() { <-lock }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res, err := cleanupOperation(ctx, &sshDockerTransport{provider: NewSSHDockerProvider(&diagnosticSSHRunner{}), conn: &environments.Connection{}}, "owned", "op_lock_deadline", time.Second)
	if res != nil || !errors.Is(err, ErrOperationNotConfirmed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock ignored deadline: %+v %v", res, err)
	}
}

// Purpose: OSCommandRunner.RunWithIO must not wait indefinitely on inherited
// output pipes after the SSH-like client exits. A real local shell/pipe, one
// bounded descendant and short WaitDelay exercise the actual os/exec boundary;
// this is a command regression test, not an agent workload or benchmark.
func TestExecOutputPipeWaitDelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	runner := &OSCommandRunner{WaitDelay: 20 * time.Millisecond}
	var stdout, stderr strings.Builder
	started := time.Now()
	err := runner.RunWithIO(ctx, nil, &stdout, &stderr, "sh", "-c", "printf 'before pipe wait\\n'; sleep 2 & exit 0")
	if !errors.Is(err, exec.ErrWaitDelay) || !strings.Contains(stdout.String(), "before pipe wait") {
		t.Fatalf("pipe hang lost diagnostics: %q %v", stdout.String(), err)
	}
	if time.Since(started) >= time.Second {
		t.Fatal("inherited pipe exceeded bounded wait")
	}
}

// Purpose: cleanupOperation must not trust a success-looking marker received
// over an errored/disconnected transport. The injected SSH command boundary is
// sufficient to prove no fabricated termination receipt or leaked diagnostic.
func TestSSHExecCleanupDisconnectEvidence(t *testing.T) {
	runner := newMockSSHRunner()
	runner.handlers["exec"] = func(string, []string) ([]byte, error) {
		return []byte("SWARM_CLEANUP:TERMINATED\nAuthorization: Bearer fixture-value\n"), errors.New("SSH connection dropped")
	}
	p := NewSSHDockerProvider(runner)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := p.CancelExec(ctx, testSSHConnection(), &environments.Deployment{Runtime: environments.RuntimeMetadata{ContainerID: "owned"}}, CancelExecRequest{OperationID: "op_cleanup_disconnect"})
	if res == nil || res.Terminated || !errors.Is(err, ErrOperationNotConfirmed) {
		t.Fatalf("termination fabricated: %+v %v", res, err)
	}
	if strings.Contains(res.ErrorMessage, "fixture-value") || strings.Contains(err.Error(), "fixture-value") {
		t.Fatal("cleanup diagnostic leaked credentials")
	}
}
