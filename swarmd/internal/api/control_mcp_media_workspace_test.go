package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/audiogen"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

// mediaWorkspaceFixture is a project with two folders (and a third registered
// workspace outside it) reached through Swarm Control's real media handlers:
// project listing and task creation run the API's own handleProjects over a
// temporary store; only the machine's workspace list is a fixture.
type mediaWorkspaceFixture struct {
	call                    *controlMCPCall
	store                   *pebblestore.SessionStore
	first, second, outsider string
	accountID, projectID    string
}

func newMediaWorkspaceFixture(t *testing.T) mediaWorkspaceFixture {
	t.Helper()
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := pebblestore.NewSessionStore(db)
	p := testPrincipal()
	f := mediaWorkspaceFixture{store: store, first: t.TempDir(), second: t.TempDir(), outsider: t.TempDir(), accountID: p.AccountScopeID, projectID: "media-project"}
	if err := store.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: f.projectID, Name: "Swarm", AccountID: p.AccountScopeID, Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: f.first}, {Path: f.second}}}); err != nil {
		t.Fatal(err)
	}
	// A video model is one the catalog lists with video output; speech needs
	// the audio service, which no provider backs here.
	var video pebblestore.ModelCatalogRecord
	if err := json.Unmarshal([]byte(`{"provider":"google","model":"fixture-video","catalog_modalities":{"outputs":["video"]},"provider_specific":{"google":{"video_generation":{"status":"verified","settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16"]}}}}}}`), &video); err != nil {
		t.Fatal(err)
	}
	if err := pebblestore.NewModelCatalogStore(db).SetRecord(video); err != nil {
		t.Fatal(err)
	}
	settings := uisettings.NewService(pebblestore.NewUISettingsStore(db))
	s := &Server{model: modelruntime.NewService(pebblestore.NewModelStore(db), nil, modelruntime.NewCatalogService(pebblestore.NewModelCatalogStore(db))), sessions: session.NewService(store, nil), audioGen: audiogen.NewService(nil, settings, billingCatalog{})}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/workspace/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"workspaces": []any{map[string]any{"path": f.first}, map[string]any{"path": f.second}, map[string]any{"path": f.outsider}}})
		case strings.HasPrefix(r.URL.Path, "/v3/projects"):
			s.handleProjects(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not in fixture"}`))
		}
	})
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "sessions:write"}})
	f.call = &controlMCPCall{request: req, next: next, server: s}
	return f
}

func (f mediaWorkspaceFixture) tasks(t *testing.T) []pebblestore.ProjectTaskRecord {
	t.Helper()
	tasks, err := f.store.ListProjectTasks(f.accountID, f.projectID, 100)
	if err != nil {
		t.Fatal(err)
	}
	return tasks
}

// Purpose:
//   - Requirement: manage_media generate over Swarm Control (no conversation)
//     with kind sound (music and speech) or video, in a project of several
//     folders, creates the media task with the named folder as its
//     WorkspacePath; it carries no source binding, session or worktree.
//     Without a folder named, or with one that is not the project's, it is
//     refused naming the project's folders and nothing is stored.
//   - Threat/regression: CreateProjectTask kept the caller's folder only for
//     in-session calls (ManageMediaSource), so the task router refused every
//     Swarm Control video and sound request as "execution target is
//     ambiguous"; the fix must not let media name a folder outside the project.
//   - Boundary/authority: controlMCPRequestMedia -> controlMCPRunTask -> the
//     real handleProjects/CreateProjectTask (directMediaOutputFolder) over a
//     temporary store.
//   - Narrowest layer: the MCP handler over the real task route is where the
//     reported refusal appeared; no provider is called (tasks wait unrun).
func TestControlMCPMediaGenerateKeepsNamedProjectFolder(t *testing.T) {
	f := newMediaWorkspaceFixture(t)
	cases := []struct {
		name string
		args map[string]any
	}{
		{"music", map[string]any{"kind": "sound", "brief": "A calm synth loop", "model": "fixture-music"}},
		{"speech", map[string]any{"kind": "sound", "operation": "speech", "model": "gemini-2.5-flash-preview-tts", "text": "Hello world", "voice": "Kore"}},
		{"video", map[string]any{"kind": "video", "brief": "A drone shot over hills", "model": "fixture-video"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"project": "Swarm", "workspace": f.second}
			for k, v := range tc.args {
				args[k] = v
			}
			known := map[string]bool{}
			for _, task := range f.tasks(t) {
				known[task.ID] = true
			}
			if _, err := controlMCPRequestMedia(f.call, args); err != nil {
				t.Fatalf("generate refused: %v", err)
			}
			var created []pebblestore.ProjectTaskRecord
			for _, task := range f.tasks(t) {
				if !known[task.ID] {
					created = append(created, task)
				}
			}
			if len(created) != 1 {
				t.Fatalf("created %d tasks", len(created))
			}
			if task := created[0]; task.WorkspacePath != f.second || task.SourceWorkspace.Path != "" || task.SessionID != "" || task.WorktreeBranch != "" {
				t.Fatalf("task %+v", task)
			}
		})
		t.Run(tc.name+"/refused", func(t *testing.T) {
			for _, workspace := range []string{"", f.outsider} {
				args := map[string]any{"project": "Swarm"}
				if workspace != "" {
					args["workspace"] = workspace
				}
				for k, v := range tc.args {
					args[k] = v
				}
				before := len(f.tasks(t))
				_, err := controlMCPRequestMedia(f.call, args)
				if err == nil || !strings.Contains(err.Error(), f.first) || !strings.Contains(err.Error(), f.second) {
					t.Fatalf("workspace %q: want a refusal naming the folders, got %v", workspace, err)
				}
				if after := len(f.tasks(t)); after != before {
					t.Fatalf("workspace %q: refusal stored a task (%d -> %d)", workspace, before, after)
				}
			}
		})
	}
}

// Purpose:
//   - Requirement: the task service itself (the HTTP route any client uses,
//     not only Swarm Control's folder check) keeps a named project folder for
//     direct video and sound and refuses, with the project's folders, a
//     folder outside the project or none named when there are several.
//   - Threat/regression: a client calling POST /v3/projects/{id}/tasks
//     directly binding media output to an arbitrary folder, or the old
//     "execution target is ambiguous" refusal with no folder list.
//   - Boundary/authority: handleProjects -> CreateProjectTask ->
//     directMediaOutputFolder over a temporary store.
//   - Narrowest layer: the route handler is the single task-create authority.
func TestProjectTaskDirectMediaOutputFolder(t *testing.T) {
	f := newMediaWorkspaceFixture(t)
	post := func(body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := withTestPrincipal(httptest.NewRequest(http.MethodPost, ProjectsPath+"/"+f.projectID+"/tasks", strings.NewReader(string(raw))))
		rec := httptest.NewRecorder()
		f.call.server.handleProjects(rec, req)
		return rec
	}
	for _, agent := range []string{"sound", "video"} {
		model := "fixture-" + agent
		rec := post(map[string]any{"id": "ok-" + agent, "title": "clip", "agent": agent, "model": model, "workspace_path": f.first, "video_type": "single"})
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s named folder: HTTP %d %s", agent, rec.Code, rec.Body.String())
		}
		task, found, err := f.store.GetProjectTask(f.accountID, f.projectID, "ok-"+agent)
		if err != nil || !found || task.WorkspacePath != f.first || task.SourceWorkspace.Path != "" {
			t.Fatalf("%s task %+v %v", agent, task, err)
		}
		for i, path := range []string{"", f.outsider, "relative/folder"} {
			before := len(f.tasks(t))
			body := map[string]any{"id": "bad-" + agent + string(rune('a'+i)), "title": "clip", "agent": agent, "model": model, "video_type": "single"}
			if path != "" {
				body["workspace_path"] = path
			}
			rec := post(body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), f.first) || !strings.Contains(rec.Body.String(), f.second) || strings.Contains(rec.Body.String(), "ambiguous") {
				t.Fatalf("%s workspace %q: HTTP %d %s", agent, path, rec.Code, rec.Body.String())
			}
			if after := len(f.tasks(t)); after != before {
				t.Fatalf("%s workspace %q stored a task", agent, path)
			}
		}
	}
}

// Purpose:
//   - Requirement: manage_media retry over Swarm Control of a video job in a
//     project of several folders creates the new job in the named folder.
//   - Threat/regression: retryMedia passed WorkspacePath without
//     ManageMediaSource, so the retry hit the same "execution target is
//     ambiguous" refusal as generate.
//   - Boundary/authority: controlMCPRequestMedia (retry_of) -> retryMedia ->
//     createManagedMedia -> CreateProjectTask over a temporary store.
//   - Narrowest layer: the handler path the operator used.
func TestControlMCPMediaRetryVideoInMultiFolderProject(t *testing.T) {
	f := newMediaWorkspaceFixture(t)
	source := &pebblestore.ProjectTaskRecord{ID: "source-video", ProjectID: f.projectID, AccountID: f.accountID, Agent: "video", Title: "hills", Description: "A drone shot over hills", Model: "fixture-video", OutcomeType: "video_clip", Status: "failed", WorkspacePath: f.second}
	if err := f.store.PutProjectTask(f.accountID, source); err != nil {
		t.Fatal(err)
	}
	if _, err := controlMCPRequestMedia(f.call, map[string]any{"retry_of": "media/" + f.projectID + "/source-video", "project": "Swarm", "workspace": f.second}); err != nil {
		t.Fatalf("retry refused: %v", err)
	}
	var retried *pebblestore.ProjectTaskRecord
	for _, task := range f.tasks(t) {
		if task.RetryOf == "media/"+f.projectID+"/source-video" {
			task := task
			retried = &task
		}
	}
	if retried == nil || retried.Agent != "video" || retried.WorkspacePath != f.second || retried.SourceWorkspace.Path != "" {
		t.Fatalf("retry task %+v", retried)
	}
}

// Purpose:
//   - Requirement: manage_media save over Swarm Control with workspace and a
//     relative folder writes under that workspace, even when the job's own
//     folder is another of the project's; a workspace outside the project, or
//     an absolute folder outside the named workspace, is refused unwritten.
//   - Threat/regression: mediaSaveDir resolved a relative folder against the
//     job's WorkspacePath (or the project's first folder) and ignored the
//     named workspace, writing into a different repository.
//   - Boundary/authority: controlMCPSaveMedia -> mediaSaveDir (place) over a
//     temporary store and real folders.
//   - Narrowest layer: the save handler is the only writer.
func TestControlMCPMediaSaveUsesNamedWorkspace(t *testing.T) {
	f := newMediaWorkspaceFixture(t)
	task := &pebblestore.ProjectTaskRecord{ID: "done", ProjectID: f.projectID, AccountID: f.accountID, Agent: "sound", Title: "loop", Status: "completed", WorkspacePath: f.first, Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "audio", Kind: "audio", Status: "ready", MediaURL: "data:audio/wav;base64," + base64.StdEncoding.EncodeToString([]byte("audio bytes"))}}}
	if err := f.store.PutProjectTask(f.accountID, task); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join("brands", "swarm", "posts", "_incoming", "x")
	for _, refused := range []map[string]any{{"workspace": f.outsider, "folder": folder}, {"workspace": f.second, "folder": filepath.Join(f.first, "elsewhere")}} {
		refused["job_id"] = "media/" + f.projectID + "/done"
		if _, err := controlMCPSaveMedia(f.call, refused); err == nil {
			t.Fatalf("save %v accepted", refused)
		}
	}
	for _, root := range []string{f.first, f.outsider} {
		if entries, _ := os.ReadDir(root); len(entries) != 0 {
			t.Fatalf("refused save wrote into %s: %v", root, entries)
		}
	}
	out, err := controlMCPSaveMedia(f.call, map[string]any{"job_id": "media/" + f.projectID + "/done", "workspace": f.second, "folder": folder})
	if err != nil {
		t.Fatal(err)
	}
	saved := out.(map[string]any)["saved"].([]map[string]any)
	path, _ := saved[0]["path"].(string)
	if len(saved) != 1 || !strings.HasPrefix(path, filepath.Join(f.second, folder)+string(filepath.Separator)) {
		t.Fatalf("saved %v", saved)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "audio bytes" {
		t.Fatalf("file %q %v", data, err)
	}
	if entries, _ := os.ReadDir(f.first); len(entries) != 0 {
		t.Fatalf("save wrote into the job's other folder: %v", entries)
	}
}
