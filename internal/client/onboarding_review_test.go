package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// Requirement: client review/baseline/save wires carry exact consent and reject
// unacknowledged success. Threat: transport success mistaken for durable readiness.
// The HTTP boundary is the narrowest proof of payload and error propagation.
func TestOnboardingReviewAcknowledgementAndConsent(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	request := OnboardingBaseline{Path: "/project", ExpectedResolvedPath: "/project", ReviewDigest: "exact", SelectedPaths: []string{"README.md"}, ConfirmBaseline: true, ConfirmOmissions: true}
	for _, ack := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Swarm-Token") != "fixture" {
				t.Error("missing authentication")
			}
			switch r.URL.Path {
			case "/v1/workspace/repository/baseline":
				var got OnboardingBaseline
				json.NewDecoder(r.Body).Decode(&got)
				if !reflect.DeepEqual(got, request) {
					t.Errorf("consent changed %+v", got)
				}
				json.NewEncoder(w).Encode(map[string]any{"ok": ack, "repository": OnboardingRepository{Path: "/project", State: "ready", HeadCommit: "head"}})
			case "/v1/workspace/repository/review":
				json.NewEncoder(w).Encode(map[string]any{"ok": ack, "review": OnboardingReview{Digest: "exact", Repository: OnboardingRepository{Path: "/project"}}})
			case "/v1/workspace/add":
				var got map[string]any
				json.NewDecoder(r.Body).Decode(&got)
				if got["confirm_committed_only"] != true {
					t.Error("missing omission consent")
				}
				w.WriteHeader(409)
				w.Write([]byte(`{"error":"stale workspace"}`))
			default:
				t.Error("unexpected endpoint")
			}
		}))
		api := New(server.URL)
		api.SetToken("fixture")
		_, err := api.PrepareOnboardingBaseline(context.Background(), request)
		if (err == nil) != ack {
			t.Fatalf("ack=%v err=%v", ack, err)
		}
		_, err = api.ReviewOnboardingRepository(context.Background(), "/project")
		if (err == nil) != ack {
			t.Fatalf("review ack=%v err=%v", ack, err)
		}
		if _, err = api.AddWorkspaceWithContentConsent(context.Background(), "/project", "project", "", true, true); err == nil {
			t.Fatal("save rejection swallowed")
		}
		server.Close()
	}
}
