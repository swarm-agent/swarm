package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videotranscription"
)

func TestManageVideoDefinitionExposesOnlyOpaqueReferences(t *testing.T) {
	raw, err := json.Marshal(manageVideoDefinition().Parameters)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"workspace_path", "file_path", "provider_uri", "provider", "model"} {
		if strings.Contains(text, `"`+forbidden+`"`) {
			t.Fatalf("manage_video schema exposes forbidden field %q", forbidden)
		}
	}
	for _, required := range []string{"job_refs", "job_ref", "transcript_ref"} {
		if !strings.Contains(text, `"`+required+`"`) {
			t.Fatalf("manage_video schema lacks %q", required)
		}
	}
}

func TestManageVideoRequiresTrustedRunContext(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "session-1", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: "/workspace", Mode: "auto"}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(sessionStore, events)
	runtime := NewRuntime(1)
	runtime.sessions = sessions
	runtime.video = fakeManageVideoService{}
	scope := WorkspaceScope{SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "session-1", UserID: "user-1", AccountScopeID: "account-1"}}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, Call{CallID: "call-1", Name: "manage_video", Arguments: `{"action":"inspect_attachments"}`})
	if err == nil || !strings.Contains(err.Error(), "trusted triggering message") {
		t.Fatalf("error = %v, want trusted run context rejection", err)
	}
}

type fakeManageVideoService struct{}

func (fakeManageVideoService) Start(context.Context, identity.Principal, string, string) (videotranscription.StartResult, error) {
	return videotranscription.StartResult{}, nil
}
func (fakeManageVideoService) Status(identity.Principal, string, []string) ([]pebblestore.TranscriptionJob, error) {
	return nil, nil
}
func (fakeManageVideoService) Read(identity.Principal, string, string) (pebblestore.NormalizedTranscript, error) {
	return pebblestore.NormalizedTranscript{}, nil
}
func (fakeManageVideoService) ReadByWorkspace(identity.Principal, string, string) (pebblestore.NormalizedTranscript, error) {
	return pebblestore.NormalizedTranscript{}, nil
}
func (fakeManageVideoService) Cancel(identity.Principal, string, string) (pebblestore.TranscriptionJob, error) {
	return pebblestore.TranscriptionJob{}, nil
}
