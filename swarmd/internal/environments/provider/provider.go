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
)

var opIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,128}$`)

// Embedded supervisor scripts for container-side process isolation and cancellation.
const (
	// probeScript checks container supervisor prerequisites: /run/swarm/operations, writable, /proc, kill.
	probeScript = `mkdir -p /run/swarm/operations && test -w /run/swarm/operations && test -d /proc && (type kill >/dev/null 2>&1 || which kill >/dev/null 2>&1)`

	// supervisorScript starts the command in its own process group, records pid/stat, and cleans up on exit.
	supervisorScript = `OP_ID="$1"
shift
OP_DIR="/run/swarm/operations/$OP_ID"
mkdir -p "$OP_DIR" || exit 1

set -m
"$@" <&0 &
PID=$!
PGID=$PID

echo "$PID" > "$OP_DIR/pid"
echo "$PGID" > "$OP_DIR/pgid"
echo "running" > "$OP_DIR/status"
if [ -f "/proc/$PID/stat" ]; then
    cat "/proc/$PID/stat" > "$OP_DIR/stat" 2>/dev/null || true
fi

cleanup_trap() {
    trap '' TERM INT
    kill -TERM -$PGID 2>/dev/null || kill -TERM $PID 2>/dev/null || true
    sleep 1
    kill -KILL -$PGID 2>/dev/null || kill -KILL $PID 2>/dev/null || true
    exit 130
}
trap cleanup_trap TERM INT

wait $PID
EXIT_CODE=$?
echo "$EXIT_CODE" > "$OP_DIR/exitcode" 2>/dev/null || true
echo "exited" > "$OP_DIR/status" 2>/dev/null || true
rm -rf "$OP_DIR" 2>/dev/null || true
exit $EXIT_CODE`

	// cleanupScript signals only the specific operation process group and descendants, checking for PID reuse.
	cleanupScript = `OP_ID="$1"
GRACE_SEC="${2:-2}"
OP_DIR="/run/swarm/operations/$OP_ID"

if [ ! -d "$OP_DIR" ]; then
    echo "SWARM_CLEANUP:NOT_RUNNING"
    exit 0
fi

PID=$(cat "$OP_DIR/pid" 2>/dev/null || true)
PGID=$(cat "$OP_DIR/pgid" 2>/dev/null || true)

if [ -z "$PID" ]; then
    rm -rf "$OP_DIR" 2>/dev/null || true
    echo "SWARM_CLEANUP:NOT_RUNNING"
    exit 0
fi

if [ ! -d "/proc/$PID" ]; then
    rm -rf "$OP_DIR" 2>/dev/null || true
    echo "SWARM_CLEANUP:ALREADY_TERMINATED"
    exit 0
fi

REC_START=""
if [ -f "$OP_DIR/stat" ] && [ -f "/proc/$PID/stat" ]; then
    REC_STAT=$(cat "$OP_DIR/stat" 2>/dev/null || true)
    CUR_STAT=$(cat "/proc/$PID/stat" 2>/dev/null || true)
    REC_START=$(echo "$REC_STAT" | sed 's/.*) //' | cut -d' ' -f20)
    CUR_START=$(echo "$CUR_STAT" | sed 's/.*) //' | cut -d' ' -f20)
    if [ -n "$REC_START" ] && [ -n "$CUR_START" ] && [ "$REC_START" != "$CUR_START" ]; then
        echo "SWARM_CLEANUP:PID_REUSE_DETECTED"
        rm -rf "$OP_DIR" 2>/dev/null || true
        exit 0
    fi
fi

if [ -n "$PGID" ]; then
    kill -TERM -$PGID 2>/dev/null || kill -TERM $PID 2>/dev/null || true
else
    kill -TERM $PID 2>/dev/null || true
fi

for p in /proc/[0-9]*; do
    [ -d "$p" ] || continue
    p_pid=${p#/proc/}
    if [ "$p_pid" != "$PID" ] && [ -f "$p/stat" ]; then
        p_stat=$(cat "$p/stat" 2>/dev/null | sed 's/.*) //')
        p_ppid=$(echo "$p_stat" | cut -d' ' -f2)
        p_pgrp=$(echo "$p_stat" | cut -d' ' -f3)
        if [ "$p_ppid" = "$PID" ] || [ -n "$PGID" -a "$p_pgrp" = "$PGID" ]; then
            kill -TERM "$p_pid" 2>/dev/null || true
        fi
    fi
done

waited=0
while [ $waited -lt "$GRACE_SEC" ]; do
    if [ ! -d "/proc/$PID" ]; then
        break
    fi
    sleep 1
    waited=$((waited + 1))
done

if [ -d "/proc/$PID" ]; then
    if [ -n "$PGID" ]; then
        kill -KILL -$PGID 2>/dev/null || kill -KILL $PID 2>/dev/null || true
    else
        kill -KILL $PID 2>/dev/null || true
    fi
fi

for p in /proc/[0-9]*; do
    [ -d "$p" ] || continue
    p_pid=${p#/proc/}
    if [ "$p_pid" != "$PID" ] && [ -f "$p/stat" ]; then
        p_stat=$(cat "$p/stat" 2>/dev/null | sed 's/.*) //')
        p_ppid=$(echo "$p_stat" | cut -d' ' -f2)
        p_pgrp=$(echo "$p_stat" | cut -d' ' -f3)
        if [ "$p_ppid" = "$PID" ] || [ -n "$PGID" -a "$p_pgrp" = "$PGID" ]; then
            kill -KILL "$p_pid" 2>/dev/null || true
        fi
    fi
done

sleep 1

STILL_RUNNING=0
if [ -d "/proc/$PID" ]; then
    CUR_STAT=$(cat "/proc/$PID/stat" 2>/dev/null || true)
    CUR_START=$(echo "$CUR_STAT" | sed 's/.*) //' | cut -d' ' -f20)
    if [ -z "$REC_START" ] || [ "$CUR_START" = "$REC_START" ]; then
        STILL_RUNNING=1
    fi
fi

if [ $STILL_RUNNING -eq 1 ]; then
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
