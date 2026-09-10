package tool

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const recoverySourceMetadataKey = "task_recovery_sources"
const recoverySourceMaxFiles = 32
const recoverySourceMaxBytes = 128 * 1024
const recoverySourceMaxSnapshots = 8

// RecoverySourceRequest selects exact relative files, never a caller-supplied
// worktree. Inspect returns a digest; Retain requires that same digest.
// Ignored files are explicit source evidence only: retention never stages them.
type RecoverySourceRequest struct {
	TaskCallID     string   `json:"task_call_id"`
	ChildSessionID string   `json:"child_session_id"`
	Paths          []string `json:"paths"`
	ExpectedDigest string   `json:"expected_digest,omitempty"`
}

type RecoverySourceFile struct {
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
	Executable bool   `json:"executable,omitempty"`
}

// RecoverySource is immutable quoted source data, not instructions or a grant
// to read the preserved sibling. The digest binds bytes, modes and lineage.
type RecoverySource struct {
	ParentSessionID string               `json:"parent_session_id"`
	ChildSessionID  string               `json:"child_session_id"`
	TaskCallID      string               `json:"task_call_id"`
	BaseCommit      string               `json:"base_commit"`
	HeadCommit      string               `json:"head_commit"`
	Branch          string               `json:"branch"`
	ChildGeneration uint64               `json:"child_generation"`
	Files           []RecoverySourceFile `json:"files"`
}

func (s RecoverySource) Digest() string {
	body, _ := json.Marshal(s)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (r *Runtime) recoverySourceIdentity(scope WorkspaceScope, req RecoverySourceRequest) (pebblestore.SessionSnapshot, pebblestore.SessionSnapshot, map[string]any, error) {
	if r == nil || r.sessions == nil || r.worktrees == nil {
		return pebblestore.SessionSnapshot{}, pebblestore.SessionSnapshot{}, nil, errors.New("recovery source services unavailable")
	}
	parent, err := r.manageWorktreeRecoveryParent(scope)
	if err != nil {
		return parent, pebblestore.SessionSnapshot{}, nil, err
	}
	launches, _ := parent.Metadata["task_launches"].(map[string]any)
	entry, _ := launches[req.TaskCallID].(map[string]any)
	var selected map[string]any
	for _, raw := range manageWorktreeLaunchRows(entry) {
		row, ok := raw.(map[string]any)
		if ok && req.ChildSessionID != "" && asString(row["child_session_id"]) == req.ChildSessionID {
			if selected != nil {
				return parent, pebblestore.SessionSnapshot{}, nil, errors.New("ambiguous recovery child lineage")
			}
			selected = row
		}
	}
	if selected == nil {
		return parent, pebblestore.SessionSnapshot{}, nil, errors.New("recovery child is not in the selected parent task call")
	}
	child, err := r.manageWorktreeRecoveryChild(parent, selected)
	if err != nil {
		return parent, child, nil, err
	}
	lifecycleAuthority, ok := r.sessions.(interface {
		GetLifecycle(string) (pebblestore.SessionLifecycleSnapshot, bool, error)
	})
	if !ok {
		return parent, child, nil, errors.New("recovery lifecycle authority unavailable")
	}
	lifecycle, found, err := lifecycleAuthority.GetLifecycle(child.ID)
	if err != nil {
		return parent, child, nil, err
	}
	if !found {
		return parent, child, nil, errors.New("recovery child lifecycle missing")
	}
	child.Lifecycle = &lifecycle
	// A recalled dirty row alone is not proof its producer stopped.
	if child.Lifecycle == nil || child.Lifecycle.Active || child.Lifecycle.EndedAt == 0 || (child.Lifecycle.Phase != "failed" && child.Lifecycle.Phase != "stopped" && child.Lifecycle.Phase != "blocked" && child.Lifecycle.Phase != "cancelled") {
		return parent, child, nil, errors.New("recovery source requires a terminal failed or stopped child")
	}
	destination, err := r.manageWorktreeRecoveryDestination(scope, parent, child, selected)
	if err != nil {
		return parent, child, nil, err
	}
	state, err := r.worktrees.InspectTaskWorkspace(child.WorktreeRootPath)
	if err != nil {
		return parent, child, nil, err
	}
	_, err = r.worktrees.VerifyTaskIntegrationWorkspace(destination, child.WorktreeRootPath, child.ID, child.WorktreeBranch, asString(selected["base_commit"]), state.HeadCommit)
	if err != nil {
		return parent, child, nil, err
	}
	selected = cloneRecoveryRow(selected)
	selected["source_head"] = state.HeadCommit
	return parent, child, selected, nil
}

func cloneRecoveryRow(row map[string]any) map[string]any {
	result := make(map[string]any, len(row)+1)
	for k, v := range row {
		result[k] = v
	}
	return result
}

// InspectRecoverySource reads only explicitly selected regular UTF-8 files.
// os.Root confines opens even under directory replacement; symlinks are rejected
// rather than granting their targets. No Git index or worktree writes occur.
func (r *Runtime) InspectRecoverySource(scope WorkspaceScope, req RecoverySourceRequest) (RecoverySource, error) {
	parent, child, row, err := r.recoverySourceIdentity(scope, req)
	if err != nil {
		return RecoverySource{}, err
	}
	if len(req.Paths) == 0 || len(req.Paths) > recoverySourceMaxFiles {
		return RecoverySource{}, errors.New("recovery source requires 1–32 exact files")
	}
	root, err := os.OpenRoot(child.WorktreeRootPath)
	if err != nil {
		return RecoverySource{}, err
	}
	defer root.Close()
	result := RecoverySource{ParentSessionID: parent.ID, ChildSessionID: child.ID, TaskCallID: req.TaskCallID, BaseCommit: asString(row["base_commit"]), HeadCommit: asString(row["source_head"]), Branch: child.WorktreeBranch, ChildGeneration: child.Lifecycle.Generation}
	paths := append([]string(nil), req.Paths...)
	sort.Strings(paths)
	total := 0
	for i, name := range paths {
		if i > 0 && paths[i-1] == name {
			return RecoverySource{}, errors.New("duplicate recovery source path")
		}
		file, err := readRecoverySourceFile(root, name, recoverySourceMaxBytes-total)
		if err != nil {
			return RecoverySource{}, err
		}
		total += len(file.Content)
		result.Files = append(result.Files, file)
	}
	// Detect producer restart or HEAD movement during the bounded read.
	_, latest, current, err := r.recoverySourceIdentity(scope, req)
	if err != nil {
		return RecoverySource{}, err
	}
	if latest.Lifecycle.Generation != result.ChildGeneration || asString(current["source_head"]) != result.HeadCommit {
		return RecoverySource{}, errors.New("recovery source identity changed during read")
	}
	return result, nil
}

func readRecoverySourceFile(root *os.Root, name string, remaining int) (RecoverySourceFile, error) {
	result := RecoverySourceFile{Path: name}
	if name == "" || len(name) > 1024 || name != path.Clean(name) || path.IsAbs(name) || strings.ContainsAny(name, "\\\x00\r\n") {
		return result, errors.New("invalid exact recovery source path")
	}
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if part == ".." || part == "." || strings.EqualFold(part, ".git") {
			return result, errors.New("recovery source path is outside source files")
		}
	}
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if os.IsNotExist(err) {
			result.Deleted = true
			return result, nil
		}
		if err != nil {
			return result, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return result, errors.New("recovery source symlinks are not supported")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return result, errors.New("recovery source ancestor is not a directory")
		}
		if i == len(parts)-1 && (!info.Mode().IsRegular() || info.Size() > int64(remaining)) {
			return result, errors.New("recovery source must be bounded regular text")
		}
	}
	file, err := openRecoverySourceFile(root, name)
	if err != nil {
		return result, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, errors.New("recovery source is not regular")
	}
	body, err := io.ReadAll(io.LimitReader(file, int64(remaining)+1))
	if err != nil {
		return result, err
	}
	if len(body) > remaining {
		return result, errors.New("recovery source exceeds byte bound")
	}
	if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return result, errors.New("binary recovery source is not supported")
	}
	selected, err := root.Lstat(name)
	if err != nil || selected.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, selected) || selected.Size() != int64(len(body)) || !selected.ModTime().Equal(info.ModTime()) {
		return result, errors.New("recovery source changed during read")
	}
	result.Content = string(body)
	result.Executable = info.Mode()&0111 != 0
	return result, nil
}

// RetainRecoverySource publishes selected bytes atomically with the parent's V3
// event/projection/outbox, using a sequence guard to avoid metadata lost updates.
func (r *Runtime) RetainRecoverySource(scope WorkspaceScope, req RecoverySourceRequest) (RecoverySource, error) {
	if len(req.ExpectedDigest) != 64 {
		return RecoverySource{}, errors.New("exact inspected recovery source digest required")
	}
	authority, ok := r.sessions.(interface {
		ListSessionEventsBefore(string, uint64, int) ([]pebblestore.V3SessionEvent, error)
	})
	if !ok {
		return RecoverySource{}, errors.New("recovery source sequence authority unavailable")
	}
	if _, err := r.manageWorktreeRecoveryParent(scope); err != nil {
		return RecoverySource{}, err
	}
	events, err := authority.ListSessionEventsBefore(scope.SessionID, 0, 1)
	if err != nil {
		return RecoverySource{}, err
	}
	if len(events) != 1 {
		return RecoverySource{}, errors.New("recovery parent sequence unavailable")
	}
	seq := events[0].Seq
	source, err := r.InspectRecoverySource(scope, req)
	if err != nil {
		return RecoverySource{}, err
	}
	if source.Digest() != req.ExpectedDigest {
		return RecoverySource{}, errors.New("recovery source changed since inspection")
	}
	parent, err := r.manageWorktreeRecoveryParent(scope)
	if err != nil {
		return RecoverySource{}, err
	}
	sources, _ := parent.Metadata[recoverySourceMetadataKey].(map[string]any)
	if raw, exists := sources[req.ExpectedDigest]; exists {
		body, _ := json.Marshal(raw)
		var saved RecoverySource
		if json.Unmarshal(body, &saved) != nil || saved.Digest() != req.ExpectedDigest {
			return RecoverySource{}, errors.New("retained recovery source integrity mismatch")
		}
		return saved, nil
	}
	if len(sources) >= recoverySourceMaxSnapshots {
		return RecoverySource{}, errors.New("parent recovery source retention limit reached")
	}
	sources = cloneRecoveryRow(sources)
	sources[req.ExpectedDigest] = source
	parent.Metadata = cloneRecoveryRow(parent.Metadata)
	parent.Metadata[recoverySourceMetadataKey] = sources
	key := "recovery-source:" + req.ExpectedDigest
	mutation, err := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{SessionID: parent.ID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, Kind: pebblestore.V3SessionMutationUpdateMetadata, Session: &parent, ExpectedLastEventSeq: &seq, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key})
	if err != nil {
		return RecoverySource{}, err
	}
	if mutation.Conflict != nil || mutation.Error != nil {
		return RecoverySource{}, errors.New("recovery source publication rejected")
	}
	if mutation.RealtimeOutbox != nil && r.publishSessionOutbox != nil {
		if err := r.publishSessionOutbox(*mutation.RealtimeOutbox); err != nil {
			return RecoverySource{}, fmt.Errorf("recovery source retained but realtime delivery failed: %w", err)
		}
	}
	return source, nil
}

// ReadRecoverySource resolves only an exact parent-owned retained digest. It
// returns immutable retained bytes only after rechecking the selected source
// and lineage; changed source requires a new explicit selection.
func (r *Runtime) ReadRecoverySource(scope WorkspaceScope, digest string) (RecoverySource, error) {
	parent, err := r.manageWorktreeRecoveryParent(scope)
	if err != nil {
		return RecoverySource{}, err
	}
	sources, _ := parent.Metadata[recoverySourceMetadataKey].(map[string]any)
	raw, found := sources[digest]
	if !found || len(digest) != 64 {
		return RecoverySource{}, errors.New("retained recovery source not found")
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return RecoverySource{}, err
	}
	var source RecoverySource
	if json.Unmarshal(body, &source) != nil || source.Digest() != digest || source.ParentSessionID != parent.ID {
		return RecoverySource{}, errors.New("retained recovery source integrity mismatch")
	}
	_, child, row, err := r.recoverySourceIdentity(scope, RecoverySourceRequest{TaskCallID: source.TaskCallID, ChildSessionID: source.ChildSessionID})
	if err != nil {
		return RecoverySource{}, err
	}
	if child.Lifecycle.Generation != source.ChildGeneration || asString(row["source_head"]) != source.HeadCommit {
		return RecoverySource{}, errors.New("retained recovery source lineage is stale")
	}
	paths := make([]string, 0, len(source.Files))
	for _, file := range source.Files {
		paths = append(paths, file.Path)
	}
	current, err := r.InspectRecoverySource(scope, RecoverySourceRequest{TaskCallID: source.TaskCallID, ChildSessionID: source.ChildSessionID, Paths: paths})
	if err != nil {
		return RecoverySource{}, err
	}
	if current.Digest() != digest {
		return RecoverySource{}, errors.New("retained recovery source has changed; inspect and select again")
	}
	return source, nil
}

func (r *Runtime) manageWorktreeRecoverySource(scope WorkspaceScope, args map[string]any) (string, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	var req RecoverySourceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", err
	}
	var source RecoverySource
	if asString(args["action"]) == "retain_source" {
		source, err = r.RetainRecoverySource(scope, req)
	} else {
		source, err = r.InspectRecoverySource(scope, req)
	}
	if err != nil {
		return "", err
	}
	files := make([]map[string]any, 0, len(source.Files))
	for _, file := range source.Files {
		files = append(files, map[string]any{"path": file.Path, "bytes": len(file.Content), "deleted": file.Deleted, "executable": file.Executable})
	}
	result, err := json.Marshal(map[string]any{"action": args["action"], "recovery_source_digest": source.Digest(), "child_session_id": source.ChildSessionID, "files": files, "guidance": "Use the exact retained digest on a new Coder launch/job with explicit owned_scope. Never replay completed jobs or read the sibling directly. Ignored files remain unstaged: return exact paths to the parent for permission-gated inclusion or an explicit tracked handoff destination; this is unfinished implementation, not an external blocker."})
	return string(result), err
}

// ValidateRecoverySourceTarget binds the replacement repository to the original
// managed child using the existing Git common-directory ownership verifier.
func (r *Runtime) ValidateRecoverySourceTarget(scope WorkspaceScope, source RecoverySource, target string) error {
	_, child, _, err := r.recoverySourceIdentity(scope, RecoverySourceRequest{TaskCallID: source.TaskCallID, ChildSessionID: source.ChildSessionID})
	if err != nil {
		return err
	}
	_, err = r.worktrees.VerifyTaskIntegrationWorkspace(target, child.WorktreeRootPath, child.ID, source.Branch, source.BaseCommit, source.HeadCommit)
	return err
}
