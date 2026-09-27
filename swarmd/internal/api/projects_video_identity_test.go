package api

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

// Requirement: result descriptions identify adapter-reported provider/model, not
// account defaults or an unqualified request. Threat: a result appears to have run
// on another provider, or missing metadata is silently replaced by the request.
// Authority: executeDirectMediaTask -> videoExecutionIdentity. This pure unit test
// is the narrow label boundary; durable completion still requires integration proof.
func TestVideoExecutionIdentity(t *testing.T) {
	for _, tc := range []struct{ provider, model, want string }{
		{"google", "veo", "google:veo"},
		{"google", "google:veo", "google:veo"},
		{"openrouter", "google/veo", "openrouter:google/veo"},
		{"", "veo", "unknown execution identity"},
		{"google", "", "unknown execution identity"},
	} {
		if got := videoExecutionIdentity(tc.provider, tc.model); got != tc.want {
			t.Errorf("identity(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

// Requirement: direct video execution preserves requested identity/options and
// durable results report execution identity. Threat: completion erases provider
// qualification or publishes failed output. Authority: executeDirectMediaTask and
// updateProjectTaskWithRetry. Real temporary storage plus a service fixture is the
// narrowest layer proving these durable postconditions; no live provider proof.
func TestProjectVideoIdentityPersistence(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			server, sessions, principal := setupDirectMediaTestServer(t)
			project := &pebblestore.ProjectRecord{ID: "identity-project", AccountID: principal.AccountScopeID, Name: "Identity"}
			if err := sessions.PutProject(principal.AccountScopeID, project); err != nil {
				t.Fatal(err)
			}
			task := &pebblestore.ProjectTaskRecord{
				ID: "identity-task", ProjectID: project.ID, AccountID: principal.AccountScopeID,
				Title: "Identity", Description: "A quiet landscape", Agent: "video", Status: "in_progress",
				Model: "google:veo-3.1-generate-preview", AspectRatio: "16:9", Resolution: "720p", DurationSeconds: 8, VariantCount: 1,
				Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "identity-clip", Kind: "video", Status: "generating"}},
			}
			if err := sessions.PutProjectTask(principal.AccountScopeID, task); err != nil {
				t.Fatal(err)
			}
			service := &fakeTestVideoGenService{shouldFail: fail, result: videogen.ManagedVideoResult{
				Provider: "google", Model: "veo-3.1-generate-preview", Bytes: []byte("fixture-video"), MediaType: "video/mp4",
				AspectRatio: "16:9", Resolution: "720p", DurationSeconds: 8,
			}}
			server.SetVideoGenerationService(service)
			server.executeDirectMediaTask(principal, project, task)
			updated, ok, err := sessions.GetProjectTask(principal.AccountScopeID, project.ID, task.ID)
			if err != nil || !ok {
				t.Fatalf("read task: %v %v", ok, err)
			}
			if service.callCount != 1 || service.lastRequest.Model != task.Model || service.lastRequest.AspectRatio != "16:9" || service.lastRequest.Resolution != "720p" || service.lastRequest.DurationSeconds != 8 {
				t.Fatalf("request identity/options changed: %+v", service.lastRequest)
			}
			if updated.Model != "google:veo-3.1-generate-preview" || len(updated.Deliverables) != 1 {
				t.Fatal("completion lost requested identity or deliverable")
			}
			output := updated.Deliverables[0]
			if fail {
				if output.Status != "failed" || output.MediaURL != "" {
					t.Fatal("failed provider published output")
				}
			} else if output.Status != "ready" || output.MediaURL == "" || !strings.Contains(output.Description, "google:veo-3.1-generate-preview") {
				t.Fatalf("result lost execution identity: %+v", output)
			}
		})
	}
}
