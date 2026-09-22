package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// Standard operation timeout bounds.
const (
	DefaultOperationTimeout = 5 * time.Minute
	MaxOperationTimeout     = 10 * time.Minute
	ProbeTimeout            = 10 * time.Second
	CleanupTimeout          = 15 * time.Second
	DefaultMaxOutputBytes   = 4 * 1024 * 1024 // 4 MB
	DefaultWaitDelay        = 3 * time.Second
	DefaultSupervisionDir   = "/run/swarm/operations"
)

// Provider operation error definitions.
var (
	ErrInvalidOperationID     = errors.New("invalid operation ID")
	ErrSupervisorUnavailable  = errors.New("supervisor primitives unavailable in target container")
	ErrOperationCleanupFailed = errors.New("operation cleanup failed")
	ErrOperationNotConfirmed  = errors.New("operation termination could not be confirmed")
	ErrOperationTimedOut      = errors.New("operation execution timed out")
	ErrOperationCancelled     = errors.New("operation execution cancelled")
)

var opIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,128}$`)

// Embedded supervisor scripts for container-side process isolation and cancellation.
const (
	// probeScript checks container supervisor prerequisites: base dir writable, /proc mounted, kill, and setsid available.
	probeScript = `BASE_DIR="${SWARM_OPERATIONS_DIR:-/run/swarm/operations}"
mkdir -p "$BASE_DIR" 2>/dev/null && test -w "$BASE_DIR" && test -d /proc && (command -v kill >/dev/null 2>&1 || which kill >/dev/null 2>&1) && (command -v setsid >/dev/null 2>&1 || which setsid >/dev/null 2>&1)`

	// supervisorScript starts the command in its own session/process group via setsid, verifies PGID, records pid/stat, and cleans up on exit.
	supervisorScript = `OP_ID="$1"
shift
if [ -z "$OP_ID" ]; then
    exit 1
fi

case "$OP_ID" in
    *[!a-zA-Z0-9_\-]*) exit 1 ;;
    '') exit 1 ;;
    '.'|'..') exit 1 ;;
esac

BASE_DIR="${SWARM_OPERATIONS_DIR:-/run/swarm/operations}"
OP_DIR="$BASE_DIR/$OP_ID"

mkdir -p -m 0700 "$OP_DIR" 2>/dev/null || exit 1
[ -d "$OP_DIR" ] && [ ! -L "$OP_DIR" ] || exit 1

# Cancel-before-exec fence
if [ -f "$OP_DIR/cancelled" ]; then
    echo "cancelled" > "$OP_DIR/status"
    echo "130" > "$OP_DIR/exitcode"
    exit 130
fi

# Launch command in a new session / process group via setsid
setsid "$@" <&0 &
PID=$!

if [ -z "$PID" ] || [ "$PID" -le 1 ] 2>/dev/null; then
    echo "failed_spawn" > "$OP_DIR/error"
    echo "failed" > "$OP_DIR/status"
    exit 1
fi

# Verify process grouping from /proc/$PID/stat (fail closed if unverified)
VERIFIED=0
PGID=""
STARTTIME=""
RAW_STAT=""

for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if [ -f "/proc/$PID/stat" ]; then
        RAW_STAT=""
        read -r RAW_STAT < "/proc/$PID/stat" 2>/dev/null || true
        if [ -n "$RAW_STAT" ]; then
            POST_COMM="${RAW_STAT##*) }"
            set -- $POST_COMM
            CUR_PGRP="$3"
            shift 19 2>/dev/null || true
            CUR_START="$1"
            if [ -n "$CUR_PGRP" ] && [ "$CUR_PGRP" -gt 1 ] 2>/dev/null && [ "$CUR_PGRP" = "$PID" ]; then
                PGID="$CUR_PGRP"
                STARTTIME="$CUR_START"
                VERIFIED=1
                break
            fi
        fi
    fi
    if [ ! -d "/proc/$PID" ]; then
        break
    fi
    sleep 0.05 2>/dev/null || sleep 1
done

if [ $VERIFIED -ne 1 ]; then
    if [ ! -d "/proc/$PID" ]; then
        echo "$PID" > "$OP_DIR/pid" 2>/dev/null || true
        echo "$PID" > "$OP_DIR/pgid" 2>/dev/null || true
        wait $PID
        EXIT_CODE=$?
        echo "$EXIT_CODE" > "$OP_DIR/exitcode" 2>/dev/null || true
        echo "exited" > "$OP_DIR/status" 2>/dev/null || true
        echo "1" > "$OP_DIR/tombstone" 2>/dev/null || true
        exit $EXIT_CODE
    fi
    kill -9 $PID 2>/dev/null || true
    echo "cannot guarantee process grouping" > "$OP_DIR/error"
    echo "failed" > "$OP_DIR/status"
    exit 1
fi

# Record metadata with exact ownership
echo "$PID" > "$OP_DIR/pid"
echo "$PGID" > "$OP_DIR/pgid"
echo "$STARTTIME" > "$OP_DIR/starttime"
echo "$RAW_STAT" > "$OP_DIR/stat"
echo "running" > "$OP_DIR/status"

cleanup_trap() {
    trap '' TERM INT
    touch "$OP_DIR/cancelled" 2>/dev/null || true
    if [ -n "$PGID" ] && [ "$PGID" -gt 1 ] 2>/dev/null; then
        kill -TERM -$PGID 2>/dev/null || true
    fi
    if [ -n "$PID" ] && [ "$PID" -gt 1 ] 2>/dev/null; then
        kill -TERM $PID 2>/dev/null || true
    fi
    w=0
    while [ $w -lt 20 ]; do
        if [ ! -d "/proc/$PID" ]; then
            break
        fi
        sleep 0.1 2>/dev/null || sleep 1
        w=$((w + 1))
    done
    if [ -d "/proc/$PID" ]; then
        if [ -n "$PGID" ] && [ "$PGID" -gt 1 ] 2>/dev/null; then
            kill -KILL -$PGID 2>/dev/null || true
        fi
        if [ -n "$PID" ] && [ "$PID" -gt 1 ] 2>/dev/null; then
            kill -KILL $PID 2>/dev/null || true
        fi
    fi
    exit 130
}
trap cleanup_trap TERM INT

wait $PID
EXIT_CODE=$?

echo "$EXIT_CODE" > "$OP_DIR/exitcode" 2>/dev/null || true
echo "exited" > "$OP_DIR/status" 2>/dev/null || true
echo "1" > "$OP_DIR/tombstone" 2>/dev/null || true

exit $EXIT_CODE`

	// cleanupScript signals only the specific operation process group and descendants, checking for PID reuse.
	cleanupScript = `OP_ID="$1"
GRACE_SEC="${2:-2}"
BASE_DIR="${SWARM_OPERATIONS_DIR:-/run/swarm/operations}"
OP_DIR="$BASE_DIR/$OP_ID"

case "$OP_ID" in
    *[!a-zA-Z0-9_\-]*)
        echo "SWARM_CLEANUP:INVALID_OP_ID"
        exit 1
        ;;
    '')
        echo "SWARM_CLEANUP:INVALID_OP_ID"
        exit 1
        ;;
    '.'|'..')
        echo "SWARM_CLEANUP:INVALID_OP_ID"
        exit 1
        ;;
esac

case "$GRACE_SEC" in
    ''|*[!0-9]*) GRACE_SEC=2 ;;
esac
if [ "$GRACE_SEC" -lt 1 ]; then GRACE_SEC=1; fi
if [ "$GRACE_SEC" -gt 15 ]; then GRACE_SEC=15; fi

# Cancel-before-exec fence
if [ ! -d "$OP_DIR" ]; then
    mkdir -p -m 0700 "$OP_DIR" 2>/dev/null || true
    echo "cancelled" > "$OP_DIR/cancelled" 2>/dev/null || true
    echo "SWARM_CLEANUP:CANCEL_BEFORE_EXEC"
    exit 0
fi

if [ -L "$OP_DIR" ]; then
    echo "SWARM_CLEANUP:INVALID_DIR"
    exit 1
fi

echo "cancelled" > "$OP_DIR/cancelled" 2>/dev/null || true

if [ ! -f "$OP_DIR/pid" ]; then
    for i in 1 2 3 4 5; do
        if [ -f "$OP_DIR/pid" ]; then
            break
        fi
        sleep 0.2 2>/dev/null || sleep 1
    done
fi

if [ ! -f "$OP_DIR/pid" ]; then
    echo "SWARM_CLEANUP:CANCEL_BEFORE_EXEC"
    exit 0
fi

PID=$(cat "$OP_DIR/pid" 2>/dev/null || true)
PGID=$(cat "$OP_DIR/pgid" 2>/dev/null || true)
REC_START=$(cat "$OP_DIR/starttime" 2>/dev/null || true)

case "$PID" in
    ''|*[!0-9]*)
        echo "SWARM_CLEANUP:INVALID_PID"
        exit 1
        ;;
esac
if [ "$PID" -le 1 ]; then
    echo "SWARM_CLEANUP:INVALID_PID"
    exit 1
fi

case "$PGID" in
    ''|*[!0-9]*)
        echo "SWARM_CLEANUP:INVALID_PGID"
        exit 1
        ;;
esac
if [ "$PGID" -le 1 ]; then
    echo "SWARM_CLEANUP:INVALID_PGID"
    exit 1
fi

PID_REUSED=0
if [ -d "/proc/$PID" ] && [ -n "$REC_START" ]; then
    CUR_STAT=""
    read -r CUR_STAT < "/proc/$PID/stat" 2>/dev/null || true
    if [ -n "$CUR_STAT" ]; then
        POST_COMM="${CUR_STAT##*) }"
        set -- $POST_COMM
        shift 19 2>/dev/null || true
        CUR_START="$1"
        if [ -n "$CUR_START" ] && [ "$CUR_START" != "$REC_START" ]; then
            PID_REUSED=1
        fi
    fi
fi

find_active_pids() {
    ACTIVE_PIDS=""
    scan_count=0
    for p in /proc/[0-9]*; do
        [ -d "$p" ] || continue
        scan_count=$((scan_count + 1))
        if [ $scan_count -gt 1024 ]; then
            break
        fi
        p_pid=${p#/proc/}
        case "$p_pid" in
            ''|*[!0-9]*) continue ;;
        esac
        [ "$p_pid" -gt 1 ] || continue
        [ "$p_pid" != "$$" ] || continue
        
        p_stat=""
        read -r p_stat < "$p/stat" 2>/dev/null || true
        [ -n "$p_stat" ] || continue
        
        post_comm="${p_stat##*) }"
        set -- $post_comm
        p_state="$1"
        [ "$p_state" != "Z" ] || continue
        p_ppid="$2"
        p_pgrp="$3"
        shift 19 2>/dev/null || true
        p_start="$1"
        
        matched=0
        if [ "$p_pid" = "$PID" ]; then
            if [ -z "$REC_START" ] || [ "$p_start" = "$REC_START" ]; then
                matched=1
            fi
        elif [ "$p_pgrp" = "$PGID" ]; then
            if [ -z "$REC_START" ] || [ -z "$p_start" ] || [ "$p_start" -ge "$REC_START" ] 2>/dev/null; then
                matched=1
            fi
        elif [ "$p_ppid" = "$PID" ]; then
            if [ -z "$REC_START" ] || [ -z "$p_start" ] || [ "$p_start" -ge "$REC_START" ] 2>/dev/null; then
                matched=1
            fi
        fi
        
        if [ $matched -eq 1 ]; then
            ACTIVE_PIDS="$ACTIVE_PIDS $p_pid"
        fi
    done
}

find_active_pids

if [ -z "$ACTIVE_PIDS" ]; then
    rm -rf "$OP_DIR" 2>/dev/null || true
    if [ $PID_REUSED -eq 1 ]; then
        echo "SWARM_CLEANUP:PID_REUSE_DETECTED"
    else
        echo "SWARM_CLEANUP:ALREADY_TERMINATED"
    fi
    exit 0
fi

if [ "$PGID" -gt 1 ] 2>/dev/null; then
    kill -TERM -$PGID 2>/dev/null || true
fi
for p in $ACTIVE_PIDS; do
    kill -TERM "$p" 2>/dev/null || true
done

waited=0
while [ $waited -lt "$GRACE_SEC" ]; do
    find_active_pids
    if [ -z "$ACTIVE_PIDS" ]; then
        break
    fi
    sleep 1
    waited=$((waited + 1))
done

find_active_pids
if [ -n "$ACTIVE_PIDS" ]; then
    if [ "$PGID" -gt 1 ] 2>/dev/null; then
        kill -KILL -$PGID 2>/dev/null || true
    fi
    for p in $ACTIVE_PIDS; do
        kill -KILL "$p" 2>/dev/null || true
    done
    sleep 1
fi

find_active_pids
if [ -n "$ACTIVE_PIDS" ]; then
    echo "SWARM_CLEANUP:CLEANUP_FAILED"
    exit 1
fi

rm -rf "$OP_DIR" 2>/dev/null || true
echo "SWARM_CLEANUP:TERMINATED"
exit 0`
)

// DeploymentProvider manages the lifecycle and runtime access of environments on a target connection.
type DeploymentProvider interface {
	// Kind returns the ConnectionKind that this provider handles (e.g. ConnectionKindLocalDocker, ConnectionKindSSH).
	Kind() environments.ConnectionKind

	// ValidateConnection checks that the connection configuration is valid and reachable.
	ValidateConnection(ctx context.Context, conn *environments.Connection) error

	// Capabilities returns the connection capabilities supported by this provider for the given connection.
	Capabilities(ctx context.Context, conn *environments.Connection) (environments.ConnectionCapabilities, error)

	// Deploy provisions and starts a deployment for the given environment on the connection host.
	Deploy(ctx context.Context, req DeployRequest) (*DeployResult, error)

	// Inspect retrieves current runtime status and health for a deployment.
	Inspect(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) (*InspectResult, error)

	// Start starts a stopped deployment container.
	Start(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) error

	// Stop stops a running deployment container.
	Stop(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) error

	// Destroy stops and cleans up the container and associated resources.
	Destroy(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) error

	// ResolveAccess returns consumer-relevant access metadata for an active deployment.
	ResolveAccess(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment) (*DeploymentAccess, error)

	// Exec executes a command inside the running deployment container.
	Exec(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment, req ExecRequest) (*ExecResult, error)
}

// OperationCanceler allows cancelling a running or orphaned execution operation by its OperationID,
// usable during active execution or after daemon restart.
type OperationCanceler interface {
	CancelExec(ctx context.Context, conn *environments.Connection, deployment *environments.Deployment, req CancelExecRequest) (*CancelExecResult, error)
}

// ContainerExecTransport abstracts execution inside a container for local or remote providers.
type ContainerExecTransport interface {
	RunExec(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, execArgs ...string) error
	RunExecCombined(ctx context.Context, execArgs ...string) ([]byte, error)
}

// CancelExecRequest specifies parameters for cancelling a running container operation.
type CancelExecRequest struct {
	OperationID string        `json:"operation_id"`
	GracePeriod time.Duration `json:"grace_period,omitempty"`
}

// CancelExecResult contains the outcome of an operation cancellation attempt.
type CancelExecResult struct {
	OperationID  string    `json:"operation_id"`
	Terminated   bool      `json:"terminated"`
	ObservedAt   time.Time `json:"observed_at"`
	SignalSent   string    `json:"signal_sent,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
}

// ExecProgress records an observed execution event with a real timestamp.
type ExecProgress struct {
	OperationID string    `json:"operation_id"`
	Timestamp   time.Time `json:"timestamp"`
	Stream      string    `json:"stream"` // "stdout", "stderr", "lifecycle"
	Data        []byte    `json:"data,omitempty"`
}

// ExecProgressCallback is invoked when meaningful execution progress or output is observed.
type ExecProgressCallback func(progress ExecProgress)

// DeployRequest contains parameters for instantiating an environment deployment.
type DeployRequest struct {
	Connection    *environments.Connection
	Environment   *environments.Environment
	Deployment    *environments.Deployment
	WorkspacePath string            // Host workspace path (used by local_mount if HostPath is empty)
	EnvOverrides  map[string]string // Optional environment variable overrides
}

// DeployResult contains the outcome of a Deploy operation.
type DeployResult struct {
	Runtime      environments.RuntimeMetadata  `json:"runtime"`
	Health       environments.HealthStatus     `json:"health"`
	Status       environments.DeploymentStatus `json:"status"`
	ErrorMessage string                        `json:"error_message,omitempty"`
}

// InspectResult contains runtime state and health retrieved from inspecting the container.
type InspectResult struct {
	Status       environments.DeploymentStatus `json:"status"`
	Health       environments.HealthStatus     `json:"health"`
	Runtime      environments.RuntimeMetadata  `json:"runtime"`
	ErrorMessage string                        `json:"error_message,omitempty"`
}

// DeploymentAccess exposes consumer-relevant access metadata for AI tasks, test runners, or workers.
type DeploymentAccess struct {
	PrimaryEndpoint     string                      `json:"primary_endpoint,omitempty"` // e.g. "http://127.0.0.1:18080"
	Endpoints           map[string]string           `json:"endpoints,omitempty"`        // port or name mapped endpoints
	ExecSupported       bool                        `json:"exec_supported"`
	RemoteWorkspacePath string                      `json:"remote_workspace_path,omitempty"` // working dir inside container (e.g. "/workspace")
	MappedPorts         []environments.AssignedPort `json:"mapped_ports,omitempty"`
	ContainerID         string                      `json:"container_id,omitempty"`
	RuntimeIP           string                      `json:"runtime_ip,omitempty"`
}

// ExecRequest specifies a command to execute inside a running container.
type ExecRequest struct {
	OperationID string               `json:"operation_id,omitempty"`
	Command     []string             `json:"command"`
	WorkingDir  string               `json:"working_dir,omitempty"`
	Env         map[string]string    `json:"env,omitempty"`
	Stdin       io.Reader            `json:"-"`
	Timeout     time.Duration        `json:"timeout,omitempty"`
	OnProgress  ExecProgressCallback `json:"-"`
	MaxOutput   int                  `json:"max_output,omitempty"`
}

// ExecResult contains the output and exit code of a command execution.
type ExecResult struct {
	ExitCode       int       `json:"exit_code"`
	Stdout         string    `json:"stdout"`
	Stderr         string    `json:"stderr"`
	OperationID    string    `json:"operation_id,omitempty"`
	Truncated      bool      `json:"truncated,omitempty"`
	LastObservedAt time.Time `json:"last_observed_at,omitempty"`
}

// Success returns true if the command exited with code 0.
func (r *ExecResult) Success() bool {
	return r != nil && r.ExitCode == 0
}

// CombinedOutput returns combined stdout and stderr.
func (r *ExecResult) CombinedOutput() string {
	if r == nil {
		return ""
	}
	if r.Stdout != "" && r.Stderr != "" {
		return r.Stdout + "\n" + r.Stderr
	}
	if r.Stdout != "" {
		return r.Stdout
	}
	return r.Stderr
}

// Registry maintains registered DeploymentProvider instances by ConnectionKind.
type Registry struct {
	mu        sync.RWMutex
	providers map[environments.ConnectionKind]DeploymentProvider
}

// NewRegistry creates a new empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[environments.ConnectionKind]DeploymentProvider),
	}
}

// Register registers a provider for its Kind().
func (r *Registry) Register(p DeploymentProvider) {
	if p == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Kind()] = p
}

// Get retrieves a provider by connection kind.
func (r *Registry) Get(kind environments.ConnectionKind) (DeploymentProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[kind]
	return p, ok
}

// Providers returns all registered providers.
func (r *Registry) Providers() []DeploymentProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]DeploymentProvider, 0, len(r.providers))
	for _, p := range r.providers {
		list = append(list, p)
	}
	return list
}

// CommandRunner abstracts command execution for testability and portability.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	RunCombined(ctx context.Context, name string, args ...string) ([]byte, error)
	RunWithIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
}

// OSCommandRunner executes commands via os/exec with bounded output and WaitDelay.
type OSCommandRunner struct {
	MaxOutputBytes int
	WaitDelay      time.Duration
}

func (r *OSCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	maxBytes := r.MaxOutputBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxOutputBytes
	}
	waitDelay := r.WaitDelay
	if waitDelay <= 0 {
		waitDelay = DefaultWaitDelay
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = waitDelay

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &boundedBuffer{buf: &stdout, max: maxBytes}
	cmd.Stderr = &boundedBuffer{buf: &stderr, max: maxBytes}
	err := cmd.Run()
	return stdout.Bytes(), err
}

func (r *OSCommandRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	maxBytes := r.MaxOutputBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxOutputBytes
	}
	waitDelay := r.WaitDelay
	if waitDelay <= 0 {
		waitDelay = DefaultWaitDelay
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = waitDelay

	var combined bytes.Buffer
	bb := &boundedBuffer{buf: &combined, max: maxBytes}
	cmd.Stdout = bb
	cmd.Stderr = bb
	err := cmd.Run()
	return combined.Bytes(), err
}

func (r *OSCommandRunner) RunWithIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	waitDelay := r.WaitDelay
	if waitDelay <= 0 {
		waitDelay = DefaultWaitDelay
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = waitDelay
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// ValidateOperationID ensures operation IDs are safe, bounded, and contain no path/shell metacharacters.
func ValidateOperationID(id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return fmt.Errorf("%w: cannot be empty", ErrInvalidOperationID)
	}
	if len(trimmed) > 128 {
		return fmt.Errorf("%w: length %d exceeds maximum 128", ErrInvalidOperationID, len(trimmed))
	}
	if trimmed == "." || trimmed == ".." {
		return fmt.Errorf("%w: cannot be %q", ErrInvalidOperationID, trimmed)
	}
	if !opIDRegex.MatchString(trimmed) {
		return fmt.Errorf("%w: %q contains invalid characters (allowed: alphanumeric, -, _)", ErrInvalidOperationID, trimmed)
	}
	return nil
}

func generateOperationID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("op-%d-%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

type boundedBuffer struct {
	buf       *bytes.Buffer
	max       int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.buf == nil {
		return len(p), nil
	}
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

type progressWriter struct {
	writer      io.Writer
	stream      string
	opID        string
	onProgress  ExecProgressCallback
	lastObsTime *time.Time
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	now := time.Now().UTC()
	if w.lastObsTime != nil {
		*w.lastObsTime = now
	}
	if w.onProgress != nil && len(p) > 0 {
		chunk := p
		if len(chunk) > 32*1024 {
			chunk = chunk[:32*1024]
		}
		dataCopy := make([]byte, len(chunk))
		copy(dataCopy, chunk)
		w.onProgress(ExecProgress{
			OperationID: w.opID,
			Timestamp:   now,
			Stream:      w.stream,
			Data:        dataCopy,
		})
	}
	return n, err
}

func redactArgs(args []string) []string {
	redacted := make([]string, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-e" && i+1 < len(args) {
			redacted[i] = "-e"
			val := args[i+1]
			if eq := strings.IndexByte(val, '='); eq != -1 {
				redacted[i+1] = val[:eq+1] + "[REDACTED]"
			} else {
				redacted[i+1] = "[REDACTED]"
			}
			i++
			continue
		}
		if strings.HasPrefix(args[i], "-e=") {
			val := args[i][3:]
			if eq := strings.IndexByte(val, '='); eq != -1 {
				redacted[i] = "-e=" + val[:eq+1] + "[REDACTED]"
			} else {
				redacted[i] = "-e=[REDACTED]"
			}
			continue
		}
		if args[i] == "sh" && i+2 < len(args) && args[i+1] == "-c" {
			redacted[i] = "sh"
			redacted[i+1] = "-c"
			redacted[i+2] = "[SUPERVISOR_SCRIPT]"
			for j := i + 3; j < len(args); j++ {
				if j == i+3 {
					redacted[j] = args[j] // "swarm-supervisor"
				} else if j == i+4 {
					redacted[j] = args[j] // opID
				} else {
					redacted[j] = "[REDACTED]"
				}
			}
			break
		}
		redacted[i] = args[i]
	}
	return redacted
}

func isContainerNotRunningError(out string, err error) bool {
	lower := strings.ToLower(out)
	if err != nil {
		lower += " " + strings.ToLower(err.Error())
	}
	return strings.Contains(lower, "no such container") ||
		strings.Contains(lower, "is not running") ||
		strings.Contains(lower, "cannot exec in a stopped state") ||
		strings.Contains(lower, "container is paused")
}

func sanitizeOutput(out string) string {
	trimmed := strings.TrimSpace(out)
	if len(trimmed) > 1024 {
		return trimmed[:1024] + "... [truncated]"
	}
	return trimmed
}

func sanitizeDockerName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

func containerName(envID, depID string) string {
	return fmt.Sprintf("swarm-%s-%s", sanitizeDockerName(envID), sanitizeDockerName(depID))
}

func resolveContainerTarget(deployment *environments.Deployment) string {
	if deployment == nil {
		return ""
	}
	if deployment.Runtime.ContainerID != "" {
		return deployment.Runtime.ContainerID
	}
	if deployment.Runtime.ProviderResourceID != "" {
		return deployment.Runtime.ProviderResourceID
	}
	if deployment.EnvironmentID != "" && deployment.ID != "" {
		return containerName(deployment.EnvironmentID, deployment.ID)
	}
	return deployment.ID
}

func validateExecParams(deployment *environments.Deployment, req ExecRequest) (string, error) {
	if deployment == nil {
		return "", errors.New("deployment cannot be nil")
	}
	if len(req.Command) == 0 {
		return "", errors.New("exec command cannot be empty")
	}
	target := resolveContainerTarget(deployment)
	if target == "" {
		return "", errors.New("cannot exec command without container target or ID")
	}
	return target, nil
}

func validateCancelParams(deployment *environments.Deployment, req CancelExecRequest) (string, error) {
	if deployment == nil {
		return "", errors.New("deployment cannot be nil")
	}
	if err := ValidateOperationID(req.OperationID); err != nil {
		return "", err
	}
	target := resolveContainerTarget(deployment)
	if target == "" {
		return "", errors.New("cannot cancel exec without container target or ID")
	}
	return target, nil
}

var (
	cleanupMu    sync.Mutex
	cleanupLocks = make(map[string]*sync.Mutex)
)

func getCleanupLock(key string) *sync.Mutex {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	l, ok := cleanupLocks[key]
	if !ok {
		l = &sync.Mutex{}
		cleanupLocks[key] = l
	}
	return l
}

func executeSupervised(ctx context.Context, transport ContainerExecTransport, target string, req ExecRequest) (*ExecResult, error) {
	opID := req.OperationID
	if opID == "" {
		opID = generateOperationID()
	} else {
		if err := ValidateOperationID(opID); err != nil {
			return nil, err
		}
	}

	// Probe container supervisor prerequisites (fail closed)
	probeCtx, probeCancel := context.WithTimeout(ctx, ProbeTimeout)
	probeErr := probeSupervisor(probeCtx, transport, target)
	probeCancel()
	if probeErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrSupervisorUnavailable, probeErr)
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultOperationTimeout
	} else if timeout > MaxOperationTimeout {
		timeout = MaxOperationTimeout
	}

	execCtx, execCancel := context.WithTimeout(ctx, timeout)
	defer execCancel()

	var execArgs []string
	if req.Stdin != nil {
		execArgs = append(execArgs, "-i")
	}
	if req.WorkingDir != "" {
		execArgs = append(execArgs, "-w", req.WorkingDir)
	}

	envKeys := make([]string, 0, len(req.Env))
	for k := range req.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		execArgs = append(execArgs, "-e", fmt.Sprintf("%s=%s", k, req.Env[k]))
	}

	execArgs = append(execArgs, target)
	execArgs = append(execArgs, "sh", "-c", supervisorScript, "swarm-supervisor", opID)
	execArgs = append(execArgs, req.Command...)

	maxOutput := req.MaxOutput
	if maxOutput <= 0 || maxOutput > DefaultMaxOutputBytes {
		maxOutput = DefaultMaxOutputBytes
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	boundedStdout := &boundedBuffer{buf: &stdoutBuf, max: maxOutput}
	boundedStderr := &boundedBuffer{buf: &stderrBuf, max: maxOutput}

	var lastObserved time.Time
	var outWriter io.Writer = boundedStdout
	var errWriter io.Writer = boundedStderr

	if req.OnProgress != nil {
		outWriter = &progressWriter{
			writer:      boundedStdout,
			stream:      "stdout",
			opID:        opID,
			onProgress:  req.OnProgress,
			lastObsTime: &lastObserved,
		}
		errWriter = &progressWriter{
			writer:      boundedStderr,
			stream:      "stderr",
			opID:        opID,
			onProgress:  req.OnProgress,
			lastObsTime: &lastObserved,
		}
	}

	err := transport.RunExec(execCtx, req.Stdin, outWriter, errWriter, execArgs...)
	if lastObserved.IsZero() {
		lastObserved = time.Now().UTC()
	}

	// On timeout or cancellation, execute bounded container cleanup
	if execCtx.Err() != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), CleanupTimeout)
		defer cleanupCancel()

		cancelRes, cleanupErr := cleanupOperation(cleanupCtx, transport, target, opID, 2*time.Second)
		if cleanupErr != nil {
			return nil, fmt.Errorf("%w for operation %s (%v): %v", ErrOperationCleanupFailed, opID, execCtx.Err(), cleanupErr)
		}
		if cancelRes != nil && !cancelRes.Terminated {
			return nil, fmt.Errorf("%w for operation %s: %s", ErrOperationNotConfirmed, opID, cancelRes.ErrorMessage)
		}
		if ctx.Err() == context.Canceled {
			return nil, fmt.Errorf("%w: operation %s cancelled (%v)", ErrOperationCancelled, opID, ctx.Err())
		}
		return nil, fmt.Errorf("%w: operation %s timed out (%v)", ErrOperationTimedOut, opID, execCtx.Err())
	}

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			redacted := redactArgs(execArgs)
			return nil, fmt.Errorf("docker exec failed: %w (command: %s)", err, strings.Join(redacted, " "))
		}
	}

	// Acknowledge operation / cleanup tombstone on normal completion
	ackCtx, ackCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = acknowledgeOperation(ackCtx, transport, target, opID)
	ackCancel()

	return &ExecResult{
		ExitCode:       exitCode,
		Stdout:         stdoutBuf.String(),
		Stderr:         stderrBuf.String(),
		OperationID:    opID,
		Truncated:      boundedStdout.truncated || boundedStderr.truncated,
		LastObservedAt: lastObserved,
	}, nil
}

func cancelSupervised(ctx context.Context, transport ContainerExecTransport, target string, req CancelExecRequest) (*CancelExecResult, error) {
	cancelCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		cancelCtx, cancel = context.WithTimeout(ctx, CleanupTimeout)
		defer cancel()
	}

	grace := req.GracePeriod
	if grace <= 0 {
		grace = 2 * time.Second
	} else if grace > 10*time.Second {
		grace = 10 * time.Second
	}

	return cleanupOperation(cancelCtx, transport, target, req.OperationID, grace)
}

func probeSupervisor(ctx context.Context, transport ContainerExecTransport, target string) error {
	args := []string{target, "sh", "-c", probeScript}
	out, err := transport.RunExecCombined(ctx, args...)
	if err != nil {
		return fmt.Errorf("probe failed: %w", err)
	}
	_ = out
	return nil
}

func cleanupOperation(ctx context.Context, transport ContainerExecTransport, target, opID string, grace time.Duration) (*CancelExecResult, error) {
	// Guard against double races on concurrent cancellation and timeout cleanup
	lockKey := target + ":" + opID
	lock := getCleanupLock(lockKey)
	lock.Lock()
	defer lock.Unlock()

	graceSec := int(grace.Seconds())
	if graceSec <= 0 {
		graceSec = 1
	}

	args := []string{target, "sh", "-c", cleanupScript, "swarm-cleanup", opID, strconv.Itoa(graceSec)}
	out, err := transport.RunExecCombined(ctx, args...)
	outStr := string(out)
	now := time.Now().UTC()

	if strings.Contains(outStr, "SWARM_CLEANUP:TERMINATED") {
		return &CancelExecResult{
			OperationID: opID,
			Terminated:  true,
			ObservedAt:  now,
			SignalSent:  "SIGTERM/SIGKILL",
		}, nil
	}
	if strings.Contains(outStr, "SWARM_CLEANUP:CANCEL_BEFORE_EXEC") {
		return &CancelExecResult{
			OperationID: opID,
			Terminated:  true,
			ObservedAt:  now,
			SignalSent:  "FENCE_BEFORE_EXEC",
		}, nil
	}
	if strings.Contains(outStr, "SWARM_CLEANUP:ALREADY_TERMINATED") || strings.Contains(outStr, "SWARM_CLEANUP:NOT_RUNNING") {
		return &CancelExecResult{
			OperationID: opID,
			Terminated:  true,
			ObservedAt:  now,
		}, nil
	}
	if strings.Contains(outStr, "SWARM_CLEANUP:PID_REUSE_DETECTED") {
		return &CancelExecResult{
			OperationID:  opID,
			Terminated:   true,
			ObservedAt:   now,
			ErrorMessage: "original process exited and PID was reused",
		}, nil
	}
	if strings.Contains(outStr, "SWARM_CLEANUP:CLEANUP_FAILED") {
		return &CancelExecResult{
			OperationID:  opID,
			Terminated:   false,
			ObservedAt:   now,
			ErrorMessage: "process still running after SIGKILL",
		}, fmt.Errorf("%w: process still running after SIGKILL", ErrOperationCleanupFailed)
	}
	if strings.Contains(outStr, "SWARM_CLEANUP:INVALID_") {
		return &CancelExecResult{
			OperationID:  opID,
			Terminated:   false,
			ObservedAt:   now,
			ErrorMessage: "invalid operation metadata or parameter",
		}, fmt.Errorf("%w: invalid metadata in target", ErrOperationCleanupFailed)
	}

	if err != nil && isContainerNotRunningError(outStr, err) {
		return &CancelExecResult{
			OperationID: opID,
			Terminated:  true,
			ObservedAt:  now,
		}, nil
	}

	if err != nil {
		return &CancelExecResult{
			OperationID:  opID,
			Terminated:   false,
			ObservedAt:   now,
			ErrorMessage: fmt.Sprintf("cleanup command failed: %v", err),
		}, fmt.Errorf("%w: %v", ErrOperationCleanupFailed, err)
	}

	return &CancelExecResult{
		OperationID:  opID,
		Terminated:   false,
		ObservedAt:   now,
		ErrorMessage: "unrecognized cleanup output",
	}, fmt.Errorf("%w: unrecognized cleanup output", ErrOperationCleanupFailed)
}

func acknowledgeOperation(ctx context.Context, transport ContainerExecTransport, target, opID string) error {
	ackScript := `BASE_DIR="${SWARM_OPERATIONS_DIR:-/run/swarm/operations}"
OP_DIR="$BASE_DIR/$1"
rm -rf "$OP_DIR" 2>/dev/null || true`
	_, err := transport.RunExecCombined(ctx, target, "sh", "-c", ackScript, "swarm-ack", opID)
	return err
}
