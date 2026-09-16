package launcher

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"testing"
)

// Requirement: in-app passwords reach only one validated new account via stdin;
// Skip locks login, existing/stale identities cannot be reset, and failure must
// not install. The injected NSS/process seam is the narrowest nonprivileged proof.
func TestOnboardingAccountCompletion(t *testing.T) {
	for _, scenario := range []string{"password", "skip", "existing skip", "existing password", "stale", "failure", "lock failure", "invalid"} {
		t.Run(scenario, func(t *testing.T) {
			u := &user.User{Username: "developer", Uid: "1234", Gid: "1234", HomeDir: "/home/developer"}
			a := &OnboardingAccount{account: u, created: !strings.HasPrefix(scenario, "existing")}
			ops := installAccountOps{euid: 0, existing: func() (string, string, bool, error) { return "", "", false, nil }, lookup: func(string) (*user.User, error) {
				copy := *u
				if scenario == "stale" {
					copy.Uid = "1235"
				}
				return &copy, nil
			}, stat: func(string) (os.FileInfo, error) {
				return ownerInfo{mode: os.ModeDir | 0750, uid: 1234, gid: 1234}, nil
			}}
			password := []byte("test-only-password")
			if strings.Contains(scenario, "skip") || scenario == "lock failure" {
				password = nil
			}
			if scenario == "invalid" {
				password = []byte("bad\nsecond:entry")
			}
			sets, locks, installs := 0, 0, 0
			err := a.complete(password, func() error { installs++; return nil }, ops, func(name string, p []byte) error {
				sets++
				if name != "developer" || string(p) != "test-only-password" {
					t.Fatal("wrong stdin target")
				}
				if scenario == "failure" {
					return errors.New("failed")
				}
				return nil
			}, func(name string) error {
				locks++
				if name != "developer" {
					t.Fatal("wrong lock target")
				}
				if scenario == "lock failure" {
					return errors.New("failed")
				}
				return nil
			})
			sshStage := scenario == "password" || scenario == "skip"
			if sshStage && !errors.Is(err, ErrOnboardingSSHRequired) {
				t.Fatalf("missing SSH decision: %v", err)
			}
			success := scenario == "existing skip"
			if (err == nil) != success {
				t.Fatalf("unexpected outcome: %v", err)
			}
			wantInstall := 0
			if success {
				wantInstall = 1
			}
			if installs != wantInstall {
				t.Fatal("installation did not honor failure")
			}
			wantSet := 0
			if scenario == "password" || scenario == "failure" {
				wantSet = 1
			}
			wantLock := 0
			if scenario == "skip" || scenario == "lock failure" {
				wantLock = 1
			}
			if sets != wantSet || locks != wantLock {
				t.Fatalf("mutations set=%d lock=%d", sets, locks)
			}
		})
	}
}

// The child test process checks stdin/argv without calling any OS account tool.
func TestOnboardingPasswordPipe(t *testing.T) {
	if os.Getenv("SWARM_PASSWORD_PIPE_TEST") == "1" {
		data, _ := io.ReadAll(os.Stdin)
		if string(data) != "developer:test-only-password\n" {
			os.Exit(3)
		}
		for _, arg := range os.Args {
			if strings.Contains(arg, "test-only-password") {
				os.Exit(4)
			}
		}
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestOnboardingPasswordPipe$")
	cmd.Env = append(os.Environ(), "SWARM_PASSWORD_PIPE_TEST=1")
	if err := runOnboardingPassword(cmd, "developer", []byte("test-only-password")); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"", "bad\nroot:injected", "bad:colon", "bad\x00nul", strings.Repeat("x", 1025)} {
		cmd := exec.Command("must-not-execute")
		if err := runOnboardingPassword(cmd, "developer", []byte(secret)); err == nil {
			t.Fatal("invalid password accepted")
		}
		if cmd.Process != nil {
			t.Fatal("invalid password started process")
		}
	}
}
