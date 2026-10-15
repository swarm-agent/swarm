package artifact

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func testVariant(id, filename, mediaType, kind string) pebblestore.SessionArtifactVariant {
	return pebblestore.SessionArtifactVariant{ID: id, CollectionID: "collection-1", AccountScopeID: "account-1", SessionID: "session-1", Filename: filename, MediaType: mediaType, Presentation: pebblestore.SessionArtifactPresentation{Kind: kind}}
}
