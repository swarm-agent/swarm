//go:build linux

package tool

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Walk from the already-open workspace root with one no-follow directory open
// per component. Pathname prechecks alone allow Bash to swap an in-root symlink
// into .git between validation and a daemon file write. Anchored directory fds
// keep mutations at the checked destination without following such aliases.
func (p *rootedWorkspacePath) openLocalMutationParent(create bool) (*os.File, string, error) {
	parts := strings.Split(filepath.Clean(p.relative), string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || part == ".git" {
			return nil, "", errors.New("mutation rejected: invalid path or Git administration component")
		}
	}
	dir, err := p.root.Open(".")
	if err != nil {
		return nil, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		fd, openErr := openNoFollowDirAt(int(dir.Fd()), part)
		if errors.Is(openErr, unix.ENOENT) && create {
			mkdirErr := unix.Mkdirat(int(dir.Fd()), part, uint32(sharedDirMode.Perm()))
			if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				dir.Close()
				return nil, "", mkdirErr
			}
			fd, openErr = openNoFollowDirAt(int(dir.Fd()), part)
		}
		dir.Close()
		if openErr != nil {
			return nil, "", openErr
		}
		dir = os.NewFile(uintptr(fd), part)
	}
	return dir, parts[len(parts)-1], nil
}

// openNoFollowDirAt opens one directory component without following a
// symlink. Some runtimes (gVisor) follow a symlinked final component despite
// O_NOFOLLOW when O_DIRECTORY is set, so the opened directory must also be the
// very non-symlink entry named in its parent (same device and inode); a
// followed symlink, or an entry swapped in between, is refused with ELOOP.
func openNoFollowDirAt(dirfd int, name string) (int, error) {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	var entry, opened unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if err := unix.Fstat(fd, &opened); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if entry.Mode&unix.S_IFMT != unix.S_IFDIR || entry.Dev != opened.Dev || entry.Ino != opened.Ino {
		unix.Close(fd)
		return -1, unix.ELOOP
	}
	return fd, nil
}

func (p *rootedWorkspacePath) openLocalMutationFile(flags int, perm fs.FileMode) (*os.File, error) {
	dir, name, err := p.openLocalMutationParent(false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, uint32(perm.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
