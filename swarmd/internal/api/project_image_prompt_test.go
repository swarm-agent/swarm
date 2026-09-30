package api

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"

	"swarm/packages/swarmd/internal/imagegen"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/uisettings"
)

// Purpose: CreateProjectTask must reserve workspace-free images with original
// prompts and no session/worktree, while identical replay reuses persisted slots.
// Threat: repository ambiguity, incidental Router naming or changed opt-in replay.
// The canonical API with a temporary store and no Router is the narrowest layer.
func TestProjectImageCreationWorkspaceFreeReplay(t *testing.T) {
	s, db, p := setupDirectMediaTestServer(t)
	project := &pebblestore.ProjectRecord{ID: "images", AccountID: p.AccountScopeID, Name: "Images", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: "/repo/one"}, {Path: "/repo/two"}}}
	if err := db.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	input := tool.ProjectTaskCreateInput{ID: "image-task", Prompt: "  café at sunrise  ", Agent: "image", Model: "snapshot-image", VariantCount: 2}
	first, err := s.CreateProjectTask(context.Background(), p, project.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID != "" || first.WorkspacePath != "" || first.SourceWorkspace.Path != "" || first.WorktreeBranch != "" || first.TaskProgram != nil || first.Description != input.Prompt || !reflect.DeepEqual(first.ImagePrompts, []string{input.Prompt, input.Prompt}) {
		t.Fatalf("image allocated source execution or changed prompt: %+v", first)
	}
	again, err := s.CreateProjectTask(context.Background(), p, project.ID, input)
	if err != nil || !reflect.DeepEqual(again.ImagePrompts, first.ImagePrompts) || !reflect.DeepEqual(again.Deliverables, first.Deliverables) {
		t.Fatalf("replay changed slots: %+v %v", again, err)
	}
	for _, bad := range []tool.ProjectTaskCreateInput{
		{ID: "bad-code", Prompt: "image", Agent: "coder", Intent: "image", Model: "snapshot-image"},
		{ID: "bad-session", Prompt: "image", Agent: "image", SessionID: "forged", Model: "snapshot-image"},
		{ID: "bad-program", Prompt: "image", Agent: "image", TaskProgramID: "forged", Model: "snapshot-image"},
	} {
		if _, err := s.CreateProjectTask(context.Background(), p, project.ID, bad); err == nil {
			t.Fatalf("accepted source execution on image path: %+v", bad)
		}
		if _, found, err := db.GetProjectTask(p.AccountScopeID, project.ID, bad.ID); err != nil || found {
			t.Fatalf("rejected request reserved task: found=%v err=%v", found, err)
		}
	}
	input.EnhancePrompt = true
	if _, err := s.CreateProjectTask(context.Background(), p, project.ID, input); err == nil {
		t.Fatal("changed opt-in reused reservation")
	}
	foreign := p
	foreign.AccountScopeID = "foreign-account"
	if _, err := s.CreateProjectTask(context.Background(), foreign, project.ID, input); err == nil {
		t.Fatal("foreign project access accepted")
	}
}

type imagePromptRecorder struct {
	mu      sync.Mutex
	prompts []string
}

func (r *imagePromptRecorder) GenerateImage(_ context.Context, req imagegen.GeminiImageGenerationRequest) (imagegen.GeminiImageGenerationResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts = append(r.prompts, req.Prompt)
	return imagegen.GeminiImageGenerationResult{Images: []imagegen.GeminiGeneratedImage{{DecodedPNG: imageGenerationTestPNGBytes(), MIMEType: "image/png"}}}, nil
}

// Purpose: persisted prompts must reach exact provider output slots, and malformed
// arrays must cause zero provider calls and no ready outputs. Recovery must not
// regenerate accepted slots. The actual execution path with a recording provider
// proves forwarding without live provider spend; this is not a benchmark.
func TestProjectImageExecutionPromptSlots(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		s, db, p := setupDirectMediaTestServer(t)
		recorder := &imagePromptRecorder{}
		svc := imagegen.NewService(nil, pebblestore.NewAuthStore(db.Underlying()), pebblestore.NewImageThreadStore(db.Underlying()), s.model)
		svc.SetGeminiImageClient(recorder)
		s.SetImageGenerationService(svc)
		project := &pebblestore.ProjectRecord{ID: "images", AccountID: p.AccountScopeID, Name: "Images"}
		if err := db.PutProject(p.AccountScopeID, project); err != nil {
			t.Fatal(err)
		}
		task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: project.ID, AccountID: p.AccountScopeID, Title: "Images", Description: "original", Agent: "image", Model: "snapshot-image", VariantCount: 2, EnhancePrompt: true, ImagePrompts: []string{"  red café\n", " blue café  "}, Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "one", Kind: "image", Status: "pending"}, {ID: "two", Kind: "image", Status: "pending"}}}
		if malformed {
			task.ImagePrompts = []string{"only one"}
		}
		if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
			t.Fatal(err)
		}
		s.executeDirectMediaTask(p, project, task)
		fresh, found, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
		if err != nil || !found {
			t.Fatalf("missing task: %v", err)
		}
		if malformed {
			if len(recorder.prompts) != 0 || fresh.Status != "failed" || fresh.Deliverables[0].Status == "ready" || fresh.Deliverables[1].Status == "ready" {
				t.Fatalf("partial malformed generation: %+v %+v", recorder.prompts, fresh)
			}
		} else {
			sort.Strings(recorder.prompts)
			if !reflect.DeepEqual(recorder.prompts, []string{"  red café\n", " blue café  "}) || fresh.Deliverables[0].Status != "ready" || fresh.Deliverables[1].Status != "ready" {
				t.Fatalf("provider slot prompts: %+v task=%+v", recorder.prompts, fresh)
			}
			recorder.prompts = nil
			s.executeDirectMediaTask(p, project, fresh)
			if len(recorder.prompts) != 0 {
				t.Fatal("recovery regenerated ready outputs")
			}
		}
	}
}

// Purpose: image preflight must validate against the same account default used
// by generation, without persisting an invented override or borrowing another
// account's default. The settings boundary is the narrowest deterministic layer.
func TestProjectImagePreflightAccountDefault(t *testing.T) {
	s, db, p := setupDirectMediaTestServer(t)
	s.uiSettings = uisettings.NewService(pebblestore.NewUISettingsStore(db.Underlying()))
	settings := uisettings.UISettings{}
	settings.Tools.Image.DefaultModel = "snapshot-image"
	if _, err := s.uiSettings.SetForAccount(p.AccountScopeID, settings); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{Agent: "image", AspectRatio: "1:1", Resolution: "1K", VariantCount: 1}
	if err := validateProjectMediaTaskSettings(s, task, p); err != nil {
		t.Fatal(err)
	}
	if task.Model != "" {
		t.Fatal("preflight invented an explicit model override")
	}
	foreign := p
	foreign.AccountScopeID = "unconfigured-account"
	if err := validateProjectMediaTaskSettings(s, task, foreign); err == nil {
		t.Fatal("preflight borrowed another account's image default")
	}
	task.Model = "invalid-image-model"
	if err := validateProjectMediaTaskSettings(s, task, p); err == nil {
		t.Fatal("invalid explicit model silently fell back to default")
	}
}

// Purpose: canonical creation through actual image execution must preserve the
// original prompt for single/default multi-image requests, including stale
// single-image opt-in. No Router is configured, so any incidental call fails.
func TestProjectImageDirectProviderPrompt(t *testing.T) {
	for _, count := range []int{1, 2} {
		s, db, p := setupDirectMediaTestServer(t)
		recorder := &imagePromptRecorder{}
		svc := imagegen.NewService(nil, pebblestore.NewAuthStore(db.Underlying()), pebblestore.NewImageThreadStore(db.Underlying()), s.model)
		svc.SetGeminiImageClient(recorder)
		s.SetImageGenerationService(svc)
		project := &pebblestore.ProjectRecord{ID: "direct-images", AccountID: p.AccountScopeID, Name: "Images"}
		if err := db.PutProject(p.AccountScopeID, project); err != nil {
			t.Fatal(err)
		}
		prompt := "  café\nwith blue chairs  "
		task, err := s.CreateProjectTask(context.Background(), p, project.ID, tool.ProjectTaskCreateInput{ID: "direct", Prompt: prompt, Agent: "image", Model: "snapshot-image", VariantCount: count, EnhancePrompt: count == 1})
		if err != nil {
			t.Fatal(err)
		}
		if task.EnhancePrompt || task.SessionID != "" {
			t.Fatal("direct image acquired enhancement or session")
		}
		s.executeDirectMediaTask(p, project, task)
		if len(recorder.prompts) != count {
			t.Fatalf("provider calls = %d, want %d", len(recorder.prompts), count)
		}
		for _, got := range recorder.prompts {
			if got != prompt {
				t.Fatalf("provider prompt changed: %q", got)
			}
		}
	}
}
