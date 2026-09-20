package pebblestore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/cockroachdb/pebble"
)

const (
	ArtifactV3CatalogDefaultLimit = 50
	ArtifactV3CatalogMaxLimit     = 100
	artifactV3CatalogScanBudget   = 256
	artifactV3CatalogByteBudget   = 8 << 20
)

// ArtifactV3CatalogOptions filters retained immutable versions. Pagination is in
// repository/record key order, not mutable selection order. A scan-budget page
// may be empty and still carry NextCursor; callers must continue that cursor.
type ArtifactV3CatalogOptions struct {
	Query         string `json:"query,omitempty"`
	Status        string `json:"status,omitempty"`
	MediaType     string `json:"media_type,omitempty"`
	SourceKind    string `json:"source_kind,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	ArtifactID    string `json:"artifact_id,omitempty"`
	CreatedAfter  int64  `json:"created_after,omitempty"`
	CreatedBefore int64  `json:"created_before,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Cursor        string `json:"cursor,omitempty"`
}

type ArtifactV3CatalogItem struct {
	ArtifactID  string                            `json:"artifact_id"`
	SessionID   string                            `json:"session_id"`
	CommitOID   string                            `json:"commit_oid"`
	RevisionRef string                            `json:"revision_ref"`
	TurnID      string                            `json:"turn_id,omitempty"`
	CandidateID string                            `json:"candidate_id,omitempty"`
	SourceKind  string                            `json:"source_kind"`
	Status      string                            `json:"status"`
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

type ArtifactV3CatalogPage struct {
	Items      []ArtifactV3CatalogItem `json:"items"`
	NextCursor string                  `json:"next_cursor,omitempty"`
	HasMore    bool                    `json:"has_more"`
}

type artifactV3CatalogCursor struct {
	Version    int    `json:"v"`
	Filter     string `json:"f"`
	Repository string `json:"r,omitempty"`
	Phase      int    `json:"p"`
	After      string `json:"a,omitempty"`
}

func (s *SessionStore) SearchArtifactV3Catalog(account, user string, options ArtifactV3CatalogOptions) (ArtifactV3CatalogPage, error) {
	return s.searchArtifactV3Catalog(context.Background(), account, user, options)
}

func (s *SessionStore) searchArtifactV3Catalog(ctx context.Context, account, user string, o ArtifactV3CatalogOptions) (ArtifactV3CatalogPage, error) {
	page := ArtifactV3CatalogPage{Items: []ArtifactV3CatalogItem{}}
	if s == nil || s.store == nil || strings.TrimSpace(account) == "" || strings.TrimSpace(user) == "" {
		return page, ErrArtifactV3Unauthorized
	}
	o.Query = strings.ToLower(strings.TrimSpace(o.Query))
	o.Status = strings.ToLower(strings.TrimSpace(o.Status))
	o.SourceKind = strings.ToLower(strings.TrimSpace(o.SourceKind))
	o.MediaType = canonicalArtifactCatalogMediaType(o.MediaType)
	o.SessionID, o.ArtifactID = strings.TrimSpace(o.SessionID), strings.TrimSpace(o.ArtifactID)
	if len(o.Query) > 1024 || len(o.Cursor) > 8192 || len(o.SessionID) > 256 || len(o.ArtifactID) > 256 || len(o.MediaType) > 256 || o.CreatedAfter < 0 || o.CreatedBefore < 0 || (o.CreatedBefore != 0 && o.CreatedAfter > o.CreatedBefore) {
		return page, ErrArtifactV3Invalid
	}
	if o.Status != "" && o.Status != "ready" && o.Status != "selected" {
		return page, ErrArtifactV3Invalid
	}
	if o.SourceKind != "" && o.SourceKind != "head" && o.SourceKind != "candidate" && o.SourceKind != "historical_revision" {
		return page, ErrArtifactV3Invalid
	}
	if o.Limit <= 0 {
		o.Limit = ArtifactV3CatalogDefaultLimit
	}
	if o.Limit > ArtifactV3CatalogMaxLimit {
		o.Limit = ArtifactV3CatalogMaxLimit
	}
	filterBytes, _ := json.Marshal([]any{account, user, o.Query, o.Status, o.SourceKind, o.MediaType, o.SessionID, o.ArtifactID, o.CreatedAfter, o.CreatedBefore})
	digest := sha256.Sum256(filterBytes)
	c := artifactV3CatalogCursor{Version: 1, Filter: hex.EncodeToString(digest[:])}
	if o.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(o.Cursor)
		if err != nil {
			return page, ErrArtifactV3Invalid
		}
		var decoded artifactV3CatalogCursor
		if json.Unmarshal(raw, &decoded) != nil || decoded.Version != 1 || decoded.Filter != c.Filter || decoded.Phase < 0 || decoded.Phase > 3 || decoded.Repository == "" {
			return page, ErrArtifactV3Invalid
		}
		c = decoded
	}
	prefix := KeyArtifactV3RepositoryPrefix(account)
	if c.Repository != "" && !strings.HasPrefix(c.Repository, prefix) {
		return page, ErrArtifactV3Invalid
	}
	repos, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return page, err
	}
	defer repos.Close()
	more := func() (ArtifactV3CatalogPage, error) {
		raw, err := json.Marshal(c)
		if err != nil {
			return page, err
		}
		page.HasMore, page.NextCursor = true, base64.RawURLEncoding.EncodeToString(raw)
		return page, nil
	}
	budget := artifactV3CatalogScanBudget
	byteBudget := artifactV3CatalogByteBudget
	valid := repos.First()
	if c.Repository != "" {
		valid = repos.SeekGE([]byte(c.Repository))
	}
	for ; valid; valid = repos.Next() {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		key := string(repos.Key())
		if c.Repository != key {
			c.Repository, c.Phase, c.After = key, 0, ""
		}
		if c.Phase == 3 {
			continue
		}
		if budget == 0 {
			return more()
		}
		if len(repos.Value()) > artifactV3CatalogByteBudget {
			return page, ErrArtifactV3Quota
		}
		if len(repos.Value()) > byteBudget {
			return more()
		}
		byteBudget -= len(repos.Value())
		budget--
		var repo ArtifactV3RepositoryProjection
		if err := json.Unmarshal(repos.Value(), &repo); err != nil {
			return page, err
		}
		if repo.AccountScopeID != account || repo.UserID != user || (o.SessionID != "" && repo.OwnerSessionID != o.SessionID) || (o.ArtifactID != "" && repo.ArtifactID != o.ArtifactID) {
			c.Phase = 3
			continue
		}
		session, ok, err := s.GetRetainedArtifactSourceSession(repo.OwnerSessionID)
		if err != nil {
			return page, err
		}
		if !ok || session.AccountScopeID != account || session.UserID != user {
			c.Phase = 3
			continue
		}
		for c.Phase < 3 {
			if c.Phase == 0 {
				c.Phase = 1
				if repo.HeadCommitOID != "" {
					rev, ok, err := s.GetArtifactV3Revision(account, user, repo.ArtifactID, repo.HeadCommitOID)
					if err != nil {
						return page, err
					}
					if ok && readyArtifactV3CatalogRevision(repo, rev) {
						item := artifactV3CatalogRevisionItem(repo, rev, "head")
						if artifactV3CatalogItemMatches(item, o) {
							page.Items = append(page.Items, item)
						}
					}
				}
				if len(page.Items) >= o.Limit {
					return more()
				}
			}
			recordPrefix := KeyArtifactV3CandidatePrefix(account, repo.ArtifactID)
			if c.Phase == 2 {
				recordPrefix = KeyArtifactV3RevisionPrefix(account, repo.ArtifactID)
			}
			if c.After != "" && !strings.HasPrefix(c.After, recordPrefix) {
				return page, ErrArtifactV3Invalid
			}
			records, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(recordPrefix), UpperBound: []byte(recordPrefix + "\xff")})
			if err != nil {
				return page, err
			}
			validRecord := records.First()
			if c.After != "" {
				validRecord = records.SeekGE([]byte(c.After))
				if validRecord && string(records.Key()) == c.After {
					validRecord = records.Next()
				}
			}
			for ; validRecord; validRecord = records.Next() {
				if err := ctx.Err(); err != nil {
					records.Close()
					return page, err
				}
				if budget == 0 {
					records.Close()
					return more()
				}
				if len(records.Value()) > artifactV3CatalogByteBudget-len(repos.Value()) {
					records.Close()
					return page, ErrArtifactV3Quota
				}
				if len(records.Value()) > byteBudget {
					records.Close()
					return more()
				}
				byteBudget -= len(records.Value())
				budget--
				c.After = string(records.Key())
				var item ArtifactV3CatalogItem
				if c.Phase == 1 {
					var cand ArtifactV3CandidateProjection
					if err := json.Unmarshal(records.Value(), &cand); err != nil {
						records.Close()
						return page, err
					}
					if cand.OwnerSessionID != repo.OwnerSessionID || cand.ArtifactID != repo.ArtifactID || cand.CandidateRef == "refs/heads/artifact" || (cand.Status != "ready" && cand.Status != "selected") || !artifactV3EvidenceReady(cand.Build, cand.CommitOID) || !artifactV3EvidenceReady(cand.Preview, cand.CommitOID) {
						continue
					}
					rev, ok, err := s.GetArtifactV3Revision(account, user, repo.ArtifactID, cand.CommitOID)
					if err != nil {
						records.Close()
						return page, err
					}
					if !ok || !readyArtifactV3CatalogRevision(repo, rev) {
						continue
					}
					item = artifactV3CatalogRevisionItem(repo, rev, "candidate")
					item.CandidateID, item.TurnID, item.Status = cand.CandidateID, cand.TurnID, cand.Status
				} else {
					var rev ArtifactV3RevisionProjection
					if err := json.Unmarshal(records.Value(), &rev); err != nil {
						records.Close()
						return page, err
					}
					if rev.CommitOID == repo.HeadCommitOID || !readyArtifactV3CatalogRevision(repo, rev) {
						continue
					}
					// Immutable revisions and candidate slots are distinct catalog records.
					// A candidate can also appear as its underlying historical ready revision.
					item = artifactV3CatalogRevisionItem(repo, rev, "historical_revision")
				}
				if artifactV3CatalogItemMatches(item, o) {
					page.Items = append(page.Items, item)
				}
				if len(page.Items) >= o.Limit {
					records.Close()
					return more()
				}
			}
			err = records.Error()
			records.Close()
			if err != nil {
				return page, err
			}
			c.Phase++
			c.After = ""
		}
	}
	if err := repos.Error(); err != nil {
		return page, err
	}
	return page, nil
}

func readyArtifactV3CatalogRevision(repo ArtifactV3RepositoryProjection, rev ArtifactV3RevisionProjection) bool {
	return rev.OwnerSessionID == repo.OwnerSessionID && rev.ArtifactID == repo.ArtifactID && artifactV3EvidenceReady(rev.Build, rev.CommitOID) && artifactV3EvidenceReady(rev.Preview, rev.CommitOID)
}

func artifactV3CatalogRevisionItem(repo ArtifactV3RepositoryProjection, rev ArtifactV3RevisionProjection, kind string) ArtifactV3CatalogItem {
	status := "ready"
	if kind == "head" {
		status = "selected"
	}
	return ArtifactV3CatalogItem{ArtifactID: repo.ArtifactID, SessionID: repo.OwnerSessionID, CommitOID: rev.CommitOID, RevisionRef: "revision-" + rev.CommitOID, SourceKind: kind, Status: status, Intent: repo.IntentReference, MediaType: "text/html", Parts: rev.Parts, FileCount: rev.FileCount, TreeBytes: rev.TreeBytes, CreatedAt: rev.CreatedAt, Build: rev.Build, Preview: rev.Preview, Lineage: rev.Lineage, Reference: SessionArtifactSelectionReference{SessionID: repo.OwnerSessionID, ArtifactID: repo.ArtifactID, CommitOID: rev.CommitOID, RevisionRef: "revision-" + rev.CommitOID, ProjectionSeq: rev.EventSeq, Action: "use"}}
}

func artifactV3CatalogItemMatches(item ArtifactV3CatalogItem, o ArtifactV3CatalogOptions) bool {
	if o.Status != "" && !(o.Status == "ready" && item.Status == "selected") && o.Status != item.Status {
		return false
	}
	if o.SourceKind != "" && item.SourceKind != o.SourceKind {
		return false
	}
	if o.MediaType != "" && canonicalArtifactCatalogMediaType(item.MediaType) != o.MediaType {
		return false
	}
	if o.CreatedAfter != 0 && item.CreatedAt < o.CreatedAfter || o.CreatedBefore != 0 && item.CreatedAt > o.CreatedBefore {
		return false
	}
	if o.Query == "" {
		return true
	}
	fields := []string{item.ArtifactID, item.Intent, item.CommitOID, item.RevisionRef, item.CandidateID, item.TurnID}
	for _, part := range item.Parts {
		fields = append(fields, part.ID, part.Label)
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), o.Query) {
			return true
		}
	}
	return false
}
