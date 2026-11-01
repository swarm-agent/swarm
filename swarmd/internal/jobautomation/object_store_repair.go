package jobautomation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// ObjectStoreRepair is what one pass of RepairObjectStore found in a
// folder's Git object store: how many entries it checked, how many it made
// readable again (mode bits and group of the folder's objects directory) and
// how many it could not (owned by another user, or a refused chown/chmod),
// with the first of those as Sample.
//
// Why it exists: Swarm's Git (the daemon's own, agent leases' and their
// rebases) writes into a person's folder under a restrictive umask (the
// service's UMask, the sandbox's 0007) and without setgid (the daemon's
// RestrictSUIDSGID refuses it), so loose objects can land mode 0440 and new
// fan-out directories in the daemon's own group. A step lease that reads
// the store as another user then cannot open them. The repair gives every
// entry the read bits the folder's objects directory grants, and the
// folder's group.
type ObjectStoreRepair struct {
	Folder     string `json:"folder"`
	Store      string `json:"store,omitempty"`
	Checked    int    `json:"checked"`
	Repaired   int    `json:"repaired"`
	Unrepaired int    `json:"unrepaired"`
	// SetgidRefused counts fan-out directories whose setgid bit could not
	// be set (the daemon's RestrictSUIDSGID); their group was still fixed.
	SetgidRefused int    `json:"setgid_refused,omitempty"`
	Sample        string `json:"sample,omitempty"`
	Error         string `json:"error,omitempty"`
	At            int64  `json:"at"`
}

// Problem reports whether the repair left entries a step lease cannot read
// (or could not inspect the store at all).
func (r ObjectStoreRepair) Problem() bool { return r.Unrepaired > 0 || r.Error != "" }

// Message is the health line for a repair that left unreadable entries.
func (r ObjectStoreRepair) Message() string {
	if r.Error != "" {
		return "folder repository " + r.Folder + ": the object store permission repair failed: " + r.Error
	}
	msg := fmt.Sprintf("folder repository %s has %d object store entries run steps cannot read that Swarm could not repair", r.Folder, r.Unrepaired)
	if r.Sample != "" {
		msg += " (" + r.Sample + ")"
	}
	return msg
}

// folderObjectStore is the object directory of folder's own repository
// (folder/.git/objects), or "" when folder has no .git directory of its own
// (not a repository, or a linked worktree whose store is elsewhere).
func folderObjectStore(folder string) string {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(folder), ".git", "objects")
}

func objectStoreRepairKey(folder string) string {
	return keyObjectStoreRepairPrefix + hexKey(filepath.Clean(folder))
}

// RecordObjectStoreRepair keeps folder's last repair result when it changed
// something or left a problem; a pass that found every entry readable
// clears it.
func (s *Store) RecordObjectStoreRepair(r ObjectStoreRepair) error {
	folder := strings.TrimSpace(r.Folder)
	if folder == "" {
		return nil
	}
	if r.Repaired == 0 && !r.Problem() {
		return s.db.Delete(objectStoreRepairKey(folder))
	}
	r.Folder = filepath.Clean(folder)
	if r.At == 0 {
		r.At = s.now().UnixMilli()
	}
	return s.db.PutJSON(objectStoreRepairKey(folder), r)
}

// ObjectStoreRepairOf is folder's recorded repair result, if any.
func (s *Store) ObjectStoreRepairOf(folder string) (ObjectStoreRepair, bool, error) {
	var r ObjectStoreRepair
	found, err := s.db.GetJSON(objectStoreRepairKey(folder), &r)
	return r, found, err
}

// ObjectStoreRepairProblems lists every folder whose last repair left
// entries run steps cannot read, oldest first.
func (s *Store) ObjectStoreRepairProblems() ([]ObjectStoreRepair, error) {
	var out []ObjectStoreRepair
	err := s.db.IteratePrefix(keyObjectStoreRepairPrefix, 0, func(_ string, value []byte) error {
		var r ObjectStoreRepair
		if json.Unmarshal(value, &r) == nil && r.Folder != "" && r.Problem() {
			out = append(out, r)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out, err
}

// RepairFolderObjects repairs each folder's object store (RepairObjectStore)
// and records the result in its repository health. Folders without a .git
// directory of their own are skipped.
func (r *Runner) RepairFolderObjects(folders []string) {
	seen := map[string]bool{}
	for _, folder := range folders {
		folder = filepath.Clean(strings.TrimSpace(folder))
		if folder == "." || seen[folder] {
			continue
		}
		seen[folder] = true
		r.repairFolderObjects(folder)
	}
}

// ObjectStoreRepairInterval is how often the daemon repairs registered
// folders' object stores between environment steps (agents' own leases
// read them too).
const ObjectStoreRepairInterval = 15 * time.Minute

// StartObjectStoreRepair repairs the object stores of folders() now and
// every interval until ctx ends (RepairFolderObjects).
func (r *Runner) StartObjectStoreRepair(ctx context.Context, folders func() []string, interval time.Duration) {
	if r == nil || folders == nil {
		return
	}
	if interval <= 0 {
		interval = ObjectStoreRepairInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			r.RepairFolderObjects(folders())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (r *Runner) repairFolderObjects(folder string) {
	if r == nil || r.store == nil {
		return
	}
	result, ok := RepairObjectStore(folder)
	if !ok {
		return
	}
	result.At = r.now().UnixMilli()
	if result.Repaired > 0 || result.Problem() {
		log.Printf("swarmd folder object store repair: %s: checked %d, made %d readable, %d left unreadable%s", result.Store, result.Checked, result.Repaired, result.Unrepaired, repairNote(result))
	}
	if err := r.store.RecordObjectStoreRepair(result); err != nil {
		log.Printf("swarmd job automations: record the folder object store repair: %v", err)
	}
}

func repairNote(r ObjectStoreRepair) string {
	switch {
	case r.Error != "":
		return ": " + r.Error
	case r.Sample != "":
		return " (" + r.Sample + ")"
	}
	return ""
}

// RepairObjectStore makes every entry of folder's Git object store
// (folder/.git/objects: the loose-object fan-out directories, pack and
// info, and the files in them) readable the way the objects directory
// itself is: each directory gets the read and search bits, each file the
// read bits, that the objects directory grants its group and others, and
// the objects directory's group (what setgid would have given it). A
// fan-out directory also gets setgid when the objects directory has it;
// where that is refused (the daemon's RestrictSUIDSGID) the group and mode
// are still fixed. Only entries this process owns can be changed: others
// that need it are counted Unrepaired. ok is false when folder has no
// object store of its own.
//
// Anyone who can write the store (the folder's owner, its group, agents
// committing as the daemon) can swap an entry for a symbolic link while a
// pass runs. So the walk never resolves a path inside the store: .git,
// objects and each directory below are checked without following links and
// opened with O_NOFOLLOW relative to their parent's descriptor, each file
// the same way, the opened descriptor must be the inode that was checked,
// and its owner, type and link count are checked on that descriptor before
// Fchown/Fchmod change exactly that inode. A link in place of any of them,
// swapped in before or after the check, is left alone; a regular file with
// more than one hard link is never changed (it may be someone else's file
// linked in).
func RepairObjectStore(folder string) (ObjectStoreRepair, bool) {
	store := folderObjectStore(folder)
	result := ObjectStoreRepair{Folder: filepath.Clean(strings.TrimSpace(folder)), Store: store}
	if store == "" {
		return result, false
	}
	folderFD, err := unix.Open(filepath.Dir(filepath.Dir(store)), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return result, false
	}
	defer unix.Close(folderFD)
	gitFD, err := openDirAt(folderFD, ".git", "")
	if err != nil {
		return result, false
	}
	defer unix.Close(gitFD)
	storeFD, err := openDirAt(gitFD, "objects", "")
	if err != nil {
		if missingOrLink(err) {
			return result, false
		}
		result.Error = "open " + store + ": " + err.Error()
		return result, true
	}
	storeDir := os.NewFile(uintptr(storeFD), store)
	defer storeDir.Close()
	var root unix.Stat_t
	if err := unix.Fstat(storeFD, &root); err != nil {
		result.Error = "stat " + store + ": " + err.Error()
		return result, true
	}
	rootMode := fs.FileMode(root.Mode & 0o777)
	p := repairPolicy{
		gid:     int(root.Gid),
		dirBits: rootMode & 0o055,
		setgid:  root.Mode&unix.S_ISGID != 0,
		uid:     os.Geteuid(),
	}
	p.fileBits = p.dirBits & 0o044
	names, err := storeDir.Readdirnames(-1)
	if err != nil {
		result.Error = "read " + store + ": " + err.Error()
		return result, true
	}
	sort.Strings(names)
	for _, name := range names {
		if !(isFanOut(name) || name == "pack" || name == "info") {
			continue
		}
		p.repairDir(&result, storeFD, filepath.Join(store, name), name)
	}
	return result, true
}

// objectStoreRepairSwap, when set (tests only), runs right before the walk
// opens a store directory or file it has listed: the window a writer of the
// store can use to swap the entry for a symbolic link.
var objectStoreRepairSwap func(path string)

// openDirAt opens the directory name inside dirFD (path names it for
// objectStoreRepairSwap) without following a symbolic link in its place:
// the entry must be a directory when checked without following links, the
// open uses O_NOFOLLOW, and the opened descriptor must be that same inode.
// The inode check is what holds where O_NOFOLLOW with O_DIRECTORY still
// follows a link (gVisor does).
func openDirAt(dirFD int, name, path string) (int, error) {
	var pre unix.Stat_t
	if err := unix.Fstatat(dirFD, name, &pre, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return -1, err
	}
	switch pre.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
	case unix.S_IFLNK:
		return -1, unix.ELOOP
	default:
		return -1, unix.ENOTDIR
	}
	if objectStoreRepairSwap != nil && path != "" {
		objectStoreRepairSwap(path)
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, err
	}
	if err := sameInode(fd, pre); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// sameInode reports ELOOP when fd is not the inode pre described: the entry
// was replaced (by a link, or anything else) between the check and the
// open.
func sameInode(fd int, pre unix.Stat_t) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Dev != pre.Dev || st.Ino != pre.Ino {
		return unix.ELOOP
	}
	return nil
}

// missingOrLink reports an open that found nothing to repair: the entry is
// gone (git gc), a symbolic link (O_NOFOLLOW) or not a directory.
func missingOrLink(err error) bool {
	return errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR)
}

type repairPolicy struct {
	gid               int
	uid               int
	dirBits, fileBits fs.FileMode
	setgid            bool
}

func isFanOut(name string) bool {
	if len(name) != 2 {
		return false
	}
	for _, c := range name {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// readable reports whether mode and gid already give what the objects
// directory grants: its read bits, and its group unless others may read.
func (p repairPolicy) readable(mode fs.FileMode, gid int, bits fs.FileMode) bool {
	if mode.Perm()&bits != bits {
		return false
	}
	return gid == p.gid || bits&0o004 != 0 || bits&0o040 == 0
}

func (p repairPolicy) sample(result *ObjectStoreRepair, path string, err error) {
	if result.Sample == "" {
		result.Sample = path + ": " + err.Error()
	}
}

func (p repairPolicy) unrepaired(result *ObjectStoreRepair, path string, err error) {
	result.Checked++
	result.Unrepaired++
	p.sample(result, path, err)
}

// repairDir repairs the directory name of the store (descriptor parentFD)
// and the regular files in it, all through descriptors opened without
// following links.
func (p repairPolicy) repairDir(result *ObjectStoreRepair, parentFD int, path, name string) {
	fd, err := openDirAt(parentFD, name, path)
	if err != nil {
		if !missingOrLink(err) {
			p.unrepaired(result, path, err)
		}
		return
	}
	dir := os.NewFile(uintptr(fd), path)
	defer dir.Close()
	p.fix(result, fd, path, true)
	names, err := dir.Readdirnames(-1)
	if err != nil {
		result.Unrepaired++
		p.sample(result, path, err)
		return
	}
	sort.Strings(names)
	for _, file := range names {
		p.repairFile(result, fd, filepath.Join(path, file), file)
	}
}

// repairFile repairs the regular file name in dirFD. Anything else (a link,
// a nested directory, a device or FIFO) is skipped without being opened; a
// file swapped for something else between the check and the open is caught
// by comparing the opened descriptor with the checked entry.
func (p repairPolicy) repairFile(result *ObjectStoreRepair, dirFD int, path, name string) {
	var pre unix.Stat_t
	if err := unix.Fstatat(dirFD, name, &pre, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if !errors.Is(err, unix.ENOENT) {
			p.unrepaired(result, path, err)
		}
		return
	}
	if pre.Mode&unix.S_IFMT != unix.S_IFREG {
		return // only regular files: no links, no nested directories
	}
	if objectStoreRepairSwap != nil {
		objectStoreRepairSwap(path)
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		if !missingOrLink(err) {
			p.unrepaired(result, path, err)
		}
		return
	}
	defer unix.Close(fd)
	if err := sameInode(fd, pre); err != nil {
		if !errors.Is(err, unix.ELOOP) {
			p.unrepaired(result, path, err)
		}
		return // replaced meanwhile: the next pass sees what is there now
	}
	p.fix(result, fd, path, false)
}

// fix gives the entry open on fd (a directory when dir) the policy's read
// bits and group. Every check and change is on fd itself, never on path,
// which only names the entry in the result.
func (p repairPolicy) fix(result *ObjectStoreRepair, fd int, path string, dir bool) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		p.unrepaired(result, path, err)
		return
	}
	kind := st.Mode & unix.S_IFMT
	if dir && kind != unix.S_IFDIR || !dir && kind != unix.S_IFREG {
		return
	}
	result.Checked++
	bits := p.fileBits
	if dir {
		bits = p.dirBits
	}
	perm := fs.FileMode(st.Mode & 0o777)
	hasSetgid := st.Mode&unix.S_ISGID != 0
	gid := int(st.Gid)
	wantSetgid := dir && p.setgid && isFanOut(filepath.Base(path)) && !hasSetgid
	if p.readable(perm, gid, bits) && gid == p.gid && !wantSetgid {
		return
	}
	if int(st.Uid) != p.uid && p.uid != 0 {
		if !p.readable(perm, gid, bits) {
			result.Unrepaired++
			p.sample(result, path, fmt.Errorf("mode %v owner %d:%d, not Swarm's to change", perm, st.Uid, st.Gid))
		}
		return
	}
	if !dir && uint64(st.Nlink) > 1 {
		if !p.readable(perm, gid, bits) {
			result.Unrepaired++
			p.sample(result, path, fmt.Errorf("mode %v with %d hard links, not changed", perm, st.Nlink))
		}
		return
	}
	changed := false
	var failure error
	if gid != p.gid {
		if err := unix.Fchown(fd, -1, p.gid); err != nil {
			failure = fmt.Errorf("set group %d: %w", p.gid, err)
		} else {
			gid, changed = p.gid, true
		}
	}
	mode := uint32(perm | bits)
	if mode != uint32(perm) || wantSetgid {
		target := mode
		if dir && (hasSetgid || wantSetgid) {
			target |= unix.S_ISGID
		}
		if err := unix.Fchmod(fd, target); err != nil && target&unix.S_ISGID != 0 && !hasSetgid {
			// RestrictSUIDSGID refuses setgid: keep the group and mode.
			result.SetgidRefused++
			if mode != uint32(perm) {
				err = unix.Fchmod(fd, mode)
			} else {
				err = nil
			}
			if err != nil && failure == nil {
				failure = fmt.Errorf("chmod %v: %w", fs.FileMode(mode), err)
			} else if err == nil && mode != uint32(perm) {
				changed = true
			}
		} else if err != nil {
			if failure == nil {
				failure = fmt.Errorf("chmod %v: %w", fs.FileMode(target&0o777), err)
			}
		} else {
			changed = true
		}
	}
	var after unix.Stat_t
	err := unix.Fstat(fd, &after)
	if err == nil {
		gid = int(after.Gid)
	}
	if err != nil || !p.readable(fs.FileMode(after.Mode&0o777), gid, bits) {
		result.Unrepaired++
		if failure == nil {
			failure = errors.New("still not readable")
		}
		p.sample(result, path, failure)
		return
	}
	if changed {
		result.Repaired++
	}
}
