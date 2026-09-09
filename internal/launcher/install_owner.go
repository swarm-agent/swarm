package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const serviceAccountName = "swarm"
const serviceAccountHome = "/var/lib/swarm"

// Account creation is install-only. Runtime and service rendering only resolve
// identities; they must never provision accounts as a side effect.
func prepareInstallOwner() error {
	if os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || os.Getenv("SUDO_GID") != "" {
		return validateInstallOwner()
	}
	if err := provisionServiceAccount(user.Lookup, user.LookupGroup, runPrivilegedCommand, os.Lstat); err != nil {
		return err
	}
	account, err := user.Lookup(serviceAccountName)
	if err != nil {
		return err
	}
	groups, err := account.GroupIds()
	if err != nil {
		return fmt.Errorf("resolve service memberships: %w", err)
	}
	status, err := exec.Command("passwd", "-S", serviceAccountName).Output()
	if err != nil {
		return fmt.Errorf("verify locked service password: %w", err)
	}
	passwd, err := exec.Command("getent", "passwd", serviceAccountName).Output()
	if err != nil {
		return fmt.Errorf("verify service login shell: %w", err)
	}
	if err := validateServiceLogin(account, groups, string(status), string(passwd)); err != nil {
		return err
	}
	return validateInstallOwner()
}

func provisionServiceAccount(lookup func(string) (*user.User, error), group func(string) (*user.Group, error), run func(...string) error, stat func(string) (os.FileInfo, error)) error {
	for _, parent := range []string{"/var", "/var/lib"} {
		info, err := stat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("unsafe service home parent %s", parent)
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
			return fmt.Errorf("service home parent must be root-owned: %s", parent)
		}
	}
	account, err := lookup(serviceAccountName)
	if err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return fmt.Errorf("resolve service account: %w", err)
		}
		if _, err := stat(serviceAccountHome); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("refusing existing or inaccessible service home %s", serviceAccountHome)
		}
		if _, err := group(serviceAccountName); err == nil {
			return errors.New("refusing orphan existing swarm group")
		} else {
			var unknownGroup user.UnknownGroupError
			if !errors.As(err, &unknownGroup) {
				return fmt.Errorf("resolve service group: %w", err)
			}
		}
		// useradd creates a locked-password account and its private primary group.
		// No supplementary groups, sudo policy, or existing account is modified.
		if err := run("useradd", "--system", "--user-group", "--create-home", "--home-dir", serviceAccountHome, "--shell", "/usr/sbin/nologin", serviceAccountName); err != nil {
			return fmt.Errorf("create locked Swarm service account (requires useradd): %w", err)
		}
		account, err = lookup(serviceAccountName)
		if err != nil {
			return fmt.Errorf("verify created service account: %w", err)
		}
	}
	g, err := group(serviceAccountName)
	if err != nil {
		return fmt.Errorf("verify service group: %w", err)
	}
	if err := validateServiceAccount(account, g); err != nil {
		return err
	}
	info, err := stat(serviceAccountHome)
	if err != nil {
		return fmt.Errorf("verify service home: %w", err)
	}
	if err := validateOwnedDirectory(serviceAccountHome, info, account.Uid, account.Gid); err != nil {
		return err
	}
	return nil
}

func validateServiceAccount(u *user.User, g *user.Group) error {
	if u == nil || g == nil {
		return errors.New("missing service account or group")
	}
	uid, e1 := strconv.Atoi(u.Uid)
	gid, e2 := strconv.Atoi(u.Gid)
	if e1 != nil || e2 != nil || uid <= 0 || gid <= 0 || u.Username != serviceAccountName || g.Name != serviceAccountName || g.Gid != u.Gid || u.HomeDir != serviceAccountHome {
		return errors.New("refusing unsafe existing swarm account: require non-root IDs, private primary group, and canonical service home")
	}
	return nil
}

func validateOwnedDirectory(path string, info os.FileInfo, uid, gid string) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || strconv.FormatUint(uint64(st.Uid), 10) != uid || strconv.FormatUint(uint64(st.Gid), 10) != gid || info.Mode().Perm()&0700 != 0700 || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("refusing unsafe owner or mode for directory %q; refusing to change its ownership or mode", path)
	}
	return nil
}

func installOwnerHome() string {
	uid, _ := installOwnerIDs()
	u, err := user.LookupId(uid)
	if err != nil {
		return ""
	}
	return filepath.Clean(u.HomeDir)
}

func validateServiceLogin(account *user.User, groups []string, status, passwd string) error {
	if len(groups) != 1 || groups[0] != account.Gid {
		return errors.New("refusing service account with supplementary groups")
	}
	fields := strings.Fields(status)
	if len(fields) < 2 || fields[0] != serviceAccountName || fields[1] != "L" {
		return errors.New("refusing service account without a locked password")
	}
	entry := strings.Split(strings.TrimSpace(passwd), ":")
	if len(entry) != 7 || entry[0] != serviceAccountName || entry[2] != account.Uid || entry[3] != account.Gid || entry[5] != serviceAccountHome || entry[6] != "/usr/sbin/nologin" {
		return errors.New("refusing unsafe service login record")
	}
	return nil
}
