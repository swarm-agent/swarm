package jobautomation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
)

// EnvironmentLeases is the folder-environment lease mechanism agents' bash
// uses (sandbox leases in the daemon: the folder's environment image, its
// isolation, lease user and network policy), as a run step with in:
// environment needs it.
type EnvironmentLeases interface {
	// TakeStepLease starts a lease of folder's environment held by owner, in
	// which the folder and its worktrees are mounted read-only. When every
	// lease of the folder is in use it returns an error wrapping
	// ErrEnvironmentBusy; any other error (no environment, one that is not
	// ready) is final.
	TakeStepLease(ctx context.Context, folder, owner string) (string, error)
	// ExecLease runs a command in owner's lease.
	ExecLease(ctx context.Context, lease, owner string, cmd LeaseCommand) (LeaseResult, error)
	// ReleaseLease removes owner's lease (its container and home).
	ReleaseLease(ctx context.Context, lease, owner string) error
}

// ErrEnvironmentBusy is TakeStepLease's answer while every lease of the
// folder's environment is in use: the step waits for one.
var ErrEnvironmentBusy = errors.New("every lease of the folder's environment is in use")

// LeaseCommand is one command in a lease.
type LeaseCommand struct {
	Argv []string
	// Dir is the working directory in the lease (the folder, mounted there
	// read-only at its own path).
	Dir      string
	Env      map[string]string
	Stdin    io.Reader
	Timeout  time.Duration
	OnOutput func([]byte)
}

// LeaseResult is how a lease command ended. TimedOut reports that its
// Timeout killed it.
type LeaseResult struct {
	ExitCode int
	TimedOut bool
}

// EnvironmentExecutor runs a run step with in: environment. It never runs
// anything on the host: it takes a lease of the folder's environment (the
// same lease agents' bash runs in, with the folder and its worktrees mounted
// read-only), makes a fresh writable clone of the folder's repository in
// the lease's own home, checks out exactly the job's head (SWARM_HEAD)
// there, and runs the step's script from that checkout with the step's
// SWARM_* variables and time limit. The folder's branches are the clone's
// origin/* (origin/dev among them), with their whole history. Nothing the
// script does reaches the folder's repository or the job's worktree; the
// lease, its home and the checkout are removed when the step ends, also on
// timeout and when the daemon stops.
type EnvironmentExecutor struct {
	Leases EnvironmentLeases
	// Poll is how often a step waiting for a free lease asks again (default
	// leaseWaitPoll).
	Poll time.Duration
}

// leaseWaitPoll is how often a step waiting for a free lease asks again,
// as agents' bash does.
const leaseWaitPoll = 2 * time.Second

// leaseReleaseTimeout bounds releasing a step's lease, also after the step's
// own context ended.
const leaseReleaseTimeout = 2 * time.Minute

// stepCheckoutDir is the checkout's directory in the lease, under the
// lease's own home (a volume removed with the lease).
const stepCheckoutDir = "$HOME/swarm-step/checkout"

// Exit codes of the prepare script (code-owned; it runs before the step's
// script, so they never mean the step's script).
const (
	prepareCloneFailed   = 10
	prepareHeadMissing   = 11
	prepareScriptMissing = 12
	prepareNotRegular    = 13
	prepareNotExecutable = 14
	prepareCheckoutFail  = 15
)

// prepareScript makes the step's checkout: a clone of the folder sharing its
// objects (read-only, through alternates), with the folder's branches as
// origin/*, checked out detached at the job's head. Arguments: the folder,
// the head, the script's path.
const prepareScript = `set -u
src=$1 head=$2 script=$3
dir="` + stepCheckoutDir + `"
rm -rf -- "$dir" || exit 10
mkdir -p -- "$(dirname -- "$dir")" || exit 10
git clone --quiet --no-checkout --shared -- "$src" "$dir" >&2 || exit 10
cd -- "$dir" || exit 10
git cat-file -e "$head^{commit}" 2>/dev/null || exit 11
entry=$(git ls-tree "$head" -- "$script") || exit 11
[ -n "$entry" ] || exit 12
mode=${entry%% *}
case "$mode" in
100755) ;;
100644) exit 14 ;;
*) exit 13 ;;
esac
git -c advice.detachedHead=false checkout --quiet --detach "$head" >&2 || exit 15
[ "$(git rev-parse HEAD)" = "$(git rev-parse "$head^{commit}")" ] || exit 15
`

// runStepScript runs the step's script from the checkout: arguments are the
// script's path and its args.
const runStepScript = `cd -- "` + stepCheckoutDir + `" || exit 125
exec "$@"`

var headPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

func (e EnvironmentExecutor) Run(ctx context.Context, req RunRequest) (RunOutcome, error) {
	if e.Leases == nil {
		return RunOutcome{}, &RefusedError{Reason: "run steps in the folder's environment are not available on this daemon"}
	}
	if err := ValidEnvironmentCommand(req.Command); err != nil {
		return RunOutcome{}, &RefusedError{Reason: err.Error()}
	}
	head := strings.TrimSpace(req.Env["SWARM_HEAD"])
	if !headPattern.MatchString(head) {
		return RunOutcome{}, &RefusedError{Reason: "the job has no head commit: a run step in the environment runs a script of the job's head"}
	}
	owner := strings.TrimSpace(req.Owner)
	if owner == "" {
		return RunOutcome{}, errors.New("an environment step needs its owner")
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultStepTimeoutSeconds * time.Second
	}
	if timeout > MaxStepTimeoutSeconds*time.Second {
		timeout = MaxStepTimeoutSeconds * time.Second
	}
	deadline := time.Now().Add(timeout)
	out := &stepOutput{tail: tailBuffer{max: MaxLogTailBytes}, onOutput: req.OnOutput}
	timedOut := func(what string) (RunOutcome, error) {
		fmt.Fprintf(out, "\nswarm: the step timed out after %s%s\n", timeout, what)
		return RunOutcome{ExitCode: timeoutExitCode, Output: out.tail.bytes()}, nil
	}
	// stopped reports the daemon stopping (the step is resumed, not
	// recorded), as opposed to the step's own time limit.
	stopped := func() bool { return errors.Is(ctx.Err(), context.Canceled) }

	lease, err := e.takeLease(ctx, req.Folder, owner, deadline, out)
	if err != nil {
		switch {
		case stopped():
			return RunOutcome{Output: out.tail.bytes()}, ctx.Err()
		case errors.Is(err, ErrEnvironmentBusy), errors.Is(err, context.DeadlineExceeded):
			return timedOut(" waiting for a free lease of the folder's environment")
		}
		return RunOutcome{Output: out.tail.bytes()}, &RefusedError{Reason: "the folder's environment cannot be leased: " + err.Error()}
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseReleaseTimeout)
		defer cancel()
		if err := e.Leases.ReleaseLease(releaseCtx, lease, owner); err != nil {
			log.Printf("swarmd job automations: release a run step's lease: %v", err)
		}
	}()

	env := stepVariables(req.Env)
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return timedOut(" before its checkout was made")
	}
	var prepared bytes.Buffer
	prep, err := e.Leases.ExecLease(ctx, lease, owner, LeaseCommand{
		Argv: []string{"bash", "-c", prepareScript, "swarm-step-prepare", req.Folder, head, req.Command},
		Dir:  req.Folder, Env: env, Timeout: remaining,
		OnOutput: func(p []byte) {
			if prepared.Len() < MaxLogTailBytes {
				prepared.Write(p)
			}
		},
	})
	switch {
	case stopped():
		return RunOutcome{Output: out.tail.bytes()}, ctx.Err()
	case prep.TimedOut || errors.Is(err, context.DeadlineExceeded) || (err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)):
		return timedOut(" while its checkout was made")
	case err != nil:
		return RunOutcome{Output: out.tail.bytes()}, fmt.Errorf("make the step's checkout in the environment: %w", err)
	}
	if prep.ExitCode != 0 {
		return RunOutcome{Output: out.tail.bytes()}, prepareFailure(prep.ExitCode, req.Command, head, prepared.String())
	}

	remaining = time.Until(deadline)
	if remaining <= 0 {
		return timedOut(" before its script started")
	}
	var stdin io.Reader
	if req.Stdin != nil {
		stdin = bytes.NewReader(req.Stdin)
	}
	argv := append([]string{"bash", "-c", runStepScript, "swarm-step", "./" + req.Command}, req.Args...)
	result, err := e.Leases.ExecLease(ctx, lease, owner, LeaseCommand{
		Argv: argv, Dir: req.Folder, Env: env, Stdin: stdin, Timeout: remaining,
		OnOutput: func(p []byte) { _, _ = out.Write(p) },
	})
	switch {
	case stopped():
		return RunOutcome{Output: out.tail.bytes()}, ctx.Err()
	case result.TimedOut || errors.Is(err, context.DeadlineExceeded) || (err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)):
		return timedOut("; its processes were killed")
	case err != nil:
		return RunOutcome{Output: out.tail.bytes()}, err
	}
	return RunOutcome{ExitCode: result.ExitCode, Output: out.tail.bytes()}, nil
}

// takeLease takes a lease of the folder's environment, waiting for a free
// one until deadline as agents' bash does.
func (e EnvironmentExecutor) takeLease(ctx context.Context, folder, owner string, deadline time.Time, out io.Writer) (string, error) {
	poll := e.Poll
	if poll <= 0 {
		poll = leaseWaitPoll
	}
	announced := false
	for {
		lease, err := e.Leases.TakeStepLease(ctx, folder, owner)
		if err == nil {
			return lease, nil
		}
		if !errors.Is(err, ErrEnvironmentBusy) {
			return "", err
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return "", err
		}
		if !announced {
			fmt.Fprintf(out, "swarm: waiting for a free lease of the folder's environment (%v)\n", err)
			announced = true
		}
		timer := time.NewTimer(min(poll, wait))
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

// stepVariables is the step's SWARM_* variables, the only ones it passes
// into the lease (as a host step's minimal environment does).
func stepVariables(in map[string]string) map[string]string {
	names := make([]string, 0, len(in))
	for name := range in {
		if strings.HasPrefix(name, "SWARM_") && !strings.ContainsAny(name, "=\x00") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make(map[string]string, len(names))
	for _, name := range names {
		out[name] = in[name]
	}
	return out
}

// prepareFailure is why the step's checkout could not be made: a refusal
// (nothing ran, no retry) for a script that is not in the head as an
// executable file, an error (retried) otherwise.
func prepareFailure(code int, command, head, output string) error {
	detail := lastLine([]byte(output))
	switch code {
	case prepareScriptMissing:
		return &RefusedError{Reason: fmt.Sprintf("%s does not exist in the job's head %s: commit the script to the branch, or fix the step's command", command, shortHead(head))}
	case prepareNotRegular:
		return &RefusedError{Reason: fmt.Sprintf("%s is not a regular file in the job's head %s (a directory, link or submodule): the command must name a script", command, shortHead(head))}
	case prepareNotExecutable:
		return &RefusedError{Reason: fmt.Sprintf("%s is not executable in the job's head %s: commit it with its executable bit (git update-index --chmod=+x %s)", command, shortHead(head), command)}
	case prepareHeadMissing:
		return fmt.Errorf("the job's head %s is not in the folder's repository", shortHead(head))
	}
	if detail != "" {
		return fmt.Errorf("could not make the step's checkout in the environment (exit %d): %s", code, detail)
	}
	return fmt.Errorf("could not make the step's checkout in the environment (exit %d)", code)
}

func shortHead(head string) string {
	if len(head) > 12 {
		return head[:12]
	}
	return head
}
