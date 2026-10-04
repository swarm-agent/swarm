package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Only exact local artifact references are supported non-inline sources. Never
// fetch URLs, resolve file paths, or treat a lookalike remote URL as local authority.
// GetReference revalidates ownership/revision even for warm derivative reads.
func (s *Server) projectPreviewSource(p identity.Principal, value string) (*artifact.Authority, artifact.Principal, pebblestore.SessionArtifactSelectionReference, error) {
	var ref pebblestore.SessionArtifactSelectionReference
	principal := artifact.Principal{AccountScopeID: p.AccountScopeID, UserID: p.UserID}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(value, "/v3/sessions/") {
		return nil, principal, ref, errors.New("unsupported preview source")
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 6 || parts[1] != "v3" || parts[2] != "sessions" || parts[4] != "artifacts" || parts[3] == "" || parts[5] == "" || parts[3] == ".." || parts[5] == ".." {
		return nil, principal, ref, errors.New("unsupported preview source")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 {
		return nil, principal, ref, errors.New("preview requires one exact revision")
	}
	revision := ""
	for key, values := range q {
		if (key != "revision" && key != "event_seq" && key != "rev" && key != "eventseq") || len(values) != 1 {
			return nil, principal, ref, errors.New("invalid preview revision")
		}
		revision = values[0]
	}
	seq, err := strconv.ParseUint(revision, 10, 64)
	if err != nil || seq <= 0 || s.artifacts == nil || s.sessions == nil {
		return nil, principal, ref, errors.New("preview authority unavailable")
	}
	variant, found, err := s.sessions.Store().GetSessionArtifactVariantByID(p.AccountScopeID, parts[3], parts[5])
	if err != nil || !found || variant.EventSeq != seq {
		return nil, principal, ref, errors.New("preview artifact unavailable")
	}
	ref = pebblestore.SessionArtifactSelectionReference{SessionID: parts[3], CollectionID: variant.CollectionID, VariantID: parts[5], EventSeq: seq}
	principal.SessionID = parts[3]
	authority := artifact.NewAuthority(s.artifacts, s.sessions)
	current, err := authority.GetReference(principal, ref)
	if err != nil {
		return nil, principal, ref, err
	}
	switch current.MediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "video/mp4", "video/webm":
	default:
		return nil, principal, ref, errors.New("unsupported preview media")
	}
	return authority, principal, ref, nil
}

func (s *Server) readProjectPreviewSource(ctx context.Context, p identity.Principal, value string) (string, error) {
	authority, principal, ref, err := s.projectPreviewSource(p, value)
	if err != nil {
		return "", err
	}
	raw, variant, err := authority.ReadReference(ctx, principal, ref, 24<<20)
	if err != nil {
		return "", err
	}
	return "data:" + variant.MediaType + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}
