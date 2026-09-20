package pebblestore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
)

const (
	ArtifactV3CatalogDefaultLimit  = 50
	ArtifactV3CatalogMaxLimit      = 100
	artifactV3CatalogCursorVersion = 1
)

// ArtifactV3CatalogOptions specifies bounded search filters for native Artifact V3 library discovery.
type ArtifactV3CatalogOptions struct {
	Query         string `json:"query,omitempty"`
	Status        string `json:"status,omitempty"`      // "ready", "selected"
	MediaType     string `json:"media_type,omitempty"`  // "text/html"
	SourceKind    string `json:"source_kind,omitempty"` // "head", "candidate", "historical_revision"
	SessionID     string `json:"session_id,omitempty"`
	ArtifactID    string `json:"artifact_id,omitempty"`
	CreatedAfter  int64  `json:"created_after,omitempty"`
	CreatedBefore int64  `json:"created_before,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Cursor        string `json:"cursor,omitempty"`
}

// ArtifactV3CatalogItem describes one discoverable native version (head, historical revision, or ready candidate).
type ArtifactV3CatalogItem struct {
	ArtifactID  string                            `json:"artifact_id"`
	SessionID   string                            `json:"session_id"`
	CommitOID   string                            `json:"commit_oid"`
	RevisionRef string                            `json:"revision_ref"`
	TurnID      string                            `json:"turn_id,omitempty"`
	CandidateID string                            `json:"candidate_id,omitempty"`
	SourceKind  string                            `json:"source_kind"` // "head", "candidate", "historical_revision"
	Status      string                            `json:"status"`      // "ready", "selected"
	Intent      string                            `json:"intent,omitempty"`
	Entrypoint  string                            `json:"entrypoint,omitempty"`
	MediaType   string                            `json:"media_type,omitempty"`
	Parts       []ArtifactV3PartProjection        `json:"parts,omitempty"`
	FileCount   int                               `json:"file_count"`
	TreeBytes   int64                             `json:"tree_bytes"`
	CreatedAt   int64                             `json:"created_at"`
	Build       ArtifactV3EvidenceProjection      `json:"build"`
	Preview     ArtifactV3EvidenceProjection      `json:"preview"`
	Reference   SessionArtifactSelectionReference `json:"reference"`
	Lineage     *ArtifactV3Lineage                `json:"lineage,omitempty"`
}

// ArtifactV3CatalogPage is a paginated collection of native Artifact V3 items.
type ArtifactV3CatalogPage struct {
	Items      []ArtifactV3CatalogItem `json:"items"`
	NextCursor string                  `json:"next_cursor,omitempty"`
	HasMore    bool                    `json:"has_more"`
}

type artifactV3CatalogCursor struct {
	Version        int    `json:"v"`
	SnapshotAt     int64  `json:"s"`
	LastCreatedAt  int64  `json:"t"`
	LastSessionID  string `json:"sid"`
	LastArtifactID string `json:"aid"`
	LastCommitOID  string `json:"cid"`
	LastCandidate  string `json:"can"`
	Filter         string `json:"f"`
}

// SearchArtifactV3Catalog traverses native artifacts owned by the authenticated account and user
// across retained sessions. It includes selected heads, historical ready revisions, and ready
// unselected turn/swarm iteration candidates.
func (s *SessionStore) SearchArtifactV3Catalog(accountScopeID, userID string, options ArtifactV3CatalogOptions) (ArtifactV3CatalogPage, error) {
	if s == nil || s.store == nil {
		return ArtifactV3CatalogPage{}, errors.New("session store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	userID = strings.TrimSpace(userID)
	if accountScopeID == "" || userID == "" {
		return ArtifactV3CatalogPage{}, errors.New("artifact v3 catalog account and user ownership are required")
	}

	options.Query = strings.ToLower(strings.TrimSpace(options.Query))
	options.Status = strings.ToLower(strings.TrimSpace(options.Status))
	options.SourceKind = strings.ToLower(strings.TrimSpace(options.SourceKind))
	options.MediaType = canonicalArtifactCatalogMediaType(options.MediaType)
	options.SessionID = strings.TrimSpace(options.SessionID)
	options.ArtifactID = strings.TrimSpace(options.ArtifactID)

	if options.CreatedAfter < 0 || options.CreatedBefore < 0 || (options.CreatedAfter != 0 && options.CreatedBefore != 0 && options.CreatedAfter > options.CreatedBefore) {
		return ArtifactV3CatalogPage{}, errors.New("artifact v3 catalog date bounds are invalid")
	}
	if options.Status != "" && options.Status != "ready" && options.Status != "selected" {
		return ArtifactV3CatalogPage{}, errors.New("artifact v3 catalog status is invalid")
	}
	if options.SourceKind != "" && options.SourceKind != "head" && options.SourceKind != "candidate" && options.SourceKind != "historical_revision" {
		return ArtifactV3CatalogPage{}, errors.New("artifact v3 catalog source_kind is invalid")
	}
	if options.Limit <= 0 {
		options.Limit = ArtifactV3CatalogDefaultLimit
	}
	if options.Limit > ArtifactV3CatalogMaxLimit {
		options.Limit = ArtifactV3CatalogMaxLimit
	}

	filter := artifactV3CatalogFilterIdentity(options)
	cursor, err := decodeArtifactV3CatalogCursor(options.Cursor, filter)
	if err != nil {
		return ArtifactV3CatalogPage{}, err
	}

	// Gather all sessions owned by accountScopeID and userID.
	const iterateAll = int(^uint(0) >> 1)
	ownedSessions := make(map[string]bool)
	if err := s.store.IteratePrefix(SessionByAccountPrefix(accountScopeID), iterateAll, func(_ string, value []byte) error {
		sessionID := strings.TrimSpace(string(value))
		if sessionID == "" {
			return nil
		}
		session, ok, err := s.GetSession(sessionID)
		if err != nil {
			return err
		}
		if !ok || strings.TrimSpace(session.AccountScopeID) != accountScopeID || strings.TrimSpace(session.UserID) != userID {
			return nil
		}
		ownedSessions[session.ID] = true
		return nil
	}); err != nil {
		return ArtifactV3CatalogPage{}, err
	}

	if options.SessionID != "" && !ownedSessions[options.SessionID] {
		return ArtifactV3CatalogPage{}, nil
	}

	prefix := KeyArtifactV3RepositoryPrefix(accountScopeID)
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return ArtifactV3CatalogPage{}, err
	}
	defer iter.Close()

	items := make([]ArtifactV3CatalogItem, 0)
	for iter.First(); iter.Valid(); iter.Next() {
		var repo ArtifactV3RepositoryProjection
		if err := json.Unmarshal(iter.Value(), &repo); err != nil {
			return ArtifactV3CatalogPage{}, err
		}
		if repo.AccountScopeID != accountScopeID || repo.UserID != userID || !ownedSessions[repo.OwnerSessionID] {
			continue
		}
		if options.SessionID != "" && repo.OwnerSessionID != options.SessionID {
			continue
		}
		if options.ArtifactID != "" && repo.ArtifactID != options.ArtifactID {
			continue
		}

		// Recheck session row immediately
		owned, ok, err := s.GetSession(repo.OwnerSessionID)
		if err != nil {
			return ArtifactV3CatalogPage{}, err
		}
		if !ok || strings.TrimSpace(owned.AccountScopeID) != accountScopeID || strings.TrimSpace(owned.UserID) != userID {
			continue
		}

		// 1. Selected Head revision
		if repo.HeadCommitOID != "" {
			rev, ok, err := s.GetArtifactV3Revision(accountScopeID, userID, repo.ArtifactID, repo.HeadCommitOID)
			if err == nil && ok && rev.OwnerSessionID == repo.OwnerSessionID &&
				artifactV3EvidenceReady(rev.Build, repo.HeadCommitOID) && artifactV3EvidenceReady(rev.Preview, repo.HeadCommitOID) {
				headItem := ArtifactV3CatalogItem{
					ArtifactID:  repo.ArtifactID,
					SessionID:   repo.OwnerSessionID,
					CommitOID:   repo.HeadCommitOID,
					RevisionRef: "revision-" + repo.HeadCommitOID,
					SourceKind:  "head",
					Status:      "selected",
					Intent:      repo.IntentReference,
					Entrypoint:  "index.html",
					MediaType:   "text/html",
					Parts:       rev.Parts,
					FileCount:   rev.FileCount,
					TreeBytes:   rev.TreeBytes,
					CreatedAt:   rev.CreatedAt,
					Build:       rev.Build,
					Preview:     rev.Preview,
					Lineage:     repo.Lineage,
					Reference: SessionArtifactSelectionReference{
						SessionID:     repo.OwnerSessionID,
						ArtifactID:    repo.ArtifactID,
						RevisionRef:   "revision-" + repo.HeadCommitOID,
						CommitOID:     repo.HeadCommitOID,
						Action:        "use",
						ProjectionSeq: repo.EventSeq,
					},
				}
				if artifactV3CatalogItemMatches(headItem, options) {
					items = append(items, headItem)
				}
			}
		}

		// 2. Candidates (including ready unselected turn/swarm candidates)
		candidates, err := s.ListArtifactV3CandidateProjections(accountScopeID, userID, repo.ArtifactID)
		if err == nil {
			for _, cand := range candidates {
				if cand.CommitOID == "" || (cand.Status != "ready" && cand.Status != "selected") {
					continue
				}
				if !artifactV3EvidenceReady(cand.Build, cand.CommitOID) || !artifactV3EvidenceReady(cand.Preview, cand.CommitOID) {
					continue
				}
				// If this candidate is the head and caller did not specifically ask for candidates,
				// it is already represented by the head item.
				if cand.CommitOID == repo.HeadCommitOID && options.SourceKind != "candidate" {
					continue
				}
				candItem := ArtifactV3CatalogItem{
					ArtifactID:  repo.ArtifactID,
					SessionID:   repo.OwnerSessionID,
					CommitOID:   cand.CommitOID,
					RevisionRef: "revision-" + cand.CommitOID,
					TurnID:      cand.TurnID,
					CandidateID: cand.CandidateID,
					SourceKind:  "candidate",
					Status:      cand.Status,
					Intent:      repo.IntentReference,
					Entrypoint:  "index.html",
					MediaType:   "text/html",
					CreatedAt:   cand.CreatedAt,
					Build:       cand.Build,
					Preview:     cand.Preview,
					Lineage:     repo.Lineage,
					Reference: SessionArtifactSelectionReference{
						SessionID:     repo.OwnerSessionID,
						ArtifactID:    repo.ArtifactID,
						RevisionRef:   "revision-" + cand.CommitOID,
						CommitOID:     cand.CommitOID,
						Action:        "use",
						ProjectionSeq: cand.EventSeq,
					},
				}
				if rev, ok, _ := s.GetArtifactV3Revision(accountScopeID, userID, repo.ArtifactID, cand.CommitOID); ok {
					candItem.Parts = rev.Parts
					candItem.FileCount = rev.FileCount
					candItem.TreeBytes = rev.TreeBytes
				}
				if artifactV3CatalogItemMatches(candItem, options) {
					items = append(items, candItem)
				}
			}
		}

		// 3. Historical ready revisions
		revisions, err := s.ListArtifactV3RevisionProjections(accountScopeID, userID, repo.ArtifactID)
		if err == nil {
			for _, rev := range revisions {
				if rev.CommitOID == repo.HeadCommitOID {
					continue
				}
				isCandidate := false
				for _, c := range candidates {
					if c.CommitOID == rev.CommitOID && (c.Status == "ready" || c.Status == "selected") {
						isCandidate = true
						break
					}
				}
				if isCandidate {
					continue
				}
				if !artifactV3EvidenceReady(rev.Build, rev.CommitOID) || !artifactV3EvidenceReady(rev.Preview, rev.CommitOID) {
					continue
				}
				revItem := ArtifactV3CatalogItem{
					ArtifactID:  repo.ArtifactID,
					SessionID:   repo.OwnerSessionID,
					CommitOID:   rev.CommitOID,
					RevisionRef: "revision-" + rev.CommitOID,
					SourceKind:  "historical_revision",
					Status:      "ready",
					Intent:      repo.IntentReference,
					Entrypoint:  "index.html",
					MediaType:   "text/html",
					Parts:       rev.Parts,
					FileCount:   rev.FileCount,
					TreeBytes:   rev.TreeBytes,
					CreatedAt:   rev.CreatedAt,
					Build:       rev.Build,
					Preview:     rev.Preview,
					Lineage:     rev.Lineage,
					Reference: SessionArtifactSelectionReference{
						SessionID:     repo.OwnerSessionID,
						ArtifactID:    repo.ArtifactID,
						RevisionRef:   "revision-" + rev.CommitOID,
						CommitOID:     rev.CommitOID,
						Action:        "use",
						ProjectionSeq: rev.EventSeq,
					},
				}
				if artifactV3CatalogItemMatches(revItem, options) {
					items = append(items, revItem)
				}
			}
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return artifactV3CatalogItemBefore(items[i], items[j])
	})

	snapshotAt := cursor.SnapshotAt
	if snapshotAt == 0 && len(items) > 0 {
		snapshotAt = items[0].CreatedAt
	}

	visible := make([]ArtifactV3CatalogItem, 0, len(items))
	snapshotBound := cursor.LastSessionID != "" || len(items) > 0
	for _, item := range items {
		if snapshotBound && item.CreatedAt > snapshotAt {
			continue
		}
		if cursor.LastSessionID != "" && !artifactV3CatalogItemAfterCursor(item, cursor) {
			continue
		}
		visible = append(visible, item)
	}

	page := ArtifactV3CatalogPage{}
	if len(visible) > options.Limit {
		page.HasMore = true
		visible = visible[:options.Limit]
	}
	page.Items = visible
	if page.HasMore && len(visible) > 0 {
		last := visible[len(visible)-1]
		page.NextCursor, err = encodeArtifactV3CatalogCursor(artifactV3CatalogCursor{
			Version:        artifactV3CatalogCursorVersion,
			SnapshotAt:     snapshotAt,
			LastCreatedAt:  last.CreatedAt,
			LastSessionID:  last.SessionID,
			LastArtifactID: last.ArtifactID,
			LastCommitOID:  last.CommitOID,
			LastCandidate:  last.CandidateID,
			Filter:         filter,
		})
		if err != nil {
			return ArtifactV3CatalogPage{}, err
		}
	}

	return page, nil
}

func artifactV3CatalogItemMatches(item ArtifactV3CatalogItem, options ArtifactV3CatalogOptions) bool {
	if options.Status != "" && item.Status != options.Status {
		return false
	}
	if options.SourceKind != "" && item.SourceKind != options.SourceKind {
		return false
	}
	if options.MediaType != "" && canonicalArtifactCatalogMediaType(item.MediaType) != options.MediaType {
		return false
	}
	if options.SessionID != "" && item.SessionID != options.SessionID {
		return false
	}
	if options.ArtifactID != "" && item.ArtifactID != options.ArtifactID {
		return false
	}
	if options.CreatedAfter != 0 && item.CreatedAt < options.CreatedAfter {
		return false
	}
	if options.CreatedBefore != 0 && item.CreatedAt > options.CreatedBefore {
		return false
	}
	if options.Query == "" {
		return true
	}
	fields := []string{
		item.ArtifactID, item.Intent, item.Entrypoint, item.CommitOID, item.RevisionRef,
		item.TurnID, item.CandidateID, item.SourceKind,
	}
	for _, p := range item.Parts {
		fields = append(fields, p.ID, p.Label)
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), options.Query) {
			return true
		}
	}
	return false
}

func artifactV3CatalogItemBefore(left, right ArtifactV3CatalogItem) bool {
	if left.CreatedAt != right.CreatedAt {
		return left.CreatedAt > right.CreatedAt
	}
	if left.SessionID != right.SessionID {
		return left.SessionID < right.SessionID
	}
	if left.ArtifactID != right.ArtifactID {
		return left.ArtifactID < right.ArtifactID
	}
	if left.CommitOID != right.CommitOID {
		return left.CommitOID < right.CommitOID
	}
	return left.CandidateID < right.CandidateID
}

func artifactV3CatalogItemAfterCursor(item ArtifactV3CatalogItem, cursor artifactV3CatalogCursor) bool {
	if item.CreatedAt != cursor.LastCreatedAt {
		return item.CreatedAt < cursor.LastCreatedAt
	}
	if item.SessionID != cursor.LastSessionID {
		return item.SessionID > cursor.LastSessionID
	}
	if item.ArtifactID != cursor.LastArtifactID {
		return item.ArtifactID > cursor.LastArtifactID
	}
	if item.CommitOID != cursor.LastCommitOID {
		return item.CommitOID > cursor.LastCommitOID
	}
	return item.CandidateID > cursor.LastCandidate
}

func artifactV3CatalogFilterIdentity(options ArtifactV3CatalogOptions) string {
	payload, _ := json.Marshal([]any{
		options.Query, options.Status, options.MediaType, options.SourceKind,
		options.SessionID, options.ArtifactID, options.CreatedAfter, options.CreatedBefore,
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func encodeArtifactV3CatalogCursor(cursor artifactV3CatalogCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeArtifactV3CatalogCursor(raw, filter string) (artifactV3CatalogCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return artifactV3CatalogCursor{Version: artifactV3CatalogCursorVersion, Filter: filter}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return artifactV3CatalogCursor{}, errors.New("artifact v3 catalog cursor is invalid")
	}
	var cursor artifactV3CatalogCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != artifactV3CatalogCursorVersion ||
		cursor.SnapshotAt < 0 || cursor.LastCreatedAt < 0 || cursor.LastSessionID == "" || cursor.LastArtifactID == "" || cursor.Filter != filter {
		return artifactV3CatalogCursor{}, errors.New("artifact v3 catalog cursor is invalid or does not match the filters")
	}
	return cursor, nil
}
