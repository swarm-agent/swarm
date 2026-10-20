package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
)

type managedControlStoreFixture struct{ manageEnvironmentStore }

type managedControlFixture struct {
	ProjectTaskLifecycleService
	calls     int
	principal identity.Principal
	session   string
	request   TaskEnvironmentRequest
	err       error
}

func (f *managedControlFixture) ManageTaskEnvironment(_ context.Context, p identity.Principal, session string, req TaskEnvironmentRequest) (TaskEnvironmentResult, error) {
	f.calls++
	f.principal, f.session, f.request = p, session, req
	return TaskEnvironmentResult{TaskRevision: 7}, f.err
}

// Requirement: a managed Swarm owner can control its receipt, including release
// after expiry, without gaining host execution, provisioning, reassignment or
// Coder management rights. executeOne and authorizeEnvironmentAccess own this
// dispatch boundary; the unchanged task service owns receipt authorization.
// A recording task authority plus provider/host effect checks is the narrowest
// hermetic proof. This does not claim provider or live lifecycle qualification.
func TestManagedReceiptControlDispatch(t *testing.T) {
	for _, name := range []string{"release", "expired_release", "released_release", "get_operation", "get_deployment", "cancel_operation", "list_attachments", "hyphen_alias", "foreign_user", "foreign_account", "foreign_session", "coder", "authority_denied", "build", "deploy", "ensure", "exec", "create", "update", "destroy", "attach_task", "acquire_attachment", "unknown"} {
		t.Run(name, func(t *testing.T) {
			r, scope, sessions, deployments := newManagedExecutionFixture(t)
			sessions.snapshot.Metadata["agent_profile"] = map[string]any{"name": "swarm"}
			authority := &managedControlFixture{}
			r.environmentsStore, r.projectTaskLifecycle = &managedControlStoreFixture{}, authority
			action, toolName, wantCalls, wantError := name, "manage_environments", 1, false
			switch name {
			case "expired_release":
				action = "release"
				deployments.lease.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
			case "released_release":
				action = "release"
				deployments.lease.Active = false
			case "hyphen_alias":
				action, toolName = "release", "manage-environments"
			case "coder":
				// A Coder may use the environments the user set up.
				action = "release"
				sessions.snapshot.Metadata["agent_profile"] = map[string]any{"name": "coder"}
			case "foreign_user", "foreign_account", "foreign_session":
				action, wantCalls, wantError = "release", 0, true
				switch name {
				case "foreign_user":
					scope.Principal.UserID = "foreign"
				case "foreign_account":
					scope.Principal.AccountScopeID = "foreign"
				case "foreign_session":
					scope.Principal.SessionID = "foreign"
				}
			case "authority_denied":
				action, wantError = "release", true
				authority.err = errors.New("receipt belongs to another consumer")
			case "release", "get_operation", "get_deployment", "cancel_operation", "list_attachments":
			default:
				wantCalls, wantError = 0, true
			}
			args, _ := json.Marshal(TaskEnvironmentRequest{Action: action, ProjectID: "project", TaskID: "task", AttachmentID: "attachment", WorkspaceID: "workspace", LeaseID: "receipt"})
			before, _ := json.Marshal(deployments.lease)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			output, err := r.executeOne(ctx, scope, Call{Name: toolName, Arguments: string(args)}, nil)
			if (err != nil) != wantError || authority.calls != wantCalls {
				t.Fatalf("wrong dispatch: calls=%d output=%s err=%v", authority.calls, output, err)
			}
			if wantCalls == 1 && (authority.session != scope.SessionID || authority.principal != scope.Principal || authority.request.Action != action || authority.request.LeaseID != "receipt" || authority.request.TaskID != "task") {
				t.Fatalf("lost authority or explicit references: %#v", authority)
			}
			if authority.err != nil && !errors.Is(err, authority.err) {
				t.Fatal("task authority denial was swallowed")
			}
			after, _ := json.Marshal(deployments.lease)
			if deployments.calls != 0 || !bytes.Equal(before, after) {
				t.Fatal("control dispatch executed a provider or changed its receipt")
			}
			for _, path := range []string{"host-marker", ".git"} {
				if _, err := os.Stat(filepath.Join(scope.PrimaryPath, path)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("host side effect: %s %v", path, err)
				}
			}
		})
	}
}
