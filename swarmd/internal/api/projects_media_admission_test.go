package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

// Purpose: the admission boundary must count the complete media batch, not a
// provider chunk, and ignore generic auto-approval at 25+. Non-media approval
// remains untouched. This pure policy test is the narrowest threshold proof;
// HTTP tests below additionally prove persistence, rejection and dispatch.
func TestMediaBatchAdmissionPolicy(t *testing.T) {
	for _, agent := range []string{"image", "video", "sound", "audio"} {
		for _, count := range []int{1, 24, 25, 26} {
			task := pebblestore.ProjectTaskRecord{Agent: agent, VariantCount: count, AutoApprove: true}
			if err := admitProjectMediaTask(&task); err != nil {
				t.Fatal(err)
			}
			if task.AutoApprove != (count < 25) || (task.Status == "in_progress") != (count < 25) {
				t.Fatalf("%s/%d: %+v", agent, count, task)
			}
			if count >= 25 && !strings.Contains(task.ActionNeeded, fmt.Sprint(count)) {
				t.Fatal("full count missing from confirmation")
			}
		}
	}
	for _, agent := range []string{"coder", "finder", "plan", "swarm", "designer"} {
		task := pebblestore.ProjectTaskRecord{Agent: agent, Status: "pending_approval", VariantCount: 1}
		before := task
		if err := admitProjectMediaTask(&task); err != nil || !reflect.DeepEqual(task, before) {
			t.Fatalf("changed non-media %s", agent)
		}
	}
	for _, counts := range [][3]int{{-1, 1, 0}, {1, -1, 0}, {1, 25, 0}, {1, 0, 25}} {
		if _, err := projectMediaBatchCount(counts[0], counts[1], counts[2]); err == nil {
			t.Fatalf("accepted invalid counts %v", counts)
		}
	}
	if count, err := projectMediaBatchCount(0, 25, 25); err != nil || count != 25 {
		t.Fatalf("alias count %d: %v", count, err)
	}
}

type admissionVideoRecorder struct {
	fakeTestVideoGenService
}

func (r *admissionVideoRecorder) PreflightVideoOperation(ctx context.Context, req videogen.VideoPreflightRequest) (*videogen.VideoPreflightResult, error) {
	return (&mockPreflightVideoService{}).PreflightVideoOperation(ctx, req)
}

func mediaAdmissionRequest(s *Server, p identity.Principal, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v3/projects/media/tasks", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, p))
	req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"projects:write", "sessions:write"}}))
	w := httptest.NewRecorder()
	s.handleProjects(w, req)
	return w
}

func awaitMediaAdmissionTask(t *testing.T, db *pebblestore.SessionStore, p identity.Principal, projectID, id string) *pebblestore.ProjectTaskRecord {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, id)
		if err != nil {
			t.Fatal(err)
		}
		if found && (task.Status == "needs_review" || task.Status == "failed") {
			return task
		}
		select {
		case <-deadline.C:
			t.Fatalf("media task %s did not finish", id)
		case <-tick.C:
		}
	}
}

// Purpose: handleProjects/CreateProjectTask must auto-dispatch supported ordinary
// batches, stage 25 images without provider calls, reject unsupported/invalid
// counts without state, and make create/approve/deploy retries idempotent.
// Threat: auto_approve, count aliases, or retries bypass confirmation and spend.
// Temporary Pebble + recording providers exercise the real admission/execution
// boundary without network or live generation; this is not a benchmark.
func TestMediaBatchAdmissionHTTP(t *testing.T) {
	for _, agent := range []string{"image", "video", "sound", "audio"} {
		for _, count := range []int{-1, 1, 24, 25, 26} {
			t.Run(fmt.Sprintf("%s/%d", agent, count), func(t *testing.T) {
				s, db, p := setupDirectMediaTestServer(t)
				if err := db.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "media", AccountID: p.AccountScopeID, Name: "Media"}); err != nil {
					t.Fatal(err)
				}
				recorder := &imagePromptRecorder{}
				svc := imagegen.NewService(nil, pebblestore.NewAuthStore(db.Underlying()), pebblestore.NewImageThreadStore(db.Underlying()), s.model)
				svc.SetGeminiImageClient(recorder)
				s.SetImageGenerationService(svc)
				video := &admissionVideoRecorder{fakeTestVideoGenService: fakeTestVideoGenService{shouldFail: true}}
				s.SetVideoGenerationService(video)
				model := "snapshot-image"
				if agent == "video" {
					model = "veo-3.1-generate-preview"
				}
				body := fmt.Sprintf(`{"id":"batch","title":"Test media","agent":%q,"intent":%q,"prompt":"test media","model":%q,"variant_count":%d,"auto_approve":%t}`, agent, agent, model, count, count >= 25)
				w := mediaAdmissionRequest(s, p, body)
				supported := count >= 1 && ((agent == "image" && count <= 25) || (agent == "video" && count <= 8) || ((agent == "audio" || agent == "sound") && count == 1))
				if !supported {
					if w.Code != 400 {
						t.Fatalf("invalid accepted: %d %s", w.Code, w.Body.String())
					}
					if _, found, err := db.GetProjectTask(p.AccountScopeID, "media", "batch"); err != nil || found {
						t.Fatalf("invalid persisted: %v %v", found, err)
					}
					return
				}
				if w.Code != 201 {
					t.Fatalf("create: %d %s", w.Code, w.Body.String())
				}
				var response struct {
					Task pebblestore.ProjectTaskRecord `json:"task"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if count == 25 {
					if response.Task.Status != "pending_approval" || len(recorder.prompts) != 0 {
						t.Fatal("unconfirmed batch dispatched")
					}
					if err := s.DeployProjectTask(context.Background(), p, "media", "batch"); err == nil {
						t.Fatal("deploy bypassed confirmation")
					}
					foreign := p
					foreign.AccountScopeID = "foreign"
					if _, err := s.ApproveProjectTask(context.Background(), foreign, "media", "batch"); err == nil {
						t.Fatal("cross-account confirmation")
					}
					if len(recorder.prompts) != 0 {
						t.Fatal("unauthorized confirmation dispatched")
					}
					var approvals sync.WaitGroup
					errors := make(chan error, 4)
					for i := 0; i < 4; i++ {
						approvals.Add(1)
						go func() {
							defer approvals.Done()
							_, err := s.ApproveProjectTask(context.Background(), p, "media", "batch")
							errors <- err
						}()
					}
					approvals.Wait()
					close(errors)
					for err := range errors {
						if err != nil {
							t.Fatal(err)
						}
					}
				} else if response.Task.Status != "in_progress" {
					t.Fatalf("ordinary batch not admitted: %+v", response.Task)
				}
				finished := awaitMediaAdmissionTask(t, db, p, "media", "batch")
				for i := 0; i < 2; i++ {
					if w := mediaAdmissionRequest(s, p, body); w.Code != 201 && w.Code != 200 {
						t.Fatalf("replay: %d %s", w.Code, w.Body.String())
					}
					if _, err := s.ApproveProjectTask(context.Background(), p, "media", "batch"); err != nil {
						t.Fatal(err)
					}
					if err := s.DeployProjectTask(context.Background(), p, "media", "batch"); err != nil {
						t.Fatal(err)
					}
				}
				fresh, _, _ := db.GetProjectTask(p.AccountScopeID, "media", "batch")
				if !reflect.DeepEqual(fresh.Deliverables, finished.Deliverables) {
					t.Fatal("replay replaced slots")
				}
				recorder.mu.Lock()
				calls := len(recorder.prompts)
				recorder.mu.Unlock()
				video.mu.Lock()
				videoCalls := video.callCount
				video.mu.Unlock()
				if agent == "image" && calls != count {
					t.Fatalf("image calls=%d want=%d", calls, count)
				}
				if agent == "video" && videoCalls != count {
					t.Fatalf("video calls=%d want=%d", videoCalls, count)
				}
			})
		}
	}
}

// Purpose: the persisted 25-image proposal must remain inert when declined;
// replay and later approval cannot resurrect rejection. Invalid aliases must
// fail before reservation. HTTP + temporary store proves both rejection and
// no provider side effects at the actual task boundary.
func TestMediaBatchAdmissionCancelAndAliases(t *testing.T) {
	s, db, p := setupDirectMediaTestServer(t)
	if err := db.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "media", AccountID: p.AccountScopeID, Name: "Media"}); err != nil {
		t.Fatal(err)
	}
	recorder := &imagePromptRecorder{}
	svc := imagegen.NewService(nil, pebblestore.NewAuthStore(db.Underlying()), pebblestore.NewImageThreadStore(db.Underlying()), s.model)
	svc.SetGeminiImageClient(recorder)
	s.SetImageGenerationService(svc)
	body := `{"id":"cancelled","intent":"image","prompt":"image","model":"snapshot-image","deliverable_count":25,"auto_approve":true}`
	if w := mediaAdmissionRequest(s, p, body); w.Code != 201 {
		t.Fatalf("create: %s", w.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/v3/projects/media/tasks/cancelled/reject", strings.NewReader(`{}`))
	req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, p))
	req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"projects:write"}}))
	w := httptest.NewRecorder()
	s.handleProjects(w, req)
	if w.Code != 200 {
		t.Fatalf("reject: %d %s", w.Code, w.Body.String())
	}
	if _, err := s.ApproveProjectTask(context.Background(), p, "media", "cancelled"); err == nil {
		t.Fatal("rejected task approved")
	}
	if w := mediaAdmissionRequest(s, p, body); w.Code != 201 {
		t.Fatalf("replay: %s", w.Body.String())
	}
	task, _, _ := db.GetProjectTask(p.AccountScopeID, "media", "cancelled")
	if task.Status != "rejected" || len(recorder.prompts) != 0 {
		t.Fatalf("cancel regenerated: %+v", task)
	}
	for _, counts := range []string{`"variant_count":-1,"deliverable_count":25`, `"variant_count":1,"deliverable_count":25`, `"variant_count":1.5`, `"variant_count":"25"`} {
		bad := `{"id":"invalid","intent":"image","prompt":"image","model":"snapshot-image",` + counts + `}`
		if w := mediaAdmissionRequest(s, p, bad); w.Code != 400 {
			t.Fatalf("invalid: %d %s", w.Code, w.Body.String())
		}
		if _, found, _ := db.GetProjectTask(p.AccountScopeID, "media", "invalid"); found {
			t.Fatal("invalid alias persisted")
		}
	}
	if w := mediaAdmissionRequest(s, p, strings.Replace(body, `"deliverable_count":25`, `"deliverable_count":24`, 1)); w.Code < 400 {
		t.Fatal("changed batch reused identity")
	}
	if len(recorder.prompts) != 0 {
		t.Fatal("invalid/cancelled request dispatched")
	}
}
