package pebblestore

func taskDeliveryInput(run string, updates []ProjectTaskUpdate) V3SessionMutationInput {
	return V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationDeliverTasks, ClientRequestID: "delivery", PayloadHash: "derived", TaskDelivery: &V3ProjectTaskDeliveryMutation{RunID: run, Updates: updates}}
}
