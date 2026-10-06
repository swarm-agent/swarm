package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const buildOwnershipFile = "build-owner.json"

// The short digest is a locator, never authority. Receipts bind the full
// operation and account-scoped build root to the exact allocated directories.
// No recipe/command output is retained here.
type buildOwnership struct {
	Operation  string `json:"operation"`
	BuildRoot  string `json:"build_root"`
	Runroot    string `json:"runroot"`
	RootDevice uint64 `json:"root_device"`
	RootInode  uint64 `json:"root_inode"`
	RunDevice  uint64 `json:"run_device"`
	RunInode   uint64 `json:"run_inode"`
}

func privateBuildDirectory(info os.FileInfo) bool {
	if info == nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func buildDirectoryIdentity(path string) (uint64, uint64, error) {
	info, err := os.Lstat(path)
	if err != nil || !privateBuildDirectory(info) {
		return 0, 0, errors.New("build ownership directory unavailable or unsafe")
	}
	stat := info.Sys().(*syscall.Stat_t)
	return uint64(stat.Dev), uint64(stat.Ino), nil
}

func (p *LocalDockerProvider) publishBuildOwnership(id, runroot string) error {
	root, err := p.buildDirectory(id)
	if err != nil {
		return err
	}
	expected, err := p.buildRunroot(id)
	if err != nil || runroot != expected {
		return errors.New("build ownership path mismatch")
	}
	owner := buildOwnership{Operation: id, BuildRoot: p.buildRoot, Runroot: runroot}
	owner.RootDevice, owner.RootInode, err = buildDirectoryIdentity(root)
	if err != nil {
		return err
	}
	owner.RunDevice, owner.RunInode, err = buildDirectoryIdentity(runroot)
	if err != nil {
		return err
	}
	data, err := json.Marshal(owner)
	if err != nil || len(data) > 4096 {
		return errors.New("build ownership receipt exceeds bound")
	}
	// Each exclusive, fsynced receipt must be complete before engines start.
	// If publication is interrupted, allocation-intent and any written receipt
	// remain for private operator recovery; neither alone authorizes deletion.
	for _, dir := range []string{runroot, root} {
		file, err := os.OpenFile(filepath.Join(dir, buildOwnershipFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("build ownership publication failed")
		}
		_, writeErr := file.Write(data)
		syncErr := file.Sync()
		closeErr := file.Close()
		if errors.Join(writeErr, syncErr, closeErr) != nil {
			return errors.New("build ownership publication unconfirmed")
		}
		directory, err := os.Open(dir)
		if err != nil {
			return errors.New("build ownership directory publication unavailable")
		}
		syncErr = directory.Sync()
		closeErr = directory.Close()
		if errors.Join(syncErr, closeErr) != nil {
			return errors.New("build ownership directory publication unconfirmed")
		}
	}
	return nil
}

func readBuildOwnership(path string) (buildOwnership, error) {
	var owner buildOwnership
	// O_NOFOLLOW and O_NONBLOCK reject symlink/FIFO receipts without following
	// attacker content or blocking cleanup. Only private regular bounded bytes.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return owner, errors.New("build ownership receipt unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
		return owner, errors.New("build ownership receipt unsafe or oversized")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return owner, errors.New("build ownership receipt owner mismatch")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return owner, errors.New("build ownership receipt read failed")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&owner) != nil || decoder.Decode(new(any)) != io.EOF {
		return owner, errors.New("build ownership receipt invalid")
	}
	return owner, nil
}

func (p *LocalDockerProvider) verifyBuildOwnership(id string) (buildOwnership, error) {
	var empty buildOwnership
	root, err := p.buildDirectory(id)
	if err != nil {
		return empty, err
	}
	runroot, err := p.buildRunroot(id)
	if err != nil {
		return empty, err
	}
	device, inode, err := buildDirectoryIdentity(root)
	if err != nil {
		return empty, err
	}
	owner, err := readBuildOwnership(filepath.Join(root, buildOwnershipFile))
	if err != nil {
		return empty, err
	}
	if owner.Operation != id || owner.BuildRoot != p.buildRoot || owner.Runroot != runroot || owner.RootDevice != device || owner.RootInode != inode {
		return empty, errors.New("build scratch ownership mismatch; resources retained")
	}
	// Idempotent retry after runroot removal requires the still-valid scratch
	// receipt. A present runroot must match its own receipt AND directory ID.
	if _, err := os.Lstat(runroot); errors.Is(err, os.ErrNotExist) {
		return owner, nil
	}
	device, inode, err = buildDirectoryIdentity(runroot)
	if err != nil {
		return empty, err
	}
	runtimeOwner, err := readBuildOwnership(filepath.Join(runroot, buildOwnershipFile))
	if err != nil {
		return empty, err
	}
	if runtimeOwner != owner || owner.RunDevice != device || owner.RunInode != inode {
		return empty, errors.New("build runroot ownership mismatch; resources retained")
	}
	return owner, nil
}

// Remove children before receipts so any partial failure keeps recovery evidence.
func removeBuildScratch(root string) error {
	file, err := os.Open(root)
	if err != nil {
		return errors.New("build scratch cleanup inventory unavailable")
	}
	entries, readErr := file.ReadDir(33)
	_ = file.Close()
	if (readErr != nil && readErr != io.EOF) || len(entries) > 32 {
		return errors.New("build scratch cleanup inventory invalid")
	}
	for _, entry := range entries {
		if entry.Name() == buildOwnershipFile || entry.Name() == "allocation-intent" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return errors.New("build scratch removal unconfirmed; ownership retained")
		}
	}
	// At this point no runtime resources or scratch children remain. Keep a
	// bounded receipt in memory to restore recovery evidence if final rmdir
	// fails (for example, a new child appeared). Delete ownership last.
	owner, err := readBuildOwnership(filepath.Join(root, buildOwnershipFile))
	if err != nil {
		return err
	}
	for _, name := range []string{"allocation-intent", buildOwnershipFile} {
		if err := os.Remove(filepath.Join(root, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("build scratch receipt cleanup unconfirmed")
		}
	}
	if err := os.Remove(root); err != nil {
		data, marshalErr := json.Marshal(owner)
		if marshalErr != nil {
			return errors.New("build scratch cleanup unconfirmed; receipt recovery failed")
		}
		file, openErr := os.OpenFile(filepath.Join(root, buildOwnershipFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			return errors.New("build scratch cleanup unconfirmed; receipt recovery failed")
		}
		_, writeErr := file.Write(data)
		syncErr := file.Sync()
		closeErr := file.Close()
		if errors.Join(writeErr, syncErr, closeErr) != nil {
			return errors.New("build scratch cleanup unconfirmed; receipt recovery failed")
		}
		return errors.New("build scratch directory cleanup unconfirmed; ownership retained")
	}
	return nil
}

// Cleanup may retry after successful runtime removal, but engine commands must
// never recreate a missing runroot using the scratch receipt alone.
func (p *LocalDockerProvider) verifyActiveBuildOwnership(id string) (buildOwnership, error) {
	owner, err := p.verifyBuildOwnership(id)
	if err != nil {
		return buildOwnership{}, err
	}
	device, inode, err := buildDirectoryIdentity(owner.Runroot)
	if err != nil || device != owner.RunDevice || inode != owner.RunInode {
		return buildOwnership{}, errors.New("active build runroot ownership unavailable")
	}
	return owner, nil
}

func writeBuildAllocationIntent(root, intent string) error {
	if len(intent) > 4096 {
		return errors.New("build allocation intent exceeds bound")
	}
	file, err := os.OpenFile(filepath.Join(root, "allocation-intent"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("build allocation intent unavailable")
	}
	_, writeErr := file.WriteString(intent)
	syncErr := file.Sync()
	closeErr := file.Close()
	if errors.Join(writeErr, syncErr, closeErr) != nil {
		return errors.New("build allocation intent publication unconfirmed")
	}
	// Publish the scratch entry and intent before allocating in another tree.
	for _, dir := range []string{root, filepath.Dir(root)} {
		directory, err := os.Open(dir)
		if err != nil {
			return errors.New("build allocation directory unavailable")
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if errors.Join(syncErr, closeErr) != nil {
			return errors.New("build allocation directory publication unconfirmed")
		}
	}
	return nil
}
