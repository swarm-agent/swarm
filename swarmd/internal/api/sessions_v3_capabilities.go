package api

import (
	"context"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// compileSessionV3MediaContract is shared by capability reads and execution.
// It consumes resolved authority only; tool compilation and instruction
// composition must remain downstream in resolveSessionV3Runtime.
func (e *sessionV3Executor) compileSessionV3MediaContract(principal identity.Principal, resolved sessionV3ResolvedRuntime) (provideriface.SessionMediaContract, error) {
	if e.server.providers == nil {
		return provideriface.SessionMediaContract{}, errors.New("v3 provider registry is not configured")
	}
	providerID := strings.ToLower(strings.TrimSpace(resolved.Preference.Provider))
	providerRunner, ok := e.server.providers.GetRunner(providerID)
	if !ok || providerRunner == nil {
		return provideriface.SessionMediaContract{}, errors.New("resolved v3 provider runner is not configured")
	}
	var catalog *pebblestore.ModelCatalogRecord
	if record, ok := resolved.ModelCatalog.(pebblestore.ModelCatalogRecord); ok {
		catalog = &record
	}
	return runruntime.CompileSessionMediaContract(runruntime.SessionMediaContractInput{
		ProviderID: providerID, Model: resolved.Preference.Model, Catalog: catalog, CatalogMeta: resolved.CatalogMeta,
		Adapter:         runruntime.ResolveMediaAdapterDeclaration(identity.ContextWithPrincipal(context.Background(), principal), providerID, providerRunner),
		AgentAuthorized: runruntime.AgentProfileAuthorizesMedia(resolved.AgentProfile), ExecutionMode: resolved.Session.Mode,
		WorkspaceScope: resolved.Scope.PrimaryPath, SessionScope: resolved.Session.ID,
	}), nil
}
