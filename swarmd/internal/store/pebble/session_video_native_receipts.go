package pebblestore

import (
	"encoding/json"
	"errors"
)

// VisitNativeVideoDerivativeReceipts enumerates immutable stored revisions for
// the startup receipt migration, never request-supplied references. Bound the
// scan explicitly and fail rather than silently migrate only part of the corpus.
func (s *SessionStore) VisitNativeVideoDerivativeReceipts(visit func(account, user string, ref ArtifactV3VideoReference) error) error {
	const maxRevisions = 10000
	count := 0
	return s.store.IteratePrefix("v3/video_project/revision/", maxRevisions+1, func(_ string, body []byte) error {
		count++
		if count > maxRevisions {
			return errors.New("native video receipt migration revision bound exceeded")
		}
		var revision VideoProjectRevisionSnapshot
		if err := json.Unmarshal(body, &revision); err != nil {
			return err
		}
		refs := []*ArtifactV3VideoReference{}
		for _, clip := range revision.Timeline.Clips {
			if clip.ArtifactV3Ref != nil {
				refs = append(refs, clip.ArtifactV3Ref)
			}
		}
		plan, err := acceptedVideoPlanFromTimeline(revision.Timeline)
		if err != nil {
			return err
		}
		if plan != nil {
			for _, part := range plan.Parts {
				refs = append(refs, part.ArtifactV3Still, part.ArtifactV3Visual)
			}
		}
		for _, ref := range refs {
			if ref != nil && ref.DerivativeID != "" {
				if err := visit(revision.AccountScopeID, revision.UserID, *ref); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
