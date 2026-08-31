package swarm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	ActivationReportURL = "https://swarmagent.dev/api/activation"
	FeedbackReportURL   = "https://swarmagent.dev/api/feedback"
	publicAPITimeout    = 5 * time.Second
	publicAPIMaxBody    = 1024
)

type FeedbackInput struct {
	Category string
	Message  string
	FormTime int64
}

type PublicAPIClient struct {
	service       *Service
	client        *http.Client
	activationURL string
	feedbackURL   string
}

func NewPublicAPIClient(service *Service) *PublicAPIClient {
	return newPublicAPIClient(service, ActivationReportURL, FeedbackReportURL, nil)
}

func newPublicAPIClient(service *Service, activationURL, feedbackURL string, client *http.Client) *PublicAPIClient {
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.Timeout = publicAPITimeout
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &PublicAPIClient{service: service, client: &bounded, activationURL: activationURL, feedbackURL: feedbackURL}
}

func (c *PublicAPIClient) ReportActivation(ctx context.Context, event string) error {
	if event != "onboarding_completed" && event != "first_successful_run" {
		return errors.New("invalid activation event")
	}
	return c.post(ctx, c.activationURL, map[string]any{"version": 1, "event": event}, true, http.StatusAccepted)
}

func (c *PublicAPIClient) SubmitFeedback(ctx context.Context, input FeedbackInput) error {
	category := strings.TrimSpace(input.Category)
	if category != "bug" && category != "general" && category != "feature" {
		return errors.New("invalid feedback category")
	}
	message := strings.TrimSpace(input.Message)
	if len([]rune(message)) < 3 || len([]rune(message)) > 4000 {
		return errors.New("feedback must be between 3 and 4,000 characters")
	}
	formTime := input.FormTime
	if formTime <= 0 {
		formTime = time.Now().UnixMilli()
	}
	return c.post(ctx, c.feedbackURL, map[string]any{"category": category, "message": message, "form_time": formTime}, true, http.StatusOK)
}

func (c *PublicAPIClient) post(ctx context.Context, rawURL string, payload any, authenticated bool, expectedStatus int) error {
	if c == nil || c.service == nil || c.client == nil {
		return errors.New("public API client is not configured")
	}
	endpoint, err := validatePublicAPIEndpoint(rawURL)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode public API request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create public API request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if authenticated {
		credential, err := c.service.ActivationCredential()
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("send public API request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, publicAPIMaxBody+1))
	if err != nil {
		return fmt.Errorf("read public API response: %w", err)
	}
	if len(responseBody) > publicAPIMaxBody {
		return errors.New("public API response exceeded size limit")
	}
	if response.StatusCode != expectedStatus {
		return fmt.Errorf("public API returned status %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || mediaType != "application/json" {
			return errors.New("public API response content type was not application/json")
		}
	}
	return nil
}

func validatePublicAPIEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse public API endpoint: %w", err)
	}
	if endpoint.Scheme != "https" || endpoint.Hostname() != "swarmagent.dev" || endpoint.Port() != "" || (endpoint.Path != "/api/activation" && endpoint.Path != "/api/feedback") || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil {
		return nil, errors.New("public API endpoint is not canonical HTTPS")
	}
	return endpoint, nil
}
