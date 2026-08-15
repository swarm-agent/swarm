package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videotranscription"
)

const (
	googleFileAPIBase          = "https://generativelanguage.googleapis.com"
	maxGoogleTranscriptBytes   = 4 << 20
	maxGoogleProviderErrorBody = 32 << 10
	googleFilePollInterval     = 750 * time.Millisecond
	googleFilePollAttempts     = 80
)

type VideoTranscriptionAdapter struct {
	authStore  *pebblestore.AuthStore
	httpClient *http.Client
	baseURL    string
}

func NewVideoTranscriptionAdapter(authStore *pebblestore.AuthStore) *VideoTranscriptionAdapter {
	return &VideoTranscriptionAdapter{
		authStore: authStore, baseURL: googleFileAPIBase,
		httpClient: &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

type googleUploadedFile struct {
	Name       string `json:"name"`
	URI        string `json:"uri"`
	State      string `json:"state"`
	Expiration string `json:"expiration_time"`
	Error      any    `json:"error,omitempty"`
}

type googleFileResponse struct {
	File googleUploadedFile `json:"file"`
}

func (a *VideoTranscriptionAdapter) Transcribe(ctx context.Context, request videotranscription.TranscribeRequest) (videotranscription.GeneratedTranscript, error) {
	if a == nil || a.authStore == nil || request.Source == nil {
		return videotranscription.GeneratedTranscript{}, errors.New("google video transcription adapter is not configured")
	}
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok || principal.AccountScopeID != strings.TrimSpace(request.AccountScopeID) {
		return videotranscription.GeneratedTranscript{}, errors.New("google video transcription requires authenticated account authority")
	}
	credential, ok, err := a.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "google")
	if err != nil {
		return videotranscription.GeneratedTranscript{}, err
	}
	if !ok || strings.TrimSpace(credential.APIKey) == "" {
		return videotranscription.GeneratedTranscript{}, errors.New("google video transcription credential is unavailable")
	}
	if request.SizeBytes <= 0 || request.SizeBytes > pebblestore.SessionVideoAttachmentMaxBytes || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(request.MIMEType)), "video/") {
		return videotranscription.GeneratedTranscript{}, errors.New("video source exceeds the supported upload contract")
	}
	file, err := a.upload(ctx, credential.APIKey, request)
	if err != nil {
		return videotranscription.GeneratedTranscript{}, err
	}
	defer a.delete(context.WithoutCancel(ctx), credential.APIKey, file.Name)
	file, err = a.waitUntilActive(ctx, credential.APIKey, file)
	if err != nil {
		return videotranscription.GeneratedTranscript{}, err
	}
	payload, err := a.generate(ctx, credential.APIKey, request.Model, request.MIMEType, file.URI, request.Prompt)
	if err != nil {
		return videotranscription.GeneratedTranscript{}, err
	}
	return parseGoogleTranscript(payload)
}

func (a *VideoTranscriptionAdapter) upload(ctx context.Context, apiKey string, request videotranscription.TranscribeRequest) (googleUploadedFile, error) {
	metadata, _ := json.Marshal(map[string]any{"file": map[string]string{"display_name": "swarm-video-transcription"}})
	start, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.baseURL, "/")+"/upload/v1beta/files", bytes.NewReader(metadata))
	if err != nil {
		return googleUploadedFile{}, err
	}
	start.Header.Set(googleAPIKeyHeader, apiKey)
	start.Header.Set("Content-Type", "application/json")
	start.Header.Set("X-Goog-Upload-Protocol", "resumable")
	start.Header.Set("X-Goog-Upload-Command", "start")
	start.Header.Set("X-Goog-Upload-Header-Content-Length", fmt.Sprint(request.SizeBytes))
	start.Header.Set("X-Goog-Upload-Header-Content-Type", request.MIMEType)
	response, err := a.client().Do(start)
	if err != nil {
		return googleUploadedFile{}, errors.New("google temporary video upload could not start")
	}
	io.Copy(io.Discard, io.LimitReader(response.Body, maxGoogleProviderErrorBody))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return googleUploadedFile{}, fmt.Errorf("google temporary video upload start failed status=%d", response.StatusCode)
	}
	uploadURL := strings.TrimSpace(response.Header.Get("X-Goog-Upload-URL"))
	if uploadURL == "" {
		return googleUploadedFile{}, errors.New("google temporary video upload did not return a resumable target")
	}
	if _, err := request.Source.Seek(0, io.SeekStart); err != nil {
		return googleUploadedFile{}, errors.New("video source could not be rewound for upload")
	}
	upload, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, io.LimitReader(request.Source, request.SizeBytes+1))
	if err != nil {
		return googleUploadedFile{}, err
	}
	upload.Header.Set("Content-Type", request.MIMEType)
	upload.Header.Set("Content-Length", fmt.Sprint(request.SizeBytes))
	upload.Header.Set("X-Goog-Upload-Offset", "0")
	upload.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	response, err = a.client().Do(upload)
	if err != nil {
		return googleUploadedFile{}, errors.New("google temporary video upload failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(response.Body, maxGoogleProviderErrorBody))
		return googleUploadedFile{}, fmt.Errorf("google temporary video upload failed status=%d", response.StatusCode)
	}
	var result googleFileResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxGoogleProviderErrorBody)).Decode(&result); err != nil || result.File.Name == "" || result.File.URI == "" {
		return googleUploadedFile{}, errors.New("google temporary video upload returned an invalid file reference")
	}
	return result.File, nil
}

func (a *VideoTranscriptionAdapter) waitUntilActive(ctx context.Context, apiKey string, file googleUploadedFile) (googleUploadedFile, error) {
	for attempt := 0; attempt < googleFilePollAttempts; attempt++ {
		switch strings.ToUpper(strings.TrimSpace(file.State)) {
		case "ACTIVE":
			return file, nil
		case "FAILED":
			return googleUploadedFile{}, errors.New("google temporary video processing failed")
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return googleUploadedFile{}, ctx.Err()
			case <-time.After(googleFilePollInterval):
			}
		}
		endpoint := strings.TrimRight(a.baseURL, "/") + "/v1beta/" + strings.TrimLeft(file.Name, "/")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return googleUploadedFile{}, err
		}
		req.Header.Set(googleAPIKeyHeader, apiKey)
		response, err := a.client().Do(req)
		if err != nil {
			if attempt < 2 {
				continue
			}
			return googleUploadedFile{}, errors.New("google temporary video processing status is unavailable")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			if attempt < 2 && response.StatusCode >= 500 {
				continue
			}
			return googleUploadedFile{}, fmt.Errorf("google temporary video status failed status=%d", response.StatusCode)
		}
		var result googleFileResponse
		err = json.NewDecoder(io.LimitReader(response.Body, maxGoogleProviderErrorBody)).Decode(&result)
		response.Body.Close()
		if err != nil {
			return googleUploadedFile{}, errors.New("google temporary video status was malformed")
		}
		file = result.File
	}
	return googleUploadedFile{}, errors.New("google temporary video processing exceeded the bounded polling window")
}

func (a *VideoTranscriptionAdapter) generate(ctx context.Context, apiKey, model, mimeType, fileURI, prompt string) ([]byte, error) {
	model = strings.TrimSpace(strings.TrimPrefix(model, "models/"))
	if model == "" {
		return nil, errors.New("google video transcription model is required")
	}
	body := map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{
			map[string]any{"file_data": map[string]string{"mime_type": mimeType, "file_uri": fileURI}},
			map[string]string{"text": prompt},
		}}},
		"generationConfig": map[string]any{"responseMimeType": "application/json", "temperature": 0},
	}
	raw, _ := json.Marshal(body)
	endpoint := strings.TrimRight(a.baseURL, "/") + "/v1beta/models/" + url.PathEscape(model) + ":generateContent"
	var lastStatus int
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set(googleAPIKeyHeader, apiKey)
		req.Header.Set("Content-Type", "application/json")
		response, err := a.client().Do(req)
		if err != nil {
			if attempt < 2 {
				continue
			}
			return nil, errors.New("google video transcription generation failed")
		}
		lastStatus = response.StatusCode
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxGoogleTranscriptBytes+1))
		response.Body.Close()
		if readErr != nil {
			return nil, errors.New("google video transcription response could not be read")
		}
		if len(payload) > maxGoogleTranscriptBytes {
			return nil, errors.New("google video transcription response exceeded the bounded limit")
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return payload, nil
		}
		if attempt < 2 && (response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
			}
			continue
		}
		break
	}
	return nil, fmt.Errorf("google video transcription generation failed status=%d", lastStatus)
}

func (a *VideoTranscriptionAdapter) delete(ctx context.Context, apiKey, name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	deleteCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(deleteCtx, http.MethodDelete, strings.TrimRight(a.baseURL, "/")+"/v1beta/"+strings.TrimLeft(name, "/"), nil)
	if err != nil {
		return
	}
	req.Header.Set(googleAPIKeyHeader, apiKey)
	response, err := a.client().Do(req)
	if err == nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, maxGoogleProviderErrorBody))
		response.Body.Close()
	}
}

func (a *VideoTranscriptionAdapter) client() *http.Client {
	if a.httpClient != nil {
		return a.httpClient
	}
	return &http.Client{Timeout: 90 * time.Second}
}

func parseGoogleTranscript(payload []byte) (videotranscription.GeneratedTranscript, error) {
	var envelope struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || len(envelope.Candidates) == 0 {
		return videotranscription.GeneratedTranscript{}, errors.New("google video transcription response was malformed")
	}
	var text string
	for _, part := range envelope.Candidates[0].Content.Parts {
		text += part.Text
	}
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	var structured struct {
		Text       string                                    `json:"text"`
		Language   string                                    `json:"language"`
		DurationMs int64                                     `json:"duration_ms"`
		Segments   []pebblestore.NormalizedTranscriptSegment `json:"segments"`
	}
	if err := json.Unmarshal([]byte(text), &structured); err != nil {
		return videotranscription.GeneratedTranscript{}, errors.New("google video transcription output did not match the structured schema")
	}
	structured.Text = strings.TrimSpace(structured.Text)
	if structured.Text == "" {
		return videotranscription.GeneratedTranscript{}, errors.New("google video transcription output contained no transcript text")
	}
	partial := len(structured.Segments) == 0
	previousEnd := int64(0)
	for _, segment := range structured.Segments {
		if segment.StartMs < previousEnd || segment.EndMs <= segment.StartMs || strings.TrimSpace(segment.Text) == "" {
			partial = true
			break
		}
		previousEnd = segment.EndMs
	}
	return videotranscription.GeneratedTranscript{Text: structured.Text, Segments: structured.Segments, Language: strings.TrimSpace(structured.Language), DurationMs: structured.DurationMs, Partial: partial}, nil
}
