package pebblestore

import (
	"encoding/json"
	"strings"

	"github.com/cockroachdb/pebble"
	"swarm-refactor/swarmtui/pkg/environments"
)

const taskEnvironmentIndexPrefix = "task-environment-index/"

type taskEnvironmentTarget struct {
	ProjectID string `json:"project_id"`
	TaskID string `json:"task_id"`
	AttachmentID string `json:"attachment_id"`
	Revision int `json:"attachment_revision"`
}

func attachmentIndexKey(a environments.TaskEnvironmentAttachment) string {
	return taskEnvironmentIndexPrefix + keyPart(a.AccountScopeID) + "/" + keyPart(a.Source.WorkspaceID) + "/" + keyPart(a.EnvironmentID) + "/" + keyPart(a.ProjectID) + "/" + keyPart(a.TaskID) + "/" + keyPart(a.ID)
}

func setTaskEnvironmentIndex(batch *pebble.Batch, prior, next ProjectTaskRecord) error {
	for _, a := range prior.EnvironmentAttachments {
		if err := batch.Delete([]byte(attachmentIndexKey(a)), nil); err != nil { return err }
	}
	for _, a := range next.EnvironmentAttachments {
		raw, err := json.Marshal(taskEnvironmentTarget{next.ProjectID, next.ID, a.ID, a.Revision})
		if err != nil { return err }
		if err := batch.Set([]byte(attachmentIndexKey(a)), raw, nil); err != nil { return err }
	}
	return nil
}

// Enrich the same durable event committed with the resource transition. Clients
// hydrate these tasks; revisions are invalidation evidence, never access grants.
// Large fanout explicitly requests workspace invalidation rather than truncating
// silently. No task scan or task revision mutation occurs on heartbeat updates.
func (s *Store) taskEnvironmentEventPayload(m *environmentRealtimeMutation) (json.RawMessage, error) {
	payload := map[string]any{}
	if len(m.eventPayload) > 0 { if err := json.Unmarshal(m.eventPayload, &payload); err != nil { return nil, err } }
	envIDs := map[string]bool{}
	for _, key := range m.deletes {
		if strings.HasPrefix(key, KeyDeploymentAccountPrefix) {
			var dep environments.Deployment
			found, err := s.GetJSON(key, &dep)
			if err != nil { return nil, err }
			if found { envIDs[dep.EnvironmentID] = true }
		}
	}
	for key, raw := range m.writes {
		if strings.HasPrefix(key, KeyDeploymentAccountPrefix) || strings.HasPrefix(key, KeyEnvironmentOperationAccountPrefix) || strings.HasPrefix(key, KeyDeploymentLeaseAccountPrefix) {
			var identity struct { EnvironmentID string `json:"environment_id"` }
			if err := json.Unmarshal(raw, &identity); err != nil { return nil, err }
			if identity.EnvironmentID != "" { envIDs[identity.EnvironmentID] = true }
		}
	}
	var targets []taskEnvironmentTarget
	for env := range envIDs {
		prefix := taskEnvironmentIndexPrefix + keyPart(m.accountScopeID) + "/" + keyPart(m.workspaceID) + "/" + keyPart(env) + "/"
		err := s.IteratePrefix(prefix, 257, func(_ string, raw []byte) error {
			var target taskEnvironmentTarget
			if err := json.Unmarshal(raw, &target); err != nil { return err }
			targets = append(targets, target)
			return nil
		})
		if err != nil { return nil, err }
		if len(targets) > 256 { payload["task_environment_workspace_invalidated"] = true; targets = nil; break }
	}
	if len(targets) > 0 { payload["task_environment_targets"] = targets }
	// Pre-index attachment records are repaired by fresh workspace hydration.
	// Always retain this bounded scope hint; never infer no attachments from an
	// empty index after upgrade.
	if len(envIDs) > 0 { payload["task_environment_workspace_invalidated"] = true }
	payload["account_scope_id"], payload["workspace_id"] = m.accountScopeID, m.workspaceID
	return json.Marshal(payload)
}

// Lease/index writes participate in the same canonical outbox transaction.
type environmentLeaseBatch struct {
	store *Store
	mutation *environmentRealtimeMutation
}
func (s *LeaseStore) realtimeBatch(account, workspace string) *environmentLeaseBatch {
	return &environmentLeaseBatch{s.store, &environmentRealtimeMutation{accountScopeID: account, workspaceID: workspace}}
}
func (b *environmentLeaseBatch) Set(key, value []byte, _ *pebble.WriteOptions) error { b.mutation.putBytes(string(key), append([]byte(nil), value...)); return nil }
func (b *environmentLeaseBatch) Delete(key []byte, _ *pebble.WriteOptions) error { b.mutation.delete(string(key)); return nil }
func (b *environmentLeaseBatch) Close() error { return nil }
func (b *environmentLeaseBatch) Commit(_ *pebble.WriteOptions) error {
	b.store.environmentsMu.Lock()
	err := b.store.commitEnvironmentRealtime(b.mutation)
	b.store.environmentsMu.Unlock()
	if err == nil { b.store.publishEnvironmentRealtime(b.mutation) }
	return err
}
