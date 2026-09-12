package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
)

// selectedInstallAccount is scoped to the synchronous setup command. Runtime
// resolution never reads an environment override for this privileged choice.
var selectedInstallAccount *user.User

var installAccountName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

type installAccountOps struct {
	euid            int
	existing        func() (string, string, bool, error)
	lookup          func(string) (*user.User, error)
	group           func(string) (*user.Group, error)
	stat            func(string) (os.FileInfo, error)
	lookPath        func(string) (string, error)
	create          func(string) error
	password        func(string) error
	terminal        func() error
	passwordCommand string // empty keeps the explicit CLI passwd workflow
}

// SelectInstallationAccount runs before runtime provisioning. Creating a human
// account is an explicit privileged operation, not authentication to Swarm.
// Passwords belong exclusively to passwd on the controlling OS terminal.
func SelectInstallationAccount(name string, create bool) (func(), error) {
	u, err := selectInstallationAccount(name, create, defaultInstallAccountOps())
	if err != nil {
		return nil, err
	}
	return selectAccountForInstallation(u), nil
}

func defaultInstallAccountOps() installAccountOps {
	return installAccountOps{
		euid: os.Geteuid(), existing: existingInstallOwner, lookup: user.Lookup,
		group: user.LookupGroup, stat: os.Lstat, lookPath: exec.LookPath,
		terminal: func() error {
			tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
			if err != nil {
				return err
			}
			return tty.Close()
		},
		create: func(name string) error {
			return runPrivilegedCommand("useradd", "--create-home", "--user-group", "--home-dir", filepath.Join("/home", name), "--shell", "/bin/bash", "--", name)
		},
		password: func(name string) error {
			tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
			if err != nil {
				return fmt.Errorf("password setup requires a controlling terminal: %w", err)
			}
			defer tty.Close()
			cmd := exec.Command("passwd", "--", name)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
			return cmd.Run()
		},
	}
}

func selectAccountForInstallation(u *user.User) func() {
	previous := selectedInstallAccount
	selectedInstallAccount = u
	return func() { selectedInstallAccount = previous }
}

func selectInstallationAccount(name string, create bool, ops installAccountOps) (*user.User, error) {
	if !installAccountName.MatchString(name) || name == "root" || name == serviceAccountName {
		return nil, errors.New("choose a non-root human account name (not the reserved swarm service account)")
	}
	uid, gid, found, err := ops.existing()
	if err != nil {
		return nil, err
	}
	u, lookupErr := ops.lookup(name)
	var unknown user.UnknownUserError
	missing := errors.As(lookupErr, &unknown)
	if lookupErr != nil && !missing {
		return nil, fmt.Errorf("resolve intended account: %w", lookupErr)
	}
	if found {
		if missing || u == nil || u.Uid != uid || u.Gid != gid || create {
			return nil, errors.New("existing installation belongs to another identity or is already provisioned; no account or ownership changes made; rerun without account creation using the existing owner")
		}
	} else if create {
		if ops.euid != 0 {
			return nil, errors.New("creating an installation account requires explicit root execution")
		}
		if !missing {
			return nil, errors.New("account already exists; complete any interrupted OS password setup with passwd, then retry with --install-user instead of --create-user; account left unchanged")
		}
		if _, err := ops.group(name); err == nil {
			return nil, errors.New("refusing existing group; no account created")
		} else {
			var unknownGroup user.UnknownGroupError
			if !errors.As(err, &unknownGroup) {
				return nil, fmt.Errorf("resolve intended group: %w", err)
			}
		}
		if info, err := ops.stat("/home"); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return nil, errors.New("new account requires a safe /home directory; no account created")
		} else if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
			return nil, errors.New("new account requires root-owned /home; no account created")
		}
		if _, err := ops.stat(filepath.Join("/home", name)); !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("refusing existing or inaccessible new account home; no account created")
		}
		commands := []string{"useradd"}
		if ops.password != nil {
			command := ops.passwordCommand
			if command == "" {
				command = "passwd"
			}
			commands = append(commands, command)
		}
		for _, command := range commands {
			if _, err := ops.lookPath(command); err != nil {
				return nil, fmt.Errorf("account prerequisite %s unavailable: %w", command, err)
			}
		}
		if info, err := ops.stat("/bin/bash"); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return nil, errors.New("new account requires executable /bin/bash")
		}
		// Check terminal availability before useradd, not after leaving an account.
		if ops.terminal != nil {
			if err := ops.terminal(); err != nil {
				return nil, fmt.Errorf("account creation requires an OS terminal: %w", err)
			}
		}
		if err := ops.create(name); err != nil {
			return nil, fmt.Errorf("OS account creation failed; any partial account/home is retained, runtime not provisioned: %w", err)
		}
		u, err = ops.lookup(name)
		if err != nil {
			return nil, fmt.Errorf("verify created account; runtime not provisioned: %w", err)
		}
		if u == nil || u.Username != name {
			return nil, errors.New("created account lookup identity mismatch")
		}
		if err := validateSelectedAccount(u, ops.stat); err != nil {
			return nil, err
		}
		if ops.password != nil {
			if err := ops.password(name); err != nil {
				return nil, fmt.Errorf("password setup cancelled or failed; account retained, runtime not provisioned; finish passwd for the account and retry --install-user: %w", err)
			}
		}
	} else if missing {
		return nil, errors.New("intended account does not exist; use --create-user with an OS terminal or select an existing user")
	}
	if u == nil || u.Username != name {
		return nil, errors.New("account lookup identity mismatch")
	}
	if ops.euid != 0 && u.Uid != strconv.Itoa(ops.euid) {
		return nil, errors.New("only root may select another installation account")
	}
	if err := validateSelectedAccount(u, ops.stat); err != nil {
		return nil, err
	}
	return u, nil
}

func validateSelectedAccount(u *user.User, stat func(string) (os.FileInfo, error)) error {
	if u == nil {
		return errors.New("missing installation account")
	}
	uid, e1 := strconv.Atoi(u.Uid)
	gid, e2 := strconv.Atoi(u.Gid)
	if e1 != nil || e2 != nil || uid <= 0 || gid <= 0 || !safeInstallOwnerHome(u.HomeDir) {
		return errors.New("installation account requires non-root IDs and a safe absolute home")
	}
	info, err := stat(filepath.Clean(u.HomeDir))
	if err != nil {
		return fmt.Errorf("inspect intended account home: %w", err)
	}
	return validateOwnedDirectory(u.HomeDir, info, u.Uid, u.Gid)
}
