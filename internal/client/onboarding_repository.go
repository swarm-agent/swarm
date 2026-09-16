package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

func (c *API) InspectOnboardingRepository(ctx context.Context, path string) (OnboardingRepository, error) {
	var resp struct {
		OK         bool                 `json:"ok"`
		Code       string               `json:"code"`
		Repository OnboardingRepository `json:"repository"`
	}
	status, body, err := c.request(ctx, http.MethodGet, "/v1/workspace/repository?path="+url.QueryEscape(path), nil, true)
	if err != nil {
		return OnboardingRepository{}, err
	}
	decodeErr := json.Unmarshal(body, &resp)
	// A missing destination is an inspection result, not setup success. Preserve
	// the daemon's typed state so the TUI can offer explicit creation consent.
	// All other conflicts and malformed responses remain failures.
	if status == http.StatusConflict && decodeErr == nil && !resp.OK && resp.Code == "workspace_repository_not_ready" && resp.Repository.State == "directory_missing" && resp.Repository.Path == path {
		return resp.Repository, nil
	}
	if status != http.StatusOK {
		return OnboardingRepository{}, decodeAPIError(status, body)
	}
	if decodeErr != nil {
		return OnboardingRepository{}, fmt.Errorf("decode repository inspection: %w", decodeErr)
	}
	if !resp.OK || resp.Repository.Path == "" || resp.Repository.State == "" {
		return OnboardingRepository{}, fmt.Errorf("repository inspection was not acknowledged")
	}
	return resp.Repository, nil
}
func (c *API) ReviewOnboardingRepository(ctx context.Context, path string) (OnboardingReview, error) {
	var resp struct {
		OK     bool             `json:"ok"`
		Review OnboardingReview `json:"review"`
	}
	err := c.getJSON(ctx, "/v1/workspace/repository/review?path="+url.QueryEscape(path), &resp, true)
	if err == nil && (!resp.OK || resp.Review.Digest == "" || resp.Review.Repository.Path == "") {
		err = fmt.Errorf("content review was not acknowledged")
	}
	return resp.Review, err
}
func (c *API) PrepareOnboardingBaseline(ctx context.Context, request OnboardingBaseline) (OnboardingRepository, error) {
	var resp struct {
		OK         bool                 `json:"ok"`
		Repository OnboardingRepository `json:"repository"`
	}
	err := c.postJSON(ctx, "/v1/workspace/repository/baseline", request, &resp, true)
	if err == nil && (!resp.OK || resp.Repository.State != "ready" || resp.Repository.HeadCommit == "") {
		err = fmt.Errorf("baseline was not acknowledged")
	}
	return resp.Repository, err
}
