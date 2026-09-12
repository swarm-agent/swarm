package session

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: proposal validation is the narrow layer enforcing complete executable
// instructions and rejecting prior execution state before store publication.
func TestAutomationV2ProposalRejectsRuntime(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{Title:"Review",Info:pebblestore.SessionPlanInfo{Goal:"Work"},AutomationV2:&pebblestore.AutomationV2Settings{SchemaVersion:2,Schedule:pebblestore.AutomationV2Schedule{Kind:"interval",IntervalSeconds:60},Missed:"skip",Overlap:"serialize",ActivateOnAccept:true,Expiration:pebblestore.AutomationV2Expiration{Kind:"indefinite"}},Checkpoints:[]pebblestore.SessionPlanCheckpoint{{ID:"cp-1",Title:"Work",Objective:"Implement",Status:"pending",Order:1,AcceptanceCriteria:[]string{"Works"}}}}
	if err := validateAutomationV2Proposal(doc); err != nil { t.Fatal(err) }
	for _,mutate := range []func(*pebblestore.SessionPlanDocument){
		func(d *pebblestore.SessionPlanDocument){ d.ExecutionState=&pebblestore.SessionPlanExecutionState{Status:"idle"} },
		func(d *pebblestore.SessionPlanDocument){ d.Checkpoints[0].RunID="forged" },
		func(d *pebblestore.SessionPlanDocument){ d.Checkpoints[0].Status="in_progress" },
		func(d *pebblestore.SessionPlanDocument){ d.Checkpoints[0].AcceptanceCriteria=nil },
	} {
		copy := clonePlanDocument(doc); mutate(copy)
		if validateAutomationV2Proposal(copy)==nil { t.Fatal("invalid proposal accepted") }
	}
}
