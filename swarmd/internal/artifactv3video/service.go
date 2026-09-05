// Package artifactv3video owns conversion of one authenticated Artifact V3 Git
// head into digest-bound Video Studio inputs. It deliberately has no Artifact V2
// dependency and never mutates source Git bytes.
package artifactv3video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	DefaultDurationMs       int64   = 4000
	DefaultFPS              float64 = 30
	DefaultAnimationProfile         = "motion_ui"
	maxDurationMs           int64   = 60000
	animationAdapterVersion         = "swarm.animation/v1"
)

// Project is the complete immutable Artifact V3 Git project returned by the V3 authority.
type Project struct {
	SessionID            string
	ArtifactID           string
	RevisionID           string
	CommitOID            string
	TreeOID              string
	ManifestDigestSHA256 string
	BuildID              string
	ValidationID         string
	EventSeq             uint64
	MediaType            string
	AnimationProfile     string
	Files                map[string][]byte
}

// Selection authenticates one exact selected V3 head. Optional Part and capture
// state identity scope rendering without changing the selected Git revision.
type Selection struct {
	AccountScopeID string
	UserID         string
	SessionID      string
	ArtifactID     string
	RevisionID     string
	CommitOID      string
	TreeOID        string
	PartID         string
	CaptureStateID string
	DurationMs     int64
	FPS            float64
}

// ArtifactAuthority is the only source of Artifact V3 ownership and current-head truth.
type ArtifactAuthority interface {
	ReadSelectedHead(context.Context, string, Selection) (Project, error)
	ReadImmutableRevision(context.Context, string, Selection) (Project, error)
}

// RenderRequest is an ephemeral trusted-render input. AnimationAdapter is
// server-owned and injected outside the Git project bytes.
type RenderRequest struct {
	Project          Project
	PartID           string
	CaptureStateID   string
	DurationMs       int64
	FPS              float64
	AnimationAdapter string
	Entrypoint       string
}

// RenderResult binds derivative bytes to the renderer-observed timing. The
// conversion service rejects a renderer that returns a valid container for a
// different duration or frame cadence.
type RenderResult struct {
	FallbackPNG []byte
	SilentMP4   []byte
	DurationMs  int64
	FPS         float64
}

// Renderer performs trusted preflight and deterministic fallback/MP4 rendering.
type Renderer interface {
	Preflight(context.Context, RenderRequest) error
	Render(context.Context, RenderRequest) (RenderResult, error)
}

// DerivativeStore atomically publishes derivatives with exact source receipts. Implementations must
// leave no visible bytes when PutAtomic returns an error.
type DerivativeStore interface {
	PutAtomic(context.Context, string, string, []Derivative) error
	Read(context.Context, string, string, pebblestore.ArtifactV3VideoReference) ([]byte, error)
}

// Derivative is one immutable private V3-owned output.
type Derivative struct {
	ID           string
	MediaType    string
	DigestSHA256 string
	Bytes        []byte
	Reference    pebblestore.ArtifactV3VideoReference
}

// Conversion is the native pending Video Studio plan and exact references.
type Conversion struct {
	Source   pebblestore.ArtifactV3VideoReference
	Fallback pebblestore.ArtifactV3VideoReference
	MP4      pebblestore.ArtifactV3VideoReference
	Plan     pebblestore.VideoPlanProposal
}

type Service struct {
	artifacts ArtifactAuthority
	renderer  Renderer
	storage   DerivativeStore
}

func New(artifacts ArtifactAuthority, renderer Renderer, storage DerivativeStore) *Service {
	return &Service{artifacts: artifacts, renderer: renderer, storage: storage}
}

// Convert authenticates the exact current head, renders before publishing, and
// atomically stores both outputs. Source Git identity remains read-only on every path.
func (s *Service) Convert(ctx context.Context, accountScopeID string, selection Selection) (Conversion, error) {
	if ctx == nil {
		return Conversion{}, errors.New("artifact V3 video conversion requires context")
	}
	if err := ctx.Err(); err != nil {
		return Conversion{}, err
	}
	if s == nil || s.artifacts == nil || s.renderer == nil || s.storage == nil {
		return Conversion{}, errors.New("artifact V3 video service is not fully configured")
	}
	selection.AccountScopeID = accountScopeID
	if err := validateSelection(selection); err != nil {
		return Conversion{}, err
	}
	project, err := s.artifacts.ReadSelectedHead(ctx, accountScopeID, selection)
	if err != nil {
		return Conversion{}, fmt.Errorf("authenticate selected Artifact V3 head: %w", err)
	}
	if err := validateProject(selection, project); err != nil {
		return Conversion{}, err
	}
	project = cloneProject(project)
	if project.AnimationProfile == "" {
		project.AnimationProfile = DefaultAnimationProfile
	}
	sections, err := projectSections(project, selection)
	if err != nil {
		return Conversion{}, err
	}
	_, fps, err := normalizedTiming(selection.DurationMs, selection.FPS)
	if err != nil {
		return Conversion{}, err
	}
	conversion := Conversion{Plan: pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Summary: "Native Artifact V3 temporal conversion"}}
	var derivatives []Derivative
	for _, section := range sections {
		request := RenderRequest{Project: cloneProject(project), CaptureStateID: section.CaptureStateID, Entrypoint: section.Entrypoint, DurationMs: section.DurationMs, FPS: fps, AnimationAdapter: animationAdapterVersion}
		if err := s.renderer.Preflight(ctx, request); err != nil {
			return Conversion{}, fmt.Errorf("trusted Artifact V3 preflight failed: %w", err)
		}
		rendered, err := s.renderer.Render(ctx, request)
		if err != nil {
			return Conversion{}, fmt.Errorf("trusted Artifact V3 render failed: %w", err)
		}
		if rendered.DurationMs != section.DurationMs || rendered.FPS != fps {
			return Conversion{}, errors.New("trusted Artifact V3 render timing does not match the requested duration/fps")
		}
		if err := validateRenderedDerivatives(rendered.FallbackPNG, rendered.SilentMP4); err != nil {
			return Conversion{}, err
		}
		fallback, mp4 := derivative("image/png", rendered.FallbackPNG), derivative("video/mp4", rendered.SilentMP4)
		sectionSelection := selection
		sectionSelection.CaptureStateID = section.CaptureStateID
		one := assemble(project, sectionSelection, section.DurationMs, fps, fallback, mp4)
		part := one.Plan.Parts[0]
		part.ID, part.Title, part.ProductionState, part.FilmingRequirements = section.ID, section.Title, section.ProductionState, section.FilmingRequirements
		if section.ProductionState == pebblestore.VideoProductionStatePending {
			part.ArtifactV3Visual, part.VisualMediaType, part.AnimationCandidates = part.ArtifactV3Still, "image/png", nil
			part.SourceEndMs = 0
		}
		fallback.Reference, mp4.Reference = one.Fallback, one.MP4
		derivatives = append(derivatives, fallback, mp4)
		conversion.Plan.Parts = append(conversion.Plan.Parts, part)
		if len(conversion.Plan.Parts) == 1 {
			conversion.Source, conversion.Fallback, conversion.MP4 = one.Source, one.Fallback, one.MP4
		}
	}
	if err := pebblestore.ValidateVideoPlanForIntent(pebblestore.VideoEditProposalIntentArtifactV3Convert, conversion.Plan); err != nil {
		return Conversion{}, fmt.Errorf("assemble native Artifact V3 video plan: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Conversion{}, err
	}
	// Rendering is long-running: reject a head that changed during capture before
	// publishing any derivative bytes. Proposal creation performs its own CAS.
	current, err := s.artifacts.ReadSelectedHead(ctx, accountScopeID, selection)
	if err != nil {
		return Conversion{}, fmt.Errorf("revalidate selected Artifact V3 head: %w", err)
	}
	if err := validateProject(selection, current); err != nil {
		return Conversion{}, err
	}
	if err := s.storage.PutAtomic(ctx, project.SessionID, project.ArtifactID, derivatives); err != nil {
		return Conversion{}, fmt.Errorf("persist Artifact V3 video derivatives atomically: %w", err)
	}
	return conversion, nil
}

// ValidateVideoReference authenticates one exact native V3 source or derivative
// without exposing bytes. userID is enforced by the Artifact authority's selected
// head lookup and retained here to satisfy the shared Video Studio boundary.
func (s *Service) ValidateVideoReference(accountScopeID, userID string, ref pebblestore.ArtifactV3VideoReference) error {
	if s == nil {
		return errors.New("Artifact V3 video reference authority is unavailable")
	}
	return s.validateVideoReference(context.Background(), accountScopeID, userID, ref)
}

func (s *Service) validateVideoReference(ctx context.Context, accountScopeID, userID string, ref pebblestore.ArtifactV3VideoReference) error {
	if ctx == nil {
		return errors.New("Artifact V3 video validation requires context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.artifacts == nil || s.storage == nil {
		return errors.New("Artifact V3 video reference authority is unavailable")
	}
	if strings.TrimSpace(accountScopeID) == "" || strings.TrimSpace(userID) == "" {
		return errors.New("Artifact V3 video reference requires authenticated account and user")
	}
	if ref.DerivativeID == "" {
		selection := Selection{AccountScopeID: accountScopeID, UserID: userID, SessionID: ref.SessionID, ArtifactID: ref.ArtifactID, RevisionID: ref.RevisionID, CommitOID: ref.CommitOID, TreeOID: ref.TreeOID, PartID: ref.PartID, CaptureStateID: ref.CaptureStateID, DurationMs: ref.DurationMs, FPS: ref.FPS}
		project, err := s.artifacts.ReadSelectedHead(ctx, accountScopeID, selection)
		if err != nil {
			return err
		}
		if project.AnimationProfile == "" {
			project.AnimationProfile = DefaultAnimationProfile
		}
		if err := validateProject(selection, project); err != nil {
			return err
		}
		if ref.ManifestDigestSHA256 != project.ManifestDigestSHA256 || ref.BuildID != project.BuildID || ref.ValidationID != project.ValidationID || ref.EventSeq != project.EventSeq || ref.DigestSHA256 != project.ManifestDigestSHA256 || ref.MediaType != "text/html" || ref.AnimationProfile != project.AnimationProfile {
			return errors.New("Artifact V3 source reference does not match authenticated selected head")
		}
		return nil
	}
	_, err := s.read(ctx, accountScopeID, userID, ref)
	return err
}

func (s *Service) ReadVideoReference(ctx context.Context, accountScopeID, userID string, ref pebblestore.ArtifactV3VideoReference) ([]byte, error) {
	if s == nil || s.artifacts == nil || s.storage == nil {
		return nil, errors.New("Artifact V3 video reference authority is unavailable")
	}
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("Artifact V3 video read requires authenticated user")
	}
	return s.read(ctx, accountScopeID, userID, ref)
}

// Read is retained for same-package callers and test fixtures. Production video
// consumers must use ReadVideoReference so user identity is explicit.
func (s *Service) Read(ctx context.Context, accountScopeID string, ref pebblestore.ArtifactV3VideoReference) ([]byte, error) {
	if s == nil {
		return nil, errors.New("Artifact V3 video reference authority is unavailable")
	}
	return s.read(ctx, accountScopeID, "", ref)
}

func (s *Service) read(ctx context.Context, accountScopeID, userID string, ref pebblestore.ArtifactV3VideoReference) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("Artifact V3 video read requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.artifacts == nil || s.storage == nil {
		return nil, errors.New("Artifact V3 video reference authority is unavailable")
	}
	if ref.DerivativeID == "" {
		return nil, errors.New("Artifact V3 video read requires derivative identity")
	}
	selection := Selection{AccountScopeID: accountScopeID, UserID: userID, SessionID: ref.SessionID, ArtifactID: ref.ArtifactID, RevisionID: ref.RevisionID, CommitOID: ref.CommitOID, TreeOID: ref.TreeOID, PartID: ref.PartID, CaptureStateID: ref.CaptureStateID, DurationMs: ref.DurationMs, FPS: ref.FPS}
	project, err := s.artifacts.ReadImmutableRevision(ctx, accountScopeID, selection)
	if err != nil {
		return nil, fmt.Errorf("authenticate Artifact V3 derivative reference: %w", err)
	}
	if project.AnimationProfile == "" {
		project.AnimationProfile = DefaultAnimationProfile
	}
	if err := validateReferenceAgainstProject(ref, project); err != nil {
		return nil, err
	}
	payload, err := s.storage.Read(ctx, ref.SessionID, ref.ArtifactID, ref)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if digestBytes(payload) != ref.DigestSHA256 {
		return nil, errors.New("Artifact V3 derivative digest mismatch")
	}
	return payload, nil
}

func validateSelection(selection Selection) error {
	for field, value := range map[string]string{"account scope": selection.AccountScopeID, "user": selection.UserID, "session": selection.SessionID, "artifact": selection.ArtifactID, "revision": selection.RevisionID, "commit": selection.CommitOID, "tree": selection.TreeOID} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("Artifact V3 %s identity is required", field)
		}
	}
	if !validGitOID(selection.CommitOID) || !validGitOID(selection.TreeOID) {
		return errors.New("Artifact V3 commit and tree identities must be Git SHA-1 object IDs")
	}
	_, _, err := normalizedTiming(selection.DurationMs, selection.FPS)
	return err
}

func normalizedTiming(duration int64, fps float64) (int64, float64, error) {
	if duration == 0 {
		duration = DefaultDurationMs
	}
	if fps == 0 {
		fps = DefaultFPS
	}
	if duration <= 0 || duration > maxDurationMs || fps <= 0 || fps > 60 {
		return 0, 0, errors.New("Artifact V3 video duration/fps are outside supported bounds")
	}
	return duration, fps, nil
}

func validateProject(selection Selection, project Project) error {
	if project.SessionID != selection.SessionID || project.ArtifactID != selection.ArtifactID || project.RevisionID != selection.RevisionID || project.CommitOID != selection.CommitOID || project.TreeOID != selection.TreeOID {
		return errors.New("selected Artifact V3 head is stale or identity-mismatched")
	}
	if project.BuildID == "" || project.ValidationID == "" {
		return errors.New("selected Artifact V3 head requires successful build and validation identity")
	}
	if project.EventSeq == 0 || !validGitOID(project.CommitOID) || !validGitOID(project.TreeOID) || !validDigest(project.ManifestDigestSHA256) {
		return errors.New("selected Artifact V3 head has incomplete exact Git/manifest identity")
	}
	if project.MediaType != "text/html" || len(project.Files) == 0 {
		return errors.New("selected Artifact V3 head must be a complete HTML Git project")
	}
	return nil
}

func validateReferenceAgainstProject(ref pebblestore.ArtifactV3VideoReference, project Project) error {
	if ref.SessionID != project.SessionID || ref.ArtifactID != project.ArtifactID || ref.RevisionID != project.RevisionID || ref.CommitOID != project.CommitOID || ref.TreeOID != project.TreeOID || ref.ManifestDigestSHA256 != project.ManifestDigestSHA256 || ref.BuildID != project.BuildID || ref.ValidationID != project.ValidationID || ref.EventSeq == 0 {
		return errors.New("Artifact V3 video reference does not match authenticated source")
	}
	if ref.DerivativeID != "av3der_"+ref.DigestSHA256 || !validDigest(ref.DigestSHA256) || ref.AnimationProfile != project.AnimationProfile || (ref.MediaType != "image/png" && ref.MediaType != "video/mp4") {
		return errors.New("Artifact V3 video reference is incomplete")
	}
	return nil
}

func assemble(project Project, selection Selection, duration int64, fps float64, fallback, mp4 Derivative) Conversion {
	profile := project.AnimationProfile
	if profile == "" {
		profile = DefaultAnimationProfile
	}
	base := pebblestore.ArtifactV3VideoReference{SessionID: project.SessionID, ArtifactID: project.ArtifactID, RevisionID: project.RevisionID, CommitOID: project.CommitOID, TreeOID: project.TreeOID, ManifestDigestSHA256: project.ManifestDigestSHA256, BuildID: project.BuildID, ValidationID: project.ValidationID, PartID: selection.PartID, CaptureStateID: selection.CaptureStateID, EventSeq: project.EventSeq, DigestSHA256: project.ManifestDigestSHA256, MediaType: "text/html", DurationMs: duration, FPS: fps, AnimationProfile: profile}
	fallbackRef := base
	fallbackRef.DerivativeID, fallbackRef.DigestSHA256, fallbackRef.MediaType = fallback.ID, fallback.DigestSHA256, fallback.MediaType
	mp4Ref := base
	mp4Ref.DerivativeID, mp4Ref.DigestSHA256, mp4Ref.MediaType = mp4.ID, mp4.DigestSHA256, mp4.MediaType
	candidateID := "artifact-v3-" + project.RevisionID
	partID := selection.PartID
	if partID == "" {
		partID = "artifact-v3"
	}
	part := pebblestore.VideoPlanPart{ID: partID, Title: "Artifact V3 animation", DurationMs: duration, CaptureStateID: selection.CaptureStateID, FilmingRequirements: []string{"Preserve the authenticated Artifact V3 project and deterministic animation timing."}, ProductionState: pebblestore.VideoProductionStateReady, ArtifactV3Source: &base, ArtifactV3Still: &fallbackRef, ArtifactV3Visual: &mp4Ref, VisualMediaType: "video/mp4", SourceStartMs: 0, SourceEndMs: duration, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{Candidates: []pebblestore.VideoAnimationCandidate{{ID: candidateID, V3Source: &base, Label: "Selected Artifact V3 head"}}, SelectedCandidateID: candidateID, V3SelectedSource: &base, V3Derivative: &mp4Ref, Status: pebblestore.VideoAnimationCandidateStatusReady}}
	return Conversion{Source: base, Fallback: fallbackRef, MP4: mp4Ref, Plan: pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Summary: "Native Artifact V3 animation conversion", Parts: []pebblestore.VideoPlanPart{part}}}
}

func validateRenderedDerivatives(fallback, mp4 []byte) error {
	pngSignature := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if len(fallback) <= len(pngSignature) || !bytes.Equal(fallback[:len(pngSignature)], pngSignature) {
		return errors.New("trusted Artifact V3 render returned an invalid PNG fallback")
	}
	// ISO BMFF requires a complete leading box header. Trusted animation output
	// starts with the ftyp box; reject arbitrary non-empty bytes before they can
	// become durable render authority.
	if len(mp4) < 12 || !bytes.Equal(mp4[4:8], []byte("ftyp")) {
		return errors.New("trusted Artifact V3 render returned an invalid MP4 container")
	}
	return nil
}

func derivative(mediaType string, payload []byte) Derivative {
	digest := digestBytes(payload)
	return Derivative{ID: "av3der_" + digest, MediaType: mediaType, DigestSHA256: digest, Bytes: append([]byte(nil), payload...)}
}

func digestBytes(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
func validGitOID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 20
}
func cloneProject(project Project) Project { project.Files = cloneFiles(project.Files); return project }
func cloneFiles(files map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for name, content := range files {
		out[name] = append([]byte(nil), content...)
	}
	return out
}
