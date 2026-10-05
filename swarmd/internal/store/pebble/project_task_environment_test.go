package pebblestore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
)

func taskEnvironmentFixture(id string) environments.TaskEnvironmentAttachment {
	source := environments.CommittedBuildSource{WorkspaceID: "workspace", WorkspaceGeneration: 1, Commit: strings.Repeat("a", 40)}
	return environments.TaskEnvironmentAttachment{
		ID: id, Revision: 1, AccountScopeID: "account", ProjectID: "project", TaskID: "task", AttemptID: "initial",
		EnvironmentID: "environment", EnvironmentName: "Review", State: "ready", ExpiresAt: 4102444800000, RetainForReview: true,
		Source: environments.PreparedDeploymentSource{
			AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "environment", DeploymentID: "deployment", CreatedAt: 1, ContainerID: "container",
			Build: environments.ImageBuildResult{OperationID: "build", ConnectionID: "local", DefinitionDigest: "definition", ContextDigest: "context", ImageID: "sha256:" + strings.Repeat("b", 64), Product: source, Recipe: source, RecipeFile: "recipe/Containerfile"},
		},
	}
}

// Purpose: attachment CAS at UpdateProjectTask must reject stale/cross-identity
// writes without changing execution or evidence. Real Pebble is the narrowest
// layer proving durable postconditions, including concurrent writers and restart.
func TestTaskEnvironmentCASAndRestart(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewSessionStore(db)
	original := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "task", Status: "needs_review", SessionID: "session", Agent: "swarm", Revision: 1}
	if err := s.PutProjectTask("account", original); err != nil {
		t.Fatal(err)
	}
	a := taskEnvironmentFixture("attachment")
	deployments := NewDeploymentStore(db)
	deployment, err := deployments.Save(environments.Deployment{ID: a.Source.DeploymentID, AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: a.EnvironmentID, ConnectionID: "local", Name: "Review", Status: environments.DeploymentStatusReady, Build: &a.Source.Build, Runtime: environments.RuntimeMetadata{ContainerID: a.Source.ContainerID}, CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	req := TaskEnvironmentMutation{ExpectedTaskRevision: 1, AttachmentID: a.ID, Attachment: &a}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.MutateProjectTaskEnvironment("account", "project", "task", req)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("CAS successes = %d", success)
	}
	before, _, err := s.GetProjectTask("account", "project", "task")
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision != 2 || before.Status != original.Status || before.SessionID != original.SessionID || before.ActiveAttemptID != original.ActiveAttemptID || len(before.EnvironmentAttachments) != 1 {
		t.Fatal("attach changed execution or lost evidence")
	}
	for _, identity := range [][3]string{{"other", "project", "task"}, {"account", "other", "task"}, {"account", "project", "other"}} {
		if _, err := s.MutateProjectTaskEnvironment(identity[0], identity[1], identity[2], req); err == nil {
			t.Fatal("cross-identity write accepted")
		}
	}
	replacement := a
	replacement.Revision = 2
	bad := TaskEnvironmentMutation{ExpectedTaskRevision: 2, ExpectedAttachmentRevision: 9, AttachmentID: a.ID, Attachment: &replacement}
	if _, err := s.MutateProjectTaskEnvironment("account", "project", "task", bad); err == nil {
		t.Fatal("stale attachment accepted")
	}
	bad.ExpectedAttachmentRevision = 1
	replacement.TaskID = "other"
	if _, err := s.MutateProjectTaskEnvironment("account", "project", "task", bad); err == nil {
		t.Fatal("mismatched payload accepted")
	}
	if err := s.PutProjectTask("account", original); err == nil {
		t.Fatal("stale full record erased attachment")
	}
	replacement = a
	replacement.Revision = 2
	s.SetProjectTaskUpdateHookForTest(func(string) error { return fmt.Errorf("injected update failure") })
	if _, err := s.MutateProjectTaskEnvironment("account", "project", "task", bad); err == nil {
		t.Fatal("injected failure ignored")
	}
	s.SetProjectTaskUpdateHookForTest(nil)
	after, _, _ := s.GetProjectTask("account", "project", "task")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejection changed durable task")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s = NewSessionStore(db)
	after, _, err = s.GetProjectTask("account", "project", "task")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("restart lost attachment")
	}
	followup, err := s.ReserveTaskFollowup("account", "project", "task", "user", "followup", "continue", 2, 200)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(followup.EnvironmentAttachments, before.EnvironmentAttachments) {
		t.Fatal("reopen rebound retained evidence")
	}
	card := compactProjectTask(*followup)
	if len(card.EnvironmentAttachments) != 1 || card.EnvironmentAttachments[0].State != "stale" || card.EnvironmentAttachments[0].AttemptID != "initial" {
		t.Fatal("reopened attempt not projected stale")
	}
	// Explicit reassignment requires both current revisions; no source is inferred.
	replacement = a
	replacement.Revision = 2
	replacement.AttemptID = followup.ActiveAttemptID
	updated, err := s.MutateProjectTaskEnvironment("account", "project", "task", TaskEnvironmentMutation{ExpectedTaskRevision: followup.Revision, ExpectedAttachmentRevision: 1, AttachmentID: a.ID, Attachment: &replacement})
	if err != nil {
		t.Fatal(err)
	}
	detached, err := s.MutateProjectTaskEnvironment("account", "project", "task", TaskEnvironmentMutation{ExpectedTaskRevision: updated.Revision, ExpectedAttachmentRevision: 2, AttachmentID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(detached.EnvironmentAttachments) != 0 || detached.Status != updated.Status || detached.SessionID != updated.SessionID || !reflect.DeepEqual(detached.Attempts, updated.Attempts) {
		t.Fatal("detach changed task execution")
	}
	retained, found, err := NewDeploymentStore(db).Get("account", "workspace", "deployment")
	if err != nil || !found || !reflect.DeepEqual(deployment, retained) {
		t.Fatal("detach changed deployment")
	}
}

// Purpose: typed attachment validation/summary projection must bound persisted
// metadata and omit lease credentials. This storage-layer test exercises the
// public mutation boundary and asserts rejected overflow leaves the task intact.
func TestTaskEnvironmentBoundsAndSerialization(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	task := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "task", SessionID: "session", Revision: 1}
	if err := s.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < environments.MaxTaskEnvironmentAttachments; i++ {
		a := taskEnvironmentFixture(fmt.Sprintf("a-%d", i))
		task, err = s.MutateProjectTaskEnvironment("account", "project", "task", TaskEnvironmentMutation{ExpectedTaskRevision: task.Revision, AttachmentID: a.ID, Attachment: &a})
		if err != nil {
			t.Fatal(err)
		}
	}
	a := taskEnvironmentFixture("overflow")
	if _, err := s.MutateProjectTaskEnvironment("account", "project", "task", TaskEnvironmentMutation{ExpectedTaskRevision: task.Revision, AttachmentID: a.ID, Attachment: &a}); err == nil {
		t.Fatal("overflow accepted")
	}
	after, _, _ := s.GetProjectTask("account", "project", "task")
	if !reflect.DeepEqual(task, after) {
		t.Fatal("overflow mutated task")
	}
	for _, mutate := range []func(*environments.TaskEnvironmentAttachment){
		func(a *environments.TaskEnvironmentAttachment) { a.EnvironmentName = strings.Repeat("x", 257) },
		func(a *environments.TaskEnvironmentAttachment) { a.Source.AccountScopeID = "other" },
		func(a *environments.TaskEnvironmentAttachment) { a.Source.Build.Product.Commit = "dev" },
		func(a *environments.TaskEnvironmentAttachment) { a.Source.CreatedAt = 0 },
		func(a *environments.TaskEnvironmentAttachment) { a.State = "building"; a.OperationID = "" },
	} {
		invalid := a
		mutate(&invalid)
		if invalid.Validate() == nil {
			t.Fatal("invalid attachment accepted")
		}
	}
	raw, err := json.Marshal(compactProjectTask(*task))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"lease_id", "lease_token", "receipt", "secret", "password"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("serialized %s", forbidden)
		}
	}
	if !strings.Contains(string(raw), "environment_attachments") || !strings.Contains(string(raw), a.Source.Build.Product.Commit) {
		t.Fatal("summary dropped provenance")
	}
	if a.EffectiveState("initial", a.ExpiresAt) != "stale" {
		t.Fatal("expiry not fenced")
	}
}

// Purpose: detach must permanently revoke an attachment identity across restart.
// MutateProjectTaskEnvironment and full-record writes own this invariant; real
// Pebble verifies that recreating an ID cannot revive a source-bound old lease.
func TestTaskEnvironmentDetachPreventsABA(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewSessionStore(db)
	task := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "task", SessionID: "session", Revision: 1}
	if err := s.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	a := taskEnvironmentFixture("attachment")
	attached, err := s.MutateProjectTaskEnvironment("account", "project", "task", TaskEnvironmentMutation{ExpectedTaskRevision: 1, AttachmentID: a.ID, Attachment: &a})
	if err != nil {
		t.Fatal(err)
	}
	detached, err := s.MutateProjectTaskEnvironment("account", "project", "task", TaskEnvironmentMutation{ExpectedTaskRevision: attached.Revision, ExpectedAttachmentRevision: 1, AttachmentID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s = NewSessionStore(db)
	if _, err := s.MutateProjectTaskEnvironment("account", "project", "task", TaskEnvironmentMutation{ExpectedTaskRevision: detached.Revision, AttachmentID: a.ID, Attachment: &a}); err == nil {
		t.Fatal("retired identity recreated")
	}
	forged := *attached
	forged.Revision = detached.Revision + 1
	if err := s.PutProjectTask("account", &forged); err == nil {
		t.Fatal("full write erased revocation")
	}
	after, _, err := s.GetProjectTask("account", "project", "task")
	if err != nil || !reflect.DeepEqual(detached, after) {
		t.Fatal("rejected ABA mutated task")
	}
	a.ID = "fresh-attachment"
	if _, err := s.MutateProjectTaskEnvironment("account", "project", "task", TaskEnvironmentMutation{ExpectedTaskRevision: detached.Revision, AttachmentID: a.ID, Attachment: &a}); err != nil {
		t.Fatal(err)
	}
}
