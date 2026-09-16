package launcher

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	"swarm-refactor/swarmtui/internal/client"
	"testing"
	"time"
)

func handoffAccountOps() (*user.User, installAccountOps) {
	u := &user.User{Username: "developer", Uid: "1234", Gid: "1234", HomeDir: "/home/developer"}
	return u, installAccountOps{euid: 0, existing: func() (string, string, bool, error) { return "", "", false, nil }, lookup: func(string) (*user.User, error) { copy := *u; return &copy, nil }, stat: func(string) (os.FileInfo, error) {
		return ownerInfo{mode: os.ModeDir | 0750, uid: 1234, gid: 1234}, nil
	}}
}

// Requirement: selectedUserCommand must discard root identity, CWD, environment
// and supplementary groups. Inspecting the exec boundary proves credentials
// without privileged execution or creating real users.
func TestSelectedUserHandoffBoundary(t *testing.T) {
	u, ops := handoffAccountOps()
	cmd, err := selectedUserCommand(context.Background(), selectedAccount(u), "/usr/local/bin/swarm", []string{"run"}, []string{"HOME=/root", "PWD=/root/private", "XDG_RUNTIME_DIR=/run/user/0", "SWARMD_TOKEN=not-a-real-token", "LD_PRELOAD=untrusted", "SSH_AUTH_SOCK=untrusted", "TERM=xterm"}, ops, func(*user.User) ([]string, error) { return []string{"1234", "2345"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != u.HomeDir || cmd.SysProcAttr.Credential.Uid != 1234 || cmd.SysProcAttr.Credential.Gid != 1234 || !reflect.DeepEqual(cmd.SysProcAttr.Credential.Groups, []uint32{1234, 2345}) {
		t.Fatalf("incorrect handoff: %+v", cmd.SysProcAttr)
	}
	env := strings.Join(cmd.Env, "\n")
	for _, bad := range []string{"/root", "XDG_", "TOKEN", "LD_PRELOAD", "SSH_AUTH"} {
		if strings.Contains(env, bad) {
			t.Fatalf("inherited %s", bad)
		}
	}
	if !strings.Contains(env, "HOME="+u.HomeDir) || !strings.Contains(env, "TERM=xterm") {
		t.Fatal("missing selected home or terminal")
	}
	stale := selectedAccount(u)
	stale.Home = "/home/changed"
	if c, err := selectedUserCommand(context.Background(), stale, "swarm", nil, nil, ops, func(*user.User) ([]string, error) { t.Fatal("groups resolved for stale user"); return nil, nil }); err == nil || c != nil {
		t.Fatal("stale home admitted")
	}
}

// Requirement: activity is not authenticated readiness; delayed readiness waits,
// failed/inactive service stops, deadlines bound attempts, no restart is invoked.
// waitSetupReadiness is the narrowest deterministic orchestration seam.
func TestSetupReadiness(t *testing.T) {
	for _, state := range []string{"active", "activating", "inactive", "failed"} {
		t.Run(state, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			calls, updates := 0, 0
			err := waitSetupReadiness(ctx, time.Millisecond, func(context.Context) error {
				calls++
				if calls == 3 {
					return nil
				}
				return errors.New("peer endpoint unavailable")
			}, func() string { return state }, func(string) { updates++ })
			ready := state == "active" || state == "activating"
			if (err == nil) != ready || (ready && calls != 3) || (!ready && calls != 1) || updates == 0 {
				t.Fatalf("calls=%d updates=%d err=%v", calls, updates, err)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	err := waitSetupReadiness(ctx, time.Millisecond, func(context.Context) error { return errors.New("unauthenticated") }, func() string { return "active" }, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline not retained: %v", err)
	}
}

// Requirement: install failure after password success must never replay password
// or lock on retry, and changed owner must reject with no install side effects.
// OnboardingAccount.complete uses fake NSS/password/install authorities only.
func TestSetupRetryPreservesPassword(t *testing.T) {
	u, ops := handoffAccountOps()
	a := &OnboardingAccount{account: u, created: true}
	passwords, locks, installs := 0, 0, 0
	set := func(string, []byte) error { passwords++; return nil }
	lock := func(string) error { locks++; return nil }
	install := func() error {
		installs++
		if installs == 1 {
			return errors.New("install failed")
		}
		return nil
	}
	if err := a.complete([]byte("test-only"), install, ops, set, lock); !errors.Is(err, ErrOnboardingSSHRequired) {
		t.Fatal("SSH decision not required", err)
	}
	if err := a.chooseSSH("", true, ops, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.complete(nil, install, ops, set, lock); err == nil {
		t.Fatal("failure swallowed")
	}
	if !a.PasswordDone() {
		t.Fatal("password success lost")
	}
	ops.existing = func() (string, string, bool, error) { return "1234", "1234", true, nil }
	if err := a.complete(nil, install, ops, set, lock); err != nil {
		t.Fatal(err)
	}
	if passwords != 1 || locks != 0 || installs != 2 {
		t.Fatalf("replayed mutations: %d %d %d", passwords, locks, installs)
	}
	ops.existing = func() (string, string, bool, error) { return "9999", "9999", true, nil }
	if err := a.complete(nil, install, ops, set, lock); err == nil || installs != 2 {
		t.Fatal("owner replacement admitted")
	}
}

// Requirement: durable restart must retain the exact selected identity/stage;
// unrelated partial installs, malformed records and symlink records fail closed.
// Fake trust permits temp files without requiring root; rejection is observable.
func TestSetupRecoveryBoundary(t *testing.T) {
	u, ops := handoffAccountOps()
	r := &setupRecovery{Version: 1, Account: selectedAccount(u), Created: true, Stage: "readiness"}
	a, err := resumeSetupAccount(r, ops)
	if err != nil || !a.PasswordDone() || a.Stage() != "readiness" {
		t.Fatalf("resume: %v", err)
	}
	r.Account.UID = "9999"
	if _, err := resumeSetupAccount(r, ops); err == nil {
		t.Fatal("stale identity resumed")
	}
	r.Account = selectedAccount(u)
	r.Stage = "password"
	ops.existing = func() (string, string, bool, error) { return "1234", "1234", true, nil }
	if _, err := resumeSetupAccount(r, ops); err == nil {
		t.Fatal("unrelated installation resumed")
	}
	for _, input := range []string{`{"version":1,"stage":"readiness"}`, `{"version":1,"stage":"unknown"}`, `{"version":1,"stage":"readiness","password":"forbidden"}`} {
		t.Run(input, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "onboarding.json")
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := readSetupRecoveryWithTrust(dir, func(string) error { return nil }, func(os.FileInfo) bool { return true })
			if (err == nil) != (input == `{"version":1,"stage":"readiness"}`) {
				t.Fatalf("decode: %v", err)
			}
		})
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(`{"version":1,"stage":"readiness"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "onboarding.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := readSetupRecoveryWithTrust(dir, func(string) error { return nil }, func(os.FileInfo) bool { return true }); err == nil {
		t.Fatal("symlink followed")
	}
}

// Requirement: a fresh enable/start is sufficient, but a real active upgrade
// still restarts; failed enable never restarts. Callback seam avoids systemd.
func TestSetupActivationPreservesUpgrade(t *testing.T) {
	for _, active := range []bool{false, true} {
		starts, restarts := 0, 0
		err := activateInstalledCandidateIfRunning(active, func() error { starts++; return nil }, func() error { restarts++; return nil })
		if err != nil || starts != 1 || restarts != map[bool]int{false: 0, true: 1}[active] {
			t.Fatal("wrong activation")
		}
	}
	restarts := 0
	if err := activateInstalledCandidateIfRunning(true, func() error { return errors.New("denied") }, func() error { restarts++; return nil }); err == nil || restarts != 0 {
		t.Fatal("restart after failed enable")
	}
}

// Requirement: health alone cannot permit handoff; existing product identities
// require successful local-session issuance, while fresh setup must not create
// an identity. Real HTTP decoding with fake auth responses isolates this gate.
func TestSetupAuthenticatedIdentity(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	for _, mode := range []string{"fresh", "existing", "denied", "missing-token", "unhealthy"} {
		t.Run(mode, func(t *testing.T) {
			sessions, mutations := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					mutations++
					w.WriteHeader(405)
					return
				}
				if r.URL.Path == "/v1/onboarding" {
					if mode == "unhealthy" {
						w.WriteHeader(503)
						return
					}
					if mode == "fresh" {
						w.Write([]byte(`{"ok":true,"identity":{"bootstrapped":false}}`))
						return
					}
					w.Write([]byte(`{"ok":true,"identity":{"bootstrapped":true,"user_id":"test-user"}}`))
					return
				}
				if r.URL.Path == "/v1/auth/desktop/session" {
					sessions++
					if mode == "denied" {
						w.WriteHeader(403)
						return
					}
					if mode == "missing-token" {
						w.Write([]byte(`{"ok":true}`))
						return
					}
					w.Write([]byte(`{"ok":true,"token":"test-session"}`))
					return
				}
				t.Errorf("unexpected endpoint %s", r.URL.Path)
				w.WriteHeader(404)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := probeSetupIdentity(ctx, client.New(server.URL))
			if (err == nil) != (mode == "fresh" || mode == "existing") || mutations != 0 {
				t.Fatalf("err=%v mutations=%d", err, mutations)
			}
			if mode == "fresh" && sessions != 0 {
				t.Fatal("created fresh identity/session")
			}
		})
	}
}

// Requirement: recovery publication is private and atomic, trust rejection must
// leave the prior record byte-identical, and reload keeps the completed stage.
// Temporary filesystem plus fake metadata is narrower than privileged setup.
func TestSetupRecoveryPublication(t *testing.T) {
	dir := t.TempDir()
	u, _ := handoffAccountOps()
	r := setupRecovery{Version: 1, Account: selectedAccount(u), Stage: "install", Artifact: "/opt/test-artifact"}
	trust := func(string) error { return nil }
	if err := saveSetupRecoveryWithTrust(dir, r, trust); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "onboarding.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("record is not private")
	}
	r.Stage = "readiness"
	if err := saveSetupRecoveryWithTrust(dir, r, func(string) error { return errors.New("unsafe parent") }); err == nil {
		t.Fatal("unsafe write admitted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("rejected write changed state")
	}
	if err := saveSetupRecoveryWithTrust(dir, r, trust); err != nil {
		t.Fatal(err)
	}
	loaded, err := readSetupRecoveryWithTrust(dir, trust, func(os.FileInfo) bool { return true })
	if err != nil || loaded.Stage != "readiness" || loaded.Account != r.Account {
		t.Fatalf("reload: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("scratch publication leaked")
	}
}
