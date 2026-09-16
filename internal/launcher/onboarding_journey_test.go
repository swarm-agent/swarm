package launcher

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

// Requirement: the production account, startup-stage, pinned Unix readiness and
// selected-user command boundaries must compose from password/skip and key/skip
// through a failed readiness attempt and deliberate retry, without replaying OS
// mutations or inheriting root CWD/environment. This fake-service journey is the
// narrowest hermetic integration proof: it executes HTTP over a temporary Unix
// socket and inspects (never runs) the credential-bearing application command.
// Real NSS, setuid execution, systemd, PAM and remote SSH are deliberately absent.
func TestSetupFirstRunJourney(t *testing.T) {
	for _, password := range []bool{false, true} {
		for _, key := range []bool{false, true} {
			name := map[bool]string{false: "skip-password", true: "password"}[password] + "/" + map[bool]string{false: "skip-key", true: "key"}[key]
			t.Run(name, func(t *testing.T) {
				u, ops := handoffAccountOps()
				a := &OnboardingAccount{account: u, created: true}
				sets, locks, keys, installs, handoffs := 0, 0, 0, 0, 0
				stage := "install"
				var events []string
				set := func(string, []byte) error { sets++; events = append(events, "password"); return nil }
				lock := func(string) error { locks++; events = append(events, "lock"); return nil }
				noInstall := func() error { t.Fatal("installed before SSH decision"); return nil }
				var secret []byte
				if password {
					secret = []byte("fixture-only")
				}
				if err := a.complete(secret, noInstall, ops, set, lock); !errors.Is(err, ErrOnboardingSSHRequired) {
					t.Fatalf("password stage: %v", err)
				}
				clear(secret)
				if !a.PasswordDone() || !a.SSHRequired() || a.passwordSkipped == password {
					t.Fatal("choice state lost")
				}
				input := ""
				if key {
					input = testSSHKey(7)
				}
				if err := a.chooseSSH(input, !key, ops, func(got *user.User, b []byte, validate func() error) error {
					keys++
					if selectedAccount(got) != selectedAccount(u) || string(b) != input+"\n" {
						t.Fatal("wrong key target")
					}
					return validate()
				}); err != nil {
					t.Fatal(err)
				}
				if err := a.chooseSSH(input, !key, ops, nil); err == nil {
					t.Fatal("repeated SSH choice admitted")
				}
				if err := a.complete([]byte("must-not-replay"), noInstall, ops, set, lock); err == nil {
					t.Fatal("password replay admitted")
				}

				var requests atomic.Int32
				var phase atomic.Int32
				var unexpected atomic.Int32
				socket := filepath.Join(t.TempDir(), "api.sock")
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" || r.URL.Path != "/v1/onboarding" {
						unexpected.Add(1)
						w.WriteHeader(403)
						return
					}
					n := requests.Add(1)
					if phase.Load() == 0 || n < 4 {
						w.WriteHeader(503)
						return
					}
					_, _ = w.Write([]byte(`{"ok":true,"identity":{"bootstrapped":false,"username":"developer"}}`))
				})}
				defer server.Close()
				go server.Serve(listener)
				api := client.NewLocalTransport(socket)
				install := func() error {
					installs++
					events = append(events, "install")
					if selectedInstallAccount == nil || selectedAccount(selectedInstallAccount) != selectedAccount(u) {
						t.Fatal("installer lost selected identity")
					}
					return nil
				}
				mark := func(next string) error { stage = next; events = append(events, next); return nil }
				ready := func() error {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					return waitSetupReadiness(ctx, time.Millisecond, func(ctx context.Context) error { return probeSetupIdentity(ctx, api) }, func() string {
						if phase.Load() == 0 {
							return "failed"
						}
						return "active"
					}, func(message string) {
						if stage != "readiness" || handoffs != 0 || !strings.Contains(message, "readiness") {
							t.Error("premature handoff or missing progress")
						}
					})
				}
				finish := func() error { return prepareSetupApplication(stage, install, ready, mark) }
				if err := a.complete(nil, finish, ops, set, lock); err == nil || stage != "readiness" || installs != 1 {
					t.Fatalf("failed readiness not retained: %v", err)
				}
				// Simulate a process restart from the exact recorded identity/stage.
				ops.existing = func() (string, string, bool, error) { return u.Uid, u.Gid, true, nil }
				a, err = resumeSetupAccount(&setupRecovery{Version: 1, Account: selectedAccount(u), Created: true, Stage: stage, PasswordSkipped: !password}, ops)
				if err != nil {
					t.Fatal(err)
				}
				phase.Store(1)
				if err := a.complete(nil, finish, ops, set, lock); err != nil {
					t.Fatal(err)
				}
				if stage != "handoff" || requests.Load() != 4 {
					t.Fatal("active service bypassed delayed application readiness")
				}
				cmd, err := selectedUserCommand(context.Background(), a.SelectedAccount(), "/usr/local/bin/swarm", []string{"run"}, []string{"HOME=/root", "PWD=/root", "SSH_AUTH_SOCK=untrusted", "TERM=xterm"}, ops, func(*user.User) ([]string, error) { return []string{"1234", "2345"}, nil })
				if err != nil {
					t.Fatal(err)
				}
				handoffs++
				cred := cmd.SysProcAttr.Credential
				if cmd.Dir != u.HomeDir || cred.Uid != 1234 || cred.Gid != 1234 || !reflect.DeepEqual(cred.Groups, []uint32{1234, 2345}) || strings.Contains(strings.Join(cmd.Env, "\n"), "/root") {
					t.Fatal("incorrect selected-user context")
				}
				wantSets, wantLocks, wantKeys := 0, 1, 0
				if password {
					wantSets, wantLocks = 1, 0
				}
				if key {
					wantKeys = 1
				}
				if sets != wantSets || locks != wantLocks || keys != wantKeys || installs != 1 || handoffs != 1 || unexpected.Load() != 0 {
					t.Fatalf("replayed or unauthorized work: %d %d %d %d %d", sets, locks, keys, installs, handoffs)
				}
				first := "lock"
				if password {
					first = "password"
				}
				if !reflect.DeepEqual(events, []string{first, "install", "readiness", "handoff"}) {
					t.Fatalf("wrong stage order: %v", events)
				}
			})
		}
	}
}

// Requirement: prepareSetupApplication must stop on failed installation or
// recovery publication and reject unfinished account stages without side effects.
// Injected failures at this production orchestration seam prove no false handoff.
func TestSetupApplicationStageFailures(t *testing.T) {
	for _, scenario := range []string{"ssh", "done", "install-failure", "save-failure", "handoff-save-failure"} {
		t.Run(scenario, func(t *testing.T) {
			installs, probes, saves := 0, 0, 0
			stage := scenario
			if strings.Contains(scenario, "failure") {
				stage = "install"
			}
			err := prepareSetupApplication(stage, func() error {
				installs++
				if scenario == "install-failure" {
					return errors.New("install failed")
				}
				return nil
			}, func() error { probes++; return nil }, func(stage string) error {
				saves++
				if scenario == "save-failure" || stage == "handoff" {
					return errors.New("save failed")
				}
				return nil
			})
			if err == nil {
				t.Fatal("failure swallowed")
			}
			if scenario == "ssh" || scenario == "done" {
				if installs+probes+saves != 0 {
					t.Fatal("invalid stage mutated")
				}
			}
			if scenario == "install-failure" && (probes != 0 || saves != 0) {
				t.Fatal("install failure advanced")
			}
			if scenario == "save-failure" && probes != 0 {
				t.Fatal("unrecorded installation advanced")
			}
		})
	}
}
