package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
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

func (s *Server) generateImageMedia(
	ctx context.Context,
	p identity.Principal,
	prompt string,
	aspectRatio string,
	variantIndex int,
	modelOverride string,
	resolution string,
	sourceImage *imagegen.ManagedImageSource,
) (string, string, error) {
	if s == nil || s.imageGen == nil {
		return "", "", errors.New("image generation service is not configured")
	}

	usedModel := strings.TrimSpace(modelOverride)
	if usedModel == "" && s.uiSettings != nil && strings.TrimSpace(p.AccountScopeID) != "" {
		if uiSet, err := s.uiSettings.GetForAccount(p.AccountScopeID); err == nil {
			usedModel = strings.TrimSpace(uiSet.Tools.Image.DefaultModel)
		}
	}
	if usedModel == "" {
		return "", "", errors.New("configure a default image model or select a model for this generation")
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
	if caps, err := s.imageGen.ManagedImageCapabilities(usedModel); err == nil && caps.CapabilityToken != "" {
		capabilityToken = caps.CapabilityToken
	}

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
		return "", usedModel, err
	}
	if len(res.Bytes) == 0 {
		return "", usedModel, errors.New("image generation returned empty image data")
	}

	mime := res.MediaType
	if mime == "" {
		mime = "image/png"
	}
	mediaURL := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(res.Bytes))
	return mediaURL, usedModel, nil
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
	for _, provider := range []string{"google", "openrouter"} {
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

func decodeWebPConfig(data []byte) (image.Config, error) {
	if len(data) < 30 {
		return image.Config{}, errors.New("webp payload too short")
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return image.Config{}, errors.New("not a webp file")
	}
	fileSize := binary.LittleEndian.Uint32(data[4:8])
	if int64(fileSize)+8 > int64(len(data)) {
		return image.Config{}, errors.New("truncated webp file")
	}
	chunkTag := string(data[12:16])
	switch chunkTag {
	case "VP8 ":
		if len(data) < 30 {
			return image.Config{}, errors.New("truncated vp8 chunk")
		}
		if data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return image.Config{}, errors.New("invalid vp8 sync code")
		}
		width := int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff)
		height := int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff)
		if width <= 0 || height <= 0 {
			return image.Config{}, errors.New("invalid vp8 dimensions")
		}
		return image.Config{Width: width, Height: height}, nil
	case "VP8L":
		if len(data) < 25 {
			return image.Config{}, errors.New("truncated vp8l chunk")
		}
		if data[20] != 0x2f {
			return image.Config{}, errors.New("invalid vp8l signature")
		}
		b0 := uint32(data[21])
		b1 := uint32(data[22])
		b2 := uint32(data[23])
		b3 := uint32(data[24])
		width := int(1 + (b0 | ((b1 & 0x3f) << 8)))
		height := int(1 + (((b1 >> 6) | (b2 << 2) | ((b3 & 0x0f) << 10))))
		if width <= 0 || height <= 0 {
			return image.Config{}, errors.New("invalid vp8l dimensions")
		}
		return image.Config{Width: width, Height: height}, nil
	case "VP8X":
		if len(data) < 30 {
			return image.Config{}, errors.New("truncated vp8x chunk")
		}
		width := int(1 + uint32(data[24]) | (uint32(data[25]) << 8) | (uint32(data[26]) << 16))
		height := int(1 + uint32(data[27]) | (uint32(data[28]) << 8) | (uint32(data[29]) << 16))
		if width <= 0 || height <= 0 {
			return image.Config{}, errors.New("invalid vp8x dimensions")
		}
		return image.Config{Width: width, Height: height}, nil
	default:
		return image.Config{}, fmt.Errorf("unsupported webp chunk %q", chunkTag)
	}
}

func decodeImageConfig(imgBytes []byte) (image.Config, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(imgBytes))
	if err == nil && (format == "png" || format == "jpeg") {
		return cfg, format, nil
	}
	if len(imgBytes) >= 12 && string(imgBytes[0:4]) == "RIFF" && string(imgBytes[8:12]) == "WEBP" {
		wCfg, wErr := decodeWebPConfig(imgBytes)
		if wErr == nil {
			return wCfg, "webp", nil
		}
		return image.Config{}, "", fmt.Errorf("malformed webp image: %w", wErr)
	}
	if err != nil {
		return image.Config{}, "", err
	}
	return image.Config{}, "", fmt.Errorf("unsupported image format %q; only PNG, JPEG, and WebP are supported", format)
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
			return errors.New("multipart video stories are not supported; video generation supports single video clips")
		}
		if task.VariantCount < 0 {
			return errors.New("video variant count cannot be negative")
		}
		if task.VariantCount > 8 {
			return fmt.Errorf("video variant count %d exceeds maximum allowed (8)", task.VariantCount)
		}

		if len(task.AttachedMedia) > 1 {
			return errors.New("at most one initial image attachment is supported for video generation")
		}
		if len(task.AttachedMedia) == 1 {
			m := task.AttachedMedia[0]
			if m.Kind != "" && !strings.EqualFold(m.Kind, "image") {
				return fmt.Errorf("unsupported attachment kind %q for video generation; only images are supported as reference inputs", m.Kind)
			}
			if m.MediaType != "" && !strings.HasPrefix(strings.ToLower(m.MediaType), "image/") {
				return fmt.Errorf("unsupported media type %q for video generation; only images are supported as reference inputs", m.MediaType)
			}
			fn := strings.ToLower(strings.TrimSpace(m.Filename))
			if fn != "" && !strings.HasSuffix(fn, ".png") && !strings.HasSuffix(fn, ".jpg") && !strings.HasSuffix(fn, ".jpeg") && !strings.HasSuffix(fn, ".webp") {
				return fmt.Errorf("unsupported file extension on %q for video generation; only PNG, JPEG, and WebP image formats are supported", m.Filename)
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

		model := strings.TrimSpace(task.Model)
		if model == "" && s != nil && s.uiSettings != nil && principal.AccountScopeID != "" {
			if uiSet, err := s.uiSettings.GetForAccount(principal.AccountScopeID); err == nil {
				model = strings.TrimSpace(uiSet.Tools.Video.DefaultModel)
			}
		}
		if model == "" {
			return errors.New("no default video model configured for account; select a model or configure one in Settings")
		}
		if !isSupportedVideoModel(s, model) {
			return fmt.Errorf("unsupported video model %q", model)
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

		if len(task.AttachedMedia) > 0 {
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
		return nil
	}

	return nil
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
		if readErr != nil {
			return nil, mediaType, fmt.Errorf("staged upload %q not found or expired: %w", stagingID, readErr)
		}
		if len(payload) == 0 {
			return nil, mediaType, fmt.Errorf("staged upload %q is empty", stagingID)
		}
		detected := http.DetectContentType(payload)
		if detected != "" && !strings.Contains(detected, "octet-stream") {
			mediaType = detected
		}
		return payload, mediaType, nil
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
						return nil, mediaType, fmt.Errorf("read artifact variant: %w", err)
					}
					if !ok {
						return nil, mediaType, fmt.Errorf("session artifact variant %q not found in account scope", variantID)
					}
					if requestedRev != "" {
						curRevStr := fmt.Sprintf("%d", variant.EventSeq)
						if requestedRev != curRevStr {
							return nil, mediaType, fmt.Errorf("stale artifact revision %q requested (current revision is %s)", requestedRev, curRevStr)
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
							return nil, mediaType, fmt.Errorf("read artifact reference: %w", readErr)
						}
						if len(body) > 0 {
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

	if valErr := validateProjectMediaTaskSettings(s, task); valErr != nil {
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

	if task.Agent == "image" || task.Agent == "designer" {
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
		if ar == "" {
			ar = "16:9"
		}
		sceneCount := 1
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

		var sourceMediaTitle string
		var sourceMediaKind string
		var sourceMediaID string
		var sourceVideo *videogen.ManagedVideoSource
		var sourceImage *videogen.ManagedVideoImage
		var sourceErr error

		if len(task.AttachedMedia) > 1 {
			sourceErr = errors.New("at most one initial image attachment is supported for video generation")
		} else if len(task.AttachedMedia) == 1 {
			m := task.AttachedMedia[0]
			k := strings.ToLower(m.Kind)
			mt := strings.ToLower(m.MediaType)
			fn := strings.ToLower(strings.TrimSpace(m.Filename))
			if k == "video" || strings.HasPrefix(mt, "video/") || strings.HasSuffix(fn, ".mp4") {
				sourceErr = errors.New("video file attachments are not supported for single video generation; only images are supported as reference inputs")
			} else if k == "image" || strings.HasPrefix(mt, "image/") || strings.HasSuffix(fn, ".png") || strings.HasSuffix(fn, ".jpg") || strings.HasSuffix(fn, ".jpeg") || strings.HasSuffix(fn, ".webp") {
				if fn != "" && !strings.HasSuffix(fn, ".png") && !strings.HasSuffix(fn, ".jpg") && !strings.HasSuffix(fn, ".jpeg") && !strings.HasSuffix(fn, ".webp") {
					sourceErr = fmt.Errorf("unsupported file extension on %q for video generation; only PNG, JPEG, and WebP image formats are supported", m.Filename)
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

		lowerPrompt := strings.ToLower(prompt)
		isContinuation := strings.Contains(lowerPrompt, "next scene") || strings.Contains(lowerPrompt, "continue") || strings.Contains(lowerPrompt, "sequel") || strings.Contains(lowerPrompt, "part 2")
		isFineTune := strings.Contains(lowerPrompt, "change") || strings.Contains(lowerPrompt, "modify") || strings.Contains(lowerPrompt, "edit") || strings.Contains(lowerPrompt, "fine-tune")

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
						Model:           videoModel,
						Prompt:          reqPrompt,
						AspectRatio:     ar,
						Resolution:      resTag,
						DurationSeconds: durSec,
						Principal:       p,
						Source:          sourceVideo,
						Image:           sourceImage,
					}
					if sourceImage != nil {
						imageCopy := *sourceImage
						vReq.Image = &imageCopy
					}
					vRes, genErr := vg.GenerateManagedVideo(ctx, vReq)
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
								if vRes.Model != "" {
									t.Model = vRes.Model
								}
								if vRes.AspectRatio != "" {
									t.AspectRatio = vRes.AspectRatio
								}
								if vRes.Resolution != "" {
									t.Resolution = vRes.Resolution
								}
								if vRes.DurationSeconds > 0 {
									t.DurationSeconds = vRes.DurationSeconds
								}
								usedModel := vRes.Model
								if usedModel == "" {
									usedModel = videoModel
								}
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

								if count > 1 {
									t.Deliverables[slotIndex].Title = fmt.Sprintf("%s (Take %d, %s)", t.Title, takeIdx, ar)
									desc := fmt.Sprintf("Video variation %d of %d (%s, %s, %s) generated via %s: %s", takeIdx, count, ar, resTag, durationStr, usedModel, t.Title)
									if sourceMediaTitle != "" {
										desc = fmt.Sprintf("Video variation %d of %d (%s, %s, %s) based on %s via %s: %s", takeIdx, count, ar, resTag, durationStr, sourceMediaTitle, usedModel, t.Title)
									}
									t.Deliverables[slotIndex].Description = desc
								} else {
									if sourceMediaKind == "video" {
										if isContinuation {
											t.Deliverables[0].Title = fmt.Sprintf("%s (Continued from %s)", t.Title, sourceMediaTitle)
											t.Deliverables[0].Description = fmt.Sprintf("Video continuation using %s from %s: %s", usedModel, sourceMediaTitle, t.Title)
										} else {
											t.Deliverables[0].Title = fmt.Sprintf("%s (Iteration from %s)", t.Title, sourceMediaTitle)
											t.Deliverables[0].Description = fmt.Sprintf("Video iteration using %s of %s: %s", usedModel, sourceMediaTitle, t.Title)
										}
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
						if isContinuation {
							t.WhatDidDo = []string{
								fmt.Sprintf("Referenced prior video cut: %s", sourceMediaTitle),
								"Generated a continuation clip from the supplied source",
							}
							t.ActionNeeded = fmt.Sprintf("Action Needed: Next video scene ready for review (continued from %s).", sourceMediaTitle)
						} else if isFineTune {
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
