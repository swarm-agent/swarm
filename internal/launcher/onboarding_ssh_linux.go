//go:build linux

package launcher

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"
)

const maxAuthorizedKeys = 1 << 20

type sshWriteHooks struct {
	beforeCommit func()
	write        func(*os.File, []byte) (int, error)
}

func installOnboardingSSHKey(u *user.User, key []byte, validate func() error) error {
	uid, e1 := strconv.Atoi(u.Uid)
	gid, e2 := strconv.Atoi(u.Gid)
	if e1 != nil || e2 != nil || uid <= 0 || gid <= 0 {
		return errors.New("invalid non-root SSH owner")
	}
	// User-writable directories must never become a root filesystem write
	// primitive. Use a dedicated locked OS thread with the selected fsuid/fsgid;
	// this does not change process-wide credentials or daemon execution identity.
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		err := func() (err error) {
			oldGID, e := unix.SetfsgidRetGid(-1)
			if e != nil {
				return e
			}
			oldUID, e := unix.SetfsuidRetUid(-1)
			if e != nil {
				return e
			}
			defer func() {
				_, _ = unix.SetfsuidRetUid(oldUID)
				_, _ = unix.SetfsgidRetGid(oldGID)
				gotUID, e1 := unix.SetfsuidRetUid(-1)
				gotGID, e2 := unix.SetfsgidRetGid(-1)
				if e1 == nil && e2 == nil && gotUID == oldUID && gotGID == oldGID {
					runtime.UnlockOSThread()
				} else {
					err = errors.New("SSH filesystem credential restoration failed; isolated thread retired, verify partial key result")
				}
			}()
			_, _ = unix.SetfsgidRetGid(gid)
			_, _ = unix.SetfsuidRetUid(uid)
			gotUID, e1 := unix.SetfsuidRetUid(-1)
			gotGID, e2 := unix.SetfsgidRetGid(-1)
			if e1 != nil || e2 != nil || gotUID != uid || gotGID != gid {
				return errors.New("could not assume account filesystem identity; no key written")
			}
			check := func() error {
				// NSS/install-owner inspection needs the setup authority, not
				// the deliberately restricted filesystem thread.
				checked := make(chan error, 1)
				go func() { checked <- validate() }()
				return <-checked
			}
			return writeOnboardingSSHKey(u, key, check, sshWriteHooks{})
		}()
		result <- err
		// If credential restoration failed this goroutine exits while locked;
		// Go retires the OS thread rather than returning it to the runtime pool.
	}()
	return <-result
}

// All traversal is kernel-constrained (including ancestors), all reads are
// bounded and nonblocking, and existing inodes are never written/chowned. A
// private replacement is synced before atomic publication. Revalidation rejects
// observed directory/file swaps; even an unobserved rename cannot redirect a
// write through a symlink/hardlink into another account's file.
func writeOnboardingSSHKey(u *user.User, key []byte, validate func() error, hooks sshWriteHooks) error {
	uid, e1 := strconv.Atoi(u.Uid)
	gid, e2 := strconv.Atoi(u.Gid)
	if e1 != nil || e2 != nil || uid < 0 || gid < 0 {
		return errors.New("invalid SSH owner")
	}
	openHome := func() (int, error) {
		return unix.Openat2(unix.AT_FDCWD, u.HomeDir, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	}
	home, err := openHome()
	if err != nil {
		return errors.New("cannot safely open account home (Linux openat2 required)")
	}
	defer unix.Close(home)
	var hs unix.Stat_t
	if err = unix.Fstat(home, &hs); err != nil {
		return err
	}
	if hs.Uid != uint32(uid) || hs.Gid != uint32(gid) || hs.Mode&0022 != 0 {
		return errors.New("unsafe account home ownership or permissions")
	}
	if err = validate(); err != nil {
		return err
	}
	made := false
	if err = unix.Mkdirat(home, ".ssh", 0700); err == nil {
		made = true
	} else if err != unix.EEXIST {
		return err
	}
	dir, err := unix.Openat(home, ".ssh", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("unsafe .ssh directory")
	}
	defer unix.Close(dir)
	var ds unix.Stat_t
	if err = unix.Fstat(dir, &ds); err != nil {
		return err
	}
	// A just-created directory belongs to the selected filesystem identity.
	// Never chown an existing unexpected inode, including a mkdir substitution.
	if made {
		if ds.Uid != uint32(uid) || ds.Mode&0777 != 0700 {
			return errors.New(".ssh changed during creation; partial directory retained")
		}
		if err = unix.Fchown(dir, uid, gid); err != nil {
			return err
		}
	} else if ds.Uid != uint32(uid) || ds.Gid != uint32(gid) || ds.Mode&0022 != 0 {
		return errors.New("unsafe .ssh owner or writable permissions")
	}
	if err = unix.Fchmod(dir, 0700); err != nil {
		return err
	}
	readKeys := func() ([]byte, unix.Stat_t, bool, error) {
		fd, err := unix.Openat(dir, "authorized_keys", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == unix.ENOENT {
			return nil, unix.Stat_t{}, false, nil
		}
		if err != nil {
			return nil, unix.Stat_t{}, false, errors.New("unsafe authorized_keys target")
		}
		f := os.NewFile(uintptr(fd), "authorized_keys")
		defer f.Close()
		var st unix.Stat_t
		if err = unix.Fstat(fd, &st); err != nil {
			return nil, st, true, err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Uid != uint32(uid) || st.Gid != uint32(gid) || st.Mode&0022 != 0 || st.Size > maxAuthorizedKeys {
			return nil, st, true, errors.New("unsafe authorized_keys type, links, owner, permissions or size")
		}
		b, err := io.ReadAll(io.LimitReader(f, maxAuthorizedKeys+1))
		if len(b) > maxAuthorizedKeys {
			return nil, st, true, errors.New("authorized_keys exceeds limit")
		}
		return b, st, true, err
	}
	before, original, existed, err := readKeys()
	if err != nil {
		return err
	}
	next := appendOnboardingKey(before, key)
	if len(next) > maxAuthorizedKeys {
		return errors.New("authorized_keys would exceed limit")
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	name := ".swarm-key-" + hex.EncodeToString(random)
	fd, err := unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(dir, name, 0)
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	write := hooks.write
	if write == nil {
		write = func(f *os.File, b []byte) (int, error) { return f.Write(b) }
	}
	n, err := write(f, next)
	if err != nil {
		return err
	}
	if n != len(next) {
		return io.ErrShortWrite
	}
	if err = f.Chown(uid, gid); err != nil {
		return err
	}
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if hooks.beforeCommit != nil {
		hooks.beforeCommit()
	}
	if err = validate(); err != nil {
		return err
	}
	checkHome, err := openHome()
	if err != nil {
		return errors.New("home changed before SSH commit")
	}
	defer unix.Close(checkHome)
	var nowHome, nowDir, temp, opened unix.Stat_t
	if unix.Fstat(checkHome, &nowHome) != nil || nowHome.Dev != hs.Dev || nowHome.Ino != hs.Ino || nowHome.Uid != uint32(uid) || nowHome.Gid != uint32(gid) || nowHome.Mode&0022 != 0 {
		return errors.New("home changed before SSH commit")
	}
	if unix.Fstatat(home, ".ssh", &nowDir, unix.AT_SYMLINK_NOFOLLOW) != nil || nowDir.Dev != ds.Dev || nowDir.Ino != ds.Ino || nowDir.Uid != uint32(uid) || nowDir.Gid != uint32(gid) || nowDir.Mode&0777 != 0700 {
		return errors.New(".ssh changed before SSH commit")
	}
	current, st, exists, err := readKeys()
	if err != nil {
		return err
	}
	if exists != existed || (exists && (st.Dev != original.Dev || st.Ino != original.Ino || st.Ctim != original.Ctim)) || !bytes.Equal(current, before) {
		return errors.New("authorized_keys changed; retry without overwriting concurrent changes")
	}
	if unix.Fstat(fd, &opened) != nil || unix.Fstatat(dir, name, &temp, unix.AT_SYMLINK_NOFOLLOW) != nil || temp.Dev != opened.Dev || temp.Ino != opened.Ino || temp.Nlink != 1 || temp.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("SSH staging file changed")
	}
	if err = unix.Renameat(dir, name, dir, "authorized_keys"); err != nil {
		return err
	}
	if err = unix.Fsync(dir); err != nil {
		return fmt.Errorf("key published but directory sync failed: %w", err)
	}
	// A moved directory does not count as successful installation in the home.
	if unix.Fstatat(checkHome, ".ssh", &nowDir, unix.AT_SYMLINK_NOFOLLOW) != nil || nowDir.Ino != ds.Ino || nowDir.Dev != ds.Dev {
		return errors.New("key published in moved .ssh directory; verify before retry")
	}
	return nil
}
