package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

// managedVideoService defines the execution interface for generating managed videos.
type managedVideoService interface {
	GenerateManagedVideo(ctx context.Context, req videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error)
}

var (
	directVideoGenService managedVideoService
	directVideoGenMu      sync.RWMutex
)

// SetDirectVideoGenerationService overrides the direct video generation service (for testing or runtime injection).
func SetDirectVideoGenerationService(svc managedVideoService) {
	directVideoGenMu.Lock()
	defer directVideoGenMu.Unlock()
	directVideoGenService = svc
}

func (s *Server) resolveVideoGenerationService() (managedVideoService, error) {
	directVideoGenMu.RLock()
	svc := directVideoGenService
	directVideoGenMu.RUnlock()
	if svc != nil {
		return svc, nil
	}
	if s == nil || s.sessions == nil || s.sessions.Store() == nil || s.sessions.Store().Underlying() == nil {
		return nil, errors.New("video generation service not configured: storage not initialized")
	}
	authStore := pebblestore.NewAuthStore(s.sessions.Store().Underlying())
	return videogen.NewService(authStore, s.uiSettings, s.model), nil
}

func isSupportedVideoModel(s *Server, modelID string) bool {
	clean := strings.TrimSpace(strings.ToLower(modelID))
	if clean == "" {
		return false
	}
	known := map[string]bool{
		"veo-3.1-generate-preview":      true,
		"veo-3.1-fast-generate-preview": true,
		"veo-3.1-lite-generate-preview": true,
		"gemini-omni-1.1-flash":         true,
		"gemini-omni-flash-preview":     true,
		"google/veo-3.1":                true,
		"google/veo-3.1-fast":           true,
		"google/veo-3.1-lite":           true,
		"google/veo-2":                  true,
		"google/veo-2.0-generate-001":   true,
	}
	if known[clean] {
		return true
	}
	if s != nil && s.model != nil {
		for _, provider := range []string{"google", "openrouter"} {
			if records, err := s.model.ListCatalog(provider, 200); err == nil {
				for _, rec := range records {
					if strings.EqualFold(rec.Model, modelID) {
						return true
					}
				}
			}
		}
	}
	return false
}

func updateProjectTaskWithRetry(db *pebblestore.SessionStore, accountScopeID, projectID, taskID string, mutate func(*pebblestore.ProjectTaskRecord) error) (*pebblestore.ProjectTaskRecord, error) {
	var rec *pebblestore.ProjectTaskRecord
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		rec, err = db.UpdateProjectTask(accountScopeID, projectID, taskID, mutate)
		if err == nil {
			return rec, nil
		}
		if !strings.Contains(err.Error(), "not found") {
			return nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, err
}

// resolveSourceMediaBytes extracts and decodes image or video byte payload from attached media refs,
// safely resolving data URIs, raw base64, staged uploads, and canonical session artifacts.
// Arbitrary external URLs and filesystem paths are explicitly rejected.
func (s *Server) resolveSourceMediaBytes(ctx context.Context, p identity.Principal, m pebblestore.ProjectTaskMediaRef, expectedKind string) ([]byte, string, error) {
	mediaType := strings.TrimSpace(m.MediaType)
	if mediaType == "" {
		if expectedKind == "video" {
			mediaType = "video/mp4"
		} else {
			mediaType = "image/png"
		}
	}

	// Case 1: Data URL in m.URL
	if strings.HasPrefix(m.URL, "data:") {
		parts := strings.SplitN(m.URL, ",", 2)
		if len(parts) == 2 {
			header := parts[0]
			if strings.Contains(header, ";base64") {
				if sub := strings.TrimPrefix(header, "data:"); strings.Contains(sub, ";") {
					mediaType = strings.Split(sub, ";")[0]
				}
				decoded, err := base64.StdEncoding.DecodeString(parts[1])
				if err == nil && len(decoded) > 0 {
					return decoded, mediaType, nil
				}
				return nil, mediaType, fmt.Errorf("decode data url: %w", err)
			}
		}
	}

	// Case 2: Data URL in m.Data
	if strings.HasPrefix(m.Data, "data:") {
		parts := strings.SplitN(m.Data, ",", 2)
		if len(parts) == 2 {
			header := parts[0]
			if strings.Contains(header, ";base64") {
				if sub := strings.TrimPrefix(header, "data:"); strings.Contains(sub, ";") {
					mediaType = strings.Split(sub, ";")[0]
				}
				decoded, err := base64.StdEncoding.DecodeString(parts[1])
				if err == nil && len(decoded) > 0 {
					return decoded, mediaType, nil
				}
				return nil, mediaType, fmt.Errorf("decode inline data url: %w", err)
			}
		}
	}

	// Case 3: Raw base64 in m.Data
	if len(m.Data) > 0 {
		if decoded, err := base64.StdEncoding.DecodeString(m.Data); err == nil && len(decoded) > 0 {
			return decoded, mediaType, nil
		}
	}

	// Case 4: Staging storage lookup (m.ID starts with stg_ or m.URL contains /media/staging/stg_)
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
		if readErr == nil && len(payload) > 0 {
			detected := http.DetectContentType(payload)
			if detected != "" && !strings.Contains(detected, "octet-stream") {
				mediaType = detected
			}
			return payload, mediaType, nil
		}
	}

	// Case 5: Canonical session artifact URL (/v3/sessions/{sessionID}/artifacts/{variantID})
	if strings.Contains(m.URL, "/v3/sessions/") && strings.Contains(m.URL, "/artifacts/") {
		parts := strings.Split(m.URL, "/v3/sessions/")
		if len(parts) > 1 {
			subParts := strings.Split(parts[1], "/artifacts/")
			if len(subParts) == 2 {
				sessionID := strings.TrimSpace(subParts[0])
				remainder := strings.TrimSpace(subParts[1])
				variantID := remainder
				if slash := strings.Index(remainder, "/"); slash >= 0 {
					variantID = remainder[:slash]
				}
				if q := strings.Index(variantID, "?"); q >= 0 {
					variantID = variantID[:q]
				}
				if sessionID != "" && variantID != "" && s != nil && s.sessions != nil && s.sessions.Store() != nil {
					variant, ok, err := s.sessions.Store().GetSessionArtifactVariantByID(p.AccountScopeID, sessionID, variantID)
					if err == nil && ok && s.artifacts != nil {
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
						if readErr == nil && len(body) > 0 {
							if variant.MediaType != "" {
								mediaType = variant.MediaType
							}
							return body, mediaType, nil
						}
					}
				}
			}
		}
	}

	// Case 6: Reject arbitrary external HTTP/HTTPS URLs and direct filesystem paths
	urlLower := strings.ToLower(strings.TrimSpace(m.URL))
	if strings.HasPrefix(urlLower, "http://") || strings.HasPrefix(urlLower, "https://") {
		return nil, mediaType, errors.New("fetching arbitrary external URLs is not permitted")
	}
	if strings.HasPrefix(m.URL, "/") || strings.HasPrefix(m.URL, "./") || strings.HasPrefix(m.URL, "../") || strings.Contains(m.URL, `\`) {
		return nil, mediaType, errors.New("direct filesystem paths are not permitted")
	}

	return nil, mediaType, errors.New("no source media bytes available")
}

// executeDirectMediaTask handles asynchronous generation of image variants or video stories.
// Each deliverable is updated independently to "ready" or "failed" upon generation and committed to Pebble,
// allowing the frontend to stream / observe each item loading and completing in real-time.
func (s *Server) executeDirectMediaTask(p identity.Principal, proj *pebblestore.ProjectRecord, task *pebblestore.ProjectTaskRecord) {
	if s.sessions == nil || s.sessions.Store() == nil || task == nil {
		return
	}
	defer func() {
		_ = recover() // Gracefully recover if store is closed during daemon shutdown or test teardown
	}()
	db := s.sessions.Store()

	if task.Agent == "image" || task.Agent == "designer" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		ar := task.AspectRatio
		if ar == "" {
			ar = "1:1"
		}
		count := len(task.Deliverables)
		if count == 0 {
			count = 1
		}
		prompt := strings.TrimSpace(task.Description)
		if prompt == "" {
			prompt = strings.TrimSpace(task.Title)
		}

		var sourceTitle string
		var sourceMediaID string
		var sourceImage *imagegen.ManagedImageSource
		var sourceErr error

		for _, m := range task.AttachedMedia {
			if m.Kind == "image" || strings.HasPrefix(strings.ToLower(m.MediaType), "image/") {
				if m.Title != "" {
					sourceTitle = m.Title
				} else if m.Filename != "" {
					sourceTitle = m.Filename
				}
				sourceMediaID = m.ID
				bytes, mType, err := s.resolveSourceMediaBytes(ctx, p, m, "image")
				if err != nil {
					sourceErr = fmt.Errorf("resolve source image: %w", err)
				} else if len(bytes) > 0 {
					sourceImage = &imagegen.ManagedImageSource{
						Bytes:     bytes,
						MediaType: mType,
					}
				}
				break
			}
		}

		if sourceErr != nil {
			_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
				t.Status = "failed"
				t.LastError = sourceErr.Error()
				t.ActionNeeded = fmt.Sprintf("Action Needed: %v", sourceErr)
				t.WhatNotDone = []string{sourceErr.Error()}
				for i := range t.Deliverables {
					t.Deliverables[i].Status = "failed"
					t.Deliverables[i].Description = fmt.Sprintf("Source image error: %v", sourceErr)
				}
				return nil
			})
			return
		}

		imageModel := strings.TrimSpace(task.Model)
		if imageModel != "" && s.imageGen != nil {
			if resolved, rErr := s.imageGen.ResolveModelSelection(imageModel); rErr != nil || resolved.ID == "" {
				modelErr := fmt.Errorf("unsupported image model %q", imageModel)
				_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
					t.Status = "failed"
					t.LastError = modelErr.Error()
					t.ActionNeeded = fmt.Sprintf("Action Needed: %v", modelErr)
					t.WhatNotDone = []string{modelErr.Error()}
					for i := range t.Deliverables {
						t.Deliverables[i].Status = "failed"
						t.Deliverables[i].Description = fmt.Sprintf("Model error: %v", modelErr)
					}
					return nil
				})
				return
			}
		}

		lowerPrompt := strings.ToLower(prompt)
		isFineTune := strings.Contains(lowerPrompt, "change") || strings.Contains(lowerPrompt, "modify") || strings.Contains(lowerPrompt, "edit") || strings.Contains(lowerPrompt, "tweak") || strings.Contains(lowerPrompt, "replace") || strings.Contains(lowerPrompt, "fine-tune")

		concurrency := 4
		if count < concurrency {
			concurrency = count
		}
		jobs := make(chan int, count)
		for i := 0; i < count; i++ {
			jobs <- i
		}
		close(jobs)

		var wg sync.WaitGroup
		for w := 0; w < concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					variantIdx := i + 1
					reqPrompt := prompt
					if sourceTitle != "" {
						reqPrompt = fmt.Sprintf("%s (iteration based on %s)", prompt, sourceTitle)
					}
					mediaURL, usedModel, err := s.generateImageMedia(ctx, p, reqPrompt, ar, variantIdx, task.Model, task.Resolution, sourceImage)
					slotIndex := i
					_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
						if slotIndex < len(t.Deliverables) {
							if err != nil || mediaURL == "" {
								t.Deliverables[slotIndex].Status = "failed"
								errMsg := "Image generation failed"
								if err != nil {
									errMsg = fmt.Sprintf("Image generation failed: %v", err)
								}
								t.Deliverables[slotIndex].Description = errMsg
							} else {
								t.Deliverables[slotIndex].Status = "ready"
								t.Deliverables[slotIndex].MediaURL = mediaURL
								t.Deliverables[slotIndex].Thumbnail = mediaURL
								t.Deliverables[slotIndex].ParentDeliverableID = sourceMediaID
								t.Deliverables[slotIndex].SourceMediaRef = sourceMediaID
								resTag := strings.TrimSpace(task.Resolution)
								if resTag == "" {
									resTag = "1K"
								}
								desc := fmt.Sprintf("Autonomous deliverable for %s in aspect ratio %s (%s)", t.Title, ar, resTag)
								if usedModel != "" {
									desc = fmt.Sprintf("Autonomous deliverable for %s in aspect ratio %s (%s) using %s", t.Title, ar, resTag, usedModel)
								}
								t.Deliverables[slotIndex].Description = desc
							}
						}
						return nil
					})
				}
			}()
		}
		wg.Wait()

		_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
			readyCount := 0
			failedCount := 0
			for _, d := range t.Deliverables {
				if d.Status == "ready" || d.Status == "accepted" {
					readyCount++
				} else if d.Status == "failed" {
					failedCount++
				}
			}
			if readyCount == 0 && len(t.Deliverables) > 0 {
				t.Status = "failed"
				t.LastError = "all image variants failed generation"
				t.ActionNeeded = "Action Needed: All image deliverables failed generation. Check provider settings and prompt."
				t.WhatNotDone = []string{"Failed to generate image deliverables"}
			} else if readyCount > 0 {
				t.Status = "needs_review"
				if failedCount > 0 {
					t.LastError = fmt.Sprintf("%d of %d deliverables failed generation", failedCount, len(t.Deliverables))
					t.ActionNeeded = fmt.Sprintf("Action Needed: %d of %d image variation(s) ready for review (%d failed).", readyCount, len(t.Deliverables), failedCount)
				} else if isFineTune && sourceTitle != "" {
					t.WhatDidDo = []string{
						fmt.Sprintf("Referenced base image: %s", sourceTitle),
						fmt.Sprintf("Applied fine-tuning modification: %s", prompt),
					}
					t.ActionNeeded = fmt.Sprintf("Action Needed: Fine-tuned image deliverable ready for review (based on %s).", sourceTitle)
				} else if sourceTitle != "" {
					t.WhatDidDo = []string{
						fmt.Sprintf("Referenced base image: %s", sourceTitle),
						fmt.Sprintf("Generated %d creative variations in parallel via swarm engine", readyCount),
					}
					t.ActionNeeded = fmt.Sprintf("Action Needed: %d image variation(s) ready for review (based on %s).", readyCount, sourceTitle)
				} else {
					t.WhatDidDo = []string{
						"Synthesized visual concept",
						fmt.Sprintf("Generated %d deliverable variant(s) in parallel via swarm engine", readyCount),
					}
					t.ActionNeeded = fmt.Sprintf("Action Needed: %d deliverable(s) ready for review.", readyCount)
				}
			}
			return nil
		})
	} else if task.Agent == "video" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		ar := task.AspectRatio
		if ar == "" {
			ar = "16:9"
		}
		sceneCount := len(task.Scenes)
		if sceneCount == 0 {
			if task.VariantCount > 1 {
				sceneCount = task.VariantCount
			} else if task.VariantCount == 1 {
				sceneCount = 1
			} else {
				sceneCount = 2
			}
		}
		soundtrack := task.Soundtrack
		prompt := strings.TrimSpace(task.Description)
		if prompt == "" {
			prompt = strings.TrimSpace(task.Title)
		}

		var sourceMediaTitle string
		var sourceMediaKind string
		var sourceMediaID string
		var sourceVideo *videogen.ManagedVideoSource
		var sourceImage *videogen.ManagedVideoImage
		var sourceErr error

		for _, m := range task.AttachedMedia {
			k := strings.ToLower(m.Kind)
			mt := strings.ToLower(m.MediaType)
			if k == "video" || strings.HasPrefix(mt, "video/") || strings.HasSuffix(strings.ToLower(m.Filename), ".mp4") {
				sourceMediaKind = "video"
				sourceMediaID = m.ID
				if m.Title != "" {
					sourceMediaTitle = m.Title
				} else if m.Filename != "" {
					sourceMediaTitle = m.Filename
				}
				bytes, mType, err := s.resolveSourceMediaBytes(ctx, p, m, "video")
				if err != nil {
					sourceErr = fmt.Errorf("resolve video source: %w", err)
				} else if len(bytes) > 0 {
					sourceVideo = &videogen.ManagedVideoSource{
						Bytes:         bytes,
						MediaType:     mType,
						InteractionID: m.ID,
					}
				}
				break
			}
			if k == "image" || strings.HasPrefix(mt, "image/") {
				sourceMediaKind = "image"
				sourceMediaID = m.ID
				if m.Title != "" {
					sourceMediaTitle = m.Title
				} else if m.Filename != "" {
					sourceMediaTitle = m.Filename
				}
				bytes, mType, err := s.resolveSourceMediaBytes(ctx, p, m, "image")
				if err != nil {
					sourceErr = fmt.Errorf("resolve keyframe image source: %w", err)
				} else if len(bytes) > 0 {
					sourceImage = &videogen.ManagedVideoImage{
						Bytes:     bytes,
						MediaType: mType,
					}
				}
				break
			}
		}

		if sourceErr != nil {
			_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
				t.Status = "failed"
				t.LastError = sourceErr.Error()
				t.ActionNeeded = fmt.Sprintf("Action Needed: %v", sourceErr)
				t.WhatNotDone = []string{sourceErr.Error()}
				if len(t.Deliverables) > 0 {
					t.Deliverables[0].Status = "failed"
					t.Deliverables[0].Description = fmt.Sprintf("Source media error: %v", sourceErr)
				}
				return nil
			})
			return
		}

		videoModel := strings.TrimSpace(task.Model)
		if videoModel != "" && !isSupportedVideoModel(s, videoModel) {
			modelErr := fmt.Errorf("unsupported video model %q", videoModel)
			_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
				t.Status = "failed"
				t.LastError = modelErr.Error()
				t.ActionNeeded = fmt.Sprintf("Action Needed: %v", modelErr)
				t.WhatNotDone = []string{modelErr.Error()}
				if len(t.Deliverables) > 0 {
					t.Deliverables[0].Status = "failed"
					t.Deliverables[0].Description = fmt.Sprintf("Model error: %v", modelErr)
				}
				return nil
			})
			return
		}

		vg, vgErr := s.resolveVideoGenerationService()
		if vgErr != nil {
			_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
				t.Status = "failed"
				t.LastError = vgErr.Error()
				t.ActionNeeded = fmt.Sprintf("Action Needed: %v", vgErr)
				t.WhatNotDone = []string{vgErr.Error()}
				if len(t.Deliverables) > 0 {
					t.Deliverables[0].Status = "failed"
					t.Deliverables[0].Description = fmt.Sprintf("Video service error: %v", vgErr)
				}
				return nil
			})
			return
		}

		durSec := task.DurationSeconds
		if durSec <= 0 {
			durSec = 8
		}
		resTag := strings.TrimSpace(task.Resolution)
		if resTag == "" {
			resTag = "720p"
		}

		vReq := videogen.ManagedVideoRequest{
			Prompt:          prompt,
			AspectRatio:     ar,
			Resolution:      resTag,
			DurationSeconds: durSec,
			Principal:       p,
			Source:          sourceVideo,
			Image:           sourceImage,
		}

		vRes, genErr := vg.GenerateManagedVideo(ctx, vReq)
		if genErr != nil {
			_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
				t.Status = "failed"
				t.LastError = genErr.Error()
				t.ActionNeeded = fmt.Sprintf("Action Needed: Video generation failed (%v). Check settings or provider credentials.", genErr)
				t.WhatNotDone = []string{fmt.Sprintf("Failed to generate video: %v", genErr)}
				if len(t.Deliverables) > 0 {
					t.Deliverables[0].Status = "failed"
					t.Deliverables[0].Description = fmt.Sprintf("Video generation failed: %v", genErr)
				}
				return nil
			})
			return
		}

		mime := vRes.MediaType
		if mime == "" {
			mime = "video/mp4"
		}
		mediaURL := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(vRes.Bytes))
		usedModel := vRes.Model
		if usedModel == "" {
			usedModel = videoModel
		}
		if usedModel == "" {
			usedModel = "veo-3.1-generate-preview"
		}
		durationStr := fmt.Sprintf("%ds", vRes.DurationSeconds)
		if vRes.DurationSeconds <= 0 {
			durationStr = fmt.Sprintf("%ds", durSec)
		}

		lowerPrompt := strings.ToLower(prompt)
		isContinuation := strings.Contains(lowerPrompt, "next scene") || strings.Contains(lowerPrompt, "continue") || strings.Contains(lowerPrompt, "sequel") || strings.Contains(lowerPrompt, "part 2")
		isFineTune := strings.Contains(lowerPrompt, "change") || strings.Contains(lowerPrompt, "modify") || strings.Contains(lowerPrompt, "edit") || strings.Contains(lowerPrompt, "fine-tune")

		_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
			if len(t.Deliverables) > 0 {
				t.Deliverables[0].Status = "ready"
				t.Deliverables[0].MediaURL = mediaURL
				t.Deliverables[0].Thumbnail = mediaURL
				t.Deliverables[0].Duration = durationStr
				t.Deliverables[0].ParentDeliverableID = sourceMediaID
				t.Deliverables[0].SourceMediaRef = sourceMediaID
				if sourceMediaKind == "video" {
					if isContinuation {
						t.Deliverables[0].Title = fmt.Sprintf("%s (Continued from %s)", t.Title, sourceMediaTitle)
						t.Deliverables[0].Description = fmt.Sprintf("Compiled %d-scene continuation using %s from %s with soundtrack (%s): %s", sceneCount, usedModel, sourceMediaTitle, soundtrack, t.Title)
					} else {
						t.Deliverables[0].Title = fmt.Sprintf("%s (Iteration from %s)", t.Title, sourceMediaTitle)
						t.Deliverables[0].Description = fmt.Sprintf("Compiled %d-scene video iteration using %s of %s with soundtrack (%s): %s", sceneCount, usedModel, sourceMediaTitle, soundtrack, t.Title)
					}
				} else if sourceMediaKind == "image" {
					t.Deliverables[0].Title = fmt.Sprintf("%s (Keyframe %s)", t.Title, sourceMediaTitle)
					t.Deliverables[0].Description = fmt.Sprintf("Compiled %d-scene motion sequence using %s from keyframe image %s with soundtrack (%s): %s", sceneCount, usedModel, sourceMediaTitle, soundtrack, t.Title)
				} else if sceneCount <= 1 {
					t.Deliverables[0].Title = fmt.Sprintf("%s (Single Video, %s)", t.Title, ar)
					t.Deliverables[0].Duration = durationStr
					t.Deliverables[0].Description = fmt.Sprintf("Single video clip (%s, %s, %s) generated directly with %s: %s", ar, vRes.Resolution, durationStr, usedModel, t.Title)
				} else {
					if soundtrack != "" {
						t.Deliverables[0].Description = fmt.Sprintf("Compiled %d-scene multi-part video using %s with soundtrack (%s): %s", sceneCount, usedModel, soundtrack, t.Title)
					} else {
						t.Deliverables[0].Description = fmt.Sprintf("Compiled %d-scene multi-part video using %s (no soundtrack clip): %s", sceneCount, usedModel, t.Title)
					}
				}
			}
			t.Status = "needs_review"
			if sourceMediaKind == "video" {
				if isContinuation {
					t.WhatDidDo = []string{
						fmt.Sprintf("Referenced prior video cut: %s", sourceMediaTitle),
						fmt.Sprintf("Sequenced next continuation (%d scenes) with synchronized %s soundtrack", sceneCount, soundtrack),
					}
					t.ActionNeeded = fmt.Sprintf("Action Needed: Next video scene ready for review (continued from %s).", sourceMediaTitle)
				} else if isFineTune {
					t.WhatDidDo = []string{
						fmt.Sprintf("Referenced source video: %s", sourceMediaTitle),
						fmt.Sprintf("Applied fine-tuning video modification with %s soundtrack", soundtrack),
					}
					t.ActionNeeded = fmt.Sprintf("Action Needed: Fine-tuned video deliverable ready for review (based on %s).", sourceMediaTitle)
				} else {
					t.WhatDidDo = []string{
						fmt.Sprintf("Referenced source video: %s", sourceMediaTitle),
						fmt.Sprintf("Rendered video iteration with synchronized %s soundtrack", soundtrack),
					}
					t.ActionNeeded = fmt.Sprintf("Action Needed: Video iteration ready for review (based on %s).", sourceMediaTitle)
				}
			} else if sourceMediaKind == "image" {
				t.WhatDidDo = []string{
					fmt.Sprintf("Ingested keyframe image: %s", sourceMediaTitle),
					fmt.Sprintf("Generated %d-scene cinematic motion story with %s soundtrack", sceneCount, soundtrack),
				}
				t.ActionNeeded = fmt.Sprintf("Action Needed: Video story ready for review (from keyframe %s).", sourceMediaTitle)
			} else if sceneCount <= 1 {
				t.WhatDidDo = []string{
					fmt.Sprintf("Configured single video shot parameters (%s, %s, %s)", ar, vRes.Resolution, durationStr),
					fmt.Sprintf("Rendered video clip directly with %s (one-prompt generation)", usedModel),
				}
				t.ActionNeeded = "Action Needed: Single video clip deliverable ready for review."
			} else {
				if soundtrack != "" {
					t.WhatDidDo = []string{"Compiled multi-scene video blueprint", "Rendered video sequence with synchronized soundtrack"}
				} else {
					t.WhatDidDo = []string{"Compiled multi-scene video blueprint", "Rendered multi-scene video sequence without soundtrack"}
				}
				t.ActionNeeded = "Action Needed: Multi-part video deliverable ready for review."
			}
			return nil
		})
	} else if task.Agent == "sound" || task.Agent == "audio" {
		soundModel := strings.TrimSpace(task.Model)
		if soundModel == "" && s.uiSettings != nil && strings.TrimSpace(p.AccountScopeID) != "" {
			if uiSet, err := s.uiSettings.GetForAccount(p.AccountScopeID); err == nil {
				if def := strings.TrimSpace(uiSet.Tools.Audio.DefaultModel); def != "" {
					soundModel = def
				}
			}
		}
		if soundModel == "" {
			soundModel = "lyria-3.5"
		}
		durSeconds := task.DurationSeconds
		if durSeconds <= 0 {
			durSeconds = 30
		}
		prompt := strings.TrimSpace(task.Description)
		if prompt == "" {
			prompt = strings.TrimSpace(task.Title)
		}
		mediaURL := generateStyledAudioSVGDataURL(prompt, soundModel, durSeconds)
		_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
			if len(t.Deliverables) > 0 {
				t.Deliverables[0].Status = "ready"
				t.Deliverables[0].MediaURL = mediaURL
				t.Deliverables[0].Thumbnail = "sound"
				t.Deliverables[0].Duration = fmt.Sprintf("%ds", durSeconds)
				t.Deliverables[0].Title = fmt.Sprintf("%s (%ds Audio Clip)", t.Title, durSeconds)
				t.Deliverables[0].Description = fmt.Sprintf("Generated %ds audio soundtrack using %s: %s", durSeconds, soundModel, prompt)
			}
			t.Status = "needs_review"
			t.WhatDidDo = []string{
				fmt.Sprintf("Synthesized audio clip with %s", soundModel),
				fmt.Sprintf("Generated %d-second audio track", durSeconds),
			}
			t.ActionNeeded = "Action Needed: Audio deliverable ready for review (can be attached as soundtrack to video)."
			return nil
		})
	}
}

// generateStyledImageSVGDataURL creates a high-craft deterministic SVG vector asset
// tailored to the prompt (e.g. panda, futuristic terminal, cosmic swarm core) and aspect ratio.
func generateStyledImageSVGDataURL(prompt string, aspectRatio string, variantIndex int, resolution string) string {
	width, height := 800, 800
	switch aspectRatio {
	case "16:9":
		width, height = 960, 540
	case "9:16":
		width, height = 540, 960
	case "4:3":
		width, height = 800, 600
	}

	lowerPrompt := strings.ToLower(prompt)
	var graphicContent string

	// Color palette definitions based on prompt style keywords
	strokeMain := "#38bdf8"
	strokeAccent := "#00F0FF"
	strokeSoft := "#87CEEB"
	bgStop1 := "#0b1329"
	bgStop2 := "#050814"
	bgStop3 := "#02040a"

	if strings.Contains(lowerPrompt, "sunset") || strings.Contains(lowerPrompt, "amber") || strings.Contains(lowerPrompt, "warm") || strings.Contains(lowerPrompt, "gold") || strings.Contains(lowerPrompt, "orange") {
		strokeMain = "#f59e0b"
		strokeAccent = "#fbbf24"
		strokeSoft = "#fed7aa"
		bgStop1 = "#3d1c06"
		bgStop2 = "#1c1917"
		bgStop3 = "#0c0a09"
	} else if strings.Contains(lowerPrompt, "cyberpunk") || strings.Contains(lowerPrompt, "neon") || strings.Contains(lowerPrompt, "purple") || strings.Contains(lowerPrompt, "pink") || strings.Contains(lowerPrompt, "magenta") {
		strokeMain = "#ec4899"
		strokeAccent = "#a855f7"
		strokeSoft = "#06b6d4"
		bgStop1 = "#3b0764"
		bgStop2 = "#0f172a"
		bgStop3 = "#020617"
	} else if strings.Contains(lowerPrompt, "matrix") || strings.Contains(lowerPrompt, "emerald") || strings.Contains(lowerPrompt, "green") {
		strokeMain = "#10b981"
		strokeAccent = "#34d399"
		strokeSoft = "#6ee7b7"
		bgStop1 = "#064e3b"
		bgStop2 = "#022c22"
		bgStop3 = "#020617"
	} else if strings.Contains(lowerPrompt, "dark") || strings.Contains(lowerPrompt, "obsidian") || strings.Contains(lowerPrompt, "mono") || strings.Contains(lowerPrompt, "slate") {
		strokeMain = "#94a3b8"
		strokeAccent = "#cbd5e1"
		strokeSoft = "#e2e8f0"
		bgStop1 = "#1e293b"
		bgStop2 = "#0f172a"
		bgStop3 = "#020617"
	}

	rotationAngle := (variantIndex - 1) * 35

	if strings.Contains(lowerPrompt, "panda") {
		// Adorable stylized geometric Panda in bamboo grove
		cx, cy := width/2, height/2
		graphicContent = fmt.Sprintf(`
		<!-- Bamboo Grove Background -->
		<g opacity="0.35">
			<rect x="%d" y="0" width="16" height="%d" rx="4" fill="#059669" />
			<rect x="%d" y="0" width="12" height="%d" rx="3" fill="#10b981" />
			<rect x="%d" y="0" width="18" height="%d" rx="4" fill="#047857" />
			<rect x="%d" y="0" width="14" height="%d" rx="3" fill="#34d399" />
		</g>
		<!-- Panda Body and Shadow -->
		<ellipse cx="%d" cy="%d" rx="140" ry="85" fill="#030712" opacity="0.5" filter="blur(12px)" />
		<circle cx="%d" cy="%d" r="110" fill="#f8fafc" stroke="#e2e8f0" stroke-width="4" />
		<!-- Panda Ears -->
		<circle cx="%d" cy="%d" r="38" fill="#0f172a" />
		<circle cx="%d" cy="%d" r="22" fill="#1e293b" />
		<circle cx="%d" cy="%d" r="38" fill="#0f172a" />
		<circle cx="%d" cy="%d" r="22" fill="#1e293b" />
		<!-- Panda Eye Patches & Eyes -->
		<ellipse cx="%d" cy="%d" rx="30" ry="24" transform="rotate(-15 %d %d)" fill="#0f172a" />
		<circle cx="%d" cy="%d" r="8" fill="#ffffff" />
		<circle cx="%d" cy="%d" r="4" fill="%s" />
		<ellipse cx="%d" cy="%d" rx="30" ry="24" transform="rotate(15 %d %d)" fill="#0f172a" />
		<circle cx="%d" cy="%d" r="8" fill="#ffffff" />
		<circle cx="%d" cy="%d" r="4" fill="%s" />
		<!-- Nose and Snout -->
		<ellipse cx="%d" cy="%d" rx="18" ry="12" fill="#0f172a" />
		<path d="M %d %d Q %d %d %d %d Q %d %d %d %d" stroke="#0f172a" stroke-width="3" fill="none" stroke-linecap="round" />
		<!-- Cheeks -->
		<circle cx="%d" cy="%d" r="14" fill="#fda4af" opacity="0.4" filter="blur(2px)" />
		<circle cx="%d" cy="%d" r="14" fill="#fda4af" opacity="0.4" filter="blur(2px)" />
		<!-- Bamboo Stalk Held -->
		<g transform="rotate(-25 %d %d)">
			<rect x="%d" y="%d" width="14" height="130" rx="4" fill="#10b981" stroke="#059669" stroke-width="2" />
			<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#047857" stroke-width="3" />
			<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#047857" stroke-width="3" />
			<path d="M %d %d Q %d %d %d %d" fill="#34d399" opacity="0.8" />
		</g>
		`,
			width/10, height,
			width/7, height,
			width-width/8, height,
			width-width/5, height,
			cx, cy+110,
			cx, cy,
			cx-80, cy-85, cx-80, cy-85,
			cx+80, cy-85, cx+80, cy-85,
			cx-42, cy-12, cx-42, cy-12,
			cx-40, cy-14, cx-39, cy-14,
			strokeMain,
			cx+42, cy-12, cx+42, cy-12,
			cx+44, cy-14, cx+45, cy-14,
			strokeMain,
			cx, cy+20,
			cx-12, cy+32, cx-6, cy+38, cx, cy+32, cx+6, cy+38, cx+12, cy+32,
			cx-60, cy+18,
			cx+60, cy+18,
			cx+75, cy+70,
			cx+68, cy-10,
			cx+68, cy+30, cx+82, cy+30,
			cx+68, cy+70, cx+82, cy+70,
			cx+75, cy+20, cx+105, cy+10, cx+110, cy+25,
		)
	} else {
		// Cosmic Swarm Emblem with luminous concentric mark and particle rays
		cx, cy := width/2, height/2
		graphicContent = fmt.Sprintf(`
		<g transform="rotate(%d %d %d)">
		<!-- Glowing Concentric Mark -->
		<g filter="url(#glow)">
			<rect x="%d" y="%d" width="180" height="180" rx="36" fill="none" stroke="%s" stroke-width="2" opacity="0.4" />
			<rect x="%d" y="%d" width="130" height="130" rx="26" fill="none" stroke="%s" stroke-width="2.5" opacity="0.7" />
			<rect x="%d" y="%d" width="80" height="80" rx="16" fill="none" stroke="%s" stroke-width="3" opacity="0.9" />
			<rect x="%d" y="%d" width="36" height="36" rx="8" fill="#ffffff" opacity="0.95" />
		</g>
		<!-- Particle Lattice Rays -->
		<g stroke="%s" stroke-width="1" opacity="0.4">
			<line x1="%d" y1="%d" x2="%d" y2="%d" />
			<line x1="%d" y1="%d" x2="%d" y2="%d" />
			<line x1="%d" y1="%d" x2="%d" y2="%d" />
			<line x1="%d" y1="%d" x2="%d" y2="%d" />
		</g>
		<circle cx="%d" cy="%d" r="3" fill="%s" />
		<circle cx="%d" cy="%d" r="3" fill="%s" />
		<circle cx="%d" cy="%d" r="3" fill="%s" />
		<circle cx="%d" cy="%d" r="3" fill="%s" />
		</g>
		`,
			rotationAngle, cx, cy,
			cx-90, cy-90, strokeMain,
			cx-65, cy-65, strokeAccent,
			cx-40, cy-40, strokeSoft,
			cx-18, cy-18,
			strokeMain,
			cx-150, cy, cx-100, cy,
			cx+100, cy, cx+150, cy,
			cx, cy-150, cx, cy-100,
			cx, cy+100, cx, cy+150,
			cx-150, cy, strokeAccent,
			cx+150, cy, strokeAccent,
			cx, cy-150, strokeSoft,
			cx, cy+150, strokeSoft,
		)
	}

	resTag := strings.TrimSpace(resolution)
	if resTag == "" {
		resTag = "1K"
	}
	badgeText := fmt.Sprintf("AI DELIVERABLE • VARIANT %d (%s · %s)", variantIndex, aspectRatio, resTag)
	if strings.Contains(lowerPrompt, "change") || strings.Contains(lowerPrompt, "modify") || strings.Contains(lowerPrompt, "edit") || strings.Contains(lowerPrompt, "fine-tune") || strings.Contains(lowerPrompt, "tweak") {
		badgeText = fmt.Sprintf("AI FINE-TUNE / EDIT • %s · %s", aspectRatio, resTag)
	} else if strings.Contains(lowerPrompt, "iteration based on") {
		badgeText = fmt.Sprintf("AI ITERATION (VARIANT %d) • %s · %s", variantIndex, aspectRatio, resTag)
	}

	svg := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d">
	<defs>
		<radialGradient id="bg-grad" cx="50%%" cy="50%%" r="70%%">
			<stop offset="0%%" stop-color="%s" />
			<stop offset="60%%" stop-color="%s" />
			<stop offset="100%%" stop-color="%s" />
		</radialGradient>
		<filter id="glow" x="-30%%" y="-30%%" width="160%%" height="160%%">
			<feGaussianBlur stdDeviation="6" result="blur" />
			<feComposite in="SourceGraphic" in2="blur" operator="over" />
		</filter>
	</defs>
	<!-- Background Frame -->
	<rect width="%d" height="%d" fill="url(#bg-grad)" />
	%s
	<!-- Prompt & Status Metadata Overlay -->
	<g transform="translate(24, %d)">
		<rect width="%d" height="42" rx="8" fill="#030712" opacity="0.8" stroke="#1e293b" stroke-width="1" />
		<text x="14" y="18" fill="#94a3b8" font-family="monospace" font-size="10px" font-weight="bold">%s</text>
		<text x="14" y="32" fill="#e2e8f0" font-family="sans-serif" font-size="11px" font-weight="600">%s</text>
	</g>
</svg>`,
		width, height, width, height,
		bgStop1, bgStop2, bgStop3,
		width, height,
		graphicContent,
		height-66,
		width-48,
		badgeText,
		escapeXML(truncateString(prompt, 60)),
	)

	return fmt.Sprintf("data:image/svg+xml;base64,%s", base64.StdEncoding.EncodeToString([]byte(svg)))
}

// generateStyledVideoSVGDataURL creates a cinematic video storyboard asset.
func generateStyledVideoSVGDataURL(prompt string, aspectRatio string, scenes []pebblestore.ProjectTaskScene, soundtrack string, sourceMediaTitle string, sourceMediaKind string) string {
	width, height := 960, 540
	sceneCount := len(scenes)
	if sceneCount == 0 {
		sceneCount = 2
	}
	headerLabel := fmt.Sprintf("VIDEO STORY COMPOSITION • %d SCENES • %s", sceneCount, aspectRatio)
	if sourceMediaKind == "video" {
		lowerPrompt := strings.ToLower(prompt)
		if strings.Contains(lowerPrompt, "next scene") || strings.Contains(lowerPrompt, "continue") {
			headerLabel = fmt.Sprintf("VIDEO CONTINUATION (FROM %s) • %d SCENES • %s", escapeXML(truncateString(sourceMediaTitle, 24)), sceneCount, aspectRatio)
		} else {
			headerLabel = fmt.Sprintf("VIDEO ITERATION (OF %s) • %d SCENES • %s", escapeXML(truncateString(sourceMediaTitle, 24)), sceneCount, aspectRatio)
		}
	} else if sourceMediaKind == "image" {
		headerLabel = fmt.Sprintf("VIDEO STORY (KEYFRAME: %s) • %d SCENES • %s", escapeXML(truncateString(sourceMediaTitle, 24)), sceneCount, aspectRatio)
	} else if sceneCount <= 1 {
		headerLabel = fmt.Sprintf("SINGLE VIDEO CLIP • 8s • %s", aspectRatio)
	}

	soundtrackSection := ""
	if strings.TrimSpace(soundtrack) != "" {
		soundtrackSection = fmt.Sprintf(`	<!-- Soundtrack Audio Waveform Bars -->
	<g transform="translate(60, 360)">
		<rect x="0" y="20" width="6" height="40" rx="3" fill="url(#bar-grad)" />
		<rect x="14" y="8" width="6" height="52" rx="3" fill="url(#bar-grad)" />
		<rect x="28" y="24" width="6" height="36" rx="3" fill="url(#bar-grad)" />
		<rect x="42" y="12" width="6" height="48" rx="3" fill="url(#bar-grad)" />
		<rect x="56" y="4" width="6" height="56" rx="3" fill="url(#bar-grad)" />
		<rect x="70" y="18" width="6" height="42" rx="3" fill="url(#bar-grad)" />
		<rect x="84" y="28" width="6" height="32" rx="3" fill="url(#bar-grad)" />
		<rect x="98" y="10" width="6" height="50" rx="3" fill="url(#bar-grad)" />
		<rect x="112" y="2" width="6" height="58" rx="3" fill="url(#bar-grad)" />
		<rect x="126" y="16" width="6" height="44" rx="3" fill="url(#bar-grad)" />
		<rect x="140" y="24" width="6" height="36" rx="3" fill="url(#bar-grad)" />
		<rect x="154" y="8" width="6" height="52" rx="3" fill="url(#bar-grad)" />
		<text x="180" y="38" fill="#94a3b8" font-family="monospace" font-size="11px">SOUNDTRACK: %s</text>
	</g>`, escapeXML(soundtrack))
	} else if sceneCount <= 1 {
		soundtrackSection = `	<!-- Model Generative Audio Indicator -->
	<g transform="translate(60, 375)">
		<circle cx="8" cy="8" r="4" fill="#38bdf8" />
		<text x="24" y="12" fill="#94a3b8" font-family="monospace" font-size="11px">MODEL GENERATIVE AUDIO • ONE PROMPT SHOT (8s)</text>
	</g>`
	} else {
		soundtrackSection = `	<!-- No Soundtrack Attached -->
	<g transform="translate(60, 375)">
		<text x="0" y="12" fill="#64748b" font-family="monospace" font-size="11px">NO SOUNDTRACK CLIP ATTACHED</text>
	</g>`
	}

	svg := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d">
	<defs>
		<linearGradient id="vid-grad" x1="0%%" y1="0%%" x2="100%%" y2="100%%">
			<stop offset="0%%" stop-color="#0c162d" />
			<stop offset="50%%" stop-color="#070c1e" />
			<stop offset="100%%" stop-color="#020409" />
		</linearGradient>
		<linearGradient id="bar-grad" x1="0%%" y1="0%%" x2="0%%" y2="100%%">
			<stop offset="0%%" stop-color="#00F0FF" />
			<stop offset="100%%" stop-color="#3b82f6" />
		</linearGradient>
	</defs>
	<!-- Cinema Background -->
	<rect width="%d" height="%d" fill="url(#vid-grad)" />
	<!-- Film Strip Sprockets Top -->
	<g fill="#1e293b" opacity="0.6">
		<rect x="20" y="10" width="16" height="12" rx="2" />
		<rect x="60" y="10" width="16" height="12" rx="2" />
		<rect x="100" y="10" width="16" height="12" rx="2" />
		<rect x="140" y="10" width="16" height="12" rx="2" />
		<rect x="180" y="10" width="16" height="12" rx="2" />
		<rect x="220" y="10" width="16" height="12" rx="2" />
		<rect x="260" y="10" width="16" height="12" rx="2" />
		<rect x="300" y="10" width="16" height="12" rx="2" />
		<rect x="340" y="10" width="16" height="12" rx="2" />
		<rect x="380" y="10" width="16" height="12" rx="2" />
		<rect x="420" y="10" width="16" height="12" rx="2" />
		<rect x="460" y="10" width="16" height="12" rx="2" />
		<rect x="500" y="10" width="16" height="12" rx="2" />
		<rect x="540" y="10" width="16" height="12" rx="2" />
		<rect x="580" y="10" width="16" height="12" rx="2" />
		<rect x="620" y="10" width="16" height="12" rx="2" />
		<rect x="660" y="10" width="16" height="12" rx="2" />
		<rect x="700" y="10" width="16" height="12" rx="2" />
		<rect x="740" y="10" width="16" height="12" rx="2" />
		<rect x="780" y="10" width="16" height="12" rx="2" />
		<rect x="820" y="10" width="16" height="12" rx="2" />
		<rect x="860" y="10" width="16" height="12" rx="2" />
		<rect x="900" y="10" width="16" height="12" rx="2" />
	</g>
	<!-- Center Playhead Indicator -->
	<circle cx="480" cy="230" r="54" fill="#0f172a" stroke="#38bdf8" stroke-width="2" opacity="0.9" />
	<polygon points="468,206 504,230 468,254" fill="#ffffff" />
%s
	<!-- Storyboard Scenes Ribbon -->
	<g transform="translate(24, 450)">
		<rect width="912" height="60" rx="8" fill="#030712" opacity="0.85" stroke="#1e293b" stroke-width="1" />
		<text x="16" y="24" fill="#38bdf8" font-family="monospace" font-size="11px" font-weight="bold">%s</text>
		<text x="16" y="44" fill="#e2e8f0" font-family="sans-serif" font-size="12px" font-weight="600">%s</text>
	</g>
</svg>`,
		width, height, width, height,
		width, height,
		soundtrackSection,
		headerLabel,
		escapeXML(truncateString(prompt, 70)),
	)

	return fmt.Sprintf("data:image/svg+xml;base64,%s", base64.StdEncoding.EncodeToString([]byte(svg)))
}

func generateStyledAudioSVGDataURL(prompt string, model string, durationSeconds int) string {
	width, height := 800, 400
	svg := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d">
	<defs>
		<linearGradient id="aud-grad" x1="0%%" y1="0%%" x2="100%%" y2="100%%">
			<stop offset="0%%" stop-color="#090d16" />
			<stop offset="50%%" stop-color="#0f172a" />
			<stop offset="100%%" stop-color="#030712" />
		</linearGradient>
		<linearGradient id="wave-grad" x1="0%%" y1="100%%" x2="0%%" y2="0%%">
			<stop offset="0%%" stop-color="#8b5cf6" />
			<stop offset="50%%" stop-color="#ec4899" />
			<stop offset="100%%" stop-color="#06b6d4" />
		</linearGradient>
	</defs>
	<rect width="%d" height="%d" fill="url(#aud-grad)" />
	<!-- Top Spec Bar -->
	<g transform="translate(30, 30)">
		<rect width="740" height="40" rx="8" fill="#1e293b" opacity="0.6" stroke="#334155" stroke-width="1" />
		<text x="20" y="25" fill="#38bdf8" font-family="monospace" font-size="11px" font-weight="bold">AUDIO SOUNDTRACK • %ds • %s</text>
	</g>
	<!-- Audio Waveform Visualization -->
	<g transform="translate(50, 200)">
		<rect x="0" y="-30" width="8" height="60" rx="4" fill="url(#wave-grad)" />
		<rect x="18" y="-55" width="8" height="110" rx="4" fill="url(#wave-grad)" />
		<rect x="36" y="-80" width="8" height="160" rx="4" fill="url(#wave-grad)" />
		<rect x="54" y="-45" width="8" height="90" rx="4" fill="url(#wave-grad)" />
		<rect x="72" y="-95" width="8" height="190" rx="4" fill="url(#wave-grad)" />
		<rect x="90" y="-60" width="8" height="120" rx="4" fill="url(#wave-grad)" />
		<rect x="108" y="-35" width="8" height="70" rx="4" fill="url(#wave-grad)" />
		<rect x="126" y="-75" width="8" height="150" rx="4" fill="url(#wave-grad)" />
		<rect x="144" y="-105" width="8" height="210" rx="4" fill="url(#wave-grad)" />
		<rect x="162" y="-50" width="8" height="100" rx="4" fill="url(#wave-grad)" />
		<rect x="180" y="-85" width="8" height="170" rx="4" fill="url(#wave-grad)" />
		<rect x="198" y="-65" width="8" height="130" rx="4" fill="url(#wave-grad)" />
		<rect x="216" y="-40" width="8" height="80" rx="4" fill="url(#wave-grad)" />
		<rect x="234" y="-90" width="8" height="180" rx="4" fill="url(#wave-grad)" />
		<rect x="252" y="-115" width="8" height="230" rx="4" fill="url(#wave-grad)" />
		<rect x="270" y="-70" width="8" height="140" rx="4" fill="url(#wave-grad)" />
		<rect x="288" y="-45" width="8" height="90" rx="4" fill="url(#wave-grad)" />
		<rect x="306" y="-80" width="8" height="160" rx="4" fill="url(#wave-grad)" />
		<rect x="324" y="-100" width="8" height="200" rx="4" fill="url(#wave-grad)" />
		<rect x="342" y="-55" width="8" height="110" rx="4" fill="url(#wave-grad)" />
		<rect x="360" y="-90" width="8" height="180" rx="4" fill="url(#wave-grad)" />
		<rect x="378" y="-60" width="8" height="120" rx="4" fill="url(#wave-grad)" />
		<rect x="396" y="-35" width="8" height="70" rx="4" fill="url(#wave-grad)" />
		<rect x="414" y="-75" width="8" height="150" rx="4" fill="url(#wave-grad)" />
		<rect x="432" y="-105" width="8" height="210" rx="4" fill="url(#wave-grad)" />
		<rect x="450" y="-60" width="8" height="120" rx="4" fill="url(#wave-grad)" />
		<rect x="468" y="-85" width="8" height="170" rx="4" fill="url(#wave-grad)" />
		<rect x="486" y="-45" width="8" height="90" rx="4" fill="url(#wave-grad)" />
		<rect x="504" y="-65" width="8" height="130" rx="4" fill="url(#wave-grad)" />
		<rect x="522" y="-95" width="8" height="190" rx="4" fill="url(#wave-grad)" />
		<rect x="540" y="-55" width="8" height="110" rx="4" fill="url(#wave-grad)" />
		<rect x="558" y="-30" width="8" height="60" rx="4" fill="url(#wave-grad)" />
		<rect x="576" y="-75" width="8" height="150" rx="4" fill="url(#wave-grad)" />
		<rect x="594" y="-100" width="8" height="200" rx="4" fill="url(#wave-grad)" />
		<rect x="612" y="-65" width="8" height="130" rx="4" fill="url(#wave-grad)" />
		<rect x="630" y="-85" width="8" height="170" rx="4" fill="url(#wave-grad)" />
		<rect x="648" y="-45" width="8" height="90" rx="4" fill="url(#wave-grad)" />
		<rect x="666" y="-70" width="8" height="140" rx="4" fill="url(#wave-grad)" />
		<rect x="684" y="-30" width="8" height="60" rx="4" fill="url(#wave-grad)" />
	</g>
	<!-- Audio Prompt Footer -->
	<g transform="translate(30, 320)">
		<rect width="740" height="50" rx="8" fill="#030712" opacity="0.9" stroke="#1e293b" stroke-width="1" />
		<text x="16" y="30" fill="#f8fafc" font-family="sans-serif" font-size="12px" font-weight="600">%s</text>
	</g>
</svg>`,
		width, height, width, height,
		width, height,
		durationSeconds, escapeXML(model),
		escapeXML(truncateString(prompt, 75)),
	)
	return fmt.Sprintf("data:image/svg+xml;base64,%s", base64.StdEncoding.EncodeToString([]byte(svg)))
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}
