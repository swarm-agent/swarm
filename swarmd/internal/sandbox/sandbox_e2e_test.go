package sandbox

// Purpose: Phase 1 requires that agent commands, and the daemon's Git on
// agent-writable repositories, run inside a per-project sandbox where normal
// development work succeeds (positive suite) but Swarm's storage, login,
// local socket, ports, the host, the tailnet, private networks and cloud
// metadata are unreachable (negative suite). The owning symbols are
// Manager.Exec, Manager.RouteGit, Manager.RunArgs and validateMounts, plus the
// installer's containers/sandbox/firewall.sh for the network rules. Only a
// real container engine can prove these properties, so this suite runs
// against Docker and is opt-in: SWARM_SANDBOX_E2E=1 with the swarm-sandbox
// image built and containers/sandbox/firewall.sh applied (see
// scripts/test-sandbox.sh). It is not part of the hermetic critical tiers.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type e2eEnv struct {
	m         *Manager
	project   string
	worktrees string
	data      string
	scope     Scope
	uid, gid  int
}

func e2eSetup(t *testing.T) *e2eEnv {
	t.Helper()
	if os.Getenv("SWARM_SANDBOX_E2E") != "1" {
		t.Skip("set SWARM_SANDBOX_E2E=1 (see scripts/test-sandbox.sh) to run the Docker sandbox suite")
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		// The manager never runs sandboxes as root; use a service-like uid.
		uid, gid = 10050, 10050
		if v, err := strconv.Atoi(os.Getenv("SWARM_SANDBOX_E2E_UID")); err == nil {
			uid, gid = v, v
		}
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := &e2eEnv{uid: uid, gid: gid,
		project:   filepath.Join(base, "projects", "demo"),
		worktrees: filepath.Join(base, "home", ".local", "share", "swarm", "worktrees"),
		data:      filepath.Join(base, "var", "lib", "swarmd"),
	}
	for _, dir := range []string{env.project, env.worktrees, filepath.Join(env.data, "local-transport")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(env.data, "swarmd-secrets.pebble.key"), []byte("daemon-root-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.data, "local-transport", "api.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	chownTree(t, env.project, uid, gid)
	chownTree(t, filepath.Dir(env.worktrees), uid, gid)

	m, err := NewManager(context.Background(), Config{
		Mode: ModeRequired, UID: uid, GID: gid,
		// A test environment behind a TLS-inspecting proxy can point this at
		// a derived image that trusts the proxy's CA.
		Image:              os.Getenv("SWARM_SANDBOX_E2E_IMAGE"),
		ProtectedRoots:     []string{env.data},
		ProtectedAncestors: []string{filepath.Join(base, "home")},
		StateDir:           filepath.Join(env.data, "sandbox"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Active() {
		t.Fatalf("sandbox not active: %s", m.Status().Reason)
	}
	m.SetLayout(Layout{
		Projects:      func() ([]string, error) { return []string{env.project}, nil },
		WorktreesRoot: env.worktrees,
		Bucket:        func(root string) (string, error) { return "demo-" + filepath.Base(root), nil },
	})
	scope, ok, err := m.ScopeFor(env.project)
	if err != nil || !ok {
		t.Fatalf("scope: %v %v", ok, err)
	}
	chownTree(t, env.worktrees, uid, gid)
	env.m, env.scope = m, scope
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		name := ContainerName(env.project)
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
		_ = exec.CommandContext(ctx, "docker", "volume", "rm", name+"-home").Run()
		// Files written by the sandbox uid must be removable by the test.
		_ = exec.CommandContext(ctx, "chown", "-R", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), base).Run()
	})
	return env
}

func chownTree(t *testing.T, root string, uid, gid int) {
	t.Helper()
	if os.Getuid() != 0 {
		return
	}
	err := filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *e2eEnv) sh(t *testing.T, script string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := e.m.Exec(ctx, e.scope, ExecRequest{
		Command:    []string{"bash", "-c", "exec 2>&1; " + script},
		WorkingDir: e.project,
		Timeout:    4 * time.Minute,
		MaxTimeout: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("exec %q: %v", script, err)
	}
	return res.Stdout, res.ExitCode
}

func (e *e2eEnv) mustSh(t *testing.T, script string) string {
	t.Helper()
	out, code := e.sh(t, script)
	if code != 0 {
		t.Fatalf("%q exited %d:\n%s", script, code, out)
	}
	return out
}

func TestSandboxPositiveSuite(t *testing.T) {
	e := e2eSetup(t)

	t.Run("same paths inside and outside", func(t *testing.T) {
		out := e.mustSh(t, "pwd; echo hello > from-sandbox.txt")
		if strings.TrimSpace(out) != e.project {
			t.Fatalf("working directory %q, want %q", out, e.project)
		}
		data, err := os.ReadFile(filepath.Join(e.project, "from-sandbox.txt"))
		if err != nil || strings.TrimSpace(string(data)) != "hello" {
			t.Fatalf("host does not see the sandbox's file: %q %v", data, err)
		}
	})
	t.Run("git", func(t *testing.T) {
		e.mustSh(t, `git init -q -b main . && git config user.email a@example.invalid && git config user.name Agent &&
git add -A && git commit -qm init && git log --oneline | grep -q init`)
	})
	t.Run("build and run a program", func(t *testing.T) {
		e.mustSh(t, `mkdir -p cprog && cd cprog && printf '#include <stdio.h>\nint main(void){puts("built");return 0;}\n' > main.c &&
printf 'main: main.c\n\tcc -O2 -o main main.c\n' > Makefile && make -s && ./main | grep -q built`)
	})
	t.Run("tests", func(t *testing.T) {
		e.mustSh(t, `mkdir -p pytests && cd pytests && printf 'import unittest\nclass T(unittest.TestCase):\n    def test_ok(self):\n        self.assertEqual(1+1, 2)\nunittest.main()\n' > test_x.py && python3 test_x.py`)
	})
	t.Run("package installs", func(t *testing.T) {
		e.mustSh(t, `mkdir -p nodeapp && cd nodeapp && npm init -y >/dev/null && npm install --no-audit --no-fund is-number@7.0.0 >/dev/null &&
node -e 'process.exit(require("is-number")(5) ? 0 : 1)'`)
		e.mustSh(t, `python3 -m venv .venv && .venv/bin/pip install -q --disable-pip-version-check six==1.16.0 && .venv/bin/python -c 'import six'`)
	})
	t.Run("dev server", func(t *testing.T) {
		e.mustSh(t, `mkdir -p site && cd site && echo served > index.html && (python3 -m http.server 8765 --bind 127.0.0.1 >/dev/null 2>&1 & echo $! > ../server.pid) &&
for i in $(seq 1 50); do curl -fsS http://127.0.0.1:8765/ 2>/dev/null | grep -q served && ok=1 && break; sleep 0.1; done; kill "$(cat ../server.pid)"; test "$ok" = 1`)
	})
	t.Run("daemon git routed into the sandbox", func(t *testing.T) {
		cmd := exec.Command("git", "--no-optional-locks", "-C", e.project, "status", "--porcelain=v2", "--branch")
		if err := e.m.RouteGit(context.Background(), cmd); err != nil {
			t.Fatal(err)
		}
		if filepath.Base(cmd.Path) != "docker" {
			t.Fatalf("git was not routed: %v", cmd.Args)
		}
		out, err := cmd.Output()
		if err != nil || !strings.Contains(string(out), "# branch.head main") {
			t.Fatalf("routed git status: %q %v", out, err)
		}
	})
	t.Run("worktrees in the project's bucket", func(t *testing.T) {
		wt := filepath.Join(e.scope.Mounts[1], "lane-1")
		cmd := exec.Command("git", "-C", e.project, "worktree", "add", "-q", "-b", "agent/lane-1", wt)
		if err := e.m.RouteGit(context.Background(), cmd); err != nil {
			t.Fatal(err)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("worktree add: %s %v", out, err)
		}
		status := exec.Command("git", "status", "--porcelain")
		status.Dir = wt
		if err := e.m.RouteGit(context.Background(), status); err != nil {
			t.Fatal(err)
		}
		if out, err := status.CombinedOutput(); err != nil {
			t.Fatalf("worktree status: %s %v", out, err)
		}
	})
	t.Run("idle stop and restart on demand", func(t *testing.T) {
		e.m.stopIdle(context.Background(), time.Now().Add(DefaultIdleTimeout+time.Minute))
		if running(t, ContainerName(e.project)) {
			t.Fatal("idle sandbox still running")
		}
		e.mustSh(t, "test -f from-sandbox.txt")
		if !running(t, ContainerName(e.project)) {
			t.Fatal("sandbox not restarted on demand")
		}
	})
}

func running(t *testing.T, name string) bool {
	t.Helper()
	out, err := exec.Command("docker", "container", "inspect", "--format", "{{.State.Running}}", name).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func TestSandboxNegativeSuite(t *testing.T) {
	t.Setenv("SWARM_E2E_DAEMON_TOKEN", "daemon-env-must-not-leak")
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "/var/lib/swarmd/local-transport/api.sock")
	e := e2eSetup(t)
	name := ContainerName(e.project)

	t.Run("daemon storage, login and socket are not visible", func(t *testing.T) {
		for _, p := range []string{e.data, filepath.Join(e.data, "swarmd-secrets.pebble.key"), filepath.Join(e.data, "local-transport", "api.sock"), "/var/lib/swarmd", "/etc/swarmd", "/run/swarmd", "/var/run/docker.sock", "/run/docker.sock"} {
			if out, code := e.sh(t, "ls -la "+p+" 2>&1; cat "+p+" 2>&1"); code == 0 || strings.Contains(out, "daemon-root-key") {
				t.Fatalf("%s is reachable from the sandbox:\n%s", p, out)
			}
		}
	})
	t.Run("no daemon environment", func(t *testing.T) {
		out := e.mustSh(t, "env")
		for _, leaked := range []string{"daemon-env-must-not-leak", "SWARMD_LOCAL_TRANSPORT_SOCKET", "SWARM_E2E_DAEMON_TOKEN"} {
			if strings.Contains(out, leaked) {
				t.Fatalf("daemon environment leaked (%s):\n%s", leaked, out)
			}
		}
	})
	t.Run("no privileges", func(t *testing.T) {
		out := e.mustSh(t, "id -u; grep -E '^(CapEff|CapPrm|CapBnd|NoNewPrivs):' /proc/self/status")
		if strings.HasPrefix(strings.TrimSpace(out), "0\n") || !strings.Contains(out, "NoNewPrivs:\t1") {
			t.Fatalf("sandbox runs privileged:\n%s", out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "Cap") && !strings.HasSuffix(line, "0000000000000000") {
				t.Fatalf("capabilities present: %s", line)
			}
		}
		if out, code := e.sh(t, "mount -t tmpfs none /mnt"); code == 0 {
			t.Fatalf("mount succeeded:\n%s", out)
		}
		if out, code := e.sh(t, "touch /etc/swarm-test"); code == 0 {
			t.Fatalf("wrote to system directories:\n%s", out)
		}
	})
	t.Run("container hardening flags", func(t *testing.T) {
		e.mustSh(t, "true")
		out, err := exec.Command("docker", "container", "inspect", name).Output()
		if err != nil {
			t.Fatal(err)
		}
		var info []struct {
			HostConfig struct {
				Privileged  bool
				CapDrop     []string
				CapAdd      []string
				SecurityOpt []string
				PidsLimit   *int64
				Memory      int64
				NetworkMode string
				Binds       []string
			}
			Mounts []struct {
				Type, Source, Destination string
				RW                        bool
			}
			Config struct {
				User string
				Env  []string
			}
		}
		if err := json.Unmarshal(out, &info); err != nil || len(info) != 1 {
			t.Fatalf("inspect: %v", err)
		}
		hc := info[0].HostConfig
		if hc.Privileged || len(hc.CapAdd) != 0 || strings.Join(hc.CapDrop, ",") != "ALL" ||
			!strings.Contains(strings.Join(hc.SecurityOpt, ","), "no-new-privileges") ||
			hc.PidsLimit == nil || *hc.PidsLimit != DefaultPidsLimit || hc.Memory == 0 || hc.NetworkMode != DefaultNetwork {
			t.Fatalf("hardening flags missing: %+v", hc)
		}
		if info[0].Config.User != fmt.Sprintf("%d:%d", e.uid, e.gid) {
			t.Fatalf("runs as %q", info[0].Config.User)
		}
		for _, mount := range info[0].Mounts {
			if mount.Type != "bind" {
				continue
			}
			if mount.Destination == "/etc/resolv.conf" {
				// The one extra mount: Swarm's resolver file, read-only.
				if mount.RW || mount.Source != filepath.Join(e.data, "sandbox", "resolv.conf") {
					t.Fatalf("unexpected resolver mount %+v", mount)
				}
				continue
			}
			if mount.Source != mount.Destination || (mount.Source != e.project && mount.Source != e.scope.Mounts[1]) {
				t.Fatalf("unexpected bind mount %+v", mount)
			}
		}
	})
	t.Run("own resolver config: public resolvers, no host search domain", func(t *testing.T) {
		// Docker's embedded resolver does not answer under gVisor and the
		// host's search domain names the tailnet; the sandbox gets its own.
		out := e.mustSh(t, "cat /etc/resolv.conf")
		if strings.Contains(out, "127.0.0.11") || strings.Contains(out, "search") || !strings.Contains(out, "nameserver 1.1.1.1") {
			t.Fatalf("unexpected resolv.conf:\n%s", out)
		}
		if out, code := e.sh(t, "echo x > /etc/resolv.conf"); code == 0 {
			t.Fatalf("resolv.conf is writable from the sandbox:\n%s", out)
		}
	})
	t.Run("network: internet yes; host, tailnet, private, metadata no", func(t *testing.T) {
		listener, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		go func() {
			for {
				c, err := listener.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		port := listener.Addr().(*net.TCPAddr).Port
		probe := func(addr string) bool {
			_, code := e.sh(t, fmt.Sprintf("timeout 4 bash -c 'exec 3<>/dev/tcp/%s/%s' 2>/dev/null", strings.Split(addr, ":")[0], strings.Split(addr, ":")[1]))
			return code == 0
		}
		blocked := []string{
			gatewayIP(t) + ":" + strconv.Itoa(port), // Swarm-like port on this host, via the bridge
			"169.254.169.254:80",                    // cloud metadata
			"100.100.100.100:53",                    // tailnet DNS
			"10.0.0.1:22", "192.168.0.1:22",         // private networks
		}
		if ip := hostPrimaryIP(); ip != "" {
			blocked = append(blocked, ip+":"+strconv.Itoa(port)) // this host's own address
		}
		for _, addr := range blocked {
			if probe(addr) {
				t.Fatalf("%s is reachable from the sandbox", addr)
			}
		}
		if !probe("1.1.1.1:443") {
			t.Fatal("the internet is not reachable from the sandbox")
		}
	})
	t.Run("planted git configuration runs inside the sandbox, not as the daemon", func(t *testing.T) {
		e.mustSh(t, `git init -q -b main . 2>/dev/null; git config core.fsmonitor "sh -c 'hostname > fsmonitor-ran-on; exit 1' --"`)
		cmd := exec.Command("git", "-C", e.project, "status", "--porcelain")
		if err := e.m.RouteGit(context.Background(), cmd); err != nil {
			t.Fatal(err)
		}
		_, _ = cmd.CombinedOutput()
		data, err := os.ReadFile(filepath.Join(e.project, "fsmonitor-ran-on"))
		if err != nil || strings.TrimSpace(string(data)) != "sandbox" {
			t.Fatalf("planted fsmonitor did not run confined to the sandbox: %q %v", data, err)
		}
	})
	t.Run("protected roots cannot be mounted", func(t *testing.T) {
		for _, scope := range []Scope{
			{Root: e.data},                     // daemon storage itself
			{Root: filepath.Dir(e.data)},       // a directory containing it
			{Root: filepath.Join(e.data, "x")}, // a directory inside it
			{Root: e.project, Mounts: []string{"/"}},
			{Root: filepath.Dir(filepath.Dir(e.worktrees)) + "/../.."}, // the protected home itself
		} {
			_ = os.MkdirAll(scope.Root, 0o700)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			_, err := e.m.Exec(ctx, scope, ExecRequest{Command: []string{"true"}, WorkingDir: scope.Root})
			cancel()
			if err == nil {
				t.Fatalf("scope %+v was mounted", scope)
			}
			if scope.Root == e.project {
				// The project's sandbox exists already; it must not have
				// gained the refused mount.
				out, _ := exec.Command("docker", "container", "inspect", "--format", "{{range .Mounts}}{{.Source}} {{end}}", name).Output()
				if strings.Contains(" "+string(out), " / ") {
					t.Fatalf("refused mount was applied: %s", out)
				}
			} else if running(t, ContainerName(scope.Root)) {
				t.Fatalf("a container was started for %+v", scope)
			}
		}
	})
	t.Run("commands outside the project are refused", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := e.m.Exec(ctx, e.scope, ExecRequest{Command: []string{"true"}, WorkingDir: e.data}); err == nil {
			t.Fatal("exec with a working directory outside the sandbox succeeded")
		}
	})
	t.Run("git env pointing outside the sandbox is refused", func(t *testing.T) {
		cmd := exec.Command("git", "-C", e.project, "status")
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(e.data, "index"))
		if err := e.m.RouteGit(context.Background(), cmd); err == nil {
			t.Fatal("GIT_INDEX_FILE outside the sandbox was accepted")
		}
	})
}

func gatewayIP(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "network", "inspect", "--format", "{{(index .IPAM.Config 0).Gateway}}", DefaultNetwork).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("sandbox network gateway: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func hostPrimaryIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:53")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
