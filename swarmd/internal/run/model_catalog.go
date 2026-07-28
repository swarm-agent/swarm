package run

import (
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func modelCatalogLookupWithMeta(modelSvc *model.Service, providerID, modelID string) (*pebblestore.ModelCatalogRecord, *pebblestore.ModelCatalogMeta, error) {
	if modelSvc == nil {
		return nil, nil, nil
	}
	lookup, err := modelSvc.GetCatalog(providerID, modelID)
	if err != nil {
		if strings.Contains(err.Error(), "model catalog is not configured") {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if !lookup.Found {
		return nil, nil, nil
	}
	record := lookup.Record
	return &record, lookup.Meta, nil
}

func modelCatalogLookup(modelSvc *model.Service, providerID, modelID string) (*pebblestore.ModelCatalogRecord, error) {
	record, _, err := modelCatalogLookupWithMeta(modelSvc, providerID, modelID)
	return record, err
}

func catalogRecordValue(record *pebblestore.ModelCatalogRecord) any {
	if record == nil {
		return nil
	}
	return *record
}

type compactModelRuntime struct {
	ProviderID string
	Preference pebblestore.ModelPreference
	Catalog    pebblestore.ModelCatalogRecord
}

func resolveCompactModelRuntime(modelSvc *model.Service, resolved model.ResolvedPreference) (compactModelRuntime, error) {
	providerID := strings.ToLower(strings.TrimSpace(resolved.Preference.Provider))
	modelID := strings.TrimSpace(resolved.Preference.Model)
	record, err := modelCatalogLookup(modelSvc, providerID, modelID)
	if err != nil {
		return compactModelRuntime{}, err
	}
	if record == nil {
		return compactModelRuntime{}, fmt.Errorf("Compact model catalog record for provider %q model %q is unavailable", providerID, modelID)
	}
	preference := resolved.Preference
	preference.Provider = providerID
	preference.Model = modelID
	preference.Thinking = normalizeThinkingWithProvider(providerID, preference.Thinking)
	preference.ServiceTier = resolvedServiceTierForProvider(providerID, preference.ServiceTier)
	return compactModelRuntime{ProviderID: providerID, Preference: preference, Catalog: *record}, nil
}

func (runtime compactModelRuntime) apply(req provideriface.Request) provideriface.Request {
	req.Model = runtime.Preference.Model
	req.Thinking = runtime.Preference.Thinking
	req.ServiceTier = runtime.Preference.ServiceTier
	req.ContextMode = runtime.Preference.ContextMode
	req.ModelCatalog = runtime.Catalog
	return req
}
