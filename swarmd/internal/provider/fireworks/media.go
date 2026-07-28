package fireworks

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	fireworksMediaAdapterID         = "fireworks-chat-completions-v1"
	fireworksMediaProviderSurface   = "chat_completions"
	fireworksMediaCredentialSurface = "fireworks_api_key"
	fireworksMaxEncodedImageBytes   = 10 << 20
	fireworksMaxImageBytes          = ((fireworksMaxEncodedImageBytes - 1) / 4) * 3
	fireworksMaxImageCount          = 30
)

var fireworksImageMIMETypes = []string{
	"image/bmp",
	"image/gif",
	"image/jpeg",
	"image/png",
	"image/tiff",
	"image/x-portable-pixmap",
}

func (r *Runner) MediaCapabilityDeclaration(ctx context.Context) (provideriface.MediaAdapterDeclaration, error) {
	record, err := r.activeCredential(ctx)
	if err != nil {
		return provideriface.MediaAdapterDeclaration{}, err
	}
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{record.AccountScopeID, record.Provider, record.ID, record.Type}, "\x00")))
	return provideriface.MediaAdapterDeclaration{
		AdapterID:             fireworksMediaAdapterID,
		ProviderID:            "fireworks",
		ProviderSurface:       fireworksMediaProviderSurface,
		CredentialSurface:     fireworksMediaCredentialSurface,
		CredentialFingerprint: hex.EncodeToString(fingerprint[:16]),
		Inputs: []provideriface.MediaAdapterCapability{{
			Modality:     "image",
			Semantics:    pebblestore.ModelCatalogMediaSemanticsNative,
			MIMETypes:    append([]string(nil), fireworksImageMIMETypes...),
			FileTypes:    []string{"bmp", "gif", "jpeg", "jpg", "png", "ppm", "tif", "tiff"},
			ContentTypes: []string{"image_url"},
			MaxBytes:     fireworksMaxImageBytes,
			MaxCount:     fireworksMaxImageCount,
		}},
	}, nil
}

func validateFireworksMediaSurface(contract provideriface.SessionMediaContract) error {
	if strings.TrimSpace(contract.Hash) == "" {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(contract.ProviderID), "fireworks") ||
		contract.ProviderSurface != fireworksMediaProviderSurface ||
		contract.CredentialSurface != fireworksMediaCredentialSurface ||
		contract.AdapterID != fireworksMediaAdapterID {
		return errors.New("media contract does not match the active Fireworks API-key Chat Completions surface")
	}
	return nil
}

type fireworksMediaRequestState struct {
	imageCount int
	totalBytes int64
}

func materializeFireworksMessageContent(req provideriface.Request, role string, raw any, state *fireworksMediaRequestState) (any, bool, error) {
	parts, structured, malformed := fireworksContentParts(raw)
	if malformed {
		return nil, false, errors.New("fireworks message content is malformed")
	}
	if !structured {
		text, ok := raw.(string)
		if !ok {
			return nil, false, errors.New("fireworks message content is malformed")
		}
		text = strings.TrimSpace(text)
		return text, text != "", nil
	}
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		partType, _ := stringField(part, "type")
		switch strings.ToLower(strings.TrimSpace(partType)) {
		case "input_text", "output_text", "text":
			text, ok := stringField(part, "text")
			if !ok || strings.TrimSpace(text) == "" {
				return nil, false, errors.New("fireworks text content block is malformed")
			}
			out = append(out, map[string]any{"type": "text", "text": strings.TrimSpace(text)})
		case "session_media":
			if role != "user" {
				return nil, false, errors.New("fireworks image input is only allowed in user messages")
			}
			payload, ok := part["media"].(provideriface.SessionMediaPayload)
			if !ok {
				return nil, false, errors.New("fireworks media input is malformed")
			}
			image, err := materializeFireworksImage(req.MediaContract, payload, state)
			if err != nil {
				return nil, false, err
			}
			out = append(out, image)
		default:
			return nil, false, errors.New("fireworks message content contains an unsupported block type")
		}
	}
	return out, len(out) != 0, nil
}

func fireworksContentParts(raw any) ([]map[string]any, bool, bool) {
	switch typed := raw.(type) {
	case []map[string]any:
		return typed, true, false
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			part, ok := item.(map[string]any)
			if !ok {
				return nil, true, true
			}
			out = append(out, part)
		}
		return out, true, false
	default:
		return nil, false, false
	}
}

func materializeFireworksImage(contract provideriface.SessionMediaContract, payload provideriface.SessionMediaPayload, state *fireworksMediaRequestState) (map[string]any, error) {
	if state == nil {
		return nil, errors.New("fireworks media request state is unavailable")
	}
	capability, err := validateFireworksImagePayload(contract, payload)
	if err != nil {
		return nil, err
	}
	nextCount := state.imageCount + 1
	nextBytes := state.totalBytes + int64(base64.StdEncoding.EncodedLen(len(payload.Bytes)))
	if nextCount > capability.MaxCount {
		return nil, errors.New("fireworks image input exceeds the active contract count limit")
	}
	if nextBytes >= fireworksMaxEncodedImageBytes {
		return nil, errors.New("fireworks image input exceeds the provider encoded-image limit")
	}
	state.imageCount = nextCount
	state.totalBytes = nextBytes
	encoded := base64.StdEncoding.EncodeToString(payload.Bytes)
	return map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "data:" + strings.ToLower(strings.TrimSpace(payload.MIMEType)) + ";base64," + encoded,
		},
	}, nil
}

func validateFireworksImagePayload(contract provideriface.SessionMediaContract, payload provideriface.SessionMediaPayload) (provideriface.MediaContractCapability, error) {
	if err := validateFireworksMediaSurface(contract); err != nil {
		return provideriface.MediaContractCapability{}, err
	}
	if strings.TrimSpace(contract.Hash) == "" {
		return provideriface.MediaContractCapability{}, errors.New("fireworks media contract is unavailable")
	}
	if len(payload.Bytes) == 0 || payload.Size <= 0 || int64(len(payload.Bytes)) != payload.Size || strings.TrimSpace(payload.AssetID) == "" || strings.TrimSpace(payload.DigestSHA256) == "" {
		return provideriface.MediaContractCapability{}, errors.New("fireworks media payload failed immutable size or identity validation")
	}
	digest := sha256.Sum256(payload.Bytes)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), strings.TrimSpace(payload.DigestSHA256)) {
		return provideriface.MediaContractCapability{}, errors.New("fireworks media payload failed immutable digest validation")
	}
	for _, capability := range contract.Capabilities {
		if capability.State != provideriface.MediaCapabilityStateAllowed || !strings.EqualFold(strings.TrimSpace(capability.Modality), "image") {
			continue
		}
		if capability.Semantics != pebblestore.ModelCatalogMediaSemanticsNative || !fireworksContains(capability.ContentTypes, "image_url") {
			break
		}
		if !fireworksContains(capability.MIMETypes, payload.MIMEType) || capability.MaxBytes <= 0 || payload.Size > capability.MaxBytes || capability.MaxCount <= 0 {
			break
		}
		if len(capability.FileTypes) > 0 && strings.TrimSpace(payload.FileType) != "" && !fireworksContains(capability.FileTypes, payload.FileType) {
			break
		}
		return capability, nil
	}
	return provideriface.MediaContractCapability{}, errors.New("fireworks media payload is denied by the active contract")
}

func fireworksContains(values []string, expected string) bool {
	expected = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(expected), "."))
	for _, value := range values {
		if strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), ".")) == expected {
			return true
		}
	}
	return false
}
