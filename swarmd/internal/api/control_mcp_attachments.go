package api

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"strings"
)

// controlMCPMaxAttachments is how many files one message may carry (Swarm's
// per-message media limit).
const controlMCPMaxAttachments = 8

// uploadAttachments uploads a message's files to the session and returns the
// references the message carries.
func (c *controlMCPCall) uploadAttachments(sessionID string, raw any) ([]map[string]any, error) {
	items := controlMCPList(raw)
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > controlMCPMaxAttachments {
		return nil, controlMCPToolFailure("at most %d attachments per message", controlMCPMaxAttachments)
	}
	references := make([]map[string]any, 0, len(items))
	for i, item := range items {
		file := controlMCPMap(item)
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(controlMCPString(file, "data_base64")))
		if err != nil || len(data) == 0 {
			return nil, controlMCPToolFailure("attachment %d is not base64 file data", i+1)
		}
		mimeType := controlMCPString(file, "mime_type")
		if mimeType == "" {
			mimeType = http.DetectContentType(data)
		}
		sub, err := http.NewRequestWithContext(c.request.Context(), http.MethodPost, controlMCPSessionPath(sessionID, "media"), bytes.NewReader(data))
		if err != nil {
			return nil, controlMCPToolFailure("invalid request target")
		}
		sub.Host, sub.RemoteAddr = c.request.Host, c.request.RemoteAddr
		sub.Header.Set("Content-Type", mimeType)
		if name := controlMCPString(file, "filename"); name != "" {
			sub.Header.Set("X-Swarm-Media-Filename", name)
		}
		response, err := c.serve(c.next, sub)
		if err != nil {
			return nil, err
		}
		asset := controlMCPMap(response["asset"])
		detected, _ := asset["detected_mime_type"].(string)
		if detected == "" {
			detected, _ = asset["declared_mime_type"].(string)
		}
		reference := controlMCPPick(asset, "modality", "file_type", "file_name", "size", "digest_sha256", "contract_hash")
		reference["asset_id"] = asset["id"]
		reference["mime_type"] = detected
		references = append(references, reference)
	}
	return references, nil
}
