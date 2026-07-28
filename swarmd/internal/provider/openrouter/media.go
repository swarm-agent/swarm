package openrouter

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	openRouterMediaAdapterID         = "openrouter-chat-completions-v1"
	openRouterMediaProviderSurface   = "chat_completions"
	openRouterMediaCredentialSurface = "openrouter_api_key"
	openRouterMaxImageBytes    int64 = 20 << 20
	openRouterMaxImageCount          = 20
)

var openRouterImageMIMETypes = []string{"image/gif", "image/jpeg", "image/png", "image/webp"}

var _ provideriface.MediaCapabilityRunner = (*Runner)(nil)

func (r *Runner) MediaCapabilityDeclaration(ctx context.Context) (provideriface.MediaAdapterDeclaration, error) {
	record, err := r.activeCredential(ctx)
	if err != nil {
		return provideriface.MediaAdapterDeclaration{}, err
	}
	return openRouterMediaDeclaration(record), nil
}

func openRouterMediaDeclaration(record pebblestore.AuthCredentialRecord) provideriface.MediaAdapterDeclaration {
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{record.AccountScopeID, record.Provider, record.ID, record.Type}, "\x00")))
	return provideriface.MediaAdapterDeclaration{
		AdapterID:             openRouterMediaAdapterID,
		ProviderID:            "openrouter",
		ProviderSurface:       openRouterMediaProviderSurface,
		CredentialSurface:     openRouterMediaCredentialSurface,
		CredentialFingerprint: hex.EncodeToString(fingerprint[:16]),
		Inputs: []provideriface.MediaAdapterCapability{{
			Modality:     "image",
			Semantics:    pebblestore.ModelCatalogMediaSemanticsNative,
			MIMETypes:    append([]string(nil), openRouterImageMIMETypes...),
			ContentTypes: []string{"image_url"},
			MaxBytes:     openRouterMaxImageBytes,
			MaxCount:     openRouterMaxImageCount,
		}},
	}
}

func validateOpenRouterMediaSurface(contract provideriface.SessionMediaContract) error {
	if strings.TrimSpace(contract.Hash) == "" {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(contract.ProviderID), "openrouter") ||
		contract.ProviderSurface != openRouterMediaProviderSurface ||
		contract.CredentialSurface != openRouterMediaCredentialSurface ||
		contract.AdapterID != openRouterMediaAdapterID {
		return errors.New("media contract does not match the active OpenRouter API-key Chat Completions surface")
	}
	return nil
}

func buildOpenRouterMessageContent(content any, req provideriface.Request, counts map[string]int) (any, bool, error) {
	switch typed := content.(type) {
	case string:
		text := strings.TrimSpace(typed)
		return text, text != "", nil
	case []map[string]any:
		items := make([]any, len(typed))
		for i := range typed {
			items[i] = typed[i]
		}
		return buildOpenRouterContentItems(items, req, counts)
	case []any:
		return buildOpenRouterContentItems(typed, req, counts)
	case nil:
		return nil, false, nil
	default:
		return nil, false, errors.New("OpenRouter message content has an unsupported shape")
	}
}

func buildOpenRouterContentItems(items []any, req provideriface.Request, counts map[string]int) (any, bool, error) {
	out := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, false, errors.New("OpenRouter message content item is malformed")
		}
		itemType, _ := stringField(item, "type")
		switch strings.ToLower(strings.TrimSpace(itemType)) {
		case "input_text", "output_text", "text":
			text, ok := stringField(item, "text")
			if !ok {
				return nil, false, errors.New("OpenRouter text content item is malformed")
			}
			if text = strings.TrimSpace(text); text != "" {
				out = append(out, map[string]any{"type": "text", "text": text})
			}
		case "session_media":
			payload, err := openRouterSessionMediaPayload(item["media"])
			if err != nil {
				return nil, false, err
			}
			image, err := materializeOpenRouterImage(req, payload, counts)
			if err != nil {
				return nil, false, err
			}
			out = append(out, image)
		case "input_image", "image_url", "file", "input_file", "input_audio", "audio", "video_url", "video":
			return nil, false, errors.New("OpenRouter media must use an authorized immutable session_media payload")
		default:
			return nil, false, fmt.Errorf("OpenRouter message content type %q is not implemented", strings.TrimSpace(itemType))
		}
	}
	return out, len(out) > 0, nil
}

func openRouterSessionMediaPayload(value any) (provideriface.SessionMediaPayload, error) {
	switch typed := value.(type) {
	case provideriface.SessionMediaPayload:
		return typed, nil
	case *provideriface.SessionMediaPayload:
		if typed != nil {
			return *typed, nil
		}
	}
	return provideriface.SessionMediaPayload{}, errors.New("OpenRouter session media payload is malformed")
}

func materializeOpenRouterImage(req provideriface.Request, payload provideriface.SessionMediaPayload, counts map[string]int) (map[string]any, error) {
	contract := req.MediaContract
	if strings.TrimSpace(contract.Hash) == "" || strings.TrimSpace(req.ProviderConfigurationHash) == "" {
		return nil, errors.New("OpenRouter image input requires an active media contract")
	}
	if err := validateOpenRouterMediaSurface(contract); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(payload.Modality), "image") {
		return nil, errors.New("OpenRouter media modality is not implemented")
	}
	mimeType := strings.ToLower(strings.TrimSpace(payload.MIMEType))
	if !openRouterStringAllowed(openRouterImageMIMETypes, mimeType) {
		return nil, errors.New("OpenRouter image MIME type is not implemented")
	}
	if len(payload.Bytes) == 0 || payload.Size <= 0 || int64(len(payload.Bytes)) != payload.Size ||
		strings.TrimSpace(payload.AssetID) == "" || strings.TrimSpace(payload.DigestSHA256) == "" {
		return nil, errors.New("OpenRouter image payload failed immutable size or identity validation")
	}
	digest := sha256.Sum256(payload.Bytes)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), strings.TrimSpace(payload.DigestSHA256)) {
		return nil, errors.New("OpenRouter image payload failed immutable digest validation")
	}
	for _, capability := range contract.Capabilities {
		if capability.State != provideriface.MediaCapabilityStateAllowed || !strings.EqualFold(strings.TrimSpace(capability.Modality), "image") {
			continue
		}
		if capability.Semantics != pebblestore.ModelCatalogMediaSemanticsNative ||
			!openRouterStringAllowed(capability.MIMETypes, mimeType) ||
			!openRouterStringAllowed(capability.ContentTypes, "image_url") ||
			capability.MaxBytes <= 0 || capability.MaxBytes > openRouterMaxImageBytes || payload.Size > capability.MaxBytes ||
			capability.MaxCount <= 0 || capability.MaxCount > openRouterMaxImageCount {
			break
		}
		counts["image"]++
		if counts["image"] > capability.MaxCount {
			return nil, errors.New("OpenRouter image input exceeds the active contract count limit")
		}
		encoded := base64.StdEncoding.EncodeToString(payload.Bytes)
		return map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:" + mimeType + ";base64," + encoded,
			},
		}, nil
	}
	return nil, errors.New("OpenRouter image input is denied by the active contract")
}

func openRouterStringAllowed(allowed []string, value string) bool {
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}
