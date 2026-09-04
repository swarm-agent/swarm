package runtime

import (
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
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/api"
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
		if err != nil || !ok || repository.OwnerSessionID != owner.SessionID || repository.HeadCommitOID != strings.TrimSpace(request.BaseCommitOID) || (request.ProjectionSeq != 0 && repository.EventSeq != request.ProjectionSeq) {
			return tool.ArtifactV3AuthorGrant{}, pebblestore.ErrArtifactV3Conflict
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
	wanted := artifactV3StableID("grant", artifactID, turnID, candidateID)
	for grantID, owner := range a.grants {
		if wanted == grantID {
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
	if _, ok, readErr := a.sessions.GetArtifactV3Repository(owner.Owner.AccountScopeID, owner.Owner.UserID, failure.ArtifactID); readErr != nil || !ok {
		// Initial authoring has no durable repository/turn until a validated genesis
		// commit exists, so a failed child leaves no partial Artifact V3 identity.
		return nil
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
		return manifest, []tool.ArtifactV3Diagnostic{{Stage: "build", Code: "manifest_invalid", Message: "swarm-artifact.json must contain only schema_version, entrypoint, and parts; every part requires id, label, and locator {kind, path/value/paths} that resolves to a project file", Path: pebblestore.ArtifactV3ManifestFilename}}
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
	previewFiles := cloneArtifactProject(request.Build.OutputFiles)
	previewFiles[manifest.Entrypoint] = injectArtifactV3CaptureRuntime(previewFiles[manifest.Entrypoint])
	requiredSelectors := make([]string, 0, len(manifest.Parts))
	for _, part := range manifest.Parts {
		if part.Locator.Kind == "selector" && part.Locator.Path == manifest.Entrypoint && strings.TrimSpace(part.Locator.Value) != "" {
			requiredSelectors = append(requiredSelectors, strings.TrimSpace(part.Locator.Value))
		}
	}
	results, err := a.renderer.Capture(ctx, htmlcapture.Request{Entry: manifest.Entrypoint, Files: previewFiles, StateIDs: []string{"default"}, RequiredSelectors: requiredSelectors, ViewportWidth: 1440, ViewportHeight: 900})
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
	entries, err := os.ReadDir(a.repositoryRoot)
	if err != nil {
		return nil, err
	}
	out := make([]api.ArtifactV3Artifact, 0, min(limit, len(entries)))
	for _, entry := range entries {
		if len(out) >= limit {
			break
		}
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".git") {
			continue
		}
		artifactID := strings.TrimSuffix(entry.Name(), ".git")
		if artifactID == "" {
			continue
		}
		repository, ok, readErr := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, artifactID)
		if readErr != nil || !ok || repository.OwnerSessionID != sessionID {
			continue
		}
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
	revision, err := a.revision(ctx, principal, repository, repository.HeadCommitOID)
	if err != nil {
		return api.ArtifactV3Artifact{}, err
	}
	turns, err := a.turns(ctx, principal, repository)
	if err != nil {
		return api.ArtifactV3Artifact{}, err
	}
	return api.ArtifactV3Artifact{ID: repository.ArtifactID, OwnerSessionID: repository.OwnerSessionID, IntentReference: repository.IntentReference, ArtifactRef: artifactV3Reference(repository), Status: "ready", Revision: repository.EventSeq, PartCount: len(revision.Manifest.Parts), Parts: revision.Manifest.Parts, Head: &revision, CurrentRevision: &revision, Revisions: []api.ArtifactV3Revision{revision}, Turns: turns, UpdatedAt: repository.UpdatedAt}, nil
}

func (a *artifactV3RuntimeAdapter) ListRevisions(ctx context.Context, principal api.ArtifactV3Principal, sessionID, artifactID, cursor string, limit int) (api.ArtifactV3RevisionPage, error) {
	repository, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, artifactID)
	if err != nil || !ok || repository.OwnerSessionID != sessionID {
		return api.ArtifactV3RevisionPage{}, pebblestore.ErrArtifactV3NotFound
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
	projection, err := a.service.OpenTurn(ctx, pebblestore.ArtifactV3OpenTurnInput{Owner: pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: request.SessionID}, ArtifactID: request.ArtifactID, TurnID: turnID, ExpectedHead: baseCommit, TargetPartID: target})
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
				turn = &api.ArtifactV3Turn{TurnID: parts[0], Revision: turnProjection.EventSeq, Status: turnProjection.Status, TargetPartIDs: canonicalStrings([]string{turnProjection.TargetPartID}), BaseCommitOID: turnProjection.BaseCommitOID, SelectedCandidateID: turnProjection.SelectedCandidateID, CreatedAt: turnProjection.CreatedAt, UpdatedAt: turnProjection.UpdatedAt}
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
	conversion, err := b.service.Convert(ctx, principal.AccountScopeID, selection)
	if err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return pebblestore.VideoEditProposalSnapshot{}, err
	}
	proposalID := artifactV3StableID("videopropv3", input.VideoSessionID, input.ProjectID, input.BaseRevisionID, input.ArtifactSessionID, input.ArtifactID, input.RevisionRef, input.RequestID)
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
	if a == nil || a.sessions == nil || a.service == nil || strings.TrimSpace(accountScopeID) == "" || accountScopeID != selection.AccountScopeID || strings.TrimSpace(selection.UserID) == "" {
		return artifactv3video.Project{}, pebblestore.ErrArtifactV3Unauthorized
	}
	repository, ok, err := a.sessions.GetArtifactV3Repository(accountScopeID, selection.UserID, selection.ArtifactID)
	if err != nil {
		return artifactv3video.Project{}, err
	}
	if !ok || repository.OwnerSessionID != selection.SessionID || repository.HeadCommitOID != selection.CommitOID || selection.RevisionID != "revision-"+selection.CommitOID {
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
	return artifactv3video.Project{SessionID: selection.SessionID, ArtifactID: selection.ArtifactID, RevisionID: selection.RevisionID, CommitOID: revision.CommitOID, TreeOID: revision.TreeOID, ManifestDigestSHA256: hex.EncodeToString(manifestDigest[:]), BuildID: revision.Build.ID, ValidationID: revision.Validation.ID, EventSeq: repository.EventSeq, MediaType: "text/html", AnimationProfile: artifactv3video.DefaultAnimationProfile, Files: files}, nil
}

// artifactV3AnimationRenderer injects only ephemeral render bytes. Source Git is
// never rewritten, and htmlcapture still installs its immutable browser bootstrap.
type artifactV3AnimationRenderer struct{ renderer htmlcapture.AnimationRenderer }

func (r artifactV3AnimationRenderer) request(input artifactv3video.RenderRequest) (htmlcapture.AnimationRequest, error) {
	if r.renderer == nil || input.AnimationAdapter != htmlcapture.AnimationVersion || input.DurationMs <= 0 || input.FPS <= 0 || input.FPS != float64(int(input.FPS)) {
		return htmlcapture.AnimationRequest{}, errors.New("trusted Artifact V3 animation renderer is unavailable or timing is invalid")
	}
	var manifest pebblestore.ArtifactV3Manifest
	if json.Unmarshal(input.Project.Files[pebblestore.ArtifactV3ManifestFilename], &manifest) != nil || strings.TrimSpace(manifest.Entrypoint) == "" || len(input.Project.Files[manifest.Entrypoint]) == 0 {
		return htmlcapture.AnimationRequest{}, errors.New("Artifact V3 animation manifest is invalid")
	}
	files := cloneArtifactProject(input.Project.Files)
	files[manifest.Entrypoint] = injectArtifactV3AnimationAdapter(files[manifest.Entrypoint], input.DurationMs, int(input.FPS))
	// Native V3 accepts CSS/WAAPI motion. The server-owned adapter below makes
	// those timelines deterministically seekable even when author code does not
	// own a requestAnimationFrame loop, so requiring artifact-owned rAF here would
	// reject valid CSS-only animations after the adapter is successfully bound.
	return htmlcapture.AnimationRequest{Entry: manifest.Entrypoint, Files: files, DurationMS: int(input.DurationMs), FPS: int(input.FPS), OutputFPS: int(input.FPS), Quality: htmlcapture.AnimationQualityStandard, RequireLivePlayback: false}, nil
}

func (r artifactV3AnimationRenderer) Preflight(ctx context.Context, input artifactv3video.RenderRequest) error {
	request, err := r.request(input)
	if err != nil {
		return err
	}
	_, err = r.renderer.PreflightAnimation(ctx, request)
	return err
}

func (r artifactV3AnimationRenderer) Render(ctx context.Context, input artifactv3video.RenderRequest) (artifactv3video.RenderResult, error) {
	request, err := r.request(input)
	if err != nil {
		return artifactv3video.RenderResult{}, err
	}
	result, err := r.renderer.RenderAnimation(ctx, request)
	if err != nil {
		return artifactv3video.RenderResult{}, err
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
	script := fmt.Sprintf(`<script data-swarm-artifact-v3-animation>(function(){"use strict";const duration=%d,fps=%d;const domReady=new Promise(resolve=>{if(document.readyState==="loading")document.addEventListener("DOMContentLoaded",resolve,{once:true});else resolve()});const animations=()=>Array.from(document.getAnimations({subtree:true}));globalThis.__SWARM_ANIMATION_BIND__({version:"swarm.animation/v1",ready:async()=>{await domReady;for(const animation of animations())animation.pause();return {duration_ms:duration,fps}},seek:async timeMs=>{await domReady;for(const animation of animations()){animation.pause();animation.currentTime=timeMs}document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);return {time_ms:timeMs}}})})();</script>`, durationMs, fps)
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
	if s == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(artifactID) == "" || len(derivatives) != 2 {
		return errors.New("Artifact V3 derivative publication requires source identity and exactly two outputs")
	}
	ids, seen := make([]string, 0, 2), map[string]bool{}
	for _, derivative := range derivatives {
		if !validArtifactV3Derivative(derivative) || seen[derivative.ID] {
			return errors.New("Artifact V3 derivative is invalid or duplicated")
		}
		seen[derivative.ID], ids = true, append(ids, derivative.ID)
	}
	sort.Strings(ids)
	parent := filepath.Join(s.root, artifactV3StorageKey(sessionID, artifactID))
	final := filepath.Join(parent, artifactV3StorageKey(ids...))
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
	if err := os.WriteFile(filepath.Join(stage, "complete"), []byte(strings.Join(ids, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(stage, final)
}

func (s *artifactV3DerivativeStore) Read(ctx context.Context, sessionID, artifactID, derivativeID string) ([]byte, error) {
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
