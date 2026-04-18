package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	capabilityTagChat     = "cap:chat"
	capabilityTagTTS      = "cap:tts"
	capabilityTagSTT      = "cap:stt"
	capabilityTagImageGen = "cap:imagegen"
)

func inferCredentialCapabilityTags(provider, authType, apiKey string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	authType = strings.ToLower(strings.TrimSpace(authType))
	apiKey = strings.TrimSpace(apiKey)
	if provider != "google" || authType != "api" || apiKey == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tags, err := detectGoogleCapabilityTags(ctx, apiKey)
	if err != nil {
		return nil
	}
	return tags
}

func detectGoogleCapabilityTags(ctx context.Context, apiKey string) ([]string, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	tags := make([]string, 0, 4)

	geminiStatus, _, err := doGet(ctx, client, "https://generativelanguage.googleapis.com/v1/models?key="+apiKey)
	if err != nil {
		return nil, err
	}
	if isInvalidAPIKeyStatus(geminiStatus) {
		return nil, io.EOF
	}
	if geminiStatus == http.StatusOK {
		tags = append(tags, capabilityTagChat, capabilityTagImageGen)
	}

	ttsStatus, _, err := doGet(ctx, client, "https://texttospeech.googleapis.com/v1/voices?key="+apiKey)
	if err == nil {
		if isInvalidAPIKeyStatus(ttsStatus) {
			return nil, io.EOF
		}
		if ttsStatus == http.StatusOK {
			tags = append(tags, capabilityTagTTS)
		}
	}

	sttStatus, _, err := doPostJSON(ctx, client, "https://speech.googleapis.com/v1/speech:recognize?key="+apiKey, map[string]any{
		"config": map[string]any{
			"languageCode": "en-US",
		},
		"audio": map[string]any{},
	})
	if err == nil {
		if sttStatus == http.StatusUnauthorized {
			return nil, io.EOF
		}
		if sttStatus == http.StatusOK || sttStatus == http.StatusBadRequest {
			tags = append(tags, capabilityTagSTT)
		}
	}

	if len(tags) == 0 {
		return nil, nil
	}
	tags = normalizeCapabilityTags(tags)
	return tags, nil
}

func doGet(ctx context.Context, client *http.Client, endpoint string) (status int, body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", "swarmd/0")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

func doPostJSON(ctx context.Context, client *http.Client, endpoint string, payload any) (status int, body []byte, err error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "swarmd/0")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

func isInvalidAPIKeyStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusBadRequest
}

func normalizeCapabilityTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, raw := range tags {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}
