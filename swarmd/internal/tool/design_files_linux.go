//go:build linux

package tool

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"swarm/packages/swarmd/internal/design"
)

const designFileMode = 0o644

// designReplaceAfterTempHook is a test seam: it runs after the replacement
// is written and before the target is checked again and renamed.
var designReplaceAfterTempHook func()

func designDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func designNoFollowError(name string, err error) error {
	if errors.Is(err, unix.ELOOP) {
		return fmt.Errorf("design files: %s is a symlink; refusing to follow it", name)
	}
	return err
}

// designOpenRegular opens name in dir without following a symlink and
// requires a regular file with one link.
func designOpenRegular(dir *os.File, name string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, designNoFollowError(name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, fmt.Errorf("design files: %s is not a regular file", name)
	}
	if links, known := fileLinkCount(info); !known || links != 1 {
		file.Close()
		return nil, nil, fmt.Errorf("design files: %s has more than one hard link", name)
	}
	return file, info, nil
}

func designReadAll(file *os.File) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, design.MaxHTMLBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > design.MaxHTMLBytes {
		return nil, errors.New("design files: file is larger than 4 MiB")
	}
	return data, nil
}

func designReadNoFollow(p *rootedWorkspacePath) ([]byte, error) {
	dir, name, err := p.openLocalMutationParent(false)
	if err != nil {
		return nil, designNoFollowError(p.relative, err)
	}
	defer dir.Close()
	file, _, err := designOpenRegular(dir, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return designReadAll(file)
}

// designWriteNew writes data to a new file name in dir (O_EXCL, never
// following a symlink) and syncs it. A partial file is removed.
func designWriteNew(dir *os.File, name string, data []byte, mode os.FileMode) error {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(sharedFileMode(mode)))
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("design files: %s already exists", name)
		}
		return designNoFollowError(name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = unix.Unlinkat(int(dir.Fd()), name, 0)
		return writeErr
	}
	return nil
}

func designCreateExclusive(p *rootedWorkspacePath, data []byte) error {
	dir, name, err := p.openLocalMutationParent(true)
	if err != nil {
		return designNoFollowError(p.relative, err)
	}
	defer dir.Close()
	return designWriteNew(dir, name, data, designFileMode)
}

func designTempName(name string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "." + name + ".swarm-design-" + hex.EncodeToString(b) + ".tmp", nil
}

// designReplace writes data next to the target and renames it over the
// target. With wantSHA256 set the target must exist and still have that
// digest, checked once before writing and again just before the rename;
// otherwise design.ErrStaleBase and nothing changes. Without it (results.json)
// a missing target is created and an existing one must be a regular file.
func designReplace(p *rootedWorkspacePath, wantSHA256 string, data []byte) error {
	dir, name, err := p.openLocalMutationParent(wantSHA256 == "")
	if err != nil {
		return designNoFollowError(p.relative, err)
	}
	defer dir.Close()
	mode := os.FileMode(designFileMode)
	current := func() (string, os.FileInfo, error) {
		file, info, err := designOpenRegular(dir, name)
		if err != nil {
			return "", nil, err
		}
		defer file.Close()
		body, err := designReadAll(file)
		if err != nil {
			return "", nil, err
		}
		return designDigest(body), info, nil
	}
	if wantSHA256 != "" {
		got, info, err := current()
		if err != nil {
			return err
		}
		if got != wantSHA256 {
			return design.ErrStaleBase
		}
		mode = info.Mode().Perm()
	} else {
		var st unix.Stat_t
		switch err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); {
		case errors.Is(err, unix.ENOENT):
		case err != nil:
			return err
		case st.Mode&unix.S_IFMT != unix.S_IFREG:
			return fmt.Errorf("design files: %s is not a regular file", name)
		}
	}
	tmp, err := designTempName(name)
	if err != nil {
		return err
	}
	if err := designWriteNew(dir, tmp, data, mode); err != nil {
		return err
	}
	removeTmp := func() { _ = unix.Unlinkat(int(dir.Fd()), tmp, 0) }
	if designReplaceAfterTempHook != nil {
		designReplaceAfterTempHook()
	}
	if wantSHA256 != "" {
		got, _, err := current()
		if err != nil {
			removeTmp()
			return err
		}
		if got != wantSHA256 {
			removeTmp()
			return design.ErrStaleBase
		}
	}
	if err := unix.Renameat(int(dir.Fd()), tmp, int(dir.Fd()), name); err != nil {
		removeTmp()
		return err
	}
	return dir.Sync()
}
