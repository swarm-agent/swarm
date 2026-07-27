package safefile

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// WriteFile replaces the contents of a regular file in place, preserving its
// inode and ownership when it already exists. It refuses symlinks and verifies
// the opened file identity before truncating to prevent path-swap attacks.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	mode = mode.Perm()
	info, err := os.Lstat(path)
	exists := err == nil
	if exists {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refuse to write non-regular file %q", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect file %q: %w", path, err)
	}

	flags := os.O_WRONLY
	if !exists {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, mode)
	if err != nil {
		return fmt.Errorf("open file %q: %w", path, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()

	openedInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened file %q: %w", path, err)
	}
	if !openedInfo.Mode().IsRegular() || (exists && !os.SameFile(info, openedInfo)) {
		return fmt.Errorf("refuse changed or non-regular file %q", path)
	}
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("set file mode %q: %w", path, err)
	}
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate file %q: %w", path, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek file %q: %w", path, err)
	}
	written, err := file.Write(data)
	if err != nil {
		return fmt.Errorf("write file %q: %w", path, err)
	}
	if written != len(data) {
		return fmt.Errorf("write file %q: %w", path, io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync file %q: %w", path, err)
	}
	currentInfo, err := os.Lstat(path)
	if err != nil || currentInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, currentInfo) {
		if err != nil {
			return fmt.Errorf("verify written file %q: %w", path, err)
		}
		return fmt.Errorf("written file changed identity at %q", path)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close file %q: %w", path, err)
	}
	closed = true
	return nil
}
