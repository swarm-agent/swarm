package pebblestore

// Internal trigger keys have no slot metadata and cannot be granted or listed.
// The reserved name contains '/', which ValidateSecretSlotName refuses.
func (s *AuthStore) PutWorkflowTriggerKey(account, id string, key []byte) error {
	return s.PutSecretValueForAccount(account, "workflow-trigger/"+id, key)
}
func (s *AuthStore) GetWorkflowTriggerKey(account, id string) ([]byte, bool, error) {
	return s.GetSecretValueForAccount(account, "workflow-trigger/"+id)
}
func (s *AuthStore) DeleteWorkflowTriggerKey(account, id string) error {
	return s.DeleteSecretValueForAccount(account, "workflow-trigger/"+id)
}
