package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/artifact"
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
	if kind == "video" {
		return true
	}
	mt := strings.ToLower(strings.TrimSpace(m.MediaType))
	if strings.HasPrefix(mt, "video/") {
		return true
	}
	fn := strings.ToLower(strings.TrimSpace(m.Filename))
	if strings.HasSuffix(fn, ".mp4") {
		return true
	}
	return false
}

func isImageAttachment(m pebblestore.ProjectTaskMediaRef) bool {
	kind := strings.ToLower(strings.TrimSpace(m.Kind))
	if kind == "image" {
		return true
	}
	mt := strings.ToLower(strings.TrimSpace(m.MediaType))
	if strings.HasPrefix(mt, "image/") {
		return true
	}
	fn := strings.ToLower(strings.TrimSpace(m.Filename))
	if strings.HasSuffix(fn, ".png") || strings.HasSuffix(fn, ".jpg") || strings.HasSuffix(fn, ".jpeg") || strings.HasSuffix(fn, ".webp") {
		return true
	}
	return false
}

// resolveSourceMediaRecord resolves full media payload, media type, and server-verified
// provenance and source link for an attached media reference.
// Client-supplied provider handles, interaction IDs, and timestamps are NEVER trusted;
// provenance is authoritatively loaded from the server's own Pebble stores.
func (s *Server) resolveSourceMediaRecord(ctx context.Context, p identity.Principal, m pebblestore.ProjectTaskMediaRef, expectedKind string) (*resolvedSourceMedia, error) {
	// Case 1: Canonical session artifact URL (/v3/sessions/{sessionID}/artifacts/{variantID})
	if strings.Contains(m.URL, "/v3/sessions/") && strings.Contains(m.URL, "/artifacts/") {
		parts := strings.Split(m.URL, "/v3/sessions/")
		if len(parts) > 1 {
			subParts := strings.Split(parts[1], "/artifacts/")
			if len(subParts) == 2 {
				sessionID := strings.TrimSpace(subParts[0])
				remainder := strings.TrimSpace(subParts[1])
				variantID := remainder
				requestedRev := ""
				if q := strings.Index(remainder, "?"); q >= 0 {
					queryStr := remainder[q+1:]
					variantID = remainder[:q]
					for _, param := range strings.Split(queryStr, "&") {
						kv := strings.SplitN(param, "=", 2)
						if len(kv) == 2 {
							key := strings.ToLower(strings.TrimSpace(kv[0]))
							if key == "event_seq" || key == "eventseq" || key == "rev" || key == "revision" {
								requestedRev = strings.TrimSpace(kv[1])
							}
						}
					}
				}
				if slash := strings.Index(variantID, "/"); slash >= 0 {
					variantID = variantID[:slash]
				}
				if sessionID != "" && variantID != "" && s != nil && s.sessions != nil && s.sessions.Store() != nil {
					variant, ok, err := s.sessions.Store().GetSessionArtifactVariantByID(p.AccountScopeID, sessionID, variantID)
					if err != nil {
						return nil, fmt.Errorf("read artifact variant: %w", err)
					}
					if !ok {
						return nil, fmt.Errorf("session artifact variant %q not found in account scope", variantID)
					}
					if requestedRev != "" {
						curRevStr := fmt.Sprintf("%d", variant.EventSeq)
						if requestedRev != curRevStr {
							return nil, fmt.Errorf("stale artifact revision %q requested (current revision is %s)", requestedRev, curRevStr)
						}
					}
					if s.artifacts != nil {
						authority := artifact.NewAuthority(s.artifacts, s.sessions)
						maxBytes := int64(64 << 20)
						if expectedKind == "video" {
							maxBytes = 512 << 20
						}
						body, _, readErr := authority.ReadReference(ctx, artifact.Principal{
							SessionID:      sessionID,
							AccountScopeID: p.AccountScopeID,
							UserID:         p.UserID,
						}, pebblestore.SessionArtifactSelectionReference{
							SessionID:    variant.SessionID,
							CollectionID: variant.CollectionID,
							VariantID:    variant.ID,
							EventSeq:     variant.EventSeq,
						}, maxBytes)
						if readErr != nil {
							return nil, fmt.Errorf("read artifact reference: %w", readErr)
						}
						if len(body) == 0 {
							return nil, errors.New("artifact reference payload is empty")
						}
						mediaType := variant.MediaType
						if mediaType == "" {
							if expectedKind == "video" {
								mediaType = "video/mp4"
							} else {
								mediaType = "image/png"
							}
						}
						h := sha256.Sum256(body)
						digest := hex.EncodeToString(h[:])
						if variant.DigestSHA256 != "" && !strings.EqualFold(variant.DigestSHA256, digest) {
							return nil, errors.New("artifact body digest mismatch")
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
							Bytes:      body,
							MediaType:  mediaType,
							Provenance: prov,
							SourceLink: srcLink,
						}, nil
					}
				}
			}
		}
	}

	// Case 2: Canonical project deliverable reference (/v3/projects/{projectID}/tasks/{taskID}/deliverables/{delivID})
	if strings.Contains(m.URL, "/v3/projects/") && strings.Contains(m.URL, "/deliverables/") {
		parts := strings.Split(m.URL, "/v3/projects/")
		if len(parts) > 1 {
			sub := parts[1]
			projID := ""
			taskID := ""
			delivID := ""
			if idx := strings.Index(sub, "/tasks/"); idx >= 0 {
				projID = sub[:idx]
				rest := sub[idx+len("/tasks/"):]
				if dIdx := strings.Index(rest, "/deliverables/"); dIdx >= 0 {
					taskID = rest[:dIdx]
					delivID = rest[dIdx+len("/deliverables/"):]
					if end := strings.IndexAny(delivID, "/?#"); end >= 0 {
						delivID = delivID[:end]
					}
				}
			}
			if projID != "" && taskID != "" && delivID != "" && s != nil && s.sessions != nil && s.sessions.Store() != nil {
				task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, projID, taskID)
				if err == nil && found && task != nil {
					for _, d := range task.Deliverables {
						if d.ID == delivID {
							if d.MediaURL != "" {
								delivRef := pebblestore.ProjectTaskMediaRef{
									ID:        d.ID,
									URL:       d.MediaURL,
									MediaType: d.Kind,
									Kind:      expectedKind,
								}
								delivRes, delivErr := s.resolveSourceMediaRecord(ctx, p, delivRef, expectedKind)
								if delivErr == nil && delivRes != nil {
									h := sha256.Sum256(delivRes.Bytes)
									digest := hex.EncodeToString(h[:])
									srcLink := &pebblestore.VideoSourceLink{
										ProjectID:     projID,
										TaskID:        taskID,
										DeliverableID: delivID,
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
									} else if delivRes.Provenance != nil {
										prov = delivRes.Provenance.Clone()
										prov.SourceLink = srcLink
									}
									return &resolvedSourceMedia{
										Bytes:      delivRes.Bytes,
										MediaType:  delivRes.MediaType,
										Provenance: prov,
										SourceLink: srcLink,
									}, nil
								}
							}
						}
					}
				}
			}
		}
	}

	// Case 3: Staging storage lookup (m.ID starts with stg_ or m.URL contains /media/staging/stg_)
	stagingID := ""
	if strings.HasPrefix(m.ID, "stg_") {
		stagingID = m.ID
	} else if idx := strings.Index(m.URL, "/media/staging/stg_"); idx >= 0 {
		sub := m.URL[idx+len("/media/staging/"):]
		if end := strings.IndexAny(sub, "/?#"); end >= 0 {
			stagingID = sub[:end]
		} else {
			stagingID = sub
		}
	}
	if stagingID != "" && s != nil && s.mediaStaging != nil {
		_, payload, readErr := s.mediaStaging.Read(p.AccountScopeID, stagingID, time.Now().UnixMilli())
		if readErr != nil {
			return nil, fmt.Errorf("staged upload %q not found or expired: %w", stagingID, readErr)
		}
		if len(payload) == 0 {
			return nil, fmt.Errorf("staged upload %q is empty", stagingID)
		}
		mediaType := m.MediaType
		detected := http.DetectContentType(payload)
		if detected != "" && !strings.Contains(detected, "octet-stream") {
			mediaType = detected
		}
		if mediaType == "" {
			if expectedKind == "video" {
				mediaType = "video/mp4"
			} else {
				mediaType = "image/png"
			}
		}
		h := sha256.Sum256(payload)
		digest := hex.EncodeToString(h[:])
		srcLink := &pebblestore.VideoSourceLink{
			DigestSHA256: digest,
			MediaRefID:   m.ID,
		}
		return &resolvedSourceMedia{
			Bytes:      payload,
			MediaType:  mediaType,
			Provenance: nil, // Uploaded media has no server-verified provenance
			SourceLink: srcLink,
		}, nil
	}

	// Case 4: Data URL in m.URL or m.Data
	dataURL := ""
	if strings.HasPrefix(m.URL, "data:") {
		dataURL = m.URL
	} else if strings.HasPrefix(m.Data, "data:") {
		dataURL = m.Data
	}
	if dataURL != "" {
		parts := strings.SplitN(dataURL, ",", 2)
		if len(parts) == 2 {
			header := parts[0]
			mediaType := m.MediaType
			if strings.Contains(header, ";base64") {
				if sub := strings.TrimPrefix(header, "data:"); strings.Contains(sub, ";") {
					mediaType = strings.Split(sub, ";")[0]
				}
				decoded, err := base64.StdEncoding.DecodeString(parts[1])
				if err != nil {
					return nil, fmt.Errorf("decode data url: %w", err)
				}
				if len(decoded) == 0 {
					return nil, errors.New("data url payload is empty")
				}
				if mediaType == "" {
					if expectedKind == "video" {
						mediaType = "video/mp4"
					} else {
						mediaType = "image/png"
					}
				}
				h := sha256.Sum256(decoded)
				digest := hex.EncodeToString(h[:])
				srcLink := &pebblestore.VideoSourceLink{
					DigestSHA256: digest,
					MediaRefID:   m.ID,
				}
				return &resolvedSourceMedia{
					Bytes:      decoded,
					MediaType:  mediaType,
					Provenance: nil,
					SourceLink: srcLink,
				}, nil
			}
		}
	}

	// Case 5: Raw base64 in m.Data
	if len(m.Data) > 0 {
		decoded, err := base64.StdEncoding.DecodeString(m.Data)
		if err == nil && len(decoded) > 0 {
			mediaType := m.MediaType
			if mediaType == "" {
				if expectedKind == "video" {
					mediaType = "video/mp4"
				} else {
					mediaType = "image/png"
				}
			}
			h := sha256.Sum256(decoded)
			digest := hex.EncodeToString(h[:])
			srcLink := &pebblestore.VideoSourceLink{
				DigestSHA256: digest,
				MediaRefID:   m.ID,
			}
			return &resolvedSourceMedia{
				Bytes:      decoded,
				MediaType:  mediaType,
				Provenance: nil,
				SourceLink: srcLink,
			}, nil
		}
	}

	// Case 6: Reject arbitrary external HTTP/HTTPS URLs and direct filesystem paths
	urlLower := strings.ToLower(strings.TrimSpace(m.URL))
	if strings.HasPrefix(urlLower, "http://") || strings.HasPrefix(urlLower, "https://") {
		return nil, errors.New("fetching arbitrary external URLs is not permitted")
	}
	if strings.HasPrefix(m.URL, "/") || strings.HasPrefix(m.URL, "./") || strings.HasPrefix(m.URL, "../") || strings.Contains(m.URL, `\`) {
		return nil, errors.New("direct filesystem paths are not permitted")
	}

	return nil, errors.New("no source media bytes available")
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
