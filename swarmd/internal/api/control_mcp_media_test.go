package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

func TestControlMCPMediaModelsAndProjectStatus(t *testing.T) {
	server, repo, db := newControlMCPProjectFixture(t)
	server.SetUISettingsService(uisettings.NewService(pebblestore.NewUISettingsStore(db)))
	p := testPrincipal()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manage := controlMCPDirectCall(ctx, p, server.apiMux(), server, "sessions:read", "sessions:write", "settings:write")
	read := controlMCPDirectCall(ctx, p, server.apiMux(), server, "sessions:read")

	if text, isError := controlMCPToolText(t, read, "swarm_set_media_models", map[string]any{"image": "img-model"}); !isError || !strings.Contains(text, "settings:write") {
		t.Fatalf("read grant changed media models: %s", text)
	}
	if text, isError := controlMCPToolText(t, manage, "swarm_set_media_models", map[string]any{}); !isError || !strings.Contains(text, "at least one") {
		t.Fatalf("empty call accepted: %s", text)
	}
	text, isError := controlMCPToolText(t, manage, "swarm_set_media_models", map[string]any{"image": "img-model", "audio": "snd-model"})
	if isError || !strings.Contains(text, `"image":"img-model"`) || !strings.Contains(text, `"audio":"snd-model"`) {
		t.Fatalf("set media models: %s", text)
	}
	text, isError = controlMCPToolText(t, manage, "swarm_set_media_models", map[string]any{"image": ""})
	if isError || !strings.Contains(text, `"image":""`) || !strings.Contains(text, `"audio":"snd-model"`) {
		t.Fatalf("clearing one slot kept the other: %s", text)
	}

	created, isError := controlMCPToolText(t, manage, "swarm_create_project", map[string]any{
		"name": "Status", "description": "status block", "workspaces": []map[string]any{{"path": repo}},
	})
	if isError {
		t.Fatalf("create project: %s", created)
	}
	var createdProject struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(created), &createdProject); err != nil || createdProject.Project.ID == "" {
		t.Fatalf("decode created project: %v %s", err, created)
	}
	projectID := createdProject.Project.ID
	text, isError = controlMCPToolText(t, read, "swarm_get_project", map[string]any{"project_id": projectID})
	if isError || !strings.Contains(text, `"status_md":"## Status: Status`) || !strings.Contains(text, `"open_tasks":[]`) {
		t.Fatalf("project status missing: %s", text)
	}
}
