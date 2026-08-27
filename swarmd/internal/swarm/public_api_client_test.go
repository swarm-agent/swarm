package swarm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPublicAPIClientSendsAuthenticatedFeedback(t *testing.T) {
	svc, _ := newTestService(t)
	state, err := svc.EnsureLocalState(EnsureLocalStateInput{})
	if err != nil {
		t.Fatalf("ensure local state: %v", err)
	}
	if err := svc.CompleteMintReportWithCredential(state.Node.SwarmID, "desktop-credential"); err != nil {
		t.Fatalf("store credential: %v", err)
	}
	var body string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != FeedbackReportURL || request.Header.Get("Authorization") != "Bearer desktop-credential" {
			t.Fatalf("unexpected request %s auth=%q", request.URL, request.Header.Get("Authorization"))
		}
		payload, _ := io.ReadAll(request.Body)
		body = string(payload)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"success":true}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	})}
	api := newPublicAPIClient(svc, ActivationReportURL, FeedbackReportURL, client)
	if err := api.SubmitFeedback(context.Background(), FeedbackInput{Category: "bug", Message: "Desktop failed.", FormTime: 123}); err != nil {
		t.Fatalf("submit feedback: %v", err)
	}
	if body != `{"category":"bug","form_time":123,"message":"Desktop failed."}` {
		t.Fatalf("body = %q", body)
	}
}

func TestPublicAPIClientSendsActivationMilestone(t *testing.T) {
	svc, _ := newTestService(t)
	state, err := svc.EnsureLocalState(EnsureLocalStateInput{})
	if err != nil {
		t.Fatalf("ensure local state: %v", err)
	}
	if err := svc.CompleteMintReportWithCredential(state.Node.SwarmID, "desktop-credential"); err != nil {
		t.Fatalf("store credential: %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != ActivationReportURL || request.Header.Get("Authorization") != "Bearer desktop-credential" {
			t.Fatalf("unexpected request %s auth=%q", request.URL, request.Header.Get("Authorization"))
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"accepted":true}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	})}
	api := newPublicAPIClient(svc, ActivationReportURL, FeedbackReportURL, client)
	if err := api.ReportActivation(context.Background(), "onboarding_completed"); err != nil {
		t.Fatalf("report activation: %v", err)
	}
}
