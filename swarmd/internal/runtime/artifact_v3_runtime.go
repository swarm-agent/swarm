package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/artifactv3video"
	"swarm/packages/swarmd/internal/htmlcapture"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/videoproject"
)

const (
	artifactV3RepositoryDir = "artifacts-v3/git"
	artifactV3WorkspaceDir  = "artifacts-v3/worktrees"
	artifactV3EvidenceDir   = "artifacts-v3/evidence"
)

// artifactV3RuntimeAdapter is the sole production bridge from managed Designer
// authoring and authenticated HTTP to the Git/Pebble Artifact V3 authority.
// It deliberately has no dependency on Artifact V1 or V2.
type artifactV3RuntimeAdapter struct {
	service        *pebblestore.ArtifactV3Service
	sessions       *pebblestore.SessionStore
	repositoryRoot string
	evidenceRoot   string
	limits         pebblestore.ArtifactV3Limits
	renderer       htmlcapture.Renderer
	publish        func(identity.Principal, api.ArtifactV3Artifact, string, string) error

	mu       sync.RWMutex
	grants   map[string]artifactV3GrantOwner
	builds   map[string]tool.ArtifactV3BuildResult
	previews map[string]tool.ArtifactV3PreviewResult
}

type artifactV3GrantOwner struct {
	Owner  pebblestore.ArtifactV3Owner
	Prompt string
}

func newArtifactV3RuntimeAdapter(service *pebblestore.ArtifactV3Service, sessions *pebblestore.SessionStore, repositoryRoot, evidenceRoot string, limits pebblestore.ArtifactV3Limits, renderer htmlcapture.Renderer) *artifactV3RuntimeAdapter {
	return &artifactV3RuntimeAdapter{service: service, sessions: sessions, repositoryRoot: repositoryRoot, evidenceRoot: evidenceRoot, limits: limits, renderer: renderer, grants: map[string]artifactV3GrantOwner{}, builds: map[string]tool.ArtifactV3BuildResult{}, previews: map[string]tool.ArtifactV3PreviewResult{}}
}

func artifactV3StorageRoots(dataDir, cacheDir string) (repository, workspace, evidence string, err error) {
	if strings.TrimSpace(dataDir) == "" || strings.TrimSpace(cacheDir) == "" {
		return "", "", "", errors.New("artifact v3 storage roots are not configured")
	}
	repository = filepath.Join(filepath.Clean(dataDir), filepath.FromSlash(artifactV3RepositoryDir))
	workspace = filepath.Join(filepath.Clean(cacheDir), filepath.FromSlash(artifactV3WorkspaceDir))
	evidence = filepath.Join(filepath.Clean(dataDir), filepath.FromSlash(artifactV3EvidenceDir))
	for _, path := range []string{repository, workspace, evidence} {
		if err = ensurePrivateDirectory(path); err != nil {
			return "", "", "", err
		}
	}
	return repository, workspace, evidence, nil
}

func artifactV3Principal(owner artifactV3GrantOwner) identity.Principal {
	return identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: owner.Owner.AccountScopeID, UserID: owner.Owner.UserID}
}

func artifactV3Reference(repository pebblestore.ArtifactV3RepositoryProjection) string {
	return fmt.Sprintf("artifact-v3:%s:%s:%s:%d", repository.OwnerSessionID, repository.ArtifactID, repository.HeadCommitOID, repository.EventSeq)
}

func artifactV3StableID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return prefix + "-" + hex.EncodeToString(h.Sum(nil)[:12])
}

func (a *artifactV3RuntimeAdapter) PrepareArtifactV3Turn(ctx context.Context, request tool.ArtifactV3PrepareTurnRequest) (tool.ArtifactV3AuthorGrant, error) {
	if a == nil || a.service == nil || a.sessions == nil || strings.TrimSpace(request.AccountScopeID) == "" || strings.TrimSpace(request.UserID) == "" || strings.TrimSpace(request.OwnerSessionID) == "" || strings.TrimSpace(request.TaskCallID) == "" {
		return tool.ArtifactV3AuthorGrant{}, pebblestore.ErrArtifactV3Invalid
	}
	owner := pebblestore.ArtifactV3Owner{AccountScopeID: strings.TrimSpace(request.AccountScopeID), UserID: strings.TrimSpace(request.UserID), SessionID: strings.TrimSpace(request.OwnerSessionID)}
	artifactID := strings.TrimSpace(request.ArtifactID)
	if request.Initial {
		if artifactID == "" {
			artifactID = artifactV3StableID("artifact", owner.SessionID, request.TaskCallID)
		}
	} else {
		if artifactID == "" || strings.TrimSpace(request.BaseCommitOID) == "" {
			return tool.ArtifactV3AuthorGrant{}, pebblestore.ErrArtifactV3Invalid
		}
		repository, ok, err := a.sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, artifactID)
		if err != nil || !ok || repository.OwnerSessionID != owner.SessionID || repository.HeadCommitOID != strings.TrimSpace(request.BaseCommitOID) {
			return tool.ArtifactV3AuthorGrant{}, pebblestore.ErrArtifactV3Conflict
		}
		if request.ProjectionSeq != 0 && repository.EventSeq != request.ProjectionSeq {
			// A sibling allocation may follow draft events from its already-authorized
			// exact-base turn; unrelated stale turns must still fail closed.
			matched := false
			for _, draft := range repository.Drafts {
				var prior tool.ArtifactV3AuthorGrant
				if json.Unmarshal(draft.Grant, &prior) == nil && prior.TurnID == artifactV3StableID("turn", artifactID, request.TaskCallID) && prior.BaseCommitOID == request.BaseCommitOID && prior.SourceProjectionSeq == request.ProjectionSeq && prior.OwnerSessionID == owner.SessionID && prior.ExpiresAt > time.Now().UnixMilli() {
					matched = true
					break
				}
			}
			if !matched {
				return tool.ArtifactV3AuthorGrant{}, pebblestore.ErrArtifactV3Conflict
			}
		}
		if len(request.TargetPartIDs) != 0 {
			revision, revisionOK, revisionErr := a.sessions.GetArtifactV3Revision(owner.AccountScopeID, owner.UserID, artifactID, repository.HeadCommitOID)
			if revisionErr != nil || !revisionOK {
				return tool.ArtifactV3AuthorGrant{}, pebblestore.ErrArtifactV3Integrity
			}
			declared := make(map[string]bool, len(revision.Parts))
			for _, part := range revision.Parts {
				declared[strings.TrimSpace(part.ID)] = true
			}
			for _, targetID := range canonicalStrings(request.TargetPartIDs) {
				if !declared[targetID] {
					return tool.ArtifactV3AuthorGrant{}, pebblestore.ErrArtifactV3Invalid
				}
			}
		}
	}
	turnID := artifactV3StableID("turn", artifactID, request.TaskCallID)
	candidateID := artifactV3StableID("candidate", turnID, fmt.Sprint(request.CandidateIndex))
	grantID := artifactV3StableID("grant", artifactID, turnID, candidateID)
	grant := tool.ArtifactV3AuthorGrant{
		SourceProjectionSeq: request.ProjectionSeq,
		AccountScopeID:      owner.AccountScopeID, UserID: owner.UserID,
		ID: grantID, ArtifactID: artifactID, OwnerSessionID: owner.SessionID, TurnID: turnID, CandidateID: candidateID,
		BaseCommitOID: strings.TrimSpace(request.BaseCommitOID), Initial: request.Initial, TargetPartIDs: canonicalStrings(request.TargetPartIDs), LockedPaths: canonicalStrings(request.LockedPaths),
		AllowedActions: []string{"inspect_context", "list_files", "read_file", "create_file", "edit_file", "rename_file", "delete_file", "diff", "build_preview", "finish_turn"},
		PolicyRevision: strings.TrimSpace(request.PolicyRevision), ExpiresAt: request.ExpiresAt,
		Limits: tool.ArtifactV3AuthorLimits{MaxFileBytes: 64 << 20, MaxTreeBytes: 256 << 20, MaxFiles: 4096, MaxPathBytes: 512, MaxPathDepth: 32, MaxListPage: 500, MaxReadBytes: 1 << 20, MaxDiffEntries: 1000},
	}
	if grant.PolicyRevision == "" || grant.ExpiresAt <= time.Now().UnixMilli() {
		return tool.ArtifactV3AuthorGrant{}, pebblestore.ErrArtifactV3Invalid
	}
	if !request.Initial {
		target := ""
		if len(grant.TargetPartIDs) != 0 {
			target = grant.TargetPartIDs[0]
		}
		if _, err := a.service.OpenTurn(ctx, pebblestore.ArtifactV3OpenTurnInput{Owner: owner, ArtifactID: artifactID, TurnID: turnID, ExpectedHead: grant.BaseCommitOID, TargetPartID: target, TargetPartIDs: grant.TargetPartIDs}); err != nil {
			return tool.ArtifactV3AuthorGrant{}, err
		}
	}
	// Match the author service's slice-copy representation before persisting identity.
	grant.TargetPartIDs = append([]string(nil), grant.TargetPartIDs...)
	grant.LockedPaths = append([]string(nil), grant.LockedPaths...)
	raw, err := json.Marshal(grant)
	if err != nil {
		return tool.ArtifactV3AuthorGrant{}, err
	}
	existing, _, err := a.sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, artifactID)
	if err != nil {
		return tool.ArtifactV3AuthorGrant{}, err
	}
	if previous, found := existing.Drafts[grantID]; found {
		if string(previous.Grant) != string(raw) {
			return tool.ArtifactV3AuthorGrant{}, tool.ErrArtifactV3AuthorConflict
		}
	} else {
		_, err = a.service.SaveDraft(owner, artifactID, grantID, strings.TrimSpace(request.Prompt), pebblestore.ArtifactV3DraftProjection{GrantID: grantID, Grant: raw, Status: "creating", ExpiresAt: grant.ExpiresAt}, 0)
		if err != nil {
			return tool.ArtifactV3AuthorGrant{}, err
		}
	}
	a.mu.Lock()
	a.grants[grantID] = artifactV3GrantOwner{Owner: owner, Prompt: strings.TrimSpace(request.Prompt)}
	a.mu.Unlock()
	return grant, nil
}

func canonicalStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (a *artifactV3RuntimeAdapter) ownerFor(artifactID, turnID, candidateID string) (artifactV3GrantOwner, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	wanted := artifactV3StableID("grant", artifactID, turnID, candidateID)
	for grantID, owner := range a.grants {
		if wanted == grantID {
			return owner, nil
		}
	}
	return artifactV3GrantOwner{}, tool.ErrArtifactV3AuthorUnauthorized
}

func (a *artifactV3RuntimeAdapter) MaterializeBase(ctx context.Context, artifactID, commitOID, destination string) error {
	grant, ok := tool.ArtifactV3GrantFromContext(ctx)
	if !ok || grant.ArtifactID != artifactID || grant.BaseCommitOID != commitOID {
		return tool.ErrArtifactV3AuthorUnauthorized
	}
	_, err := a.LoadAuthorDraft(ctx, artifactV3AuthorPrincipal(grant), grant)
	owner := artifactV3GrantOwner{Owner: artifactV3Owner(grant)}
	if err != nil {
		return err
	}
	repository, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, artifactID, owner.Owner, a.limits)
	if err != nil {
		return err
	}
	return repository.Materialize(ctx, commitOID, destination)
}

func (a *artifactV3RuntimeAdapter) SubmitProject(ctx context.Context, request tool.ArtifactV3SubmitRequest) (tool.ArtifactV3Revision, error) {
	grant, ok := tool.ArtifactV3GrantFromContext(ctx)
	if !ok || grant.ArtifactID != request.ArtifactID || grant.TurnID != request.TurnID || grant.CandidateID != request.CandidateID || grant.BaseCommitOID != request.BaseCommitOID || grant.Initial != request.Initial || grant.PolicyRevision != request.PolicyRevision {
		return tool.ArtifactV3Revision{}, tool.ErrArtifactV3AuthorUnauthorized
	}
	draft, err := a.LoadAuthorDraft(ctx, artifactV3AuthorPrincipal(grant), grant)
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	if draft.Finished != nil {
		if draft.Gate == nil || draft.Gate.ProjectDigest != request.ProjectDigest || digestArtifactProject(request.Project) != request.ProjectDigest {
			return tool.ArtifactV3Revision{}, tool.ErrArtifactV3AuthorConflict
		}
		return draft.Finished.Revision, nil
	}
	if draft.Gate == nil || !draft.Gate.Ready || draft.Gate.ProjectDigest != request.ProjectDigest || digestArtifactProject(request.Project) != request.ProjectDigest || !reflect.DeepEqual(draft.Project, request.Project) {
		return tool.ArtifactV3Revision{}, tool.ErrArtifactV3AuthorConflict
	}
	if !draft.Publishing {
		if draft.Sequence != request.DraftSequence {
			return tool.ArtifactV3Revision{}, tool.ErrArtifactV3AuthorConflict
		}
		draft.Publishing = true
		draft, err = a.saveAuthorDraft(grant, draft)
		if err != nil {
			return tool.ArtifactV3Revision{}, err
		}
	}
	// Reservation freezes the exact validated source before Git is touched.
	request.Build, request.Preview = draft.Gate.Build, draft.Gate.Preview
	stored, _, err := a.sessions.GetArtifactV3Repository(grant.AccountScopeID, grant.UserID, grant.ArtifactID)
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	owner := artifactV3GrantOwner{Owner: artifactV3Owner(grant), Prompt: stored.IntentReference}
	project := pebblestore.ArtifactV3Project{Files: request.Project}
	transactionID := artifactV3StableID("tx", request.ArtifactID, request.TurnID, request.CandidateID, request.ProjectDigest)
	if request.Initial {
		buildDigest := request.ProjectDigest
		if len(request.Build.OutputFiles) != 0 {
			buildDigest = digestArtifactProject(request.Build.OutputFiles)
		}
		build := artifactV3Evidence(request.Build.ID, request.Build.Status, "", buildDigest)
		previewDigest := ""
		if len(request.Preview.EvidenceDigests) != 0 {
			previewDigest = request.Preview.EvidenceDigests[0]
		}
		preview := artifactV3Evidence(request.Preview.ID, "succeeded", "", previewDigest)
		created, err := a.service.Create(ctx, pebblestore.ArtifactV3CreateInput{Owner: owner.Owner, ArtifactID: request.ArtifactID, TransactionID: transactionID, Project: project, Message: owner.Prompt, Build: build, Preview: preview})
		if err != nil {
			return tool.ArtifactV3Revision{}, err
		}
		if err := a.publishProjection(owner, request.ArtifactID, pebblestore.V3SessionMutationArtifactV3GenesisCommitted, transactionID); err != nil {
			return tool.ArtifactV3Revision{}, err
		}
		return a.finishAuthorDraft(grant, draft, tool.ArtifactV3Revision{CommitOID: created.Revision.CommitOID, TreeOID: created.Revision.TreeOID, ManifestBlobOID: created.Revision.ManifestBlobOID})
	}
	repository, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, request.ArtifactID, owner.Owner, a.limits)
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	candidate, err := repository.Candidate(ctx, pebblestore.ArtifactV3CandidateRequest{TurnID: request.TurnID, CandidateID: request.CandidateID, TransactionID: transactionID, BaseCommit: request.BaseCommitOID, Project: project, Message: owner.Prompt})
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	buildDigest := request.ProjectDigest
	if len(request.Build.OutputFiles) != 0 {
		buildDigest = digestArtifactProject(request.Build.OutputFiles)
	}
	build := artifactV3Evidence(request.Build.ID, request.Build.Status, candidate.CommitOID, buildDigest)
	previewDigest := ""
	if len(request.Preview.EvidenceDigests) != 0 {
		previewDigest = request.Preview.EvidenceDigests[0]
	}
	preview := artifactV3Evidence(request.Preview.ID, "succeeded", candidate.CommitOID, previewDigest)
	committed, err := a.service.SubmitCandidate(ctx, pebblestore.ArtifactV3SubmitCandidateInput{Owner: owner.Owner, ArtifactID: request.ArtifactID, TurnID: request.TurnID, CandidateID: request.CandidateID, TransactionID: transactionID, ExpectedHead: request.BaseCommitOID, Project: project, Message: owner.Prompt, Build: build, Preview: preview})
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	if err := a.publishProjection(owner, request.ArtifactID, "artifact.v3.candidate.ready", transactionID); err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	return a.finishAuthorDraft(grant, draft, tool.ArtifactV3Revision{CommitOID: committed.Revision.CommitOID, TreeOID: committed.Revision.TreeOID, ManifestBlobOID: committed.Revision.ManifestBlobOID})
}

func artifactV3Evidence(id, status, commit, digest string) pebblestore.ArtifactV3EvidenceProjection {
	if status == "valid" {
		status = "succeeded"
	}
	return pebblestore.ArtifactV3EvidenceProjection{Status: status, CommitOID: commit, DigestSHA256: digest, Reference: id}
}

func (a *artifactV3RuntimeAdapter) FailArtifactV3Turn(_ context.Context, failure tool.ArtifactV3TurnFailure) error {
	owner, err := a.ownerFor(failure.ArtifactID, failure.TurnID, failure.CandidateID)
	if err != nil {
		return err
	}
	repository, found, readErr := a.sessions.GetArtifactV3Repository(owner.Owner.AccountScopeID, owner.Owner.UserID, failure.ArtifactID)
	if readErr != nil {
		return readErr
	}
	if !found {
		return pebblestore.ErrArtifactV3NotFound
	}
	{
		id := artifactV3StableID("grant", failure.ArtifactID, failure.TurnID, failure.CandidateID)
		draft, exists := repository.Drafts[id]
		if !exists {
			return tool.ErrArtifactV3AuthorUnauthorized
		}
		var state tool.ArtifactV3AuthorDraft
		if len(draft.State) != 0 {
			if err := json.Unmarshal(draft.State, &state); err != nil {
				return err
			}
		}
		if state.Publishing || state.Finished != nil {
			return tool.ErrArtifactV3AuthorConflict
		}
		if state.ProducerSessionID != "" && (state.ProducerSessionID != failure.ProducerSessionID || state.ProducerRunID != failure.ProducerRunID) {
			return tool.ErrArtifactV3AuthorUnauthorized
		}
		draft.Status = "error"
		_, err = a.service.SaveDraft(owner.Owner, failure.ArtifactID, artifactV3StableID("failed", id, fmt.Sprint(draft.Sequence)), repository.IntentReference, draft, draft.Sequence)
		if err != nil || repository.HeadCommitOID == "" {
			return err
		}
	}
	_, err = a.service.RecordCandidateTerminal(owner.Owner, failure.ArtifactID, failure.TurnID, failure.CandidateID, artifactV3StableID("fail", failure.ArtifactID, failure.TurnID, failure.CandidateID), "failed", strings.TrimSpace(failure.Code), 0)
	return err
}

func (a *artifactV3RuntimeAdapter) Build(_ context.Context, request tool.ArtifactV3BuildRequest) (tool.ArtifactV3BuildResult, error) {
	manifest, diagnostics := parseArtifactV3Manifest(request.Project)
	if len(diagnostics) != 0 {
		return tool.ArtifactV3BuildResult{Status: "failed", Diagnostics: diagnostics}, nil
	}
	entry := request.Project[manifest.Entrypoint]
	if len(entry) == 0 {
		return tool.ArtifactV3BuildResult{Status: "failed", Diagnostics: []tool.ArtifactV3Diagnostic{{Stage: "build", Code: "entrypoint_empty", Message: "the Artifact V3 entrypoint is empty", Path: manifest.Entrypoint}}}, nil
	}
	for path, body := range request.Project {
		if strings.EqualFold(filepath.Ext(path), ".html") && (!bytesContainsFold(body, "<html") || !bytesContainsFold(body, "<body")) {
			return tool.ArtifactV3BuildResult{Status: "failed", Diagnostics: []tool.ArtifactV3Diagnostic{{Stage: "build", Code: "html_document_invalid", Message: "HTML source is missing a complete document body", Path: path}}}, nil
		}
	}
	id := artifactV3StableID("build", request.ArtifactID, request.TurnID, fmt.Sprint(request.Attempt), digestArtifactProject(request.Project))
	result := tool.ArtifactV3BuildResult{ID: id, Status: "succeeded", OutputFiles: cloneArtifactProject(request.Project)}
	a.mu.Lock()
	a.builds[id] = result
	a.mu.Unlock()
	return result, nil
}

func bytesContainsFold(body []byte, text string) bool {
	return strings.Contains(strings.ToLower(string(body)), strings.ToLower(text))
}

func parseArtifactV3Manifest(project map[string][]byte) (pebblestore.ArtifactV3Manifest, []tool.ArtifactV3Diagnostic) {
	if _, ok := project[pebblestore.ArtifactV3ManifestFilename]; !ok {
		return pebblestore.ArtifactV3Manifest{}, []tool.ArtifactV3Diagnostic{{Stage: "build", Code: "manifest_missing", Message: "swarm-artifact.json is required", Path: pebblestore.ArtifactV3ManifestFilename}}
	}
	manifest, err := pebblestore.ValidateArtifactV3Project(pebblestore.ArtifactV3Project{Files: project}, pebblestore.ArtifactV3Limits{})
	if err != nil {
		diagnostic := tool.ArtifactV3Diagnostic{Stage: "build", Code: "manifest_invalid", Message: "project must satisfy Artifact V3 file, path, and quota limits", Path: pebblestore.ArtifactV3ManifestFilename}
		var manifestError *pebblestore.ArtifactV3ManifestError
		if errors.As(err, &manifestError) {
			diagnostic.Code = manifestError.SafeDiagnosticCode()
			diagnostic.Message = manifestError.SafeDiagnosticMessage()
		}
		return manifest, []tool.ArtifactV3Diagnostic{diagnostic}
	}
	return manifest, nil
}

func (a *artifactV3RuntimeAdapter) Preview(ctx context.Context, request tool.ArtifactV3PreviewRequest) (tool.ArtifactV3PreviewResult, error) {
	manifest, diagnostics := parseArtifactV3Manifest(request.Build.OutputFiles)
	if len(diagnostics) != 0 {
		return tool.ArtifactV3PreviewResult{Status: "failed", Diagnostics: diagnostics}, nil
	}
	if a.renderer == nil {
		return tool.ArtifactV3PreviewResult{Status: "failed", Diagnostics: []tool.ArtifactV3Diagnostic{{Stage: "preview", Code: "previewer_unavailable", Message: "trusted browser preview gate is unavailable"}}}, nil
	}
	capture, err := artifactV3PreviewCaptureRequest(manifest, request.Build.OutputFiles)
	if err != nil {
		return tool.ArtifactV3PreviewResult{Status: "failed", Diagnostics: []tool.ArtifactV3Diagnostic{{Stage: "preview", Code: "animation_contract_invalid", Message: err.Error()}}}, nil
	}
	results, err := a.renderer.Capture(ctx, capture)
	if err != nil {
		diagnostic := tool.ArtifactV3Diagnostic{Stage: "preview", Code: "browser_capture_failed", Message: "the complete Artifact V3 project failed its browser preview gate"}
		type safe interface {
			SafeDiagnosticCode() string
			SafeDiagnosticMessage() string
		}
		if value, ok := err.(safe); ok {
			if code := strings.TrimSpace(value.SafeDiagnosticCode()); code != "" {
				diagnostic.Code = code
			}
			if message := strings.TrimSpace(value.SafeDiagnosticMessage()); message != "" {
				diagnostic.Message = message
			}
		}
		return tool.ArtifactV3PreviewResult{Status: "failed", Diagnostics: []tool.ArtifactV3Diagnostic{diagnostic}}, nil
	}
	if len(results) != len(capture.StateIDs) || len(results) == 0 || len(results[0].PNG) == 0 {
		return tool.ArtifactV3PreviewResult{Status: "failed", Diagnostics: []tool.ArtifactV3Diagnostic{{Stage: "preview", Code: "browser_evidence_missing", Message: "the browser preview gate returned no inspectable pixels"}}}, nil
	}
	missing := unresolvedArtifactV3Targets(manifest, request.TargetPartIDs, request.Build.OutputFiles)
	if len(missing) != 0 {
		return tool.ArtifactV3PreviewResult{Status: "failed", Diagnostics: missing}, nil
	}
	evidenceDigests := make([]string, 0, len(results))
	for index, result := range results {
		if result.StateID != capture.StateIDs[index] || len(result.PNG) == 0 {
			return tool.ArtifactV3PreviewResult{}, errors.New("temporal preview evidence is incomplete")
		}
		sum := sha256.Sum256(result.PNG)
		evidenceDigests = append(evidenceDigests, hex.EncodeToString(sum[:]))
		evidenceDigests = append(evidenceDigests, result.SectionDigests...)
	}
	digest := sha256.Sum256(results[0].PNG)
	digestHex := hex.EncodeToString(digest[:])
	id := artifactV3StableID("preview", request.ArtifactID, request.TurnID, fmt.Sprint(request.Attempt), digestHex)
	if err := os.MkdirAll(a.evidenceRoot, 0o700); err != nil {
		return tool.ArtifactV3PreviewResult{}, err
	}
	if err := os.WriteFile(filepath.Join(a.evidenceRoot, id+".png"), results[0].PNG, 0o600); err != nil {
		return tool.ArtifactV3PreviewResult{}, err
	}
	result := tool.ArtifactV3PreviewResult{ID: id, Status: "valid", EvidenceDigests: evidenceDigests}
	a.mu.Lock()
	a.previews[id] = result
	a.mu.Unlock()
	return result, nil
}

// Temporal Parts are verified at their declared playhead samples, never made
// artificially visible. Static documents capture independently reachable sections.
func artifactV3PreviewCaptureRequest(manifest pebblestore.ArtifactV3Manifest, files map[string][]byte) (htmlcapture.Request, error) {
	request := htmlcapture.Request{Entry: manifest.Entrypoint, Files: cloneArtifactProject(files), StateIDs: []string{"default"}, ViewportWidth: 1440, ViewportHeight: 900}
	var durationMS int64
	if manifest.AnimationProfile != nil {
		canonical, err := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: manifest.AnimationProfile.ProfileID})
		if err != nil || canonical.ProfileID != "motion_ui" || !reflect.DeepEqual(canonical, manifest.AnimationProfile) {
			return request, errors.New("native HTML requires an unchanged reviewed motion_ui profile")
		}
		durationMS, err = tool.ArtifactHTMLAnimationDurationMS(files[manifest.Entrypoint])
		if err != nil {
			return request, err
		}
	}
	times := map[string]int64{}
	request.StateRequiredSelectors = map[string][]string{}
	for _, part := range manifest.Parts {
		if part.CaptureTimeMS != nil {
			if manifest.AnimationProfile == nil || part.Locator.Kind != "selector" || part.Locator.Path != manifest.Entrypoint || strings.TrimSpace(part.Locator.Value) == "" || *part.CaptureTimeMS < 0 || *part.CaptureTimeMS > durationMS {
				return request, errors.New("invalid native temporal Part capture contract")
			}
			if len(times) >= htmlcapture.MaxStates {
				return request, errors.New("native temporal preview exceeds bounded state count")
			}
			times[part.ID] = *part.CaptureTimeMS
			request.StateRequiredSelectors[part.ID] = []string{part.Locator.Value}
		} else if part.Locator.Kind == "selector" && part.Locator.Path == manifest.Entrypoint && strings.TrimSpace(part.Locator.Value) != "" {
			request.RequiredSelectors = append(request.RequiredSelectors, part.Locator.Value)
		}
	}
	if len(times) == 0 && manifest.AnimationProfile != nil {
		times["animation-preview"] = durationMS / 2
		request.StateIDs = []string{"animation-preview"}
	} else {
		request.StateIDs = nil
		for _, part := range manifest.Parts {
			if part.CaptureTimeMS != nil {
				request.StateIDs = append(request.StateIDs, part.ID)
			}
		}
	}
	if len(times) == 0 {
		request.StateIDs = []string{"default"}
		if manifest.AnimationProfile == nil && len(request.RequiredSelectors) > 0 {
			request.DocumentSections = true
			request.StateIDs = nil
			request.RequiredSelectors = nil
			for _, part := range manifest.Parts {
				if part.Locator.Kind == "selector" && part.Locator.Path == manifest.Entrypoint && strings.TrimSpace(part.Locator.Value) != "" {
					request.StateIDs = append(request.StateIDs, part.ID)
					request.StateRequiredSelectors[part.ID] = []string{part.Locator.Value}
				}
			}
			if len(request.StateIDs) > htmlcapture.MaxDocumentSections {
				return request, errors.New("native document exceeds bounded section count")
			}
		}
		request.Files[manifest.Entrypoint] = injectArtifactV3CaptureRuntime(request.Files[manifest.Entrypoint])
		return request, nil
	}
	request.TemporalStates = true
	encoded, err := json.Marshal(times)
	if err != nil {
		return request, err
	}
	bridge := `<script data-swarm-capture-ui>(()=>{const times=` + string(encoded) + `;globalThis.__SWARM_CAPTURE_V1__={version:"swarm.capture/v1",select:async id=>{if(!Object.hasOwn(times,id))throw Error("unknown temporal Part");const api=globalThis.__SWARM_ANIMATION_V1__;if(!api||api.version!=="swarm.animation/v1"||typeof api.ready!=="function"||typeof api.seek!=="function")throw Error("temporal Part requires swarm.animation/v1 ready/seek");await api.ready();if(typeof api.pause==="function")await api.pause();const ack=await api.seek(times[id]);if(!ack||ack.time_ms!==times[id])throw Error("temporal seek acknowledgement mismatch");document.documentElement.dataset.swarmCaptureState=id},ready:async id=>({state_id:id})}})();</script>`
	body := request.Files[manifest.Entrypoint]
	if index := strings.LastIndex(strings.ToLower(string(body)), "</body>"); index >= 0 {
		request.Files[manifest.Entrypoint] = []byte(string(body[:index]) + bridge + string(body[index:]))
	} else {
		request.Files[manifest.Entrypoint] = append(body, []byte(bridge)...)
	}
	return request, nil
}

func injectArtifactV3CaptureRuntime(body []byte) []byte {
	const runtime = `<script data-swarm-capture-ui>globalThis.__SWARM_CAPTURE_V1__={version:"swarm.capture/v1",select:async id=>{document.documentElement.dataset.swarmCaptureState=id},ready:async id=>({state_id:id})};</script>`
	lower := strings.ToLower(string(body))
	if index := strings.LastIndex(lower, "</body>"); index >= 0 {
		out := make([]byte, 0, len(body)+len(runtime))
		out = append(out, body[:index]...)
		out = append(out, runtime...)
		out = append(out, body[index:]...)
		return out
	}
	return append(append([]byte(nil), body...), runtime...)
}

func unresolvedArtifactV3Targets(manifest pebblestore.ArtifactV3Manifest, targets []string, files map[string][]byte) []tool.ArtifactV3Diagnostic {
	parts := make(map[string]pebblestore.ArtifactV3Part, len(manifest.Parts))
	for _, part := range manifest.Parts {
		parts[part.ID] = part
	}
	var out []tool.ArtifactV3Diagnostic
	for _, target := range targets {
		part, ok := parts[target]
		if !ok {
			out = append(out, tool.ArtifactV3Diagnostic{Stage: "locator", Code: "target_missing", Message: "the requested Part is not declared", Path: pebblestore.ArtifactV3ManifestFilename})
			continue
		}
		if part.Locator.Kind == "selector" && strings.HasPrefix(part.Locator.Value, "#") {
			body := files[part.Locator.Path]
			needle := `id="` + strings.TrimPrefix(part.Locator.Value, "#") + `"`
			if !bytesContainsFold(body, needle) {
				out = append(out, tool.ArtifactV3Diagnostic{Stage: "locator", Code: "selector_unresolved", Message: "the requested Part selector is unresolved", Path: part.Locator.Path})
			}
		}
	}
	return out
}

func cloneArtifactProject(input map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(input))
	for path, body := range input {
		out[path] = append([]byte(nil), body...)
	}
	return out
}

func digestArtifactProject(input map[string][]byte) string {
	paths := make([]string, 0, len(input))
	for path := range input {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		h.Write([]byte(path))
		h.Write([]byte{0})
		sum := sha256.Sum256(input[path])
		h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (a *artifactV3RuntimeAdapter) ReadArtifactV3PreviewEvidence(_ context.Context, accountScopeID, userID, sessionID, artifactID, revisionRef string) ([]byte, error) {
	if a == nil || a.sessions == nil || strings.TrimSpace(accountScopeID) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(artifactID) == "" || strings.TrimSpace(revisionRef) == "" {
		return nil, pebblestore.ErrArtifactV3Unauthorized
	}
	repository, ok, err := a.sessions.GetArtifactV3Repository(accountScopeID, userID, artifactID)
	if err != nil || !ok || repository.OwnerSessionID != sessionID {
		return nil, pebblestore.ErrArtifactV3NotFound
	}
	commit := strings.TrimPrefix(strings.TrimSpace(revisionRef), "revision-")
	revision, ok, err := a.sessions.GetArtifactV3Revision(accountScopeID, userID, artifactID, commit)
	if err != nil || !ok || revision.Preview.CommitOID != commit || revision.Preview.Reference == "" || revision.Preview.DigestSHA256 == "" {
		return nil, pebblestore.ErrArtifactV3Integrity
	}
	body, err := os.ReadFile(filepath.Join(a.evidenceRoot, revision.Preview.Reference+".png"))
	if err != nil || len(body) == 0 {
		return nil, pebblestore.ErrArtifactV3Integrity
	}
	digest := sha256.Sum256(body)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), revision.Preview.DigestSHA256) {
		return nil, pebblestore.ErrArtifactV3Integrity
	}
	return body, nil
}

func (a *artifactV3RuntimeAdapter) ListArtifacts(ctx context.Context, principal api.ArtifactV3Principal, sessionID string, limit int) ([]api.ArtifactV3Artifact, error) {
	if a == nil || a.sessions == nil || strings.TrimSpace(principal.AccountScopeID) == "" || strings.TrimSpace(principal.UserID) == "" || strings.TrimSpace(sessionID) == "" {
		return nil, pebblestore.ErrArtifactV3Unauthorized
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	entries, err := a.sessions.ListArtifactV3Repositories(principal.AccountScopeID, principal.UserID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]api.ArtifactV3Artifact, 0, len(entries))
	for _, repository := range entries {
		artifact, readErr := a.artifact(ctx, principal, repository)
		if readErr != nil {
			return nil, readErr
		}
		out = append(out, artifact)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

func (a *artifactV3RuntimeAdapter) GetArtifact(ctx context.Context, principal api.ArtifactV3Principal, sessionID, artifactID string) (api.ArtifactV3Artifact, error) {
	repository, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, artifactID)
	if err != nil || !ok || repository.OwnerSessionID != sessionID {
		return api.ArtifactV3Artifact{}, pebblestore.ErrArtifactV3NotFound
	}
	return a.artifact(ctx, principal, repository)
}

func (a *artifactV3RuntimeAdapter) artifact(ctx context.Context, principal api.ArtifactV3Principal, repository pebblestore.ArtifactV3RepositoryProjection) (api.ArtifactV3Artifact, error) {
	draft, label, err := artifactV3PublicDraft(repository)
	if err != nil {
		return api.ArtifactV3Artifact{}, err
	}
	if repository.HeadCommitOID == "" {
		return api.ArtifactV3Artifact{ID: repository.ArtifactID, Label: label, OwnerSessionID: repository.OwnerSessionID, IntentReference: repository.IntentReference, Status: repository.DraftStatus, CurrentDraft: draft, Revision: repository.EventSeq, UpdatedAt: repository.UpdatedAt}, nil
	}
	revision, err := a.revision(ctx, principal, repository, repository.HeadCommitOID)
	if err != nil {
		return api.ArtifactV3Artifact{}, err
	}
	turns, err := a.turns(ctx, principal, repository)
	if err != nil {
		return api.ArtifactV3Artifact{}, err
	}
	repo, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, repository.ArtifactID, pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: repository.OwnerSessionID}, a.limits)
	if err != nil {
		return api.ArtifactV3Artifact{}, err
	}
	entrypoint, err := repo.ReadFile(ctx, revision.CommitOID, revision.Manifest.Entrypoint)
	if err != nil {
		return api.ArtifactV3Artifact{}, err
	}
	return api.ArtifactV3Artifact{CurrentDraft: draft, Label: artifactV3DocumentTitle(entrypoint), ID: repository.ArtifactID, OwnerSessionID: repository.OwnerSessionID, IntentReference: repository.IntentReference, ArtifactRef: artifactV3Reference(repository), Status: "ready", Revision: repository.EventSeq, PartCount: len(revision.Manifest.Parts), Parts: revision.Manifest.Parts, Head: &revision, CurrentRevision: &revision, Revisions: []api.ArtifactV3Revision{revision}, Turns: turns, UpdatedAt: repository.UpdatedAt}, nil
}

func (a *artifactV3RuntimeAdapter) ListRevisions(ctx context.Context, principal api.ArtifactV3Principal, sessionID, artifactID, cursor string, limit int) (api.ArtifactV3RevisionPage, error) {
	repository, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, artifactID)
	if err != nil || !ok || repository.OwnerSessionID != sessionID {
		return api.ArtifactV3RevisionPage{}, pebblestore.ErrArtifactV3NotFound
	}
	if repository.HeadCommitOID == "" {
		return api.ArtifactV3RevisionPage{Revisions: []api.ArtifactV3Revision{}}, nil
	}
	repo, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, artifactID, pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID}, a.limits)
	if err != nil {
		return api.ArtifactV3RevisionPage{}, err
	}
	page, err := repo.ListRevisions(ctx, cursor, limit)
	if err != nil {
		return api.ArtifactV3RevisionPage{}, err
	}
	out := api.ArtifactV3RevisionPage{NextCursor: page.NextCursor}
	for _, value := range page.Revisions {
		revision, err := a.revision(ctx, principal, repository, value.CommitOID)
		if errors.Is(err, pebblestore.ErrArtifactV3NotFound) {
			continue
		}
		if err != nil {
			return api.ArtifactV3RevisionPage{}, err
		}
		out.Revisions = append(out.Revisions, revision)
	}
	return out, nil
}

func (a *artifactV3RuntimeAdapter) GetRevision(ctx context.Context, principal api.ArtifactV3Principal, sessionID, artifactID, revisionRef string) (api.ArtifactV3Revision, error) {
	repository, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, artifactID)
	if err != nil || !ok || repository.OwnerSessionID != sessionID {
		return api.ArtifactV3Revision{}, pebblestore.ErrArtifactV3NotFound
	}
	commit := strings.TrimPrefix(revisionRef, "revision-")
	return a.revision(ctx, principal, repository, commit)
}

func (a *artifactV3RuntimeAdapter) revision(ctx context.Context, principal api.ArtifactV3Principal, repository pebblestore.ArtifactV3RepositoryProjection, commit string) (api.ArtifactV3Revision, error) {
	projection, ok, err := a.sessions.GetArtifactV3Revision(principal.AccountScopeID, principal.UserID, repository.ArtifactID, commit)
	if err != nil || !ok {
		return api.ArtifactV3Revision{}, pebblestore.ErrArtifactV3NotFound
	}
	repo, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, repository.ArtifactID, pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: repository.OwnerSessionID}, a.limits)
	if err != nil {
		return api.ArtifactV3Revision{}, err
	}
	gitRevision, err := repo.ReadRevision(ctx, commit)
	if err != nil || gitRevision.TreeOID != projection.TreeOID || gitRevision.ManifestBlobOID != projection.ManifestBlobOID {
		return api.ArtifactV3Revision{}, pebblestore.ErrArtifactV3Integrity
	}
	build := artifactV3APIBuildEvidence(projection.Build, projection.TreeOID)
	validation := artifactV3APIValidationEvidence(projection.Preview, projection.TreeOID)
	if build == nil || validation == nil {
		return api.ArtifactV3Revision{}, pebblestore.ErrArtifactV3Integrity
	}
	return api.ArtifactV3Revision{RevisionRef: "revision-" + commit, CommitOID: commit, TreeOID: projection.TreeOID, ManifestBlobOID: projection.ManifestBlobOID, Parents: projection.ParentCommitOIDs, Manifest: gitRevision.Manifest, FileCount: projection.FileCount, TreeBytes: projection.TreeBytes, ChangedFiles: projection.ChangedFiles, Build: build, Validation: validation, CreatedAt: projection.CreatedAt}, nil
}

func (a *artifactV3RuntimeAdapter) OpenPreview(ctx context.Context, principal api.ArtifactV3Principal, sessionID, artifactID, revisionRef, assetPath, accessToken string) (api.ArtifactV3Preview, error) {
	revision, err := a.GetRevision(ctx, principal, sessionID, artifactID, revisionRef)
	if err != nil {
		return api.ArtifactV3Preview{}, err
	}
	repository, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, artifactID, pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID}, a.limits)
	if err != nil {
		return api.ArtifactV3Preview{}, err
	}
	filePath := revision.Manifest.Entrypoint
	mediaType := "text/html; charset=utf-8"
	if strings.TrimSpace(assetPath) != "" {
		decoded := assetPath
		if decoded == "" || decoded != path.Clean(decoded) || strings.HasPrefix(decoded, "../") || strings.HasPrefix(decoded, "/") || strings.ContainsRune(decoded, '\x00') {
			return api.ArtifactV3Preview{}, pebblestore.ErrArtifactV3Invalid
		}
		filePath = decoded
		mediaType = mime.TypeByExtension(strings.ToLower(path.Ext(filePath)))
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
	}
	body, err := repository.ReadFile(ctx, revision.CommitOID, filePath)
	if err != nil {
		return api.ArtifactV3Preview{}, err
	}
	if filePath == revision.Manifest.Entrypoint {
		body = rewriteArtifactV3PreviewReferences(body, revision.Manifest.Entrypoint, sessionID, artifactID, revision.RevisionRef, accessToken)
		body = injectArtifactV3PreviewSelection(body, revision)
	}
	return api.ArtifactV3Preview{RevisionRef: revision.RevisionRef, CommitOID: revision.CommitOID, MediaType: mediaType, Body: body, ETag: `"` + revision.TreeOID + `"`}, nil
}

var artifactV3PreviewURLAttribute = regexp.MustCompile(`(?i)(\b(?:src|href)\s*=\s*["'])([^"']+)(["'])`)

func rewriteArtifactV3PreviewReferences(body []byte, entrypoint, sessionID, artifactID, revisionRef, accessToken string) []byte {
	baseDir := path.Dir(entrypoint)
	if baseDir == "." {
		baseDir = ""
	}
	prefix := "/v3/sessions/" + url.PathEscape(sessionID) + "/artifacts-v3/" + url.PathEscape(artifactID) + "/preview/"
	if strings.TrimSpace(accessToken) != "" {
		prefix += "access/" + url.PathEscape(accessToken) + "/"
	}
	prefix += "files/"
	query := "?revision=" + url.QueryEscape(revisionRef)
	return artifactV3PreviewURLAttribute.ReplaceAllFunc(body, func(match []byte) []byte {
		parts := artifactV3PreviewURLAttribute.FindSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		reference := string(parts[2])
		parsed, err := url.Parse(reference)
		if err != nil || parsed.Scheme != "" || parsed.Host != "" || strings.HasPrefix(reference, "//") || strings.HasPrefix(reference, "#") || strings.HasPrefix(reference, "data:") || strings.HasPrefix(reference, "blob:") || strings.HasPrefix(reference, "javascript:") {
			return match
		}
		clean := path.Clean(path.Join(baseDir, parsed.Path))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return match
		}
		rewritten := prefix + artifactV3EscapePreviewPath(clean) + query
		if parsed.RawQuery != "" {
			rewritten += "&asset_query=" + url.QueryEscape(parsed.RawQuery)
		}
		if parsed.Fragment != "" {
			rewritten += "#" + url.PathEscape(parsed.Fragment)
		}
		return append(append(append([]byte{}, parts[1]...), []byte(rewritten)...), parts[3]...)
	})
}

func artifactV3EscapePreviewPath(value string) string {
	parts := strings.Split(value, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

func artifactV3APIBuildEvidence(e pebblestore.ArtifactV3EvidenceProjection, treeOID string) *api.ArtifactV3BuildEvidence {
	if e.Reference == "" || e.Status != "succeeded" || e.CommitOID == "" || e.DigestSHA256 == "" {
		return nil
	}
	return &api.ArtifactV3BuildEvidence{ID: e.Reference, Status: e.Status, CommitOID: e.CommitOID, TreeOID: treeOID}
}

func artifactV3APIValidationEvidence(e pebblestore.ArtifactV3EvidenceProjection, treeOID string) *api.ArtifactV3ValidationEvidence {
	if e.Reference == "" || e.Status != "succeeded" || e.CommitOID == "" || e.DigestSHA256 == "" {
		return nil
	}
	return &api.ArtifactV3ValidationEvidence{ID: e.Reference, Status: "valid", CommitOID: e.CommitOID, TreeOID: treeOID, EvidenceDigests: []string{e.DigestSHA256}}
}

func (a *artifactV3RuntimeAdapter) OpenTurn(ctx context.Context, principal api.ArtifactV3Principal, request api.ArtifactV3OpenTurnRequest) (api.ArtifactV3Turn, error) {
	repository, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, request.ArtifactID)
	if err != nil || !ok || repository.OwnerSessionID != request.SessionID {
		return api.ArtifactV3Turn{}, pebblestore.ErrArtifactV3NotFound
	}
	baseCommit := strings.TrimPrefix(strings.TrimSpace(request.BaseRevisionRef), "revision-")
	if baseCommit != repository.HeadCommitOID {
		return api.ArtifactV3Turn{}, pebblestore.ErrArtifactV3Conflict
	}
	turnID := artifactV3StableID("turn", request.ArtifactID, request.ClientRequestID)
	target := ""
	if len(request.TargetPartIDs) != 0 {
		target = request.TargetPartIDs[0]
	}
	projection, err := a.service.OpenTurn(ctx, pebblestore.ArtifactV3OpenTurnInput{Owner: pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: request.SessionID}, ArtifactID: request.ArtifactID, TurnID: turnID, ExpectedHead: baseCommit, TargetPartID: target, TargetPartIDs: canonicalStrings(request.TargetPartIDs)})
	if err != nil {
		return api.ArtifactV3Turn{}, err
	}
	if projection.Turn == nil {
		return api.ArtifactV3Turn{}, pebblestore.ErrArtifactV3Integrity
	}
	return api.ArtifactV3Turn{TurnID: projection.Turn.TurnID, Revision: projection.Turn.EventSeq, Status: projection.Turn.Status, Intent: request.Intent, TargetPartIDs: canonicalStrings(request.TargetPartIDs), BaseCommitOID: projection.Turn.BaseCommitOID, CreatedAt: projection.Turn.CreatedAt, UpdatedAt: projection.Turn.UpdatedAt}, nil
}

func (a *artifactV3RuntimeAdapter) ReadArtifactV3DirectRevision(ctx context.Context, accountScopeID, userID, sessionID, artifactID, revisionRef string) (map[string][]byte, []pebblestore.ArtifactV3Part, error) {
	revision, err := a.GetRevision(ctx, api.ArtifactV3Principal{AccountScopeID: accountScopeID, UserID: userID}, sessionID, artifactID, revisionRef)
	if err != nil {
		return nil, nil, err
	}
	repository, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, artifactID, pebblestore.ArtifactV3Owner{AccountScopeID: accountScopeID, UserID: userID, SessionID: sessionID}, a.limits)
	if err != nil {
		return nil, nil, err
	}
	page, err := repository.ListFiles(ctx, revision.CommitOID, "", 0)
	if err != nil || page.NextCursor != "" {
		if err == nil {
			err = pebblestore.ErrArtifactV3Quota
		}
		return nil, nil, err
	}
	project := make(map[string][]byte, len(page.Files))
	for _, file := range page.Files {
		body, readErr := repository.ReadFile(ctx, revision.CommitOID, file.Path)
		if readErr != nil {
			return nil, nil, readErr
		}
		project[file.Path] = body
	}
	parts := append([]pebblestore.ArtifactV3Part(nil), revision.Manifest.Parts...)
	return project, parts, nil
}

func (a *artifactV3RuntimeAdapter) SelectArtifactV3DirectHead(ctx context.Context, accountScopeID, userID, sessionID, artifactID, turnID, candidateID string) (tool.ArtifactV3Revision, error) {
	repository, ok, err := a.sessions.GetArtifactV3Repository(accountScopeID, userID, artifactID)
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	if !ok || repository.OwnerSessionID != sessionID {
		return tool.ArtifactV3Revision{}, pebblestore.ErrArtifactV3NotFound
	}
	turn, ok, err := a.sessions.GetArtifactV3Turn(accountScopeID, userID, artifactID, turnID)
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	if !ok {
		return tool.ArtifactV3Revision{}, pebblestore.ErrArtifactV3Conflict
	}
	selected, err := a.SelectCandidate(ctx, api.ArtifactV3Principal{AccountScopeID: accountScopeID, UserID: userID}, api.ArtifactV3SelectCandidateRequest{
		SessionID:            sessionID,
		ArtifactID:           artifactID,
		TurnID:               turnID,
		ClientRequestID:      artifactV3StableID("direct-select", artifactID, turnID, candidateID),
		CandidateID:          candidateID,
		ExpectedHeadRef:      "revision-" + repository.HeadCommitOID,
		ExpectedTurnRevision: turn.EventSeq,
	})
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	return tool.ArtifactV3Revision{CommitOID: selected.Head.CommitOID, TreeOID: selected.Head.TreeOID, ManifestBlobOID: selected.Head.ManifestBlobOID}, nil
}

func (a *artifactV3RuntimeAdapter) SelectCandidate(ctx context.Context, principal api.ArtifactV3Principal, request api.ArtifactV3SelectCandidateRequest) (api.ArtifactV3SelectionResult, error) {
	repository, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, request.ArtifactID)
	if err != nil || !ok || repository.OwnerSessionID != request.SessionID {
		return api.ArtifactV3SelectionResult{}, pebblestore.ErrArtifactV3NotFound
	}
	if "revision-"+repository.HeadCommitOID != request.ExpectedHeadRef {
		return api.ArtifactV3SelectionResult{}, pebblestore.ErrArtifactV3Conflict
	}
	turn, ok, err := a.sessions.GetArtifactV3Turn(principal.AccountScopeID, principal.UserID, request.ArtifactID, request.TurnID)
	if err != nil || !ok || turn.EventSeq != request.ExpectedTurnRevision {
		return api.ArtifactV3SelectionResult{}, pebblestore.ErrArtifactV3Conflict
	}
	owner := pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: request.SessionID}
	selected, err := a.service.Select(ctx, pebblestore.ArtifactV3SelectInput{Owner: owner, ArtifactID: request.ArtifactID, TurnID: request.TurnID, CandidateID: request.CandidateID, TransactionID: strings.TrimSpace(request.ClientRequestID), ExpectedHead: repository.HeadCommitOID})
	if err != nil {
		return api.ArtifactV3SelectionResult{}, err
	}
	if err := a.publishProjection(artifactV3GrantOwner{Owner: owner}, request.ArtifactID, pebblestore.V3SessionMutationArtifactV3HeadSelected, request.ClientRequestID); err != nil {
		return api.ArtifactV3SelectionResult{}, err
	}
	updated, err := a.GetArtifact(ctx, principal, request.SessionID, request.ArtifactID)
	if err != nil {
		return api.ArtifactV3SelectionResult{}, err
	}
	var selectedTurn api.ArtifactV3Turn
	for _, value := range updated.Turns {
		if value.TurnID == request.TurnID {
			selectedTurn = value
			break
		}
	}
	_ = selected
	return api.ArtifactV3SelectionResult{Head: *updated.Head, Turn: selectedTurn}, nil
}

func (a *artifactV3RuntimeAdapter) turns(ctx context.Context, principal api.ArtifactV3Principal, repository pebblestore.ArtifactV3RepositoryProjection) ([]api.ArtifactV3Turn, error) {
	repo, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, repository.ArtifactID, pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: repository.OwnerSessionID}, a.limits)
	if err != nil {
		return nil, err
	}
	cursor := ""
	byTurn := map[string]*api.ArtifactV3Turn{}
	for {
		page, err := repo.ListRefs(ctx, "refs/swarm/turns/", cursor, 500)
		if err != nil {
			return nil, err
		}
		for _, ref := range page.Refs {
			parts := strings.Split(strings.TrimPrefix(ref.Name, "refs/swarm/turns/"), "/")
			if len(parts) != 3 || parts[1] != "candidate" {
				return nil, pebblestore.ErrArtifactV3Integrity
			}
			turnProjection, ok, err := a.sessions.GetArtifactV3Turn(principal.AccountScopeID, principal.UserID, repository.ArtifactID, parts[0])
			if err != nil || !ok {
				return nil, pebblestore.ErrArtifactV3Integrity
			}
			candidateProjection, ok, err := a.sessions.GetArtifactV3Candidate(principal.AccountScopeID, principal.UserID, repository.ArtifactID, parts[0], parts[2])
			if err != nil || !ok || candidateProjection.CommitOID != ref.CommitOID {
				return nil, pebblestore.ErrArtifactV3Integrity
			}
			turn := byTurn[parts[0]]
			if turn == nil {
				turn = &api.ArtifactV3Turn{TurnID: parts[0], Revision: turnProjection.EventSeq, Status: turnProjection.Status, TargetPartIDs: canonicalStrings(append(append([]string(nil), turnProjection.TargetPartIDs...), turnProjection.TargetPartID)), BaseCommitOID: turnProjection.BaseCommitOID, SelectedCandidateID: turnProjection.SelectedCandidateID, CreatedAt: turnProjection.CreatedAt, UpdatedAt: turnProjection.UpdatedAt}
				byTurn[parts[0]] = turn
			}
			revision, err := a.revision(ctx, principal, repository, ref.CommitOID)
			if err != nil {
				return nil, err
			}
			turn.Candidates = append(turn.Candidates, api.ArtifactV3Candidate{CandidateID: parts[2], Status: candidateProjection.Status, Revision: &revision, Build: revision.Build, Validation: revision.Validation})
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	// Git refs prove ready bytes, but cannot enumerate failed slots. Merge the
	// authenticated durable candidate projection without manufacturing revisions.
	slots, err := a.sessions.ListArtifactV3CandidateProjections(principal.AccountScopeID, principal.UserID, repository.ArtifactID)
	if err != nil {
		return nil, err
	}
	for _, slot := range slots {
		// Genesis is represented by revision history, not an iteration turn.
		if slot.CandidateRef == "refs/heads/artifact" && slot.Status == "selected" {
			revision, readErr := a.revision(ctx, principal, repository, slot.CommitOID)
			if readErr != nil || len(revision.Parents) != 0 {
				return nil, pebblestore.ErrArtifactV3Integrity
			}
			continue
		}
		turn := byTurn[slot.TurnID]
		found := false
		if turn != nil {
			for _, candidate := range turn.Candidates {
				if candidate.CandidateID == slot.CandidateID {
					found = true
					break
				}
			}
		}
		if found {
			continue
		}
		if slot.CommitOID != "" || (slot.Status != "failed" && slot.Status != "cancelled") {
			return nil, pebblestore.ErrArtifactV3Integrity
		}
		if turn == nil {
			projection, ok, readErr := a.sessions.GetArtifactV3Turn(principal.AccountScopeID, principal.UserID, repository.ArtifactID, slot.TurnID)
			if readErr != nil || !ok {
				return nil, pebblestore.ErrArtifactV3Integrity
			}
			turn = &api.ArtifactV3Turn{TurnID: slot.TurnID, Revision: projection.EventSeq, Status: projection.Status, TargetPartIDs: canonicalStrings(append(append([]string(nil), projection.TargetPartIDs...), projection.TargetPartID)), BaseCommitOID: projection.BaseCommitOID, SelectedCandidateID: projection.SelectedCandidateID, CreatedAt: projection.CreatedAt, UpdatedAt: projection.UpdatedAt}
			byTurn[slot.TurnID] = turn
		}
		turn.Candidates = append(turn.Candidates, api.ArtifactV3Candidate{CandidateID: slot.CandidateID, Status: slot.Status, Diagnostics: []api.ArtifactV3Diagnostic{{Code: slot.FailureCode, Message: "Designer candidate did not finish; no revision was published"}}})
	}
	out := make([]api.ArtifactV3Turn, 0, len(byTurn))
	for _, turn := range byTurn {
		sort.Slice(turn.Candidates, func(i, j int) bool { return turn.Candidates[i].CandidateID < turn.Candidates[j].CandidateID })
		out = append(out, *turn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

func (a *artifactV3RuntimeAdapter) publishProjection(owner artifactV3GrantOwner, artifactID, eventType, requestID string) error {
	if a.publish == nil {
		return errors.New("artifact v3 realtime publisher is not configured")
	}
	artifact, err := a.GetArtifact(context.Background(), api.ArtifactV3Principal{AccountScopeID: owner.Owner.AccountScopeID, UserID: owner.Owner.UserID}, owner.Owner.SessionID, artifactID)
	if err != nil {
		return err
	}
	return a.publish(artifactV3Principal(owner), artifact, eventType, requestID+":projection")
}

func recoverArtifactV3Repositories(ctx context.Context, adapter *artifactV3RuntimeAdapter) error {
	if adapter == nil || adapter.sessions == nil {
		return nil
	}
	entries, err := os.ReadDir(adapter.repositoryRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".git") {
			continue
		}
		artifactID := strings.TrimSuffix(entry.Name(), ".git")
		ownerBody, readErr := os.ReadFile(filepath.Join(adapter.repositoryRoot, entry.Name(), "swarm-owner.json"))
		if readErr != nil {
			return readErr
		}
		var owner pebblestore.ArtifactV3Owner
		if json.Unmarshal(ownerBody, &owner) != nil {
			return pebblestore.ErrArtifactV3Integrity
		}
		if _, recoverErr := adapter.service.Recover(ctx, owner, artifactID); recoverErr != nil {
			return fmt.Errorf("recover artifact %s: %w", artifactID, recoverErr)
		}
	}
	return nil
}

const artifactV3VideoDerivativeDir = "artifacts-v3/video-derivatives"

// artifactV3VideoBridge is the sole model-facing V3-to-Video-Studio boundary.
// It accepts only exact native V3 identity and lets the server assemble the plan.
type artifactV3VideoBridge struct {
	artifacts *artifactV3RuntimeAdapter
	service   *artifactv3video.Service
	projects  *videoproject.Service
}

func (b *artifactV3VideoBridge) ValidateVideoReference(accountScopeID, userID string, ref pebblestore.ArtifactV3VideoReference) error {
	if b == nil || b.service == nil {
		return errors.New("artifact v3 video conversion authority is unavailable")
	}
	return b.service.ValidateVideoReference(accountScopeID, userID, ref)
}

func (b *artifactV3VideoBridge) ReadVideoReference(ctx context.Context, accountScopeID, userID string, ref pebblestore.ArtifactV3VideoReference) ([]byte, error) {
	if b == nil || b.service == nil {
		return nil, errors.New("artifact v3 video conversion authority is unavailable")
	}
	return b.service.ReadVideoReference(ctx, accountScopeID, userID, ref)
}

func (b *artifactV3VideoBridge) ConvertToPendingProposal(ctx context.Context, principal identity.Principal, input tool.ArtifactV3VideoConversionInput) (pebblestore.VideoEditProposalSnapshot, error) {
	if ctx == nil {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("artifact v3 video conversion requires context")
	}
	if b == nil || b.artifacts == nil || b.service == nil || b.projects == nil || !principal.Valid() {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("artifact v3 video conversion authority is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, err
	}
	input.VideoSessionID, input.ProjectID, input.BaseRevisionID = strings.TrimSpace(input.VideoSessionID), strings.TrimSpace(input.ProjectID), strings.TrimSpace(input.BaseRevisionID)
	input.ArtifactSessionID, input.ArtifactID, input.RevisionRef = strings.TrimSpace(input.ArtifactSessionID), strings.TrimSpace(input.ArtifactID), strings.TrimSpace(input.RevisionRef)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" || input.VideoSessionID == "" || input.ProjectID == "" || input.BaseRevisionID == "" || input.ArtifactSessionID == "" || input.ArtifactID == "" || input.RevisionRef == "" {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("artifact v3 video conversion requires project base and exact source revision")
	}
	project, ok, err := b.projects.GetProject(principal, input.VideoSessionID, input.ProjectID)
	if err != nil || !ok || project.SessionID != input.VideoSessionID || project.CurrentRevisionID != input.BaseRevisionID {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("artifact v3 video conversion project base is stale or unavailable")
	}
	baseRevision, ok, err := b.projects.GetRevision(principal, input.VideoSessionID, input.ProjectID, input.BaseRevisionID)
	if err != nil || !ok || baseRevision.ProjectID != project.ID || baseRevision.SessionID != input.VideoSessionID {
		return pebblestore.VideoEditProposalSnapshot{}, errors.New("artifact v3 video conversion project base revision is unavailable")
	}
	selection, err := b.artifacts.videoSelection(principal, input.ArtifactSessionID, input.ArtifactID, input.RevisionRef)
	if err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, err
	}
	selection.PartID, selection.CaptureStateID = strings.TrimSpace(input.PartID), strings.TrimSpace(input.CaptureStateID)
	conversion, err := b.service.Convert(ctx, principal.AccountScopeID, selection)
	if err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, err
	}
	if baseRevision.Timeline.Metadata["accepted_video_plan"] != nil {
		conversion.Plan.Kind = pebblestore.VideoPlanKindRevision
	}
	proposalID := artifactV3StableID("videopropv3", input.VideoSessionID, input.ProjectID, input.BaseRevisionID, input.ArtifactSessionID, input.ArtifactID, input.RevisionRef, input.RequestID, input.CaptureStateID)
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = "Artifact V3 video proposal"
	}
	return b.projects.CreateEditProposal(ctx, principal, videoproject.CreateEditProposalInput{
		SessionID: input.VideoSessionID, ProjectID: input.ProjectID, ProposalID: proposalID,
		BaseRevisionID: input.BaseRevisionID, Title: title, Rationale: strings.TrimSpace(input.Rationale),
		Intent: pebblestore.VideoEditProposalIntentArtifactV3Convert, Plan: &conversion.Plan, NowUnixMs: time.Now().UnixMilli(),
	})
}

func (a *artifactV3RuntimeAdapter) videoSelection(principal identity.Principal, sessionID, artifactID, revisionRef string) (artifactv3video.Selection, error) {
	if a == nil || a.sessions == nil || !principal.Valid() {
		return artifactv3video.Selection{}, pebblestore.ErrArtifactV3Unauthorized
	}
	sessionID, artifactID, revisionRef = strings.TrimSpace(sessionID), strings.TrimSpace(artifactID), strings.TrimSpace(revisionRef)
	if sessionID == "" || artifactID == "" || !strings.HasPrefix(revisionRef, "revision-") {
		return artifactv3video.Selection{}, errors.New("artifact v3 video conversion requires an exact revision_ref")
	}
	commit := strings.TrimPrefix(revisionRef, "revision-")
	repository, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, artifactID)
	if err != nil {
		return artifactv3video.Selection{}, err
	}
	if !ok || repository.OwnerSessionID != sessionID {
		return artifactv3video.Selection{}, pebblestore.ErrArtifactV3NotFound
	}
	if repository.HeadCommitOID != commit {
		return artifactv3video.Selection{}, errors.New("artifact v3 video source revision is not the selected head")
	}
	revision, ok, err := a.sessions.GetArtifactV3Revision(principal.AccountScopeID, principal.UserID, artifactID, commit)
	if err != nil {
		return artifactv3video.Selection{}, err
	}
	if !ok || revision.CommitOID != commit || revision.TreeOID == "" {
		return artifactv3video.Selection{}, pebblestore.ErrArtifactV3Integrity
	}
	return artifactv3video.Selection{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID, ArtifactID: artifactID, RevisionID: revisionRef, CommitOID: commit, TreeOID: revision.TreeOID}, nil
}

// ReadSelectedHead authenticates ownership, selected-head status, Git identity,
// and successful build/validation before exposing immutable project bytes.
func (a *artifactV3RuntimeAdapter) ReadSelectedHead(ctx context.Context, accountScopeID string, selection artifactv3video.Selection) (artifactv3video.Project, error) {
	return a.readVideoRevision(ctx, accountScopeID, selection, true)
}

// ReadImmutableRevision authenticates the owned immutable Git revision, not the
// mutable selected-head pointer. Previously converted media outlives selection.
func (a *artifactV3RuntimeAdapter) ReadImmutableRevision(ctx context.Context, accountScopeID string, selection artifactv3video.Selection) (artifactv3video.Project, error) {
	return a.readVideoRevision(ctx, accountScopeID, selection, false)
}

func (a *artifactV3RuntimeAdapter) readVideoRevision(ctx context.Context, accountScopeID string, selection artifactv3video.Selection, requireHead bool) (artifactv3video.Project, error) {
	if a == nil || a.sessions == nil || a.service == nil || strings.TrimSpace(accountScopeID) == "" || accountScopeID != selection.AccountScopeID || strings.TrimSpace(selection.UserID) == "" {
		return artifactv3video.Project{}, pebblestore.ErrArtifactV3Unauthorized
	}
	repository, ok, err := a.sessions.GetArtifactV3Repository(accountScopeID, selection.UserID, selection.ArtifactID)
	if err != nil {
		return artifactv3video.Project{}, err
	}
	if !ok || repository.OwnerSessionID != selection.SessionID || (requireHead && repository.HeadCommitOID != selection.CommitOID) || selection.RevisionID != "revision-"+selection.CommitOID {
		return artifactv3video.Project{}, errors.New("selected Artifact V3 head is stale or not owned")
	}
	principal := api.ArtifactV3Principal{AccountScopeID: accountScopeID, UserID: selection.UserID}
	revision, err := a.revision(ctx, principal, repository, selection.CommitOID)
	if err != nil {
		return artifactv3video.Project{}, err
	}
	if revision.TreeOID != selection.TreeOID || revision.Build == nil || revision.Validation == nil || revision.Build.Status != "succeeded" || revision.Validation.Status != "valid" {
		return artifactv3video.Project{}, pebblestore.ErrArtifactV3Integrity
	}
	files, _, err := a.ReadArtifactV3DirectRevision(ctx, accountScopeID, selection.UserID, selection.SessionID, selection.ArtifactID, selection.RevisionID)
	if err != nil {
		return artifactv3video.Project{}, err
	}
	manifestBody := files[pebblestore.ArtifactV3ManifestFilename]
	if len(manifestBody) == 0 {
		return artifactv3video.Project{}, pebblestore.ErrArtifactV3Integrity
	}
	manifestDigest := sha256.Sum256(manifestBody)
	projection, ok, err := a.sessions.GetArtifactV3Revision(accountScopeID, selection.UserID, selection.ArtifactID, selection.CommitOID)
	if err != nil || !ok || projection.EventSeq == 0 {
		return artifactv3video.Project{}, pebblestore.ErrArtifactV3Integrity
	}
	return artifactv3video.Project{SessionID: selection.SessionID, ArtifactID: selection.ArtifactID, RevisionID: selection.RevisionID, CommitOID: revision.CommitOID, TreeOID: revision.TreeOID, ManifestDigestSHA256: hex.EncodeToString(manifestDigest[:]), BuildID: revision.Build.ID, ValidationID: revision.Validation.ID, EventSeq: projection.EventSeq, MediaType: "text/html", AnimationProfile: artifactv3video.DefaultAnimationProfile, Files: files}, nil
}

// artifactV3AnimationRenderer injects only ephemeral render bytes. Source Git is
// never rewritten, and htmlcapture still installs its immutable browser bootstrap.
type artifactV3AnimationRenderer struct{ renderer htmlcapture.AnimationRenderer }

func (r artifactV3AnimationRenderer) request(input artifactv3video.RenderRequest) (htmlcapture.AnimationRequest, error) {
	if input.PartID != "" || (input.CaptureStateID != "" && input.Entrypoint == "") {
		return htmlcapture.AnimationRequest{}, errors.New("spatial Part or capture-state targeting requires an explicit temporal section; whole-project animation cannot silently ignore a target")
	}
	if r.renderer == nil || input.AnimationAdapter != htmlcapture.AnimationVersion || input.DurationMs <= 0 || input.FPS <= 0 || input.FPS != float64(int(input.FPS)) {
		return htmlcapture.AnimationRequest{}, errors.New("trusted Artifact V3 animation renderer is unavailable or timing is invalid")
	}
	var manifest pebblestore.ArtifactV3Manifest
	if json.Unmarshal(input.Project.Files[pebblestore.ArtifactV3ManifestFilename], &manifest) != nil || strings.TrimSpace(manifest.Entrypoint) == "" || len(input.Project.Files[manifest.Entrypoint]) == 0 {
		return htmlcapture.AnimationRequest{}, errors.New("Artifact V3 animation manifest is invalid")
	}
	if input.Entrypoint != "" {
		sections, err := artifactv3video.Sections(input.Project, input.CaptureStateID)
		if err != nil || len(sections) != 1 || sections[0].Entrypoint != input.Entrypoint || sections[0].DurationMs != input.DurationMs {
			return htmlcapture.AnimationRequest{}, errors.New("render target does not match exact storyboard state")
		}
		manifest.Entrypoint = input.Entrypoint
	}
	files := cloneArtifactProject(input.Project.Files)
	duration, fps, declared, err := htmlcapture.AnimationTiming(files[manifest.Entrypoint])
	if err != nil {
		return htmlcapture.AnimationRequest{}, err
	}
	if declared {
		if int64(duration) != input.DurationMs || float64(fps) != input.FPS {
			return htmlcapture.AnimationRequest{}, errors.New("render timing conflicts with authored animation manifest")
		}
	} else {
		if manifest.AnimationProfile != nil {
			return htmlcapture.AnimationRequest{}, errors.New("profiled animation requires authored timing declaration")
		}
		files[manifest.Entrypoint] = injectArtifactV3AnimationAdapter(files[manifest.Entrypoint], input.DurationMs, int(input.FPS))
	}
	// Native V3 accepts CSS/WAAPI motion. The server-owned adapter below makes
	// those timelines deterministically seekable even when author code does not
	// own a requestAnimationFrame loop, so requiring artifact-owned rAF here would
	// reject valid CSS-only animations after the adapter is successfully bound.
	return htmlcapture.AnimationRequest{Entry: manifest.Entrypoint, Files: files, DurationMS: int(input.DurationMs), FPS: int(input.FPS), OutputFPS: int(input.FPS), Quality: htmlcapture.AnimationQualityStandard, RequireLivePlayback: false, AllowBooleanReady: !declared}, nil
}

func (r artifactV3AnimationRenderer) Preflight(ctx context.Context, input artifactv3video.RenderRequest) error {
	request, err := r.request(input)
	if err != nil {
		return err
	}
	result, err := r.renderer.PreflightAnimation(ctx, request)
	return htmlcapture.WithAnimationDiagnostics(err, result.Diagnostics)
}

func (r artifactV3AnimationRenderer) Render(ctx context.Context, input artifactv3video.RenderRequest) (artifactv3video.RenderResult, error) {
	request, err := r.request(input)
	if err != nil {
		return artifactv3video.RenderResult{}, err
	}
	result, err := r.renderer.RenderAnimation(ctx, request)
	if err != nil {
		return artifactv3video.RenderResult{}, htmlcapture.WithAnimationDiagnostics(err, result.Diagnostics)
	}
	if result.DurationMS <= 0 || result.FPS <= 0 || result.FrameCount <= 0 {
		return artifactv3video.RenderResult{}, errors.New("Artifact V3 animation renderer returned incomplete timing evidence")
	}
	expectedFrames := int((int64(result.DurationMS)*int64(result.FPS) + 999) / 1000)
	if result.FrameCount != expectedFrames {
		return artifactv3video.RenderResult{}, errors.New("Artifact V3 animation renderer frame count does not match duration/fps")
	}
	return artifactv3video.RenderResult{FallbackPNG: result.PreviewPNG, SilentMP4: result.MP4, DurationMs: int64(result.DurationMS), FPS: float64(result.FPS)}, nil
}

func injectArtifactV3AnimationAdapter(body []byte, durationMs int64, fps int) []byte {
	script := fmt.Sprintf(`<script data-swarm-artifact-v3-animation>(function(){"use strict";const duration=%d,fps=%d;const domReady=new Promise(resolve=>{if(document.readyState==="loading")document.addEventListener("DOMContentLoaded",resolve,{once:true});else resolve()});const animations=()=>Array.from(document.getAnimations({subtree:true}));const tick=()=>new Promise(resolve=>setTimeout(resolve,0));const bindFallback=()=>{const bootstrap=globalThis.__SWARM_ANIMATION_BOOTSTRAP_V1__;if(bootstrap&&(bootstrap.settled||bootstrap.lifecycle.includes("bind_claimed")))return;globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready:async()=>{await domReady;for(const animation of animations())animation.pause();return {duration_ms:duration,fps}},seek:async timeMs=>{await domReady;for(const animation of animations()){animation.pause();animation.currentTime=timeMs}await tick();void document.documentElement.offsetHeight;await tick();for(const animation of animations()){animation.currentTime=timeMs;animation.pause()}void document.documentElement.offsetHeight;document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}})};if(document.readyState==="loading")globalThis.addEventListener("DOMContentLoaded",bindFallback,{once:true,capture:true});else bindFallback()})();</script>`, durationMs, fps)
	lower := strings.ToLower(string(body))
	if index := strings.Index(lower, "</head>"); index >= 0 {
		out := make([]byte, 0, len(body)+len(script))
		out = append(out, body[:index]...)
		out = append(out, script...)
		out = append(out, body[index:]...)
		return out
	}
	return append([]byte(script), body...)
}

type artifactV3DerivativeStore struct {
	root string
	mu   sync.Mutex
}

func newArtifactV3DerivativeStore(root string) (*artifactV3DerivativeStore, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == "" {
		return nil, errors.New("Artifact V3 derivative root is not configured")
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return nil, err
	}
	return &artifactV3DerivativeStore{root: root}, nil
}

// PutAtomic publishes one immutable two-file directory by rename. Any failure
// removes staging and leaves no visible partial derivative set.
func (s *artifactV3DerivativeStore) PutAtomic(ctx context.Context, sessionID, artifactID string, derivatives []artifactv3video.Derivative) error {
	if ctx == nil {
		return errors.New("Artifact V3 derivative publication requires context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(artifactID) == "" || len(derivatives) < 1 || len(derivatives) > 32 {
		return errors.New("Artifact V3 derivative publication requires source identity and one to 32 outputs")
	}
	ids, seen := make([]string, 0, 2), map[string]bool{}
	receipts := make(map[string]pebblestore.ArtifactV3VideoReference, len(derivatives))
	for _, derivative := range derivatives {
		if !validArtifactV3Derivative(derivative) {
			return errors.New("Artifact V3 derivative is invalid or duplicated")
		}
		if !seen[derivative.ID] {
			seen[derivative.ID], ids = true, append(ids, derivative.ID)
		}
		refBytes, err := json.Marshal(derivative.Reference)
		if err != nil {
			return err
		}
		receipts[sha256Hex(refBytes)] = derivative.Reference
	}
	sort.Strings(ids)
	parent := filepath.Join(s.root, artifactV3StorageKey(sessionID, artifactID))
	receiptBytes, err := json.Marshal(receipts)
	if err != nil {
		return err
	}
	final := filepath.Join(parent, artifactV3StorageKey(append(ids, sha256Hex(receiptBytes))...))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ensurePrivateDirectory(parent); err != nil {
		return err
	}
	if info, err := os.Lstat(final); err == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Artifact V3 derivative set is not a private directory")
		}
		marker, markerErr := os.ReadFile(filepath.Join(final, "complete"))
		if markerErr != nil || string(marker) != strings.Join(ids, "\n")+"\n" {
			return errors.New("existing Artifact V3 derivative set is incomplete")
		}
		storedReceipts, err := os.ReadFile(filepath.Join(final, "references.json"))
		if err != nil || !bytes.Equal(storedReceipts, receiptBytes) {
			return errors.New("existing Artifact V3 derivative receipts failed integrity validation")
		}
		for _, derivative := range derivatives {
			if err := ctx.Err(); err != nil {
				return err
			}
			body, readErr := os.ReadFile(filepath.Join(final, derivative.ID))
			if readErr != nil || sha256Hex(body) != derivative.DigestSHA256 {
				return errors.New("existing Artifact V3 derivative set failed integrity validation")
			}
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return err
	}
	for _, derivative := range derivatives {
		if err := os.WriteFile(filepath.Join(stage, derivative.ID), derivative.Bytes, 0o600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(stage, "references.json"), receiptBytes, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "complete"), []byte(strings.Join(ids, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(stage, final)
}

func (s *artifactV3DerivativeStore) Read(ctx context.Context, sessionID, artifactID string, ref pebblestore.ArtifactV3VideoReference) ([]byte, error) {
	derivativeID := ref.DerivativeID
	if ctx == nil {
		return nil, errors.New("Artifact V3 derivative read requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || !validArtifactV3DerivativeID(derivativeID) {
		return nil, errors.New("Artifact V3 derivative identity is invalid")
	}
	parent := filepath.Join(s.root, artifactV3StorageKey(sessionID, artifactID))
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		setRoot := filepath.Join(parent, entry.Name())
		marker, markerErr := os.ReadFile(filepath.Join(setRoot, "complete"))
		if markerErr != nil || !strings.Contains(string(marker), derivativeID+"\n") {
			continue
		}
		receiptBytes, receiptErr := os.ReadFile(filepath.Join(setRoot, "references.json"))
		var receipts map[string]pebblestore.ArtifactV3VideoReference
		if receiptErr != nil || json.Unmarshal(receiptBytes, &receipts) != nil || receipts[artifactV3ReferenceKey(ref)] != ref {
			continue
		}
		path := filepath.Join(setRoot, derivativeID)
		info, statErr := os.Lstat(path)
		if statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, readErr
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return body, nil
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
	}
	return nil, os.ErrNotExist
}

func validArtifactV3Derivative(derivative artifactv3video.Derivative) bool {
	return validArtifactV3DerivativeID(derivative.ID) && (derivative.MediaType == "image/png" || derivative.MediaType == "video/mp4") && len(derivative.Bytes) != 0 && derivative.DigestSHA256 == strings.TrimPrefix(derivative.ID, "av3der_") && derivative.DigestSHA256 == sha256Hex(derivative.Bytes)
}

func validArtifactV3DerivativeID(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "av3der_") || len(value) != len("av3der_")+sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "av3der_"))
	return err == nil && len(decoded) == sha256.Size
}

func artifactV3StorageKey(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(strings.TrimSpace(part)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sha256Hex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func artifactV3ReferenceKey(ref pebblestore.ArtifactV3VideoReference) string {
	body, _ := json.Marshal(ref)
	return sha256Hex(body)
}

func artifactV3Owner(g tool.ArtifactV3AuthorGrant) pebblestore.ArtifactV3Owner {
	return pebblestore.ArtifactV3Owner{AccountScopeID: g.AccountScopeID, UserID: g.UserID, SessionID: g.OwnerSessionID}
}
func artifactV3AuthorPrincipal(g tool.ArtifactV3AuthorGrant) tool.ArtifactV3AuthorPrincipal {
	return tool.ArtifactV3AuthorPrincipal{AccountScopeID: g.AccountScopeID, UserID: g.UserID, ProducerSessionID: g.ProducerSessionID, ProducerRunID: g.ProducerRunID}
}

// The first producer binding requires server-injected context. Subsequent reads
// compare the persisted exact producer, not just two caller-supplied strings.
func (a *artifactV3RuntimeAdapter) LoadAuthorDraft(ctx context.Context, p tool.ArtifactV3AuthorPrincipal, g tool.ArtifactV3AuthorGrant) (tool.ArtifactV3AuthorDraft, error) {
	zero := tool.ArtifactV3AuthorDraft{}
	if p != artifactV3AuthorPrincipal(g) || p.AccountScopeID == "" || p.UserID == "" || p.ProducerSessionID == "" || p.ProducerRunID == "" {
		return zero, tool.ErrArtifactV3AuthorUnauthorized
	}
	r, found, err := a.sessions.GetArtifactV3Repository(p.AccountScopeID, p.UserID, g.ArtifactID)
	if err != nil || !found || r.OwnerSessionID != g.OwnerSessionID {
		return zero, tool.ErrArtifactV3AuthorUnauthorized
	}
	d, found := r.Drafts[g.ID]
	if !found {
		return zero, tool.ErrArtifactV3AuthorUnauthorized
	}
	if d.ExpiresAt <= time.Now().UnixMilli() {
		return zero, tool.ErrArtifactV3AuthorExpired
	}
	unbound := g
	unbound.ProducerSessionID, unbound.ProducerRunID = "", ""
	raw, err := json.Marshal(unbound)
	if err != nil || string(raw) != string(d.Grant) {
		return zero, tool.ErrArtifactV3AuthorUnauthorized
	}
	state := zero
	if len(d.State) != 0 {
		if err := json.Unmarshal(d.State, &state); err != nil {
			return zero, err
		}
		if digestArtifactProject(state.Project) != d.Digest {
			return zero, pebblestore.ErrArtifactV3Integrity
		}
	}
	state.Sequence = d.Sequence
	if state.ProducerSessionID == "" {
		trusted, ok := tool.ArtifactV3GrantFromContext(ctx)
		if !ok || !reflect.DeepEqual(trusted, g) {
			return zero, tool.ErrArtifactV3AuthorUnauthorized
		}
		state.ProducerSessionID, state.ProducerRunID = p.ProducerSessionID, p.ProducerRunID
		return a.saveAuthorDraft(g, state)
	}
	if state.ProducerSessionID != p.ProducerSessionID || state.ProducerRunID != p.ProducerRunID {
		return zero, tool.ErrArtifactV3AuthorUnauthorized
	}
	if !state.Publishing && state.Finished == nil && r.HeadCommitOID != g.BaseCommitOID {
		return zero, tool.ErrArtifactV3AuthorConflict
	}
	return state, nil
}

func (a *artifactV3RuntimeAdapter) SaveAuthorDraft(ctx context.Context, p tool.ArtifactV3AuthorPrincipal, g tool.ArtifactV3AuthorGrant, state tool.ArtifactV3AuthorDraft) (tool.ArtifactV3AuthorDraft, error) {
	current, err := a.LoadAuthorDraft(ctx, p, g)
	if err != nil {
		return tool.ArtifactV3AuthorDraft{}, err
	}
	if current.Sequence != state.Sequence || current.Publishing || current.Finished != nil || state.Publishing || state.Finished != nil {
		return tool.ArtifactV3AuthorDraft{}, tool.ErrArtifactV3AuthorConflict
	}
	state.ProducerSessionID, state.ProducerRunID = current.ProducerSessionID, current.ProducerRunID
	return a.saveAuthorDraft(g, state)
}

func (a *artifactV3RuntimeAdapter) saveAuthorDraft(g tool.ArtifactV3AuthorGrant, state tool.ArtifactV3AuthorDraft) (tool.ArtifactV3AuthorDraft, error) {
	zero := tool.ArtifactV3AuthorDraft{}
	if len(state.Project) > g.Limits.MaxFiles {
		return zero, tool.ErrArtifactV3AuthorQuota
	}
	var total int64
	for path, body := range state.Project {
		total += int64(len(body))
		if len(path) > g.Limits.MaxPathBytes || int64(len(body)) > g.Limits.MaxFileBytes || total > g.Limits.MaxTreeBytes {
			return zero, tool.ErrArtifactV3AuthorQuota
		}
	}
	state.Gate = boundedArtifactV3Gate(state.Gate)
	if len(state.History) > 8 {
		state.History = state.History[len(state.History)-8:]
	}
	history := make([]tool.ArtifactV3AuthorGate, 0, len(state.History))
	for _, gate := range state.History {
		history = append(history, *boundedArtifactV3Gate(&gate))
	}
	state.History = history
	r, found, err := a.sessions.GetArtifactV3Repository(g.AccountScopeID, g.UserID, g.ArtifactID)
	if err != nil {
		return zero, err
	}
	if !found || r.OwnerSessionID != g.OwnerSessionID {
		return zero, tool.ErrArtifactV3AuthorUnauthorized
	}
	d, found := r.Drafts[g.ID]
	if !found || d.Sequence != state.Sequence {
		return zero, tool.ErrArtifactV3AuthorConflict
	}
	d.State, err = json.Marshal(state)
	if err != nil {
		return zero, err
	}
	d.Digest = digestArtifactProject(state.Project)
	d.Status = "fixing"
	// Source writes before the first gate are creation, not repair. Keep that
	// durable status through the initial browser build so clients can observe it.
	if state.Project == nil || (g.Initial && state.Gate == nil && len(state.History) == 0) {
		d.Status = "creating"
	}
	if state.Gate != nil && !state.Gate.Ready {
		d.Status = "error"
	}
	if state.Publishing {
		d.Status = "publishing"
	}
	if state.Finished != nil {
		d.Status = "ready"
	}
	saved, err := a.service.SaveDraft(artifactV3Owner(g), g.ArtifactID, artifactV3StableID("draft", g.ID, fmt.Sprint(state.Sequence), sha256Hex(d.State)), r.IntentReference, d, state.Sequence)
	if err != nil {
		return zero, err
	}
	state.Sequence = saved.Sequence
	// SaveDraft commits through the durable store, but the active Desktop needs
	// the same primary-mutation/outbox wakeup used by ready revisions while a
	// long author tool is still running. Publish only the safe public projection.
	if a.publish != nil {
		if err := a.publishProjection(artifactV3GrantOwner{Owner: artifactV3Owner(g)}, g.ArtifactID, pebblestore.V3SessionMutationArtifactV3DraftSaved, artifactV3StableID("draft-progress", g.ID, fmt.Sprint(saved.Sequence))); err != nil {
			return zero, err
		}
	}
	return state, nil
}

func boundedArtifactV3Gate(input *tool.ArtifactV3AuthorGate) *tool.ArtifactV3AuthorGate {
	if input == nil {
		return nil
	}
	gate := *input
	gate.Build.OutputFiles = nil
	if len(gate.Preview.EvidenceDigests) > 64 {
		gate.Preview.EvidenceDigests = gate.Preview.EvidenceDigests[:64]
	}
	gate.Preview.EvidenceDigests = append([]string(nil), gate.Preview.EvidenceDigests...)
	for i, digest := range gate.Preview.EvidenceDigests {
		if len(digest) > 256 {
			gate.Preview.EvidenceDigests[i] = digest[:256]
		}
	}
	if len(gate.Build.ID) > 256 {
		gate.Build.ID = gate.Build.ID[:256]
	}
	if len(gate.Preview.ID) > 256 {
		gate.Preview.ID = gate.Preview.ID[:256]
	}
	if len(gate.Build.Status) > 64 {
		gate.Build.Status = gate.Build.Status[:64]
	}
	if len(gate.Preview.Status) > 64 {
		gate.Preview.Status = gate.Preview.Status[:64]
	}
	bound := func(input []tool.ArtifactV3Diagnostic) []tool.ArtifactV3Diagnostic {
		if len(input) > 64 {
			input = input[:64]
		}
		out := append([]tool.ArtifactV3Diagnostic(nil), input...)
		clip := func(s string, n int) string {
			if len(s) > n {
				return strings.ToValidUTF8(s[:n], "")
			}
			return s
		}
		for i := range out {
			out[i].Stage = clip(out[i].Stage, 64)
			out[i].Code = clip(out[i].Code, 128)
			out[i].Message = clip(out[i].Message, 2048)
			out[i].Path = clip(out[i].Path, 512)
		}
		return out
	}
	gate.Diagnostics = bound(gate.Diagnostics)
	gate.Build.Diagnostics = bound(gate.Build.Diagnostics)
	gate.Preview.Diagnostics = bound(gate.Preview.Diagnostics)
	return &gate
}

func (a *artifactV3RuntimeAdapter) finishAuthorDraft(g tool.ArtifactV3AuthorGrant, draft tool.ArtifactV3AuthorDraft, revision tool.ArtifactV3Revision) (tool.ArtifactV3Revision, error) {
	draft.Finished = &tool.ArtifactV3AuthorFinish{Revision: revision, Gate: *draft.Gate}
	_, err := a.saveAuthorDraft(g, draft)
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	return revision, nil
}

// ResolveArtifactV3DirectDraft treats the primary handle only as a locator.
// Authority comes from the stored grant and its exact persisted producer binding.
func (a *artifactV3RuntimeAdapter) ResolveArtifactV3DirectDraft(ctx context.Context, p tool.ArtifactV3AuthorPrincipal, h tool.ArtifactV3DraftHandle) (tool.ArtifactV3AuthorGrant, error) {
	var grant tool.ArtifactV3AuthorGrant
	if ctx == nil || a == nil || a.sessions == nil || p.AccountScopeID == "" || p.UserID == "" || p.ProducerRunID == "" || h.SessionID != p.ProducerSessionID {
		return grant, tool.ErrArtifactV3AuthorUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return grant, err
	}
	repository, found, err := a.sessions.GetArtifactV3Repository(p.AccountScopeID, p.UserID, h.ArtifactID)
	if err != nil || !found || repository.OwnerSessionID != h.SessionID {
		return grant, tool.ErrArtifactV3AuthorUnauthorized
	}
	draft, found := repository.Drafts[h.GrantID]
	if !found || json.Unmarshal(draft.Grant, &grant) != nil {
		return tool.ArtifactV3AuthorGrant{}, tool.ErrArtifactV3AuthorUnauthorized
	}
	if grant.ID != h.GrantID || grant.ArtifactID != h.ArtifactID || grant.TurnID != h.TurnID || grant.CandidateID != h.CandidateID || grant.OwnerSessionID != h.SessionID || grant.AccountScopeID != p.AccountScopeID || grant.UserID != p.UserID {
		return tool.ArtifactV3AuthorGrant{}, tool.ErrArtifactV3AuthorUnauthorized
	}
	var state tool.ArtifactV3AuthorDraft
	if len(draft.State) == 0 || json.Unmarshal(draft.State, &state) != nil || state.ProducerSessionID != p.ProducerSessionID || state.ProducerRunID != p.ProducerRunID {
		return tool.ArtifactV3AuthorGrant{}, tool.ErrArtifactV3AuthorUnauthorized
	}
	grant.ProducerSessionID, grant.ProducerRunID = state.ProducerSessionID, state.ProducerRunID
	if _, err := a.LoadAuthorDraft(ctx, p, grant); err != nil {
		return tool.ArtifactV3AuthorGrant{}, err
	}
	return grant, nil
}

// artifactV3PublicDraft deliberately does not convert the private grant or raw
// diagnostic text. Renderer/compiler errors can contain source and host paths.
func artifactV3PublicDraft(repository pebblestore.ArtifactV3RepositoryProjection) (*api.ArtifactV3DraftSummary, string, error) {
	label := strings.Join(strings.Fields(repository.IntentReference), " ")
	if len([]rune(label)) > 100 {
		label = string([]rune(label)[:100])
	}
	if label == "" {
		label = "Untitled artifact"
	}
	var latest pebblestore.ArtifactV3DraftProjection
	for _, draft := range repository.Drafts {
		if latest.GrantID == "" || draft.EventSeq > latest.EventSeq || (draft.EventSeq == latest.EventSeq && draft.GrantID > latest.GrantID) {
			latest = draft
		}
	}
	if latest.GrantID == "" {
		return nil, label, nil
	}
	var state tool.ArtifactV3AuthorDraft
	if len(latest.State) != 0 && json.Unmarshal(latest.State, &state) != nil {
		return nil, label, pebblestore.ErrArtifactV3Integrity
	}
	if body := state.Project["index.html"]; len(body) != 0 {
		if title := artifactV3DocumentTitle(body); title != "Untitled artifact" && title != "" {
			label = title
		}
	}
	out := &api.ArtifactV3DraftSummary{SessionID: repository.OwnerSessionID, ArtifactID: repository.ArtifactID, Status: latest.Status, Sequence: latest.Sequence, ProjectionSeq: latest.EventSeq, Diagnostics: []api.ArtifactV3Diagnostic{}, History: []api.ArtifactV3DraftGate{}}
	if state.Gate != nil {
		out.Diagnostics = artifactV3PublicGateDiagnostics(*state.Gate)
	}
	if out.Status == "error" && len(out.Diagnostics) == 0 {
		out.Diagnostics = []api.ArtifactV3Diagnostic{{Stage: "authoring", Code: "draft_authoring_failed", Message: "Artifact creation or repair stopped before completion."}}
	}
	history := state.History
	if len(history) > 8 {
		history = history[len(history)-8:]
	}
	for _, gate := range history {
		out.History = append(out.History, api.ArtifactV3DraftGate{Ready: gate.Ready, Diagnostics: artifactV3PublicGateDiagnostics(gate)})
	}
	return out, label, nil
}

func artifactV3PublicGateDiagnostics(gate tool.ArtifactV3AuthorGate) []api.ArtifactV3Diagnostic {
	out := []api.ArtifactV3Diagnostic{}
	if gate.Ready {
		return out
	}
	// Public progress uses fixed messages, never strings returned by a compiler,
	// browser, provider, or author. Full bounded diagnostics stay capability-only.
	if gate.Build.Status != "succeeded" && gate.Build.Status != "valid" {
		out = append(out, api.ArtifactV3Diagnostic{Stage: "build", Code: "draft_build_failed", Message: "The artifact needs a build repair."})
	} else {
		out = append(out, api.ArtifactV3Diagnostic{Stage: "validation", Code: "draft_validation_failed", Message: "The artifact needs a preview or validation repair."})
	}
	return out
}

// ResumeArtifactV3DirectDraft uses the public current projection as a locator,
// never a caller-supplied grant or producer. The store checks run liveness again
// inside the canonical session mutation, alongside the draft/head CAS.
func (a *artifactV3RuntimeAdapter) ResumeArtifactV3DirectDraft(ctx context.Context, p tool.ArtifactV3AuthorPrincipal, request tool.ArtifactV3DraftResumeRequest) (tool.ArtifactV3AuthorGrant, error) {
	zero := tool.ArtifactV3AuthorGrant{}
	if ctx == nil || p.AccountScopeID == "" || p.UserID == "" || p.ProducerRunID == "" || request.SessionID != p.ProducerSessionID {
		return zero, tool.ErrArtifactV3AuthorUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	repository, found, err := a.sessions.GetArtifactV3Repository(p.AccountScopeID, p.UserID, request.ArtifactID)
	if err != nil {
		return zero, err
	}
	if !found || repository.OwnerSessionID != request.SessionID {
		return zero, tool.ErrArtifactV3AuthorUnauthorized
	}
	if repository.EventSeq != request.ExpectedProjectionSeq || repository.HeadCommitOID != request.ExpectedHead {
		return zero, tool.ErrArtifactV3AuthorConflict
	}
	var draft pebblestore.ArtifactV3DraftProjection
	for _, candidate := range repository.Drafts {
		if draft.GrantID == "" || candidate.EventSeq > draft.EventSeq || (candidate.EventSeq == draft.EventSeq && candidate.GrantID > draft.GrantID) {
			draft = candidate
		}
	}
	if draft.GrantID == "" || draft.Sequence != request.ExpectedSequence {
		return zero, tool.ErrArtifactV3AuthorConflict
	}
	var grant tool.ArtifactV3AuthorGrant
	var state tool.ArtifactV3AuthorDraft
	if json.Unmarshal(draft.Grant, &grant) != nil || json.Unmarshal(draft.State, &state) != nil || digestArtifactProject(state.Project) != draft.Digest {
		return zero, pebblestore.ErrArtifactV3Integrity
	}
	if grant.AccountScopeID != p.AccountScopeID || grant.UserID != p.UserID || grant.OwnerSessionID != p.ProducerSessionID || grant.ArtifactID != request.ArtifactID || state.ProducerSessionID != p.ProducerSessionID {
		return zero, tool.ErrArtifactV3AuthorUnauthorized
	}
	if state.Finished != nil || (!state.Publishing && grant.BaseCommitOID != repository.HeadCommitOID) {
		return zero, tool.ErrArtifactV3AuthorConflict
	}
	oldID := draft.GrantID
	grant.ID = artifactV3StableID("resume", oldID, p.ProducerRunID, fmt.Sprint(draft.Sequence))
	grant.ExpiresAt = time.Now().Add(30 * time.Minute).UnixMilli()
	state.ProducerRunID = p.ProducerRunID
	// A deliberate later-run handoff gets a new bounded repair budget. Source
	// and diagnostic history remain intact; ordinary retries never reset it.
	if !state.Publishing {
		state.Attempt = 0
		// Resume invalidates the current gate, not its diagnostic evidence.
		// Without this append a failed first gate disappears on later-run repair.
		if state.Gate != nil {
			state.History = append(state.History, *boundedArtifactV3Gate(state.Gate))
			if len(state.History) > 8 {
				state.History = state.History[len(state.History)-8:]
			}
		}
		state.Gate = nil
	}
	draft.GrantID, draft.ExpiresAt, draft.Status = grant.ID, grant.ExpiresAt, "fixing"
	if state.Publishing {
		draft.Status = "publishing"
	}
	draft.Grant, err = json.Marshal(grant)
	if err != nil {
		return zero, err
	}
	draft.State, err = json.Marshal(state)
	if err != nil {
		return zero, err
	}
	err = a.service.ResumeDraft(artifactV3Owner(grant), grant.ArtifactID, grant.ID, draft, draft.Sequence, pebblestore.ArtifactV3DraftResume{GrantID: oldID, ProjectionSeq: request.ExpectedProjectionSeq, ExpectedHead: request.ExpectedHead, ProducerRunID: p.ProducerRunID})
	if err != nil {
		return zero, err
	}
	grant.ProducerSessionID, grant.ProducerRunID = p.ProducerSessionID, p.ProducerRunID
	return grant, nil
}

func (a *artifactV3RuntimeAdapter) LocateArtifactV3DirectDraft(ctx context.Context, p tool.ArtifactV3AuthorPrincipal, artifactID string) (tool.ArtifactV3DraftResumeRequest, error) {
	zero := tool.ArtifactV3DraftResumeRequest{}
	if ctx == nil || p.AccountScopeID == "" || p.UserID == "" || p.ProducerSessionID == "" {
		return zero, tool.ErrArtifactV3AuthorUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	repository, found, err := a.sessions.GetArtifactV3Repository(p.AccountScopeID, p.UserID, artifactID)
	if err != nil {
		return zero, err
	}
	if !found || repository.OwnerSessionID != p.ProducerSessionID {
		return zero, tool.ErrArtifactV3AuthorUnauthorized
	}
	public, _, err := artifactV3PublicDraft(repository)
	if err != nil {
		return zero, err
	}
	if public == nil {
		return zero, tool.ErrArtifactV3AuthorInvalid
	}
	return tool.ArtifactV3DraftResumeRequest{SessionID: repository.OwnerSessionID, ArtifactID: artifactID, ExpectedSequence: public.Sequence, ExpectedProjectionSeq: repository.EventSeq, ExpectedHead: repository.HeadCommitOID}, nil
}
