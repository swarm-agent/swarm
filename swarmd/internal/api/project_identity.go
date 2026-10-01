package api

import (
	"context"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// resolveProjectUploadBytes preserves the documented inline-text contract for
// documents. Encoded image/media inputs still use the existing strict resolver.
func (s *Server) resolveProjectUploadBytes(ctx context.Context, p identity.Principal, item pebblestore.ProjectTaskMediaRef) ([]byte, string, error) {
	if item.Data != "" && strings.TrimSpace(item.URL) == "" && !strings.HasPrefix(item.Data, "data:") &&
		(item.Kind == "doc" || strings.HasPrefix(item.MediaType, "text/")) {
		mediaType := item.MediaType
		if mediaType == "" {
			mediaType = "text/plain"
		}
		return []byte(item.Data), mediaType, nil
	}
	return s.resolveSourceMediaBytes(ctx, p, item, "")
}
