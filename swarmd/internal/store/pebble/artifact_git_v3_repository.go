package pebblestore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var artifactV3IDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var artifactV3OIDPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

func OpenArtifactV3Repository(ctx context.Context, root, artifactID string, owner ArtifactV3Owner, limits ArtifactV3Limits) (*ArtifactV3Repository, error) {
	if !artifactV3IDPattern.MatchString(artifactID) || invalidArtifactV3Owner(owner) {
		return nil, ErrArtifactV3Invalid
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("native git required: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := secureArtifactV3Directory(root); err != nil {
		return nil, err
	}
	hooks := filepath.Join(root, ".empty-hooks")
	if err := secureArtifactV3Directory(hooks); err != nil {
		return nil, err
	}
	path := filepath.Join(root, artifactID+".git")
	if filepath.Dir(path) != filepath.Clean(root) {
		return nil, ErrArtifactV3Integrity
	}
	r := &ArtifactV3Repository{root: root, path: path, hooks: hooks, git: git, id: artifactID, owner: owner, limits: limits.normalized()}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if _, err = r.raw(ctx, nil, "init", "--bare", "--initial-branch=artifact", path); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := secureArtifactV3Directory(path); err != nil {
		return nil, err
	}
	bare, err := r.gitCommand(ctx, nil, "rev-parse", "--is-bare-repository")
	if err != nil || strings.TrimSpace(string(bare)) != "true" {
		return nil, ErrArtifactV3Integrity
	}
	if err := r.bindOwner(); err != nil {
		return nil, err
	}
	return r, nil
}

func invalidArtifactV3Owner(owner ArtifactV3Owner) bool {
	return strings.TrimSpace(owner.AccountScopeID) == "" || strings.TrimSpace(owner.UserID) == "" || strings.TrimSpace(owner.SessionID) == ""
}

func secureArtifactV3Directory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrArtifactV3Integrity
	}
	return os.Chmod(path, 0o700)
}

func (r *ArtifactV3Repository) bindOwner() error {
	path := filepath.Join(r.path, "swarm-owner.json")
	body, err := json.Marshal(r.owner)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if got, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(got, body) {
			return ErrArtifactV3Unauthorized
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrArtifactV3Unauthorized
		}
		return err
	}
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (r *ArtifactV3Repository) env() []string {
	return append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Swarm Artifact", "GIT_AUTHOR_EMAIL=artifact@swarm.invalid", "GIT_COMMITTER_NAME=Swarm Artifact", "GIT_COMMITTER_EMAIL=artifact@swarm.invalid", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")
}

func (r *ArtifactV3Repository) raw(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.git, args...)
	cmd.Env = r.env()
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("artifact v3 git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (r *ArtifactV3Repository) gitCommand(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	base := []string{"--git-dir=" + r.path, "-c", "core.hooksPath=" + r.hooks, "-c", "core.attributesFile=" + os.DevNull, "-c", "commit.gpgSign=false", "-c", "tag.gpgSign=false", "-c", "protocol.allow=never", "-c", "protocol.file.allow=never"}
	return r.raw(ctx, input, append(base, args...)...)
}

func (r *ArtifactV3Repository) Genesis(ctx context.Context, req ArtifactV3GenesisRequest) (ArtifactV3Revision, error) {
	if !artifactV3IDPattern.MatchString(req.TransactionID) {
		return ArtifactV3Revision{}, ErrArtifactV3Invalid
	}
	files, _, validationErr := r.validateProject(req.Project)
	if validationErr != nil {
		return ArtifactV3Revision{}, validationErr
	}
	if current, err := r.Head(ctx); err == nil {
		transaction, txErr := r.Transaction(ctx, req.TransactionID)
		if txErr == nil && transaction.CommitOID == current {
			existing, readErr := r.listFiles(ctx, current, "", r.limits.MaxFiles, false)
			if readErr != nil {
				return ArtifactV3Revision{}, readErr
			}
			if sameArtifactV3ProjectShapeAndBytes(ctx, r, req.Project, files, existing.Files, current) {
				return r.ReadRevision(ctx, current)
			}
			return ArtifactV3Revision{}, ErrArtifactV3TxReuse
		}
		return ArtifactV3Revision{}, ErrArtifactV3Conflict
	} else if !errors.Is(err, ErrArtifactV3NotFound) {
		return ArtifactV3Revision{}, err
	}
	commit, err := r.commitProject(ctx, req.Project, nil, req.Message)
	if err != nil {
		return ArtifactV3Revision{}, err
	}
	if err := r.atomicCreateAndAdvance(ctx, req.TransactionID, "", "", commit); err != nil {
		return ArtifactV3Revision{}, err
	}
	return r.ReadRevision(ctx, commit)
}

func (r *ArtifactV3Repository) Candidate(ctx context.Context, req ArtifactV3CandidateRequest) (ArtifactV3Revision, error) {
	if !artifactV3IDPattern.MatchString(req.TurnID) || !artifactV3IDPattern.MatchString(req.CandidateID) || !artifactV3IDPattern.MatchString(req.TransactionID) || !artifactV3OIDPattern.MatchString(req.BaseCommit) {
		return ArtifactV3Revision{}, ErrArtifactV3Invalid
	}
	if _, err := r.ReadRevision(ctx, req.BaseCommit); err != nil {
		return ArtifactV3Revision{}, err
	}
	if transaction, err := r.Transaction(ctx, req.TransactionID); err == nil {
		candidateRef := artifactV3CandidateRef(req.TurnID, req.CandidateID)
		candidate, candidateErr := r.ref(ctx, candidateRef)
		if candidateErr != nil || candidate != transaction.CommitOID {
			return ArtifactV3Revision{}, ErrArtifactV3Integrity
		}
		revision, readErr := r.ReadRevision(ctx, candidate)
		if readErr != nil {
			return ArtifactV3Revision{}, readErr
		}
		if len(revision.Parents) != 1 || revision.Parents[0] != req.BaseCommit {
			return ArtifactV3Revision{}, ErrArtifactV3TxReuse
		}
		files, _, validationErr := r.validateProject(req.Project)
		if validationErr != nil {
			return ArtifactV3Revision{}, validationErr
		}
		existing, listErr := r.listFiles(ctx, candidate, "", r.limits.MaxFiles, false)
		if listErr != nil {
			return ArtifactV3Revision{}, listErr
		}
		if !sameArtifactV3ProjectShapeAndBytes(ctx, r, req.Project, files, existing.Files, candidate) {
			return ArtifactV3Revision{}, ErrArtifactV3TxReuse
		}
		return revision, nil
	} else if !errors.Is(err, ErrArtifactV3NotFound) {
		return ArtifactV3Revision{}, err
	}
	commit, err := r.commitProject(ctx, req.Project, []string{req.BaseCommit}, req.Message)
	if err != nil {
		return ArtifactV3Revision{}, err
	}
	candidateRef := artifactV3CandidateRef(req.TurnID, req.CandidateID)
	if err := r.atomicCreateRefs(ctx, req.TransactionID, candidateRef, commit); err != nil {
		return ArtifactV3Revision{}, err
	}
	return r.ReadRevision(ctx, commit)
}

func (r *ArtifactV3Repository) Select(ctx context.Context, req ArtifactV3SelectionRequest) (ArtifactV3Revision, error) {
	if !artifactV3IDPattern.MatchString(req.TransactionID) || !artifactV3IDPattern.MatchString(req.TurnID) || !artifactV3IDPattern.MatchString(req.CandidateID) || !artifactV3OIDPattern.MatchString(req.ExpectedHead) || !artifactV3OIDPattern.MatchString(req.Candidate) {
		return ArtifactV3Revision{}, ErrArtifactV3Invalid
	}
	candidateRef := artifactV3CandidateRef(req.TurnID, req.CandidateID)
	candidate, err := r.ref(ctx, candidateRef)
	if err != nil || candidate != req.Candidate {
		return ArtifactV3Revision{}, ErrArtifactV3Conflict
	}
	if _, err := r.ReadRevision(ctx, req.Candidate); err != nil {
		return ArtifactV3Revision{}, err
	}
	if err := r.atomicCreateAndAdvance(ctx, req.TransactionID, req.ExpectedHead, candidateRef, req.Candidate); err != nil {
		return ArtifactV3Revision{}, err
	}
	return r.ReadRevision(ctx, req.Candidate)
}

func (r *ArtifactV3Repository) commitProject(ctx context.Context, project ArtifactV3Project, parents []string, message string) (string, error) {
	files, manifest, err := r.validateProject(project)
	if err != nil {
		return "", err
	}
	files, err = r.storeProject(ctx, project, files)
	if err != nil {
		return "", err
	}
	manifestBlob := ""
	var index bytes.Buffer
	for _, file := range files {
		fmt.Fprintf(&index, "100644 %s\t%s\x00", file.OID, file.Path)
		if file.Path == ArtifactV3ManifestFilename {
			manifestBlob = file.OID
		}
	}
	if manifestBlob == "" || manifest.SchemaVersion != ArtifactV3ManifestVersion {
		return "", ErrArtifactV3Integrity
	}
	indexFile, err := os.CreateTemp(r.root, ".artifact-v3-index-")
	if err != nil {
		return "", err
	}
	indexPath := indexFile.Name()
	if err := indexFile.Close(); err != nil {
		os.Remove(indexPath)
		return "", err
	}
	if err := os.Remove(indexPath); err != nil {
		return "", err
	}
	defer os.Remove(indexPath)
	cmd := exec.CommandContext(ctx, r.git, append([]string{"--git-dir=" + r.path}, "update-index", "-z", "--index-info")...)
	cmd.Env = append(r.env(), "GIT_INDEX_FILE="+indexPath)
	cmd.Stdin = &index
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("artifact v3 write index: %w: %s", err, strings.TrimSpace(string(out)))
	}
	cmd = exec.CommandContext(ctx, r.git, "--git-dir="+r.path, "write-tree")
	cmd.Env = append(r.env(), "GIT_INDEX_FILE="+indexPath)
	treeOut, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("artifact v3 write tree: %w: %s", err, strings.TrimSpace(string(treeOut)))
	}
	args := []string{"commit-tree", strings.TrimSpace(string(treeOut))}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	args = append(args, "-m", boundedArtifactV3Message(message))
	commit, err := r.gitCommand(ctx, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(commit)), nil
}

// ValidateArtifactV3Project applies the exact canonical tree and manifest checks
// used before Git commit. Build/preview gates call this same authority so a
// project cannot preview successfully and then fail only at finish_turn.
func ValidateArtifactV3Project(project ArtifactV3Project, limits ArtifactV3Limits) (ArtifactV3Manifest, error) {
	repository := &ArtifactV3Repository{limits: limits.normalized()}
	_, manifest, err := repository.validateProject(project)
	return manifest, err
}

func (r *ArtifactV3Repository) validateProject(project ArtifactV3Project) ([]ArtifactV3File, ArtifactV3Manifest, error) {
	if len(project.Files) == 0 || len(project.Files) > r.limits.MaxFiles {
		return nil, ArtifactV3Manifest{}, ErrArtifactV3Quota
	}
	paths := make([]string, 0, len(project.Files))
	seenFold := map[string]bool{}
	var total int64
	for path, body := range project.Files {
		clean, err := validateArtifactV3Path(path, r.limits)
		if err != nil {
			return nil, ArtifactV3Manifest{}, err
		}
		fold := strings.ToLower(clean)
		if seenFold[fold] {
			return nil, ArtifactV3Manifest{}, ErrArtifactV3Invalid
		}
		seenFold[fold] = true
		if int64(len(body)) > r.limits.MaxFileBytes {
			return nil, ArtifactV3Manifest{}, ErrArtifactV3Quota
		}
		total += int64(len(body))
		if total > r.limits.MaxTreeBytes {
			return nil, ArtifactV3Manifest{}, ErrArtifactV3Quota
		}
		paths = append(paths, clean)
	}
	manifestBody, ok := project.Files[ArtifactV3ManifestFilename]
	if !ok {
		return nil, ArtifactV3Manifest{}, ErrArtifactV3Invalid
	}
	manifest, err := validateArtifactV3Manifest(manifestBody, project.Files, r.limits)
	if err != nil {
		return nil, ArtifactV3Manifest{}, err
	}
	sort.Strings(paths)
	files := make([]ArtifactV3File, 0, len(paths))
	for _, path := range paths {
		files = append(files, ArtifactV3File{Path: path, Size: int64(len(project.Files[path])), Mode: "100644"})
	}
	return files, manifest, nil
}

func (r *ArtifactV3Repository) storeProject(ctx context.Context, project ArtifactV3Project, files []ArtifactV3File) ([]ArtifactV3File, error) {
	for index := range files {
		out, err := r.gitCommand(ctx, project.Files[files[index].Path], "hash-object", "-w", "--stdin")
		if err != nil {
			return nil, err
		}
		files[index].OID = strings.TrimSpace(string(out))
	}
	return files, nil
}

func sameArtifactV3ProjectShapeAndBytes(ctx context.Context, r *ArtifactV3Repository, project ArtifactV3Project, requested, existing []ArtifactV3File, commit string) bool {
	if len(requested) != len(existing) {
		return false
	}
	byPath := make(map[string]ArtifactV3File, len(existing))
	for _, file := range existing {
		byPath[file.Path] = file
	}
	for _, file := range requested {
		stored, ok := byPath[file.Path]
		if !ok || stored.Size != file.Size {
			return false
		}
		body, err := r.ReadFile(ctx, commit, file.Path)
		if err != nil || !bytes.Equal(body, project.Files[file.Path]) {
			return false
		}
	}
	return true
}

func validateArtifactV3Path(path string, limits ArtifactV3Limits) (string, error) {
	if path == "" || strings.Contains(path, "\\") || strings.IndexByte(path, 0) >= 0 || filepath.IsAbs(path) || len(path) > limits.MaxPathBytes {
		return "", ErrArtifactV3Invalid
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean != path || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Count(clean, "/")+1 > limits.MaxPathDepth {
		return "", ErrArtifactV3Invalid
	}
	for _, component := range strings.Split(clean, "/") {
		lower := strings.ToLower(component)
		if component == "" || lower == ".git" || strings.HasPrefix(lower, ".git") {
			return "", ErrArtifactV3Invalid
		}
	}
	return clean, nil
}

func validateArtifactV3Manifest(body []byte, files map[string][]byte, limits ArtifactV3Limits) (ArtifactV3Manifest, error) {
	var manifest ArtifactV3Manifest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, ErrArtifactV3Invalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return manifest, ErrArtifactV3Invalid
	}
	if manifest.SchemaVersion != ArtifactV3ManifestVersion {
		return manifest, ErrArtifactV3Invalid
	}
	entrypoint, err := validateArtifactV3Path(manifest.Entrypoint, limits)
	if err != nil || entrypoint != manifest.Entrypoint {
		return manifest, ErrArtifactV3Invalid
	}
	if _, ok := files[manifest.Entrypoint]; !ok {
		return manifest, ErrArtifactV3Invalid
	}
	if len(manifest.Parts) > limits.MaxParts {
		return manifest, ErrArtifactV3Quota
	}
	seen := map[string]bool{}
	for _, part := range manifest.Parts {
		if !artifactV3IDPattern.MatchString(part.ID) || strings.TrimSpace(part.Label) == "" || seen[part.ID] {
			return manifest, ErrArtifactV3Invalid
		}
		seen[part.ID] = true
		paths := append([]string(nil), part.Locator.Paths...)
		if part.Locator.Path != "" {
			paths = append(paths, part.Locator.Path)
		}
		switch part.Locator.Kind {
		case "file":
			if part.Locator.Path == "" || part.Locator.Value != "" || len(part.Locator.Paths) != 0 {
				return manifest, ErrArtifactV3Invalid
			}
		case "selector", "state":
			if strings.TrimSpace(part.Locator.Value) == "" {
				return manifest, ErrArtifactV3Invalid
			}
		case "semantic":
			if strings.TrimSpace(part.Locator.Value) == "" || len(paths) == 0 {
				return manifest, ErrArtifactV3Invalid
			}
		default:
			return manifest, ErrArtifactV3Invalid
		}
		for _, path := range paths {
			if _, ok := files[path]; !ok {
				return manifest, ErrArtifactV3Invalid
			}
		}
	}
	return manifest, nil
}

func boundedArtifactV3Message(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "Swarm Artifact V3 revision"
	}
	if len(message) > 1024 {
		return message[:1024]
	}
	return message
}

func artifactV3CandidateRef(turnID, candidateID string) string {
	return "refs/swarm/turns/" + turnID + "/candidate/" + candidateID
}
func artifactV3TransactionRef(id string) string { return "refs/swarm/transactions/" + id }

func (r *ArtifactV3Repository) atomicCreateRefs(ctx context.Context, transactionID, candidateRef, commit string) error {
	txRef := artifactV3TransactionRef(transactionID)
	if transaction, err := r.Transaction(ctx, transactionID); err == nil {
		if transaction.CommitOID == commit {
			if candidate, candidateErr := r.ref(ctx, candidateRef); candidateErr == nil && candidate == commit {
				return nil
			}
		}
		return ErrArtifactV3TxReuse
	} else if !errors.Is(err, ErrArtifactV3NotFound) {
		return err
	}
	input := fmt.Sprintf("start\ncreate %s %s\ncreate %s %s\nprepare\ncommit\n", txRef, commit, candidateRef, commit)
	if _, err := r.gitCommand(ctx, []byte(input), "update-ref", "--stdin"); err != nil {
		return fmt.Errorf("%w: %v", ErrArtifactV3Conflict, err)
	}
	return nil
}

func (r *ArtifactV3Repository) atomicCreateAndAdvance(ctx context.Context, transactionID, expected, candidateRef, commit string) error {
	if transaction, err := r.Transaction(ctx, transactionID); err == nil {
		if transaction.CommitOID != commit {
			return ErrArtifactV3TxReuse
		}
		head, headErr := r.Head(ctx)
		if headErr == nil && head == commit {
			return nil
		}
		return ErrArtifactV3Integrity
	} else if !errors.Is(err, ErrArtifactV3NotFound) {
		return err
	}
	var input string
	if expected == "" {
		input = fmt.Sprintf("start\ncreate refs/heads/artifact %s\ncreate %s %s\nprepare\ncommit\n", commit, artifactV3TransactionRef(transactionID), commit)
	} else {
		input = fmt.Sprintf("start\nupdate refs/heads/artifact %s %s\ncreate %s %s\nprepare\ncommit\n", commit, expected, artifactV3TransactionRef(transactionID), commit)
	}
	if _, err := r.gitCommand(ctx, []byte(input), "update-ref", "--stdin"); err != nil {
		return fmt.Errorf("%w: %v", ErrArtifactV3Conflict, err)
	}
	_ = candidateRef
	return nil
}

func (r *ArtifactV3Repository) ref(ctx context.Context, name string) (string, error) {
	out, err := r.gitCommand(ctx, nil, "rev-parse", "--verify", name+"^{commit}")
	if err != nil {
		return "", ErrArtifactV3NotFound
	}
	return strings.TrimSpace(string(out)), nil
}
func (r *ArtifactV3Repository) Head(ctx context.Context) (string, error) {
	return r.ref(ctx, "refs/heads/artifact")
}

func (r *ArtifactV3Repository) CandidateRef(turnID, candidateID string) (string, error) {
	if !artifactV3IDPattern.MatchString(turnID) || !artifactV3IDPattern.MatchString(candidateID) {
		return "", ErrArtifactV3Invalid
	}
	return artifactV3CandidateRef(turnID, candidateID), nil
}

func (r *ArtifactV3Repository) ChangedFiles(ctx context.Context, baseCommit, commit string) ([]string, error) {
	if !artifactV3OIDPattern.MatchString(commit) || (baseCommit != "" && !artifactV3OIDPattern.MatchString(baseCommit)) {
		return nil, ErrArtifactV3Invalid
	}
	var args []string
	if baseCommit == "" {
		args = []string{"diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "-z", commit}
	} else {
		args = []string{"diff", "--name-only", "-z", baseCommit, commit}
	}
	out, err := r.gitCommand(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	for _, raw := range bytes.Split(out, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		path := string(raw)
		if _, err := validateArtifactV3Path(path, r.limits); err != nil {
			return nil, ErrArtifactV3Integrity
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}
func (r *ArtifactV3Repository) Transaction(ctx context.Context, id string) (ArtifactV3Transaction, error) {
	if !artifactV3IDPattern.MatchString(id) {
		return ArtifactV3Transaction{}, ErrArtifactV3Invalid
	}
	commit, err := r.ref(ctx, artifactV3TransactionRef(id))
	if err != nil {
		return ArtifactV3Transaction{}, err
	}
	head, headErr := r.Head(ctx)
	state := ArtifactV3TransactionRecorded
	if headErr == nil && head == commit {
		state = ArtifactV3TransactionApplied
	}
	return ArtifactV3Transaction{ID: id, CommitOID: commit, HeadOID: head, State: state}, nil
}

func (r *ArtifactV3Repository) ReadRevision(ctx context.Context, commit string) (ArtifactV3Revision, error) {
	if !artifactV3OIDPattern.MatchString(commit) {
		return ArtifactV3Revision{}, ErrArtifactV3Invalid
	}
	typeOut, err := r.gitCommand(ctx, nil, "cat-file", "-t", commit)
	if err != nil || strings.TrimSpace(string(typeOut)) != "commit" {
		return ArtifactV3Revision{}, ErrArtifactV3NotFound
	}
	tree, err := r.gitCommand(ctx, nil, "show", "-s", "--format=%T", commit)
	if err != nil {
		return ArtifactV3Revision{}, err
	}
	parents, err := r.gitCommand(ctx, nil, "show", "-s", "--format=%P", commit)
	if err != nil {
		return ArtifactV3Revision{}, err
	}
	page, err := r.listFiles(ctx, commit, "", r.limits.MaxFiles, false)
	if err != nil {
		return ArtifactV3Revision{}, err
	}
	var total int64
	manifestOID := ""
	for _, file := range page.Files {
		total += file.Size
		if file.Path == ArtifactV3ManifestFilename {
			manifestOID = file.OID
		}
	}
	body, err := r.ReadFile(ctx, commit, ArtifactV3ManifestFilename)
	if err != nil {
		return ArtifactV3Revision{}, err
	}
	manifest, err := validateArtifactV3ManifestForRevision(body, page.Files, r.limits)
	if err != nil {
		return ArtifactV3Revision{}, err
	}
	return ArtifactV3Revision{CommitOID: commit, TreeOID: strings.TrimSpace(string(tree)), ManifestBlobOID: manifestOID, Parents: strings.Fields(string(parents)), Manifest: manifest, FileCount: len(page.Files), TreeBytes: total}, nil
}

func validateArtifactV3ManifestForRevision(body []byte, files []ArtifactV3File, limits ArtifactV3Limits) (ArtifactV3Manifest, error) {
	set := make(map[string][]byte, len(files))
	var total int64
	for _, file := range files {
		if file.Mode != "100644" || file.Size < 0 || file.Size > limits.MaxFileBytes {
			return ArtifactV3Manifest{}, ErrArtifactV3Integrity
		}
		if _, err := validateArtifactV3Path(file.Path, limits); err != nil {
			return ArtifactV3Manifest{}, ErrArtifactV3Integrity
		}
		set[file.Path] = nil
		total += file.Size
	}
	if len(files) > limits.MaxFiles || total > limits.MaxTreeBytes {
		return ArtifactV3Manifest{}, ErrArtifactV3Quota
	}
	set[ArtifactV3ManifestFilename] = body
	var manifest ArtifactV3Manifest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, ErrArtifactV3Integrity
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return manifest, ErrArtifactV3Integrity
	}
	if manifest.SchemaVersion != ArtifactV3ManifestVersion {
		return manifest, ErrArtifactV3Integrity
	}
	if _, err := validateArtifactV3Path(manifest.Entrypoint, limits); err != nil {
		return manifest, ErrArtifactV3Integrity
	}
	if _, ok := set[manifest.Entrypoint]; !ok {
		return manifest, ErrArtifactV3Integrity
	}
	if len(manifest.Parts) > limits.MaxParts {
		return manifest, ErrArtifactV3Quota
	}
	seen := map[string]bool{}
	for _, part := range manifest.Parts {
		if !artifactV3IDPattern.MatchString(part.ID) || strings.TrimSpace(part.Label) == "" || seen[part.ID] {
			return manifest, ErrArtifactV3Integrity
		}
		seen[part.ID] = true
		paths := append([]string(nil), part.Locator.Paths...)
		if part.Locator.Path != "" {
			paths = append(paths, part.Locator.Path)
		}
		if part.Locator.Kind == "file" && (part.Locator.Path == "" || part.Locator.Value != "" || len(part.Locator.Paths) != 0) {
			return manifest, ErrArtifactV3Integrity
		}
		if (part.Locator.Kind == "selector" || part.Locator.Kind == "state") && strings.TrimSpace(part.Locator.Value) == "" {
			return manifest, ErrArtifactV3Integrity
		}
		if part.Locator.Kind == "semantic" && (strings.TrimSpace(part.Locator.Value) == "" || len(paths) == 0) {
			return manifest, ErrArtifactV3Integrity
		}
		if part.Locator.Kind != "file" && part.Locator.Kind != "selector" && part.Locator.Kind != "state" && part.Locator.Kind != "semantic" {
			return manifest, ErrArtifactV3Integrity
		}
		for _, path := range paths {
			if _, ok := set[path]; !ok {
				return manifest, ErrArtifactV3Integrity
			}
		}
	}
	return manifest, nil
}

func (r *ArtifactV3Repository) ReadFile(ctx context.Context, commit, path string) ([]byte, error) {
	clean, err := validateArtifactV3Path(path, r.limits)
	if err != nil {
		return nil, err
	}
	out, err := r.gitCommand(ctx, nil, "show", commit+":"+clean)
	if err != nil {
		return nil, ErrArtifactV3NotFound
	}
	if int64(len(out)) > r.limits.MaxFileBytes {
		return nil, ErrArtifactV3Quota
	}
	return out, nil
}

func (r *ArtifactV3Repository) ListFiles(ctx context.Context, commit, cursor string, limit int) (ArtifactV3FilePage, error) {
	return r.listFiles(ctx, commit, cursor, limit, true)
}

func (r *ArtifactV3Repository) listFiles(ctx context.Context, commit, cursor string, limit int, capPage bool) (ArtifactV3FilePage, error) {
	if !artifactV3OIDPattern.MatchString(commit) {
		return ArtifactV3FilePage{}, ErrArtifactV3Invalid
	}
	if capPage {
		limit = r.pageLimit(limit)
	} else if limit <= 0 || limit > r.limits.MaxFiles {
		limit = r.limits.MaxFiles
	}
	out, err := r.gitCommand(ctx, nil, "ls-tree", "-r", "-l", "-z", commit)
	if err != nil {
		return ArtifactV3FilePage{}, ErrArtifactV3NotFound
	}
	start, err := decodeArtifactV3Cursor(cursor)
	if err != nil {
		return ArtifactV3FilePage{}, err
	}
	entries := bytes.Split(out, []byte{0})
	files := make([]ArtifactV3File, 0, min(limit+1, len(entries)))
	for _, entry := range entries {
		if len(entry) == 0 {
			continue
		}
		tab := bytes.IndexByte(entry, '\t')
		if tab < 0 {
			return ArtifactV3FilePage{}, ErrArtifactV3Integrity
		}
		meta, path := strings.Fields(string(entry[:tab])), string(entry[tab+1:])
		if len(meta) != 4 || meta[1] != "blob" {
			return ArtifactV3FilePage{}, ErrArtifactV3Integrity
		}
		if start != "" && path <= start {
			continue
		}
		size, parseErr := strconv.ParseInt(meta[3], 10, 64)
		if parseErr != nil || size < 0 {
			return ArtifactV3FilePage{}, ErrArtifactV3Integrity
		}
		files = append(files, ArtifactV3File{Path: path, OID: meta[2], Mode: meta[0], Size: size})
		if len(files) > limit {
			break
		}
	}
	page := ArtifactV3FilePage{Files: files}
	if len(files) > limit {
		page.Files = files[:limit]
		page.NextCursor = encodeArtifactV3Cursor(page.Files[len(page.Files)-1].Path)
	}
	return page, nil
}

func (r *ArtifactV3Repository) ListRefs(ctx context.Context, prefix, cursor string, limit int) (ArtifactV3RefPage, error) {
	if prefix != "refs/swarm/transactions/" && prefix != "refs/swarm/turns/" {
		return ArtifactV3RefPage{}, ErrArtifactV3Invalid
	}
	start, err := decodeArtifactV3Cursor(cursor)
	if err != nil {
		return ArtifactV3RefPage{}, err
	}
	out, err := r.gitCommand(ctx, nil, "for-each-ref", "--format=%(refname) %(objectname)", prefix)
	if err != nil {
		return ArtifactV3RefPage{}, err
	}
	refs := make([]ArtifactV3Ref, 0)
	limit = r.pageLimit(limit)
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		total++
		if total > r.limits.MaxRefs {
			return ArtifactV3RefPage{}, ErrArtifactV3Quota
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !artifactV3OIDPattern.MatchString(fields[1]) {
			return ArtifactV3RefPage{}, ErrArtifactV3Integrity
		}
		if start != "" && fields[0] <= start {
			continue
		}
		if len(refs) <= limit {
			refs = append(refs, ArtifactV3Ref{Name: fields[0], CommitOID: fields[1]})
		}
	}
	page := ArtifactV3RefPage{Refs: refs}
	if len(refs) > limit {
		page.Refs = refs[:limit]
		page.NextCursor = encodeArtifactV3Cursor(page.Refs[len(page.Refs)-1].Name)
	}
	return page, nil
}

func (r *ArtifactV3Repository) ListRevisions(ctx context.Context, cursor string, limit int) (ArtifactV3RevisionPage, error) {
	if _, err := r.Head(ctx); err != nil {
		return ArtifactV3RevisionPage{}, err
	}
	out, err := r.gitCommand(ctx, nil, "rev-list", "--topo-order", "--all")
	if err != nil {
		return ArtifactV3RevisionPage{}, err
	}
	start, err := decodeArtifactV3Cursor(cursor)
	if err != nil {
		return ArtifactV3RevisionPage{}, err
	}
	limit = r.pageLimit(limit)
	ids := strings.Fields(string(out))
	begin := start == ""
	page := ArtifactV3RevisionPage{}
	for _, id := range ids {
		if !begin {
			if id == start {
				begin = true
			}
			continue
		}
		revision, readErr := r.ReadRevision(ctx, id)
		if readErr != nil {
			return ArtifactV3RevisionPage{}, readErr
		}
		page.Revisions = append(page.Revisions, revision)
		if len(page.Revisions) > limit {
			break
		}
	}
	if len(page.Revisions) > limit {
		page.Revisions = page.Revisions[:limit]
		page.NextCursor = encodeArtifactV3Cursor(page.Revisions[len(page.Revisions)-1].CommitOID)
	}
	return page, nil
}

func (r *ArtifactV3Repository) pageLimit(limit int) int {
	if limit <= 0 || limit > r.limits.MaxPageSize {
		return r.limits.MaxPageSize
	}
	return limit
}
func encodeArtifactV3Cursor(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}
func decodeArtifactV3Cursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	body, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(body) > 1024 {
		return "", ErrArtifactV3Invalid
	}
	return string(body), nil
}

func (r *ArtifactV3Repository) Materialize(ctx context.Context, commit, destination string) error {
	if _, err := r.ReadRevision(ctx, commit); err != nil {
		return err
	}
	if err := secureArtifactV3Directory(destination); err != nil {
		return err
	}
	page, err := r.listFiles(ctx, commit, "", r.limits.MaxFiles, false)
	if err != nil {
		return err
	}
	for _, file := range page.Files {
		path := filepath.Join(destination, filepath.FromSlash(file.Path))
		relative, err := filepath.Rel(destination, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return ErrArtifactV3Integrity
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		body, err := r.ReadFile(ctx, commit, file.Path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (r *ArtifactV3Repository) IntegrityCheck(ctx context.Context) error {
	if _, err := r.gitCommand(ctx, nil, "fsck", "--full", "--strict"); err != nil {
		return fmt.Errorf("%w: %v", ErrArtifactV3Integrity, err)
	}
	head, err := r.Head(ctx)
	if err != nil {
		return err
	}
	_, err = r.ReadRevision(ctx, head)
	return err
}

func (r *ArtifactV3Repository) Delete() error {
	if filepath.Dir(r.path) != filepath.Clean(r.root) || filepath.Base(r.path) != r.id+".git" {
		return ErrArtifactV3Integrity
	}
	return os.RemoveAll(r.path)
}

// GitObjectPath returns a contained loose-object path for deterministic
// corruption tests and offline repair tooling; callers never receive it through
// the external API.
func (r *ArtifactV3Repository) GitObjectPath(oid string) (string, error) {
	if !artifactV3OIDPattern.MatchString(oid) || len(oid) != 40 {
		return "", ErrArtifactV3Invalid
	}
	return filepath.Join(r.path, "objects", oid[:2], oid[2:]), nil
}
