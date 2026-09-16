package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"swarm/packages/swarmd/internal/identity"
)

const repositoryReviewFiles = 2048
const repositoryReviewBytes int64 = 32 << 20

// Files are explicit review candidates, never automatically selected. Hidden and
// ignored files remain visible; symlinks and nested repositories are not importable.
type RepositoryReviewFile struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Mode       uint32 `json:"mode"`
	Digest     string `json:"digest"`
	Selectable bool   `json:"selectable"`
}
type RepositoryReview struct {
	Repository RepositoryState        `json:"repository"`
	Digest     string                 `json:"digest"`
	Files      []RepositoryReviewFile `json:"files"`
	Warning    string                 `json:"warning"`
}
type RepositoryBaselineRequest struct {
	Path                 string   `json:"path"`
	ExpectedResolvedPath string   `json:"expected_resolved_path"`
	ReviewDigest         string   `json:"review_digest"`
	SelectedPaths        []string `json:"selected_paths"`
	ConfirmBaseline      bool     `json:"confirm_baseline"`
	ConfirmOmissions     bool     `json:"confirm_omissions"`
}

// Bounded lock striping serializes preparation through every Service instance.
// Git update-ref additionally rejects concurrent external HEAD writes.
var repositoryPreparationLocks [64]sync.Mutex

func lockRepositoryPreparation(path string) func() {
	sum := sha256.Sum256([]byte(path))
	mu := &repositoryPreparationLocks[int(sum[0])%len(repositoryPreparationLocks)]
	mu.Lock()
	return mu.Unlock
}

func (s *Service) ReviewRepositoryForPrincipal(principal identity.Principal, path string) (RepositoryReview, error) {
	state, err := s.InspectRepositoryForPrincipal(principal, path)
	review := RepositoryReview{Repository: state, Files: []RepositoryReviewFile{}, Warning: "Only selected files enter the baseline. Omitted, ignored, and uncommitted files are not copied into managed worktrees. Existing index bytes are preserved; staged changes may need review against the new HEAD."}
	if err != nil {
		return review, err
	}
	switch state.State {
	case RepositoryStateReady, RepositoryStateNeedsInitialCommit, RepositoryStateNeedsAssistedSetup, RepositoryStateNotRepository:
	default:
		return review, &RepositoryPrerequisiteError{Repository: state}
	}
	if state.Repository != "" && state.Repository != state.Path || state.Message == repositoryMessageNonWorkTree {
		return review, errors.New("select a normal repository root")
	}
	root, err := os.OpenRoot(state.Path)
	if err != nil {
		return review, err
	}
	defer root.Close()
	var total int64
	deadline := time.Now().Add(2 * time.Minute)
	visited := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if time.Now().After(deadline) {
			return errors.New("content review exceeded its time limit")
		}
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		visited++
		if visited > repositoryReviewFiles*2 {
			return errors.New("project exceeds bounded review entry limit")
		}
		if name == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if _, e := root.Lstat(filepath.Join(name, ".git")); e == nil {
				review.Files = append(review.Files, RepositoryReviewFile{Path: name, Selectable: false})
				return fs.SkipDir
			} else if !errors.Is(e, os.ErrNotExist) {
				return e
			}
			return nil
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		file := RepositoryReviewFile{Path: name, Size: info.Size(), Mode: uint32(info.Mode()), Selectable: info.Mode().IsRegular()}
		if file.Selectable {
			total += info.Size()
			if total > repositoryReviewBytes {
				return errors.New("project exceeds bounded review byte limit")
			}
			f, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if e != nil {
				return e
			}
			h := sha256.New()
			n, e := io.Copy(h, io.LimitReader(f, repositoryReviewBytes+1))
			closeErr := f.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
			if n != info.Size() {
				return errors.New("project changed during content review; retry")
			}
			file.Digest = hex.EncodeToString(h.Sum(nil))
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, e := root.Readlink(name)
			if e != nil {
				return e
			}
			sum := sha256.Sum256([]byte(target))
			file.Digest = hex.EncodeToString(sum[:])
		}
		review.Files = append(review.Files, file)
		if len(review.Files) > repositoryReviewFiles {
			return errors.New("project exceeds bounded review file limit")
		}
		return nil
	})
	if err != nil {
		return review, err
	}
	sort.Slice(review.Files, func(i, j int) bool { return review.Files[i].Path < review.Files[j].Path })
	// Bind consent to caller, canonical path, selected directory identity, HEAD,
	// existing index and exact content; a rename/replacement invalidates review.
	info, err := root.Stat(".")
	if err != nil {
		return review, err
	}
	indexDigest := ""
	if state.Repository != "" {
		indexed, err := runRepositoryGit(state.Path, "ls-files", "-z")
		if err != nil {
			return review, err
		}
		seen := map[string]bool{}
		for _, file := range review.Files {
			seen[file.Path] = true
		}
		for _, name := range strings.Split(indexed, "\x00") {
			if name != "" && !seen[name] {
				review.Files = append(review.Files, RepositoryReviewFile{Path: name, Selectable: false})
				seen[name] = true
				if len(review.Files) > repositoryReviewFiles {
					return review, errors.New("index exceeds review file limit")
				}
			}
		}
		sort.Slice(review.Files, func(i, j int) bool { return review.Files[i].Path < review.Files[j].Path })
		indexPath, e := runRepositoryGit(state.Path, "rev-parse", "--git-path", "index")
		if e != nil {
			return review, e
		}
		if !filepath.IsAbs(indexPath) {
			indexPath = filepath.Join(state.Path, indexPath)
		}
		f, e := os.Open(indexPath)
		if e == nil {
			h := sha256.New()
			n, e := io.Copy(h, io.LimitReader(f, 4<<20+1))
			f.Close()
			if e != nil || n > (4<<20) {
				return review, errors.New("index exceeds review limit")
			}
			indexDigest = hex.EncodeToString(h.Sum(nil))
		} else if !errors.Is(e, os.ErrNotExist) {
			return review, e
		}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return review, errors.New("directory identity unavailable")
	}
	payload, _ := json.Marshal([]any{principal.AccountScopeID, principal.UserID, state.Path, stat.Dev, stat.Ino, state.HeadCommit, indexDigest, review.Files})
	sum := sha256.Sum256(payload)
	review.Digest = hex.EncodeToString(sum[:])
	return review, nil
}

// PrepareRepositoryBaselineForPrincipal is an explicit, provider-free mutation.
// It never reads the real index into the commit, invokes hooks/filters, overwrites
// history, changes trust configuration, saves a workspace, or selects a session.
func (s *Service) PrepareRepositoryBaselineForPrincipal(principal identity.Principal, req RepositoryBaselineRequest) (RepositoryState, error) {
	if err := requirePrincipal(principal); err != nil {
		return RepositoryState{}, err
	}
	requested, err := absoluteWorkspacePath(req.Path)
	if err != nil {
		return RepositoryState{}, err
	}
	if !req.ConfirmBaseline || req.ReviewDigest == "" || req.ExpectedResolvedPath != requested {
		return RepositoryState{}, errors.New("exact path, content review and explicit baseline consent are required")
	}
	unlock := lockRepositoryPreparation(requested)
	defer unlock()
	if len(req.SelectedPaths) > repositoryReviewFiles {
		return RepositoryState{}, errors.New("too many selected files")
	}
	selected := append([]string{}, req.SelectedPaths...)
	sort.Strings(selected)
	keyBytes, _ := json.Marshal([]any{principal.AccountScopeID, principal.UserID, requested, req.ReviewDigest, selected, req.ConfirmOmissions})
	key := sha256.Sum256(keyBytes)
	marker := fmt.Sprintf("Swarm-Baseline: %x", key)
	state, err := s.InspectRepositoryForPrincipal(principal, requested)
	if err != nil {
		return state, err
	}
	// Lost responses and reconnect retries acknowledge the exact already-published
	// operation, before checking a review token necessarily changed by that commit.
	if state.State == RepositoryStateReady {
		message, e := runRepositoryGit(requested, "log", "-1", "--format=%B")
		if e == nil && strings.Contains(message, "\n"+marker+"\n") {
			return state, nil
		}
		return state, errors.New("baseline preparation never adds commits to existing history; review and use its existing HEAD")
	}
	review, err := s.ReviewRepositoryForPrincipal(principal, requested)
	if err != nil {
		return state, err
	}
	if review.Digest != req.ReviewDigest {
		return state, errors.New("content review is stale; review current files before retrying")
	}
	byPath := map[string]RepositoryReviewFile{}
	for _, file := range review.Files {
		byPath[file.Path] = file
	}
	for i, name := range selected {
		file, ok := byPath[name]
		if !ok || !file.Selectable || i > 0 && selected[i-1] == name {
			return state, errors.New("selection contains duplicate, unsupported or unreviewed paths")
		}
	}
	if len(selected) != len(review.Files) && !req.ConfirmOmissions {
		return state, errors.New("explicit consent is required for files omitted from managed worktrees")
	}
	if requested == string(filepath.Separator) {
		return state, errors.New("choose a project directory, not the filesystem root")
	}
	if err := rejectRepositoryHome(requested); err != nil {
		return state, err
	}
	// Stable root capability prevents selected symlinks escaping the project.
	root, err := os.OpenRoot(requested)
	if err != nil {
		return state, err
	}
	defer root.Close()
	// Init is intentionally resumable. A later commit failure retains metadata,
	// not a misleading successful catalog entry; no user files are removed.
	if state.Repository == "" {
		if err := root.Mkdir(".git", 0700); err != nil {
			return state, err
		}
		reserved, err := root.Lstat(".git")
		if err != nil {
			return state, err
		}
		if _, err := runRepositoryGit(requested, "--git-dir=.git", "--work-tree=.", "init", "--initial-branch=main", "--template="); err != nil {
			current, statErr := root.Lstat(".git")
			if statErr != nil || !os.SameFile(reserved, current) {
				return state, errors.Join(err, errors.New("repository metadata changed during failed init; retained for review"))
			}
			return state, errors.Join(err, root.RemoveAll(".git"))
		}
	}
	scratch, err := os.MkdirTemp("", "swarm-baseline-")
	if err != nil {
		return state, err
	}
	defer os.RemoveAll(scratch)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "index")}
	if _, err := runRepositoryGitWithEnv(requested, env, "read-tree", "--empty"); err != nil {
		return state, err
	}
	deadline := time.Now().Add(2 * time.Minute)
	for _, name := range selected {
		if time.Now().After(deadline) {
			return state, errors.New("baseline preparation exceeded its time limit; retry")
		}
		file := byPath[name]
		info, err := root.Lstat(name)
		if err != nil {
			return state, err
		}
		if !info.Mode().IsRegular() || uint32(info.Mode()) != file.Mode {
			return state, errors.New("selected file changed; review again")
		}
		f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return state, err
		}
		data, err := io.ReadAll(io.LimitReader(f, repositoryReviewBytes+1))
		f.Close()
		if err != nil {
			return state, err
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != file.Size || hex.EncodeToString(sum[:]) != file.Digest {
			return state, errors.New("selected file changed; review again")
		}
		blob, err := runRepositoryGitInput(requested, env, strings.NewReader(string(data)), "hash-object", "-w", "--no-filters", "--stdin")
		if err != nil {
			return state, err
		}
		mode := "100644"
		if info.Mode()&0111 != 0 {
			mode = "100755"
		}
		record := mode + " " + blob + "\t" + name + "\x00"
		if _, err := runRepositoryGitInput(requested, env, strings.NewReader(record), "update-index", "-z", "--index-info"); err != nil {
			return state, err
		}
	}
	tree, err := runRepositoryGitWithEnv(requested, env, "write-tree")
	if err != nil {
		return state, err
	}
	// Reinspect content and index immediately before publishing. For an initially
	// non-repository review, init added only metadata, not content or index bytes.
	current, err := s.ReviewRepositoryForPrincipal(principal, requested)
	if err != nil {
		return state, err
	}
	if current.Digest != req.ReviewDigest {
		return state, errors.New("project changed before baseline publication; review again")
	}
	commit, err := runRepositoryGit(requested, "-c", "user.name=Swarm Workspace Setup", "-c", "user.email=swarm-workspace-setup@localhost", "commit-tree", tree, "-m", "Initialize Swarm workspace\n\n"+marker+"\n")
	if err != nil {
		return state, err
	}
	if _, err := runRepositoryGit(requested, "-c", "core.hooksPath="+os.DevNull, "update-ref", "HEAD", commit, strings.Repeat("0", len(commit))); err != nil {
		return state, err
	}
	state = inspectRepository(requested)
	if state.State != RepositoryStateReady {
		return state, &RepositoryPrerequisiteError{Repository: state}
	}
	return state, nil
}
