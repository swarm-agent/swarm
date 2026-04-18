package app

import (
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

func authCredentialUpsertToast(upsert *ui.AuthModalUpsert, record client.AuthCredential) (ui.ToastLevel, string) {
	provider := strings.TrimSpace(record.Provider)
	if provider == "" && upsert != nil {
		provider = strings.TrimSpace(upsert.Provider)
	}
	provider = emptyFallback(provider, "provider")

	requestedActive := upsert != nil && upsert.Active
	switch {
	case requestedActive && record.Active:
		return ui.ToastSuccess, fmt.Sprintf("credential saved for %s and set active (replaced previous active credential, if any)", provider)
	case requestedActive && !record.Active:
		return ui.ToastWarning, fmt.Sprintf("credential saved for %s but it is not active. Press a on this credential to set it active.", provider)
	case record.Active:
		return ui.ToastSuccess, fmt.Sprintf("credential saved for %s and set active", provider)
	default:
		return ui.ToastWarning, fmt.Sprintf("credential saved for %s but it is not active. Existing active credential is unchanged. Press a on this credential to activate it, or save with active=y.", provider)
	}
}
