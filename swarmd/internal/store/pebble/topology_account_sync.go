package pebblestore

import (
	"errors"
	"strings"
)

func ensureTopologyMergeSameAccount(existingAccountScopeID, incomingAccountScopeID string) error {
	existingAccountScopeID = strings.TrimSpace(existingAccountScopeID)
	incomingAccountScopeID = strings.TrimSpace(incomingAccountScopeID)
	if existingAccountScopeID == "" || incomingAccountScopeID == "" {
		return nil
	}
	if existingAccountScopeID != incomingAccountScopeID {
		return errors.New("topology merge across account scopes is forbidden")
	}
	return nil
}
