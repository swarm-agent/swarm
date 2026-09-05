package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var (
	ErrArtifactV3AuthorUnauthorized = errors.New("artifact v3 author: capability does not match")
	ErrArtifactV3AuthorExpired      = errors.New("artifact v3 author: capability expired")
	ErrArtifactV3AuthorInvalid      = errors.New("artifact v3 author: invalid request")
	ErrArtifactV3AuthorConflict     = errors.New("artifact v3 author: compare-and-swap conflict")
	ErrArtifactV3AuthorNotReady     = errors.New("artifact v3 author: complete project is not ready")
	ErrArtifactV3AuthorQuota        = errors.New("artifact v3 author: quota exceeded")
	ErrArtifactV3AuthorLocked       = errors.New("artifact v3 author: locked path cannot be changed")
)

const (
	artifactV3ActionInspect = "inspect_context"
	artifactV3ActionList    = "list_files"
	artifactV3ActionRead    = "read_file"
	artifactV3ActionCreate  = "create_file"
	artifactV3ActionEdit    = "edit_file"
	artifactV3ActionRename  = "rename_file"
	artifactV3ActionDelete  = "delete_file"
	artifactV3ActionDiff    = "diff"
	artifactV3ActionBuild   = "build_preview"
	artifactV3ActionFinish  = "finish_turn"
)

type ArtifactV3AuthorLimits struct {
	MaxFileBytes, MaxTreeBytes                                                      int64
	MaxFiles, MaxPathBytes, MaxPathDepth, MaxListPage, MaxReadBytes, MaxDiffEntries int
}

func (l ArtifactV3AuthorLimits) normalized() ArtifactV3AuthorLimits {
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = 8 << 20
	}
	if l.MaxTreeBytes <= 0 {
		l.MaxTreeBytes = 64 << 20
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = 4096
	}
	if l.MaxPathBytes <= 0 {
		l.MaxPathBytes = 512
	}
	if l.MaxPathDepth <= 0 {
		l.MaxPathDepth = 32
	}
	if l.MaxListPage <= 0 {
		l.MaxListPage = 500
	}
	if l.MaxReadBytes <= 0 {
		l.MaxReadBytes = 1 << 20
	}
	if l.MaxDiffEntries <= 0 {
		l.MaxDiffEntries = 1000
	}
	return l
}

type ArtifactV3AuthorGrant struct {
	ID, ArtifactID, OwnerSessionID, ProducerSessionID, ProducerRunID string
	TurnID, CandidateID, BaseCommitOID, PolicyRevision               string
	Initial                                                          bool
	AllowedActions, TargetPartIDs, LockedPaths                       []string
	ExpiresAt                                                        int64
	Limits                                                           ArtifactV3AuthorLimits
}

func (g ArtifactV3AuthorGrant) Allows(action string) bool {
	for _, allowed := range g.AllowedActions {
		if strings.EqualFold(strings.TrimSpace(allowed), strings.TrimSpace(action)) {
			return true
		}
	}
	return false
}

type ArtifactV3AuthorRunContext struct{ Grant ArtifactV3AuthorGrant }

func BindArtifactV3AuthorRunContext(input *ArtifactV3AuthorRunContext, producerRunID string) *ArtifactV3AuthorRunContext {
	if input == nil {
		return nil
	}
	cloned := *input
	cloned.Grant.AllowedActions = append([]string(nil), input.Grant.AllowedActions...)
	cloned.Grant.TargetPartIDs = append([]string(nil), input.Grant.TargetPartIDs...)
	cloned.Grant.LockedPaths = append([]string(nil), input.Grant.LockedPaths...)
	cloned.Grant.ProducerRunID = strings.TrimSpace(producerRunID)
	return &cloned
}

type artifactV3AuthorContextKey struct{}

func WithArtifactV3AuthorRunContext(parent context.Context, run ArtifactV3AuthorRunContext) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithValue(parent, artifactV3AuthorContextKey{}, run)
}

type ArtifactV3Diagnostic struct {
	Stage, Code, Message, Path string
	Line                       int
}
type ArtifactV3BuildRequest struct {
	ArtifactID, TurnID, PolicyRevision string
	Attempt                            int
	Project                            map[string][]byte
}
type ArtifactV3BuildResult struct {
	ID, Status  string
	OutputFiles map[string][]byte
	Diagnostics []ArtifactV3Diagnostic
}
type ArtifactV3PreviewRequest struct {
	ArtifactID, TurnID, PolicyRevision string
	Attempt                            int
	Project                            map[string][]byte
	Build                              ArtifactV3BuildResult
	TargetPartIDs                      []string
}
type ArtifactV3PreviewResult struct {
	ID, Status      string
	EvidenceDigests []string
	Diagnostics     []ArtifactV3Diagnostic
}
type ArtifactV3SubmitRequest struct {
	ArtifactID, TurnID, CandidateID, BaseCommitOID, PolicyRevision, ProjectDigest string
	Initial                                                                       bool
	Project                                                                       map[string][]byte
	Build                                                                         ArtifactV3BuildResult
	Preview                                                                       ArtifactV3PreviewResult
}
type ArtifactV3Revision struct{ CommitOID, TreeOID, ManifestBlobOID string }

type ArtifactV3PrepareTurnRequest struct {
	AccountScopeID, UserID, OwnerSessionID, TaskCallID, Prompt string
	ArtifactID, BaseCommitOID, PolicyRevision                  string
	ProjectionSeq                                              uint64
	CandidateIndex                                             int
	Initial                                                    bool
	TargetPartIDs, LockedPaths                                 []string
	ExpiresAt                                                  int64
}

type ArtifactV3TurnFailure struct {
	ArtifactID, TurnID, CandidateID, ProducerSessionID, ProducerRunID string
	Code, Message                                                     string
}

// ArtifactV3TurnCoordinator is implemented by the durable Git/Pebble V3
// repository. Preparation authenticates or allocates the exact artifact/head
// before a child exists; failure records diagnostics without moving its head.
type ArtifactV3TurnCoordinator interface {
	PrepareArtifactV3Turn(context.Context, ArtifactV3PrepareTurnRequest) (ArtifactV3AuthorGrant, error)
	FailArtifactV3Turn(context.Context, ArtifactV3TurnFailure) error
}

// ArtifactV3AuthorRepository is a V3-only adapter. Implementations materialize
// an exact Git base and submit one complete project tree; they never stitch Parts.
type ArtifactV3AuthorRepository interface {
	MaterializeBase(context.Context, string, string, string) error
	SubmitProject(context.Context, ArtifactV3SubmitRequest) (ArtifactV3Revision, error)
}
type ArtifactV3Builder interface {
	Build(context.Context, ArtifactV3BuildRequest) (ArtifactV3BuildResult, error)
}
type ArtifactV3Previewer interface {
	Preview(context.Context, ArtifactV3PreviewRequest) (ArtifactV3PreviewResult, error)
}
type ArtifactV3PreviewEvidenceReader interface {
	ReadArtifactV3PreviewEvidence(context.Context, string, string, string, string, string) ([]byte, error)
}
type ArtifactV3DirectHeadSelector interface {
	SelectArtifactV3DirectHead(context.Context, string, string, string, string, string, string) (ArtifactV3Revision, error)
}
type ArtifactV3DirectRevisionReader interface {
	ReadArtifactV3DirectRevision(context.Context, string, string, string, string, string) (map[string][]byte, []pebblestore.ArtifactV3Part, error)
}

type ArtifactV3AuthorFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}
type ArtifactV3AuthorFilePage struct {
	Files      []ArtifactV3AuthorFile `json:"files"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}
type ArtifactV3AuthorRead struct {
	Path, Content                  string
	Offset, NextOffset, TotalBytes int
	EOF                            bool
}
type ArtifactV3AuthorChange struct {
	Path, Status            string
	BeforeBytes, AfterBytes int64
}
type ArtifactV3AuthorDiff struct {
	Changes   []ArtifactV3AuthorChange
	Truncated bool
}
type ArtifactV3AuthorGate struct {
	Attempt       int
	ProjectDigest string
	Ready         bool
	Build         ArtifactV3BuildResult
	Preview       ArtifactV3PreviewResult
	Diagnostics   []ArtifactV3Diagnostic
}
type ArtifactV3AuthorFinish struct {
	Revision ArtifactV3Revision
	Gate     ArtifactV3AuthorGate
}
type ArtifactV3AuthorContext struct {
	ArtifactID, TurnID, CandidateID, BaseCommitOID, PolicyRevision string
	Initial                                                        bool
	TargetPartIDs, LockedPaths                                     []string
	Files                                                          []ArtifactV3AuthorFile
	LatestGate                                                     *ArtifactV3AuthorGate
}

type artifactV3TurnState struct {
	root     string
	base     map[string][]byte
	attempt  int
	gate     *ArtifactV3AuthorGate
	finished *ArtifactV3AuthorFinish
}
type ArtifactV3AuthorService struct {
	root           string
	repository     ArtifactV3AuthorRepository
	builder        ArtifactV3Builder
	previewer      ArtifactV3Previewer
	now            func() time.Time
	mu             sync.Mutex
	operationLocks map[string]*artifactV3OperationLock
	turns          map[string]*artifactV3TurnState
}

// Serialize all operations within one candidate, including read/modify/write,
// gate creation, finish and discard. Independent sibling candidates retain
// parallelism. Count holders and waiters before locking so discard cannot split
// lock identity; release idle locks instead of leaking one per historical turn.
type artifactV3OperationLock struct {
	mu    sync.Mutex
	users int
}

func (s *ArtifactV3AuthorService) lockTurn(grant ArtifactV3AuthorGrant) func() {
	if s == nil {
		return func() {}
	}
	key := artifactV3WorkspaceKey(grant)
	s.mu.Lock()
	if s.operationLocks == nil {
		s.operationLocks = make(map[string]*artifactV3OperationLock)
	}
	lock := s.operationLocks[key]
	if lock == nil {
		lock = &artifactV3OperationLock{}
		s.operationLocks[key] = lock
	}
	lock.users++
	s.mu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.mu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(s.operationLocks, key)
		}
		s.mu.Unlock()
	}
}

func NewArtifactV3AuthorService(root string, repository ArtifactV3AuthorRepository, builder ArtifactV3Builder, previewer ArtifactV3Previewer) *ArtifactV3AuthorService {
	root = strings.TrimSpace(root)
	if root != "" {
		root = filepath.Clean(root)
	}
	return &ArtifactV3AuthorService{root: root, repository: repository, builder: builder, previewer: previewer, now: time.Now, turns: map[string]*artifactV3TurnState{}}
}

func artifactV3AuthorDefinition() Definition {
	return Definition{Type: "function", Name: "artifact_v3_author", Description: "Context-bound whole-project Artifact V3 authoring. Operates on the complete exact base tree with ordinary file operations, repeated server-owned build/browser preview gates, and one final complete candidate. Targets express user intent and do not restrict coherent cross-project edits; only server-locked paths are immutable. Destination, repository, refs, policy, build commands, and output paths are injected and cannot be caller supplied.", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{artifactV3ActionInspect, artifactV3ActionList, artifactV3ActionRead, artifactV3ActionCreate, artifactV3ActionEdit, artifactV3ActionRename, artifactV3ActionDelete, artifactV3ActionDiff, artifactV3ActionBuild, artifactV3ActionFinish}},
			"path":   map[string]any{"type": "string", "maxLength": 512}, "to_path": map[string]any{"type": "string", "maxLength": 512}, "content": map[string]any{"type": "string"}, "old_string": map[string]any{"type": "string", "description": "Exact literal substring from decoded read_file Content, not JSON escape notation. Must match once unless replace_all is true. On mismatch, read again before retrying; no mutation occurs."}, "new_string": map[string]any{"type": "string"}, "replace_all": map[string]any{"type": "boolean"}, "cursor": map[string]any{"type": "string", "maxLength": 1024}, "limit": map[string]any{"type": "integer", "minimum": 1}, "offset": map[string]any{"type": "integer", "minimum": 0},
		}, "required": []string{"action"}, "additionalProperties": false,
	}}
}

// File payloads are bytes, not identifiers: never trim meaningful whitespace.
func artifactV3LiteralString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func (r *Runtime) SetArtifactV3AuthorService(service *ArtifactV3AuthorService) {
	if r != nil {
		r.artifactV3Author = service
	}
}
func (r *Runtime) ArtifactV3AuthorService() *ArtifactV3AuthorService {
	if r == nil {
		return nil
	}
	return r.artifactV3Author
}
func (s *ArtifactV3AuthorService) ReadPreviewEvidence(ctx context.Context, accountScopeID, userID, sessionID, artifactID, revisionRef string) ([]byte, error) {
	if s == nil || s.repository == nil {
		return nil, errors.New("artifact v3 author: repository is not configured")
	}
	reader, ok := s.repository.(ArtifactV3PreviewEvidenceReader)
	if !ok {
		return nil, errors.New("artifact v3 author: preview evidence reader is not configured")
	}
	return reader.ReadArtifactV3PreviewEvidence(ctx, accountScopeID, userID, sessionID, artifactID, revisionRef)
}

func (s *ArtifactV3AuthorService) PrepareTurn(ctx context.Context, request ArtifactV3PrepareTurnRequest) (ArtifactV3AuthorGrant, error) {
	if s == nil || s.repository == nil {
		return ArtifactV3AuthorGrant{}, errors.New("artifact v3 author: repository is not configured")
	}
	coordinator, ok := s.repository.(ArtifactV3TurnCoordinator)
	if !ok {
		return ArtifactV3AuthorGrant{}, errors.New("artifact v3 author: durable turn coordinator is not configured")
	}
	grant, err := coordinator.PrepareArtifactV3Turn(ctx, request)
	if err != nil {
		return ArtifactV3AuthorGrant{}, err
	}
	if strings.TrimSpace(grant.ID) == "" || strings.TrimSpace(grant.ArtifactID) == "" || strings.TrimSpace(grant.TurnID) == "" || strings.TrimSpace(grant.CandidateID) == "" || strings.TrimSpace(grant.OwnerSessionID) == "" || strings.TrimSpace(grant.PolicyRevision) == "" || grant.ExpiresAt <= s.now().UnixMilli() {
		return ArtifactV3AuthorGrant{}, errors.New("artifact v3 author: coordinator returned an incomplete grant")
	}
	grant.TargetPartIDs = append([]string(nil), grant.TargetPartIDs...)
	grant.LockedPaths = append([]string(nil), grant.LockedPaths...)
	grant.AllowedActions = append([]string(nil), grant.AllowedActions...)
	return grant, nil
}

func (s *ArtifactV3AuthorService) MarkFailed(ctx context.Context, grant ArtifactV3AuthorGrant, code, message string) error {
	if s == nil || s.repository == nil {
		return errors.New("artifact v3 author: repository is not configured")
	}
	coordinator, ok := s.repository.(ArtifactV3TurnCoordinator)
	if !ok {
		return errors.New("artifact v3 author: durable turn coordinator is not configured")
	}
	if strings.TrimSpace(grant.ArtifactID) == "" || strings.TrimSpace(grant.TurnID) == "" || strings.TrimSpace(grant.CandidateID) == "" || strings.TrimSpace(grant.ProducerSessionID) == "" || strings.TrimSpace(grant.ProducerRunID) == "" || strings.TrimSpace(code) == "" {
		return ErrArtifactV3AuthorUnauthorized
	}
	return coordinator.FailArtifactV3Turn(ctx, ArtifactV3TurnFailure{
		ArtifactID: grant.ArtifactID, TurnID: grant.TurnID, CandidateID: grant.CandidateID,
		ProducerSessionID: grant.ProducerSessionID, ProducerRunID: grant.ProducerRunID,
		Code: strings.TrimSpace(code), Message: strings.TrimSpace(message),
	})
}

func (s *ArtifactV3AuthorService) Finished(grant ArtifactV3AuthorGrant) (ArtifactV3AuthorFinish, bool) {
	defer s.lockTurn(grant)()
	if s == nil {
		return ArtifactV3AuthorFinish{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.turns[artifactV3WorkspaceKey(grant)]
	if state == nil || state.finished == nil {
		return ArtifactV3AuthorFinish{}, false
	}
	return *state.finished, true
}

// Discard removes only the ephemeral authoring workspace. Durable turn failure
// and candidate diagnostics remain owned by the coordinator/repository.
func (s *ArtifactV3AuthorService) Discard(grant ArtifactV3AuthorGrant) error {
	defer s.lockTurn(grant)()
	if s == nil {
		return nil
	}
	key := artifactV3WorkspaceKey(grant)
	s.mu.Lock()
	state := s.turns[key]
	delete(s.turns, key)
	s.mu.Unlock()
	if state == nil || strings.TrimSpace(state.root) == "" {
		return nil
	}
	return os.RemoveAll(state.root)
}

func (r *Runtime) executeArtifactV3Author(ctx context.Context, scope WorkspaceScope, callID string, args map[string]any) (string, error) {
	if r == nil || r.artifactV3Author == nil {
		return "", errors.New("artifact_v3_author service is not configured")
	}
	run, ok := ctx.Value(artifactV3AuthorContextKey{}).(ArtifactV3AuthorRunContext)
	if !ok || strings.TrimSpace(run.Grant.ID) == "" {
		return "", errors.New("artifact_v3_author requires trusted context-bound capability")
	}
	if strings.TrimSpace(scope.SessionID) != strings.TrimSpace(run.Grant.ProducerSessionID) || strings.TrimSpace(scope.Principal.AccountScopeID) == "" || strings.TrimSpace(scope.Principal.UserID) == "" {
		return "", errors.New("artifact_v3_author producer or authenticated principal does not match the capability")
	}
	action := strings.TrimSpace(mapString(args, "action"))
	principal := ArtifactV3AuthorPrincipal{AccountScopeID: scope.Principal.AccountScopeID, UserID: scope.Principal.UserID, ProducerSessionID: scope.SessionID, ProducerRunID: run.Grant.ProducerRunID}
	var result any
	var err error
	switch action {
	case artifactV3ActionInspect:
		if err = requireOnlyArtifactV3Fields(args, "action"); err == nil {
			result, err = r.artifactV3Author.Inspect(ctx, principal, run.Grant)
		}
	case artifactV3ActionList:
		if err = requireOnlyArtifactV3Fields(args, "action", "cursor", "limit"); err == nil {
			result, err = r.artifactV3Author.List(ctx, principal, run.Grant, mapString(args, "cursor"), int(nonnegativeUint64(args["limit"])))
		}
	case artifactV3ActionRead:
		if err = requireOnlyArtifactV3Fields(args, "action", "path", "offset", "limit"); err == nil {
			result, err = r.artifactV3Author.Read(ctx, principal, run.Grant, mapString(args, "path"), int(nonnegativeUint64(args["offset"])), int(nonnegativeUint64(args["limit"])))
		}
	case artifactV3ActionCreate:
		if err = requireOnlyArtifactV3Fields(args, "action", "path", "content"); err == nil {
			err = r.artifactV3Author.Create(ctx, principal, run.Grant, mapString(args, "path"), []byte(artifactV3LiteralString(args, "content")))
			result = map[string]any{"path": mapString(args, "path")}
		}
	case artifactV3ActionEdit:
		if err = requireOnlyArtifactV3Fields(args, "action", "path", "old_string", "new_string", "replace_all"); err == nil {
			replaceAll, _ := args["replace_all"].(bool)
			err = r.artifactV3Author.Edit(ctx, principal, run.Grant, mapString(args, "path"), []byte(artifactV3LiteralString(args, "old_string")), []byte(artifactV3LiteralString(args, "new_string")), replaceAll)
			result = map[string]any{"path": mapString(args, "path")}
		}
	case artifactV3ActionRename:
		if err = requireOnlyArtifactV3Fields(args, "action", "path", "to_path"); err == nil {
			err = r.artifactV3Author.Rename(ctx, principal, run.Grant, mapString(args, "path"), mapString(args, "to_path"))
			result = map[string]any{"path": mapString(args, "to_path")}
		}
	case artifactV3ActionDelete:
		if err = requireOnlyArtifactV3Fields(args, "action", "path"); err == nil {
			err = r.artifactV3Author.Delete(ctx, principal, run.Grant, mapString(args, "path"))
			result = map[string]any{"path": mapString(args, "path")}
		}
	case artifactV3ActionDiff:
		if err = requireOnlyArtifactV3Fields(args, "action"); err == nil {
			result, err = r.artifactV3Author.Diff(ctx, principal, run.Grant)
		}
	case artifactV3ActionBuild:
		if err = requireOnlyArtifactV3Fields(args, "action"); err == nil {
			result, err = r.artifactV3Author.BuildPreview(ctx, principal, run.Grant)
		}
	case artifactV3ActionFinish:
		if err = requireOnlyArtifactV3Fields(args, "action"); err == nil {
			result, err = r.artifactV3Author.Finish(ctx, principal, run.Grant)
		}
	default:
		err = fmt.Errorf("artifact_v3_author action %q is unsupported", action)
	}
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{"tool": "artifact_v3_author", "action": action, "status": "ok", "result": result})
	if err != nil {
		return "", err
	}
	_ = callID
	return string(payload), nil
}

type ArtifactV3AuthorPrincipal struct{ AccountScopeID, UserID, ProducerSessionID, ProducerRunID string }

func requireOnlyArtifactV3Fields(args map[string]any, allowed ...string) error {
	set := map[string]bool{}
	for _, key := range allowed {
		set[key] = true
	}
	for key := range args {
		if !set[key] {
			return fmt.Errorf("artifact_v3_author rejects caller-authored field %q", key)
		}
	}
	return nil
}

func (s *ArtifactV3AuthorService) Inspect(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant) (ArtifactV3AuthorContext, error) {
	defer s.lockTurn(g)()
	state, err := s.state(ctx, p, g, artifactV3ActionInspect)
	if err != nil {
		return ArtifactV3AuthorContext{}, err
	}
	files, err := artifactV3Snapshot(state.root, g.Limits.normalized())
	if err != nil {
		return ArtifactV3AuthorContext{}, err
	}
	return artifactV3Context(g, files, state.gate), nil
}
func (s *ArtifactV3AuthorService) List(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant, cursor string, limit int) (ArtifactV3AuthorFilePage, error) {
	defer s.lockTurn(g)()
	state, err := s.state(ctx, p, g, artifactV3ActionList)
	if err != nil {
		return ArtifactV3AuthorFilePage{}, err
	}
	files, err := artifactV3Snapshot(state.root, g.Limits.normalized())
	if err != nil {
		return ArtifactV3AuthorFilePage{}, err
	}
	paths := artifactV3Paths(files)
	start := 0
	if cursor != "" {
		start = sort.SearchStrings(paths, cursor)
		for start < len(paths) && paths[start] <= cursor {
			start++
		}
	}
	max := g.Limits.normalized().MaxListPage
	if limit <= 0 || limit > max {
		limit = max
	}
	end := min(start+limit, len(paths))
	page := ArtifactV3AuthorFilePage{}
	for _, path := range paths[start:end] {
		page.Files = append(page.Files, ArtifactV3AuthorFile{Path: path, Size: int64(len(files[path]))})
	}
	if end < len(paths) && len(page.Files) > 0 {
		page.NextCursor = page.Files[len(page.Files)-1].Path
	}
	return page, nil
}
func (s *ArtifactV3AuthorService) Read(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant, path string, offset, limit int) (ArtifactV3AuthorRead, error) {
	defer s.lockTurn(g)()
	state, err := s.state(ctx, p, g, artifactV3ActionRead)
	if err != nil {
		return ArtifactV3AuthorRead{}, err
	}
	clean, err := artifactV3CleanPath(path, g.Limits.normalized())
	if err != nil {
		return ArtifactV3AuthorRead{}, err
	}
	body, err := artifactV3ReadRegular(state.root, clean)
	if err != nil {
		return ArtifactV3AuthorRead{}, err
	}
	if offset < 0 || offset > len(body) {
		return ArtifactV3AuthorRead{}, ErrArtifactV3AuthorInvalid
	}
	max := g.Limits.normalized().MaxReadBytes
	if limit <= 0 || limit > max {
		limit = max
	}
	end := min(offset+limit, len(body))
	return ArtifactV3AuthorRead{Path: clean, Content: string(body[offset:end]), Offset: offset, NextOffset: end, TotalBytes: len(body), EOF: end == len(body)}, nil
}
func (s *ArtifactV3AuthorService) Create(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant, path string, body []byte) error {
	defer s.lockTurn(g)()
	state, err := s.state(ctx, p, g, artifactV3ActionCreate)
	if err != nil {
		return err
	}
	clean, err := artifactV3CleanPath(path, g.Limits.normalized())
	if err != nil {
		return err
	}
	if artifactV3Locked(g, clean) {
		return ErrArtifactV3AuthorLocked
	}
	if _, err = os.Lstat(filepath.Join(state.root, filepath.FromSlash(clean))); err == nil || !errors.Is(err, os.ErrNotExist) {
		return ErrArtifactV3AuthorConflict
	}
	if err = artifactV3WriteRegular(state.root, clean, body, true, g.Limits.normalized()); err == nil {
		s.invalidate(state)
	}
	return err
}
func (s *ArtifactV3AuthorService) Edit(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant, path string, old, replacement []byte, all bool) error {
	defer s.lockTurn(g)()
	state, err := s.state(ctx, p, g, artifactV3ActionEdit)
	if err != nil {
		return err
	}
	clean, err := artifactV3CleanPath(path, g.Limits.normalized())
	if err != nil {
		return err
	}
	if artifactV3Locked(g, clean) {
		return ErrArtifactV3AuthorLocked
	}
	body, err := artifactV3ReadRegular(state.root, clean)
	if err != nil {
		return err
	}
	if len(old) == 0 {
		return ErrArtifactV3AuthorInvalid
	}
	count := bytes.Count(body, old)
	if count == 0 || (!all && count != 1) {
		return fmt.Errorf("%w: edit_file old_string matched %d times; require exactly one literal match (or replace_all for multiple). Read the current file, decode its JSON Content string, and retry with an exact substring; no file was changed", ErrArtifactV3AuthorConflict, count)
	}
	n := 1
	if all {
		n = -1
	}
	if err = artifactV3WriteRegular(state.root, clean, bytes.Replace(body, old, replacement, n), false, g.Limits.normalized()); err == nil {
		s.invalidate(state)
	}
	return err
}
func (s *ArtifactV3AuthorService) Rename(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant, from, to string) error {
	defer s.lockTurn(g)()
	state, err := s.state(ctx, p, g, artifactV3ActionRename)
	if err != nil {
		return err
	}
	limits := g.Limits.normalized()
	source, err := artifactV3CleanPath(from, limits)
	if err != nil {
		return err
	}
	destination, err := artifactV3CleanPath(to, limits)
	if err != nil {
		return err
	}
	if artifactV3Locked(g, source) || artifactV3Locked(g, destination) {
		return ErrArtifactV3AuthorLocked
	}
	if _, err = artifactV3ReadRegular(state.root, source); err != nil {
		return err
	}
	if _, err = os.Lstat(filepath.Join(state.root, filepath.FromSlash(destination))); err == nil || !errors.Is(err, os.ErrNotExist) {
		return ErrArtifactV3AuthorConflict
	}
	if err = artifactV3SecureParents(state.root, destination); err != nil {
		return err
	}
	if err = os.Rename(filepath.Join(state.root, filepath.FromSlash(source)), filepath.Join(state.root, filepath.FromSlash(destination))); err == nil {
		s.invalidate(state)
	}
	return err
}
func (s *ArtifactV3AuthorService) Delete(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant, path string) error {
	defer s.lockTurn(g)()
	state, err := s.state(ctx, p, g, artifactV3ActionDelete)
	if err != nil {
		return err
	}
	clean, err := artifactV3CleanPath(path, g.Limits.normalized())
	if err != nil {
		return err
	}
	if artifactV3Locked(g, clean) {
		return ErrArtifactV3AuthorLocked
	}
	if _, err = artifactV3ReadRegular(state.root, clean); err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(state.root, filepath.FromSlash(clean))); err == nil {
		s.invalidate(state)
	}
	return err
}
func (s *ArtifactV3AuthorService) Diff(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant) (ArtifactV3AuthorDiff, error) {
	defer s.lockTurn(g)()
	state, err := s.state(ctx, p, g, artifactV3ActionDiff)
	if err != nil {
		return ArtifactV3AuthorDiff{}, err
	}
	current, err := artifactV3Snapshot(state.root, g.Limits.normalized())
	if err != nil {
		return ArtifactV3AuthorDiff{}, err
	}
	set := map[string]bool{}
	for path := range state.base {
		set[path] = true
	}
	for path := range current {
		set[path] = true
	}
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := ArtifactV3AuthorDiff{}
	for _, path := range paths {
		before, bok := state.base[path]
		after, aok := current[path]
		if bok && aok && bytes.Equal(before, after) {
			continue
		}
		if len(result.Changes) >= g.Limits.normalized().MaxDiffEntries {
			result.Truncated = true
			break
		}
		status := "modified"
		if !bok {
			status = "added"
		} else if !aok {
			status = "deleted"
		}
		result.Changes = append(result.Changes, ArtifactV3AuthorChange{Path: path, Status: status, BeforeBytes: int64(len(before)), AfterBytes: int64(len(after))})
	}
	return result, nil
}
func (s *ArtifactV3AuthorService) BuildPreview(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant) (ArtifactV3AuthorGate, error) {
	defer s.lockTurn(g)()
	state, err := s.state(ctx, p, g, artifactV3ActionBuild)
	if err != nil {
		return ArtifactV3AuthorGate{}, err
	}
	project, err := artifactV3Snapshot(state.root, g.Limits.normalized())
	if err != nil {
		return ArtifactV3AuthorGate{}, err
	}
	if len(project) == 0 {
		return ArtifactV3AuthorGate{}, ErrArtifactV3AuthorInvalid
	}
	state.attempt++
	gate := ArtifactV3AuthorGate{Attempt: state.attempt, ProjectDigest: artifactV3Digest(project)}
	if s.builder == nil {
		gate.Diagnostics = append(gate.Diagnostics, ArtifactV3Diagnostic{Stage: "build", Code: "builder_unavailable", Message: "trusted whole-project builder is not configured"})
		state.gate = &gate
		return gate, nil
	}
	gate.Build, err = s.builder.Build(ctx, ArtifactV3BuildRequest{ArtifactID: g.ArtifactID, TurnID: g.TurnID, PolicyRevision: g.PolicyRevision, Attempt: state.attempt, Project: artifactV3Clone(project)})
	if err != nil {
		gate.Diagnostics = append(gate.Diagnostics, artifactV3SafeDiagnostic("build", err))
		state.gate = &gate
		return gate, nil
	}
	gate.Diagnostics = append(gate.Diagnostics, gate.Build.Diagnostics...)
	if gate.Build.Status != "succeeded" {
		state.gate = &gate
		return gate, nil
	}
	if s.previewer == nil {
		gate.Diagnostics = append(gate.Diagnostics, ArtifactV3Diagnostic{Stage: "preview", Code: "previewer_unavailable", Message: "trusted browser preview gate is not configured"})
		state.gate = &gate
		return gate, nil
	}
	gate.Preview, err = s.previewer.Preview(ctx, ArtifactV3PreviewRequest{ArtifactID: g.ArtifactID, TurnID: g.TurnID, PolicyRevision: g.PolicyRevision, Attempt: state.attempt, Project: artifactV3Clone(project), Build: gate.Build, TargetPartIDs: append([]string(nil), g.TargetPartIDs...)})
	if err != nil {
		gate.Diagnostics = append(gate.Diagnostics, artifactV3SafeDiagnostic("preview", err))
		state.gate = &gate
		return gate, nil
	}
	gate.Diagnostics = append(gate.Diagnostics, gate.Preview.Diagnostics...)
	gate.Ready = gate.Preview.Status == "valid" && len(gate.Preview.EvidenceDigests) > 0
	state.gate = &gate
	return gate, nil
}
func (s *ArtifactV3AuthorService) Finish(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant) (ArtifactV3AuthorFinish, error) {
	defer s.lockTurn(g)()
	state, err := s.state(ctx, p, g, artifactV3ActionFinish)
	if err != nil {
		return ArtifactV3AuthorFinish{}, err
	}
	if state.finished != nil {
		return *state.finished, nil
	}
	project, err := artifactV3Snapshot(state.root, g.Limits.normalized())
	if err != nil {
		return ArtifactV3AuthorFinish{}, err
	}
	if len(project) == 0 || state.gate == nil || !state.gate.Ready || state.gate.ProjectDigest != artifactV3Digest(project) {
		return ArtifactV3AuthorFinish{}, ErrArtifactV3AuthorNotReady
	}
	if s.repository == nil {
		return ArtifactV3AuthorFinish{}, errors.New("artifact v3 author: repository is not configured")
	}
	revision, err := s.repository.SubmitProject(ctx, ArtifactV3SubmitRequest{ArtifactID: g.ArtifactID, TurnID: g.TurnID, CandidateID: g.CandidateID, BaseCommitOID: g.BaseCommitOID, PolicyRevision: g.PolicyRevision, ProjectDigest: state.gate.ProjectDigest, Initial: g.Initial, Project: artifactV3Clone(project), Build: state.gate.Build, Preview: state.gate.Preview})
	if err != nil {
		return ArtifactV3AuthorFinish{}, err
	}
	if strings.TrimSpace(revision.CommitOID) == "" || strings.TrimSpace(revision.TreeOID) == "" {
		return ArtifactV3AuthorFinish{}, errors.New("artifact v3 author: repository returned an inexact revision")
	}
	result := ArtifactV3AuthorFinish{Revision: revision, Gate: *state.gate}
	state.finished = &result
	return result, nil
}
func (s *ArtifactV3AuthorService) state(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant, action string) (*artifactV3TurnState, error) {
	if s == nil || s.root == "" || g.ID == "" || g.ArtifactID == "" || g.TurnID == "" || !g.Allows(action) {
		return nil, ErrArtifactV3AuthorUnauthorized
	}
	if p.AccountScopeID == "" || p.UserID == "" || g.OwnerSessionID == "" || p.ProducerSessionID != g.ProducerSessionID || p.ProducerRunID == "" || g.ProducerRunID == "" || p.ProducerRunID != g.ProducerRunID {
		return nil, ErrArtifactV3AuthorUnauthorized
	}
	if g.ExpiresAt <= s.now().UnixMilli() {
		return nil, ErrArtifactV3AuthorExpired
	}
	key := artifactV3WorkspaceKey(g)
	s.mu.Lock()
	defer s.mu.Unlock()
	if state := s.turns[key]; state != nil {
		return state, nil
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(s.root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrArtifactV3AuthorInvalid
	}
	if err = os.Chmod(s.root, 0o700); err != nil {
		return nil, err
	}
	root := filepath.Join(s.root, key)
	if err = os.Mkdir(root, 0o700); err != nil {
		return nil, err
	}
	state := &artifactV3TurnState{root: root, base: map[string][]byte{}}
	if !g.Initial {
		if s.repository == nil || g.BaseCommitOID == "" {
			os.RemoveAll(root)
			return nil, ErrArtifactV3AuthorInvalid
		}
		if err = s.repository.MaterializeBase(ctx, g.ArtifactID, g.BaseCommitOID, root); err != nil {
			os.RemoveAll(root)
			return nil, err
		}
		base, loadErr := artifactV3Snapshot(root, g.Limits.normalized())
		if loadErr != nil {
			os.RemoveAll(root)
			return nil, loadErr
		}
		state.base = base
	}
	s.turns[key] = state
	return state, nil
}
func (s *ArtifactV3AuthorService) invalidate(state *artifactV3TurnState) {
	state.gate = nil
	state.finished = nil
}
func artifactV3WorkspaceKey(g ArtifactV3AuthorGrant) string {
	sum := sha256.Sum256([]byte(g.ID + "\x00" + g.ArtifactID + "\x00" + g.TurnID))
	return "turn-" + hex.EncodeToString(sum[:16])
}
func artifactV3Context(g ArtifactV3AuthorGrant, files map[string][]byte, gate *ArtifactV3AuthorGate) ArtifactV3AuthorContext {
	out := ArtifactV3AuthorContext{ArtifactID: g.ArtifactID, TurnID: g.TurnID, CandidateID: g.CandidateID, BaseCommitOID: g.BaseCommitOID, PolicyRevision: g.PolicyRevision, Initial: g.Initial, TargetPartIDs: append([]string(nil), g.TargetPartIDs...), LockedPaths: append([]string(nil), g.LockedPaths...), LatestGate: gate}
	for _, path := range artifactV3Paths(files) {
		out.Files = append(out.Files, ArtifactV3AuthorFile{Path: path, Size: int64(len(files[path]))})
	}
	return out
}
func artifactV3Paths(files map[string][]byte) []string {
	out := make([]string, 0, len(files))
	for path := range files {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
func artifactV3Clone(input map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(input))
	for path, body := range input {
		out[path] = append([]byte(nil), body...)
	}
	return out
}
func artifactV3Digest(files map[string][]byte) string {
	h := sha256.New()
	for _, path := range artifactV3Paths(files) {
		h.Write([]byte(path))
		h.Write([]byte{0})
		sum := sha256.Sum256(files[path])
		h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}
func artifactV3SafeDiagnostic(stage string, err error) ArtifactV3Diagnostic {
	code := stage + "_failed"
	message := "trusted " + stage + " gate failed"
	type safe interface {
		SafeDiagnosticCode() string
		SafeDiagnosticMessage() string
	}
	if value, ok := err.(safe); ok {
		if strings.TrimSpace(value.SafeDiagnosticCode()) != "" {
			code = value.SafeDiagnosticCode()
		}
		if strings.TrimSpace(value.SafeDiagnosticMessage()) != "" {
			message = value.SafeDiagnosticMessage()
		}
	}
	return ArtifactV3Diagnostic{Stage: stage, Code: code, Message: message}
}
func artifactV3Locked(g ArtifactV3AuthorGrant, path string) bool {
	for _, value := range g.LockedPaths {
		if strings.TrimSpace(value) == path {
			return true
		}
	}
	return false
}
func artifactV3CleanPath(value string, limits ArtifactV3AuthorLimits) (string, error) {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") || strings.IndexByte(value, 0) >= 0 || len(value) > limits.MaxPathBytes {
		return "", ErrArtifactV3AuthorInvalid
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Count(clean, "/")+1 > limits.MaxPathDepth {
		return "", ErrArtifactV3AuthorInvalid
	}
	for _, part := range strings.Split(clean, "/") {
		lower := strings.ToLower(part)
		if part == "" || lower == ".git" || strings.HasPrefix(lower, ".git") {
			return "", ErrArtifactV3AuthorInvalid
		}
	}
	return clean, nil
}
func artifactV3SecureParents(root, path string) error {
	current := root
	parts := strings.Split(path, "/")
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err = os.Mkdir(current, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrArtifactV3AuthorInvalid
		}
	}
	return nil
}
func artifactV3ReadRegular(root, path string) ([]byte, error) {
	full := filepath.Join(root, filepath.FromSlash(path))
	info, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrArtifactV3AuthorInvalid
	}
	return os.ReadFile(full)
}
func artifactV3WriteRegular(root, path string, body []byte, exclusive bool, limits ArtifactV3AuthorLimits) error {
	if int64(len(body)) > limits.MaxFileBytes {
		return ErrArtifactV3AuthorQuota
	}
	if err := artifactV3SecureParents(root, path); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if exclusive {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(filepath.Join(root, filepath.FromSlash(path)), flags, 0o600)
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		file.Close()
		return ErrArtifactV3AuthorInvalid
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
func artifactV3Snapshot(root string, limits ArtifactV3AuthorLimits) (map[string][]byte, error) {
	result := map[string][]byte{}
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrArtifactV3AuthorInvalid
		}
		if entry.IsDir() {
			if strings.HasPrefix(strings.ToLower(entry.Name()), ".git") {
				return ErrArtifactV3AuthorInvalid
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrArtifactV3AuthorInvalid
		}
		clean, err := artifactV3CleanPath(rel, limits)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if int64(len(body)) > limits.MaxFileBytes {
			return ErrArtifactV3AuthorQuota
		}
		total += int64(len(body))
		if total > limits.MaxTreeBytes || len(result)+1 > limits.MaxFiles {
			return ErrArtifactV3AuthorQuota
		}
		result[clean] = body
		return nil
	})
	return result, err
}
