package pebblestore

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

const (
	V3SessionMutationArtifactV2WorkingCreated             = "artifact.v2.working.created"
	V3SessionMutationArtifactV2PartDeclared               = "artifact.v2.part.declared"
	V3SessionMutationArtifactV2PartRevisionAppended       = "artifact.v2.part_revision.appended"
	V3SessionMutationArtifactV2CompositionHeadAdvanced    = "artifact.v2.composition_head.advanced"
	V3SessionMutationArtifactV2BuildQueued                = "artifact.v2.build.queued"
	V3SessionMutationArtifactV2BuildStarted               = "artifact.v2.build.started"
	V3SessionMutationArtifactV2BuildSucceeded             = "artifact.v2.build.succeeded"
	V3SessionMutationArtifactV2BuildFailed                = "artifact.v2.build.failed"
	V3SessionMutationArtifactV2BuildCancelled             = "artifact.v2.build.cancelled"
	V3SessionMutationArtifactV2ValidationQueued           = "artifact.v2.validation.queued"
	V3SessionMutationArtifactV2ValidationStarted          = "artifact.v2.validation.started"
	V3SessionMutationArtifactV2ValidationValid            = "artifact.v2.validation.valid"
	V3SessionMutationArtifactV2ValidationInvalid          = "artifact.v2.validation.invalid"
	V3SessionMutationArtifactV2ValidationFailedToRun      = "artifact.v2.validation.failed_to_run"
	V3SessionMutationArtifactV2ValidationCancelled        = "artifact.v2.validation.cancelled"
	V3SessionMutationArtifactV2IterationOpened            = "artifact.v2.iteration.opened"
	V3SessionMutationArtifactV2IterationCandidateAppended = "artifact.v2.iteration.candidate_appended"
	V3SessionMutationArtifactV2IterationAwaitingSelection = "artifact.v2.iteration.awaiting_selection"
	V3SessionMutationArtifactV2IterationSelected          = "artifact.v2.iteration.selected"
	V3SessionMutationArtifactV2IterationClosed            = "artifact.v2.iteration.closed"
	V3SessionMutationArtifactV2CandidateFailed            = "artifact.v2.candidate.failed"
	V3SessionMutationArtifactV2DerivativeCreated          = "artifact.v2.derivative.created"
	V3SessionMutationArtifactV2DerivativeFailed           = "artifact.v2.derivative.failed"
	V3SessionMutationArtifactV2PublishedHeadCreated       = "artifact.v2.published_head.created"
)

type preparedArtifactV2Mutation struct {
	Mutation   *ArtifactV2Mutation
	Projection ArtifactV2Projection
	Previous   *ArtifactV2WorkingArtifact
	PartCount  int
}

func normalizeArtifactV2Mutation(input *V3SessionMutationInput) {
	if input == nil || input.ArtifactV2 == nil {
		return
	}
	m := input.ArtifactV2
	if m.Working != nil {
		m.Working.ID = strings.TrimSpace(m.Working.ID)
		m.Working.AccountScopeID = strings.TrimSpace(m.Working.AccountScopeID)
		m.Working.UserID = strings.TrimSpace(m.Working.UserID)
		m.Working.SessionID = strings.TrimSpace(m.Working.SessionID)
		m.Working.Kind = strings.TrimSpace(m.Working.Kind)
		m.Working.State = strings.TrimSpace(m.Working.State)
		m.Working.PolicyRevision = strings.TrimSpace(m.Working.PolicyRevision)
		m.Working.CapabilityClass = strings.TrimSpace(m.Working.CapabilityClass)
		m.Working.IntentReference = strings.TrimSpace(m.Working.IntentReference)
		m.Working.CreationRequestID = strings.TrimSpace(m.Working.CreationRequestID)
	}
}

func isArtifactV2MutationKind(kind string) bool {
	switch kind {
	case V3SessionMutationArtifactV2WorkingCreated,
		V3SessionMutationArtifactV2PartDeclared,
		V3SessionMutationArtifactV2PartRevisionAppended,
		V3SessionMutationArtifactV2CompositionHeadAdvanced,
		V3SessionMutationArtifactV2BuildQueued,
		V3SessionMutationArtifactV2BuildStarted,
		V3SessionMutationArtifactV2BuildSucceeded,
		V3SessionMutationArtifactV2BuildFailed,
		V3SessionMutationArtifactV2BuildCancelled,
		V3SessionMutationArtifactV2ValidationQueued,
		V3SessionMutationArtifactV2ValidationStarted,
		V3SessionMutationArtifactV2ValidationValid,
		V3SessionMutationArtifactV2ValidationInvalid,
		V3SessionMutationArtifactV2ValidationFailedToRun,
		V3SessionMutationArtifactV2ValidationCancelled,
		V3SessionMutationArtifactV2IterationOpened,
		V3SessionMutationArtifactV2IterationCandidateAppended,
		V3SessionMutationArtifactV2IterationAwaitingSelection,
		V3SessionMutationArtifactV2IterationSelected,
		V3SessionMutationArtifactV2IterationClosed,
		V3SessionMutationArtifactV2CandidateFailed,
		V3SessionMutationArtifactV2DerivativeCreated,
		V3SessionMutationArtifactV2DerivativeFailed,
		V3SessionMutationArtifactV2PublishedHeadCreated:
		return true
	default:
		return false
	}
}

func validateArtifactV2MutationInput(input V3SessionMutationInput) error {
	if input.ArtifactV2 == nil {
		if isArtifactV2MutationKind(input.Kind) {
			return errors.New("artifact v2 mutation payload is required")
		}
		return nil
	}
	if !isArtifactV2MutationKind(input.Kind) {
		return errors.New("artifact v2 payload requires an artifact.v2 mutation kind")
	}
	m := input.ArtifactV2
	if m.Working == nil {
		return errors.New("artifact v2 mutation requires the complete working artifact projection")
	}
	w := m.Working
	if w.SchemaVersion != ArtifactV2SchemaVersion || w.ID == "" || w.Kind == "" || w.State == "" || w.PolicyRevision == "" || w.CapabilityClass == "" || w.CreationRequestID == "" {
		return errors.New("artifact v2 working artifact is incomplete")
	}
	if err := validateV3MutationEmbeddedOwnership(input, "artifact v2 working artifact", w.SessionID, w.UserID, w.AccountScopeID); err != nil {
		return err
	}
	if w.Revision == 0 {
		return errors.New("artifact v2 working revision is required")
	}
	if m.Part != nil && (m.Part.ArtifactID != w.ID || m.Part.ID == "" || m.Part.Key == "" || m.Part.MediaClass == "") {
		return errors.New("artifact v2 part is incomplete or foreign")
	}
	validateRevision := func(r *ArtifactV2PartRevision) error {
		if r == nil || r.ArtifactID != w.ID || r.ID == "" || r.PartID == "" || r.Blob.DigestSHA256 == "" || r.Blob.CommitOID == "" || r.Blob.BlobOID == "" || r.Blob.RepositoryID == "" || r.Blob.Size < 0 || r.Blob.MediaType == "" {
			return errors.New("artifact v2 part revision is incomplete or foreign")
		}
		return nil
	}
	if m.PartRevision != nil {
		if err := validateRevision(m.PartRevision); err != nil {
			return err
		}
	}
	for i := range m.PartRevisions {
		if err := validateRevision(&m.PartRevisions[i]); err != nil {
			return err
		}
	}
	if m.Composition != nil {
		c := m.Composition
		if c.ArtifactID != w.ID || c.ID == "" || c.PolicyRevision != w.PolicyRevision || c.ConstructionVersion == "" || len(c.Parts) == 0 || c.DigestSHA256 != ArtifactV2CompositionDigest(c.PolicyRevision, c.ConstructionVersion, c.Parts) {
			return errors.New("artifact v2 composition is incomplete, foreign, or has an invalid digest")
		}
	}
	if m.Derivative != nil {
		d := m.Derivative
		if d.ArtifactID != w.ID || d.ID == "" || d.CompositionID == "" || d.CompositionDigest == "" || d.BuildID == "" || d.ValidationID == "" || d.PolicyRevision != w.PolicyRevision || (d.Kind != "preview" && d.Kind != "fallback" && d.Kind != "mp4" && d.Kind != "storyboard_still") || (d.Status != "ready" && d.Status != "failed") {
			return errors.New("artifact v2 derivative is incomplete, foreign, or invalid")
		}
		if d.Status == "ready" && (d.Output == nil || d.Output.DigestSHA256 == "" || d.Output.RepositoryID == "") {
			return errors.New("artifact v2 ready derivative requires immutable output")
		}
		if d.Kind == "storyboard_still" && (d.SourcePartID == "" || d.SourcePartRevisionID == "" || d.CaptureStateID == "" || d.Output == nil || d.Output.MediaType != "image/png") {
			return errors.New("artifact v2 storyboard still requires exact source-part lineage, capture state, and PNG output")
		}
		if d.Status == "failed" && d.Output != nil {
			return errors.New("artifact v2 failed derivative cannot carry output")
		}
	}
	return nil
}

func (s *SessionStore) prepareArtifactV2Mutation(input V3SessionMutationInput, seq uint64, now int64) (preparedArtifactV2Mutation, error) {
	if input.ArtifactV2 == nil {
		return preparedArtifactV2Mutation{}, nil
	}
	m := *input.ArtifactV2
	w := *m.Working
	session, sessionOK, err := s.GetSession(input.SessionID)
	if err != nil {
		return preparedArtifactV2Mutation{}, err
	}
	if !sessionOK || session.AccountScopeID != input.AccountScopeID || session.UserID != input.UserID {
		return preparedArtifactV2Mutation{}, errors.New("artifact v2 mutation session ownership does not match")
	}
	current, found, err := s.GetArtifactV2Working(input.AccountScopeID, w.ID)
	if err != nil {
		return preparedArtifactV2Mutation{}, err
	}
	if input.Kind == V3SessionMutationArtifactV2WorkingCreated {
		if found {
			return preparedArtifactV2Mutation{}, fmt.Errorf("artifact v2 working artifact %q already exists", w.ID)
		}
		if m.ExpectedWorkingRevision != nil || w.Revision != 1 {
			return preparedArtifactV2Mutation{}, errors.New("artifact v2 create requires revision 1 without an expected prior revision")
		}
	} else {
		if !found {
			return preparedArtifactV2Mutation{}, fmt.Errorf("artifact v2 working artifact %q was not found", w.ID)
		}
		if current.SessionID != input.SessionID || current.AccountScopeID != input.AccountScopeID || current.UserID != input.UserID {
			return preparedArtifactV2Mutation{}, errors.New("artifact v2 working artifact ownership does not match")
		}
		if m.ExpectedWorkingRevision == nil || *m.ExpectedWorkingRevision != current.Revision || w.Revision != current.Revision+1 {
			return preparedArtifactV2Mutation{}, errors.New("artifact v2 working revision is stale")
		}
	}
	w.SchemaVersion, w.AccountScopeID, w.UserID, w.SessionID, w.EventSeq, w.UpdatedAt = ArtifactV2SchemaVersion, input.AccountScopeID, input.UserID, input.SessionID, seq, now
	if w.CreatedAt == 0 {
		if found {
			w.CreatedAt = current.CreatedAt
		} else {
			w.CreatedAt = now
		}
	}
	m.Working = &w
	var previous *ArtifactV2WorkingArtifact
	if found {
		copy := current
		previous = &copy
	}

	parts, err := s.ListArtifactV2Parts(input.AccountScopeID, w.ID, 256)
	if err != nil {
		return preparedArtifactV2Mutation{}, err
	}
	partCount := len(parts)
	if m.Part != nil {
		part := *m.Part
		if _, exists, err := s.GetArtifactV2Part(input.AccountScopeID, w.ID, part.ID); err != nil {
			return preparedArtifactV2Mutation{}, err
		} else if exists {
			return preparedArtifactV2Mutation{}, fmt.Errorf("artifact v2 part %q already exists", part.ID)
		}
		for _, existing := range parts {
			if existing.Key == part.Key {
				return preparedArtifactV2Mutation{}, fmt.Errorf("artifact v2 part key %q already exists", part.Key)
			}
		}
		part.SchemaVersion, part.AccountScopeID, part.UserID, part.SessionID, part.Revision, part.EventSeq, part.CreatedAt, part.UpdatedAt = ArtifactV2SchemaVersion, input.AccountScopeID, input.UserID, input.SessionID, 1, seq, now, now
		m.Part = &part
		partCount++
	}
	prepareRevision := func(source ArtifactV2PartRevision) (ArtifactV2PartRevision, error) {
		r := source
		part, ok, err := s.GetArtifactV2Part(input.AccountScopeID, w.ID, r.PartID)
		if err != nil {
			return r, err
		}
		if !ok || part.ArtifactID != w.ID {
			return r, errors.New("artifact v2 revision part was not found")
		}
		if _, exists, err := s.GetArtifactV2PartRevision(input.AccountScopeID, w.ID, r.PartID, r.ID); err != nil {
			return r, err
		} else if exists {
			return r, fmt.Errorf("artifact v2 part revision %q already exists", r.ID)
		}
		if r.ParentRevisionID != "" {
			if _, ok, err := s.GetArtifactV2PartRevision(input.AccountScopeID, w.ID, r.PartID, r.ParentRevisionID); err != nil || !ok {
				if err != nil {
					return r, err
				}
				return r, errors.New("artifact v2 parent part revision was not found")
			}
		}
		r.SchemaVersion, r.AccountScopeID, r.UserID, r.SessionID, r.Revision, r.EventSeq, r.CreatedAt = ArtifactV2SchemaVersion, input.AccountScopeID, input.UserID, input.SessionID, 1, seq, now
		return r, nil
	}
	if m.PartRevision != nil {
		r, err := prepareRevision(*m.PartRevision)
		if err != nil {
			return preparedArtifactV2Mutation{}, err
		}
		m.PartRevision = &r
	}
	if len(m.PartRevisions) != 0 {
		seen := make(map[string]bool, len(m.PartRevisions))
		prepared := make([]ArtifactV2PartRevision, 0, len(m.PartRevisions))
		for _, source := range m.PartRevisions {
			key := source.PartID + "\x00" + source.ID
			if seen[key] {
				return preparedArtifactV2Mutation{}, errors.New("artifact v2 mutation contains duplicate part revisions")
			}
			seen[key] = true
			r, err := prepareRevision(source)
			if err != nil {
				return preparedArtifactV2Mutation{}, err
			}
			prepared = append(prepared, r)
		}
		m.PartRevisions = prepared
	}
	if m.Composition != nil {
		c := *m.Composition
		if _, exists, err := s.GetArtifactV2Composition(input.AccountScopeID, w.ID, c.ID); err != nil {
			return preparedArtifactV2Mutation{}, err
		} else if exists {
			return preparedArtifactV2Mutation{}, fmt.Errorf("artifact v2 composition %q already exists", c.ID)
		}
		actualHead := uint64(0)
		if current.CompositionHead != nil {
			actualHead = current.CompositionHead.HeadRevision
		}
		if m.AdvanceCompositionHead && (m.ExpectedCompositionHeadRevision == nil || *m.ExpectedCompositionHeadRevision != actualHead) {
			return preparedArtifactV2Mutation{}, errors.New("artifact v2 composition head is stale")
		}
		seen := map[string]bool{}
		pendingRevisions := make(map[string]ArtifactV2PartRevision, len(m.PartRevisions))
		for _, revision := range m.PartRevisions {
			pendingRevisions[revision.PartID+"\x00"+revision.ID] = revision
		}
		for _, selected := range c.Parts {
			if seen[selected.PartID] {
				return preparedArtifactV2Mutation{}, errors.New("artifact v2 composition contains duplicate parts")
			}
			seen[selected.PartID] = true
			revision, ok, err := s.GetArtifactV2PartRevision(input.AccountScopeID, w.ID, selected.PartID, selected.PartRevisionID)
			if !ok && err == nil {
				revision, ok = pendingRevisions[selected.PartID+"\x00"+selected.PartRevisionID]
			}
			if err != nil {
				return preparedArtifactV2Mutation{}, err
			}
			if !ok || revision.Blob.DigestSHA256 != selected.DigestSHA256 {
				return preparedArtifactV2Mutation{}, errors.New("artifact v2 composition references a missing or stale part revision")
			}
			if current.CompositionHead != nil {
				previousComposition, ok, err := s.GetArtifactV2Composition(input.AccountScopeID, w.ID, current.CompositionHead.CompositionID)
				if err != nil {
					return preparedArtifactV2Mutation{}, err
				}
				if !ok {
					return preparedArtifactV2Mutation{}, errors.New("artifact v2 current composition is missing")
				}
				for _, old := range previousComposition.Parts {
					if old.PartID == selected.PartID && old.Locked && (old.PartRevisionID != selected.PartRevisionID || !selected.Locked) {
						if !m.AllowLockedPartChanges {
							return preparedArtifactV2Mutation{}, errors.New("artifact v2 locked part cannot change")
						}
						if old.PartRevisionID != selected.PartRevisionID {
							return preparedArtifactV2Mutation{}, errors.New("artifact v2 user lock command cannot replace locked bytes")
						}
					}
				}
			}
		}
		if len(seen) != partCount {
			return preparedArtifactV2Mutation{}, errors.New("artifact v2 composition must select every declared part exactly once")
		}
		c.SchemaVersion, c.AccountScopeID, c.UserID, c.SessionID, c.Revision, c.EventSeq, c.CreatedAt = ArtifactV2SchemaVersion, input.AccountScopeID, input.UserID, input.SessionID, actualHead+1, seq, now
		m.Composition = &c
		if m.AdvanceCompositionHead {
			w.CompositionHead = &ArtifactV2CompositionHead{CompositionID: c.ID, HeadRevision: c.Revision, DigestSHA256: c.DigestSHA256, EventSeq: seq}
			m.Working = &w
		}
	}
	if m.Build != nil {
		b := *m.Build
		b.SchemaVersion, b.AccountScopeID, b.UserID, b.SessionID, b.EventSeq = ArtifactV2SchemaVersion, input.AccountScopeID, input.UserID, input.SessionID, seq
		m.Build = &b
	}
	if m.Validation != nil {
		v := *m.Validation
		v.SchemaVersion, v.AccountScopeID, v.UserID, v.SessionID, v.EventSeq = ArtifactV2SchemaVersion, input.AccountScopeID, input.UserID, input.SessionID, seq
		m.Validation = &v
	}
	if m.Derivative != nil {
		d := *m.Derivative
		if _, exists, err := s.GetArtifactV2Derivative(input.AccountScopeID, w.ID, d.ID); err != nil {
			return preparedArtifactV2Mutation{}, err
		} else if exists {
			return preparedArtifactV2Mutation{}, fmt.Errorf("artifact v2 derivative %q already exists", d.ID)
		}
		d.SchemaVersion, d.AccountScopeID, d.UserID, d.SessionID, d.Revision, d.EventSeq, d.CreatedAt = ArtifactV2SchemaVersion, input.AccountScopeID, input.UserID, input.SessionID, 1, seq, now
		m.Derivative = &d
	}
	if m.Iteration != nil {
		r := *m.Iteration
		existing, exists, err := s.GetArtifactV2Iteration(input.AccountScopeID, w.ID, r.ID)
		if err != nil {
			return preparedArtifactV2Mutation{}, err
		}
		if exists {
			if m.ExpectedIterationRevision == nil || *m.ExpectedIterationRevision != existing.Revision || r.Revision != existing.Revision+1 {
				return preparedArtifactV2Mutation{}, errors.New("artifact v2 iteration revision is stale")
			}
		} else if m.ExpectedIterationRevision != nil || r.Revision != 1 {
			return preparedArtifactV2Mutation{}, errors.New("artifact v2 new iteration must start at revision 1")
		}
		r.SchemaVersion, r.AccountScopeID, r.UserID, r.SessionID, r.EventSeq = ArtifactV2SchemaVersion, input.AccountScopeID, input.UserID, input.SessionID, seq
		m.Iteration = &r
	}
	if m.PublishedHead != nil {
		p := *m.PublishedHead
		p.SchemaVersion, p.AccountScopeID, p.UserID, p.SessionID, p.EventSeq = ArtifactV2SchemaVersion, input.AccountScopeID, input.UserID, input.SessionID, seq
		m.PublishedHead = &p
	}
	projection := artifactV2ProjectionFromWorking(w, partCount)
	return preparedArtifactV2Mutation{Mutation: &m, Projection: projection, Previous: previous, PartCount: partCount}, nil
}

func setArtifactV2MutationInBatch(batch *pebble.Batch, prepared preparedArtifactV2Mutation) error {
	if prepared.Mutation == nil {
		return nil
	}
	m, w := prepared.Mutation, prepared.Mutation.Working
	if w == nil {
		return errors.New("prepared artifact v2 mutation is missing working state")
	}
	if prepared.Previous != nil && prepared.Previous.UpdatedAt != w.UpdatedAt {
		if err := batch.Delete([]byte(KeyArtifactV2ByAccountUpdated(w.AccountScopeID, prepared.Previous.UpdatedAt, w.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return err
		}
	}
	if err := setArtifactV2JSON(batch, KeyArtifactV2Working(w.AccountScopeID, w.ID), w); err != nil {
		return err
	}
	if err := batch.Set([]byte(KeyArtifactV2BySession(w.AccountScopeID, w.SessionID, w.ID)), []byte(w.ID), nil); err != nil {
		return err
	}
	if err := batch.Set([]byte(KeyArtifactV2ByAccountUpdated(w.AccountScopeID, w.UpdatedAt, w.ID)), []byte(w.ID), nil); err != nil {
		return err
	}
	if m.Part != nil {
		if err := setArtifactV2JSON(batch, KeyArtifactV2Part(w.AccountScopeID, w.ID, m.Part.ID), m.Part); err != nil {
			return err
		}
	}
	if m.PartRevision != nil {
		if err := setArtifactV2JSON(batch, KeyArtifactV2PartRevision(w.AccountScopeID, w.ID, m.PartRevision.PartID, m.PartRevision.ID), m.PartRevision); err != nil {
			return err
		}
	}
	for i := range m.PartRevisions {
		revision := &m.PartRevisions[i]
		if err := setArtifactV2JSON(batch, KeyArtifactV2PartRevision(w.AccountScopeID, w.ID, revision.PartID, revision.ID), revision); err != nil {
			return err
		}
	}
	if m.Composition != nil {
		if err := setArtifactV2JSON(batch, KeyArtifactV2Composition(w.AccountScopeID, w.ID, m.Composition.ID), m.Composition); err != nil {
			return err
		}
	}
	if m.Build != nil {
		if err := setArtifactV2JSON(batch, KeyArtifactV2Build(w.AccountScopeID, w.ID, m.Build.ID), m.Build); err != nil {
			return err
		}
	}
	if m.Validation != nil {
		if err := setArtifactV2JSON(batch, KeyArtifactV2Validation(w.AccountScopeID, w.ID, m.Validation.ID), m.Validation); err != nil {
			return err
		}
	}
	if m.Derivative != nil {
		if err := setArtifactV2JSON(batch, KeyArtifactV2Derivative(w.AccountScopeID, w.ID, m.Derivative.ID), m.Derivative); err != nil {
			return err
		}
	}
	if m.Iteration != nil {
		if err := setArtifactV2JSON(batch, KeyArtifactV2Iteration(w.AccountScopeID, w.ID, m.Iteration.ID), m.Iteration); err != nil {
			return err
		}
	}
	if m.PublishedHead != nil {
		if err := setArtifactV2JSON(batch, KeyArtifactV2PublishedHead(w.AccountScopeID, w.ID, m.PublishedHead.ID), m.PublishedHead); err != nil {
			return err
		}
	}
	return nil
}
