package pebblestore

// DesignArchive changes library visibility, never execution or selection. The
// identity is the artifact across all requests and immutable revisions.
type DesignArchive struct {
	IdempotencyKey  string    `json:"idempotency_key"`
	ExpectedVersion uint64    `json:"expected_version"`
	Ref             DesignRef `json:"ref"`
	Archived        bool      `json:"archived"`
}

func (s *Store) SetDesignArchived(p DesignPrincipal, sessionID string, in DesignArchive) (DesignArtifact, error) {
	var zero DesignArtifact
	if designOwner(p) != nil || !designID(sessionID) || !designID(in.IdempotencyKey) {
		return zero, ErrDesignInvalid
	}
	s.designMu.Lock()
	defer s.designMu.Unlock()
	a, err := s.RequireDesignArtifactSession(p, sessionID, in.Ref.ArtifactID)
	if err != nil {
		return zero, err
	}
	r, err := s.GetDesignRequest(p, a.RequestGroupID)
	if err != nil {
		return zero, err
	}
	parent, found, err := NewSessionStore(s).GetSession(sessionID)
	if err != nil {
		return zero, err
	}
	if (!found && r.Canonical) || (found && (parent.AccountScopeID != p.AccountID || parent.UserID != p.PrincipalID)) {
		return zero, ErrDesignNotFound
	}
	receipt := designKey(p, "archive", a.ID+"/"+in.IdempotencyKey)
	if found, err := s.designReplay(receipt, in, &zero); found || err != nil {
		return zero, err
	}
	if _, err := s.ReadDesignRevision(p, in.Ref); err != nil {
		return zero, err
	}
	if a.ArchiveVersion != in.ExpectedVersion || a.ArchiveVersion == ^uint64(0) {
		return zero, ErrDesignConflict
	}
	a.Archived = in.Archived
	a.ArchiveVersion++
	b := s.db.NewBatch()
	defer b.Close()
	if err := designSet(b, designKey(p, "artifact", a.ID), a); err != nil {
		return zero, err
	}
	if err := designSaveReceipt(b, receipt, in, a); err != nil {
		return zero, err
	}
	if err := s.commitDesignChange(p, r, receipt, map[string]any{"request_id": r.ID, "artifact_id": a.ID, "archived": a.Archived, "archive_version": a.ArchiveVersion}, b); err != nil {
		return zero, err
	}
	return a, nil
}

// Catalog-only overlay: never persist these candidate fields as an authority.
// Keep candidate indexes intact because attempt and preview refs depend on them.
func (s *Store) designCatalogArchive(p DesignPrincipal, r *DesignRequest) error {
	for i := range r.Candidates {
		a, err := s.GetDesignArtifact(p, r.Candidates[i].Spec.ArtifactID)
		if err != nil {
			return err
		}
		r.Candidates[i].Archived = a.Archived
		r.Candidates[i].ArchiveVersion = a.ArchiveVersion
	}
	return nil
}

// FilterDesignCatalogView filters rows, not candidate indexes. Mixed request
// groups appear in both views; consumers filter candidates using Archived.
func FilterDesignCatalogView(rows []DesignRequest, archived bool) []DesignRequest {
	out := make([]DesignRequest, 0, len(rows))
	for _, r := range rows {
		for _, c := range r.Candidates {
			if c.Archived == archived {
				out = append(out, r)
				break
			}
		}
	}
	return out
}
