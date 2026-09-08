package artifactv2

import (
	"context"
	"errors"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) BuildVideoReference(principal Principal, ref ReadyReference) (pebblestore.ArtifactV2VideoReference, error) {
	principal, working, err := s.load(principal, ref.ArtifactID)
	if err != nil {
		return pebblestore.ArtifactV2VideoReference{}, err
	}
	resolved, err := s.ResolveReady(principal, ref.ArtifactID, ref.PublishedHeadID)
	if err != nil || resolved != ref {
		return pebblestore.ArtifactV2VideoReference{}, errors.New("artifact v2 video source head is stale or mismatched")
	}
	build, ok, err := s.store.GetArtifactV2Build(principal.AccountScopeID, working.ID, ref.BuildID)
	if err != nil || !ok || build.Output == nil || build.Status != pebblestore.ArtifactV2BuildSucceeded {
		return pebblestore.ArtifactV2VideoReference{}, errors.New("artifact v2 video source build is unavailable")
	}
	return pebblestore.ArtifactV2VideoReference{SessionID: working.SessionID, ArtifactID: working.ID, CompositionID: ref.CompositionID, BuildID: ref.BuildID, ValidationID: ref.ValidationID, EventSeq: ref.EventSeq, CompositionDigest: ref.DigestSHA256, DigestSHA256: build.Output.DigestSHA256, PolicyRevision: ref.PolicyRevision, MediaType: build.Output.MediaType, DurationMs: int64(build.DurationMS), AnimationProfile: animationProfileFromPolicy(ref.PolicyRevision)}, nil
}

func (s *Service) DerivativeVideoReference(principal Principal, artifactID, derivativeID string) (pebblestore.ArtifactV2VideoReference, error) {
	principal, working, err := s.load(principal, artifactID)
	if err != nil {
		return pebblestore.ArtifactV2VideoReference{}, err
	}
	derivative, ok, err := s.store.GetArtifactV2Derivative(principal.AccountScopeID, working.ID, strings.TrimSpace(derivativeID))
	if err != nil || !ok || derivative.Status != "ready" || derivative.Output == nil {
		return pebblestore.ArtifactV2VideoReference{}, errors.New("artifact v2 video derivative is unavailable")
	}
	build, ok, err := s.store.GetArtifactV2Build(principal.AccountScopeID, working.ID, derivative.BuildID)
	if err != nil || !ok {
		return pebblestore.ArtifactV2VideoReference{}, errors.New("artifact v2 video derivative build is unavailable")
	}
	return pebblestore.ArtifactV2VideoReference{SessionID: working.SessionID, ArtifactID: working.ID, CompositionID: derivative.CompositionID, BuildID: derivative.BuildID, ValidationID: derivative.ValidationID, DerivativeID: derivative.ID, PartID: derivative.SourcePartID, PartRevisionID: derivative.SourcePartRevisionID, CaptureStateID: derivative.CaptureStateID, EventSeq: derivative.EventSeq, CompositionDigest: derivative.CompositionDigest, DigestSHA256: derivative.Output.DigestSHA256, PolicyRevision: derivative.PolicyRevision, MediaType: derivative.Output.MediaType, DurationMs: int64(build.DurationMS), AnimationProfile: animationProfileFromPolicy(derivative.PolicyRevision)}, nil
}

func (s *Service) ValidateVideoReference(accountScopeID, userID string, ref pebblestore.ArtifactV2VideoReference) error {
	if strings.TrimSpace(accountScopeID) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(ref.SessionID) == "" || strings.TrimSpace(ref.ArtifactID) == "" || strings.TrimSpace(ref.CompositionID) == "" || strings.TrimSpace(ref.BuildID) == "" || strings.TrimSpace(ref.ValidationID) == "" || ref.EventSeq == 0 || strings.TrimSpace(ref.CompositionDigest) == "" || strings.TrimSpace(ref.DigestSHA256) == "" || strings.TrimSpace(ref.PolicyRevision) == "" || strings.TrimSpace(ref.MediaType) == "" {
		return errors.New("artifact v2 video reference is incomplete")
	}
	working, ok, err := s.store.GetArtifactV2Working(accountScopeID, ref.ArtifactID)
	if err != nil || !ok || working.UserID != userID || working.SessionID != ref.SessionID || working.PolicyRevision != ref.PolicyRevision {
		return errors.New("artifact v2 video reference ownership or policy does not match")
	}
	build, ok, err := s.store.GetArtifactV2Build(accountScopeID, ref.ArtifactID, ref.BuildID)
	if err != nil || !ok || build.CompositionID != ref.CompositionID || build.CompositionDigest != ref.CompositionDigest || build.PolicyRevision != ref.PolicyRevision {
		return errors.New("artifact v2 video reference build does not match")
	}
	validation, ok, err := s.store.GetArtifactV2Validation(accountScopeID, ref.ArtifactID, ref.ValidationID)
	if err != nil || !ok || validation.Status != pebblestore.ArtifactV2ValidationValid || validation.BuildID != build.ID || validation.CompositionDigest != build.CompositionDigest {
		return errors.New("artifact v2 video reference validation does not match")
	}
	if ref.DerivativeID == "" {
		if build.Output == nil || build.Output.DigestSHA256 != ref.DigestSHA256 || build.Output.MediaType != ref.MediaType {
			return errors.New("artifact v2 video build output does not match")
		}
		return nil
	}
	derivative, ok, err := s.store.GetArtifactV2Derivative(accountScopeID, ref.ArtifactID, ref.DerivativeID)
	if err != nil || !ok || derivative.Status != "ready" || derivative.Output == nil || derivative.CompositionID != ref.CompositionID || derivative.BuildID != ref.BuildID || derivative.ValidationID != ref.ValidationID || derivative.EventSeq != ref.EventSeq || derivative.Output.DigestSHA256 != ref.DigestSHA256 || derivative.Output.MediaType != ref.MediaType || derivative.SourcePartID != ref.PartID || derivative.SourcePartRevisionID != ref.PartRevisionID || derivative.CaptureStateID != ref.CaptureStateID {
		return errors.New("artifact v2 video derivative reference does not match")
	}
	return nil
}

func (s *Service) ReadVideoReference(ctx context.Context, accountScopeID, userID string, ref pebblestore.ArtifactV2VideoReference) ([]byte, error) {
	if err := s.ValidateVideoReference(accountScopeID, userID, ref); err != nil {
		return nil, err
	}
	principal := Principal{AccountScopeID: accountScopeID, UserID: userID, SessionID: ref.SessionID}
	if ref.DerivativeID != "" {
		derivative, _, _ := s.store.GetArtifactV2Derivative(accountScopeID, ref.ArtifactID, ref.DerivativeID)
		return s.blobs.GetExact(ctx, principal, *derivative.Output)
	}
	build, _, _ := s.store.GetArtifactV2Build(accountScopeID, ref.ArtifactID, ref.BuildID)
	return s.blobs.GetExact(ctx, principal, *build.Output)
}

func animationProfileFromPolicy(policy string) string {
	for _, profile := range []string{"motion_ui", "spatial_3d", "vector_playback", "final_render"} {
		if strings.Contains(policy, profile) {
			return profile
		}
	}
	return ""
}
