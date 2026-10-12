// Package hostchange talks to the host-change helper: a root-owned socket
// service the headless installer places next to Swarm. The daemon runs as a
// locked-down user and cannot change the machine itself; after the owner
// approves a request it asks the helper, over one line on the socket, to make
// exactly one folder visible (or hidden again) and, when the helper says so,
// to restart the daemon under its new unit settings. The daemon never runs
// sudo, a shell or the helper program itself.
package hostchange

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	OpMountWorkspace   = "mount-workspace"
	OpUnmountWorkspace = "unmount-workspace"
	OpRestartDaemon    = "restart-daemon"
	OpList             = "list"

	// DefaultTimeout bounds one request to the helper, dial included.
	DefaultTimeout = 120 * time.Second
	// maxReplyBytes bounds the helper's one-line JSON reply.
	maxReplyBytes = 64 << 10
)

// ErrUnsupported reports a Swarm installed without the helper.
var ErrUnsupported = errors.New("This Swarm was installed without the host-change helper; run the headless installer's host-helper step.")

// ErrNotRunning reports a configured helper socket nobody answers on.
var ErrNotRunning = errors.New("host-change helper is not running on this machine")

// Result is the helper's reply to one request.
type Result struct {
	OK              bool   `json:"ok"`
	Op              string `json:"op,omitempty"`
	Path            string `json:"path,omitempty"`
	Owner           string `json:"owner,omitempty"`
	RestartRequired bool   `json:"restart_required,omitempty"`
	Error           string `json:"error,omitempty"`
	// Raw is the reply line as received (bounded), kept on the record.
	Raw string `json:"-"`
}

// Client sends requests to the helper socket.
type Client struct {
	socket  string
	timeout time.Duration
	dial    func(ctx context.Context, socket string) (net.Conn, error)
}

// NewClient returns a client for the configured socket path; an empty path
// means the helper is not installed and every request fails with
// ErrUnsupported.
func NewClient(socket string) *Client {
	return &Client{
		socket:  strings.TrimSpace(socket),
		timeout: DefaultTimeout,
		dial: func(ctx context.Context, socket string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
}

// ValidateSocketPath checks the --host-change-helper value: empty (helper
// not installed) or an absolute, clean path.
func ValidateSocketPath(socket string) error {
	socket = strings.TrimSpace(socket)
	if socket == "" {
		return nil
	}
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return fmt.Errorf("--host-change-helper %q must be an absolute, clean unix socket path", socket)
	}
	return nil
}

// Supported reports whether a helper socket is configured. It does not probe
// the socket: a helper that is not running fails the request it is asked for.
func (c *Client) Supported() bool { return c != nil && c.socket != "" }

// Run sends `op path` (path empty for restart-daemon and list) and returns
// the helper's reply. A reply with ok:false is returned as a Result with
// Error set and no Go error; transport failures (helper not running, no
// reply, malformed reply, timeout) are Go errors.
func (c *Client) Run(ctx context.Context, op, path string) (Result, error) {
	if !c.Supported() {
		return Result{}, ErrUnsupported
	}
	line, err := requestLine(op, path)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	conn, err := c.dial(ctx, c.socket)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := io.WriteString(conn, line); err != nil {
		return Result{}, fmt.Errorf("send to host-change helper: %w", err)
	}
	// The helper answers once; closing our write side tells it the request
	// is complete without waiting on the daemon.
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	reader := bufio.NewReader(io.LimitReader(conn, maxReplyBytes+1))
	reply, err := reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && reply != "") {
		var netErr net.Error
		if ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return Result{}, fmt.Errorf("host-change helper did not answer within %s", c.timeout)
		}
		return Result{}, fmt.Errorf("host-change helper closed without a reply: %w", err)
	}
	reply = strings.TrimSpace(reply)
	if len(reply) > maxReplyBytes {
		return Result{}, errors.New("host-change helper reply is too large")
	}
	var result Result
	if err := json.Unmarshal([]byte(reply), &result); err != nil {
		return Result{}, fmt.Errorf("host-change helper reply is not one JSON line: %w", err)
	}
	result.Raw = reply
	if !result.OK && strings.TrimSpace(result.Error) == "" {
		result.Error = "host-change helper refused the request"
	}
	return result, nil
}

// requestLine builds the one-line request `OP PATH\n`: the helper takes the
// first word as the operation and the rest of the line as the path, so a path
// may contain spaces but never a line break.
func requestLine(op, path string) (string, error) {
	switch op {
	case OpMountWorkspace, OpUnmountWorkspace:
		if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\n\r\x00") {
			return "", fmt.Errorf("host change %s needs an absolute path without line breaks", op)
		}
		return op + " " + path + "\n", nil
	case OpRestartDaemon, OpList:
		if path != "" {
			return "", fmt.Errorf("host change %s takes no path", op)
		}
		return op + "\n", nil
	}
	return "", fmt.Errorf("unsupported host change %q", op)
}
