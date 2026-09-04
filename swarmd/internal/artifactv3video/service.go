// Package artifactv3video owns conversion of one authenticated Artifact V3 Git
// head into digest-bound Video Studio inputs. It deliberately has no Artifact V2
// dependency and never mutates source Git bytes.
package artifactv3video

import (
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
	Files                 map[string][]byte
}

// Selection authenticates one exact selected V3 head. Optional Part and capture
// state identity scope rendering without changing the selected Git revision.
type Selection struct {
	AccountScopeID string
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
}

// Renderer performs trusted preflight and deterministic fallback/MP4 rendering.
type Renderer interface {
	Preflight(context.Context, RenderRequest) error
	Render(context.Context, RenderRequest) (fallbackPNG, silentMP4 []byte, err error)
}

// DerivativeStore atomically publishes both derivatives. Implementations must
// leave no visible bytes when PutAtomic returns an error.
type DerivativeStore interface {
	PutAtomic(context.Context, string, string, []Derivative) error
	Read(context.Context, string, string, string) ([]byte, error)
}

// Derivative is one immutable private V3-owned output.
type Derivative struct {
	ID           string
	MediaType    string
	DigestSHA256 string
	Bytes       []byte
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
	duration, fps, err := normalizedTiming(selection.DurationMs, selection.FPS)
	if err != nil {
		return Conversion{}, err
	}
	request := RenderRequest{Project: cloneProject(project), PartID: selection.PartID, CaptureStateID: selection.CaptureStateID, DurationMs: duration, FPS: fps, AnimationAdapter: animationAdapterVersion}
	if err := s.renderer.Preflight(ctx, request); err != nil {
		return Conversion{}, fmt.Errorf("trusted Artifact V3 preflight failed: %w", err)
	}
	fallback, mp4, err := s.renderer.Render(ctx, request)
	if err != nil {
		return Conversion{}, fmt.Errorf("trusted Artifact V3 render failed: %w", err)
	}
	if len(fallback) == 0 || len(mp4) == 0 {
		return Conversion{}, errors.New("trusted Artifact V3 render returned empty derivative bytes")
	}
	fallbackDerivative := derivative("image/png", fallback)
	mp4Derivative := derivative("video/mp4", mp4)
	conversion := assemble(project, selection, duration, fps, fallbackDerivative, mp4Derivative)
	if err := pebblestore.ValidateArtifactV3ConversionPlan(conversion.Plan); err != nil {
		return Conversion{}, fmt.Errorf("assemble native Artifact V3 video plan: %w", err)
	}
	if err := s.storage.PutAtomic(ctx, project.SessionID, project.ArtifactID, []Derivative{fallbackDerivative, mp4Derivative}); err != nil {
		return Conversion{}, fmt.Errorf("persist Artifact V3 video derivatives atomically: %w", err)
	}
	return conversion, nil
}

// Read validates the exact reference and verifies the private bytes against it.
func (s *Service) Read(ctx context.Context, accountScopeID string, ref pebblestore.ArtifactV3VideoReference) ([]byte, error) {
	if ref.DerivativeID == "" {
		return nil, errors.New("Artifact V3 video read requires derivative identity")
	}
	selection := Selection{AccountScopeID: accountScopeID, SessionID: ref.SessionID, ArtifactID: ref.ArtifactID, RevisionID: ref.RevisionID, CommitOID: ref.CommitOID, TreeOID: ref.TreeOID, PartID: ref.PartID, CaptureStateID: ref.CaptureStateID, DurationMs: ref.DurationMs, FPS: ref.FPS}
	project, err := s.artifacts.ReadSelectedHead(ctx, accountScopeID, selection)
	if err != nil {
		return nil, fmt.Errorf("authenticate Artifact V3 derivative reference: %w", err)
	}
	if project.AnimationProfile == "" {
		project.AnimationProfile = DefaultAnimationProfile
	}
	if err := validateReferenceAgainstProject(ref, project); err != nil {
		return nil, err
	}
	payload, err := s.storage.Read(ctx, ref.SessionID, ref.ArtifactID, ref.DerivativeID)
	if err != nil {
		return nil, err
	}
	if digestBytes(payload) != ref.DigestSHA256 {
		return nil, errors.New("Artifact V3 derivative digest mismatch")
	}
	return payload, nil
}

func validateSelection(selection Selection) error {
	for field, value := range map[string]string{"account scope": selection.AccountScopeID, "session": selection.SessionID, "artifact": selection.ArtifactID, "revision": selection.RevisionID, "commit": selection.CommitOID, "tree": selection.TreeOID} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("Artifact V3 %s identity is required", field)
		}
	}
	if !validDigest(selection.CommitOID) || !validDigest(selection.TreeOID) {
		return errors.New("Artifact V3 commit and tree identities must be sha256 digests")
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
	if project.EventSeq == 0 || !validDigest(project.CommitOID) || !validDigest(project.TreeOID) || !validDigest(project.ManifestDigestSHA256) {
		return errors.New("selected Artifact V3 head has incomplete exact Git/manifest identity")
	}
	if project.MediaType != "text/html" || len(project.Files) == 0 {
		return errors.New("selected Artifact V3 head must be a complete HTML Git project")
	}
	return nil
}

func validateReferenceAgainstProject(ref pebblestore.ArtifactV3VideoReference, project Project) error {
	if ref.SessionID != project.SessionID || ref.ArtifactID != project.ArtifactID || ref.RevisionID != project.RevisionID || ref.CommitOID != project.CommitOID || ref.TreeOID != project.TreeOID || ref.ManifestDigestSHA256 != project.ManifestDigestSHA256 || ref.BuildID != project.BuildID || ref.ValidationID != project.ValidationID || ref.EventSeq != project.EventSeq {
		return errors.New("Artifact V3 video reference does not match authenticated source")
	}
	if ref.DerivativeID == "" || !validDigest(ref.DigestSHA256) || (ref.MediaType != "image/png" && ref.MediaType != "video/mp4") {
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

func derivative(mediaType string, payload []byte) Derivative {
	digest := digestBytes(payload)
	return Derivative{ID: "av3der_" + digest, MediaType: mediaType, DigestSHA256: digest, Bytes: append([]byte(nil), payload...)}
}

func digestBytes(payload []byte) string { sum := sha256.Sum256(payload); return hex.EncodeToString(sum[:]) }
func validDigest(value string) bool { decoded, err := hex.DecodeString(value); return err == nil && len(decoded) == sha256.Size }
func cloneProject(project Project) Project { project.Files = cloneFiles(project.Files); return project }
func cloneFiles(files map[string][]byte) map[string][]byte { out := make(map[string][]byte, len(files)); for name, content := range files { out[name] = append([]byte(nil), content...) }; return out }
