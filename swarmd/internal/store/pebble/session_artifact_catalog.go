package pebblestore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

const (
	SessionArtifactCatalogDefaultLimit = 50
	SessionArtifactCatalogMaxLimit     = 100
	artifactCatalogCursorVersion       = 1
)

// SessionArtifactCatalogOptions describes one bounded account/user-owned catalog
// query. Cursor is opaque to callers and is bound to the complete filter set.
type SessionArtifactCatalogOptions struct {
	Query         string
	Status        string
	MediaType     string
	CreatedAfter  int64
	CreatedBefore int64
	Limit         int
	Cursor        string
}

// SessionArtifactCatalogItem is one flattened artifact result. Ready variants
// always carry the complete immutable reference needed by cross-session callers.
type SessionArtifactCatalogItem struct {
	Collection SessionArtifactCollection          `json:"collection"`
	Variant    SessionArtifactVariant             `json:"variant"`
	Reference  *SessionArtifactSelectionReference `json:"reference,omitempty"`
}

type SessionArtifactCatalogPage struct {
	Items      []SessionArtifactCatalogItem `json:"items"`
	NextCursor string                       `json:"next_cursor,omitempty"`
	HasMore    bool                         `json:"has_more"`
}

type sessionArtifactCatalogCursor struct {
	Version        int    `json:"v"`
	SnapshotAt     int64  `json:"s"`
	LastCreatedAt  int64  `json:"t"`
	LastSessionID  string `json:"sid"`
	LastCollection string `json:"cid"`
	LastVariant    string `json:"vid"`
	Filter         string `json:"f"`
}

// SearchSessionArtifactCatalog traverses only sessions owned by the supplied
// authenticated account and user. Results are ordered by immutable creation
// time descending, then session/collection/variant ids ascending. The first
// cursor pins the highest visible creation time and every continuation is bound
// to the normalized filters, so newly created artifacts cannot enter a walk.
func (s *SessionStore) SearchSessionArtifactCatalog(accountScopeID, userID string, options SessionArtifactCatalogOptions) (SessionArtifactCatalogPage, error) {
	if s == nil || s.store == nil {
		return SessionArtifactCatalogPage{}, errors.New("session store is not configured")
	}
	accountScopeID, userID = strings.TrimSpace(accountScopeID), strings.TrimSpace(userID)
	if accountScopeID == "" || userID == "" {
		return SessionArtifactCatalogPage{}, errors.New("artifact catalog account and user ownership are required")
	}
	options.Query = strings.ToLower(strings.TrimSpace(options.Query))
	options.Status = strings.ToLower(strings.TrimSpace(options.Status))
	options.MediaType = canonicalArtifactCatalogMediaType(options.MediaType)
	if options.CreatedAfter < 0 || options.CreatedBefore < 0 || (options.CreatedAfter != 0 && options.CreatedBefore != 0 && options.CreatedAfter > options.CreatedBefore) {
		return SessionArtifactCatalogPage{}, errors.New("artifact catalog date bounds are invalid")
	}
	if options.Status != "" && options.Status != SessionArtifactStatusStaging && options.Status != SessionArtifactStatusReady && options.Status != SessionArtifactStatusFailed && options.Status != SessionArtifactStatusUnavailable {
		return SessionArtifactCatalogPage{}, errors.New("artifact catalog status is invalid")
	}
	if options.Limit <= 0 {
		options.Limit = SessionArtifactCatalogDefaultLimit
	}
	if options.Limit > SessionArtifactCatalogMaxLimit {
		options.Limit = SessionArtifactCatalogMaxLimit
	}
	filter := artifactCatalogFilterIdentity(options)
	cursor, err := decodeSessionArtifactCatalogCursor(options.Cursor, filter)
	if err != nil {
		return SessionArtifactCatalogPage{}, err
	}

	const iterateAll = int(^uint(0) >> 1)
	sessions := make([]SessionSnapshot, 0, 64)
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
		sessions = append(sessions, session)
		return nil
	}); err != nil {
		return SessionArtifactCatalogPage{}, err
	}
	items := make([]SessionArtifactCatalogItem, 0)
	for _, session := range sessions {
		// Recheck ownership against the authoritative session row immediately
		// before traversing artifacts; stale or forged index rows must never widen
		// access even if ownership changes during a catalog request.
		owned, ok, err := s.GetSession(session.ID)
		if err != nil {
			return SessionArtifactCatalogPage{}, err
		}
		if !ok || strings.TrimSpace(owned.AccountScopeID) != accountScopeID || strings.TrimSpace(owned.UserID) != userID {
			continue
		}
		collections, err := s.ListAllSessionArtifactCollections(accountScopeID, session.ID, "")
		if err != nil {
			return SessionArtifactCatalogPage{}, err
		}
		for _, collection := range collections {
			variants, err := s.ListSessionArtifactVariants(accountScopeID, session.ID, collection.ID, SessionArtifactMaxVariantsPerCollection)
			if err != nil {
				return SessionArtifactCatalogPage{}, err
			}
			for _, variant := range variants {
				if !artifactCatalogVariantMatches(collection, variant, options) {
					continue
				}
				item := SessionArtifactCatalogItem{Collection: collection, Variant: variant}
				if variant.Status == SessionArtifactStatusReady && variant.EventSeq != 0 {
					item.Reference = &SessionArtifactSelectionReference{SessionID: variant.SessionID, CollectionID: variant.CollectionID, VariantID: variant.ID, EventSeq: variant.EventSeq}
				}
				items = append(items, item)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return artifactCatalogItemBefore(items[i], items[j]) })

	snapshotAt := cursor.SnapshotAt
	if snapshotAt == 0 && len(items) > 0 {
		snapshotAt = items[0].Variant.CreatedAt
	}
	visible := make([]SessionArtifactCatalogItem, 0, len(items))
	snapshotBound := cursor.LastSessionID != "" || len(items) > 0
	for _, item := range items {
		if snapshotBound && item.Variant.CreatedAt > snapshotAt {
			continue
		}
		if cursor.LastSessionID != "" && !artifactCatalogItemAfterCursor(item, cursor) {
			continue
		}
		visible = append(visible, item)
	}
	page := SessionArtifactCatalogPage{}
	if len(visible) > options.Limit {
		page.HasMore = true
		visible = visible[:options.Limit]
	}
	page.Items = visible
	if page.HasMore && len(visible) > 0 {
		last := visible[len(visible)-1].Variant
		page.NextCursor, err = encodeSessionArtifactCatalogCursor(sessionArtifactCatalogCursor{
			Version: artifactCatalogCursorVersion, SnapshotAt: snapshotAt, LastCreatedAt: last.CreatedAt,
			LastSessionID: last.SessionID, LastCollection: last.CollectionID, LastVariant: last.ID, Filter: filter,
		})
		if err != nil {
			return SessionArtifactCatalogPage{}, err
		}
	}
	return page, nil
}

func artifactCatalogVariantMatches(collection SessionArtifactCollection, variant SessionArtifactVariant, options SessionArtifactCatalogOptions) bool {
	if options.Status != "" && variant.Status != options.Status {
		return false
	}
	if options.MediaType != "" && canonicalArtifactCatalogMediaType(variant.MediaType) != options.MediaType {
		return false
	}
	if options.CreatedAfter != 0 && variant.CreatedAt < options.CreatedAfter {
		return false
	}
	if options.CreatedBefore != 0 && variant.CreatedAt > options.CreatedBefore {
		return false
	}
	if options.Query == "" {
		return true
	}
	fields := []string{
		collection.Name, collection.Description, collection.Presentation.Kind, collection.Presentation.Label, collection.Presentation.Description,
		variant.Filename, variant.MediaType, variant.Presentation.Kind, variant.Presentation.Label, variant.Presentation.Description,
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), options.Query) {
			return true
		}
	}
	return false
}

func artifactCatalogItemBefore(left, right SessionArtifactCatalogItem) bool {
	l, r := left.Variant, right.Variant
	if l.CreatedAt != r.CreatedAt {
		return l.CreatedAt > r.CreatedAt
	}
	if l.SessionID != r.SessionID {
		return l.SessionID < r.SessionID
	}
	if l.CollectionID != r.CollectionID {
		return l.CollectionID < r.CollectionID
	}
	return l.ID < r.ID
}

func artifactCatalogItemAfterCursor(item SessionArtifactCatalogItem, cursor sessionArtifactCatalogCursor) bool {
	variant := item.Variant
	if variant.CreatedAt != cursor.LastCreatedAt {
		return variant.CreatedAt < cursor.LastCreatedAt
	}
	if variant.SessionID != cursor.LastSessionID {
		return variant.SessionID > cursor.LastSessionID
	}
	if variant.CollectionID != cursor.LastCollection {
		return variant.CollectionID > cursor.LastCollection
	}
	return variant.ID > cursor.LastVariant
}

func artifactCatalogFilterIdentity(options SessionArtifactCatalogOptions) string {
	payload, _ := json.Marshal([]any{options.Query, options.Status, options.MediaType, options.CreatedAfter, options.CreatedBefore})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func encodeSessionArtifactCatalogCursor(cursor sessionArtifactCatalogCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeSessionArtifactCatalogCursor(raw, filter string) (sessionArtifactCatalogCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return sessionArtifactCatalogCursor{Version: artifactCatalogCursorVersion, Filter: filter}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return sessionArtifactCatalogCursor{}, errors.New("artifact catalog cursor is invalid")
	}
	var cursor sessionArtifactCatalogCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != artifactCatalogCursorVersion || cursor.SnapshotAt < 0 || cursor.LastCreatedAt < 0 || cursor.LastSessionID == "" || cursor.LastCollection == "" || cursor.LastVariant == "" || cursor.Filter != filter {
		return sessionArtifactCatalogCursor{}, errors.New("artifact catalog cursor is invalid or does not match the filters")
	}
	return cursor, nil
}

func canonicalArtifactCatalogMediaType(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
}
