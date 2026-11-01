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
// objects (read-only, through alternates: nothing is copied, and only the
// objects the head needs are read), with the folder's branches as origin/*,
// checked out detached at the job's head. Arguments: the folder, the head,
// the script's path.
//
// When reading an object from the folder's store fails (git calls a loose
// object it cannot open "corrupt", whether its bytes are bad or the lease's
// user may not read it), the script says which object and why
// (swarm-step-object unreadable|corrupt|missing), then fetches from the
// folder's origin remote into the lease's home: the head commit alone
// (depth 1) when origin has it, else origin's branches. Those objects are
// packed into the checkout's own store, which git reads before the folder's
// loose objects, and the checkout is tried again (swarm-step-fallback says
// which fetch was used, or why none worked). The folder's repository is
// mounted read-only and never written. A checkout that read every object
// from the folder says swarm-step-checkout clean.
//
// git clone lists the folder's refs through upload-pack, which reads every
// branch tip: one tip object the lease may not read failed the whole clone
// (exit 10) before any fallback was tried. When the clone fails, the script
// makes the same shared clone by hand (the folder's objects as alternates,
// its branches as origin/*; swarm-step-clone by-hand, with the clone's own
// error as swarm-step-clone-error lines), so the checkout reads only what
// the head needs and the origin fallback covers an object it cannot read.
// The git command that failed is named on exit (swarm-step-git).
const prepareScript = `set -u
src=$1 head=$2 script=$3
dir="` + stepCheckoutDir + `"
base=$(dirname -- "$dir")
err="$base/prepare.err"
gitcmd=""
trap 'st=$?; if [ "$st" -ne 0 ] && [ -n "$gitcmd" ]; then printf "swarm-step-git %s\n" "$gitcmd" >&2; fi' EXIT
rm -rf -- "$dir" "$base/origin" || exit 10
mkdir -p -- "$base" || exit 10
gitcmd="git clone --no-checkout --shared $src"
if ! git clone --quiet --no-checkout --shared -- "$src" "$dir" 2>"$err"; then
	sed 's/^/swarm-step-clone-error /' "$err" >&2
	rm -rf -- "$dir"
	gitcmd="git init with $src's objects as alternates and its branches as origin/*"
	if common=$(git -C "$src" rev-parse --path-format=absolute --git-common-dir 2>"$err") &&
		git -c init.defaultBranch=swarm-step init --quiet -- "$dir" 2>>"$err" &&
		printf '%s\n' "$common/objects" >"$dir/.git/objects/info/alternates" &&
		git -C "$src" for-each-ref --format='%(objectname) refs/remotes/origin/%(refname:lstrip=2)' refs/heads/ >"$dir/.git/packed-refs" 2>>"$err" &&
		git -C "$dir" config remote.origin.url "$src" 2>>"$err" &&
		git -C "$dir" config remote.origin.fetch '+refs/heads/*:refs/remotes/origin/*' 2>>"$err"; then
		echo "swarm-step-clone by-hand" >&2
	else
		cat -- "$err" >&2
		exit 10
	fi
fi
cd -- "$dir" || exit 10
attempt() {
	gitcmd="git cat-file -e $head^{commit}"
	git cat-file -e "$head^{commit}" || return 11
	gitcmd="git ls-tree $head -- $script"
	entry=$(git ls-tree "$head" -- "$script") || return 11
	gitcmd=""
	[ -n "$entry" ] || return 12
	mode=${entry%% *}
	case "$mode" in
	100755) ;;
	100644) return 14 ;;
	*) return 13 ;;
	esac
	gitcmd="git checkout --force --detach $head"
	git -c advice.detachedHead=false checkout --quiet --force --detach "$head" || return 15
	gitcmd="git rev-parse HEAD"
	[ "$(git rev-parse HEAD)" = "$(git rev-parse "$head^{commit}")" ] || return 15
	gitcmd=""
}
attempt 2>"$err"
rc=$?
if [ "$rc" -eq 0 ]; then echo "swarm-step-checkout clean" >&2; exit 0; fi
if [ "$rc" -ne 11 ] && [ "$rc" -ne 15 ]; then cat -- "$err" >&2; exit "$rc"; fi
store=$(head -n 1 .git/objects/info/alternates 2>/dev/null)
found=0
for id in $(grep -oE '[0-9a-f]{40}([0-9a-f]{24})?' "$err" | sort -u); do
	rest=${id#??}
	p="$store/${id%"$rest"}/$rest"
	[ -n "$store" ] && [ -e "$p" ] || continue
	if [ ! -r "$p" ]; then
		file=$(ls -ln -- "$p" 2>/dev/null | awk '{print $1" "$3":"$4}')
		echo "swarm-step-object unreadable $id file $file lease-user $(id -u):$(id -G | tr ' ' ',')" >&2
		found=1
	elif grep -q "loose object $id.*is corrupt" "$err"; then
		echo "swarm-step-object corrupt $id" >&2
		found=1
	fi
done
if [ "$found" -eq 0 ]; then
	if ! grep -qiE 'is corrupt|unable to read|bad object|missing (blob|tree|commit|object)|inflate|invalid object|object file .* is empty' "$err"; then cat -- "$err" >&2; exit "$rc"; fi
	echo "swarm-step-object missing" >&2
fi
url=$(git -C "$src" config --get remote.origin.url 2>/dev/null)
if [ -z "$url" ]; then echo "swarm-step-fallback none: the folder's repository has no origin remote" >&2; cat -- "$err" >&2; exit "$rc"; fi
fetch="$base/origin"
if ! git init --quiet --bare -- "$fetch" 2>>"$err"; then echo "swarm-step-fallback failed: could not make a fetch repository in the lease" >&2; cat -- "$err" >&2; exit "$rc"; fi
fb=""
if git -C "$fetch" -c fetch.unpackLimit=1 -c transfer.unpackLimit=1 fetch --quiet --no-tags --depth=1 -- "$url" "$head" 2>"$err.head"; then
	fb=origin-head
elif git -C "$fetch" -c fetch.unpackLimit=1 -c transfer.unpackLimit=1 fetch --quiet --no-tags -- "$url" '+refs/heads/*:refs/heads/*' 2>"$err.branches"; then
	fb=origin-branches
else
	echo "swarm-step-fallback failed: could not fetch from the folder's origin remote" >&2
	cat -- "$err" "$err.head" "$err.branches" >&2 2>/dev/null
	exit "$rc"
fi
mkdir -p .git/objects/pack && cp -- "$fetch"/objects/pack/*.pack "$fetch"/objects/pack/*.idx .git/objects/pack/ 2>>"$err" || { echo "swarm-step-fallback failed: could not use the objects fetched from origin ($fb)" >&2; cat -- "$err" >&2; exit "$rc"; }
attempt 2>"$err.retry"
rc=$?
if [ "$rc" -eq 0 ]; then echo "swarm-step-fallback $fb" >&2; exit 0; fi
echo "swarm-step-fallback failed: the checkout still failed after fetching from origin ($fb)" >&2
cat -- "$err" "$err.retry" >&2
exit "$rc"
`

// runStepScript runs the step's script from the checkout: arguments are the
// script's path and its args.
const runStepScript = `cd -- "` + stepCheckoutDir + `" || exit 125
exec "$@"`

var headPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

func (e EnvironmentExecutor) Run(ctx context.Context, req RunRequest) (RunOutcome, error) {
	if e.Leases == nil {
		return RunOutcome{}, &RefusedError{Reason: "run steps in the folder's environment are not available on this daemon", Kind: ErrorKindEnvironment}
	}
	if err := ValidEnvironmentCommand(req.Command); err != nil {
		return RunOutcome{}, &RefusedError{Reason: err.Error()}
	}
	head := strings.TrimSpace(req.Env["SWARM_HEAD"])
	if !headPattern.MatchString(head) {
		return RunOutcome{}, &RefusedError{Reason: "the job has no head commit: a run step in the environment runs a script of the job's head", Kind: ErrorKindCheckout}
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
	// startTimedOut is the time limit ending before the script started
	// (waiting for a lease, making the checkout): the script never ran.
	startTimedOut := func(what string, repo *RepositoryCheck) (RunOutcome, error) {
		reason := fmt.Sprintf("the step timed out after %s%s; its script never started", timeout, what)
		fmt.Fprintf(out, "\nswarm: %s\n", reason)
		return RunOutcome{ExitCode: timeoutExitCode, Output: out.tail.bytes(), Repository: repo}, &StartError{Kind: ErrorKindStartTimeout, Reason: reason}
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
			return startTimedOut(" waiting for a free lease of the folder's environment", nil)
		}
		return RunOutcome{Output: out.tail.bytes()}, &RefusedError{Reason: "the folder's environment cannot be leased: " + err.Error(), Kind: ErrorKindLease}
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
		return startTimedOut(" before its checkout was made", nil)
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
	check := parseCheckout(prepared.String())
	switch {
	case stopped():
		return RunOutcome{Output: out.tail.bytes()}, ctx.Err()
	case prep.TimedOut || errors.Is(err, context.DeadlineExceeded) || (err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)):
		return startTimedOut(" while its checkout was made", check.repository())
	case err != nil:
		return RunOutcome{Output: out.tail.bytes()}, &StartError{Kind: ErrorKindLease, Reason: "could not run the step's checkout in the lease: " + err.Error()}
	}
	if note := check.cloneNote(); note != "" {
		fmt.Fprintf(out, "swarm: %s\n", note)
	}
	if prep.ExitCode != 0 {
		if note := check.failureNote(); note != "" {
			fmt.Fprintf(out, "swarm: %s\n", note)
		}
		failure := prepareFailure(prep.ExitCode, req.Command, head, prepared.String(), check)
		repo := check.repository()
		if repo == nil {
			repo = checkoutFailure(prep.ExitCode, prepared.String(), check)
		}
		return RunOutcome{Output: out.tail.bytes(), Repository: repo}, failure
	}
	if note := check.fallbackNote(); note != "" {
		// The step's output says which fallback made its checkout.
		fmt.Fprintf(out, "swarm: %s\n", note)
	}
	repo := check.repository()

	remaining = time.Until(deadline)
	if remaining <= 0 {
		return startTimedOut(" before its script started", repo)
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
		return RunOutcome{Output: out.tail.bytes(), Repository: repo}, ctx.Err()
	case result.TimedOut || errors.Is(err, context.DeadlineExceeded) || (err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)):
		outcome, timeoutErr := timedOut("; its processes were killed")
		outcome.Repository = repo
		return outcome, timeoutErr
	case err != nil:
		return RunOutcome{Output: out.tail.bytes(), Repository: repo}, &StartError{Kind: ErrorKindLease, Reason: "could not start the step's script in the lease: " + err.Error()}
	}
	return RunOutcome{ExitCode: result.ExitCode, Output: out.tail.bytes(), Repository: repo}, nil
}

// checkoutReport is what the prepare script said about reading the folder's
// objects: each object it could not read (and why), which fallback made the
// checkout, and whether every object came from the folder (clean).
type checkoutReport struct {
	objects  []checkoutObject
	missing  bool
	fallback string // origin-head, origin-branches
	failed   string // why no fallback made the checkout
	clean    bool
	// gitCommand is the git command whose failure ended the prepare
	// script (swarm-step-git).
	gitCommand string
	// byHand is a shared clone made by hand after git clone failed
	// (cloneError is that clone's own error).
	byHand     bool
	cloneError []string
}

type checkoutObject struct {
	id, problem, detail string
}

// Repository problems a step's checkout reports.
const (
	RepositoryObjectCorrupt    = "corrupt"
	RepositoryObjectUnreadable = "unreadable"
	// RepositoryCheckoutFailed is a checkout git refused without naming
	// an object (git clone or checkout failed for another reason).
	RepositoryCheckoutFailed = "checkout_failed"
)

func parseCheckout(output string) checkoutReport {
	var r checkoutReport
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "swarm-step-checkout clean":
			r.clean = true
		case line == "swarm-step-object missing":
			r.missing = true
		case line == "swarm-step-clone by-hand":
			r.byHand = true
		case strings.HasPrefix(line, "swarm-step-clone-error "):
			if text := strings.TrimSpace(strings.TrimPrefix(line, "swarm-step-clone-error ")); text != "" {
				r.cloneError = append(r.cloneError, text)
			}
		case strings.HasPrefix(line, "swarm-step-git "):
			r.gitCommand = strings.TrimSpace(strings.TrimPrefix(line, "swarm-step-git "))
		case strings.HasPrefix(line, "swarm-step-object "):
			fields := strings.Fields(strings.TrimPrefix(line, "swarm-step-object "))
			if len(fields) < 2 || !objectIDPattern.MatchString(fields[1]) {
				continue
			}
			obj := checkoutObject{problem: fields[0], id: fields[1]}
			switch obj.problem {
			case RepositoryObjectUnreadable:
				// "file <mode> <uid:gid> lease-user <uid:groups>"
				var mode, owner, user string
				for i := 2; i+1 < len(fields); i++ {
					switch fields[i] {
					case "file":
						mode = fields[i+1]
						if i+2 < len(fields) && fields[i+2] != "lease-user" {
							owner = fields[i+2]
						}
					case "lease-user":
						user = fields[i+1]
					}
				}
				obj.detail = "permission denied: the step's lease user"
				if user != "" {
					obj.detail += " (uid:groups " + user + ")"
				}
				obj.detail += " cannot read it"
				if mode != "" {
					obj.detail += " (file " + strings.TrimSpace(mode+" "+owner) + ")"
				}
			case RepositoryObjectCorrupt:
				obj.detail = "its bytes do not inflate to the object (corrupt)"
			default:
				continue
			}
			r.objects = append(r.objects, obj)
		case strings.HasPrefix(line, "swarm-step-fallback "):
			value := strings.TrimPrefix(line, "swarm-step-fallback ")
			switch {
			case value == "origin-head" || value == "origin-branches":
				r.fallback = value
			case strings.HasPrefix(value, "none: "):
				r.failed = strings.TrimPrefix(value, "none: ")
			case strings.HasPrefix(value, "failed: "):
				r.failed = strings.TrimPrefix(value, "failed: ")
			}
		}
	}
	return r
}

var objectIDPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// problem says which objects of the folder's store could not be read, and
// why ("" when every object read).
func (r checkoutReport) problem() string {
	if len(r.objects) == 0 {
		if r.missing {
			return "an object the head needs could not be read from the folder's repository"
		}
		return ""
	}
	parts := make([]string, 0, len(r.objects))
	for _, obj := range r.objects {
		parts = append(parts, "object "+obj.id+" of the folder's repository could not be read: "+obj.detail)
	}
	return strings.Join(parts, "; ")
}

func fallbackLabel(fallback string) string {
	if fallback == "origin-head" {
		return "origin-head: the head commit fetched from the folder's origin remote"
	}
	return "origin-branches: the folder's origin branches fetched, the rest read from the folder"
}

// fallbackNote is the step output line for a checkout an origin fetch made.
func (r checkoutReport) fallbackNote() string {
	if r.fallback == "" {
		return ""
	}
	note := "the checkout used fallback " + fallbackLabel(r.fallback)
	if p := r.problem(); p != "" {
		note = p + "; " + note
	}
	return note
}

// cloneNote is the step output line for a shared clone made by hand after
// git clone failed, with git clone's own error.
func (r checkoutReport) cloneNote() string {
	if !r.byHand {
		return ""
	}
	note := "git clone of the folder's repository failed"
	if text := gitErrorLines(r.cloneError); text != "" {
		note += " (" + text + ")"
	}
	return note + "; the checkout was made from a shared clone set up by hand"
}

// failureNote is the step output line for a checkout no fallback made.
func (r checkoutReport) failureNote() string {
	p := r.problem()
	if p == "" {
		return ""
	}
	if r.failed != "" {
		return p + "; no fallback made the checkout: " + r.failed
	}
	return p
}

// repository is the health record of the folder's repository this checkout
// gives: its first object that could not be read, or clean; nil when the
// checkout said neither.
func (r checkoutReport) repository() *RepositoryCheck {
	if len(r.objects) > 0 {
		obj := r.objects[0]
		return &RepositoryCheck{Object: obj.id, Problem: obj.problem, Detail: obj.detail, Fallback: r.fallback, Command: r.gitCommand}
	}
	if r.clean {
		return &RepositoryCheck{Clean: true}
	}
	return nil
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

// prepareFailure is why the step's checkout could not be made, as a start
// error (the script never ran): final for a script that is not in the head
// as an executable file, retried otherwise. An object the folder's store
// could not give is named with git's underlying cause (permission denied or
// corrupt bytes) and the fallback that was tried.
func prepareFailure(code int, command, head, output string, check checkoutReport) error {
	detail := prepareGitError(output)
	if detail != "" && check.gitCommand != "" {
		detail = check.gitCommand + " failed: " + detail
	} else if check.gitCommand != "" {
		detail = check.gitCommand + " failed"
	}
	if len(check.cloneError) > 0 && !check.byHand {
		// Neither git clone nor the shared clone by hand worked: keep
		// git clone's own error too.
		detail = strings.TrimSuffix("git clone failed: "+gitErrorLines(check.cloneError)+"; "+detail, "; ")
	}
	switch code {
	case prepareScriptMissing:
		return &RefusedError{Kind: ErrorKindScriptMissing, Reason: fmt.Sprintf("%s does not exist in the job's head %s: commit the script to the branch, or fix the step's command", command, shortHead(head))}
	case prepareNotRegular:
		return &RefusedError{Kind: ErrorKindScriptUnusable, Reason: fmt.Sprintf("%s is not a regular file in the job's head %s (a directory, link or submodule): the command must name a script", command, shortHead(head))}
	case prepareNotExecutable:
		return &RefusedError{Kind: ErrorKindScriptUnusable, Reason: fmt.Sprintf("%s is not executable in the job's head %s: commit it with its executable bit (git update-index --chmod=+x %s)", command, shortHead(head), command)}
	}
	if note := check.failureNote(); note != "" {
		reason := "could not make the step's checkout in the environment: " + note
		if detail != "" {
			reason += " (git: " + detail + ")"
		}
		return &StartError{Kind: ErrorKindCheckout, Reason: reason}
	}
	if code == prepareHeadMissing {
		return &StartError{Kind: ErrorKindCheckout, Reason: fmt.Sprintf("the job's head %s is not in the folder's repository", shortHead(head))}
	}
	if detail != "" {
		return &StartError{Kind: ErrorKindCheckout, Reason: fmt.Sprintf("could not make the step's checkout in the environment (exit %d): %s", code, detail)}
	}
	return &StartError{Kind: ErrorKindCheckout, Reason: fmt.Sprintf("could not make the step's checkout in the environment (exit %d)", code)}
}

// checkoutFailure is the repository health record of a checkout that failed
// without naming an object (a clone or checkout git refused): the failing
// git command and git's error. Script refusals (missing, not executable)
// and a head missing from the folder are the job's, not the repository's.
func checkoutFailure(code int, output string, check checkoutReport) *RepositoryCheck {
	if code != prepareCloneFailed && code != prepareCheckoutFail {
		return nil
	}
	return &RepositoryCheck{Problem: RepositoryCheckoutFailed, Command: check.gitCommand, Detail: prepareGitError(output), Fallback: check.fallback}
}

// prepareGitError is git's own error from the prepare script's output (its
// swarm-step-* report lines are read by parseCheckout): every line, joined
// with " | ", never cut to a fragment. A long one keeps its first and last
// maxGitErrorLines/2 lines with a marker saying how many were left out.
func prepareGitError(output string) string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "swarm-step-") {
			lines = append(lines, line)
		}
	}
	return gitErrorLines(lines)
}

// maxGitErrorLines bounds the git error lines a step reason keeps.
const maxGitErrorLines = 12

func gitErrorLines(lines []string) string {
	if len(lines) > maxGitErrorLines {
		half := maxGitErrorLines / 2
		kept := append([]string(nil), lines[:half]...)
		kept = append(kept, fmt.Sprintf("[... %d lines of git's error left out ...]", len(lines)-2*half))
		lines = append(kept, lines[len(lines)-half:]...)
	}
	return strings.Join(lines, " | ")
}

func shortHead(head string) string {
	if len(head) > 12 {
		return head[:12]
	}
	return head
}
