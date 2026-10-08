package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"swarm-refactor/swarmtui/pkg/environments"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// probeWorkerSSHDeployment is subordinate to the canonical control authority.
// It must not be exposed as a generic remote-command API. Success proves only
// authenticated access to an executable, not worker registration or readiness.
func (s *WorkerExecutionService) probeWorkerSSHDeployment(ctx context.Context, worker, deployment string, auth []ssh.AuthMethod, executable string) (string, error) {
	account, _, err := s.authorizeWorkerControl(ctx, false)
	if err != nil {
		return "", err
	}
	ws, err := s.workerStore()
	if err != nil {
		return "", err
	}
	d, err := ws.GetWorkerDeployment(account, worker, deployment)
	if err != nil {
		return "", err
	}
	if d.Target.Kind != "ssh" || d.ApprovalState != "approved" || d.DesiredState != "running" {
		return "", store.ErrWorkerConflict
	}
	target, err := s.ResolveWorkerTarget(ctx, d.Target)
	if err != nil {
		return "", err
	}
	if target != d.Target {
		return "", store.ErrWorkerConflict
	}
	conn, found, err := store.NewConnectionStore(s.host.runs.sessions.Store().Underlying()).Get(account, target.WorkspaceID, target.ReferenceID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", store.ErrWorkerNotFound
	}
	// Recheck the exact digest of the fetched connection: it may have changed
	// after ResolveWorkerTarget. No remotely observed state is written here.
	raw, err := json.Marshal(conn)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != target.ReferenceDigest {
		return "", store.ErrWorkerConflict
	}
	return workerSSHProbe(ctx, conn, auth, executable)
}

// workerSSHProbe is an internal transport preflight, not deployment readiness.
// The caller must resolve and authorize the canonical connection before calling
// it. Credentials stay in the process-owned SSH agent, never a deployment DTO.
// This helper deliberately cannot bootstrap, stop or dispatch a worker.
func workerSSHProbe(ctx context.Context, connection environments.Connection, auth []ssh.AuthMethod, executable string) (string, error) {
	if err := validateWorkerSSHProbe(connection, executable); err != nil {
		return "", err
	}
	if len(auth) == 0 {
		return "", errors.New("worker SSH authentication is unavailable")
	}
	config := connection.SSH
	checkHost, err := knownhosts.New(config.KnownHostsFile)
	if err != nil {
		return "", errors.New("worker SSH trusted host database is unavailable")
	}
	// One total deadline bounds dial, handshake and command execution. A parent
	// cancellation also closes a stalled handshake or channel immediately.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return "", errors.New("worker SSH connection failed")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return "", errors.New("worker SSH deadline setup failed")
	}
	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{
		User: config.User, Auth: auth, HostKeyCallback: checkHost,
	})
	if err != nil {
		// Do not return remote banners, credential-provider errors or private
		// known-hosts paths in user-visible diagnostics.
		return "", errors.New("worker SSH authentication or host verification failed")
	}
	client := ssh.NewClient(clientConn, channels, requests)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", errors.New("worker SSH session failed")
	}
	defer session.Close()
	output := &workerSSHProbeOutput{limit: 4096}
	session.Stdout, session.Stderr = output, output
	// Path validation excludes shell metacharacters. There is no caller-supplied
	// command, forwarding, PTY, environment, interactive prompt or sudo.
	err = session.Run(executable + " --version")
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if output.exceeded {
		return "", errors.New("worker SSH probe output exceeds limit")
	}
	if err != nil {
		return "", errors.New("worker SSH executable probe failed")
	}
	version := strings.TrimSpace(output.buf.String())
	if version == "" {
		return "", errors.New("worker SSH executable probe returned no version")
	}
	return version, nil
}

func validateWorkerSSHProbe(c environments.Connection, executable string) error {
	if c.Kind != environments.ConnectionKindSSH || c.SSH == nil || c.AccountScopeID == "" || c.WorkspaceID == "" || c.ID == "" {
		return errors.New("worker SSH requires a canonical scoped connection")
	}
	s := c.SSH
	if s.Host == "" || strings.HasPrefix(s.Host, "-") || strings.ContainsAny(s.Host, " \t\r\n\x00/@\\;|&`$<>\"'") || s.User == "" || s.User == "root" || strings.HasPrefix(s.User, "-") || strings.ContainsAny(s.User, " \t\r\n\x00/:@\\;|&`$<>\"'") || s.Port < 1 || s.Port > 65535 {
		return errors.New("worker SSH requires an explicit unprivileged host identity and port")
	}
	if s.KnownHostsFile == "" || !path.IsAbs(s.KnownHostsFile) {
		return errors.New("worker SSH requires an explicit trusted host database")
	}
	if !path.IsAbs(executable) || path.Clean(executable) != executable || strings.ContainsAny(executable, " \t\r\n\x00\\;|&`$<>\"'(){}[]*?!~") {
		return errors.New("worker SSH executable must be a clean absolute path without shell syntax")
	}
	return nil
}

// SSH copies stderr and stdout concurrently. Both share one strict bound and
// lock; untrusted output is never allowed to grow an unbounded buffer.
type workerSSHProbeOutput struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func (o *workerSSHProbeOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(p) > o.limit-o.buf.Len() {
		o.exceeded = true
		return 0, fmt.Errorf("SSH probe output limit")
	}
	return o.buf.Write(p)
}
