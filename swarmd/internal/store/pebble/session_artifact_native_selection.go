package pebblestore

import (
	"encoding/hex"
	"errors"
	"strings"
)

// Native selection authority is the account-owned, exact published Git projection.
// Display labels and task source metadata are derived here, never from the client.
func (s *SessionStore) validateNativeArtifactMessageSelection(account, user string, in SessionArtifactSelectionReference) (SessionArtifactSelectionReference, error) {
	fail := func(message string) (SessionArtifactSelectionReference, error) {
		return SessionArtifactSelectionReference{}, errors.New(message)
	}
	if in.CollectionID != "" || in.VariantID != "" || in.EventSeq != 0 || in.PartID != "" || in.Part != nil || in.PartLabel != "" || in.PartKind != "" || in.IterationID != "" || in.IterationIndex != 0 || in.IterationLabel != "" || in.IterationTheme != "" || in.IterationSectionID != "" || in.IterationSectionLabel != "" || in.IterationSectionStartMs != 0 || in.IterationSectionEndMs != 0 || in.PendingRequest != "" {
		return fail("native and legacy artifact selections cannot be mixed")
	}
	ref := SessionArtifactSelectionReference{SessionID: strings.TrimSpace(in.SessionID), ArtifactID: strings.TrimSpace(in.ArtifactID), RevisionRef: strings.TrimSpace(in.RevisionRef), Action: strings.TrimSpace(in.Action)}
	if ref.Action != "use" && ref.Action != "select" {
		return fail("native artifact action must be select or use")
	}
	if ref.SessionID == "" || len(ref.SessionID) > 256 || strings.ContainsAny(ref.SessionID, `/\\`) {
		return fail("invalid native source session")
	}
	if err := validateArtifactID("native selection", ref.ArtifactID); err != nil {
		return ref, err
	}
	commit := strings.TrimPrefix(ref.RevisionRef, "revision-")
	if len(commit) != 40 || ref.RevisionRef != "revision-"+commit || commit != strings.ToLower(commit) {
		return fail("invalid native revision reference")
	}
	if _, err := hex.DecodeString(commit); err != nil {
		return fail("invalid native revision reference")
	}
	var targetIDs []string
	if in.TargetPartIDs != nil {
		targetIDs = *in.TargetPartIDs
	}
	if len(targetIDs) > 256 {
		return fail("native target Part count exceeds bounds")
	}
	session, ok, err := s.GetSession(ref.SessionID)
	if err != nil {
		return ref, err
	}
	if !ok || session.AccountScopeID != account || session.UserID != user {
		return fail("native source session is not owned by the principal")
	}
	repository, ok, err := s.GetArtifactV3Repository(account, user, ref.ArtifactID)
	if err != nil {
		return ref, err
	}
	if !ok || repository.OwnerSessionID != ref.SessionID {
		return fail("native artifact was not found for the source session")
	}
	if repository.HeadCommitOID != commit {
		return fail("native artifact revision is stale")
	}
	revision, ok, err := s.GetArtifactV3Revision(account, user, ref.ArtifactID, commit)
	if err != nil {
		return ref, err
	}
	if !ok || revision.OwnerSessionID != ref.SessionID || revision.CommitOID != commit || revision.Build.Status != "succeeded" || revision.Preview.Status != "succeeded" || revision.Build.CommitOID != commit || revision.Preview.CommitOID != commit {
		return fail("native revision lacks exact ready evidence")
	}
	labels := make([]string, 0, len(targetIDs))
	seen := make(map[string]bool)
	normalizedIDs := make([]string, 0, len(targetIDs))
	for _, incoming := range targetIDs {
		id := strings.TrimSpace(incoming)
		if id == "" || len(id) > 128 || seen[id] {
			return fail("invalid or duplicate native target Part")
		}
		seen[id] = true
		found := false
		for _, part := range revision.Parts {
			if part.ID == id {
				labels = append(labels, part.Label)
				found = true
				break
			}
		}
		if !found {
			return fail("native target Part was not found on the exact revision")
		}
		normalizedIDs = append(normalizedIDs, id)
	}
	if len(normalizedIDs) > 0 {
		ref.TargetPartIDs = &normalizedIDs
	}
	ref.Label = "Artifact"
	if len(labels) > 0 {
		ref.Label = strings.Join(labels, ", ")
	}
	// Keep display metadata bounded even when selecting every Part.
	if len(ref.Label) > 256 {
		ref.Label = "Selected artifact Parts"
	}
	ref.CommitOID, ref.ProjectionSeq = commit, repository.EventSeq
	return ref, nil
}
