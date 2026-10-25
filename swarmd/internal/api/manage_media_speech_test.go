package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/audiogen"
	"swarm/packages/swarmd/internal/identity"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/uisettings"
)

// Purpose: native ManageMedia and MCP's HTTP task boundary must preserve the
// same speech request and reject invalid scripts before persistence. Temporary
// storage with approval required proves admission without provider calls; saved
// fixture deliverables prove the real file bridge preserves audio and timings.
func TestManageMediaSpeech(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	root := t.TempDir()
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session", WorkspacePath: root, Metadata: map[string]any{"project_id": "project", pebblestore.SessionAccessMetadataKey: map[string]any{"tasks": "ask"}}}, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Speech", AccountID: p.AccountScopeID}); err != nil {
		t.Fatal(err)
	}
	settings := uisettings.NewService(pebblestore.NewUISettingsStore(db))
	ui, err := settings.GetForAccount(p.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	ui.Media.TranscriptionModel = "transcription-test"
	if _, err := settings.SetForAccount(p.AccountScopeID, ui); err != nil {
		t.Fatal(err)
	}
	var rec pebblestore.ModelCatalogRecord
	if err := json.Unmarshal([]byte(`{"provider":"google","model":"transcription-test","catalog_modalities":{"inputs":["audio"],"outputs":["text"]}}`), &rec); err != nil {
		t.Fatal(err)
	}
	s := &Server{model: modelruntime.NewService(pebblestore.NewModelStore(db), nil, modelruntime.NewCatalogService(pebblestore.NewModelCatalogStore(db))), sessions: session.NewService(store, nil), audioGen: audiogen.NewService(nil, settings, billingCatalog{rec})}
	scope := tool.WorkspaceScope{SessionID: "session", Principal: p, PrimaryPath: root}
	args := map[string]any{"action": "generate", "kind": "sound", "operation": "speech", "model": "gemini-2.5-flash-preview-tts", "text": "Hello world", "voice": "Kore", "style": "Calm", "language": "English", "format": "wav"}
	want, err := mediaSpeechSettings(args, "sound")
	if err != nil {
		t.Fatal(err)
	}
	for field, message := range map[string]string{
		"text":      "speech requires text and a complete prompt of at most 8000 UTF-8 bytes",
		"voice":     "unsupported speech voice; consult swarm_media_models",
		"model":     "speech requires a supported explicit Google TTS model",
		"operation": "text requires operation speech",
	} {
		bad := map[string]any{}
		for k, v := range args {
			bad[k] = v
		}
		delete(bad, field)
		_, wantErr := mediaSpeechSettings(bad, "sound")
		_, gotErr := s.ManageMedia(context.Background(), scope, bad)
		if wantErr == nil || gotErr == nil || wantErr.Error() != gotErr.Error() || gotErr.Error() != message {
			t.Fatalf("%s: %v != %v", field, gotErr, wantErr)
		}
	}
	tasks, err := store.ListProjectTasks(p.AccountScopeID, "project", 10)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("rejection persisted: %v %v", tasks, err)
	}
	result, err := s.ManageMedia(context.Background(), scope, args)
	if err != nil {
		t.Fatal(err)
	}
	job := result.(map[string]any)["job_id"].(string)
	tasks, err = store.ListProjectTasks(p.AccountScopeID, "project", 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks: %v %v", tasks, err)
	}
	native := tasks[0]
	if !reflect.DeepEqual(native.Speech, want) || native.Operation != "speech" || native.Model != args["model"] || native.Status != "pending_approval" {
		t.Fatalf("native: %+v", native)
	}
	body, _ := json.Marshal(map[string]any{"id": "http-speech", "title": "Narration", "agent": "sound", "operation": "speech", "model": args["model"], "speech": want})
	req := httptest.NewRequest(http.MethodPost, ProjectsPath+"/project/tasks", strings.NewReader(string(body)))
	req = req.WithContext(identity.ContextWithPrincipal(req.Context(), p))
	response := httptest.NewRecorder()
	s.handleProjects(response, req)
	if response.Code != http.StatusCreated {
		t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
	}
	httpTask, found, err := store.GetProjectTask(p.AccountScopeID, "project", "http-speech")
	if err != nil || !found || !reflect.DeepEqual(speechRequest(httpTask, p), speechRequest(&native, p)) {
		t.Fatalf("HTTP task: %+v %v", httpTask, err)
	}
	payloads := []string{"audio bytes", `{"granularity":"word","words":[{"word":"Hello","start":0,"end":1}]}`}
	native.Status = "completed"
	native.Deliverables = []pebblestore.ProjectTaskDeliverable{{ID: "audio", Kind: "audio", Status: "ready", MediaURL: "data:audio/wav;base64," + base64.StdEncoding.EncodeToString([]byte(payloads[0]))}, {ID: "timings", Kind: "json", Status: "ready", MediaURL: "data:application/json;base64," + base64.StdEncoding.EncodeToString([]byte(payloads[1]))}}
	if err := store.PutProjectTask(p.AccountScopeID, &native); err != nil {
		t.Fatal(err)
	}
	view, err := s.ManageMedia(context.Background(), scope, map[string]any{"action": "get", "job_id": job})
	if err != nil || len(view.(map[string]any)["files"].([]map[string]any)) != 2 {
		t.Fatalf("view: %v %v", view, err)
	}
	if _, err := s.ManageMedia(context.Background(), scope, map[string]any{"action": "save", "job_id": job, "folder": "narration"}); err != nil {
		t.Fatal(err)
	}
	for i, f := range mediaTaskFiles(&native) {
		data, err := os.ReadFile(filepath.Join(root, "narration", f.filename(native.Title, i+1)))
		if err != nil || string(data) != payloads[i] {
			t.Fatalf("file %d: %q %v", i, data, err)
		}
	}
}
