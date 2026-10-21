package pebblestore

import (
	"errors"
	"reflect"
	"testing"
)

// Purpose: FinishProjectTaskIntegrationGuarded must complete only code PR
// deliverables after verified delivery, never after failed or stale promotion.
// Its mutation callback is the narrowest layer owning this metadata transition;
// a transactional test store checks rejection leaves the entire record unchanged.
func TestIntegrationCodeDeliverables(t *testing.T) {
	for _, state := range []string{"integrated", "already_integrated", "recovered", "equivalent", "failed", "conflict", "apply_error", "stale", "stale_revision"} {
		t.Run(state, func(t *testing.T) {
			task := ProjectTaskRecord{ID: "task", ProjectID: "project", SessionID: "session", ActiveAttemptID: "attempt", Revision: 2,
				Integration: &ProjectTaskIntegration{OperationID: "operation", State: "in_progress"},
				Deliverables: []ProjectTaskDeliverable{
					{ID: "deliv_code", Kind: "pr", Status: "pending", Title: "Code PR & Verified Tests"},
					{ID: "code", Kind: "code_pr", Status: "pending", Title: "Code PR"},
					{ID: "media", Kind: "video", Status: "pending", Title: "Video", MediaURL: "media"},
					{ID: "report", Kind: "report", Status: "ready", Title: "Report"},
				},
			}
			before := task
			store := &deliveryMutationStore{task: task}
			receipt := &ProjectTaskIntegration{OperationID: "operation", SessionID: "session", AttemptID: "attempt", State: state, TargetBranch: "dev"}
			var apply func() error
			if state == "apply_error" {
				receipt.State = "integrated"
				apply = func() error { return errors.New("promotion failed") }
			}
			if state == "stale" {
				receipt.State = "integrated"
				receipt.OperationID = "old"
			}
			revision := 2
			if state == "stale_revision" {
				receipt.State = "integrated"
				revision++
			}
			_, err := FinishProjectTaskIntegrationGuarded(store, "account", &task, receipt, revision, apply)
			if state == "apply_error" || state == "stale" || state == "stale_revision" {
				if err == nil || !reflect.DeepEqual(store.task, before) {
					t.Fatalf("rejected promotion mutated task: err=%v task=%+v", err, store.task)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := append([]ProjectTaskDeliverable(nil), before.Deliverables...)
			if state != "failed" && state != "conflict" {
				for i := 0; i < 2; i++ {
					want[i].Status = "ready"
					want[i].Title = "Code merged into dev"
					if state == "recovered" || state == "equivalent" {
						want[i].Title = "Code delta delivered to dev"
					}
				}
			}
			if !reflect.DeepEqual(store.task.Deliverables, want) {
				t.Fatalf("deliverables = %+v; want %+v", store.task.Deliverables, want)
			}
		})
	}
}

type deliveryMutationStore struct{ task ProjectTaskRecord }

func (s *deliveryMutationStore) UpdateProjectTask(_, _, _ string, mutate func(*ProjectTaskRecord) error) (*ProjectTaskRecord, error) {
	next := s.task
	next.Deliverables = append([]ProjectTaskDeliverable(nil), s.task.Deliverables...)
	if err := mutate(&next); err != nil {
		return nil, err
	}
	s.task = next
	return &next, nil
}

func taskDeliveryInput(run string, updates []ProjectTaskUpdate) V3SessionMutationInput {
	return V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationDeliverTasks, ClientRequestID: "delivery", PayloadHash: "derived", TaskDelivery: &V3ProjectTaskDeliveryMutation{RunID: run, Updates: updates}}
}
