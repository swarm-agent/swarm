package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"swarm/packages/swarmd/internal/gitenv"
	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const manageSessionsCommitManifestVersion = 2

type manageSessionsCommitRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

type manageSessionsCommitFile struct {
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	GitMode     string `json:"git_mode,omitempty"`
	BlobOID     string `json:"blob_oid,omitempty"`
	Deleted     bool   `json:"deleted,omitempty"`
}

type manageSessionsCommitCandidate struct {
	SessionID         string                     `json:"session_id"`
	Message           string                     `json:"message"`
	ExpectedUpdatedAt int64                      `json:"expected_updated_at"`
	Repository        string                     `json:"repository"`
	InitialHead       string                     `json:"initial_head"`
	Files             []manageSessionsCommitFile `json:"files"`
}

type manageSessionsCommitManifest struct {
	Version int                             `json:"version"`
	Commits []manageSessionsCommitCandidate `json:"commits"`
}

type manageSessionsCommitPrepared struct {
	manifest manageSessionsCommitManifest
	locks    []*sync.Mutex
}

var manageSessionsRepositoryLocks = struct {
	sync.Mutex
	byRepository map[string]*sync.Mutex
}{byRepository: map[string]*sync.Mutex{}}

// PrepareManageSessionsCommitManifest resolves model-supplied session/message pairs
// into an authoritative, approval-safe manifest. Paths are derived exclusively from
// durable terminal checkpoint data.
func (r *Runtime) PrepareManageSessionsCommitManifest(ctx context.Context, scope WorkspaceScope, args map[string]any) (map[string]any, error) {
	prepared, err := r.prepareManageSessionsCommit(ctx, scope, args, nil)
	if err != nil {
		return nil, err
	}
	defer releaseManageSessionsCommitLocks(prepared.locks)
	raw, err := json.Marshal(prepared.manifest)
	if err != nil {
		return nil, err
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	return map[string]any{"action": "commit", "manifest": manifest}, nil
}

func (r *Runtime) manageSessionsCommit(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	manifest, err := decodeManageSessionsCommitManifest(args["manifest"])
	if err != nil {
		return "", err
	}
	prepared, err := r.prepareManageSessionsCommit(ctx, scope, args, &manifest)
	if err != nil {
		return "", err
	}
	defer releaseManageSessionsCommitLocks(prepared.locks)

	results := make([]map[string]any, 0, len(prepared.manifest.Commits))
	repoHeads := map[string]string{}
	for i, candidate := range prepared.manifest.Commits {
		expectedHead := candidate.InitialHead
		if prior := repoHeads[candidate.Repository]; prior != "" {
			expectedHead = prior
		}
		if err := r.validateManageSessionsCommitCandidate(ctx, scope, candidate, expectedHead); err != nil {
			return marshalManageSessionsCommitFailure(results, i, candidate, err)
		}
		paths := commitCandidatePaths(candidate.Files)
		if err := stageManageSessionsApprovedFiles(ctx, candidate.Repository, candidate.Files); err != nil {
			_ = unstageManageSessionsCommitPaths(ctx, candidate.Repository, paths)
			return marshalManageSessionsCommitFailure(results, i, candidate, fmt.Errorf("stage approved files: %w", err))
		}
		// The index was required clean and is populated from the approved blob IDs,
		// never by reopening mutable worktree content after final validation. Build
		// and advance the commit directly so repository hooks cannot rewrite the index.
		tree, err := manageSessionsGitOutput(ctx, candidate.Repository, "write-tree")
		if err != nil {
			_ = unstageManageSessionsCommitPaths(ctx, candidate.Repository, paths)
			return marshalManageSessionsCommitFailure(results, i, candidate, fmt.Errorf("write approved tree: %w", err))
		}
		created, err := runManageSessionsGitInput(ctx, candidate.Repository, []byte(candidate.Message+"\n"), "commit-tree", tree, "-p", expectedHead)
		if err != nil {
			_ = unstageManageSessionsCommitPaths(ctx, candidate.Repository, paths)
			return marshalManageSessionsCommitFailure(results, i, candidate, fmt.Errorf("create commit object: %w", err))
		}
		head := strings.TrimSpace(string(created))
		if _, err := runManageSessionsGit(ctx, candidate.Repository, "update-ref", "HEAD", head, expectedHead); err != nil {
			_ = unstageManageSessionsCommitPaths(ctx, candidate.Repository, paths)
			return marshalManageSessionsCommitFailure(results, i, candidate, fmt.Errorf("advance repository HEAD: %w", err))
		}
		if _, err := runManageSessionsGit(ctx, candidate.Repository, "reset", "--quiet", head); err != nil {
			return marshalManageSessionsCommitFailure(results, i, candidate, fmt.Errorf("refresh index after commit: %w", err))
		}
		committedRaw, err := manageSessionsGitOutput(ctx, candidate.Repository, "diff-tree", "--no-commit-id", "--name-only", "-z", "-r", "--root", head)
		if err != nil {
			return marshalManageSessionsCommitFailure(results, i, candidate, fmt.Errorf("verify created commit: %w", err))
		}
		committed := nonEmptyNULPaths(committedRaw)
		sort.Strings(committed)
		want := append([]string(nil), paths...)
		sort.Strings(want)
		if strings.Join(committed, "\x00") != strings.Join(want, "\x00") {
			return marshalManageSessionsCommitFailure(results, i, candidate, fmt.Errorf("created commit path set %v does not match approved paths %v", committed, want))
		}
		repoHeads[candidate.Repository] = head
		results = append(results, map[string]any{"session_id": candidate.SessionID, "message": candidate.Message, "commit_hash": head, "files": paths, "repository": candidate.Repository, "ready_for_testing": true, "session_state": "needs_review"})
	}
	return marshalManageSessions(map[string]any{"action": "commit", "commits": results, "created_count": len(results), "ready_for_testing": true, "sessions_remain_needs_review": true})
}

func (r *Runtime) prepareManageSessionsCommit(ctx context.Context, scope WorkspaceScope, args map[string]any, approved *manageSessionsCommitManifest) (manageSessionsCommitPrepared, error) {
	requests, err := parseManageSessionsCommitRequests(args, approved)
	if err != nil {
		return manageSessionsCommitPrepared{}, err
	}
	candidates := make([]manageSessionsCommitCandidate, 0, len(requests))
	seenSessions := map[string]struct{}{}
	for _, request := range requests {
		if _, exists := seenSessions[request.SessionID]; exists {
			return manageSessionsCommitPrepared{}, fmt.Errorf("commit contains duplicate session %s", request.SessionID)
		}
		seenSessions[request.SessionID] = struct{}{}
		session, archived, err := r.ownedManageSession(scope, request.SessionID)
		if err != nil {
			return manageSessionsCommitPrepared{}, err
		}
		state, stateErr := r.manageSessionAuthoritativeState(session)
		if stateErr != nil {
			return manageSessionsCommitPrepared{}, stateErr
		}
		if archived || state != "needs_review" {
			return manageSessionsCommitPrepared{}, fmt.Errorf("session %s must be active and needs_review", request.SessionID)
		}
		candidate, err := r.resolveManageSessionsCommitCandidate(ctx, scope, session, request)
		if err != nil {
			return manageSessionsCommitPrepared{}, err
		}
		candidates = append(candidates, candidate)
	}
	repositories := uniqueCommitRepositories(candidates)
	locks := acquireManageSessionsCommitLocks(repositories)
	prepared := manageSessionsCommitPrepared{locks: locks}
	ok := false
	defer func() {
		if !ok {
			releaseManageSessionsCommitLocks(locks)
		}
	}()

	// Resolve again under repository locks, then validate the complete batch before
	// any mutation. This closes the discovery-to-lock race.
	for i, request := range requests {
		session, archived, getErr := r.ownedManageSession(scope, request.SessionID)
		if getErr != nil {
			return manageSessionsCommitPrepared{}, getErr
		}
		state, stateErr := r.manageSessionAuthoritativeState(session)
		if stateErr != nil {
			return manageSessionsCommitPrepared{}, stateErr
		}
		if archived || state != "needs_review" {
			return manageSessionsCommitPrepared{}, fmt.Errorf("session %s must remain active and needs_review", request.SessionID)
		}
		candidate, resolveErr := r.resolveManageSessionsCommitCandidate(ctx, scope, session, request)
		if resolveErr != nil {
			return manageSessionsCommitPrepared{}, resolveErr
		}
		candidates[i] = candidate
	}
	if err := validateManageSessionsCommitBatch(candidates); err != nil {
		return manageSessionsCommitPrepared{}, err
	}
	manifest := manageSessionsCommitManifest{Version: manageSessionsCommitManifestVersion, Commits: candidates}
	if approved != nil && !equalManageSessionsCommitManifest(*approved, manifest) {
		return manageSessionsCommitPrepared{}, errors.New("approved commit manifest is stale; session version, repository HEAD, file set, or file content changed")
	}
	prepared.manifest = manifest
	ok = true
	return prepared, nil
}

func (r *Runtime) resolveManageSessionsCommitCandidate(ctx context.Context, scope WorkspaceScope, session pebblestore.SessionSnapshot, request manageSessionsCommitRequest) (manageSessionsCommitCandidate, error) {
	path := strings.TrimSpace(session.WorkspacePath)
	if session.WorktreeEnabled && strings.TrimSpace(session.WorktreeRootPath) != "" {
		path = strings.TrimSpace(session.WorktreeRootPath)
	}
	path, err := canonicalExistingPath(path)
	if err != nil {
		return manageSessionsCommitCandidate{}, fmt.Errorf("session %s workspace path: %w", session.ID, err)
	}
	// Commit authorization never treats a caller-supplied lexical scope as authority.
	// Resolve the selected repository canonically, then require an account-owned
	// workspace binding or a managed linked-worktree relationship.
	allowed, err := r.accountOwnsSessionGitPath(ctx, scope, session, path)
	if err != nil {
		return manageSessionsCommitCandidate{}, err
	}
	if !allowed {
		return manageSessionsCommitCandidate{}, fmt.Errorf("session %s repository is not account-owned", session.ID)
	}
	snapshot, err := gitstatus.SnapshotForPath(ctx, path, gitstatus.Options{})
	if err != nil || !snapshot.HasGit || strings.TrimSpace(snapshot.RepoRoot) == "" {
		return manageSessionsCommitCandidate{}, fmt.Errorf("session %s workspace is not an available Git repository", session.ID)
	}
	repo, err := canonicalExistingPath(snapshot.RepoRoot)
	if err != nil {
		return manageSessionsCommitCandidate{}, err
	}
	if snapshot.StagedCount != 0 {
		return manageSessionsCommitCandidate{}, fmt.Errorf("repository %s has staged work; commit requires a clean index", repo)
	}
	if snapshot.ConflictCount != 0 {
		return manageSessionsCommitCandidate{}, fmt.Errorf("repository %s has conflicts", repo)
	}
	changedFiles, err := r.sessionTerminalChangedFiles(session.ID)
	if err != nil {
		return manageSessionsCommitCandidate{}, err
	}
	dirty := map[string]gitstatus.FileStatus{}
	for _, file := range snapshot.Files {
		dirty[filepath.ToSlash(filepath.Clean(file.Path))] = file
	}
	files := make([]manageSessionsCommitFile, 0, len(changedFiles))
	seen := map[string]struct{}{}
	for _, declared := range changedFiles {
		rel, err := normalizeCommitCandidatePath(repo, path, declared)
		if err != nil {
			return manageSessionsCommitCandidate{}, fmt.Errorf("session %s changed_file %q: %w", session.ID, declared, err)
		}
		if _, exists := seen[rel]; exists {
			continue
		}
		status, isDirty := dirty[rel]
		if !isDirty {
			continue
		}
		if status.Staged || status.Conflict {
			return manageSessionsCommitCandidate{}, fmt.Errorf("session %s file %s is staged or conflicted", session.ID, rel)
		}
		if strings.TrimSpace(status.XY) == "" {
			return manageSessionsCommitCandidate{}, fmt.Errorf("session %s file %s has an unsupported ambiguous Git status", session.ID, rel)
		}
		if strings.ContainsAny(status.Path, "\x00\r\n") || strings.ContainsAny(status.OrigPath, "\x00\r\n") {
			return manageSessionsCommitCandidate{}, fmt.Errorf("session %s file %s has an unsupported control character in its Git status path", session.ID, rel)
		}
		if status.Kind == "rename" || strings.TrimSpace(status.OrigPath) != "" {
			return manageSessionsCommitCandidate{}, fmt.Errorf("session %s file %s has unsupported rename/copy status; declare the resulting paths separately after Git reports unambiguous ordinary statuses", session.ID, rel)
		}
		if strings.TrimSpace(status.Submodule) != "" && status.Submodule != "N..." {
			return manageSessionsCommitCandidate{}, fmt.Errorf("session %s file %s is a submodule and cannot be committed through manage-sessions", session.ID, rel)
		}
		file, err := materializeManageSessionsCommitFile(ctx, repo, rel, status)
		if err != nil {
			return manageSessionsCommitCandidate{}, err
		}
		seen[rel] = struct{}{}
		files = append(files, file)
	}
	if len(files) == 0 {
		return manageSessionsCommitCandidate{}, fmt.Errorf("session %s has no attributable dirty files", session.ID)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return manageSessionsCommitCandidate{SessionID: session.ID, Message: request.Message, ExpectedUpdatedAt: session.UpdatedAt, Repository: filepath.Clean(repo), InitialHead: snapshot.HeadOID, Files: files}, nil
}

func (r *Runtime) sessionTerminalChangedFiles(sessionID string) ([]string, error) {
	plan, ok, err := r.sessions.GetActivePlan(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok || plan.Document == nil {
		return nil, fmt.Errorf("session %s has no durable active plan", sessionID)
	}
	for i := len(plan.Document.Checkpoints) - 1; i >= 0; i-- {
		checkpoint := plan.Document.Checkpoints[i]
		status := strings.ToLower(strings.TrimSpace(checkpoint.Status))
		if status == "needs_review" || status == "completed" {
			if len(checkpoint.ChangedFiles) == 0 {
				return nil, fmt.Errorf("session %s terminal checkpoint %s has no changed_files", sessionID, checkpoint.ID)
			}
			return append([]string(nil), checkpoint.ChangedFiles...), nil
		}
	}
	return nil, fmt.Errorf("session %s has no durable terminal checkpoint", sessionID)
}

func parseManageSessionsCommitRequests(args map[string]any, approved *manageSessionsCommitManifest) ([]manageSessionsCommitRequest, error) {
	if approved != nil {
		if approved.Version != manageSessionsCommitManifestVersion || len(approved.Commits) == 0 || len(approved.Commits) > manageSessionsMaxBatch {
			return nil, errors.New("commit requires a valid approved manifest")
		}
		out := make([]manageSessionsCommitRequest, len(approved.Commits))
		for i, item := range approved.Commits {
			out[i] = manageSessionsCommitRequest{SessionID: item.SessionID, Message: item.Message}
		}
		return out, nil
	}
	raw, ok := args["commits"].([]any)
	if !ok || len(raw) == 0 || len(raw) > manageSessionsMaxBatch {
		return nil, fmt.Errorf("commit requires 1 to %d commits", manageSessionsMaxBatch)
	}
	out := make([]manageSessionsCommitRequest, 0, len(raw))
	for i, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("commits[%d] must be an object", i)
		}
		if len(item) != 2 {
			return nil, fmt.Errorf("commits[%d] accepts only session_id and message", i)
		}
		sessionID, message := stringValue(item["session_id"]), stringValue(item["message"])
		if sessionID == "" {
			return nil, fmt.Errorf("commits[%d].session_id is required", i)
		}
		if err := validateManageSessionsCommitMessage(message); err != nil {
			return nil, fmt.Errorf("commits[%d].message: %w", i, err)
		}
		out = append(out, manageSessionsCommitRequest{SessionID: sessionID, Message: message})
	}
	return out, nil
}

func validateManageSessionsCommitMessage(message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return errors.New("is required")
	}
	if len(message) > 4096 {
		return errors.New("must be at most 4096 bytes")
	}
	if strings.IndexByte(message, 0) >= 0 || strings.Contains(message, "\r") {
		return errors.New("contains an unsupported control character")
	}
	return nil
}

func decodeManageSessionsCommitManifest(value any) (manageSessionsCommitManifest, error) {
	if value == nil {
		return manageSessionsCommitManifest{}, errors.New("commit requires an approved canonical manifest")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return manageSessionsCommitManifest{}, err
	}
	var manifest manageSessionsCommitManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, fmt.Errorf("approved commit manifest invalid: %w", err)
	}
	if manifest.Version != manageSessionsCommitManifestVersion {
		return manifest, errors.New("approved commit manifest version is invalid")
	}
	return manifest, nil
}

func validateManageSessionsCommitBatch(candidates []manageSessionsCommitCandidate) error {
	paths := map[string]string{}
	for _, candidate := range candidates {
		for _, file := range candidate.Files {
			key := candidate.Repository + "\x00" + file.Path
			if prior := paths[key]; prior != "" {
				return fmt.Errorf("sessions %s and %s overlap on %s", prior, candidate.SessionID, file.Path)
			}
			paths[key] = candidate.SessionID
		}
	}
	return nil
}

func (r *Runtime) validateManageSessionsCommitCandidate(ctx context.Context, scope WorkspaceScope, candidate manageSessionsCommitCandidate, expectedHead string) error {
	session, archived, err := r.ownedManageSession(scope, candidate.SessionID)
	if err != nil {
		return err
	}
	state, stateErr := r.manageSessionAuthoritativeState(session)
	if stateErr != nil {
		return stateErr
	}
	if archived || session.UpdatedAt != candidate.ExpectedUpdatedAt || state != "needs_review" {
		return fmt.Errorf("session %s version or needs_review state changed after approval", candidate.SessionID)
	}
	head, err := manageSessionsGitOutput(ctx, candidate.Repository, "rev-parse", "HEAD")
	if err != nil || head != expectedHead {
		return fmt.Errorf("repository HEAD changed: got %s, expected %s", head, expectedHead)
	}
	snapshot, err := gitstatus.SnapshotForPath(ctx, candidate.Repository, gitstatus.Options{})
	if err != nil {
		return err
	}
	if snapshot.StagedCount != 0 || snapshot.ConflictCount != 0 {
		return errors.New("repository index is dirty or conflicted")
	}
	dirty := map[string]gitstatus.FileStatus{}
	for _, file := range snapshot.Files {
		dirty[filepath.ToSlash(filepath.Clean(file.Path))] = file
	}
	for _, file := range candidate.Files {
		status, ok := dirty[file.Path]
		if !ok || status.Staged || status.Conflict || status.Kind == "rename" || strings.TrimSpace(status.OrigPath) != "" || (strings.TrimSpace(status.Submodule) != "" && status.Submodule != "N...") {
			return fmt.Errorf("approved file %s is no longer an eligible unambiguous dirty file", file.Path)
		}
		current, err := materializeManageSessionsCommitFile(ctx, candidate.Repository, file.Path, status)
		if err != nil {
			return err
		}
		if current != file {
			return fmt.Errorf("approved file %s changed after approval", file.Path)
		}
	}
	return nil
}

func canonicalExistingPath(path string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func normalizeCommitCandidatePath(repo, workspace, declared string) (string, error) {
	declared = strings.TrimSpace(declared)
	if declared == "" {
		return "", errors.New("path is empty")
	}
	if strings.ContainsAny(declared, "\x00\r\n") {
		return "", errors.New("path contains an unsupported control character")
	}
	absolute := declared
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(workspace, declared)
	}
	absolute, err := filepath.Abs(absolute)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(repo, absolute)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("path is outside repository")
	}
	return filepath.ToSlash(filepath.Clean(rel)), nil
}

func materializeManageSessionsCommitFile(ctx context.Context, repo, rel string, status gitstatus.FileStatus) (manageSessionsCommitFile, error) {
	file := manageSessionsCommitFile{Path: rel}
	path := filepath.Join(repo, filepath.FromSlash(rel))
	info, err := os.Lstat(path)
	var contents []byte
	switch {
	case os.IsNotExist(err):
		if status.Untracked || status.Kind == "untracked" {
			return file, fmt.Errorf("approved untracked path %s disappeared while it was read", rel)
		}
		file.Deleted = true
	case err != nil:
		return file, err
	case info.Mode()&os.ModeSymlink != 0:
		target, readErr := os.Readlink(path)
		if readErr != nil {
			return file, readErr
		}
		file.GitMode = "120000"
		contents = []byte(target)
	case info.Mode().IsRegular():
		contents, err = os.ReadFile(path)
		if err != nil {
			return file, err
		}
		file.GitMode = "100644"
		if info.Mode()&0o111 != 0 {
			file.GitMode = "100755"
		}
	default:
		return file, fmt.Errorf("approved path %s is not a regular file or symlink", rel)
	}
	if !file.Deleted {
		before, beforeErr := os.Lstat(path)
		if beforeErr != nil || !os.SameFile(info, before) || before.Mode() != info.Mode() || before.Size() != info.Size() || !before.ModTime().Equal(info.ModTime()) {
			return file, fmt.Errorf("approved path %s changed while it was read", rel)
		}
		output, hashErr := runManageSessionsGitInput(ctx, repo, contents, "hash-object", "-w", "--stdin")
		if hashErr != nil {
			return file, fmt.Errorf("identify approved blob for %s: %w", rel, hashErr)
		}
		file.BlobOID = strings.TrimSpace(string(output))
		if len(file.BlobOID) != 40 && len(file.BlobOID) != 64 {
			return file, fmt.Errorf("identify approved blob for %s returned an invalid object id", rel)
		}
	}
	h := sha256.New()
	_, _ = h.Write([]byte(status.XY + "\x00" + status.Kind + "\x00" + rel + "\x00" + file.GitMode + "\x00" + file.BlobOID))
	if file.Deleted {
		_, _ = h.Write([]byte("\x00deleted"))
	}
	file.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return file, nil
}

func acquireManageSessionsCommitLocks(repositories []string) []*sync.Mutex {
	sort.Strings(repositories)
	locks := make([]*sync.Mutex, 0, len(repositories))
	manageSessionsRepositoryLocks.Lock()
	for _, repository := range repositories {
		lock := manageSessionsRepositoryLocks.byRepository[repository]
		if lock == nil {
			lock = &sync.Mutex{}
			manageSessionsRepositoryLocks.byRepository[repository] = lock
		}
		locks = append(locks, lock)
	}
	manageSessionsRepositoryLocks.Unlock()
	for _, lock := range locks {
		lock.Lock()
	}
	return locks
}

func releaseManageSessionsCommitLocks(locks []*sync.Mutex) {
	for i := len(locks) - 1; i >= 0; i-- {
		locks[i].Unlock()
	}
}

func uniqueCommitRepositories(candidates []manageSessionsCommitCandidate) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seen[candidate.Repository]; !ok {
			seen[candidate.Repository] = struct{}{}
			out = append(out, candidate.Repository)
		}
	}
	return out
}

func equalManageSessionsCommitManifest(a, b manageSessionsCommitManifest) bool {
	aRaw, _ := json.Marshal(a)
	bRaw, _ := json.Marshal(b)
	return string(aRaw) == string(bRaw)
}

func commitCandidatePaths(files []manageSessionsCommitFile) []string {
	paths := make([]string, len(files))
	for i := range files {
		paths[i] = files[i].Path
	}
	return paths
}

func runManageSessionsGit(ctx context.Context, repository string, first string, rest ...string) ([]byte, error) {
	return runManageSessionsGitInput(ctx, repository, nil, first, rest...)
}

func runManageSessionsGitInput(ctx context.Context, repository string, input []byte, first string, rest ...string) ([]byte, error) {
	canonicalRepository, err := canonicalExistingPath(repository)
	if err != nil || canonicalRepository != filepath.Clean(repository) {
		if err == nil {
			err = errors.New("repository path identity changed")
		}
		return nil, fmt.Errorf("canonicalize repository before git %s: %w", first, err)
	}
	args := append([]string{first}, rest...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = canonicalRepository
	cmd.Env = gitenv.FilterIdentityOverrides(os.Environ())
	if input != nil {
		cmd.Stdin = strings.NewReader(string(input))
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("git %s: %w: %s", first, err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func manageSessionsGitOutput(ctx context.Context, repository string, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("git command is required")
	}
	output, err := runManageSessionsGit(ctx, repository, args[0], args[1:]...)
	return strings.TrimSpace(string(output)), err
}

func stageManageSessionsApprovedFiles(ctx context.Context, repository string, files []manageSessionsCommitFile) error {
	for _, file := range files {
		if file.Deleted {
			if _, err := runManageSessionsGit(ctx, repository, "update-index", "--force-remove", "--", file.Path); err != nil {
				return err
			}
			continue
		}
		if file.GitMode != "100644" && file.GitMode != "100755" && file.GitMode != "120000" {
			return fmt.Errorf("approved path %s has unsupported Git mode %q", file.Path, file.GitMode)
		}
		if file.BlobOID == "" {
			return fmt.Errorf("approved path %s has no blob identity", file.Path)
		}
		if _, err := runManageSessionsGit(ctx, repository, "cat-file", "-e", file.BlobOID+"^{blob}"); err != nil {
			return fmt.Errorf("approved path %s blob is unavailable: %w", file.Path, err)
		}
		if _, err := runManageSessionsGit(ctx, repository, "update-index", "--add", "--cacheinfo", file.GitMode, file.BlobOID, file.Path); err != nil {
			return err
		}
	}
	return nil
}

func unstageManageSessionsCommitPaths(ctx context.Context, repository string, paths []string) error {
	_, err := runManageSessionsGit(ctx, repository, "reset", append([]string{"--quiet", "--"}, paths...)...)
	return err
}

func nonEmptyNULPaths(value string) []string {
	out := []string{}
	for _, path := range strings.Split(value, "\x00") {
		if path != "" {
			out = append(out, filepath.ToSlash(filepath.Clean(path)))
		}
	}
	return out
}

func marshalManageSessionsCommitFailure(completed []map[string]any, index int, candidate manageSessionsCommitCandidate, err error) (string, error) {
	output, marshalErr := marshalManageSessions(map[string]any{"action": "commit", "status": "partial_failure", "failed_index": index, "failed_session_id": candidate.SessionID, "error": err.Error(), "completed_commits": completed, "completed_count": len(completed), "earlier_commits_preserved": len(completed) > 0})
	if marshalErr != nil {
		return "", marshalErr
	}
	return output, fmt.Errorf("commit batch stopped after %d completed commit(s): %w", len(completed), err)
}

// ManageSessionsCommitScope builds the authoritative scope used by the run-layer
// approval and execution paths without trusting model-supplied paths.
func ManageSessionsCommitScope(session pebblestore.SessionSnapshot, principal identity.Principal) WorkspaceScope {
	// Deliberately omit workspace roots. The commit engine must prove repository
	// ownership through the account workspace service instead of trusting the
	// selected session's stored path as an authorization grant.
	return WorkspaceScope{SessionID: session.ID, Principal: principal}
}
