package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"swarm-refactor/swarmtui/internal/client"
)

type providerModelResolverResult struct {
	ProviderIDs         []string
	ProviderStatuses    map[string]client.ProviderStatus
	ModelsByProvider    map[string][]string
	ReasoningByKey      map[string]bool
	CatalogByKey        map[string]client.ModelCatalogRecord
	FavoritesByKey      map[string]client.ModelFavoriteRecord
	Warnings            []string
	AuthOnlyProviderIDs []string
}

func (a *App) resolveProviderModelData(ctx context.Context, hints []string, favoritesLimit, catalogLimit int) providerModelResolverResult {
	result := providerModelResolverResult{
		ProviderStatuses: make(map[string]client.ProviderStatus, 16),
		ModelsByProvider: make(map[string][]string, 16),
		ReasoningByKey:   make(map[string]bool, 2048),
		CatalogByKey:     make(map[string]client.ModelCatalogRecord, 2048),
		FavoritesByKey:   make(map[string]client.ModelFavoriteRecord, 1024),
		Warnings:         make([]string, 0, 16),
	}
	if favoritesLimit <= 0 {
		favoritesLimit = 2000
	}
	if catalogLimit <= 0 {
		catalogLimit = 1200
	}
	if a.api == nil {
		result.Warnings = append(result.Warnings, "model API is unavailable")
		return result
	}

	providerStatuses, providerErr := a.api.ListProviders(ctx)
	if providerErr != nil {
		result.Warnings = append(result.Warnings, "provider status unavailable")
	}
	favorites, favoritesErr := a.api.ListModelFavorites(ctx, "", "", favoritesLimit)
	if favoritesErr != nil {
		result.Warnings = append(result.Warnings, "favorites unavailable")
	}

	providerSet := make(map[string]struct{}, len(modelPresetsByProvider)+len(providerStatuses)+len(favorites)+len(hints)+2)
	addProvider := func(providerID string) {
		providerID = normalizeModelProviderID(providerID)
		if providerID != "" {
			providerSet[providerID] = struct{}{}
		}
	}

	for providerID := range modelPresetsByProvider {
		addProvider(providerID)
	}
	for _, status := range providerStatuses {
		id := normalizeModelProviderID(status.ID)
		if id == "" {
			continue
		}
		status.ID = id
		result.ProviderStatuses[id] = status
		addProvider(id)
	}
	for _, hint := range hints {
		addProvider(hint)
	}
	addProvider(a.homeModel.ModelProvider)

	for _, favorite := range favorites {
		providerID := normalizeModelProviderID(favorite.Provider)
		modelID := strings.TrimSpace(favorite.Model)
		key := modelEntryKey(providerID, modelID)
		if key == "" {
			continue
		}
		favorite.Provider = providerID
		favorite.Model = modelID
		result.FavoritesByKey[key] = favorite
		addProvider(providerID)
	}

	providerIDs := make([]string, 0, len(providerSet))
	authOnlyProviderIDs := make([]string, 0, 4)
	for providerID := range providerSet {
		providerIDs = append(providerIDs, providerID)
	}
	sort.Strings(providerIDs)
	for _, providerID := range providerIDs {
		status, ok := result.ProviderStatuses[providerID]
		if providerID == "exa" {
			authOnlyProviderIDs = append(authOnlyProviderIDs, providerID)
			continue
		}
		if ok && providerID != "copilot" && !status.Runnable && strings.Contains(strings.ToLower(strings.TrimSpace(status.RunReason)), "no model runner") {
			authOnlyProviderIDs = append(authOnlyProviderIDs, providerID)
			continue
		}
		result.ProviderIDs = append(result.ProviderIDs, providerID)
	}
	result.AuthOnlyProviderIDs = authOnlyProviderIDs

	for _, providerID := range providerIDs {
		records, err := a.api.ListModelCatalog(ctx, providerID, catalogLimit)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s catalog unavailable", providerID))
			continue
		}
		for _, record := range records {
			recordProvider := normalizeModelProviderID(record.Provider)
			if recordProvider == "" {
				recordProvider = providerID
			}
			modelID := strings.TrimSpace(record.Model)
			if modelID == "" || !modelAllowedByProviderPreset(recordProvider, modelID) {
				continue
			}
			key := modelEntryKey(recordProvider, modelID)
			if key == "" {
				continue
			}
			record.Provider = recordProvider
			record.Model = modelID
			result.CatalogByKey[key] = record
			result.ModelsByProvider[recordProvider] = append(result.ModelsByProvider[recordProvider], modelID)
			result.ReasoningByKey[key] = record.Reasoning
		}
	}

	for _, providerID := range providerIDs {
		for _, preset := range modelPresetListForProvider(providerID) {
			modelID := strings.TrimSpace(preset)
			key := modelEntryKey(providerID, modelID)
			if key == "" {
				continue
			}
			result.ModelsByProvider[providerID] = append(result.ModelsByProvider[providerID], modelID)
			if _, ok := result.ReasoningByKey[key]; !ok {
				result.ReasoningByKey[key] = true
			}
		}
		if status, ok := result.ProviderStatuses[providerID]; ok {
			modelID := strings.TrimSpace(status.DefaultModel)
			key := modelEntryKey(providerID, modelID)
			if key != "" && modelAllowedByProviderPreset(providerID, modelID) {
				result.ModelsByProvider[providerID] = append(result.ModelsByProvider[providerID], modelID)
				if _, ok := result.ReasoningByKey[key]; !ok {
					result.ReasoningByKey[key] = true
				}
			}
		}
	}

	for key, favorite := range result.FavoritesByKey {
		providerID := normalizeModelProviderID(favorite.Provider)
		modelID := strings.TrimSpace(favorite.Model)
		if providerID == "" || modelID == "" || !modelAllowedByProviderPreset(providerID, modelID) {
			delete(result.FavoritesByKey, key)
			continue
		}
		result.ModelsByProvider[providerID] = append(result.ModelsByProvider[providerID], modelID)
		if _, ok := result.ReasoningByKey[key]; !ok {
			result.ReasoningByKey[key] = true
		}
	}

	for providerID, models := range result.ModelsByProvider {
		uniqueModels := dedupeModelValues(models)
		sort.SliceStable(uniqueModels, func(i, j int) bool {
			return strings.ToLower(strings.TrimSpace(uniqueModels[i])) < strings.ToLower(strings.TrimSpace(uniqueModels[j]))
		})
		result.ModelsByProvider[providerID] = uniqueModels
	}
	result.Warnings = uniqueNonEmpty(result.Warnings)
	return result
}

func dedupeModelValues(models []string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	return out
}
