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
	"syscall"
	"time"
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
// that need it are counted Unrepaired. Symbolic links are never followed
// or changed. ok is false when folder has no object store of its own.
func RepairObjectStore(folder string) (ObjectStoreRepair, bool) {
	store := folderObjectStore(folder)
	result := ObjectStoreRepair{Folder: filepath.Clean(strings.TrimSpace(folder)), Store: store}
	if store == "" {
		return result, false
	}
	gitDir, err := os.Lstat(filepath.Dir(store))
	if err != nil || !gitDir.IsDir() {
		return result, false
	}
	root, err := os.Lstat(store)
	if err != nil || !root.IsDir() {
		return result, false
	}
	rootStat, ok := root.Sys().(*syscall.Stat_t)
	if !ok {
		return result, false
	}
	p := repairPolicy{
		gid:     int(rootStat.Gid),
		dirBits: root.Mode().Perm() & 0o055,
		setgid:  root.Mode()&os.ModeSetgid != 0,
		uid:     os.Geteuid(),
	}
	p.fileBits = p.dirBits & 0o044
	entries, err := os.ReadDir(store)
	if err != nil {
		result.Error = "read " + store + ": " + err.Error()
		return result, true
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !(isFanOut(name) || name == "pack" || name == "info") {
			continue
		}
		dir := filepath.Join(store, name)
		p.fix(&result, dir, true)
		files, err := os.ReadDir(dir)
		if err != nil {
			result.Unrepaired++
			p.sample(&result, dir, err)
			continue
		}
		for _, file := range files {
			if file.Type()&fs.ModeType != 0 {
				continue // only regular files: no links, no nested directories
			}
			p.fix(&result, filepath.Join(dir, file.Name()), false)
		}
	}
	return result, true
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

func (p repairPolicy) fix(result *ObjectStoreRepair, path string, dir bool) {
	info, err := os.Lstat(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) { // gone meanwhile (git gc): nothing to repair
			result.Checked++
			result.Unrepaired++
			p.sample(result, path, err)
		}
		return
	}
	if info.Mode()&fs.ModeSymlink != 0 || info.IsDir() != dir {
		return
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	result.Checked++
	bits := p.fileBits
	if dir {
		bits = p.dirBits
	}
	gid := int(st.Gid)
	wantSetgid := dir && p.setgid && isFanOut(filepath.Base(path)) && info.Mode()&os.ModeSetgid == 0
	if p.readable(info.Mode(), gid, bits) && gid == p.gid && !wantSetgid {
		return
	}
	if int(st.Uid) != p.uid && p.uid != 0 {
		if !p.readable(info.Mode(), gid, bits) {
			result.Unrepaired++
			p.sample(result, path, fmt.Errorf("mode %v owner %d:%d, not Swarm's to change", info.Mode().Perm(), st.Uid, st.Gid))
		}
		return
	}
	changed := false
	var failure error
	if gid != p.gid {
		if err := os.Lchown(path, -1, p.gid); err != nil {
			failure = fmt.Errorf("set group %d: %w", p.gid, err)
		} else {
			gid, changed = p.gid, true
		}
	}
	mode := info.Mode().Perm() | bits
	if mode != info.Mode().Perm() || wantSetgid {
		target := mode
		if dir && (info.Mode()&os.ModeSetgid != 0 || wantSetgid) {
			target |= os.ModeSetgid
		}
		if err := os.Chmod(path, target); err != nil && target&os.ModeSetgid != 0 && info.Mode()&os.ModeSetgid == 0 {
			// RestrictSUIDSGID refuses setgid: keep the group and mode.
			result.SetgidRefused++
			if mode != info.Mode().Perm() {
				err = os.Chmod(path, mode)
			} else {
				err = nil
			}
			if err != nil && failure == nil {
				failure = fmt.Errorf("chmod %v: %w", mode, err)
			} else if err == nil && mode != info.Mode().Perm() {
				changed = true
			}
		} else if err != nil {
			if failure == nil {
				failure = fmt.Errorf("chmod %v: %w", target, err)
			}
		} else {
			changed = true
		}
	}
	after, err := os.Lstat(path)
	if err == nil {
		if st2, ok := after.Sys().(*syscall.Stat_t); ok {
			gid = int(st2.Gid)
		}
	}
	if err != nil || !p.readable(after.Mode(), gid, bits) {
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
