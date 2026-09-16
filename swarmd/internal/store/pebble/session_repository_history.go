package pebblestore

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble"
)

const repositoryHistoryMetaKey = "v3/repository_history/meta_v3"
const repositoryHistoryRevisionKey = "v3/repository_history/revision"

var ErrRepositoryHistoryNotReady = errors.New("repository history requires explicit backfill")
var ErrRepositoryHistoryCursor = errors.New("invalid or stale repository history cursor")

// RepositoryHistoryQuery is principal-scoped. Limit must be 1..100 and must
// remain unchanged while following an opaque cursor.
type RepositoryHistoryQuery struct {
	AccountScopeID  string
	UserID          string
	ParentSessionID string
	Limit           int
	Cursor          string
}

// SessionRepositoryHistory retains each observed workspace/worktree context.
// Snapshot metadata is evidence, never authorization to access its paths.
// Historical rows must be independently authorized against current grants.
type SessionRepositoryHistory struct {
	Session            SessionSnapshot  `json:"session"`
	Archived           bool             `json:"archived"`
	Deleted            bool             `json:"deleted"`
	ContextID          string           `json:"context_id"`
	Grants             []WorkspaceGrant `json:"grants,omitempty"`
	Projected          bool             `json:"projected,omitempty"`
	HistoricalWorktree bool             `json:"historical_worktree,omitempty"`
}

type RepositoryHistoryPage struct {
	Sessions   []SessionRepositoryHistory `json:"sessions"`
	Programs   []TaskProgramRecord        `json:"programs"`
	NextCursor string                     `json:"next_cursor,omitempty"`
	// Coverage includes canonical bounded worktree provenance retained in snapshots.
	HistoryCoverage string `json:"history_coverage"`
}

type repositoryHistoryMeta struct {
	Secret []byte `json:"secret"`
	Phase  int    `json:"phase"`
	After  string `json:"after"`
	Ready  bool   `json:"ready"`
}

type repositoryHistoryCursor struct {
	Scope    string
	Revision string
	After    string
	Limit    int
}

func repositoryHistoryPrefix(account, user, parent string) string {
	return "v3/repository_history/rows/" + keyPart(account) + "/" + keyPart(user) + "/" + keyPart(parent) + "/"
}

func repositoryHistoryRevision(batch *pebble.Batch, parent string) error {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	return batch.Set([]byte(repositoryHistoryRevisionKey+"/"+keyPart(parent)), token, nil)
}

// Called in the same batch as canonical snapshot/library updates. Context keys
// are stable across lifecycle changes but differ across default/grant changes.
func (s *SessionStore) retainRepositoryHistoryInBatch(batch *pebble.Batch, session SessionSnapshot, archived, deleted bool) error {
	// Import recorded lanes first; the current context wins shared source claims.
	// These are evidence only, never new session grants or filesystem authority.
	for _, historical := range repositoryHistoricalWorktrees(session) {
		if err := s.retainRepositoryContextInBatch(batch, historical, archived, deleted, true); err != nil {
			return err
		}
	}
	return s.retainRepositoryContextInBatch(batch, session, archived, deleted, false)
}

func (s *SessionStore) retainRepositoryContextInBatch(batch *pebble.Batch, session SessionSnapshot, archived, deleted, historical bool) error {
	session = normalizeSessionOwnership(session)
	identity := struct {
		Workspace  string
		Root       string
		Branch     string
		Base       string
		BaseCommit string
		Source     string
		Grants     []WorkspaceGrant
		Enabled    bool
	}{session.WorkspacePath, session.WorktreeRootPath, session.WorktreeBranch, session.WorktreeBaseBranch, v3LibraryMetadataString(session.Metadata, "base_commit"), v3LibraryMetadataString(session.Metadata, "swarm_v3_source_workspace_path"), session.WorkspaceGrants, session.WorktreeEnabled}
	payload, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	row := SessionRepositoryHistory{Session: session, Archived: archived, Deleted: deleted, ContextID: hex.EncodeToString(digest[:]), HistoricalWorktree: historical}
	payload, err = json.Marshal(row)
	if err != nil {
		return err
	}
	parents := []string{session.ID}
	if parent := v3LibraryMetadataString(session.Metadata, "parent_session_id"); parent != "" && parent != session.ID {
		var owner SessionSnapshot
		found, err := getJSONFromReader(s.store.db, KeySession(parent), &owner)
		if err != nil {
			return err
		}
		if !found {
			var tomb V3SessionTombstone
			found, err = getJSONFromReader(s.store.db, KeyV3SessionTombstone(parent), &tomb)
			if err != nil {
				return err
			}
			owner = tomb.Session
		}
		owner = normalizeSessionOwnership(owner)
		if found && owner.AccountScopeID == session.AccountScopeID && owner.UserID == session.UserID {
			parents = append(parents, parent)
		}
	}
	for _, parent := range parents {
		prefix := repositoryHistoryPrefix(session.AccountScopeID, session.UserID, parent)
		key := prefix + keyPart(session.ID) + "/" + row.ContextID
		var previous SessionRepositoryHistory
		found, err := getJSONFromReader(s.store.db, key, &previous)
		if err != nil {
			return err
		}
		_, revisionCloser, revisionErr := s.store.db.Get([]byte(repositoryHistoryRevisionKey + "/" + keyPart(parent)))
		if revisionErr == nil {
			revisionCloser.Close()
		} else if !errors.Is(revisionErr, pebble.ErrNotFound) {
			return revisionErr
		}
		before, _ := json.Marshal(repositoryHistoryMeaning(previous))
		after, _ := json.Marshal(repositoryHistoryMeaning(row))
		if !found || revisionErr != nil || !bytes.Equal(before, after) {
			if err := batch.Set([]byte(key), payload, nil); err != nil {
				return err
			}
			if err := repositoryHistoryRevision(batch, parent); err != nil {
				return err
			}
		}
		for _, grant := range repositoryRowGrants(row) {
			claim := repositoryHistoryClaimKey(prefix, session.ID, grant)
			value, closer, err := s.store.db.Get([]byte(claim))
			changed := errors.Is(err, pebble.ErrNotFound)
			if err == nil {
				changed = string(value) != key
				closer.Close()
			} else if !changed {
				return err
			}
			if changed {
				if err := repositoryHistoryRevision(batch, parent); err != nil {
					return err
				}
			}
			if err := batch.Set([]byte(claim), []byte(key), nil); err != nil {
				return err
			}
			if err := batch.Set([]byte(repositoryHistoryExactKey(prefix, grant.Path)), []byte(key), nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// BackfillRepositoryHistory explicitly advances at most limit durable rows.
// It shares the canonical library repair exclusion lock; progress and rows
// commit atomically. Reads never trigger migration. Snapshots, tombstones and
// their bounded canonical worktree history are the migration authority.
func (s *SessionStore) BackfillRepositoryHistory(limit int) (bool, error) {
	if limit < 1 || limit > 100 {
		return false, errors.New("backfill limit must be 1..100")
	}
	s.store.sessionMutations.libraryRepairMu.Lock()
	defer s.store.sessionMutations.libraryRepairMu.Unlock()
	var meta repositoryHistoryMeta
	_, err := s.store.GetJSON(repositoryHistoryMetaKey, &meta)
	if err != nil {
		return false, err
	}
	if meta.Ready {
		return true, nil
	}
	if len(meta.Secret) == 0 {
		meta.Secret = make([]byte, 32)
		if _, err := rand.Read(meta.Secret); err != nil {
			return false, err
		}
	}
	prefix := "v3/repository_history/rows/"
	if meta.Phase == 1 {
		prefix = SessionPrefix()
	}
	if meta.Phase == 2 {
		prefix = V3SessionTombstonePrefix()
	}
	if meta.Phase == 3 {
		prefix = "task_program/"
	}
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return false, err
	}
	defer iter.Close()
	batch := s.store.NewBatch()
	defer batch.Close()
	valid := iter.First()
	if meta.After != "" {
		valid = iter.SeekGE([]byte(meta.After))
		if valid && string(iter.Key()) == meta.After {
			valid = iter.Next()
		}
	}
	for n := 0; valid && n < limit; n++ {
		if meta.Phase == 0 {
			var row SessionRepositoryHistory
			if err := json.Unmarshal(iter.Value(), &row); err != nil {
				return false, err
			}
			parentPrefix := string(iter.Key())[:strings.LastIndex(string(iter.Key()), "/")]
			parentPrefix = parentPrefix[:strings.LastIndex(parentPrefix, "/")+1]
			for _, grant := range repositoryRowGrants(row) {
				if err := batch.Set([]byte(repositoryHistoryClaimKey(parentPrefix, row.Session.ID, grant)), append([]byte(nil), iter.Key()...), nil); err != nil {
					return false, err
				}
				if err := batch.Set([]byte(repositoryHistoryExactKey(parentPrefix, grant.Path)), append([]byte(nil), iter.Key()...), nil); err != nil {
					return false, err
				}
			}
			meta.After = string(iter.Key())
			valid = iter.Next()
			continue
		}
		if meta.Phase == 3 {
			var record TaskProgramRecord
			if err := json.Unmarshal(iter.Value(), &record); err != nil {
				return false, err
			}
			if err := s.indexRepositoryLane(batch, record); err != nil {
				return false, err
			}
			meta.After = string(iter.Key())
			valid = iter.Next()
			continue
		}
		var session SessionSnapshot
		archived, deleted := false, false
		if meta.Phase == 1 {
			err = json.Unmarshal(iter.Value(), &session)
		} else {
			var tombstone V3SessionTombstone
			err = json.Unmarshal(iter.Value(), &tombstone)
			session, archived, deleted = tombstone.Session, tombstone.Archived, tombstone.Deleted
		}
		if err != nil {
			return false, err
		}
		if session.ID != "" {
			if err := s.retainRepositoryHistoryInBatch(batch, session, archived, deleted); err != nil {
				return false, err
			}
		}
		meta.After = string(iter.Key())
		valid = iter.Next()
	}
	if err := iter.Error(); err != nil {
		return false, err
	}
	if !valid {
		meta.Phase++
		meta.After = ""
		meta.Ready = meta.Phase == 4
	}
	payload, err := json.Marshal(meta)
	if err != nil {
		return false, err
	}
	if err := batch.Set([]byte(repositoryHistoryMetaKey), payload, nil); err != nil {
		return false, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return false, err
	}
	return meta.Ready, nil
}

func repositoryHistoryOwner(reader pebble.Reader, q RepositoryHistoryQuery) error {
	if strings.TrimSpace(q.AccountScopeID) == "" || strings.TrimSpace(q.UserID) == "" || strings.TrimSpace(q.ParentSessionID) == "" || q.Limit < 1 || q.Limit > 100 {
		return errors.New("repository history requires principal, parent and limit 1..100")
	}
	var parent SessionSnapshot
	ok, err := getJSONFromReader(reader, KeySession(q.ParentSessionID), &parent)
	if err != nil {
		return err
	}
	if !ok {
		var tombstone V3SessionTombstone
		ok, err = getJSONFromReader(reader, KeyV3SessionTombstone(q.ParentSessionID), &tombstone)
		if err != nil {
			return err
		}
		if !ok || tombstone.Deleted {
			return errors.New("repository history parent unavailable")
		}
		parent = tombstone.Session
	}
	parent = normalizeSessionOwnership(parent)
	if parent.AccountScopeID != q.AccountScopeID || parent.UserID != q.UserID {
		return errors.New("repository history principal mismatch")
	}
	return nil
}

// ListSessionRepositoryHistory reads at most Limit+1 parent-indexed rows from
// one snapshot. It includes the parent's retained contexts and direct children,
// regardless of archive, rotation or worker outcome; it never touches Git.
func (s *SessionStore) ListSessionRepositoryHistory(q RepositoryHistoryQuery) (RepositoryHistoryPage, error) {
	return s.repositoryHistoryPage(q, false)
}

// ListTaskProgramRepositoryHistory returns bounded full durable program records
// (including exact lane, job and generation history), without reconciliation.
func (s *SessionStore) ListTaskProgramRepositoryHistory(q RepositoryHistoryQuery) (RepositoryHistoryPage, error) {
	return s.repositoryHistoryPage(q, true)
}

func (s *SessionStore) repositoryHistoryPage(q RepositoryHistoryQuery, programs bool) (RepositoryHistoryPage, error) {
	out := RepositoryHistoryPage{Sessions: []SessionRepositoryHistory{}, Programs: []TaskProgramRecord{}, HistoryCoverage: "retained_snapshots_worktree_provenance_and_indexed_contexts"}
	reader := s.store.db.NewSnapshot()
	defer reader.Close()
	if err := repositoryHistoryOwner(reader, q); err != nil {
		return out, err
	}
	var meta repositoryHistoryMeta
	ok, err := getJSONFromReader(reader, repositoryHistoryMetaKey, &meta)
	if err != nil {
		return out, err
	}
	if !ok || !meta.Ready {
		return out, ErrRepositoryHistoryNotReady
	}
	block, err := aes.NewCipher(meta.Secret)
	if err != nil {
		return out, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return out, err
	}
	revision, closer, err := reader.Get([]byte(repositoryHistoryRevisionKey + "/" + keyPart(q.ParentSessionID)))
	if err != nil {
		return out, err
	}
	rev := hex.EncodeToString(revision)
	closer.Close()
	prefix := repositoryHistoryPrefix(q.AccountScopeID, q.UserID, q.ParentSessionID)
	scope := prefix
	if programs {
		prefix = TaskProgramSessionPrefix(q.ParentSessionID)
		scope += "programs"
	}
	cursor := repositoryHistoryCursor{Scope: scope, Revision: rev, Limit: q.Limit}
	if q.Cursor != "" {
		if len(q.Cursor) > 12000 {
			return out, ErrRepositoryHistoryCursor
		}
		data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || len(data) < aead.NonceSize() || len(data) > 8192 {
			return out, ErrRepositoryHistoryCursor
		}
		plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], nil)
		if err != nil || json.Unmarshal(plain, &cursor) != nil || cursor.Scope != scope || cursor.Revision != rev || cursor.Limit != q.Limit || !strings.HasPrefix(cursor.After, prefix) {
			return out, ErrRepositoryHistoryCursor
		}
	}
	iter, err := reader.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return out, err
	}
	defer iter.Close()
	valid := iter.First()
	if cursor.After != "" {
		valid = iter.SeekGE([]byte(cursor.After))
		if valid && string(iter.Key()) == cursor.After {
			valid = iter.Next()
		}
	}
	for n := 0; valid && n < q.Limit; n++ {
		if programs {
			var row TaskProgramRecord
			if err := json.Unmarshal(iter.Value(), &row); err != nil {
				return RepositoryHistoryPage{}, err
			}
			if row.ParentSessionID != q.ParentSessionID {
				return RepositoryHistoryPage{}, fmt.Errorf("repository history program ownership mismatch")
			}
			if row.RepositoryLane != nil {
				value, closer, err := reader.Get([]byte(repositoryLaneKey(q.ParentSessionID, row.RepositoryLane.WorkspacePath)))
				if err != nil {
					return out, err
				}
				claimed := string(value) == string(iter.Key())
				closer.Close()
				if !claimed {
					row.RepositoryLane = nil
				}
			}
			out.Programs = append(out.Programs, row)
		} else {
			var row SessionRepositoryHistory
			if err := json.Unmarshal(iter.Value(), &row); err != nil {
				return RepositoryHistoryPage{}, err
			}
			if row.Session.AccountScopeID != q.AccountScopeID || row.Session.UserID != q.UserID {
				return RepositoryHistoryPage{}, errors.New("repository history row ownership mismatch")
			}
			// Claims point to the newest context for each logical attachment. This
			// bounds deduplication to this row's grants, never prior pages.
			grants := []WorkspaceGrant{}
			for _, grant := range repositoryRowGrants(row) {
				value, closer, err := reader.Get([]byte(repositoryHistoryClaimKey(repositoryHistoryPrefix(q.AccountScopeID, q.UserID, q.ParentSessionID), row.Session.ID, grant)))
				if err != nil {
					return out, err
				}
				claimed := string(value) == string(iter.Key())
				closer.Close()
				if claimed {
					grants = append(grants, grant)
				}
			}
			row.Grants = grants
			row.Projected = true
			var current SessionSnapshot
			found, err := getJSONFromReader(reader, KeySession(row.Session.ID), &current)
			if err != nil {
				return out, err
			}
			if found {
				row.Archived, row.Deleted = false, false
			}
			if !found {
				var tomb V3SessionTombstone
				found, err = getJSONFromReader(reader, KeyV3SessionTombstone(row.Session.ID), &tomb)
				if err != nil {
					return out, err
				}
				current = tomb.Session
				if found {
					if tomb.AccountScopeID != q.AccountScopeID || tomb.UserID != q.UserID {
						return out, errors.New("repository tombstone owner mismatch")
					}
					row.Archived, row.Deleted = tomb.Archived, tomb.Deleted
					if tomb.Deleted {
						current = row.Session
					}
				}
			}
			if found {
				row.Session.Lifecycle = current.Lifecycle
				if current.AccountScopeID != q.AccountScopeID || current.UserID != q.UserID {
					return out, errors.New("repository history current owner mismatch")
				}
				for _, name := range []string{"integration_status", "task_status"} {
					if row.Session.Metadata == nil {
						row.Session.Metadata = map[string]any{}
					}
					row.Session.Metadata[name] = current.Metadata[name]
				}
			}
			out.Sessions = append(out.Sessions, row)
		}
		cursor.After = string(iter.Key())
		valid = iter.Next()
	}
	if err := iter.Error(); err != nil {
		return RepositoryHistoryPage{}, err
	}
	if valid {
		plain, err := json.Marshal(cursor)
		if err != nil {
			return RepositoryHistoryPage{}, err
		}
		nonce := make([]byte, aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return RepositoryHistoryPage{}, err
		}
		out.NextCursor = base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, nil))
	}
	return out, nil
}

func (s *SessionStore) putTaskProgramHistory(record TaskProgramRecord) error {
	batch := s.store.NewBatch()
	defer batch.Close()
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := batch.Set([]byte(KeyTaskProgram(record.ParentSessionID, record.ProgramID)), payload, nil); err != nil {
		return err
	}
	var previous TaskProgramRecord
	found, err := getJSONFromReader(s.store.db, KeyTaskProgram(record.ParentSessionID, record.ProgramID), &previous)
	if err != nil {
		return err
	}
	before, err := json.Marshal(previous.RepositoryLane)
	if err != nil {
		return err
	}
	after, err := json.Marshal(record.RepositoryLane)
	if err != nil {
		return err
	}
	previousJobs, err := json.Marshal(previous.Jobs)
	if err != nil {
		return err
	}
	nextJobs, err := json.Marshal(record.Jobs)
	if err != nil {
		return err
	}
	if !found || previous.State != record.State || !bytes.Equal(before, after) || !bytes.Equal(previousJobs, nextJobs) {
		if err := s.indexRepositoryLane(batch, record); err != nil {
			return err
		}
		if err := repositoryHistoryRevision(batch, record.ParentSessionID); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

// CompleteRepositoryHistoryMaintenance is startup-only, resumable maintenance.
// Cancellation is checked between atomic batches of at most 100 durable rows.
func (s *SessionStore) CompleteRepositoryHistoryMaintenance(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, err := s.BackfillRepositoryHistory(100)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
	}
}

func repositoryHistoryPhase(session SessionSnapshot) string {
	if session.Lifecycle != nil {
		return session.Lifecycle.Phase
	}
	return ""
}

func repositoryHistoryMeaning(row SessionRepositoryHistory) any {
	return struct {
		Context                                       string
		Phase                                         string
		Historical                                    bool
		Archived, Deleted                             bool
		Integration, Task, SourceID, SourceGeneration string
	}{row.ContextID, repositoryHistoryPhase(row.Session), row.HistoricalWorktree, row.Archived, row.Deleted,
		v3LibraryMetadataString(row.Session.Metadata, "integration_status"),
		v3LibraryMetadataString(row.Session.Metadata, "task_status"),
		v3LibraryMetadataString(row.Session.Metadata, "swarm_v3_source_workspace_id"),
		v3LibraryMetadataString(row.Session.Metadata, "swarm_v3_source_workspace_generation")}
}

// repositoryHistoricalWorktrees projects only the provenance fields written by
// appendSessionWorktreeHistory. It never borrows the current default's identity
// for an old lane. Malformed/unowned entries confer no claim.
func repositoryHistoricalWorktrees(owner SessionSnapshot) []SessionSnapshot {
	items, _ := owner.Metadata["swarm_v3_worktree_history"].([]any)
	if len(items) > 64 {
		return nil
	}
	out := make([]SessionSnapshot, 0, len(items))
	for _, value := range items {
		item, ok := value.(map[string]any)
		if !ok || v3LibraryMetadataString(item, "owner_session_id") != owner.ID {
			continue
		}
		path := v3LibraryMetadataString(item, "path")
		source := v3LibraryMetadataString(item, "source_workspace_path")
		id := v3LibraryMetadataString(item, "workspace_id")
		branch := v3LibraryMetadataString(item, "branch")
		base := v3LibraryMetadataString(item, "base_commit")
		generation, err := strconv.ParseInt(fmt.Sprint(item["workspace_generation"]), 10, 64)
		if err != nil || generation < 1 || id == "" || branch == "" || base == "" || !filepath.IsAbs(path) || !filepath.IsAbs(source) || path == source || path == owner.WorktreeRootPath {
			continue
		}
		historical := owner
		historical.WorkspacePath, historical.WorktreeRootPath = path, path
		historical.WorktreeEnabled = true
		historical.WorktreeBranch = branch
		historical.WorktreeBaseBranch = v3LibraryMetadataString(item, "base_branch")
		historical.WorkspaceGrants = []WorkspaceGrant{
			{Kind: WorkspaceGrantAdditional, Path: source, WorkspaceID: id, WorkspaceGeneration: generation},
			{Kind: WorkspaceGrantWorktree, Path: path},
		}
		historical.Metadata = map[string]any{
			"parent_session_id":                    owner.Metadata["parent_session_id"],
			"base_commit":                          base,
			"swarm_v3_source_workspace_id":         id,
			"swarm_v3_source_workspace_generation": strconv.FormatInt(generation, 10),
			"swarm_v3_source_workspace_path":       source,
		}
		for _, name := range []string{"integration_status", "task_status", "task_program_id", "task_program_job_id", "parent_task_call_id"} {
			historical.Metadata[name] = owner.Metadata[name]
		}
		out = append(out, historical)
	}
	return out
}

func repositoryRowGrants(row SessionRepositoryHistory) []WorkspaceGrant {
	if row.HistoricalWorktree {
		return []WorkspaceGrant{{Kind: WorkspaceGrantWorktree, Path: row.Session.WorktreeRootPath}}
	}
	return repositoryHistoryGrants(row.Session)
}

func repositoryHistoryGrants(owner SessionSnapshot) []WorkspaceGrant {
	grants := NormalizeSessionWorkspaceGrants(owner)
	add := func(path, kind string) {
		if path == "" {
			return
		}
		for _, grant := range grants {
			if grant.Path == path {
				return
			}
		}
		grants = append(grants, WorkspaceGrant{Path: path, Kind: kind})
	}
	if owner.WorktreeEnabled {
		add(owner.WorktreeRootPath, WorkspaceGrantWorktree)
	}
	source := v3LibraryMetadataString(owner.Metadata, "swarm_v3_source_workspace_path")
	if source == "" {
		source = owner.WorkspacePath
	}
	add(source, WorkspaceGrantAdditional)
	seen := map[string]bool{}
	out := make([]WorkspaceGrant, 0, len(grants))
	for _, grant := range grants {
		key := grant.WorkspaceID + "\x00" + grant.Path
		if !seen[key] {
			out = append(out, grant)
			seen[key] = true
		}
	}
	return out
}

func repositoryHistoryClaimKey(prefix, owner string, grant WorkspaceGrant) string {
	// Kind (primary/additional) is mutable presentation, not attachment identity.
	digest := sha256.Sum256([]byte(owner + "\x00" + grant.WorkspaceID + "\x00" + grant.Path))
	return strings.Replace(prefix, "/rows/", "/claims/", 1) + hex.EncodeToString(digest[:])
}

func repositoryHistoryExactKey(prefix, path string) string {
	digest := sha256.Sum256([]byte(path))
	return strings.Replace(prefix, "/rows/", "/exact/", 1) + hex.EncodeToString(digest[:])
}

func repositoryLaneKey(parent, path string) string {
	digest := sha256.Sum256([]byte(path))
	return "v3/repository_history/lanes/" + keyPart(parent) + "/" + hex.EncodeToString(digest[:])
}

func (s *SessionStore) indexRepositoryLane(batch *pebble.Batch, record TaskProgramRecord) error {
	if record.RepositoryLane == nil {
		return nil
	}
	return batch.Set([]byte(repositoryLaneKey(record.ParentSessionID, record.RepositoryLane.WorkspacePath)), []byte(KeyTaskProgram(record.ParentSessionID, record.ProgramID)), nil)
}

// ExactRepositoryHistory performs direct indexed lookup only. Its evidence is
// not a path grant: the API must revalidate catalog and managed lane ownership.
func (s *SessionStore) ExactRepositoryHistory(q RepositoryHistoryQuery, path string) (RepositoryHistoryPage, error) {
	out := RepositoryHistoryPage{}
	reader := s.store.db.NewSnapshot()
	defer reader.Close()
	if err := repositoryHistoryOwner(reader, q); err != nil {
		return out, err
	}
	var meta repositoryHistoryMeta
	ok, err := getJSONFromReader(reader, repositoryHistoryMetaKey, &meta)
	if err != nil {
		return out, err
	}
	if !ok || !meta.Ready {
		return out, ErrRepositoryHistoryNotReady
	}
	prefix := repositoryHistoryPrefix(q.AccountScopeID, q.UserID, q.ParentSessionID)
	// A child can capture the parent's lane as its source. Prefer the exact
	// parent's worktree claim so that child's source cannot shadow lane identity.
	key, closer, err := reader.Get([]byte(repositoryHistoryClaimKey(prefix, q.ParentSessionID, WorkspaceGrant{Kind: WorkspaceGrantWorktree, Path: path})))
	if errors.Is(err, pebble.ErrNotFound) {
		key, closer, err = reader.Get([]byte(repositoryHistoryExactKey(prefix, path)))
	}
	if err == nil {
		rowKey := string(key)
		closer.Close()
		if !strings.HasPrefix(rowKey, prefix) {
			return out, errors.New("repository lookup scope mismatch")
		}
		var row SessionRepositoryHistory
		ok, err := getJSONFromReader(reader, rowKey, &row)
		if err != nil {
			return out, err
		}
		if !ok || row.Session.AccountScopeID != q.AccountScopeID || row.Session.UserID != q.UserID {
			return out, errors.New("repository lookup owner mismatch")
		}
		// Exact selectors must report the same durable deletion/archive state as
		// paginated inventory, not the stale state of the retained context.
		var current SessionSnapshot
		found, err := getJSONFromReader(reader, KeySession(row.Session.ID), &current)
		if err != nil {
			return out, err
		}
		if found {
			row.Archived, row.Deleted = false, false
		} else {
			var tomb V3SessionTombstone
			found, err = getJSONFromReader(reader, KeyV3SessionTombstone(row.Session.ID), &tomb)
			if err != nil {
				return out, err
			}
			if found {
				if tomb.AccountScopeID != q.AccountScopeID || tomb.UserID != q.UserID {
					return out, errors.New("repository tombstone owner mismatch")
				}
				current = tomb.Session
				row.Archived, row.Deleted = tomb.Archived, tomb.Deleted
				if tomb.Deleted {
					current = row.Session
				}
			}
		}
		if found {
			if current.AccountScopeID != q.AccountScopeID || current.UserID != q.UserID {
				return out, errors.New("repository lookup current owner mismatch")
			}
			row.Session.Lifecycle = current.Lifecycle
		}
		out.Sessions = append(out.Sessions, row)
		return out, nil
	}
	if !errors.Is(err, pebble.ErrNotFound) {
		return out, err
	}
	key, closer, err = reader.Get([]byte(repositoryLaneKey(q.ParentSessionID, path)))
	if errors.Is(err, pebble.ErrNotFound) {
		return out, errors.New("unknown session repository selector")
	}
	if err != nil {
		return out, err
	}
	rowKey := string(key)
	closer.Close()
	if !strings.HasPrefix(rowKey, TaskProgramSessionPrefix(q.ParentSessionID)) {
		return out, errors.New("repository lane scope mismatch")
	}
	var record TaskProgramRecord
	ok, err = getJSONFromReader(reader, rowKey, &record)
	if err != nil {
		return out, err
	}
	if !ok || record.ParentSessionID != q.ParentSessionID || record.RepositoryLane == nil || record.RepositoryLane.WorkspacePath != path {
		return out, errors.New("repository lane identity mismatch")
	}
	out.Programs = append(out.Programs, record)
	return out, nil
}

// RepositoryContinuation authenticates the entire HTTP continuation, including
// phase, inner cursor, context and offset. A supplied anchor must still match
// the current parent revision before a next-page token can be issued.
func (s *SessionStore) RepositoryContinuation(q RepositoryHistoryQuery, token string, payload []byte) ([]byte, string, error) {
	if len(token) > 24000 || len(payload) > 16000 {
		return nil, "", ErrRepositoryHistoryCursor
	}
	reader := s.store.db.NewSnapshot()
	defer reader.Close()
	if err := repositoryHistoryOwner(reader, q); err != nil {
		return nil, "", err
	}
	var meta repositoryHistoryMeta
	ok, err := getJSONFromReader(reader, repositoryHistoryMetaKey, &meta)
	if err != nil {
		return nil, "", err
	}
	if !ok || !meta.Ready {
		return nil, "", ErrRepositoryHistoryNotReady
	}
	block, err := aes.NewCipher(meta.Secret)
	if err != nil {
		return nil, "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}
	revision, closer, err := reader.Get([]byte(repositoryHistoryRevisionKey + "/" + keyPart(q.ParentSessionID)))
	if err != nil {
		return nil, "", err
	}
	rev := hex.EncodeToString(revision)
	closer.Close()
	scope := repositoryHistoryPrefix(q.AccountScopeID, q.UserID, q.ParentSessionID) + "http"
	envelope := repositoryHistoryCursor{Scope: scope, Revision: rev, Limit: q.Limit}
	var decoded []byte
	if token != "" {
		data, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(data) < aead.NonceSize() {
			return nil, "", ErrRepositoryHistoryCursor
		}
		plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], nil)
		if err != nil || json.Unmarshal(plain, &envelope) != nil || envelope.Scope != scope || envelope.Revision != rev || envelope.Limit != q.Limit {
			return nil, "", ErrRepositoryHistoryCursor
		}
		decoded = []byte(envelope.After)
	}
	if payload != nil {
		envelope.After = string(payload)
	}
	plain, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	return decoded, base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, nil)), nil
}
