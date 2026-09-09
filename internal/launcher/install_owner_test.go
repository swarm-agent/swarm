package launcher

import (
	"errors"
	"os"
	"os/user"
	"reflect"
	"syscall"
	"testing"
	"time"
)

// Requirement: provisionServiceAccount must create one locked, non-root identity
// on a fresh root host, reuse only safe state, and stop on partial failure. The
// injected account database/command boundary proves postconditions without host
// account mutation; real useradd/systemd integration remains a distro-gate proof.
func TestProvisionServiceAccount(t *testing.T) {
	for _, scenario := range []string{"fresh", "repeat", "root uid", "wrong group", "wrong home", "orphan group", "existing home", "symlink home", "wrong owner", "writable home", "create failure", "lookup failure", "verification failure"} {
		t.Run(scenario, func(t *testing.T) {
			u := &user.User{Username: "swarm", Uid: "1234", Gid: "1234", HomeDir: serviceAccountHome}
			g := &user.Group{Name: "swarm", Gid: "1234"}
			exists := scenario != "fresh" && scenario != "create failure" && scenario != "verification failure" && scenario != "orphan group" && scenario != "existing home" && scenario != "lookup failure"
			calls := 0
			lookup := func(string) (*user.User, error) {
				if scenario == "lookup failure" {
					return nil, errors.New("database unavailable")
				}
				if !exists {
					return nil, user.UnknownUserError("swarm")
				}
				return u, nil
			}
			group := func(string) (*user.Group, error) {
				if exists || scenario == "orphan group" {
					return g, nil
				}
				return nil, user.UnknownGroupError("swarm")
			}
			stat := func(path string) (os.FileInfo, error) {
				if path != serviceAccountHome {
					return ownerInfo{mode: os.ModeDir | 0755, uid: 0, gid: 0}, nil
				}
				if !exists && scenario != "existing home" {
					return nil, os.ErrNotExist
				}
				mode := os.ModeDir | 0750
				uid := uint32(1234)
				if scenario == "symlink home" {
					mode = os.ModeSymlink | 0777
				}
				if scenario == "wrong owner" {
					uid = 0
				}
				if scenario == "writable home" {
					mode = os.ModeDir | 0777
				}
				return ownerInfo{mode: mode, uid: uid, gid: 1234}, nil
			}
			switch scenario {
			case "root uid":
				u.Uid = "0"
			case "wrong group":
				g.Gid = "0"
			case "wrong home":
				u.HomeDir = "/root"
			}
			run := func(args ...string) error {
				calls++
				want := []string{"useradd", "--system", "--user-group", "--create-home", "--home-dir", serviceAccountHome, "--shell", "/usr/sbin/nologin", serviceAccountName}
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("unsafe account command: %v", args)
				}
				if scenario == "create failure" {
					return errors.New("injected creation failure")
				}
				if scenario != "verification failure" {
					exists = true
				}
				return nil
			}
			err := provisionServiceAccount(lookup, group, run, stat)
			success := scenario == "fresh" || scenario == "repeat"
			if (err == nil) != success {
				t.Fatalf("error=%v, success=%v", err, success)
			}
			wantCalls := 0
			if scenario == "fresh" || scenario == "create failure" || scenario == "verification failure" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want %d", calls, wantCalls)
			}
			if scenario == "fresh" {
				if err := provisionServiceAccount(lookup, group, run, stat); err != nil {
					t.Fatal(err)
				}
				if calls != 1 {
					t.Fatal("reinstall recreated account")
				}
			}
		})
	}
}

type ownerInfo struct {
	mode     os.FileMode
	uid, gid uint32
}

func (ownerInfo) Name() string        { return "home" }
func (ownerInfo) Size() int64         { return 0 }
func (i ownerInfo) Mode() os.FileMode { return i.mode }
func (ownerInfo) ModTime() time.Time  { return time.Time{} }
func (i ownerInfo) IsDir() bool       { return i.mode.IsDir() }
func (i ownerInfo) Sys() any          { return &syscall.Stat_t{Uid: i.uid, Gid: i.gid} }

// Requirement: failed owned-directory provisioning cannot reach later paths.
// The filesystem boundary checks both error and absence of subsequent mutation.
func TestProvisionStopsBeforeLaterDirectory(t *testing.T) {
	root := t.TempDir()
	bad := root + "/file"
	later := root + "/later"
	if err := os.WriteFile(bad, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureDirsPrivileged([]systemDirSpec{{Path: bad, Owner: true}, {Path: later, Owner: true}}); err == nil {
		t.Fatal("accepted file")
	}
	if _, err := os.Stat(later); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created later directory")
	}
}

// Requirement: sudo retains its caller, direct root resolves the provisioned
// account, and unprivileged environment spoofing cannot select another owner.
func TestResolveInstallOwnerIDs(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		euid, uid, gid           int
		su, sg, wantUID, wantGID string
	}{
		{"direct root", 0, 0, 0, "", "", "1234", "1234"},
		{"sudo", 0, 0, 0, "2000", "2001", "2000", "2001"},
		{"partial sudo", 0, 0, 0, "2000", "", "", ""},
		{"nonroot spoof", 2000, 2000, 2001, "0", "0", "2000", "2001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uid, gid := resolveInstallOwnerIDs(tc.euid, tc.uid, tc.gid, tc.su, tc.sg, func(string) (*user.User, error) { return &user.User{Uid: "1234", Gid: "1234"}, nil })
			if uid != tc.wantUID || gid != tc.wantGID {
				t.Fatalf("got %s:%s want %s:%s", uid, gid, tc.wantUID, tc.wantGID)
			}
		})
	}
}

// Requirement: reused service accounts must not gain interactive login or
// supplementary privilege. Validate the exact account-record boundary.
func TestServiceLoginRejectsUnsafeReuse(t *testing.T) {
	u := &user.User{Uid: "1234", Gid: "1234"}
	record := "swarm:x:1234:1234::/var/lib/swarm:/usr/sbin/nologin"
	for _, tc := range []struct {
		name, status, record string
		groups               []string
		ok                   bool
	}{
		{"locked", "swarm L", record, []string{"1234"}, true},
		{"unlocked", "swarm P", record, []string{"1234"}, false},
		{"supplementary", "swarm L", record, []string{"1234", "0"}, false},
		{"interactive", "swarm L", "swarm:x:1234:1234::/var/lib/swarm:/bin/sh", []string{"1234"}, false},
		{"mismatch", "swarm L", "swarm:x:0:1234::/var/lib/swarm:/usr/sbin/nologin", []string{"1234"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateServiceLogin(u, tc.groups, tc.status, tc.record); (err == nil) != tc.ok {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
