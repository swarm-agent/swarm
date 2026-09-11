package launcher

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"os/user"
	"strings"
	"testing"
)

func testSSHKey(seed byte) string {
	b := make([]byte, 51)
	binary.BigEndian.PutUint32(b[:4], 11)
	copy(b[4:], "ssh-ed25519")
	binary.BigEndian.PutUint32(b[15:19], 32)
	for i := 19; i < len(b); i++ {
		b[i] = seed
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(b)
}

// Requirement: parseOnboardingSSHKey admits a bounded single supported public
// key, never options, private material or extra records. Pure parsing is the
// narrowest boundary proving invalid input cannot reach privileged file I/O.
func TestOnboardingSSHParser(t *testing.T) {
	good := testSSHKey(1)
	for _, input := range []string{good, good + " workstation", good + "\n"} {
		key, err := parseOnboardingSSHKey(input)
		if err != nil || string(key) != good+"\n" {
			t.Fatalf("valid: %v", err)
		}
	}
	for _, input := range []string{"", "-----BEGIN OPENSSH PRIVATE KEY-----", "command=\"id\" " + good, good + "\n" + good, good + " " + good, good + " -----BEGIN OPENSSH PRIVATE KEY-----", good + "\n\n", good + "\r\n", good + "\x00", good + "\tcomment", strings.Repeat("x", 4097), "ssh-rsa AAAA", "ssh-ed25519 AAAA", good + "\n# comment"} {
		if _, err := parseOnboardingSSHKey(input); err == nil {
			t.Fatal("unsafe key accepted")
		}
	}
}

// Requirement: the exact new-account capability is the only SSH mutation
// authority. Inject NSS/filesystem operations to prove stale/wrong identity,
// phase, privilege, partial install and malformed requests never advance setup.
func TestOnboardingSSHAccountBoundary(t *testing.T) {
	for _, scenario := range []string{"add", "skip", "existing", "before password", "stale uid", "stale gid", "stale home", "wrong name", "not root", "installation", "bad key", "skip key", "write failure"} {
		t.Run(scenario, func(t *testing.T) {
			u := &user.User{Username: "developer", Uid: "1234", Gid: "1234", HomeDir: "/home/developer"}
			a := &OnboardingAccount{account: u, created: true, passwordDone: true}
			ops := installAccountOps{euid: 0, existing: func() (string, string, bool, error) { return "", "", scenario == "installation", nil }, lookup: func(string) (*user.User, error) {
				v := *u
				switch scenario {
				case "stale uid":
					v.Uid = "1235"
				case "stale gid":
					v.Gid = "1235"
				case "stale home":
					v.HomeDir = "/home/another"
				case "wrong name":
					v.Username = "another"
				}
				return &v, nil
			}, stat: func(string) (os.FileInfo, error) {
				return ownerInfo{mode: os.ModeDir | 0750, uid: 1234, gid: 1234}, nil
			}}
			if scenario == "not root" {
				ops.euid = 1234
			}
			if scenario == "existing" {
				a.created = false
			}
			if scenario == "before password" {
				a.passwordDone = false
			}
			key, skip := testSSHKey(2), scenario == "skip" || scenario == "skip key"
			if scenario == "skip" {
				key = ""
			}
			if scenario == "bad key" {
				key = "PRIVATE KEY"
			}
			writes := 0
			err := a.chooseSSH(key, skip, ops, func(target *user.User, key []byte, check func() error) error {
				writes++
				if *target != *u {
					t.Fatal("wrong target")
				}
				if err := check(); err != nil {
					return err
				}
				if scenario == "write failure" {
					return errors.New("injected")
				}
				return nil
			})
			success := scenario == "add" || scenario == "skip"
			if (err == nil) != success || a.sshDone != success {
				t.Fatalf("state/error: %v %+v", err, a)
			}
			want := 0
			if scenario == "add" || scenario == "write failure" {
				want = 1
			}
			if writes != want {
				t.Fatalf("writes %d", writes)
			}
		})
	}
}

// Requirement: both password choices stop at SSH exactly once; retrying after
// an SSH decision installs without replaying password writes or unlocks.
func TestOnboardingSSHPasswordThenDecision(t *testing.T) {
	for _, skipped := range []bool{false, true} {
		t.Run(map[bool]string{true: "skip", false: "password"}[skipped], func(t *testing.T) {
			u := &user.User{Username: "developer", Uid: "1234", Gid: "1234", HomeDir: "/home/developer"}
			a := &OnboardingAccount{account: u, created: true}
			ops := installAccountOps{euid: 0, existing: func() (string, string, bool, error) { return "", "", false, nil }, lookup: func(string) (*user.User, error) { v := *u; return &v, nil }, stat: func(string) (os.FileInfo, error) {
				return ownerInfo{mode: os.ModeDir | 0750, uid: 1234, gid: 1234}, nil
			}}
			mutations, installs := 0, 0
			set := func(string, []byte) error { mutations++; return nil }
			lock := func(string) error { mutations++; return nil }
			install := func() error { installs++; return nil }
			password := []byte("test-only")
			if skipped {
				password = nil
			}
			if err := a.complete(password, install, ops, set, lock); !errors.Is(err, ErrOnboardingSSHRequired) {
				t.Fatal(err)
			}
			if mutations != 1 || installs != 0 || !a.SSHRequired() || a.passwordSkipped != skipped {
				t.Fatal("password stage not isolated")
			}
			if err := a.chooseSSH("", true, ops, nil); err != nil {
				t.Fatal(err)
			}
			if err := a.complete(nil, install, ops, set, lock); err != nil {
				t.Fatal(err)
			}
			if mutations != 1 || installs != 1 {
				t.Fatal("replayed earlier mutation")
			}
		})
	}
}

// Requirement: unrelated authorized_keys bytes (including restricted copies of
// the same key) are retained, while repeated unrestricted keys are deduplicated.
func TestOnboardingSSHAppend(t *testing.T) {
	key := []byte(testSSHKey(3) + "\n")
	original := []byte("# retained\ncommand=\"restricted\" " + testSSHKey(3))
	next := appendOnboardingKey(original, key)
	if !bytes.HasPrefix(next, original) || !bytes.Equal(appendOnboardingKey(next, key), next) {
		t.Fatal("preservation/dedup failed")
	}
}

// Requirement: guidance binds the intended user and confirmed local address to
// a configured port without claiming login. Fake sshd/service evidence proves
// ambiguous ports, unknown addresses, disabled service and locked accounts are
// disclosed without commands that mutate SSH/PAM/firewall policy.
func TestOnboardingSSHGuidance(t *testing.T) {
	config := "port 2222\npubkeyauthentication yes\nusepam yes\nauthenticationmethods publickey\n"
	good := sshGuidance("developer", true, "192.0.2.10", []string{"192.0.2.10"}, config, nil, true)
	for _, want := range []string{"ssh -p 2222 developer@192.0.2.10", "NOT verified", "may be locked", "PAM", "Blank-password authentication is NOT enabled"} {
		if !strings.Contains(good, want) {
			t.Fatalf("missing %q", want)
		}
	}
	for _, result := range []string{
		sshGuidance("developer", false, "", nil, config, nil, true),
		sshGuidance("developer", false, "192.0.2.11", []string{"192.0.2.10"}, config, nil, true),
		sshGuidance("developer", false, "192.0.2.10", []string{"192.0.2.10"}, config+"port 22\n", nil, true),
		sshGuidance("developer", false, "192.0.2.10", []string{"192.0.2.10"}, config, nil, false),
		sshGuidance("developer", false, "192.0.2.10", []string{"192.0.2.10"}, config, errors.New("unavailable"), true),
	} {
		if strings.Contains(result, "ssh -p") {
			t.Fatal("invented connection command")
		}
	}
}

// Requirement: restart retains the SSH choice boundary and password-lock warning
// without storing key bytes or replaying password mutation. Exercise the same
// strict record reader and NSS resolution used by ResumeOnboardingAccount.
func TestOnboardingSSHRecovery(t *testing.T) {
	u, ops := handoffAccountOps()
	r := setupRecovery{Version: 1, Account: selectedAccount(u), Created: true, Stage: "ssh", PasswordSkipped: true}
	dir := t.TempDir()
	if err := saveSetupRecoveryWithTrust(dir, r, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	loaded, err := readSetupRecoveryWithTrust(dir, func(string) error { return nil }, func(os.FileInfo) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	a, err := resumeSetupAccount(loaded, ops)
	if err != nil || !a.SSHRequired() || !a.passwordSkipped {
		t.Fatal("SSH recovery lost", err)
	}
	r.Stage = "install"
	a, err = resumeSetupAccount(&r, ops)
	if err != nil || a.SSHRequired() || !a.sshDone {
		t.Fatal("decision replayed", err)
	}
	r.Stage = "ssh"
	ops.existing = func() (string, string, bool, error) { return "1234", "1234", true, nil }
	if _, err := resumeSetupAccount(&r, ops); err == nil {
		t.Fatal("foreign installation admitted")
	}
}
