package tool

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"swarm/packages/swarmd/internal/taskscope"
)

// MaterializeRecoverySource is allocation-only: destination must be a fresh,
// unpublished replacement lane. The allocator must discard the entire lane on
// any error. This is not an arbitrary destination tool or a filesystem grant.
func MaterializeRecoverySource(destination string, source RecoverySource, scopes []string) error {
	if len(scopes) == 0 {
		return errors.New("recovery requires explicit owned scope")
	}
	canonical := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		name, whole, err := taskscope.Canonical(scope)
		if err != nil {
			return err
		}
		if whole {
			return errors.New("recovery requires narrow owned scope")
		}
		canonical = append(canonical, name)
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	// Preflight every path before writing even the first file. Reject destination
	// symlinks, directories and special files, including unchanged Git symlinks.
	for _, file := range source.Files {
		allowed := false
		for _, scope := range canonical {
			if file.Path == scope || strings.HasPrefix(file.Path, scope+"/") {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("recovery path %q is outside replacement owned scope", file.Path)
		}
		if _, err := readRecoverySourceFile(root, file.Path, recoverySourceMaxBytes); err != nil {
			return err
		}
	}
	for _, file := range source.Files {
		if err := root.Remove(file.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
		if file.Deleted {
			continue
		}
		if err := root.MkdirAll(path.Dir(file.Path), 0755); err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if file.Executable {
			mode = 0755
		}
		out, err := root.OpenFile(file.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, writeErr := out.WriteString(file.Content)
		chmodErr := out.Chmod(mode)
		closeErr := out.Close()
		if err := errors.Join(writeErr, chmodErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}
