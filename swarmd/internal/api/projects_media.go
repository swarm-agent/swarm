package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/audiogen"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

// SetAudioGenerationService configures the audio generation service for project tasks.
func (s *Server) SetAudioGenerationService(svc *audiogen.Service) {
	s.audioGen = svc
}

// managedVideoService defines the execution interface for generating managed videos.
type managedVideoService interface {
	GenerateManagedVideo(ctx context.Context, req videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error)
}

// SetVideoGenerationService supplies the daemon-owned service, including its
// canonical account credential authority. Configure it before serving requests.
func (s *Server) SetVideoGenerationService(svc managedVideoService) {
	s.videoGen = svc
}

func (s *Server) resolveVideoGenerationService() (managedVideoService, error) {
	if s == nil || s.videoGen == nil {
		return nil, errors.New("video generation service is not configured")
	}
	return s.videoGen, nil
}

func (s *Server) generateImageMedia(
	ctx context.Context,
	p identity.Principal,
	prompt string,
	aspectRatio string,
	variantIndex int,
	modelOverride string,
	resolution string,
	sourceImage *imagegen.ManagedImageSource,
) (string, string, string, string, error) {
	if s == nil || s.imageGen == nil {
		return "", "", "", "", errors.New("image generation service is not configured")
	}

	usedModel := strings.TrimSpace(modelOverride)
	if usedModel == "" && s.uiSettings != nil && strings.TrimSpace(p.AccountScopeID) != "" {
		if uiSet, err := s.uiSettings.GetForAccount(p.AccountScopeID); err == nil {
			usedModel = strings.TrimSpace(uiSet.Tools.Image.DefaultModel)
		}
	}
	if usedModel == "" {
		return "", "", "", "", errors.New("configure a default image model or select a model for this generation")
	}

	ar := strings.TrimSpace(aspectRatio)
	resTag := strings.TrimSpace(resolution)

	settings := make(map[string]any)
	if ar != "" {
		settings["aspect_ratio"] = ar
	}
	if resTag != "" {
		settings["image_size"] = resTag
	}

	var capabilityToken string
	if caps, err := s.imageGen.ManagedImageCapabilities(usedModel); err == nil {
		if caps.CapabilityToken != "" {
			capabilityToken = caps.CapabilityToken
		}
		if ar == "" && caps.Settings != nil {
			if arSet, ok := caps.Settings["aspect_ratio"]; ok {
				if defVal, ok := arSet.DefaultValue.(string); ok && strings.TrimSpace(defVal) != "" {
					ar = strings.TrimSpace(defVal)
					settings["aspect_ratio"] = ar
				}
			}
		}
		if resTag == "" && caps.Settings != nil {
			if resSet, ok := caps.Settings["image_size"]; ok {
				if defVal, ok := resSet.DefaultValue.(string); ok && strings.TrimSpace(defVal) != "" {
					resTag = strings.TrimSpace(defVal)
					settings["image_size"] = resTag
				}
			}
		}
	}

	selection, err := s.imageGen.ResolveModelSelection(usedModel)
	if err != nil {
		return "", "", "", "", err
	}
	// The image service returns bytes, not an alternate result model. Preserve
	// its canonical resolved selection ID, also used by the image catalog.
	actualModel := selection.ID

	genReq := imagegen.ManagedGenerateRequest{
		SelectionID:     usedModel,
		Prompt:          prompt,
		Size:            resTag,
		Settings:        settings,
		CapabilityToken: capabilityToken,
		Principal:       p,
		Source:          sourceImage,
	}

	res, err := s.imageGen.GenerateManagedImage(ctx, genReq)
	if err != nil {
		return "", actualModel, ar, resTag, err
	}
	if len(res.Bytes) == 0 {
		return "", actualModel, ar, resTag, errors.New("image generation returned empty image data")
	}

	mime := res.MediaType
	if mime == "" {
		mime = "image/png"
	}
	mediaURL := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(res.Bytes))
	return mediaURL, actualModel, ar, resTag, nil
}

func isSupportedImageModel(s *Server, modelID string) bool {
	clean := strings.TrimSpace(modelID)
	if clean == "" {
		return true
	}
	if s != nil && s.imageGen != nil {
		if resolved, err := s.imageGen.ResolveModelSelection(clean); err == nil && resolved.ID != "" {
			return true
		}
	}
	if s != nil && s.model != nil {
		for _, provider := range []string{"google", "codex", "openrouter"} {
			if lookup, err := s.model.GetCatalog(provider, clean); err == nil && lookup.Found {
				if containsStringFold(lookup.Record.CatalogModalities.Outputs, "image") {
					return true
				}
			}
			if records, err := s.model.ListCatalog(provider, 200); err == nil {
				for _, rec := range records {
					if strings.EqualFold(rec.Model, clean) && containsStringFold(rec.CatalogModalities.Outputs, "image") {
						return true
					}
				}
			}
		}
	}
	return false
}

func (s *Server) getVideoModelOptions(modelID string) *videogen.ParsedVideoOptions {
	if s == nil || s.model == nil {
		return nil
	}
	clean := strings.TrimSpace(modelID)
	providers := []string{"google", "openrouter"}
	if provider, model, qualified := strings.Cut(clean, ":"); qualified {
		if provider != "google" && provider != "openrouter" {
			return nil
		}
		providers, clean = []string{provider}, model
	}
	for _, provider := range providers {
		if lookup, err := s.model.GetCatalog(provider, clean); err == nil && lookup.Found {
			return videogen.ExtractVideoOptions(lookup.Record)
		}
		if records, err := s.model.ListCatalog(provider, 200); err == nil {
			for _, rec := range records {
				if strings.EqualFold(rec.Model, clean) {
					return videogen.ExtractVideoOptions(rec)
				}
			}
		}
	}
	return nil
}

func isSupportedVideoModel(s *Server, modelID string) bool {
	clean := strings.TrimSpace(modelID)
	if clean == "" {
		return false
	}
	opts := s.getVideoModelOptions(clean)
	return opts != nil && opts.HasVideoOutput
}

func (s *Server) getModelGenerationOptions(modelID string) *mediaCatalogGenerationOptions {
	clean := strings.TrimSpace(modelID)
	if clean == "" || s == nil {
		return nil
	}
	if s.model != nil {
		for _, provider := range []string{"google", "openrouter", "codex"} {
			if lookup, err := s.model.GetCatalog(provider, clean); err == nil && lookup.Found {
				if opts := extractModelGenerationOptions(lookup.Record); opts != nil {
					return opts
				}
			}
		}
		for _, provider := range []string{"google", "openrouter", "codex"} {
			if records, err := s.model.ListCatalog(provider, 200); err == nil {
				for _, rec := range records {
					if strings.EqualFold(rec.Model, clean) {
						if opts := extractModelGenerationOptions(rec); opts != nil {
							return opts
						}
					}
				}
			}
		}
	}
	if s.imageGen != nil {
		if caps, err := s.imageGen.ManagedImageCapabilities(clean); err == nil && caps.Available && len(caps.Settings) > 0 {
			var arList, resList []string
			var defAR, defRes string
			if arCap, ok := caps.Settings["aspect_ratio"]; ok {
				for _, v := range arCap.SupportedValues {
					if str, ok := v.(string); ok && str != "" {
						arList = append(arList, str)
					}
				}
				if str, ok := arCap.DefaultValue.(string); ok {
					defAR = str
				}
			}
			if resCap, ok := caps.Settings["image_size"]; ok {
				for _, v := range resCap.SupportedValues {
					if str, ok := v.(string); ok && str != "" {
						resList = append(resList, str)
					}
				}
				if str, ok := resCap.DefaultValue.(string); ok {
					defRes = str
				}
			}
			if len(arList) > 0 || len(resList) > 0 {
				return &mediaCatalogGenerationOptions{
					AspectRatios: arList,
					Resolutions:  resList,
					DefaultRatio: defAR,
					DefaultRes:   defRes,
				}
			}
		}
	}
	return nil
}

func containsStringFold(slice []string, val string) bool {
	for _, s := range slice {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(val)) {
			return true
		}
	}
	return false
}

func isEquivalentAspectRatio(supported []string, requested string) bool {
	reqLower := strings.ToLower(strings.TrimSpace(requested))
	for _, s := range supported {
		sLower := strings.ToLower(strings.TrimSpace(s))
		if (reqLower == "landscape" && sLower == "16:9") ||
			(reqLower == "portrait" && sLower == "9:16") ||
			(reqLower == "16:9" && sLower == "landscape") ||
			(reqLower == "9:16" && sLower == "portrait") {
			return true
		}
	}
	return false
}

func isOmniModel(modelID string) bool {
	return strings.Contains(strings.ToLower(modelID), "omni")
}

func decodeImageConfig(imgBytes []byte) (image.Config, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(imgBytes))
	if err == nil && (format == "png" || format == "jpeg") {
		return cfg, format, nil
	}
	if err != nil {
		return image.Config{}, "", err
	}
	return image.Config{}, "", fmt.Errorf("unsupported image format %q; only PNG and JPEG are supported", format)
}

func validateImageBytes(imgBytes []byte, mediaType string) error {
	if len(imgBytes) == 0 {
		return errors.New("image bytes are empty")
	}
	if len(imgBytes) > 50<<20 {
		return errors.New("image payload exceeds maximum allowed size (50MB)")
	}
	cfg, format, err := decodeImageConfig(imgBytes)
	if err != nil {
		return fmt.Errorf("malformed or unsupported image data: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("invalid image dimensions (%dx%d)", cfg.Width, cfg.Height)
	}
	if cfg.Width > 8192 || cfg.Height > 8192 {
		return fmt.Errorf("image dimensions (%dx%d) exceed maximum allowed limit (8192x8192)", cfg.Width, cfg.Height)
	}
	mType := strings.ToLower(strings.TrimSpace(mediaType))
	if mType != "" {
		expectedMIME := "image/" + format
		if format == "jpeg" && (mType == "image/jpeg" || mType == "image/jpg") {
			// match
		} else if mType != expectedMIME {
			return fmt.Errorf("image content does not match declared media type %q (detected %s)", mediaType, expectedMIME)
		}
	}
	// Decode the complete image, not just its header, before provider dispatch.
	if _, _, err := image.Decode(bytes.NewReader(imgBytes)); err != nil {
		return fmt.Errorf("malformed image payload: %w", err)
	}
	return nil
}

func validateProjectMediaTaskSettings(s *Server, task *pebblestore.ProjectTaskRecord, p ...identity.Principal) error {
	if task == nil {
		return nil
	}
	var principal identity.Principal
	if len(p) > 0 {
		principal = p[0]
	}
	if principal.AccountScopeID == "" && task.AccountID != "" {
		principal.AccountScopeID = task.AccountID
	}
	agent := strings.TrimSpace(task.Agent)
	if agent == "video" || task.OutcomeType == "video_clip" || task.OutcomeType == "video_story" {
		if task.OutcomeType == "video_story" || len(task.Scenes) > 1 {
			if err := validateVideoScenes(task.Scenes, task.Operation, task.VariantCount, task.Soundtrack); err != nil {
				return err
			}
		}
		if task.VariantCount < 0 {
			return errors.New("video variant count cannot be negative")
		}
		if task.VariantCount > 8 {
			return fmt.Errorf("video variant count %d exceeds maximum allowed (8)", task.VariantCount)
		}

		op := strings.ToLower(strings.TrimSpace(task.Operation))
		if op == "" {
			if len(task.AttachedMedia) == 1 && isVideoAttachment(task.AttachedMedia[0]) {
				return errors.New("video operation must be explicitly specified when source media is provided (edit or extend)")
			}
			op = pebblestore.VideoOperationCreate
		}
		if op != pebblestore.VideoOperationCreate && op != pebblestore.VideoOperationEdit && op != pebblestore.VideoOperationExtend {
			return fmt.Errorf("task operation %q is invalid; must be create, edit, or extend", task.Operation)
		}

		if op == pebblestore.VideoOperationCreate {
			if len(task.AttachedMedia) > 1 {
				return errors.New("at most one initial image attachment is supported for video generation")
			}
			if len(task.AttachedMedia) == 1 {
				m := task.AttachedMedia[0]
				if isVideoAttachment(m) {
					return errors.New("cannot provide source video for create operation; use edit or extend")
				}
				if !isImageAttachment(m) {
					return fmt.Errorf("unsupported attachment kind %q for video generation; only images are supported as reference inputs", m.Kind)
				}
				fn := strings.ToLower(strings.TrimSpace(m.Filename))
				if fn != "" && !strings.HasSuffix(fn, ".png") && !strings.HasSuffix(fn, ".jpg") && !strings.HasSuffix(fn, ".jpeg") {
					return fmt.Errorf("unsupported file extension on %q for video generation; only PNG and JPEG image formats are supported", m.Filename)
				}
				if s != nil {
					imgBytes, mType, err := s.resolveSourceMediaBytes(context.Background(), principal, m, "image")
					if err != nil {
						return fmt.Errorf("invalid image attachment: %w", err)
					}
					if len(imgBytes) == 0 {
						return errors.New("image attachment payload is empty")
					}
					if err := validateImageBytes(imgBytes, mType); err != nil {
						return err
					}
				}
			}
		} else {
			if len(task.AttachedMedia) == 0 {
				return fmt.Errorf("video %s operation requires source video", op)
			}
			if len(task.AttachedMedia) > 1 {
				return fmt.Errorf("video %s operation requires exactly 1 source video; multiple attachments are not supported", op)
			}
			m := task.AttachedMedia[0]
			if isImageAttachment(m) {
				return fmt.Errorf("initial image input is not supported for video %s operation; only create operation supports initial image", op)
			}
			if !isVideoAttachment(m) {
				return fmt.Errorf("unsupported attachment kind %q for video %s operation; only video attachments are supported", m.Kind, op)
			}
			fn := strings.ToLower(strings.TrimSpace(m.Filename))
			if fn != "" && !strings.HasSuffix(fn, ".mp4") {
				return fmt.Errorf("unsupported file extension on %q for video %s operation; only MP4 video format is supported", m.Filename, op)
			}
			if s != nil {
				srcRec, err := s.resolveSourceMediaRecord(context.Background(), principal, m, "video", task.ProjectID)
				if err != nil {
					return fmt.Errorf("invalid video attachment: %w", err)
				}
				if len(srcRec.Bytes) == 0 && srcRec.Provenance == nil {
					return errors.New("video attachment payload is empty")
				}
			}
		}

		model := strings.TrimSpace(task.Model)
		if model == "" && s != nil && s.uiSettings != nil && principal.AccountScopeID != "" {
			if uiSet, err := s.uiSettings.GetForAccount(principal.AccountScopeID); err == nil {
				if op == pebblestore.VideoOperationEdit {
					model = strings.TrimSpace(uiSet.Tools.Video.IterationModel)
				} else {
					model = strings.TrimSpace(uiSet.Tools.Video.DefaultModel)
				}
			}
		}
		if model == "" {
			if op == pebblestore.VideoOperationEdit {
				return errors.New("no default video iteration model configured for account; select a model or configure one in Settings")
			}
			return errors.New("no default video model configured for account; select a model or configure one in Settings")
		}
		if !isSupportedVideoModel(s, model) {
			return fmt.Errorf("unsupported video model %q", model)
		}
		if op == pebblestore.VideoOperationEdit && videogen.IsVeoModel(model) {
			return errors.New("Veo models do not support video editing; use Gemini Omni for video editing or extend for Veo continuation")
		}

		vOpts := s.getVideoModelOptions(model)
		if vOpts == nil {
			return fmt.Errorf("model %q does not support video generation", model)
		}

		if ar := strings.TrimSpace(task.AspectRatio); ar != "" {
			if len(vOpts.AspectRatios) > 0 {
				if !videogen.ContainsStringFold(vOpts.AspectRatios, ar) && !videogen.IsEquivalentAspectRatio(vOpts.AspectRatios, ar) {
					return fmt.Errorf("unsupported video aspect ratio %q; supported ratios are %s", ar, strings.Join(vOpts.AspectRatios, ", "))
				}
			} else {
				return errors.New("video aspect ratio metadata unavailable for selected model")
			}
		}

		if res := strings.TrimSpace(task.Resolution); res != "" {
			if len(vOpts.Resolutions) > 0 {
				if !videogen.ContainsStringFold(vOpts.Resolutions, res) {
					return fmt.Errorf("unsupported video resolution %q; supported resolutions are %s", res, strings.Join(vOpts.Resolutions, ", "))
				}
			} else {
				return errors.New("video resolution metadata unavailable for selected model")
			}
		}

		if videogen.IsOmniModel(model) {
			if task.DurationSeconds > 0 {
				return fmt.Errorf("model %q does not accept duration selection", model)
			}
		} else if dur := task.DurationSeconds; dur > 0 {
			if len(vOpts.Durations) > 0 {
				resLower := strings.ToLower(strings.TrimSpace(task.Resolution))
				if resLower == "" && vOpts.DefaultRes != "" {
					resLower = strings.ToLower(vOpts.DefaultRes)
				}
				allowedDurs := vOpts.Durations
				if vOpts.ResolutionDurations != nil {
					if rd, ok := vOpts.ResolutionDurations[resLower]; ok && len(rd) > 0 {
						allowedDurs = rd
					}
				}
				found := false
				for _, d := range allowedDurs {
					if d == dur {
						found = true
						break
					}
				}
				if !found {
					if resLower == "1080p" || resLower == "4k" {
						return fmt.Errorf("video resolution %s requires 8s duration", task.Resolution)
					}
					var durStrs []string
					for _, d := range allowedDurs {
						durStrs = append(durStrs, fmt.Sprintf("%d", d))
					}
					return fmt.Errorf("unsupported video duration %d seconds; supported durations are %s seconds", dur, strings.Join(durStrs, ", "))
				}
			} else {
				return errors.New("video duration metadata unavailable for selected model")
			}
		}

		if len(task.Scenes) > 1 {
			for i, scene := range task.Scenes {
				if scene.DurationSec == 0 {
					continue
				}
				copy := *task
				copy.Scenes = nil
				copy.OutcomeType = "video_clip"
				copy.DurationSeconds = scene.DurationSec
				if err := validateProjectMediaTaskSettings(s, &copy, principal); err != nil {
					return fmt.Errorf("scene %d: %w", i+1, err)
				}
			}
		}
		if op == pebblestore.VideoOperationCreate && len(task.AttachedMedia) > 0 {
			if !vOpts.InitialImageSupported {
				return fmt.Errorf("selected video model %q does not support image input", model)
			}
		}
		return nil
	}

	if agent == "image" || (agent == "designer" && (task.Tier == "swarm" || len(task.Deliverables) > 1 || task.OutcomeType == "media_bundle")) {
		model := strings.TrimSpace(task.Model)
		if model != "" {
			if !isSupportedImageModel(s, model) {
				return fmt.Errorf("unsupported image model %q", model)
			}
		}
		opts := s.getModelGenerationOptions(model)
		if ar := strings.TrimSpace(task.AspectRatio); ar != "" {
			if opts != nil && len(opts.AspectRatios) > 0 {
				if !containsStringFold(opts.AspectRatios, ar) && !isEquivalentAspectRatio(opts.AspectRatios, ar) {
					return fmt.Errorf("unsupported image aspect ratio %q; supported ratios are %s", ar, strings.Join(opts.AspectRatios, ", "))
				}
			} else {
				return errors.New("image aspect ratio metadata unavailable for selected model")
			}
		}
		if res := strings.TrimSpace(task.Resolution); res != "" {
			if opts != nil && len(opts.Resolutions) > 0 {
				if !containsStringFold(opts.Resolutions, res) {
					return fmt.Errorf("unsupported image resolution %q; supported resolutions are %s", res, strings.Join(opts.Resolutions, ", "))
				}
			} else {
				return errors.New("image resolution metadata unavailable for selected model")
			}
		}
		if task.VariantCount < 0 {
			return errors.New("image variant count cannot be negative")
		}
		if task.VariantCount > 25 {
			return fmt.Errorf("image variant count %d exceeds maximum allowed (25)", task.VariantCount)
		}
		if agent == "image" {
			count := task.VariantCount
			if count == 0 {
				count = max(1, len(task.Deliverables))
			}
			if len(task.Deliverables) > 0 && len(task.Deliverables) != count {
				return errors.New("image deliverable count does not match variant count")
			}
			if len(task.ImagePrompts) > 0 || task.EnhancePrompt {
				if err := pebblestore.ValidateImagePrompts(task.ImagePrompts, count); err != nil {
					return err
				}
			}
		}
		return nil
	}

	return nil
}

var projectTaskUpdateMu sync.Mutex

func updateProjectTaskWithRetry(db *pebblestore.SessionStore, accountScopeID, projectID, taskID string, mutate func(*pebblestore.ProjectTaskRecord) error) (*pebblestore.ProjectTaskRecord, error) {
	projectTaskUpdateMu.Lock()
	defer projectTaskUpdateMu.Unlock()
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
	trimmedURL := strings.TrimSpace(m.URL)
	urlLower := strings.ToLower(trimmedURL)

	// Disallow arbitrary external URLs
	if strings.HasPrefix(urlLower, "http://") || strings.HasPrefix(urlLower, "https://") {
		return nil, "", errors.New("fetching arbitrary external URLs is not permitted")
	}

	// Disallow direct filesystem paths
	isRecognizedRoute := strings.HasPrefix(trimmedURL, "/v3/sessions/") ||
		strings.HasPrefix(trimmedURL, "/v3/projects/") ||
		strings.HasPrefix(trimmedURL, "/v3/media-staging/") ||
		strings.HasPrefix(trimmedURL, "/media/staging/")
	if (strings.HasPrefix(trimmedURL, "/") && !isRecognizedRoute) ||
		strings.HasPrefix(trimmedURL, "./") ||
		strings.HasPrefix(trimmedURL, "../") ||
		strings.Contains(trimmedURL, "\\") {
		return nil, "", errors.New("direct filesystem paths are not permitted")
	}

	// Session media asset URL: /v3/sessions/{sessionID}/media/{assetID}
	if strings.Contains(trimmedURL, "/media/asset_") || strings.Contains(trimmedURL, "/media/media_") || (strings.HasPrefix(trimmedURL, "/v3/sessions/") && strings.Contains(trimmedURL, "/media/")) {
		u, parseErr := url.Parse(trimmedURL)
		if parseErr == nil {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) == 5 && parts[0] == "v3" && parts[1] == "sessions" && parts[3] == "media" {
				sessionID := strings.TrimSpace(parts[2])
				assetID := strings.TrimSpace(parts[4])
				if s != nil && s.sessions != nil {
					asset, payload, readErr := s.sessions.ReadSessionMediaAsset(p.AccountScopeID, sessionID, assetID)
					if readErr == nil && len(payload) > 0 {
						mediaType := asset.DetectedMIMEType
						if mediaType == "" {
							mediaType = m.MediaType
						}
						if expectedKind != "" {
							if err := validateSourceKindMIME(expectedKind, mediaType, payload); err != nil {
								return nil, "", err
							}
						}
						return payload, mediaType, nil
					}
				}
			}
		}
	}

	// Project media asset URL: /v3/projects/{projectID}/media/{mediaID}
	if strings.Contains(trimmedURL, "/v3/projects/") && strings.Contains(trimmedURL, "/media/") && !strings.Contains(trimmedURL, "/tasks/") {
		u, parseErr := url.Parse(trimmedURL)
		if parseErr == nil {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) == 5 && parts[0] == "v3" && parts[1] == "projects" && parts[3] == "media" {
				projID := strings.TrimSpace(parts[2])
				mediaID := strings.TrimSpace(parts[4])
				if s != nil && s.sessions != nil && s.sessions.Store() != nil {
					proj, found, err := s.sessions.Store().GetProject(p.AccountScopeID, projID)
					if err == nil && found && proj != nil {
						for _, up := range proj.UploadedMedia {
							if up.ID == mediaID {
								return s.resolveSourceMediaBytes(ctx, p, up, expectedKind)
							}
						}
					}
				}
			}
		}
	}

	// 1. Data URI in URL or Data
	dataURL := ""
	if strings.HasPrefix(trimmedURL, "data:") {
		dataURL = trimmedURL
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
					return nil, "", fmt.Errorf("decode data url: %w", err)
				}
				if len(decoded) == 0 {
					return nil, "", errors.New("data url payload is empty")
				}
				if mediaType == "" {
					if expectedKind == "video" {
						mediaType = "video/mp4"
					} else {
						mediaType = "image/png"
					}
				}
				if err := validateSourceKindMIME(expectedKind, mediaType, decoded); err != nil {
					return nil, "", err
				}
				return decoded, mediaType, nil
			}
		}
		return nil, "", errors.New("invalid data url format")
	}

	// 2. Raw base64 in m.Data
	if len(m.Data) > 0 {
		decoded, err := base64.StdEncoding.DecodeString(m.Data)
		if err != nil {
			return nil, "", fmt.Errorf("decode base64 data: %w", err)
		}
		if len(decoded) == 0 {
			return nil, "", errors.New("base64 payload is empty")
		}
		mediaType := m.MediaType
		if mediaType == "" {
			if expectedKind == "video" {
				mediaType = "video/mp4"
			} else {
				mediaType = "image/png"
			}
		}
		if err := validateSourceKindMIME(expectedKind, mediaType, decoded); err != nil {
			return nil, "", err
		}
		return decoded, mediaType, nil
	}

	// 3. Staged upload lookup
	stagingID := ""
	if strings.HasPrefix(m.ID, "stg_") {
		stagingID = m.ID
	} else if idx := strings.Index(trimmedURL, "/media/staging/stg_"); idx >= 0 {
		sub := trimmedURL[idx+len("/media/staging/"):]
		if end := strings.IndexAny(sub, "/?#"); end >= 0 {
			stagingID = sub[:end]
		} else {
			stagingID = sub
		}
	} else if idx := strings.Index(trimmedURL, "/v3/media-staging/stg_"); idx >= 0 {
		sub := trimmedURL[idx+len("/v3/media-staging/"):]
		if end := strings.IndexAny(sub, "/?#"); end >= 0 {
			stagingID = sub[:end]
		} else {
			stagingID = sub
		}
	}
	if stagingID != "" {
		if s == nil || s.mediaStaging == nil {
			return nil, "", errors.New("media staging service is not configured")
		}
		var payload []byte
		var readErr error
		stgRecord, found, getErr := s.mediaStaging.Get(p.AccountScopeID, stagingID)
		if getErr == nil && found && stgRecord.State == pebblestore.MediaStagingStateBound && stgRecord.AuthorityAssetID != "" && stgRecord.BoundSessionID != "" && s.sessions != nil {
			_, payload, readErr = s.sessions.ReadSessionMediaAsset(p.AccountScopeID, stgRecord.BoundSessionID, stgRecord.AuthorityAssetID)
		} else {
			_, payload, readErr = s.mediaStaging.Read(p.AccountScopeID, stagingID, time.Now().UnixMilli())
		}
		if readErr != nil {
			return nil, "", fmt.Errorf("staged upload %q not found or expired: %w", stagingID, readErr)
		}
		if len(payload) == 0 {
			return nil, "", fmt.Errorf("staged upload %q is empty", stagingID)
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
		if err := validateSourceKindMIME(expectedKind, mediaType, payload); err != nil {
			return nil, "", err
		}
		return payload, mediaType, nil
	}

	// 4. Canonical session artifact
	if strings.Contains(trimmedURL, "/artifacts/") || (strings.HasPrefix(trimmedURL, "/v3/sessions/") && strings.Contains(trimmedURL, "/artifacts")) {
		u, parseErr := url.Parse(trimmedURL)
		if parseErr != nil {
			return nil, "", fmt.Errorf("invalid artifact url: %w", parseErr)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 5 || parts[0] != "v3" || parts[1] != "sessions" || parts[3] != "artifacts" {
			return nil, "", errors.New("invalid artifact URL path; must be /v3/sessions/{sessionID}/artifacts/{variantID}")
		}
		sessionID := strings.TrimSpace(parts[2])
		variantID := strings.TrimSpace(parts[4])

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
			return nil, "", errors.New("artifact source requires exact pinned revision (e.g. ?revision=N or ?event_seq=N)")
		}

		if s == nil || s.sessions == nil || s.sessions.Store() == nil {
			return nil, "", errors.New("session store is not configured")
		}
		variant, ok, err := s.sessions.Store().GetSessionArtifactVariantByID(p.AccountScopeID, sessionID, variantID)
		if err != nil {
			return nil, "", fmt.Errorf("read artifact variant: %w", err)
		}
		if !ok {
			return nil, "", fmt.Errorf("session artifact variant %q not found in account scope", variantID)
		}
		curRevStr := fmt.Sprintf("%d", variant.EventSeq)
		if requestedRev != curRevStr {
			return nil, "", fmt.Errorf("stale artifact revision %q requested (current revision is %s)", requestedRev, curRevStr)
		}

		if s.artifacts == nil {
			return nil, "", errors.New("artifact authority is not configured")
		}
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
			return nil, "", fmt.Errorf("read artifact reference: %w", readErr)
		}
		if len(body) == 0 {
			return nil, "", errors.New("artifact reference payload is empty")
		}
		mediaType := variant.MediaType
		if mediaType == "" {
			if expectedKind == "video" {
				mediaType = "video/mp4"
			} else {
				mediaType = "image/png"
			}
		}
		if err := validateSourceKindMIME(expectedKind, mediaType, body); err != nil {
			return nil, "", err
		}
		return body, mediaType, nil
	}

	// 5. Canonical project deliverable reference
	if strings.Contains(trimmedURL, "/deliverables/") || (strings.HasPrefix(trimmedURL, "/v3/projects/") && strings.Contains(trimmedURL, "/tasks/")) {
		u, parseErr := url.Parse(trimmedURL)
		if parseErr != nil {
			return nil, "", fmt.Errorf("invalid deliverable url: %w", parseErr)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 7 || parts[0] != "v3" || parts[1] != "projects" || parts[3] != "tasks" || parts[5] != "deliverables" {
			return nil, "", errors.New("invalid deliverable URL path; must be /v3/projects/{projectID}/tasks/{taskID}/deliverables/{delivID}")
		}
		projID := strings.TrimSpace(parts[2])
		tID := strings.TrimSpace(parts[4])
		dID := strings.TrimSpace(parts[6])
		if s == nil || s.sessions == nil || s.sessions.Store() == nil {
			return nil, "", errors.New("session store is not configured")
		}
		task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, projID, tID)
		if err != nil {
			return nil, "", fmt.Errorf("read project task: %w", err)
		}
		if !found || task == nil {
			return nil, "", fmt.Errorf("project task %q not found in account scope", tID)
		}
		var targetDeliv *pebblestore.ProjectTaskDeliverable
		for _, d := range task.Deliverables {
			if d.ID == dID {
				targetDeliv = &d
				break
			}
		}
		if targetDeliv == nil {
			return nil, "", fmt.Errorf("deliverable %q not found in task %q", dID, tID)
		}
		if targetDeliv.Status != "ready" && targetDeliv.Status != "accepted" {
			return nil, "", fmt.Errorf("deliverable %q is not ready", dID)
		}
		if strings.Contains(targetDeliv.MediaURL, "/deliverables/") {
			return nil, "", errors.New("nested deliverable references are not permitted")
		}
		delivRef := pebblestore.ProjectTaskMediaRef{
			ID:        targetDeliv.ID,
			URL:       targetDeliv.MediaURL,
			Kind:      expectedKind,
			MediaType: targetDeliv.Kind,
		}
		return s.resolveSourceMediaBytes(ctx, p, delivRef, expectedKind)
	}

	return nil, "", errors.New("no source media bytes available")
}

func validateSourceKindMIME(expectedKind, mediaType string, bytes []byte) error {
	detected := http.DetectContentType(bytes)
	if expectedKind == "image" {
		if strings.HasPrefix(strings.ToLower(mediaType), "video/") || (detected != "" && strings.HasPrefix(detected, "video/")) {
			return fmt.Errorf("source media MIME mismatch: expected image, got %s", mediaType)
		}
	} else if expectedKind == "video" {
		if strings.HasPrefix(strings.ToLower(mediaType), "image/") || (detected != "" && strings.HasPrefix(detected, "image/")) {
			return fmt.Errorf("source media MIME mismatch: expected video, got %s", mediaType)
		}
	}
	return nil
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

	if valErr := validateProjectMediaTaskSettings(s, task, p); valErr != nil {
		_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "failed"
			t.LastError = valErr.Error()
			t.ActionNeeded = fmt.Sprintf("Action Needed: %v", valErr)
			t.WhatNotDone = []string{valErr.Error()}
			for i := range t.Deliverables {
				t.Deliverables[i].Status = "failed"
				t.Deliverables[i].Description = fmt.Sprintf("Settings error: %v", valErr)
			}
			return nil
		})
		return
	}

	if task.Agent == "image" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		ar := task.AspectRatio
		if ar == "" {
			ar = "1:1"
		}
		count := len(task.Deliverables)
		if count == 0 {
			count = task.VariantCount
		}
		if count <= 0 {
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

		lowerPrompt := strings.ToLower(prompt)
		isFineTune := strings.Contains(lowerPrompt, "change") || strings.Contains(lowerPrompt, "modify") || strings.Contains(lowerPrompt, "edit") || strings.Contains(lowerPrompt, "tweak") || strings.Contains(lowerPrompt, "replace") || strings.Contains(lowerPrompt, "fine-tune")
		if sourceErr == nil && sourceImage == nil {
			if len(task.AttachedMedia) > 0 {
				sourceErr = errors.New("attached source image could not be resolved")
			} else if isFineTune {
				sourceErr = errors.New("image edit or modification requires an attached source image; cannot fall back to text-to-image")
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
		if imageModel != "" && !isSupportedImageModel(s, imageModel) {
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
					if i < len(task.Deliverables) && (task.Deliverables[i].Status == "ready" || task.Deliverables[i].Status == "accepted") {
						continue
					}
					reqPrompt := prompt
					if len(task.ImagePrompts) > 0 {
						reqPrompt = task.ImagePrompts[i]
					}
					mediaURL, usedModel, resolvedAR, resolvedRes, err := s.generateImageMedia(ctx, p, reqPrompt, ar, variantIdx, task.Model, task.Resolution, sourceImage)
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
								t.Deliverables[slotIndex].Model = usedModel
								t.Deliverables[slotIndex].AspectRatio = resolvedAR
								t.Deliverables[slotIndex].Resolution = resolvedRes
								desc := fmt.Sprintf("Autonomous deliverable for %s in aspect ratio %s", t.Title, resolvedAR)
								if resolvedRes != "" {
									desc = fmt.Sprintf("Autonomous deliverable for %s in aspect ratio %s (%s)", t.Title, resolvedAR, resolvedRes)
								}
								if usedModel != "" {
									if resolvedRes != "" {
										desc = fmt.Sprintf("Autonomous deliverable for %s in aspect ratio %s (%s) using %s", t.Title, resolvedAR, resolvedRes, usedModel)
									} else {
										desc = fmt.Sprintf("Autonomous deliverable for %s in aspect ratio %s using %s", t.Title, resolvedAR, usedModel)
									}
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
			t.AspectRatio = ar
			if task.Resolution != "" {
				t.Resolution = task.Resolution
			}
			if task.Model != "" {
				t.Model = task.Model
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
				} else {
					t.LastError = ""
					if isFineTune && sourceTitle != "" {
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
			}
			return nil
		})
	} else if task.Agent == "video" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		ar := task.AspectRatio
		sceneCount := max(1, len(task.Scenes))
		count := len(task.Deliverables)
		if count == 0 {
			count = task.VariantCount
		}
		if count <= 0 {
			count = 1
		}
		if count > 8 {
			count = 8
		}
		soundtrack := task.Soundtrack
		prompt := strings.TrimSpace(task.Description)
		if prompt == "" {
			prompt = strings.TrimSpace(task.Title)
		}

		op := strings.ToLower(strings.TrimSpace(task.Operation))
		if op == "" {
			op = pebblestore.VideoOperationCreate
		}

		var sourceMediaTitle string
		var sourceMediaKind string
		var sourceMediaID string
		var sourceVideo *videogen.ManagedVideoSource
		var sourceImage *videogen.ManagedVideoImage
		var sourceErr error

		if op == pebblestore.VideoOperationCreate {
			if len(task.AttachedMedia) > 1 {
				sourceErr = errors.New("at most one initial image attachment is supported for video generation")
			} else if len(task.AttachedMedia) == 1 {
				m := task.AttachedMedia[0]
				if isVideoAttachment(m) {
					sourceErr = errors.New("cannot provide source video for create operation; use edit or extend")
				} else if isImageAttachment(m) {
					fn := strings.ToLower(strings.TrimSpace(m.Filename))
					if fn != "" && !strings.HasSuffix(fn, ".png") && !strings.HasSuffix(fn, ".jpg") && !strings.HasSuffix(fn, ".jpeg") {
						sourceErr = fmt.Errorf("unsupported file extension on %q for video generation; only PNG and JPEG image formats are supported", m.Filename)
					} else {
						sourceMediaKind = "image"
						sourceMediaID = m.ID
						sourceMediaTitle = m.Title
						if sourceMediaTitle == "" {
							sourceMediaTitle = m.Filename
						}
						bytes, mType, err := s.resolveSourceMediaBytes(ctx, p, m, "image")
						if err != nil {
							sourceErr = fmt.Errorf("resolve initial image source: %w", err)
						} else if len(bytes) == 0 {
							sourceErr = errors.New("attached image is empty")
						} else if err := validateImageBytes(bytes, mType); err != nil {
							sourceErr = fmt.Errorf("invalid initial image: %w", err)
						} else {
							sourceImage = &videogen.ManagedVideoImage{
								Bytes:     bytes,
								MediaType: mType,
							}
						}
					}
				} else {
					sourceErr = fmt.Errorf("unsupported attachment kind %q for video generation; only images are supported as reference inputs", m.Kind)
				}
			}
		} else {
			if len(task.AttachedMedia) == 0 {
				sourceErr = fmt.Errorf("video %s operation requires source video", op)
			} else if len(task.AttachedMedia) > 1 {
				sourceErr = fmt.Errorf("video %s operation requires exactly 1 source video; multiple attachments are not supported", op)
			} else {
				m := task.AttachedMedia[0]
				if isImageAttachment(m) {
					sourceErr = fmt.Errorf("initial image input is not supported for video %s operation; only create operation supports initial image", op)
				} else if !isVideoAttachment(m) {
					sourceErr = fmt.Errorf("unsupported attachment kind %q for video %s operation; only video attachments are supported", m.Kind, op)
				} else {
					sourceMediaKind = "video"
					sourceMediaID = m.ID
					sourceMediaTitle = m.Title
					if sourceMediaTitle == "" {
						sourceMediaTitle = m.Filename
					}
					srcRec, err := s.resolveSourceMediaRecord(ctx, p, m, "video", task.ProjectID)
					if err != nil {
						sourceErr = fmt.Errorf("resolve video source: %w", err)
					} else if len(srcRec.Bytes) == 0 && srcRec.Provenance == nil {
						sourceErr = errors.New("attached video payload is empty")
					} else if task.SourceDigestSHA256 != "" && srcRec.SourceLink != nil && srcRec.SourceLink.DigestSHA256 != "" && !strings.EqualFold(task.SourceDigestSHA256, srcRec.SourceLink.DigestSHA256) {
						sourceErr = fmt.Errorf("source media digest changed since submission (expected %s, got %s)", task.SourceDigestSHA256, srcRec.SourceLink.DigestSHA256)
					} else {
						sourceVideo = &videogen.ManagedVideoSource{
							Bytes:      srcRec.Bytes,
							MediaType:  srcRec.MediaType,
							Provenance: srcRec.Provenance,
							SourceLink: srcRec.SourceLink,
						}
						if srcRec.Provenance != nil {
							sourceVideo.InteractionID = srcRec.Provenance.InteractionID
							sourceVideo.URI = srcRec.Provenance.ProviderResource
							sourceVideo.Model = srcRec.Provenance.Model
						}
					}
				}
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
					t.Deliverables[i].Description = fmt.Sprintf("Source media error: %v", sourceErr)
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
				for i := range t.Deliverables {
					t.Deliverables[i].Status = "failed"
					t.Deliverables[i].Description = fmt.Sprintf("Model error: %v", modelErr)
				}
				return nil
			})
			return
		}

		if op == pebblestore.VideoOperationEdit && videogen.IsVeoModel(videoModel) {
			veoErr := errors.New("Veo models do not support video editing; use Gemini Omni for video editing or extend for Veo continuation")
			_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
				t.Status = "failed"
				t.LastError = veoErr.Error()
				t.ActionNeeded = fmt.Sprintf("Action Needed: %v", veoErr)
				t.WhatNotDone = []string{veoErr.Error()}
				for i := range t.Deliverables {
					t.Deliverables[i].Status = "failed"
					t.Deliverables[i].Description = fmt.Sprintf("Operation error: %v", veoErr)
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
				for i := range t.Deliverables {
					t.Deliverables[i].Status = "failed"
					t.Deliverables[i].Description = fmt.Sprintf("Video service error: %v", vgErr)
				}
				return nil
			})
			return
		}

		durSec := task.DurationSeconds
		if isOmniModel(videoModel) {
			durSec = 0
		}
		resTag := strings.TrimSpace(task.Resolution)
		if resTag == "" {
			resTag = "720p"
		}

		// Revalidate capabilities and references at execution before dispatching
		pfReq := videogen.VideoPreflightRequest{
			AccountScopeID:  p.AccountScopeID,
			Operation:       op,
			ExplicitModel:   videoModel,
			AspectRatio:     ar,
			Resolution:      resTag,
			DurationSeconds: durSec,
			Prompt:          prompt,
			Principal:       p,
			Source:          sourceVideo,
			SourceProvenance: func() *pebblestore.VideoProvenance {
				if sourceVideo != nil {
					return sourceVideo.Provenance
				}
				return nil
			}(),
			Image:        sourceImage,
			IsQueuedTask: true,
		}
		pfRes, pfErr := s.preflightVideoOperation(ctx, pfReq)
		if pfErr != nil {
			_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
				t.Status = "failed"
				t.LastError = pfErr.Error()
				t.ActionNeeded = fmt.Sprintf("Action Needed: %v", pfErr)
				t.WhatNotDone = []string{pfErr.Error()}
				for i := range t.Deliverables {
					t.Deliverables[i].Status = "failed"
					t.Deliverables[i].Description = fmt.Sprintf("Preflight error: %v", pfErr)
				}
				return nil
			})
			return
		}
		if pfRes != nil {
			if task.Model != "" && pfRes.ResolvedModel != "" && task.Model != pfRes.ResolvedModel {
				changeErr := fmt.Errorf("task execution model changed: task submitted with %q but preflight resolved %q", task.Model, pfRes.ResolvedModel)
				_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
					t.Status = "failed"
					t.LastError = changeErr.Error()
					t.ActionNeeded = fmt.Sprintf("Action Needed: %v", changeErr)
					t.WhatNotDone = []string{changeErr.Error()}
					for i := range t.Deliverables {
						t.Deliverables[i].Status = "failed"
						t.Deliverables[i].Description = fmt.Sprintf("Model mismatch error: %v", changeErr)
					}
					return nil
				})
				return
			}
			if task.Provider != "" && pfRes.ResolvedProvider != "" && !strings.EqualFold(task.Provider, pfRes.ResolvedProvider) {
				changeErr := fmt.Errorf("task execution provider changed: task submitted with %q but preflight resolved %q", task.Provider, pfRes.ResolvedProvider)
				_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
					t.Status = "failed"
					t.LastError = changeErr.Error()
					t.ActionNeeded = fmt.Sprintf("Action Needed: %v", changeErr)
					t.WhatNotDone = []string{changeErr.Error()}
					for i := range t.Deliverables {
						t.Deliverables[i].Status = "failed"
						t.Deliverables[i].Description = fmt.Sprintf("Provider mismatch error: %v", changeErr)
					}
					return nil
				})
				return
			}
			if pfRes.ResolvedModel != "" {
				videoModel = pfRes.ResolvedModel
			}
		}

		concurrency := 4
		if count < concurrency {
			concurrency = count
		}
		jobs := make(chan int, count)
		for i := 0; i < count; i++ {
			jobs <- i
		}
		close(jobs)

		var firstGenErr error
		var errMu sync.Mutex

		var wg sync.WaitGroup
		for w := 0; w < concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					slotIndex := i
					takeIdx := i + 1
					reqPrompt := prompt
					if count > 1 {
						if sourceMediaTitle != "" {
							reqPrompt = fmt.Sprintf("%s (iteration %d based on %s)", prompt, takeIdx, sourceMediaTitle)
						} else {
							reqPrompt = fmt.Sprintf("%s (take %d)", prompt, takeIdx)
						}
					}
					vReq := videogen.ManagedVideoRequest{
						Operation:       op,
						Model:           videoModel,
						Prompt:          reqPrompt,
						AspectRatio:     ar,
						Resolution:      resTag,
						DurationSeconds: durSec,
						Principal:       p,
						Source:          sourceVideo,
						SourceProvenance: func() *pebblestore.VideoProvenance {
							if sourceVideo != nil {
								return sourceVideo.Provenance
							}
							return nil
						}(),
						Image: sourceImage,
					}
					if sourceImage != nil {
						imageCopy := *sourceImage
						vReq.Image = &imageCopy
					}
					var vRes videogen.ManagedVideoResult
					var genErr error
					if sceneCount > 1 {
						vRes, genErr = generateProjectVideoStory(ctx, vg, vReq, task.Scenes)
					} else {
						vRes, genErr = vg.GenerateManagedVideo(ctx, vReq)
					}
					if genErr == nil && (len(vRes.Bytes) == 0 || !strings.HasPrefix(vRes.MediaType, "video/")) {
						genErr = errors.New("video provider returned no playable video")
					}
					if genErr != nil {
						errMu.Lock()
						if firstGenErr == nil {
							firstGenErr = genErr
						}
						errMu.Unlock()
					}

					_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
						if slotIndex < len(t.Deliverables) {
							if genErr != nil {
								t.Deliverables[slotIndex].Status = "failed"
								t.Deliverables[slotIndex].Description = fmt.Sprintf("Video generation failed: %v", genErr)
							} else {
								mime := vRes.MediaType
								if mime == "" {
									mime = "video/mp4"
								}
								mediaURL := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(vRes.Bytes))
								// Preserve the requested model on the task; report execution
								// identity on the individual result instead.
								if vRes.AspectRatio != "" {
									t.AspectRatio = vRes.AspectRatio
								}
								if vRes.Resolution != "" {
									t.Resolution = vRes.Resolution
								}
								if vRes.DurationSeconds > 0 {
									t.DurationSeconds = vRes.DurationSeconds
								}
								usedModel := videoExecutionIdentity(vRes.Provider, vRes.Model)
								durationStr := fmt.Sprintf("%ds", vRes.DurationSeconds)
								if vRes.DurationSeconds <= 0 {
									durationStr = fmt.Sprintf("%ds", durSec)
								}

								t.Deliverables[slotIndex].Status = "ready"
								t.Deliverables[slotIndex].MediaURL = mediaURL
								t.Deliverables[slotIndex].Thumbnail = mediaURL
								t.Deliverables[slotIndex].Duration = durationStr
								t.Deliverables[slotIndex].ParentDeliverableID = sourceMediaID
								t.Deliverables[slotIndex].SourceMediaRef = sourceMediaID
								t.Deliverables[slotIndex].VideoProvenance = vRes.Provenance
								t.Deliverables[slotIndex].Model = usedModel
								if vRes.Provenance != nil && vRes.Provenance.Model != "" {
									t.Deliverables[slotIndex].Model = videoExecutionIdentity(vRes.Provenance.Provider, vRes.Provenance.Model)
								}
								t.Deliverables[slotIndex].AspectRatio = vRes.AspectRatio
								t.Deliverables[slotIndex].Resolution = vRes.Resolution
								t.Deliverables[slotIndex].DurationSeconds = vRes.DurationSeconds
								if vRes.Provenance != nil {
									// Preflight-resolved request settings, not total playback duration.
									t.Deliverables[slotIndex].AspectRatio = vRes.Provenance.AspectRatio
									t.Deliverables[slotIndex].Resolution = vRes.Provenance.Resolution
									t.Deliverables[slotIndex].DurationSeconds = vRes.Provenance.DurationSeconds
								}
								t.VideoProvenance = vRes.Provenance

								if count > 1 {
									t.Deliverables[slotIndex].Title = fmt.Sprintf("%s (Take %d, %s)", t.Title, takeIdx, ar)
									desc := fmt.Sprintf("Video variation %d of %d (%s, %s, %s) generated via %s: %s", takeIdx, count, ar, resTag, durationStr, usedModel, t.Title)
									if sourceMediaTitle != "" {
										desc = fmt.Sprintf("Video variation %d of %d (%s, %s, %s) based on %s via %s: %s", takeIdx, count, ar, resTag, durationStr, sourceMediaTitle, usedModel, t.Title)
									}
									t.Deliverables[slotIndex].Description = desc
								} else {
									if op == pebblestore.VideoOperationExtend {
										t.Deliverables[0].Title = fmt.Sprintf("%s (Continued from %s)", t.Title, sourceMediaTitle)
										t.Deliverables[0].Description = fmt.Sprintf("Video continuation using %s from %s: %s", usedModel, sourceMediaTitle, t.Title)
									} else if op == pebblestore.VideoOperationEdit {
										t.Deliverables[0].Title = fmt.Sprintf("%s (Iteration from %s)", t.Title, sourceMediaTitle)
										t.Deliverables[0].Description = fmt.Sprintf("Video iteration using %s of %s: %s", usedModel, sourceMediaTitle, t.Title)
									} else if sourceMediaKind == "image" {
										t.Deliverables[0].Title = fmt.Sprintf("%s (Keyframe %s)", t.Title, sourceMediaTitle)
										t.Deliverables[0].Description = fmt.Sprintf("Video clip using %s from keyframe image %s: %s", usedModel, sourceMediaTitle, t.Title)
									} else if sceneCount <= 1 {
										t.Deliverables[0].Title = fmt.Sprintf("%s (Single Video, %s)", t.Title, ar)
										t.Deliverables[0].Duration = durationStr
										t.Deliverables[0].Description = fmt.Sprintf("Single video clip (%s, %s, %s) generated directly with %s: %s", ar, resTag, durationStr, usedModel, t.Title)
									} else {
										if soundtrack != "" {
											t.Deliverables[0].Description = fmt.Sprintf("Generated video clip using %s: %s", usedModel, t.Title)
										} else {
											t.Deliverables[0].Description = fmt.Sprintf("Generated video clip using %s: %s", usedModel, t.Title)
										}
									}
								}
							}
						}
						return nil
					})
				}
			}()
		}
		wg.Wait()

		if firstGenErr != nil || ctx.Err() != nil {
			errToReport := firstGenErr
			if errToReport == nil {
				errToReport = ctx.Err()
			}
			_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
				t.Status = "failed"
				t.LastError = errToReport.Error()
				t.ActionNeeded = fmt.Sprintf("Action Needed: %v", errToReport)
				t.WhatNotDone = []string{errToReport.Error()}
				for i := range t.Deliverables {
					t.Deliverables[i].Status = "failed"
					t.Deliverables[i].Description = fmt.Sprintf("Video generation failed: %v", errToReport)
					t.Deliverables[i].MediaURL = ""
					t.Deliverables[i].Thumbnail = ""
				}
				return nil
			})
			return
		}

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
			if t.AspectRatio == "" {
				t.AspectRatio = ar
			}
			if t.Resolution == "" {
				t.Resolution = resTag
			}
			if t.DurationSeconds == 0 {
				t.DurationSeconds = durSec
			}
			if t.Model == "" {
				t.Model = videoModel
			}

			if readyCount == 0 && len(t.Deliverables) > 0 {
				t.Status = "failed"
				errMsg := "all video deliverables failed generation"
				if firstGenErr != nil {
					errMsg = firstGenErr.Error()
				}
				t.LastError = errMsg
				t.ActionNeeded = fmt.Sprintf("Action Needed: Video generation failed (%v). Check settings or provider credentials.", errMsg)
				t.WhatNotDone = []string{fmt.Sprintf("Failed to generate video: %v", errMsg)}
			} else if readyCount > 0 {
				t.Status = "needs_review"
				if failedCount > 0 {
					t.LastError = fmt.Sprintf("%d of %d deliverables failed generation", failedCount, len(t.Deliverables))
					t.ActionNeeded = fmt.Sprintf("Action Needed: %d of %d video variation(s) ready for review (%d failed).", readyCount, len(t.Deliverables), failedCount)
				} else {
					t.LastError = ""
					if count > 1 {
						t.ActionNeeded = fmt.Sprintf("Action Needed: %d video deliverable(s) ready for review.", readyCount)
						t.WhatDidDo = []string{
							"Configured multi-take video parameters",
							fmt.Sprintf("Generated %d video variation(s) in parallel via swarm engine", readyCount),
						}
					} else if sourceMediaKind == "video" {
						if op == pebblestore.VideoOperationExtend {
							t.WhatDidDo = []string{
								fmt.Sprintf("Referenced prior video cut: %s", sourceMediaTitle),
								"Generated a continuation clip from the supplied source",
							}
							t.ActionNeeded = fmt.Sprintf("Action Needed: Next video scene ready for review (continued from %s).", sourceMediaTitle)
						} else if op == pebblestore.VideoOperationEdit {
							t.WhatDidDo = []string{
								fmt.Sprintf("Referenced source video: %s", sourceMediaTitle),
								"Generated a refined clip from the supplied source",
							}
							t.ActionNeeded = fmt.Sprintf("Action Needed: Fine-tuned video deliverable ready for review (based on %s).", sourceMediaTitle)
						} else {
							t.WhatDidDo = []string{
								fmt.Sprintf("Referenced source video: %s", sourceMediaTitle),
								"Generated a video iteration from the supplied source",
							}
							t.ActionNeeded = fmt.Sprintf("Action Needed: Video iteration ready for review (based on %s).", sourceMediaTitle)
						}
					} else if sourceMediaKind == "image" {
						t.WhatDidDo = []string{
							fmt.Sprintf("Ingested keyframe image: %s", sourceMediaTitle),
							"Generated a video clip from the supplied image",
						}
						t.ActionNeeded = fmt.Sprintf("Action Needed: Video story ready for review (from keyframe %s).", sourceMediaTitle)
					} else if sceneCount <= 1 {
						t.WhatDidDo = []string{
							fmt.Sprintf("Configured single video shot parameters (%s, %s, %ds)", ar, resTag, durSec),
							fmt.Sprintf("Rendered video clip directly with %s (one-prompt generation)", videoModel),
						}
						t.ActionNeeded = "Action Needed: Single video clip deliverable ready for review."
					} else {
						if soundtrack != "" {
							t.WhatDidDo = []string{"Generated a video clip from the supplied prompt"}
						} else {
							t.WhatDidDo = []string{"Generated a video clip from the supplied prompt"}
						}
						t.ActionNeeded = "Action Needed: Multi-part video deliverable ready for review."
					}
				}
			}
			return nil
		})
	} else if task.Agent == "sound" || task.Agent == "audio" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
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
		var genErr error
		var mediaURL string
		if s.audioGen != nil {
			res, err := s.audioGen.GenerateManagedAudio(ctx, audiogen.ManagedAudioRequest{
				Prompt:          prompt,
				DurationSeconds: durSeconds,
				Principal:       p,
			})
			if err != nil {
				genErr = err
			} else if len(res.Bytes) > 0 {
				mime := res.MediaType
				if mime == "" {
					mime = "audio/mp3"
				}
				mediaURL = fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(res.Bytes))
			} else {
				genErr = errors.New("empty audio generation response")
			}
		} else {
			genErr = errors.New("audio generation service not configured")
		}
		_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
			if len(t.Deliverables) > 0 {
				t.Deliverables[0].Status = "ready"
				t.Deliverables[0].MediaURL = mediaURL
				if genErr != nil || mediaURL == "" {
					t.Deliverables[0].Status = "failed"
					errMsg := "audio generation failed"
					if genErr != nil {
						errMsg = genErr.Error()
					}
					t.Deliverables[0].Description = fmt.Sprintf("Audio generation failed: %s", errMsg)
					t.Status = "failed"
					t.LastError = errMsg
					t.ActionNeeded = "Audio generation failed. Review error and retry."
					t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Audio generation failed: %s", errMsg))
					return nil
				}
				t.Deliverables[0].Thumbnail = "sound"
				t.Deliverables[0].Duration = fmt.Sprintf("%ds", durSeconds)
				t.Deliverables[0].Title = fmt.Sprintf("%s (%ds Audio Clip)", t.Title, durSeconds)
				t.Deliverables[0].Description = fmt.Sprintf("Generated %ds audio soundtrack using %s: %s", durSeconds, soundModel, prompt)
				t.Deliverables[0].Model = soundModel
				t.Deliverables[0].DurationSeconds = durSeconds
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

// videoExecutionIdentity labels adapter-reported execution, not the requested default
// or independently verified billing identity. Missing evidence stays explicit.
func videoExecutionIdentity(provider, modelID string) string {
	provider = strings.TrimSpace(provider)
	modelID = strings.TrimSpace(modelID)
	if provider == "" || modelID == "" {
		return "unknown execution identity"
	}
	if strings.HasPrefix(modelID, provider+":") {
		return modelID
	}
	return provider + ":" + modelID
}
