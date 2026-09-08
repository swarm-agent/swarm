package videoproject

import (
	"context"
	"errors"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ReadArtifactV3Media uses the native immutable authority. The route session is
// the source owner session, never an alias for a collection or video project.
func (s *Service) ReadArtifactV3Media(ctx context.Context, principal identity.Principal, sessionID string, ref pebblestore.ArtifactV3VideoReference) ([]byte, error) {
	if !principal.Valid() || ref.SessionID != sessionID {
		return nil, errors.New("Artifact V3 media owner session mismatch")
	}
	reader, ok := s.artifactV3.(interface {
		ReadVideoReference(context.Context, string, string, pebblestore.ArtifactV3VideoReference) ([]byte, error)
	})
	if !ok {
		return nil, errors.New("Artifact V3 media reader unavailable")
	}
	return reader.ReadVideoReference(ctx, principal.AccountScopeID, principal.UserID, ref)
}
