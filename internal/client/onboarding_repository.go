package client

import (
	"context"
	"fmt"
	"net/url"
)

func (c *API) InspectOnboardingRepository(ctx context.Context, path string) (OnboardingRepository, error) {
	var resp struct {
		OK         bool                 `json:"ok"`
		Repository OnboardingRepository `json:"repository"`
	}
	err := c.getJSON(ctx, "/v1/workspace/repository?path="+url.QueryEscape(path), &resp, true)
	if err == nil && (!resp.OK || resp.Repository.Path == "" || resp.Repository.State == "") {
		err = fmt.Errorf("repository inspection was not acknowledged")
	}
	return resp.Repository, err
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
