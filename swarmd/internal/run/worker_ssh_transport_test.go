package run

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"

	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"swarm-refactor/swarmtui/pkg/environments"
)

// Requirement: the remote worker preflight must authenticate the selected host
// before executing even a version probe, bound hostile output, and reject root
// or arbitrary shell input. workerSSHProbe owns this transport boundary. A
// loopback SSH protocol fixture is the narrowest layer proving host-key rejection
// before command execution; it is NOT a Swarm deployment or real-model test.
func TestWorkerSSHProbeVerifiesHostBeforeExecution(t *testing.T) {
	for _, trusted := range []bool{true, false} {
		t.Run(strconv.FormatBool(trusted), func(t *testing.T) {
			c, signer, commands := workerSSHTestServer(t, "swarmd test-version\n")
			if !trusted {
				signer = workerSSHTestSigner(t)
			}
			line := knownhosts.Line([]string{net.JoinHostPort(c.SSH.Host, strconv.Itoa(c.SSH.Port))}, signer.PublicKey())
			if err := os.WriteFile(c.SSH.KnownHostsFile, []byte(line+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			v, err := workerSSHProbe(context.Background(), c, []ssh.AuthMethod{ssh.Password("fixture-only")}, "/opt/swarm/swarmd")
			if trusted {
				if err != nil || v != "swarmd test-version" || commands.Load() != 1 {
					t.Fatalf("probe %q %v commands=%d", v, err, commands.Load())
				}
			} else if err == nil || v != "" || commands.Load() != 0 {
				t.Fatalf("untrusted host executed: %q %v commands=%d", v, err, commands.Load())
			}
		})
	}
}

func TestWorkerSSHProbeRejectsUnsafeConfig(t *testing.T) {
	base := environments.Connection{ID: "connection", AccountScopeID: "account", WorkspaceID: "workspace", Kind: environments.ConnectionKindSSH, SSH: &environments.SSHConfig{Host: "host.example", User: "worker", Port: 22, KnownHostsFile: "/trust/known_hosts"}}
	for _, mutate := range []func(*environments.Connection){
		func(c *environments.Connection) { c.AccountScopeID = "" },
		func(c *environments.Connection) { c.SSH.User = "root" },
		func(c *environments.Connection) { c.SSH.Host = "-oProxyCommand=x" },
		func(c *environments.Connection) { c.SSH.KnownHostsFile = "" },
		func(c *environments.Connection) { c.SSH.Port = 0 },
	} {
		c := base.Clone()
		mutate(c)
		if err := validateWorkerSSHProbe(*c, "/opt/swarm/swarmd"); err == nil {
			t.Fatal("unsafe connection accepted")
		}
	}
	for _, executable := range []string{"swarmd", "/opt/../swarmd", "/opt/swarmd;id", "/opt/$(id)", "/opt/swarmd\nwhoami"} {
		if err := validateWorkerSSHProbe(base, executable); err == nil {
			t.Fatalf("unsafe executable %q accepted", executable)
		}
	}
	if _, err := workerSSHProbe(context.Background(), base, nil, "/opt/swarm/swarmd"); err == nil {
		t.Fatal("missing credentials accepted")
	}
}

func TestWorkerSSHProbeBoundsOutput(t *testing.T) {
	c, signer, _ := workerSSHTestServer(t, strings.Repeat("x", 8192))
	line := knownhosts.Line([]string{net.JoinHostPort(c.SSH.Host, strconv.Itoa(c.SSH.Port))}, signer.PublicKey())
	if err := os.WriteFile(c.SSH.KnownHostsFile, []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if v, err := workerSSHProbe(context.Background(), c, []ssh.AuthMethod{ssh.Password("fixture-only")}, "/opt/swarm/swarmd"); err == nil || v != "" {
		t.Fatalf("unbounded output accepted: %d %v", len(v), err)
	}
}

func workerSSHTestSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func workerSSHTestServer(t *testing.T, output string) (environments.Connection, ssh.Signer, *atomic.Int32) {
	t.Helper()
	signer := workerSSHTestSigner(t)
	config := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	commands := &atomic.Int32{}
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		for incoming := range channels {
			if incoming.ChannelType() != "session" {
				_ = incoming.Reject(ssh.UnknownChannelType, "session only")
				continue
			}
			channel, requests, err := incoming.Accept()
			if err != nil {
				return
			}
			for req := range requests {
				var command struct{ Command string }
				if req.Type != "exec" || ssh.Unmarshal(req.Payload, &command) != nil || command.Command != "/opt/swarm/swarmd --version" {
					_ = req.Reply(false, nil)
					continue
				}
				commands.Add(1)
				_ = req.Reply(true, nil)
				_, _ = channel.Write([]byte(output))
				code := make([]byte, 4)
				binary.BigEndian.PutUint32(code, 0)
				_, _ = channel.SendRequest("exit-status", false, code)
				_ = channel.Close()
				break
			}
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return environments.Connection{ID: "connection", AccountScopeID: "account", WorkspaceID: "workspace", Kind: environments.ConnectionKindSSH, SSH: &environments.SSHConfig{Host: host, User: "worker", Port: p, KnownHostsFile: filepath.Join(t.TempDir(), "known_hosts")}}, signer, commands
}

// Requirement: ordinary chat/system/missing identities cannot reach even the
// read-only probe. Exercise the service with a real authority fixture, asserting
// no deployment or context mutation; no SSH listener is involved.
func TestWorkerSSHProbeServiceAuthorization(t *testing.T) {
	_, sessions, service, _ := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { t.Fatal("unexpected dispatch"); return false })
	ws := sessions.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "probe"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "owner", AccountScopeSource: identity.AccountScopeSourceServerState}
	for _, origin := range []string{"missing", "agent", "system"} {
		ctx := context.Background()
		if origin != "missing" {
			id := ""
			if origin == "agent" {
				id = "ordinary-chat"
			}
			ctx, err = automation.BindRuntimeIdentity(ctx, p, origin, id)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err = service.probeWorkerSSHDeployment(ctx, w.ID, "deployment", nil, "/opt/swarm/swarmd"); !errors.Is(err, automation.ErrDenied) {
			t.Fatalf("%s bypass: %v", origin, err)
		}
	}
	ds, err := ws.ListWorkerDeployments("account", w.ID)
	if err != nil || len(ds) != 0 {
		t.Fatalf("probe mutated deployments: %v %v", ds, err)
	}
	c, err := ws.GetWorkerContext("account", w.ID, 0)
	if err != nil || c.Revision != 0 {
		t.Fatalf("probe mutated context: %+v %v", c, err)
	}
}
