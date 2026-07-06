package run

import (
	"strings"

	"swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func modelCatalogLookup(modelSvc *model.Service, providerID, modelID string) (*pebblestore.ModelCatalogRecord, error) {
	if modelSvc == nil {
		return nil, nil
	}
	lookup, err := modelSvc.GetCatalog(providerID, modelID)
	if err != nil {
		if strings.Contains(err.Error(), "model catalog is not configured") {
			return nil, nil
		}
		return nil, err
	}
	if !lookup.Found {
		return nil, nil
	}
	record := lookup.Record
	return &record, nil
}

func catalogRecordValue(record *pebblestore.ModelCatalogRecord) any {
	if record == nil {
		return nil
	}
	return *record
}
