package api

import (
	"swarm/packages/swarmd/internal/flow"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func ownedTestAcceptedAssignment(assignment flow.Assignment) flow.AcceptedAssignment {
	return pebblestore.ApplyFlowOwnerToAcceptedAssignment(flow.AcceptedAssignment{Assignment: assignment}, testAccountScopeID, testUserID)
}

func ownedTestRunStart(start flow.RunStart) flow.RunStart {
	start.AccountScopeID = testAccountScopeID
	start.UserID = testUserID
	return start
}

func ownedTestRunSummary(record pebblestore.FlowRunSummaryRecord) pebblestore.FlowRunSummaryRecord {
	record.AccountScopeID = testAccountScopeID
	record.UserID = testUserID
	return record
}
