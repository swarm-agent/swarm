package launcher

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/storagecontract"
)

// Requirement: account selection precedes runtime provisioning, preserves the
// existing owner, requires root for cross-user/create operations, and retains a
// newly created account on OS-password cancellation. The injected NSS/OS command
// boundary proves commands and absence of mutation without touching host users.
func TestInstallationAccountSelection(t *testing.T) {
	for _, scenario := range []string{"root existing", "self", "foreign", "create", "cancel password", "retry", "missing", "root account", "unsafe name", "service name", "existing owner", "owner conflict", "create on reinstall", "storage error", "lookup error", "group conflict", "create failure", "missing passwd", "no terminal", "unsafe home", "symlink home", "orphan home", "unsafe parent", "nonroot create"} {
		t.Run(scenario, func(t *testing.T) {
			name := "developer"
			u := &user.User{Username: name, Uid: "1234", Gid: "1234", HomeDir: "/home/developer"}
			create := scenario == "create" || scenario == "cancel password" || scenario == "group conflict" || scenario == "create failure" || scenario == "missing passwd" || scenario == "no terminal" || scenario == "create on reinstall" || scenario == "orphan home" || scenario == "unsafe parent" || scenario == "nonroot create"
			exists := !create && scenario != "missing"
			if scenario == "create on reinstall" {
				exists = true
			}
			creates, passwords := 0, 0
			euid := 0
			if scenario == "self" {
				euid = 1234
			}
			if scenario == "foreign" || scenario == "nonroot create" {
				euid = 2000
			}
			if scenario == "root account" {
				u.Uid = "0"
			}
			if scenario == "unsafe name" {
				name = "--root"
			}
			if scenario == "service name" {
				name = "swarm"
			}
			ops := installAccountOps{
				euid: euid,
				existing: func() (string, string, bool, error) {
					if scenario == "storage error" {
						return "", "", false, errors.New("conflicting storage")
					}
					if scenario == "existing owner" || scenario == "create on reinstall" {
						return "1234", "1234", true, nil
					}
					if scenario == "owner conflict" {
						return "2000", "2000", true, nil
					}
					return "", "", false, nil
				},
				lookup: func(string) (*user.User, error) {
					if scenario == "lookup error" {
						return nil, errors.New("NSS unavailable")
					}
					if !exists {
						return nil, user.UnknownUserError(name)
					}
					return u, nil
				},
				group: func(string) (*user.Group, error) {
					if scenario == "group conflict" {
						return &user.Group{Gid: "2000"}, nil
					}
					return nil, user.UnknownGroupError(name)
				},
				stat: func(path string) (os.FileInfo, error) {
					if path == "/home" {
						if scenario == "unsafe parent" {
							return ownerInfo{mode: os.ModeDir | 0755, uid: 2000}, nil
						}
						return ownerInfo{mode: os.ModeDir | 0755}, nil
					}
					if path == u.HomeDir && !exists && scenario != "orphan home" {
						return nil, os.ErrNotExist
					}
					if path == "/bin/bash" {
						return ownerInfo{mode: 0755}, nil
					}
					mode := os.ModeDir | 0750
					if scenario == "unsafe home" {
						mode = os.ModeDir | 0777
					}
					if scenario == "symlink home" {
						mode = os.ModeSymlink | 0777
					}
					return ownerInfo{mode: mode, uid: 1234, gid: 1234}, nil
				},
				lookPath: func(command string) (string, error) {
					if scenario == "missing passwd" && command == "passwd" {
						return "", os.ErrNotExist
					}
					return command, nil
				},
				terminal: func() error {
					if scenario == "no terminal" {
						return os.ErrNotExist
					}
					return nil
				},
				create: func(got string) error {
					if got != name {
						t.Fatal("wrong account")
					}
					creates++
					if scenario == "create failure" {
						return errors.New("injected failure")
					}
					exists = true
					return nil
				},
				password: func(got string) error {
					if got != name {
						t.Fatal("wrong password target")
					}
					passwords++
					if scenario == "cancel password" {
						return errors.New("cancelled")
					}
					return nil
				},
			}
			got, err := selectInstallationAccount(name, create, ops)
			ok := scenario == "root existing" || scenario == "self" || scenario == "create" || scenario == "retry" || scenario == "existing owner"
			if (err == nil) != ok {
				t.Fatalf("account=%v err=%v want success=%v", got, err, ok)
			}
			if !ok && got != nil {
				t.Fatal("returned identity on failure")
			}
			wantCreate, wantPassword := 0, 0
			if scenario == "create" || scenario == "cancel password" || scenario == "create failure" {
				wantCreate = 1
			}
			if scenario == "create" || scenario == "cancel password" {
				wantPassword = 1
			}
			if creates != wantCreate || passwords != wantPassword {
				t.Fatalf("commands create=%d passwd=%d", creates, passwords)
			}
			if scenario == "cancel password" {
				if !exists {
					t.Fatal("cancel removed created account")
				}
				if _, err := selectInstallationAccount(name, true, ops); err == nil {
					t.Fatal("retry recreated or reset existing account")
				}
				if _, err := selectInstallationAccount(name, false, ops); err != nil {
					t.Fatal(err)
				}
				if creates != 1 || passwords != 1 {
					t.Fatal("retry repeated account/password mutation")
				}
			}
		})
	}
}

// Requirement: the selected UID/GID must feed the actual systemd renderer, and
// a later conflicting installation owner must abort rather than reassign. Uses
// isolated absent storage roots and injected selection, never a live unit.
func TestInstallationAccountReachesServiceAndRejectsChangedOwner(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"SWARM_SYSTEM_INSTALL_ROOT", "CONFIGURATION_DIRECTORY", "STATE_DIRECTORY", "CACHE_DIRECTORY", "RUNTIME_DIRECTORY", "LOGS_DIRECTORY"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	old := selectedInstallAccount
	t.Cleanup(func() { selectedInstallAccount = old })
	selectedInstallAccount = &user.User{Username: "developer", Uid: "1234", Gid: "1234", HomeDir: "/home/developer"}
	uid, gid := installOwnerIDs()
	if uid != "1234" || gid != "1234" {
		t.Fatalf("selected identity lost: %s:%s", uid, gid)
	}
	unit := renderSystemdServiceUnit(storagecontract.Roots{})
	if !strings.Contains(unit, "\nUser=1234\nGroup=1234\n") {
		t.Fatal("service does not use intended identity")
	}
	// An owned directory appearing between selection and provisioning cannot
	// override the explicit selection. Use this process's real temp ownership.
	if err := os.Mkdir(os.Getenv("STATE_DIRECTORY"), 0700); err != nil {
		t.Fatal(err)
	}
	selectedInstallAccount = &user.User{Uid: "2147483646", Gid: "2147483646"}
	if err := prepareInstallOwner(); err == nil {
		t.Fatal("accepted changed owner")
	}
	if _, err := os.Stat(os.Getenv("SWARM_SYSTEM_INSTALL_ROOT")); !os.IsNotExist(err) {
		t.Fatal("provisioned runtime after conflict")
	}
}
