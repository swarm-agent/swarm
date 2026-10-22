package api

import (
	"reflect"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: publication must describe a ready PR, not claim a local merge or
// leave code owed. recordPublishedPullRequest owns the post-publication task
// mutation; testing it directly isolates metadata from external forge commands.
func TestPublishedPullRequestDeliverables(t *testing.T) {
	task := &pebblestore.ProjectTaskRecord{Deliverables: []pebblestore.ProjectTaskDeliverable{
		{ID: "code", Kind: "pr", Status: "pending", Title: "Code PR & Verified Tests"},
		{ID: "legacy", Kind: "code_pr", Status: "pending", Title: "Code PR"},
		{ID: "media", Kind: "video", Status: "pending", Title: "Video", MediaURL: "media"},
	}}
	want := append([]pebblestore.ProjectTaskDeliverable(nil), task.Deliverables...)
	receipt := &pebblestore.ProjectTaskIntegration{State: "pull_request", PullRequestURL: "https://example.com/pulls/7"}
	for i := 0; i < 2; i++ {
		want[i].Status = "ready"
		want[i].Title = "Pull request " + receipt.PullRequestURL
	}
	recordPublishedPullRequest(task, receipt)
	if !reflect.DeepEqual(task.Deliverables, want) || !reflect.DeepEqual(task.Integration, receipt) || task.IsIntegrated {
		t.Fatalf("unexpected publication metadata: %+v", task)
	}
	recordPublishedPullRequest(task, receipt)
	if !reflect.DeepEqual(task.Deliverables, want) {
		t.Fatal("repeated publication changed deliverables")
	}
}
