package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/htmlcapture"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
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
	service      *pebblestore.ArtifactV3Service
	sessions     *pebblestore.SessionStore
	repositoryRoot string
	evidenceRoot string
	limits       pebblestore.ArtifactV3Limits
	renderer     htmlcapture.Renderer
	publish      func(identity.Principal, api.ArtifactV3Artifact, string, string) error

	mu      sync.RWMutex
	grants  map[string]artifactV3GrantOwner
	builds  map[string]tool.ArtifactV3BuildResult
	previews map[string]tool.ArtifactV3PreviewResult
}

type artifactV3GrantOwner struct {
	Owner pebblestore.ArtifactV3Owner
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
		if err != nil || !ok || repository.OwnerSessionID != owner.SessionID || repository.HeadCommitOID != strings.TrimSpace(request.BaseCommitOID) || (request.ProjectionSeq != 0 && repository.EventSeq != request.ProjectionSeq) {
			return tool.ArtifactV3AuthorGrant{}, pebblestore.ErrArtifactV3Conflict
		}
	}
	turnID := artifactV3StableID("turn", artifactID, request.TaskCallID)
	candidateID := artifactV3StableID("candidate", turnID, fmt.Sprint(request.CandidateIndex))
	grantID := artifactV3StableID("grant", artifactID, turnID, candidateID)
	grant := tool.ArtifactV3AuthorGrant{
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
		if _, err := a.service.OpenTurn(ctx, pebblestore.ArtifactV3OpenTurnInput{Owner: owner, ArtifactID: artifactID, TurnID: turnID, ExpectedHead: grant.BaseCommitOID, TargetPartID: target}); err != nil {
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
	for grantID, owner := range a.grants {
		_ = grantID
		if artifactV3StableID("grant", artifactID, turnID, candidateID) == grantID {
			return owner, nil
		}
	}
	return artifactV3GrantOwner{}, tool.ErrArtifactV3AuthorUnauthorized
}

func (a *artifactV3RuntimeAdapter) MaterializeBase(ctx context.Context, artifactID, commitOID, destination string) error {
	owner, err := a.ownerForBase(artifactID, commitOID)
	if err != nil {
		return err
	}
	repository, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, artifactID, owner.Owner, a.limits)
	if err != nil {
		return err
	}
	return repository.Materialize(ctx, commitOID, destination)
}

func (a *artifactV3RuntimeAdapter) ownerForBase(artifactID, commitOID string) (artifactV3GrantOwner, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, owner := range a.grants {
		repository, ok, err := a.sessions.GetArtifactV3Repository(owner.Owner.AccountScopeID, owner.Owner.UserID, artifactID)
		if err == nil && ok && repository.HeadCommitOID == commitOID {
			return owner, nil
		}
	}
	return artifactV3GrantOwner{}, tool.ErrArtifactV3AuthorUnauthorized
}

func (a *artifactV3RuntimeAdapter) SubmitProject(ctx context.Context, request tool.ArtifactV3SubmitRequest) (tool.ArtifactV3Revision, error) {
	owner, err := a.ownerFor(request.ArtifactID, request.TurnID, request.CandidateID)
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	project := pebblestore.ArtifactV3Project{Files: request.Project}
	transactionID := artifactV3StableID("tx", request.ArtifactID, request.TurnID, request.CandidateID, request.ProjectDigest)
	if request.Initial {
		created, err := a.service.Create(ctx, pebblestore.ArtifactV3CreateInput{Owner: owner.Owner, ArtifactID: request.ArtifactID, TransactionID: transactionID, Project: project, Message: owner.Prompt})
		if err != nil {
			return tool.ArtifactV3Revision{}, err
		}
		if err := a.publishProjection(owner, request.ArtifactID, pebblestore.V3SessionMutationArtifactV3GenesisCommitted, transactionID); err != nil {
			return tool.ArtifactV3Revision{}, err
		}
		return tool.ArtifactV3Revision{CommitOID: created.Revision.CommitOID, TreeOID: created.Revision.TreeOID, ManifestBlobOID: created.Revision.ManifestBlobOID}, nil
	}
	repository, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, request.ArtifactID, owner.Owner, a.limits)
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	candidate, err := repository.Candidate(ctx, pebblestore.ArtifactV3CandidateRequest{TurnID: request.TurnID, CandidateID: request.CandidateID, TransactionID: transactionID, BaseCommit: request.BaseCommitOID, Project: project, Message: owner.Prompt})
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	build := artifactV3Evidence(request.Build.ID, request.Build.Status, candidate.CommitOID, request.ProjectDigest)
	previewDigest := ""
	if len(request.Preview.EvidenceDigests) != 0 {
		previewDigest = request.Preview.EvidenceDigests[0]
	}
	preview := artifactV3Evidence(request.Preview.ID, "succeeded", candidate.CommitOID, previewDigest)
	committed, err := a.service.SubmitCandidate(ctx, pebblestore.ArtifactV3SubmitCandidateInput{Owner: owner.Owner, ArtifactID: request.ArtifactID, TurnID: request.TurnID, CandidateID: request.CandidateID, TransactionID: transactionID, ExpectedHead: request.BaseCommitOID, Project: project, Message: owner.Prompt, Build: build, Preview: preview})
	if err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	if err := a.publishProjection(owner, request.ArtifactID, pebblestore.V3SessionMutationArtifactV3CandidateCommitted, transactionID); err != nil {
		return tool.ArtifactV3Revision{}, err
	}
	return tool.ArtifactV3Revision{CommitOID: committed.Revision.CommitOID, TreeOID: committed.Revision.TreeOID, ManifestBlobOID: committed.Revision.ManifestBlobOID}, nil
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
	_, err = a.service.RecordCandidateTerminal(owner.Owner, failure.ArtifactID, failure.TurnID, failure.CandidateID, artifactV3StableID("fail", failure.ArtifactID, failure.TurnID, failure.CandidateID), "failed", strings.TrimSpace(failure.Code), 0)
	return err
}

func (a *artifactV3RuntimeAdapter) Build(_ context.Context, request tool.ArtifactV3BuildRequest) (tool.ArtifactV3BuildResult, error) {
	_, diagnostics := parseArtifactV3Manifest(request.Project)
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

func bytesContainsFold(body []byte, text string) bool { return strings.Contains(strings.ToLower(string(body)), strings.ToLower(text)) }

func parseArtifactV3Manifest(project map[string][]byte) (pebblestore.ArtifactV3Manifest, []tool.ArtifactV3Diagnostic) {
	body, ok := project[pebblestore.ArtifactV3ManifestFilename]
	if !ok {
		return pebblestore.ArtifactV3Manifest{}, []tool.ArtifactV3Diagnostic{{Stage: "build", Code: "manifest_missing", Message: "swarm-artifact.json is required", Path: pebblestore.ArtifactV3ManifestFilename}}
	}
	var manifest pebblestore.ArtifactV3Manifest
	if err := json.Unmarshal(body, &manifest); err != nil || manifest.SchemaVersion != pebblestore.ArtifactV3ManifestVersion || strings.TrimSpace(manifest.Entrypoint) == "" {
		return manifest, []tool.ArtifactV3Diagnostic{{Stage: "build", Code: "manifest_invalid", Message: "swarm-artifact.json is not a valid Artifact V3 manifest", Path: pebblestore.ArtifactV3ManifestFilename}}
	}
	if _, ok := project[manifest.Entrypoint]; !ok {
		return manifest, []tool.ArtifactV3Diagnostic{{Stage: "build", Code: "entrypoint_missing", Message: "the manifest entrypoint does not exist", Path: manifest.Entrypoint}}
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
	results, err := a.renderer.Capture(ctx, htmlcapture.Request{Entry: manifest.Entrypoint, Files: request.Build.OutputFiles, StateIDs: []string{"default"}})
	if err != nil {
		return tool.ArtifactV3PreviewResult{Status: "failed", Diagnostics: []tool.ArtifactV3Diagnostic{{Stage: "preview", Code: "browser_capture_failed", Message: "the complete Artifact V3 project failed its browser preview gate"}}}, nil
	}
	if len(results) != 1 || len(results[0].PNG) == 0 {
		return tool.ArtifactV3PreviewResult{Status: "failed", Diagnostics: []tool.ArtifactV3Diagnostic{{Stage: "preview", Code: "browser_evidence_missing", Message: "the browser preview gate returned no inspectable pixels"}}}, nil
	}
	missing := unresolvedArtifactV3Targets(manifest, request.TargetPartIDs, request.Build.OutputFiles)
	if len(missing) != 0 {
		return tool.ArtifactV3PreviewResult{Status: "failed", Diagnostics: missing}, nil
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
	result := tool.ArtifactV3PreviewResult{ID: id, Status: "valid", EvidenceDigests: []string{digestHex}}
	a.mu.Lock()
	a.previews[id] = result
	a.mu.Unlock()
	return result, nil
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
	for path := range input { paths = append(paths, path) }
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths { h.Write([]byte(path)); h.Write([]byte{0}); sum := sha256.Sum256(input[path]); h.Write(sum[:]) }
	return hex.EncodeToString(h.Sum(nil))
}

func (a *artifactV3RuntimeAdapter) ListArtifacts(ctx context.Context, principal api.ArtifactV3Principal, sessionID string, limit int) ([]api.ArtifactV3Artifact, error) {
	if limit <= 0 || limit > 500 { limit = 500 }
	entries, err := os.ReadDir(a.repositoryRoot)
	if err != nil { return nil, err }
	out := make([]api.ArtifactV3Artifact, 0, min(limit, len(entries)))
	for _, entry := range entries {
		if len(out) >= limit { break }
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".git") { continue }
		artifactID := strings.TrimSuffix(entry.Name(), ".git")
		repository, ok, readErr := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, artifactID)
		if readErr != nil || !ok || repository.OwnerSessionID != sessionID { continue }
		artifact, readErr := a.artifact(ctx, principal, repository)
		if readErr != nil { return nil, readErr }
		out = append(out, artifact)
	}
	sort.Slice(out, func(i,j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

func (a *artifactV3RuntimeAdapter) GetArtifact(ctx context.Context, principal api.ArtifactV3Principal, sessionID, artifactID string) (api.ArtifactV3Artifact, error) {
	repository, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, artifactID)
	if err != nil || !ok || repository.OwnerSessionID != sessionID { return api.ArtifactV3Artifact{}, pebblestore.ErrArtifactV3NotFound }
	return a.artifact(ctx, principal, repository)
}

func (a *artifactV3RuntimeAdapter) artifact(ctx context.Context, principal api.ArtifactV3Principal, repository pebblestore.ArtifactV3RepositoryProjection) (api.ArtifactV3Artifact, error) {
	revision, err := a.revision(ctx, principal, repository, repository.HeadCommitOID)
	if err != nil { return api.ArtifactV3Artifact{}, err }
	turns, err := a.turns(ctx, principal, repository)
	if err != nil { return api.ArtifactV3Artifact{}, err }
	return api.ArtifactV3Artifact{ID: repository.ArtifactID, OwnerSessionID: repository.OwnerSessionID, Status: "ready", Revision: repository.EventSeq, PartCount: len(revision.Manifest.Parts), Parts: revision.Manifest.Parts, Head: &revision, CurrentRevision: &revision, Turns: turns, UpdatedAt: repository.UpdatedAt}, nil
}

func (a *artifactV3RuntimeAdapter) ListRevisions(ctx context.Context, principal api.ArtifactV3Principal, sessionID, artifactID, cursor string, limit int) (api.ArtifactV3RevisionPage, error) {
	repository, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, artifactID)
	if err != nil || !ok || repository.OwnerSessionID != sessionID { return api.ArtifactV3RevisionPage{}, pebblestore.ErrArtifactV3NotFound }
	repo, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, artifactID, pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID}, a.limits)
	if err != nil { return api.ArtifactV3RevisionPage{}, err }
	page, err := repo.ListRevisions(ctx, cursor, limit)
	if err != nil { return api.ArtifactV3RevisionPage{}, err }
	out := api.ArtifactV3RevisionPage{NextCursor: page.NextCursor}
	for _, value := range page.Revisions { revision, err := a.revision(ctx, principal, repository, value.CommitOID); if err != nil { return api.ArtifactV3RevisionPage{}, err }; out.Revisions = append(out.Revisions, revision) }
	return out, nil
}

func (a *artifactV3RuntimeAdapter) GetRevision(ctx context.Context, principal api.ArtifactV3Principal, sessionID, artifactID, revisionRef string) (api.ArtifactV3Revision, error) {
	repository, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, artifactID)
	if err != nil || !ok || repository.OwnerSessionID != sessionID { return api.ArtifactV3Revision{}, pebblestore.ErrArtifactV3NotFound }
	commit := strings.TrimPrefix(revisionRef, "revision-")
	return a.revision(ctx, principal, repository, commit)
}

func (a *artifactV3RuntimeAdapter) revision(ctx context.Context, principal api.ArtifactV3Principal, repository pebblestore.ArtifactV3RepositoryProjection, commit string) (api.ArtifactV3Revision, error) {
	projection, ok, err := a.sessions.GetArtifactV3Revision(principal.AccountScopeID, principal.UserID, repository.ArtifactID, commit)
	if err != nil || !ok { return api.ArtifactV3Revision{}, pebblestore.ErrArtifactV3NotFound }
	repo, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, repository.ArtifactID, pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: repository.OwnerSessionID}, a.limits)
	if err != nil { return api.ArtifactV3Revision{}, err }
	gitRevision, err := repo.ReadRevision(ctx, commit)
	if err != nil || gitRevision.TreeOID != projection.TreeOID || gitRevision.ManifestBlobOID != projection.ManifestBlobOID { return api.ArtifactV3Revision{}, pebblestore.ErrArtifactV3Integrity }
	return api.ArtifactV3Revision{RevisionRef: "revision-"+commit, CommitOID: commit, TreeOID: projection.TreeOID, ManifestBlobOID: projection.ManifestBlobOID, Parents: projection.ParentCommitOIDs, Manifest: gitRevision.Manifest, FileCount: projection.FileCount, TreeBytes: projection.TreeBytes, ChangedFiles: projection.ChangedFiles, CreatedAt: projection.CreatedAt}, nil
}

func (a *artifactV3RuntimeAdapter) OpenPreview(ctx context.Context, principal api.ArtifactV3Principal, sessionID, artifactID, revisionRef string) (api.ArtifactV3Preview, error) {
	revision, err := a.GetRevision(ctx, principal, sessionID, artifactID, revisionRef)
	if err != nil { return api.ArtifactV3Preview{}, err }
	repository, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, artifactID, pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID}, a.limits)
	if err != nil { return api.ArtifactV3Preview{}, err }
	body, err := repository.ReadFile(ctx, revision.CommitOID, revision.Manifest.Entrypoint)
	if err != nil { return api.ArtifactV3Preview{}, err }
	return api.ArtifactV3Preview{RevisionRef: revision.RevisionRef, CommitOID: revision.CommitOID, MediaType: "text/html; charset=utf-8", Body: body, ETag: `"`+revision.TreeOID+`"`}, nil
}

func (a *artifactV3RuntimeAdapter) OpenTurn(ctx context.Context, principal api.ArtifactV3Principal, request api.ArtifactV3OpenTurnRequest) (api.ArtifactV3Turn, error) {
	repository, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, request.ArtifactID)
	if err != nil || !ok || repository.OwnerSessionID != request.SessionID { return api.ArtifactV3Turn{}, pebblestore.ErrArtifactV3NotFound }
	baseCommit := strings.TrimPrefix(strings.TrimSpace(request.BaseRevisionRef), "revision-")
	if baseCommit != repository.HeadCommitOID { return api.ArtifactV3Turn{}, pebblestore.ErrArtifactV3Conflict }
	turnID := artifactV3StableID("turn", request.ArtifactID, request.ClientRequestID)
	target := ""; if len(request.TargetPartIDs) != 0 { target = request.TargetPartIDs[0] }
	projection, err := a.service.OpenTurn(ctx, pebblestore.ArtifactV3OpenTurnInput{Owner:pebblestore.ArtifactV3Owner{AccountScopeID:principal.AccountScopeID,UserID:principal.UserID,SessionID:request.SessionID},ArtifactID:request.ArtifactID,TurnID:turnID,ExpectedHead:baseCommit,TargetPartID:target})
	if err != nil { return api.ArtifactV3Turn{}, err }
	if projection.Turn == nil { return api.ArtifactV3Turn{}, pebblestore.ErrArtifactV3Integrity }
	return api.ArtifactV3Turn{TurnID:projection.Turn.TurnID,Revision:projection.Turn.EventSeq,Status:projection.Turn.Status,Intent:request.Intent,TargetPartIDs:canonicalStrings(request.TargetPartIDs),BaseCommitOID:projection.Turn.BaseCommitOID,CreatedAt:projection.Turn.CreatedAt,UpdatedAt:projection.Turn.UpdatedAt},nil
}

func (a *artifactV3RuntimeAdapter) SelectCandidate(ctx context.Context, principal api.ArtifactV3Principal, request api.ArtifactV3SelectCandidateRequest) (api.ArtifactV3SelectionResult, error) {
	repository, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, request.ArtifactID)
	if err != nil || !ok || repository.OwnerSessionID != request.SessionID { return api.ArtifactV3SelectionResult{}, pebblestore.ErrArtifactV3NotFound }
	if "revision-"+repository.HeadCommitOID != request.ExpectedHeadRef { return api.ArtifactV3SelectionResult{}, pebblestore.ErrArtifactV3Conflict }
	turn, ok, err := a.sessions.GetArtifactV3Turn(principal.AccountScopeID, principal.UserID, request.ArtifactID, request.TurnID)
	if err != nil || !ok || turn.EventSeq != request.ExpectedTurnRevision { return api.ArtifactV3SelectionResult{}, pebblestore.ErrArtifactV3Conflict }
	owner := pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: request.SessionID}
	selected, err := a.service.Select(ctx, pebblestore.ArtifactV3SelectInput{Owner: owner, ArtifactID: request.ArtifactID, TurnID: request.TurnID, CandidateID: request.CandidateID, TransactionID: strings.TrimSpace(request.ClientRequestID), ExpectedHead: repository.HeadCommitOID})
	if err != nil { return api.ArtifactV3SelectionResult{}, err }
	if err := a.publishProjection(artifactV3GrantOwner{Owner: owner}, request.ArtifactID, pebblestore.V3SessionMutationArtifactV3HeadSelected, request.ClientRequestID); err != nil { return api.ArtifactV3SelectionResult{}, err }
	updated, err := a.GetArtifact(ctx, principal, request.SessionID, request.ArtifactID)
	if err != nil { return api.ArtifactV3SelectionResult{}, err }
	var selectedTurn api.ArtifactV3Turn
	for _, value := range updated.Turns { if value.TurnID == request.TurnID { selectedTurn = value; break } }
	_ = selected
	return api.ArtifactV3SelectionResult{Head: *updated.Head, Turn: selectedTurn}, nil
}

func (a *artifactV3RuntimeAdapter) turns(_ context.Context, _ api.ArtifactV3Principal, _ pebblestore.ArtifactV3RepositoryProjection) ([]api.ArtifactV3Turn, error) {
	// Turn and candidate records remain available through exact select inputs and
	// durable realtime projections. A bounded catalog scan will be added when the
	// Pebble store exposes a public typed turn iterator.
	return []api.ArtifactV3Turn{}, nil
}

func (a *artifactV3RuntimeAdapter) publishProjection(owner artifactV3GrantOwner, artifactID, eventType, requestID string) error {
	if a.publish == nil { return errors.New("artifact v3 realtime publisher is not configured") }
	artifact, err := a.GetArtifact(context.Background(), api.ArtifactV3Principal{AccountScopeID:owner.Owner.AccountScopeID,UserID:owner.Owner.UserID}, owner.Owner.SessionID, artifactID)
	if err != nil { return err }
	return a.publish(artifactV3Principal(owner), artifact, eventType, requestID+":projection")
}

func recoverArtifactV3Repositories(ctx context.Context, adapter *artifactV3RuntimeAdapter) error {
	if adapter == nil || adapter.sessions == nil { return nil }
	entries, err := os.ReadDir(adapter.repositoryRoot)
	if err != nil { return err }
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".git") { continue }
		artifactID := strings.TrimSuffix(entry.Name(), ".git")
		ownerBody, readErr := os.ReadFile(filepath.Join(adapter.repositoryRoot, entry.Name(), "swarm-owner.json"))
		if readErr != nil { return readErr }
		var owner pebblestore.ArtifactV3Owner
		if json.Unmarshal(ownerBody, &owner) != nil { return pebblestore.ErrArtifactV3Integrity }
		if _, recoverErr := adapter.service.Recover(ctx, owner, artifactID); recoverErr != nil {
			return fmt.Errorf("recover artifact %s: %w", artifactID, recoverErr)
		}
	}
	return nil
}
