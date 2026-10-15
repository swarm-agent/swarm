package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/appstorage"
)

// A receipt is written before rename. Only checkout is recursively deleted;
// the receipt survives interruption until both registration and files are gone.
type removalReceipt struct {
	Source string
	Path   string
}

var removalMu sync.Mutex
var removalInFlight = map[string]<-chan error{}
var removalSlots = make(chan struct{}, 2)

func noSymlinkDirectory(path string) error {
	path = filepath.Clean(path)
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe removal directory %q", p)
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

func removeManagedWorktree(source, path string) error {
	removalMu.Lock()
	defer removalMu.Unlock()
	root, err := worktreeCacheRoot(source)
	if err != nil {
		return err
	}
	path = filepath.Clean(path)
	listing, err := runGit(source, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	for _, wt := range parseWorktreeList(listing) {
		if sameCleanPath(wt.Path, path) && wt.Locked {
			return errors.New("refuse removal of locked worktree")
		}
	}
	if filepath.Dir(path) != root {
		return errors.New("removal requires a direct managed checkout")
	}
	if err := noSymlinkDirectory(root); err != nil {
		return err
	}
	trash := filepath.Join(root, ".trash")
	if err := os.Mkdir(trash, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := noSymlinkDirectory(trash); err != nil {
		return err
	}
	// Deterministic receipts make retries and restart recovery idempotent.
	entry := filepath.Join(trash, filepath.Base(path))
	if err := os.Mkdir(entry, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := noSymlinkDirectory(entry); err != nil {
		return err
	}
	receipt := removalReceipt{source, path}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	receiptPath := filepath.Join(entry, "receipt.json")
	if info, err := os.Lstat(receiptPath); err == nil && !info.Mode().IsRegular() {
		return errors.New("invalid trash receipt")
	}
	if old, err := os.ReadFile(receiptPath); err == nil {
		if string(old) != string(data) {
			return errors.New("trash receipt identity mismatch")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(receiptPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
	} else {
		return err
	}
	return finishRemoval(entry, receipt)
}

func finishRemoval(entry string, receipt removalReceipt) error {
	return finishRemovalWithWait(entry, receipt, 2*time.Minute)
}

func finishRemovalWithWait(entry string, receipt removalReceipt, wait time.Duration) error {
	if err := noSymlinkDirectory(entry); err != nil {
		return err
	}
	checkout := filepath.Join(entry, "checkout")
	if err := noSymlinkDirectory(filepath.Dir(receipt.Path)); err != nil {
		return err
	}
	marker := filepath.Join(entry, "renamed")
	_, markerErr := os.Lstat(marker)
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return markerErr
	}
	if info, err := os.Lstat(receipt.Path); err == nil {
		if markerErr == nil {
			return errors.New("refuse removal of a replacement checkout")
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("checkout must be a directory, not a symlink")
		}
		if _, err := os.Lstat(checkout); !errors.Is(err, os.ErrNotExist) {
			return errors.New("both live and trash checkout exist")
		}
		if err := os.Rename(receipt.Path, checkout); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(markerErr, os.ErrNotExist) {
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			return err
		}
	}
	if err := removeWorktreeRegistration(receipt.Source, receipt.Path); err != nil {
		return err
	}
	// Go removal never runs under the Git command deadline. If the caller's
	// generous wait expires, deletion continues and the receipt remains retryable.
	done := removalInFlight[entry]
	if done == nil {
		select {
		case removalSlots <- struct{}{}:
		default:
			return errors.New("trash deletion queue busy")
		}
		anchor, err := os.OpenRoot(entry)
		if err != nil {
			<-removalSlots
			return err
		}
		result := make(chan error, 1)
		done = result
		removalInFlight[entry] = done
		go func() { defer func() { <-removalSlots; anchor.Close() }(); result <- anchor.RemoveAll("checkout") }()
	}
	select {
	case err := <-done:
		delete(removalInFlight, entry)
		if err != nil {
			return err
		}
		return os.RemoveAll(entry)
	case <-time.After(wait):
		return errors.New("trash deletion continuing in background")
	}
}

func removeWorktreeRegistration(source, path string) error {
	common, err := runGit(source, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	adminRoot := filepath.Join(strings.TrimSpace(common), "worktrees")
	if err := noSymlinkDirectory(adminRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	entries, err := os.ReadDir(adminRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		admin := filepath.Join(adminRoot, entry.Name())
		pointer := filepath.Join(admin, "gitdir")
		info, err := os.Lstat(pointer)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("invalid worktree admin pointer")
		}
		data, err := os.ReadFile(pointer)
		if err != nil {
			return err
		}
		if filepath.Clean(strings.TrimSpace(string(data))) != filepath.Join(path, ".git") {
			continue
		}
		anchor, err := os.OpenRoot(adminRoot)
		if err != nil {
			return err
		}
		err = anchor.RemoveAll(entry.Name())
		anchor.Close()
		if err != nil {
			return err
		}
	}
	registered, err := worktreePathRegistered(source, path)
	if err != nil {
		return err
	}
	if registered {
		return errors.New("checkout remains registered")
	}
	return nil
}

// ResumeTrash runs once at service construction, without touching live lanes or
// pruning unrelated Git entries. Invalid receipts remain for explicit repair.
func ResumeTrash() error {
	root, err := appstorage.WorktreesRoot()
	if err != nil {
		return err
	}
	return resumeTrashRoot(root)
}

func resumeTrashRoot(root string) error {
	removalMu.Lock()
	defer removalMu.Unlock()
	if err := noSymlinkDirectory(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	repos, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var failures []error
	for _, repo := range repos {
		if !repo.IsDir() || repo.Type()&os.ModeSymlink != 0 {
			continue
		}
		trash := filepath.Join(root, repo.Name(), ".trash")
		if err := noSymlinkDirectory(trash); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			failures = append(failures, err)
			continue
		}
		entries, err := os.ReadDir(trash)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			dir := filepath.Join(trash, entry.Name())
			pointer := filepath.Join(dir, "receipt.json")
			info, statErr := os.Lstat(pointer)
			if statErr != nil || !info.Mode().IsRegular() {
				failures = append(failures, errors.New("invalid trash receipt"))
				continue
			}
			data, err := os.ReadFile(pointer)
			var receipt removalReceipt
			if err == nil {
				err = json.Unmarshal(data, &receipt)
			}
			if err != nil {
				failures = append(failures, err)
				continue
			}
			expected, err := worktreeCacheRoot(receipt.Source)
			if err != nil || expected != filepath.Dir(trash) || receipt.Path != filepath.Join(expected, entry.Name()) {
				failures = append(failures, errors.New("invalid trash receipt scope"))
				continue
			}
			if err := finishRemoval(dir, receipt); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}
