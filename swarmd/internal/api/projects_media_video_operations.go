package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

type videoPreflightService interface {
	PreflightVideoOperation(ctx context.Context, req videogen.VideoPreflightRequest) (*videogen.VideoPreflightResult, error)
}

type resolvedSourceMedia struct {
	Bytes      []byte
	MediaType  string
	Provenance *pebblestore.VideoProvenance
	SourceLink *pebblestore.VideoSourceLink
}

func isVideoAttachment(m pebblestore.ProjectTaskMediaRef) bool {
	kind := strings.ToLower(strings.TrimSpace(m.Kind))
	mt := strings.ToLower(strings.TrimSpace(m.MediaType))
	fn := strings.ToLower(strings.TrimSpace(m.Filename))

	isImg := kind == "image" || strings.HasPrefix(mt, "image/") || strings.HasSuffix(fn, ".png") || strings.HasSuffix(fn, ".jpg") || strings.HasSuffix(fn, ".jpeg") || strings.HasSuffix(fn, ".webp")
	isVid := kind == "video" || strings.HasPrefix(mt, "video/") || strings.HasSuffix(fn, ".mp4")
	if isImg && isVid {
		return false
	}
	return isVid
}

func isImageAttachment(m pebblestore.ProjectTaskMediaRef) bool {
	kind := strings.ToLower(strings.TrimSpace(m.Kind))
	mt := strings.ToLower(strings.TrimSpace(m.MediaType))
	fn := strings.ToLower(strings.TrimSpace(m.Filename))

	isImg := kind == "image" || strings.HasPrefix(mt, "image/") || strings.HasSuffix(fn, ".png") || strings.HasSuffix(fn, ".jpg") || strings.HasSuffix(fn, ".jpeg") || strings.HasSuffix(fn, ".webp")
	isVid := kind == "video" || strings.HasPrefix(mt, "video/") || strings.HasSuffix(fn, ".mp4")
	if isImg && isVid {
		return false
	}
	return isImg
}

func hasConflictingMediaDeclaration(m pebblestore.ProjectTaskMediaRef) bool {
	kind := strings.ToLower(strings.TrimSpace(m.Kind))
	mt := strings.ToLower(strings.TrimSpace(m.MediaType))
	fn := strings.ToLower(strings.TrimSpace(m.Filename))

	isImg := kind == "image" || strings.HasPrefix(mt, "image/") || strings.HasSuffix(fn, ".png") || strings.HasSuffix(fn, ".jpg") || strings.HasSuffix(fn, ".jpeg") || strings.HasSuffix(fn, ".webp")
	isVid := kind == "video" || strings.HasPrefix(mt, "video/") || strings.HasSuffix(fn, ".mp4")
	return isImg && isVid
}

// resolveSourceMediaRecord resolves full media payload, media type, and server-verified
// provenance and source link for an attached media reference.
// Client-supplied provider handles, interaction IDs, and timestamps are NEVER trusted;
// provenance is authoritatively loaded from the server's own Pebble stores.
func (s *Server) resolveSourceMediaRecord(ctx context.Context, p identity.Principal, m pebblestore.ProjectTaskMediaRef, expectedKind string, projectID ...string) (*resolvedSourceMedia, error) {
	if hasConflictingMediaDeclaration(m) {
		return nil, errors.New("conflicting attachment declarations between kind, media_type, and filename")
	}

	trimmedURL := strings.TrimSpace(m.URL)

	// Case 1: Session artifact URL (/v3/sessions/{sessionID}/artifacts/{variantID})
	if strings.Contains(trimmedURL, "/artifacts/") || (strings.HasPrefix(trimmedURL, "/v3/sessions/") && strings.Contains(trimmedURL, "/artifacts")) {
		u, parseErr := url.Parse(trimmedURL)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid artifact url: %w", parseErr)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 5 || parts[0] != "v3" || parts[1] != "sessions" || parts[3] != "artifacts" {
			return nil, errors.New("invalid artifact URL path; must be /v3/sessions/{sessionID}/artifacts/{variantID}")
		}
		sessionID := strings.TrimSpace(parts[2])
		variantID := strings.TrimSpace(parts[4])
		if sessionID == "" || variantID == "" {
			return nil, errors.New("invalid artifact URL; session_id and variant_id are required")
		}

		q := u.Query()
		requestedRev := q.Get("revision")
		if requestedRev == "" {
			requestedRev = q.Get("event_seq")
		}
		if requestedRev == "" {
			requestedRev = q.Get("rev")
		}
		if requestedRev == "" {
			requestedRev = q.Get("eventseq")
		}
		if requestedRev == "" {
			return nil, errors.New("artifact source requires exact pinned revision (e.g. ?revision=N or ?event_seq=N)")
		}

		if s == nil || s.sessions == nil || s.sessions.Store() == nil {
			return nil, errors.New("session store is not configured")
		}
		variant, ok, err := s.sessions.Store().GetSessionArtifactVariantByID(p.AccountScopeID, sessionID, variantID)
		if err != nil {
			return nil, fmt.Errorf("read artifact variant: %w", err)
		}
		if !ok {
			return nil, fmt.Errorf("session artifact variant %q not found in account scope", variantID)
		}
		curRevStr := fmt.Sprintf("%d", variant.EventSeq)
		if requestedRev != curRevStr {
			return nil, fmt.Errorf("stale artifact revision %q requested (current revision is %s)", requestedRev, curRevStr)
		}

		bytes, mType, readErr := s.resolveSourceMediaBytes(ctx, p, m, expectedKind)
		if readErr != nil {
			if variant.Lineage.VideoProvenance != nil && (variant.Lineage.VideoProvenance.InteractionID != "" || variant.Lineage.VideoProvenance.ProviderResource != "") {
				bytes = nil
				mType = variant.MediaType
				if mType == "" {
					mType = "video/mp4"
				}
			} else {
				return nil, fmt.Errorf("read artifact reference: %w", readErr)
			}
		}

		digest := variant.DigestSHA256
		if len(bytes) > 0 {
			h := sha256.Sum256(bytes)
			calcDigest := hex.EncodeToString(h[:])
			if digest != "" && !strings.EqualFold(digest, calcDigest) {
				return nil, errors.New("artifact body digest mismatch")
			}
			digest = calcDigest
		}

		srcLink := &pebblestore.VideoSourceLink{
			SessionID:    variant.SessionID,
			CollectionID: variant.CollectionID,
			VariantID:    variant.ID,
			EventSeq:     variant.EventSeq,
			DigestSHA256: digest,
			MediaRefID:   m.ID,
		}

		var prov *pebblestore.VideoProvenance
		if variant.Lineage.VideoProvenance != nil {
			if variant.Lineage.VideoProvenance.AccountScopeID != "" && p.AccountScopeID != "" && variant.Lineage.VideoProvenance.AccountScopeID != p.AccountScopeID {
				return nil, errors.New("video source belongs to a different account scope")
			}
			prov = variant.Lineage.VideoProvenance.Clone()
			prov.SourceLink = srcLink
			if prov.OutputDigestSHA256 == "" {
				prov.OutputDigestSHA256 = digest
			}
		}

		return &resolvedSourceMedia{
			Bytes:      bytes,
			MediaType:  mType,
			Provenance: prov,
			SourceLink: srcLink,
		}, nil
	}

	// Case 2: Canonical project deliverable reference (/v3/projects/{projectID}/tasks/{taskID}/deliverables/{delivID})
	if strings.Contains(trimmedURL, "/deliverables/") || (strings.HasPrefix(trimmedURL, "/v3/projects/") && strings.Contains(trimmedURL, "/tasks/")) {
		u, parseErr := url.Parse(trimmedURL)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid deliverable url: %w", parseErr)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 7 || parts[0] != "v3" || parts[1] != "projects" || parts[3] != "tasks" || parts[5] != "deliverables" {
			return nil, errors.New("invalid deliverable URL path; must be /v3/projects/{projectID}/tasks/{taskID}/deliverables/{delivID}")
		}
		projID := strings.TrimSpace(parts[2])
		tID := strings.TrimSpace(parts[4])
		dID := strings.TrimSpace(parts[6])

		if s == nil || s.sessions == nil || s.sessions.Store() == nil {
			return nil, errors.New("session store is not configured")
		}
		task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, projID, tID)
		if err != nil {
			return nil, fmt.Errorf("read project task: %w", err)
		}
		if !found || task == nil {
			return nil, fmt.Errorf("project task %q not found in account scope", tID)
		}

		var targetDeliv *pebblestore.ProjectTaskDeliverable
		for _, d := range task.Deliverables {
			if d.ID == dID {
				targetDeliv = &d
				break
			}
		}
		if targetDeliv == nil {
			return nil, fmt.Errorf("deliverable %q not found in task %q", dID, tID)
		}
		if targetDeliv.Status != "ready" {
			return nil, fmt.Errorf("deliverable %q is not ready", dID)
		}

		if strings.Contains(targetDeliv.MediaURL, "/deliverables/") {
			return nil, errors.New("nested deliverable references are not permitted")
		}

		delivRef := pebblestore.ProjectTaskMediaRef{
			ID:        targetDeliv.ID,
			URL:       targetDeliv.MediaURL,
			Kind:      expectedKind,
			MediaType: targetDeliv.Kind,
		}
		bytes, mType, readErr := s.resolveSourceMediaBytes(ctx, p, delivRef, expectedKind)
		if readErr != nil && targetDeliv.VideoProvenance == nil {
			return nil, fmt.Errorf("read deliverable media: %w", readErr)
		}

		h := sha256.Sum256(bytes)
		digest := hex.EncodeToString(h[:])
		srcLink := &pebblestore.VideoSourceLink{
			ProjectID:     projID,
			TaskID:        tID,
			DeliverableID: dID,
			DigestSHA256:  digest,
			MediaRefID:    m.ID,
		}

		var prov *pebblestore.VideoProvenance
		if targetDeliv.VideoProvenance != nil {
			if targetDeliv.VideoProvenance.AccountScopeID != "" && p.AccountScopeID != "" && targetDeliv.VideoProvenance.AccountScopeID != p.AccountScopeID {
				return nil, errors.New("video source belongs to a different account scope")
			}
			prov = targetDeliv.VideoProvenance.Clone()
			prov.SourceLink = srcLink
			if prov.OutputDigestSHA256 == "" {
				prov.OutputDigestSHA256 = digest
			}
		}

		return &resolvedSourceMedia{
			Bytes:      bytes,
			MediaType:  mType,
			Provenance: prov,
			SourceLink: srcLink,
		}, nil
	}

	// Case 3: Match deliverable by ID in project tasks
	activeProjID := ""
	if len(projectID) > 0 && strings.TrimSpace(projectID[0]) != "" {
		activeProjID = strings.TrimSpace(projectID[0])
	}
	if activeProjID != "" && m.ID != "" && !strings.HasPrefix(m.ID, "stg_") && s != nil && s.sessions != nil && s.sessions.Store() != nil {
		if tasks, err := s.sessions.Store().ListProjectTasks(p.AccountScopeID, activeProjID, 100); err == nil {
			for _, task := range tasks {
				for _, d := range task.Deliverables {
					if d.ID == m.ID && d.Status == "ready" {
						bytes, mType, readErr := s.resolveSourceMediaBytes(ctx, p, m, expectedKind)
						if readErr != nil && d.MediaURL != "" {
							bytes, mType, readErr = s.resolveSourceMediaBytes(ctx, p, pebblestore.ProjectTaskMediaRef{ID: d.ID, URL: d.MediaURL, Kind: expectedKind}, expectedKind)
						}
						if readErr == nil && (len(bytes) > 0 || d.VideoProvenance != nil) {
							h := sha256.Sum256(bytes)
							digest := hex.EncodeToString(h[:])
							srcLink := &pebblestore.VideoSourceLink{
								ProjectID:     activeProjID,
								TaskID:        task.ID,
								DeliverableID: d.ID,
								DigestSHA256:  digest,
								MediaRefID:    m.ID,
							}
							var prov *pebblestore.VideoProvenance
							if d.VideoProvenance != nil {
								if d.VideoProvenance.AccountScopeID != "" && p.AccountScopeID != "" && d.VideoProvenance.AccountScopeID != p.AccountScopeID {
									return nil, errors.New("video source belongs to a different account scope")
								}
								prov = d.VideoProvenance.Clone()
								prov.SourceLink = srcLink
								if prov.OutputDigestSHA256 == "" {
									prov.OutputDigestSHA256 = digest
								}
							}
							return &resolvedSourceMedia{
								Bytes:      bytes,
								MediaType:  mType,
								Provenance: prov,
								SourceLink: srcLink,
							}, nil
						}
					}
				}
			}
		}
	}

	// Case 4: Staging storage lookup
	isStaging := strings.HasPrefix(m.ID, "stg_") || strings.Contains(trimmedURL, "/media/staging/stg_") || strings.Contains(trimmedURL, "/v3/media-staging/stg_")
	if isStaging {
		bytes, mType, err := s.resolveSourceMediaBytes(ctx, p, m, expectedKind)
		if err != nil {
			return nil, fmt.Errorf("staged upload not found or expired: %w", err)
		}
		h := sha256.Sum256(bytes)
		digest := hex.EncodeToString(h[:])
		srcLink := &pebblestore.VideoSourceLink{
			DigestSHA256: digest,
			MediaRefID:   m.ID,
		}
		return &resolvedSourceMedia{
			Bytes:      bytes,
			MediaType:  mType,
			Provenance: nil, // Uploaded media has no server-verified provenance
			SourceLink: srcLink,
		}, nil
	}

	// Case 5: Data URL or raw base64 data
	if strings.HasPrefix(trimmedURL, "data:") || strings.HasPrefix(m.Data, "data:") || len(m.Data) > 0 {
		bytes, mType, err := s.resolveSourceMediaBytes(ctx, p, m, expectedKind)
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(bytes)
		digest := hex.EncodeToString(h[:])
		srcLink := &pebblestore.VideoSourceLink{
			DigestSHA256: digest,
			MediaRefID:   m.ID,
		}
		return &resolvedSourceMedia{
			Bytes:      bytes,
			MediaType:  mType,
			Provenance: nil,
			SourceLink: srcLink,
		}, nil
	}

	// Case 6: External URL or filesystem path rejection via resolveSourceMediaBytes
	bytes, mType, err := s.resolveSourceMediaBytes(ctx, p, m, expectedKind)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(bytes)
	digest := hex.EncodeToString(h[:])
	return &resolvedSourceMedia{
		Bytes:      bytes,
		MediaType:  mType,
		Provenance: nil,
		SourceLink: &pebblestore.VideoSourceLink{DigestSHA256: digest, MediaRefID: m.ID},
	}, nil
}

// preflightVideoOperation evaluates preflight checks for a video creation, editing, or extension operation.
func (s *Server) preflightVideoOperation(ctx context.Context, req videogen.VideoPreflightRequest) (*videogen.VideoPreflightResult, error) {
	if s == nil {
		return nil, errors.New("server is not configured")
	}
	if pf, ok := s.videoGen.(videoPreflightService); ok {
		return pf.PreflightVideoOperation(ctx, req)
	}
	// Fall back to direct videogen evaluator service constructed with server authority
	svc := videogen.NewService(s.auth, s.uiSettings, s.model)
	return svc.PreflightVideoOperation(ctx, req)
}
