// Package sandbox runs agent commands, and the daemon's own Git on
// agent-writable repositories, inside one hardened container per project.
//
// Swarm stays outside with its storage, credentials and local socket. A
// project's sandbox mounts only that project's directory and its worktree
// bucket, at the same paths as on the host, so commands, Git output and file
// paths need no mapping. Nothing from the daemon's environment is passed in.
package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
)

// Mode selects whether agent execution must, may or must not use sandboxes.
type Mode string

const (
	// ModeAuto uses sandboxes when the engine, image and network are ready at
	// startup and otherwise runs on the host with permission bypass disabled.
	ModeAuto Mode = "auto"
	// ModeRequired always uses sandboxes and fails closed when they are not
	// ready. Server installs run in this mode.
	ModeRequired Mode = "required"
	// ModeOff never uses sandboxes; permission bypass is disabled.
	ModeOff Mode = "off"
)

const (
	DefaultImage       = "swarm-sandbox:local"
	DefaultNetwork     = "swarm-sandbox"
	DefaultPidsLimit   = 1024
	DefaultIdleTimeout = 30 * time.Minute
	gvisorRuntime      = "runsc"
	sandboxHome        = "/home/sandbox"
	sandboxPath        = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	containerPrefix    = "swarm-sandbox-"
	labelSandbox       = "swarm.sandbox"
	labelRoot          = "swarm.sandbox.root"
	labelSpec          = "swarm.sandbox.spec"
	probeTimeout       = 15 * time.Second
	ensureTimeout      = 2 * time.Minute
)

// ParseMode validates a mode name.
func ParseMode(value string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(value))) {
	case "", ModeAuto:
		return ModeAuto, nil
	case ModeRequired:
		return ModeRequired, nil
	case ModeOff:
		return ModeOff, nil
	}
	return "", fmt.Errorf("unsupported sandbox mode %q (expected auto, required or off)", value)
}

// Config describes how sandboxes are created.
type Config struct {
	Mode    Mode
	Engine  string // container engine CLI, default "docker"
	Image   string
	Network string
	// Runtime is the OCI runtime; empty selects gVisor (runsc) when the engine
	// has it registered and the engine default otherwise.
	Runtime     string
	Memory      string
	PidsLimit   int
	CPUs        string
	DNS         []string
	IdleTimeout time.Duration
	// UID and GID the sandbox runs as. They default to the daemon's own ids so
	// files written in mounted projects keep the daemon user as owner; the
	// sandbox still cannot reach anything the daemon owns outside its mounts.
	UID, GID int
	// ProtectedRoots are never mounted, nor anything inside them, nor any
	// directory containing them (daemon storage, credential directories).
	ProtectedRoots []string
	// ProtectedAncestors may contain mounts but are never mounted themselves,
	// nor any directory containing them (the daemon user's home).
	ProtectedAncestors []string
	// StateDir holds the durable list of projects that have used a sandbox.
	StateDir string
	// SecretBroker, when set, supplies per-project secret wiring (egress proxy
	// env, stand-ins, CA mount) for projects with an active secret grant.
	SecretBroker SecretBroker
}

// SecretSetup is the per-sandbox secret wiring returned by a SecretBroker.
type SecretSetup struct {
	Env         []string    // extra "K=V" lines: proxy, stand-ins, CA paths
	Files       []FileMount // read-only files to mount (CA, CA bundle)
	Fingerprint string      // changes when grants change, so the sandbox is rebuilt
}

// FileMount is one read-only host file mounted into the sandbox.
type FileMount struct {
	HostPath string
	Dest     string
}

// SecretBroker prepares a project's secret wiring. PrepareSandbox is called on
// every acquire (cheap); it returns empty when the project has no grant.
type SecretBroker interface {
	PrepareSandbox(root string) (SecretSetup, error)
	ReleaseSandbox(root string)
}

// Status reports whether sandboxes are in force for this process.
type Status struct {
	Mode    Mode   `json:"mode"`
	Active  bool   `json:"active"`
	Runtime string `json:"runtime,omitempty"`
	Memory  string `json:"memory,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Scope is one project's sandbox: Root identifies it and Mounts are the host
// directories bind-mounted at the same paths.
type Scope struct {
	Root   string
	Mounts []string
	// ReviewCheckout selects a separate container with read-only project mounts
	// and only this disposable checkout writable.
	ReviewCheckout string
}

// Manager owns the per-project sandbox containers.
type Manager struct {
	cfg    Config
	runner provider.CommandRunner

	mu      sync.Mutex
	status  Status
	imageID string
	runtime string
	// envResolver names folders' environment images (environment.go).
	envResolver   EnvironmentResolver
	reviewClosing map[string]bool   // terminal tombstones; never reacquire
	reviews       map[string]string // checkout -> project root; daemon-owned admission only
	boxes         map[string]*box
	used          map[string]struct{}
	layout        *layoutCache
	stop          chan struct{}
	// resolvConf is the sandbox's own /etc/resolv.conf (public resolvers, no
	// search domain). Docker's embedded resolver (127.0.0.11) does not
	// answer under gVisor, and the host's search domain names the tailnet.
	resolvConf string
	// owner reports a path's owning uid (tests inject it); safeDirs caches the
	// foreign-owned project directories Git needs safe.directory for.
	owner      func(path string) (int, bool)
	safeMu     sync.Mutex
	safeDirs   []string
	safeDirsAt time.Time
}

type box struct {
	// All state is guarded by Manager.mu. changing reserves lifecycle work;
	// commands hold only an inflight lease, never a mutex.
	changing  chan struct{}
	name      string
	root      string
	spec      string
	mounts    []string
	lastUsed  time.Time
	verified  time.Time
	startedAt string
	invalid   bool // explicit execution failure, not merely periodic staleness
	inflight  int
	secret    SecretSetup
}

// verifyInterval bounds how long a container is trusted to still be running
// (it can be stopped or removed outside Swarm) before it is inspected again.
const verifyInterval = 30 * time.Second

var ErrInactive = errors.New("sandbox is not active")

// NewManager validates cfg and probes the engine. In auto mode the probe
// result decides, once, whether this process uses sandboxes.
func NewManager(ctx context.Context, cfg Config, runner provider.CommandRunner) (*Manager, error) {
	if cfg.Engine == "" {
		cfg.Engine = "docker"
	}
	if cfg.Image == "" {
		cfg.Image = DefaultImage
	}
	if cfg.Network == "" {
		cfg.Network = DefaultNetwork
	}
	if err := ValidateMemory(cfg.Memory); err != nil {
		return nil, err
	}
	if cfg.Memory == "" {
		var err error
		cfg.Memory, err = hostMemory()
		if err != nil {
			cfg.Memory = "4g"
			slog.Warn("cannot size sandbox from host memory; using minimum limit", "error", err)
		}
	}
	if cfg.PidsLimit <= 0 {
		cfg.PidsLimit = DefaultPidsLimit
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if len(cfg.DNS) == 0 {
		// Public resolvers: host, tailnet and link-local resolvers are not
		// reachable from the sandbox network.
		cfg.DNS = []string{"1.1.1.1", "9.9.9.9"}
	}
	if cfg.UID == 0 && cfg.GID == 0 {
		cfg.UID, cfg.GID = os.Getuid(), os.Getgid()
	}
	if cfg.UID == 0 {
		return nil, errors.New("sandbox refuses to run agent commands as root; run Swarm as a service user")
	}
	if runner == nil {
		runner = &provider.OSCommandRunner{}
	}
	m := &Manager{cfg: cfg, runner: runner, boxes: map[string]*box{}, used: map[string]struct{}{}, stop: make(chan struct{})}
	if err := m.loadUsed(); err != nil {
		return nil, err
	}
	if err := m.writeResolvConf(); err != nil {
		return nil, err
	}
	m.status = Status{Mode: cfg.Mode, Memory: cfg.Memory}
	if cfg.Mode == ModeOff {
		m.status.Reason = "sandbox mode is off"
		return m, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	err := m.probe(probeCtx)
	cancel()
	if err != nil {
		m.status.Reason = err.Error()
		if cfg.Mode == ModeAuto {
			// Auto resolves once: this process runs on the host with
			// permissions enforced.
			m.cfg.Mode = ModeOff
			m.status.Mode = ModeOff
		}
		return m, nil
	}
	m.status.Active = true
	m.status.Runtime = m.runtime
	if cfg.Mode == ModeAuto {
		m.cfg.Mode = ModeRequired
		m.status.Mode = ModeRequired
	}
	return m, nil
}

// SetSecretBroker installs the per-project secret wiring broker. The daemon
// calls it once at startup when the secret gateway is enabled.
func (m *Manager) SetSecretBroker(broker SecretBroker) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.cfg.SecretBroker = broker
	m.mu.Unlock()
}

func (m *Manager) secretBroker() SecretBroker {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.SecretBroker
}

// Status returns the current sandbox status.
func (m *Manager) Status() Status {
	if m == nil {
		return Status{Mode: ModeOff, Reason: "sandbox is not configured"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Active reports whether agent execution runs in sandboxes right now.
func (m *Manager) Active() bool { return m.Status().Active }

// Enforced reports whether execution must go through sandboxes (failing
// closed when they are unavailable) rather than the host.
func (m *Manager) Enforced() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Mode == ModeRequired
}

func (m *Manager) probe(ctx context.Context) error {
	if _, err := m.runner.Run(ctx, m.cfg.Engine, "version", "--format", "{{.Server.Version}}"); err != nil {
		return fmt.Errorf("container engine is not reachable: %w", err)
	}
	out, err := m.runner.Run(ctx, m.cfg.Engine, "image", "inspect", "--format", "{{.Id}}", m.cfg.Image)
	if err != nil {
		return fmt.Errorf("sandbox image %s is not available: %w", m.cfg.Image, err)
	}
	imageID := strings.TrimSpace(string(out))
	if _, err := m.runner.Run(ctx, m.cfg.Engine, "network", "inspect", "--format", "{{.Name}}", m.cfg.Network); err != nil {
		return fmt.Errorf("sandbox network %s is not available: %w", m.cfg.Network, err)
	}
	runtime := m.cfg.Runtime
	if runtime == "" {
		out, err := m.runner.Run(ctx, m.cfg.Engine, "info", "--format", "{{json .Runtimes}}")
		if err == nil {
			var runtimes map[string]json.RawMessage
			if json.Unmarshal(out, &runtimes) == nil {
				if _, ok := runtimes[gvisorRuntime]; ok {
					runtime = gvisorRuntime
				}
			}
		}
	}
	m.mu.Lock()
	m.imageID = imageID
	m.runtime = runtime
	m.mu.Unlock()
	return nil
}

// Start runs the idle reaper until ctx ends or Close is called.
func (m *Manager) Start(ctx context.Context) {
	if m == nil || !m.Active() {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stop:
				return
			case <-ticker.C:
				m.stopIdle(ctx, time.Now())
			}
		}
	}()
}

// Close stops the idle reaper. Containers are left for the next start.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
}

func (m *Manager) stopIdle(ctx context.Context, now time.Time) {
	m.mu.Lock()
	idle := make([]*box, 0)
	for _, b := range m.boxes {
		if b.inflight == 0 && now.Sub(b.lastUsed) > m.cfg.IdleTimeout {
			idle = append(idle, b)
		}
	}
	m.mu.Unlock()
	for _, b := range idle {
		m.mu.Lock()
		// Re-check after the snapshot, atomically with acquire's lease.
		if b.inflight != 0 || b.changing != nil || now.Sub(b.lastUsed) <= m.cfg.IdleTimeout {
			m.mu.Unlock()
			continue
		}
		b.changing = make(chan struct{})
		m.mu.Unlock()
		stopCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, _ = m.runner.Run(stopCtx, m.cfg.Engine, "stop", "--time", "5", b.name)
		cancel()
		if broker := m.secretBroker(); broker != nil && b.root != "" {
			broker.ReleaseSandbox(b.root)
		}
		m.mu.Lock()
		b.spec = "" // next use re-verifies and restarts it
		close(b.changing)
		b.changing = nil
		m.mu.Unlock()
	}
}

func (m *Manager) writeResolvConf() error {
	if m.cfg.StateDir == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString("# Written by Swarm for agent sandboxes.\n")
	for _, dns := range m.cfg.DNS {
		b.WriteString("nameserver " + dns + "\n")
	}
	b.WriteString("options edns0\n")
	if err := os.MkdirAll(m.cfg.StateDir, 0o700); err != nil {
		return fmt.Errorf("sandbox state directory: %w", err)
	}
	path := filepath.Join(m.cfg.StateDir, "resolv.conf")
	tmp := path + ".tmp"
	// World-readable: it is mounted read-only into sandboxes running as the
	// daemon's uid and holds only public resolver addresses.
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write sandbox resolv.conf: %w", err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	m.resolvConf = path
	return nil
}

// ContainerName is the deterministic container name for a project root.
func ContainerName(root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	base := strings.ToLower(filepath.Base(root))
	clean := make([]rune, 0, 24)
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			clean = append(clean, r)
		}
		if len(clean) == 24 {
			break
		}
	}
	slug := strings.Trim(string(clean), "-")
	if slug == "" {
		slug = "project"
	}
	return containerPrefix + slug + "-" + hex.EncodeToString(sum[:6])
}

// validateMounts refuses mounts that are, contain, or sit inside a protected
// root, the filesystem root, or that use characters Docker's mount syntax
// cannot carry. Paths are resolved through symlinks first.
func (m *Manager) validateMounts(mounts []string) ([]string, error) {
	out := make([]string, 0, len(mounts))
	seen := map[string]struct{}{}
	for _, mount := range mounts {
		if !filepath.IsAbs(mount) {
			return nil, fmt.Errorf("sandbox mount %q is not absolute", mount)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Clean(mount))
		if err != nil {
			return nil, fmt.Errorf("sandbox mount %q: %w", mount, err)
		}
		if resolved == string(filepath.Separator) {
			return nil, errors.New("sandbox cannot mount the filesystem root")
		}
		if strings.ContainsAny(resolved, ",\x00\n") {
			return nil, fmt.Errorf("sandbox mount %q contains unsupported characters", resolved)
		}
		for _, protected := range m.cfg.ProtectedRoots {
			p := resolveForCompare(protected)
			if p == "" {
				continue
			}
			if within(p, resolved) || within(resolved, p) {
				return nil, fmt.Errorf("sandbox mount %q overlaps protected Swarm storage", resolved)
			}
		}
		for _, ancestor := range m.cfg.ProtectedAncestors {
			if a := resolveForCompare(ancestor); a != "" && within(resolved, a) {
				return nil, fmt.Errorf("sandbox cannot mount %q: it contains %q", resolved, a)
			}
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		out = append(out, resolved)
	}
	sort.Strings(out)
	return out, nil
}

func resolveForCompare(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved
	}
	return clean
}

// within reports whether path is root or below it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// sandboxUser returns the uid:gid a project's sandbox runs as. A folder a
// person made visible to Swarm belongs to that person, and gVisor decides
// file access from plain ownership and mode bits (it does not see POSIX
// ACLs), so such a sandbox runs as the folder's owner with the daemon's
// group: agent commands and Git see the repository as its owner does, and
// what they create stays the person's. Everything else runs as the daemon.
func (m *Manager) sandboxUser(root string) (int, int) {
	owner := m.owner
	if owner == nil {
		owner = fileOwnerUID
	}
	if uid, ok := owner(root); ok && uid != 0 && uid != m.cfg.UID {
		return uid, m.cfg.GID
	}
	return m.cfg.UID, m.cfg.GID
}

// RunsAsOwner reports whether root's sandbox runs as the folder's owner
// rather than the daemon.
func (m *Manager) RunsAsOwner(root string) bool {
	uid, _ := m.sandboxUser(filepath.Clean(root))
	return uid != m.cfg.UID
}

func (m *Manager) specHash(root string, mounts []string, secretFingerprint string) string {
	m.mu.Lock()
	imageID := m.imageID
	m.mu.Unlock()
	return m.specHashImage(root, mounts, secretFingerprint, imageID)
}

// specHashImage is specHash for the image identity a folder runs on.
func (m *Manager) specHashImage(root string, mounts []string, secretFingerprint, imageID string) string {
	uid, gid := m.sandboxUser(root)
	m.mu.Lock()
	parts := []string{
		"v5", imageID, m.runtime, m.cfg.Network, m.cfg.Memory, strconv.Itoa(m.cfg.PidsLimit), m.cfg.CPUs, m.resolvConf,
		strconv.Itoa(uid), strconv.Itoa(gid), strings.Join(m.cfg.DNS, ","), secretFingerprint,
	}
	m.mu.Unlock()
	parts = append(parts, mounts...)
	parts = append(parts, gitGuardSpec(gitGuardMounts(root))...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:12])
}

// RunArgs builds the hardened `docker run` arguments for a sandbox. It is
// exported for the negative test suite, which asserts every hardening flag.
func (m *Manager) RunArgs(name, root, spec string, mounts []string, secret SecretSetup) []string {
	return m.runArgsImage(name, root, spec, mounts, secret, EnvironmentImage{Image: m.cfg.Image, Path: sandboxPath})
}

// runArgsImage is RunArgs for the image a folder runs on; only the image and
// its PATH differ, never the hardening.
func (m *Manager) runArgsImage(name, root, spec string, mounts []string, secret SecretSetup, image EnvironmentImage) []string {
	m.mu.Lock()
	runtime := m.runtime
	m.mu.Unlock()
	uid, gid := m.sandboxUser(root)
	user := fmt.Sprintf("%d:%d", uid, gid)
	args := []string{"run", "-d", "--init", "--name", name,
		"--label", labelSandbox + "=1",
		"--label", labelRoot + "=" + root,
		"--label", labelSpec + "=" + spec,
	}
	if runtime != "" {
		args = append(args, "--runtime", runtime)
	}
	args = append(args,
		"--user", user,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--memory", m.cfg.Memory, "--memory-swap", m.cfg.Memory,
		"--pids-limit", strconv.Itoa(m.cfg.PidsLimit),
		"--network", m.cfg.Network,
		"--hostname", "sandbox",
		"--ipc", "private",
		"--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=2g,mode=1777",
		"--tmpfs", fmt.Sprintf("/run/swarm:rw,nosuid,nodev,noexec,size=16m,uid=%d,gid=%d,mode=0700", uid, gid),
		"--mount", "type=volume,src="+name+"-home,dst="+sandboxHome,
		"--env", "HOME="+sandboxHome,
		"--env", "GOTMPDIR="+sandboxHome+"/.cache/go-tmp",
		"--env", "PATH="+image.Path,
		"--env", "LANG=C.UTF-8",
		"--workdir", root,
	)
	if m.cfg.CPUs != "" {
		args = append(args, "--cpus", m.cfg.CPUs)
	}
	for _, dns := range m.cfg.DNS {
		args = append(args, "--dns", dns)
	}
	if m.resolvConf != "" {
		// A mount on /etc/resolv.conf replaces Docker's generated one.
		args = append(args, "--mount", "type=bind,src="+m.resolvConf+",dst=/etc/resolv.conf,readonly")
	}
	for _, mount := range mounts {
		args = append(args, "--mount", "type=bind,src="+mount+",dst="+mount)
	}
	// Read-only guards over the project's hooks and config (gitguard.go).
	for _, g := range gitGuardMounts(root) {
		args = append(args, "--mount", "type=bind,src="+g.HostPath+",dst="+g.Dest+",readonly")
	}
	for _, kv := range secret.Env {
		args = append(args, "--env", kv)
	}
	for _, f := range secret.Files {
		if f.HostPath != "" && f.Dest != "" {
			args = append(args, "--mount", "type=bind,src="+f.HostPath+",dst="+f.Dest+",readonly")
		}
	}
	if image.Image != m.cfg.Image {
		// An environment image may carry any entrypoint (the base image's, or
		// the build container's sleep, which docker commit keeps): run sleep
		// directly so the sandbox stays up whatever the image declares.
		args = append(args, "--entrypoint", "sleep", image.Image, "infinity")
		return args
	}
	args = append(args, image.Image, "sleep", "infinity")
	return args
}

// acquire makes sure the project's sandbox is running with the given mounts
// and returns an inflight lease; the caller must call release.
func (m *Manager) acquire(ctx context.Context, scope Scope, command ...string) (*box, error) {
	nameOnly := "acquire"
	if len(command) > 0 && command[0] != "" {
		nameOnly = filepath.Base(command[0])
	}
	stopLog := logSlowSandboxWait(nameOnly)
	defer stopLog()
	if !m.Active() {
		return nil, fmt.Errorf("%w: %s", ErrInactive, m.Status().Reason)
	}
	m.mu.Lock()
	closing := scope.ReviewCheckout != "" && m.reviewClosing[scope.ReviewCheckout]
	m.mu.Unlock()
	if closing {
		return nil, errors.New("review sandbox is closing")
	}
	root := filepath.Clean(scope.Root)
	if scope.ReviewCheckout != "" {
		if _, err := reviewGitMounts(scope.ReviewCheckout, root); err != nil {
			return nil, err
		}
	}
	mounts, err := m.validateMounts(append([]string{root}, scope.Mounts...))
	if err != nil {
		return nil, err
	}
	if err := m.markUsed(root); err != nil {
		return nil, err
	}
	name := scopeContainerName(scope)
	secret := SecretSetup{}
	if broker := m.secretBroker(); broker != nil {
		if secret, err = broker.PrepareSandbox(root); err != nil {
			return nil, fmt.Errorf("prepare sandbox secrets: %w", err)
		}
	}
	image, imageID, err := m.sandboxImage(root)
	if err != nil {
		return nil, err
	}
	spec := m.specHashImage(root, mounts, secret.Fingerprint, imageID)
	if scope.ReviewCheckout != "" {
		spec += "-review-" + ContainerName(scope.ReviewCheckout)
	}

	for {
		m.mu.Lock()
		if scope.ReviewCheckout != "" && m.reviewClosing[scope.ReviewCheckout] {
			m.mu.Unlock()
			return nil, errors.New("review sandbox is closing")
		}
		b := m.boxes[name]
		if b == nil {
			b = &box{name: name, root: root}
			m.boxes[name] = b
		}
		if changing := b.changing; changing != nil {
			m.mu.Unlock()
			select {
			case <-changing:
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("wait for sandbox lifecycle: %w", ctx.Err())
			}
		}
		// A matching container already in use needs no periodic inspection.
		// Reserving a lease here also prevents the idle reaper from stopping it.
		if b.spec == spec && !b.invalid && (b.inflight > 0 || time.Since(b.verified) < verifyInterval) {
			b.inflight++
			m.mu.Unlock()
			return b, nil
		}
		if b.inflight > 0 {
			m.mu.Unlock()
			// Fail promptly rather than starving reads of the current spec or
			// destroying the container underneath running commands.
			return nil, errors.New("sandbox requires recreation or recovery while commands are running; retry after they finish")
		}
		b.changing = make(chan struct{})
		m.mu.Unlock()

		// Starting has its own bounded budget, including for Warm callers
		// whose command deadline has already expired.
		ensureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ensureTimeout)
		var startedAt string
		startedAt, err = m.ensure(ensureCtx, name, root, spec, mounts, secret, image, scope.ReviewCheckout)
		cancel()
		m.mu.Lock()
		if err == nil {
			b.spec, b.mounts, b.secret = spec, mounts, secret
			b.verified = time.Now()
			b.startedAt = startedAt
			b.invalid = false
			b.inflight++
		} else {
			b.spec = ""
		}
		close(b.changing)
		b.changing = nil
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return b, nil
	}
}

// logSlowSandboxWait reports a blocked acquisition once, without arguments,
// paths, environment or output (all may contain private command data).
func logSlowSandboxWait(command string) func() {
	timer := time.AfterFunc(2*time.Second, func() {
		slog.Warn("sandbox acquisition waiting longer than 2s", "command", command)
	})
	return func() { timer.Stop() }
}

func (m *Manager) release(b *box) {
	m.mu.Lock()
	b.inflight--
	b.lastUsed = time.Now()
	m.mu.Unlock()
}

func (m *Manager) ensure(ctx context.Context, name, root, spec string, mounts []string, secret SecretSetup, image EnvironmentImage, reviewCheckouts ...string) (string, error) {
	reviewCheckout := ""
	if len(reviewCheckouts) > 0 {
		reviewCheckout = reviewCheckouts[0]
	}
	out, err := m.runner.Run(ctx, m.cfg.Engine, "container", "inspect", "--format", "{{index .Config.Labels \""+labelSpec+"\"}} {{.State.Running}} {{.State.StartedAt}}", name)
	if err == nil {
		fields := strings.Fields(string(out))
		if len(fields) == 3 && fields[0] == spec {
			if fields[1] == "true" {
				return fields[2], nil
			}
			if _, err := m.runner.Run(ctx, m.cfg.Engine, "start", name); err != nil {
				return "", fmt.Errorf("start sandbox: %w", err)
			}
			state, err := m.inspectState(ctx, name)
			return state.StartedAt, err
		}
		// Different spec (new mounts, image or limits): replace it. The home
		// volume survives, so caches and tool installs are kept.
		if _, err := m.runner.Run(ctx, m.cfg.Engine, "rm", "-f", name); err != nil {
			return "", fmt.Errorf("replace sandbox: %w", err)
		}
	}
	if _, err := m.runner.Run(ctx, m.cfg.Engine, m.scopeRunArgs(name, root, spec, mounts, secret, image, reviewCheckout)...); err != nil {
		return "", fmt.Errorf("create sandbox: %w", err)
	}
	m.initializeHome(ctx, name, root, image)
	if err := m.prepareGoTemp(ctx, name); err != nil {
		return "", err
	}
	if err := m.configureGitIdentity(ctx, name); err != nil {
		slog.Warn("configure sandbox Git identity failed", "error", err)
	}
	state, err := m.inspectState(ctx, name)
	return state.StartedAt, err
}

// ExecRequest is a command for a project's sandbox.
type ExecRequest struct {
	Command      []string
	WorkingDir   string
	Env          map[string]string
	SecretValues []string
	Timeout      time.Duration
	MaxTimeout   time.Duration
	MaxOutput    int
	OnOutput     func([]byte)
}

// Exec runs req in the project's sandbox under the supervised exec contract.
func (m *Manager) Exec(ctx context.Context, scope Scope, req ExecRequest) (*provider.ExecResult, error) {
	var err error
	scope, err = m.enforceReviewIdentity(ctx, scope, req.WorkingDir)
	if err != nil {
		return nil, err
	}
	result, err := m.execOnce(ctx, scope, req)
	if errors.Is(err, provider.ErrSupervisorUnavailable) && ctx.Err() == nil {
		// The container went away underneath us (stopped or removed outside
		// Swarm); nothing ran. Re-verify it and try once more.
		m.invalidate(scopeContainerName(scope))
		return m.execOnce(ctx, scope, req)
	}
	return result, err
}

func (m *Manager) execOnce(ctx context.Context, scope Scope, req ExecRequest) (*provider.ExecResult, error) {
	b, err := m.acquire(ctx, scope, req.Command...)
	if err != nil {
		return nil, err
	}
	defer m.release(b)
	if err := m.requireMounted(b, req.WorkingDir); err != nil {
		return nil, err
	}
	// Agent Git inside the sandbox needs safe.directory for folders the owner
	// made visible, which belong to another uid.
	req.Env = safeDirectoryEnvMap(req.Env, m.safeDirectories())
	if scope.ReviewCheckout != "" {
		req.Env = safeDirectoryEnvMap(req.Env, []string{scope.ReviewCheckout})
		req.Env["GIT_OPTIONAL_LOCKS"] = "0"
		if values, ok := reviewEnvironments.Load(scope.ReviewCheckout); ok {
			for key, value := range values.(map[string]string) {
				req.Env[key] = value
			}
		}
	}
	var progress provider.ExecProgressCallback
	if req.OnOutput != nil {
		progress = func(p provider.ExecProgress) { req.OnOutput(p.Data) }
	}
	m.mu.Lock()
	startedAt := b.startedAt
	m.mu.Unlock()
	before := containerState{Running: true, StartedAt: startedAt}
	result, execErr := provider.ExecSupervised(ctx, &transport{m: m}, b.name, provider.ExecRequest{
		Command:      withSandboxUmask(req.Command...),
		WorkingDir:   req.WorkingDir,
		Env:          req.Env,
		SecretValues: req.SecretValues,
		Timeout:      req.Timeout,
		MaxOutput:    req.MaxOutput,
		OnProgress:   progress,
	}, req.MaxTimeout)
	if execErr != nil || (result != nil && result.ExitCode != 0) {
		after, inspectErr := m.inspectState(ctx, b.name)
		if inspectErr == nil && sandboxDied(before, after) {
			m.invalidate(b.name)
			return nil, &ErrSandboxDied{OOMKilled: after.OOMKilled, Memory: m.cfg.Memory}
		}
	}
	return result, execErr
}

func (m *Manager) invalidate(name string) {
	m.mu.Lock()
	b := m.boxes[name]
	if b != nil {
		b.verified = time.Time{}
		b.invalid = true
	}
	m.mu.Unlock()
}

func (m *Manager) requireMounted(b *box, paths ...string) error {
	for _, p := range paths {
		if p == "" {
			continue
		}
		ok := false
		for _, mount := range b.mounts {
			if within(mount, p) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("path %q is outside the project's sandbox", p)
		}
	}
	return nil
}

type transport struct{ m *Manager }

func (t *transport) RunExec(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, execArgs ...string) error {
	return t.m.runner.RunWithIO(ctx, stdin, stdout, stderr, t.m.cfg.Engine, append([]string{"exec"}, execArgs...)...)
}

func (t *transport) RunExecCombined(ctx context.Context, execArgs ...string) ([]byte, error) {
	return t.m.runner.RunCombined(ctx, t.m.cfg.Engine, append([]string{"exec"}, execArgs...)...)
}

// Projects that ever used a sandbox stay sandbox-only: their repositories may
// hold agent-planted Git configuration, so a later process without sandboxes
// must refuse to run Git on them rather than run it as the daemon.
const usedFile = "sandboxed-projects.json"

func (m *Manager) loadUsed() error {
	if m.cfg.StateDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(m.cfg.StateDir, usedFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read sandbox project list: %w", err)
	}
	var roots []string
	if err := json.Unmarshal(data, &roots); err != nil {
		return fmt.Errorf("parse sandbox project list: %w", err)
	}
	for _, r := range roots {
		m.used[r] = struct{}{}
	}
	return nil
}

func (m *Manager) markUsed(root string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.used[root]; ok {
		return nil
	}
	m.used[root] = struct{}{}
	if m.cfg.StateDir == "" {
		return nil
	}
	roots := make([]string, 0, len(m.used))
	for r := range m.used {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	data, err := json.Marshal(roots)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.cfg.StateDir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(m.cfg.StateDir, usedFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(m.cfg.StateDir, usedFile))
}

// WasSandboxed reports whether root has ever run in a sandbox.
func (m *Manager) WasSandboxed(root string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.used[filepath.Clean(root)]
	return ok
}
