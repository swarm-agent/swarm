package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type SessionUsageDashboardResponse struct {
	OK             bool                         `json:"ok"`
	Summary        SessionUsageDashboardSummary `json:"summary"`
	Daily          []SessionUsageDailyItem      `json:"daily"`
	ByProvider     []SessionUsageProviderItem   `json:"by_provider"`
	ByModel        []SessionUsageModelItem      `json:"by_model"`
	Media          SessionUsageMediaSummary     `json:"media"`
	RecentSessions []SessionUsageSessionItem    `json:"recent_sessions"`
	Limits         SessionUsageLimitsStatus     `json:"limits"`
	Meta           SessionUsageDashboardMeta    `json:"meta"`
}

type SessionUsageDashboardSummary struct {
	TotalTokens         int64   `json:"total_tokens"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CachedTokens        int64   `json:"cached_tokens"`
	ThinkingTokens      int64   `json:"thinking_tokens"`
	TotalCostUSD        float64 `json:"total_cost_usd"`
	CodexNominalCostUSD float64 `json:"codex_nominal_cost_usd"`
	TotalTurns          int     `json:"total_turns"`
	TotalSessions       int     `json:"total_sessions"`
	ActiveSessions      int     `json:"active_sessions"`
	ArchivedSessions    int     `json:"archived_sessions"`
	TotalMediaCalls     int     `json:"total_media_calls"`
	MediaCostUSD        float64 `json:"media_cost_usd"`
}

type SessionUsageDailyItem struct {
	Date                string           `json:"date"`
	Timestamp           int64            `json:"timestamp"`
	TotalTokens         int64            `json:"total_tokens"`
	InputTokens         int64            `json:"input_tokens"`
	OutputTokens        int64            `json:"output_tokens"`
	CachedTokens        int64            `json:"cached_tokens"`
	ThinkingTokens      int64            `json:"thinking_tokens"`
	CostUSD             float64          `json:"cost_usd"`
	CodexNominalCostUSD float64          `json:"codex_nominal_cost_usd"`
	Turns               int              `json:"turns"`
	MediaCalls          int              `json:"media_calls"`
	MediaCostUSD        float64          `json:"media_cost_usd"`
	ModelsUsed          map[string]int64 `json:"models_used"`
}

type SessionUsageProviderItem struct {
	Provider            string   `json:"provider"`
	DisplayName         string   `json:"display_name"`
	TotalTokens         int64    `json:"total_tokens"`
	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	CachedTokens        int64    `json:"cached_tokens"`
	ThinkingTokens      int64    `json:"thinking_tokens"`
	CostUSD             float64  `json:"cost_usd"`
	CodexNominalCostUSD float64  `json:"codex_nominal_cost_usd"`
	IsSubscription      bool     `json:"is_subscription"`
	Turns               int      `json:"turns"`
	Sessions            int      `json:"sessions"`
	Models              []string `json:"models"`
}

type SessionUsageModelItem struct {
	Model                 string  `json:"model"`
	Provider              string  `json:"provider"`
	DisplayName           string  `json:"display_name"`
	TotalTokens           int64   `json:"total_tokens"`
	InputTokens           int64   `json:"input_tokens"`
	OutputTokens          int64   `json:"output_tokens"`
	CachedTokens          int64   `json:"cached_tokens"`
	ThinkingTokens        int64   `json:"thinking_tokens"`
	CostUSD               float64 `json:"cost_usd"`
	CodexNominalCostUSD   float64 `json:"codex_nominal_cost_usd"`
	Turns                 int     `json:"turns"`
	Sessions              int     `json:"sessions"`
	InputPricePerMillion  float64 `json:"input_price_per_million"`
	OutputPricePerMillion float64 `json:"output_price_per_million"`
	CachedPricePerMillion float64 `json:"cached_price_per_million"`
}

type SessionUsageMediaSummary struct {
	TotalCount   int                     `json:"total_count"`
	TotalCostUSD float64                 `json:"total_cost_usd"`
	ImageCount   int                     `json:"image_count"`
	ImageCostUSD float64                 `json:"image_cost_usd"`
	VideoCount   int                     `json:"video_count"`
	VideoCostUSD float64                 `json:"video_cost_usd"`
	AudioCount   int                     `json:"audio_count"`
	AudioCostUSD float64                 `json:"audio_cost_usd"`
	RecentItems  []SessionUsageMediaItem `json:"recent_items"`
}

type SessionUsageMediaItem struct {
	ID        string  `json:"id"`
	SessionID string  `json:"session_id"`
	MediaType string  `json:"media_type"`
	Kind      string  `json:"kind"`
	Filename  string  `json:"filename"`
	Label     string  `json:"label"`
	Size      int64   `json:"size"`
	CostUSD   float64 `json:"cost_usd"`
	CreatedAt int64   `json:"created_at"`
}

type SessionUsageSessionItem struct {
	SessionID      string  `json:"session_id"`
	Title          string  `json:"title"`
	Provider       string  `json:"provider"`
	Model          string  `json:"model"`
	TotalTokens    int64   `json:"total_tokens"`
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	CachedTokens   int64   `json:"cached_tokens"`
	ThinkingTokens int64   `json:"thinking_tokens"`
	CostUSD        float64 `json:"cost_usd"`
	TurnCount      int     `json:"turn_count"`
	LastActiveAt   int64   `json:"last_active_at"`
	Archived       bool    `json:"archived"`
}

type SessionUsageDashboardMeta struct {
	GeneratedAt          int64  `json:"generated_at"`
	TimeRange            string `json:"time_range"`
	TotalRecordsAnalyzed int    `json:"total_records_analyzed"`
}

type catalogPricingLookup struct {
	InputPrice  float64
	OutputPrice float64
	CachedPrice float64
	HasCached   bool
	IsFree      bool
	DisplayName string
}

func (s *Server) handleSessionsV3Usage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}

	query := r.URL.Query()
	timeRange := strings.ToLower(strings.TrimSpace(query.Get("time_range")))
	if timeRange == "" {
		timeRange = "30d"
	}
	providerFilter := strings.ToLower(strings.TrimSpace(query.Get("provider")))
	modelFilter := strings.ToLower(strings.TrimSpace(query.Get("model")))
	sessionFilter := strings.TrimSpace(query.Get("session_id"))
	archivedMode := strings.ToLower(strings.TrimSpace(query.Get("archived_mode")))
	if archivedMode == "" {
		if a := strings.ToLower(strings.TrimSpace(query.Get("archived"))); a != "" {
			if a == "true" || a == "1" || a == "only" {
				archivedMode = "only"
			} else if a == "false" || a == "0" {
				archivedMode = "exclude"
			}
		}
	}
	switch archivedMode {
	case "active", "exclude":
		archivedMode = "exclude"
	case "archived", "only":
		archivedMode = "only"
	default:
		archivedMode = "include"
	}

	sessionLimit := 50
	if rawSessionLimit := strings.TrimSpace(query.Get("session_limit")); rawSessionLimit != "" {
		if parsed, err := strconv.Atoi(rawSessionLimit); err == nil && parsed > 0 {
			sessionLimit = parsed
		}
	}

	now := time.Now().UTC()
	var cutoffTime int64
	switch timeRange {
	case "today":
		startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		cutoffTime = startOfDay.UnixMilli()
	case "7d":
		cutoffTime = now.AddDate(0, 0, -7).UnixMilli()
	case "30d":
		cutoffTime = now.AddDate(0, 0, -30).UnixMilli()
	case "90d":
		cutoffTime = now.AddDate(0, 0, -90).UnixMilli()
	case "all":
		cutoffTime = 0
	default:
		cutoffTime = now.AddDate(0, 0, -30).UnixMilli()
	}

	if customStart := strings.TrimSpace(query.Get("start_date")); customStart != "" {
		if t, err := time.Parse("2006-01-02", customStart); err == nil {
			cutoffTime = t.UTC().UnixMilli()
		}
	}
	var endTimeCutoff int64
	if customEnd := strings.TrimSpace(query.Get("end_date")); customEnd != "" {
		if t, err := time.Parse("2006-01-02", customEnd); err == nil {
			endTimeCutoff = t.UTC().Add(24*time.Hour - time.Millisecond).UnixMilli()
		}
	}

	limit := 10000
	if rawLimit := strings.TrimSpace(query.Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	// 1. Fetch raw records from Pebble
	turnRecords, err := s.sessions.ListAllTurnUsage(principal.AccountScopeID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("list turn usage: %w", err))
		return
	}

	mediaVariants, _ := s.sessions.ListAllMediaArtifactVariants(principal.AccountScopeID, 2000)

	// Session metadata resolver with point-lookup caching: resolves title and archived
	// state on-demand for active/participating sessions without performing a full-table
	// account scan across all sessions and lifecycles in Pebble.
	type sessionMeta struct {
		title    string
		archived bool
	}
	sessionMetaCache := make(map[string]sessionMeta)
	resolveSessionMeta := func(sessionID string) sessionMeta {
		if meta, ok := sessionMetaCache[sessionID]; ok {
			return meta
		}
		var meta sessionMeta
		store := s.sessions.Store()
		if store != nil {
			if sess, ok, err := store.GetSession(sessionID); err == nil && ok {
				meta.title = strings.TrimSpace(sess.Title)
				meta.archived = false
			} else if tombstone, ok, err := store.GetV3SessionTombstone(sessionID); err == nil && ok && tombstone.Archived && !tombstone.Deleted {
				meta.title = strings.TrimSpace(tombstone.Session.Title)
				meta.archived = true
			}
		}
		if meta.title == "" {
			if len(sessionID) > 8 {
				meta.title = "Session " + sessionID[:8]
			} else {
				meta.title = "Session " + sessionID
			}
		}
		sessionMetaCache[sessionID] = meta
		return meta
	}

	// 2. Build model pricing lookup from catalog
	pricingMap := s.buildPricingMap()

	// 3. Process and aggregate turn usage
	var summary SessionUsageDashboardSummary
	dailyMap := make(map[string]*SessionUsageDailyItem)
	providerMap := make(map[string]*SessionUsageProviderItem)
	modelMap := make(map[string]*SessionUsageModelItem)
	sessionUsageMap := make(map[string]*SessionUsageSessionItem)
	uniqueSessions := make(map[string]struct{})

	for _, rec := range turnRecords {
		ts := rec.CreatedAt
		if ts <= 0 {
			ts = rec.UpdatedAt
		}
		if ts <= 0 {
			continue
		}
		if cutoffTime > 0 && ts < cutoffTime {
			continue
		}
		if endTimeCutoff > 0 && ts > endTimeCutoff {
			continue
		}

		provID := strings.ToLower(strings.TrimSpace(rec.Provider))
		modelID := strings.TrimSpace(rec.Model)
		if providerFilter != "" && provID != providerFilter {
			continue
		}
		if modelFilter != "" && !strings.Contains(strings.ToLower(modelID), modelFilter) {
			continue
		}
		if sessionFilter != "" && rec.SessionID != sessionFilter {
			continue
		}

		costUSD, codexNominalUSD := calculateTurnCost(rec, pricingMap)

		summary.TotalTokens += rec.TotalTokens
		summary.InputTokens += rec.InputTokens
		summary.OutputTokens += rec.OutputTokens
		summary.CachedTokens += rec.CacheReadTokens
		summary.ThinkingTokens += rec.ThinkingTokens
		summary.TotalCostUSD += costUSD
		summary.CodexNominalCostUSD += codexNominalUSD
		summary.TotalTurns++
		uniqueSessions[rec.SessionID] = struct{}{}

		// Daily bin
		dayKey := time.UnixMilli(ts).UTC().Format("2006-01-02")
		dayItem, exists := dailyMap[dayKey]
		if !exists {
			dayStart := time.Date(time.UnixMilli(ts).UTC().Year(), time.UnixMilli(ts).UTC().Month(), time.UnixMilli(ts).UTC().Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
			dayItem = &SessionUsageDailyItem{
				Date:       dayKey,
				Timestamp:  dayStart,
				ModelsUsed: make(map[string]int64),
			}
			dailyMap[dayKey] = dayItem
		}
		dayItem.TotalTokens += rec.TotalTokens
		dayItem.InputTokens += rec.InputTokens
		dayItem.OutputTokens += rec.OutputTokens
		dayItem.CachedTokens += rec.CacheReadTokens
		dayItem.ThinkingTokens += rec.ThinkingTokens
		dayItem.CostUSD += costUSD
		dayItem.CodexNominalCostUSD += codexNominalUSD
		dayItem.Turns++
		dayItem.ModelsUsed[modelID] += rec.TotalTokens

		// Provider bin
		provItem, exists := providerMap[provID]
		if !exists {
			provItem = &SessionUsageProviderItem{
				Provider:       provID,
				DisplayName:    formatProviderDisplayName(provID),
				IsSubscription: provID == "codex",
				Models:         []string{},
			}
			providerMap[provID] = provItem
		}
		provItem.TotalTokens += rec.TotalTokens
		provItem.InputTokens += rec.InputTokens
		provItem.OutputTokens += rec.OutputTokens
		provItem.CachedTokens += rec.CacheReadTokens
		provItem.ThinkingTokens += rec.ThinkingTokens
		provItem.CostUSD += costUSD
		provItem.CodexNominalCostUSD += codexNominalUSD
		provItem.Turns++
		if !containsUsageString(provItem.Models, modelID) {
			provItem.Models = append(provItem.Models, modelID)
		}

		// Model bin
		modelKey := provID + ":" + modelID
		mItem, exists := modelMap[modelKey]
		if !exists {
			pInfo := pricingMap[modelKey]
			if pInfo.DisplayName == "" {
				pInfo = pricingMap[modelID]
			}
			mItem = &SessionUsageModelItem{
				Model:                 modelID,
				Provider:              provID,
				DisplayName:           pInfo.DisplayName,
				InputPricePerMillion:  pInfo.InputPrice,
				OutputPricePerMillion: pInfo.OutputPrice,
				CachedPricePerMillion: pInfo.CachedPrice,
			}
			if mItem.DisplayName == "" {
				mItem.DisplayName = modelID
			}
			modelMap[modelKey] = mItem
		}
		mItem.TotalTokens += rec.TotalTokens
		mItem.InputTokens += rec.InputTokens
		mItem.OutputTokens += rec.OutputTokens
		mItem.CachedTokens += rec.CacheReadTokens
		mItem.ThinkingTokens += rec.ThinkingTokens
		mItem.CostUSD += costUSD
		mItem.CodexNominalCostUSD += codexNominalUSD
		mItem.Turns++

		// Session usage
		sessItem, exists := sessionUsageMap[rec.SessionID]
		if !exists {
			sessItem = &SessionUsageSessionItem{
				SessionID:    rec.SessionID,
				Provider:     provID,
				Model:        modelID,
				LastActiveAt: ts,
			}
			sessionUsageMap[rec.SessionID] = sessItem
		}
		sessItem.TotalTokens += rec.TotalTokens
		sessItem.InputTokens += rec.InputTokens
		sessItem.OutputTokens += rec.OutputTokens
		sessItem.CachedTokens += rec.CacheReadTokens
		sessItem.ThinkingTokens += rec.ThinkingTokens
		sessItem.CostUSD += costUSD
		sessItem.TurnCount++
		if ts > sessItem.LastActiveAt {
			sessItem.LastActiveAt = ts
			sessItem.Provider = provID
			sessItem.Model = modelID
		}
	}
	// Calculate session counts from fast library summary index if available,
	// ensuring all active and archived sessions in the account are counted accurately.
	searchLimit := sessionLimit
	if searchLimit < 100 {
		searchLimit = 100
	}
	searchOpts := pebblestore.V3SessionSearchOptions{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		Global:         true,
		ArchivedMode:   archivedMode,
		Limit:          searchLimit,
	}
	searchResult, searchErr := s.sessions.SearchSessions(searchOpts)
	if searchErr == nil && (searchResult.Summary.ActiveConversationCount > 0 || searchResult.Summary.ArchivedConversationCount > 0) {
		summary.ActiveSessions = searchResult.Summary.ActiveConversationCount
		summary.ArchivedSessions = searchResult.Summary.ArchivedConversationCount
		summary.TotalSessions = summary.ActiveSessions + summary.ArchivedSessions
	} else {
		summary.TotalSessions = len(uniqueSessions)
		for sid := range uniqueSessions {
			meta := resolveSessionMeta(sid)
			if meta.archived {
				summary.ArchivedSessions++
			} else {
				summary.ActiveSessions++
			}
		}
	}

	// 4. Process media generation calls
	var mediaSummary SessionUsageMediaSummary
	for _, v := range mediaVariants {
		ts := v.CreatedAt
		if ts <= 0 {
			continue
		}
		if cutoffTime > 0 && ts < cutoffTime {
			continue
		}
		if endTimeCutoff > 0 && ts > endTimeCutoff {
			continue
		}
		if sessionFilter != "" && v.SessionID != sessionFilter {
			continue
		}

		if !pebblestore.IsAIGeneratedMedia(v) {
			continue
		}

		mt := strings.ToLower(v.MediaType)
		var kind string
		if strings.HasPrefix(mt, "image/") {
			kind = "image"
		} else if strings.HasPrefix(mt, "video/") {
			kind = "video"
		} else if strings.HasPrefix(mt, "audio/") {
			kind = "audio"
		} else {
			continue
		}

		var cost float64
		if v.EstimatedCostUSD > 0 {
			cost = v.EstimatedCostUSD
		} else {
			res := ""
			if v.OutputRequirements != nil {
				res = v.OutputRequirements.PresetID
				if res == "" && v.OutputRequirements.Width > 0 {
					if v.OutputRequirements.Width >= 3840 {
						res = "4k"
					} else if v.OutputRequirements.Width >= 1920 {
						res = "1080p"
					} else {
						res = "720p"
					}
				}
			}
			if res == "" && v.Presentation.Width > 0 {
				if v.Presentation.Width >= 3840 {
					res = "4k"
				} else if v.Presentation.Width >= 1920 {
					res = "1080p"
				} else {
					res = "720p"
				}
			}
			if res == "" {
				descLower := strings.ToLower(v.Presentation.Description)
				labelLower := strings.ToLower(v.Presentation.Label)
				if strings.Contains(descLower, "8k") || strings.Contains(descLower, "4k") || strings.Contains(labelLower, "4k") || v.Size > 32<<20 {
					res = "4k"
				}
			}
			cost = pebblestore.CalculateBaselineMediaCost(v.MediaType, v.ModelID, res, 8)
		}

		if kind == "image" {
			mediaSummary.ImageCount++
			mediaSummary.ImageCostUSD += cost
		} else if kind == "video" {
			mediaSummary.VideoCount++
			mediaSummary.VideoCostUSD += cost
		} else if kind == "audio" {
			mediaSummary.AudioCount++
			mediaSummary.AudioCostUSD += cost
		}

		mediaSummary.TotalCount++
		mediaSummary.TotalCostUSD += cost

		label := strings.TrimSpace(v.Presentation.Label)
		if label == "" {
			label = v.Filename
		}
		if label == "" {
			label = fmt.Sprintf("%s generation", kind)
		}

		if len(mediaSummary.RecentItems) < 30 {
			mediaSummary.RecentItems = append(mediaSummary.RecentItems, SessionUsageMediaItem{
				ID:        v.ID,
				SessionID: v.SessionID,
				MediaType: v.MediaType,
				Kind:      kind,
				Filename:  v.Filename,
				Label:     label,
				Size:      v.Size,
				CostUSD:   cost,
				CreatedAt: ts,
			})
		}

		// Also attach media count and cost to daily bin
		dayKey := time.UnixMilli(ts).UTC().Format("2006-01-02")
		dayItem, exists := dailyMap[dayKey]
		if exists {
			dayItem.MediaCalls++
			dayItem.MediaCostUSD += cost
			dayItem.CostUSD += cost
		} else {
			dayStart := time.Date(time.UnixMilli(ts).UTC().Year(), time.UnixMilli(ts).UTC().Month(), time.UnixMilli(ts).UTC().Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
			dailyMap[dayKey] = &SessionUsageDailyItem{
				Date:         dayKey,
				Timestamp:    dayStart,
				MediaCalls:   1,
				MediaCostUSD: cost,
				CostUSD:      cost,
				ModelsUsed:   make(map[string]int64),
			}
			dayItem = dailyMap[dayKey]
		}
		if v.ModelID != "" {
			dayItem.ModelsUsed[v.ModelID]++
		}

		// Also attach media cost to session item
		sessItem, exists := sessionUsageMap[v.SessionID]
		if exists {
			sessItem.CostUSD += cost
		} else {
			meta := resolveSessionMeta(v.SessionID)
			sessItem = &SessionUsageSessionItem{
				SessionID:    v.SessionID,
				Title:        meta.title,
				Archived:     meta.archived,
				Provider:     v.ProviderID,
				Model:        v.ModelID,
				LastActiveAt: ts,
				CostUSD:      cost,
			}
			sessionUsageMap[v.SessionID] = sessItem
		}
	}

	summary.TotalMediaCalls = mediaSummary.TotalCount
	summary.MediaCostUSD = mediaSummary.TotalCostUSD
	summary.TotalCostUSD += mediaSummary.TotalCostUSD

	// 5. Convert maps to sorted slices
	dailyList := make([]SessionUsageDailyItem, 0, len(dailyMap))
	for _, item := range dailyMap {
		dailyList = append(dailyList, *item)
	}
	sort.Slice(dailyList, func(i, j int) bool {
		return dailyList[i].Date < dailyList[j].Date // chronological ascending
	})

	providerList := make([]SessionUsageProviderItem, 0, len(providerMap))
	for _, item := range providerMap {
		providerList = append(providerList, *item)
	}
	sort.Slice(providerList, func(i, j int) bool {
		return providerList[i].TotalTokens > providerList[j].TotalTokens
	})

	modelList := make([]SessionUsageModelItem, 0, len(modelMap))
	for _, item := range modelMap {
		modelList = append(modelList, *item)
	}
	sort.Slice(modelList, func(i, j int) bool {
		return modelList[i].TotalTokens > modelList[j].TotalTokens
	})

	seenSessionIDs := make(map[string]struct{})
	sessionList := make([]SessionUsageSessionItem, 0, len(searchResult.Items)+len(sessionUsageMap))

	// 1. Populate from fast indexed search results (guarantees active sessions in view
	// and archived sessions in library are retrieved with O(1) usage lookup).
	if searchErr == nil {
		for _, sItem := range searchResult.Items {
			seenSessionIDs[sItem.ID] = struct{}{}
			if usageItem, exists := sessionUsageMap[sItem.ID]; exists {
				meta := resolveSessionMeta(sItem.ID)
				usageItem.Title = meta.title
				usageItem.Archived = meta.archived
				sessionList = append(sessionList, *usageItem)
			} else {
				// Session had no turns in this specific time window; pull canonical lifetime usage summary
				item := SessionUsageSessionItem{
					SessionID:    sItem.ID,
					Title:        sItem.Title,
					Archived:     sItem.Archived,
					LastActiveAt: sItem.UpdatedAt,
				}
				if store := s.sessions.Store(); store != nil {
					if lSummary, hasSummary, _ := store.GetUsageSummary(sItem.ID); hasSummary && lSummary.TotalTokens > 0 {
						item.TotalTokens = lSummary.TotalTokens
						item.InputTokens = lSummary.InputTokens
						item.OutputTokens = lSummary.OutputTokens
						item.CachedTokens = lSummary.CacheReadTokens
						item.ThinkingTokens = lSummary.ThinkingTokens
						item.TurnCount = lSummary.TurnCount
						item.Provider = lSummary.Provider
						item.Model = lSummary.Model
						item.CostUSD = lSummary.EstimatedCostUSD
						if item.CostUSD <= 0 {
							pKey := item.Provider + ":" + item.Model
							pInfo := pricingMap[pKey]
							if pInfo.DisplayName == "" {
								pInfo = pricingMap[item.Model]
							}
							if pInfo.InputPrice > 0 || pInfo.OutputPrice > 0 {
								item.CostUSD = (float64(item.InputTokens)*pInfo.InputPrice + float64(item.OutputTokens)*pInfo.OutputPrice) / 1_000_000.0
							}
						}
						if lSummary.UpdatedAt > item.LastActiveAt {
							item.LastActiveAt = lSummary.UpdatedAt
						}
					}
				}
				if item.Title == "" {
					meta := resolveSessionMeta(sItem.ID)
					item.Title = meta.title
				}
				sessionList = append(sessionList, item)
			}
		}
	}

	// 2. Also append any sessions with turn usage in the time window not caught in top search items
	for sid, item := range sessionUsageMap {
		if _, seen := seenSessionIDs[sid]; seen {
			continue
		}
		meta := resolveSessionMeta(sid)
		item.Title = meta.title
		item.Archived = meta.archived
		if archivedMode == "exclude" && item.Archived {
			continue
		}
		if archivedMode == "only" && !item.Archived {
			continue
		}
		sessionList = append(sessionList, *item)
	}

	// Apply session, provider, model filters to the session list
	if sessionFilter != "" || providerFilter != "" || modelFilter != "" {
		filtered := make([]SessionUsageSessionItem, 0, len(sessionList))
		for _, s := range sessionList {
			if sessionFilter != "" && s.SessionID != sessionFilter {
				continue
			}
			if providerFilter != "" && !strings.EqualFold(s.Provider, providerFilter) {
				continue
			}
			if modelFilter != "" && !strings.Contains(strings.ToLower(s.Model), modelFilter) {
				continue
			}
			filtered = append(filtered, s)
		}
		sessionList = filtered
	}

	sort.Slice(sessionList, func(i, j int) bool {
		hasUsageI := sessionList[i].TotalTokens > 0 || sessionList[i].TurnCount > 0
		hasUsageJ := sessionList[j].TotalTokens > 0 || sessionList[j].TurnCount > 0
		if hasUsageI != hasUsageJ {
			return hasUsageI
		}
		return sessionList[i].LastActiveAt > sessionList[j].LastActiveAt
	})
	if len(sessionList) > sessionLimit {
		sessionList = sessionList[:sessionLimit]
	}

	// Calculate session counts per provider
	provSessionSet := make(map[string]map[string]struct{})
	for _, item := range sessionList {
		if provSessionSet[item.Provider] == nil {
			provSessionSet[item.Provider] = make(map[string]struct{})
		}
		provSessionSet[item.Provider][item.SessionID] = struct{}{}
	}
	for i := range providerList {
		providerList[i].Sessions = len(provSessionSet[providerList[i].Provider])
	}

	limitRec, _, _ := s.sessions.GetUsageLimit(principal.AccountScopeID)
	todayCost, todayTokens, _ := s.sessions.GetTodayUsageTotal(principal.AccountScopeID)
	limitExceeded := limitRec.Enabled && limitRec.DailyCostLimitUSD > 0 && todayCost >= limitRec.DailyCostLimitUSD

	writeJSON(w, http.StatusOK, SessionUsageDashboardResponse{
		OK:             true,
		Summary:        summary,
		Daily:          dailyList,
		ByProvider:     providerList,
		ByModel:        modelList,
		Media:          mediaSummary,
		RecentSessions: sessionList,
		Limits: SessionUsageLimitsStatus{
			AccountScopeID:    principal.AccountScopeID,
			DailyCostLimitUSD: limitRec.DailyCostLimitUSD,
			DailyTokensLimit:  limitRec.DailyTokensLimit,
			Enabled:           limitRec.Enabled,
			TodayCostUSD:      todayCost,
			TodayTokens:       todayTokens,
			LimitExceeded:     limitExceeded,
			UpdatedAt:         limitRec.UpdatedAt,
		},
		Meta: SessionUsageDashboardMeta{
			GeneratedAt:          now.UnixMilli(),
			TimeRange:            timeRange,
			TotalRecordsAnalyzed: len(turnRecords),
		},
	})
}

func (s *Server) buildPricingMap() map[string]catalogPricingLookup {
	out := make(map[string]catalogPricingLookup)

	// Baseline fallback pricing for key flagship models in case catalog is unavailable
	baselinePricing := map[string]catalogPricingLookup{
		"google:gemini-3.8-flash":       {InputPrice: 0.75, OutputPrice: 3.75, CachedPrice: 0.075, HasCached: true, DisplayName: "Gemini 3.8 Flash"},
		"google:gemini-3.7-flash":       {InputPrice: 0.75, OutputPrice: 3.75, CachedPrice: 0.075, HasCached: true, DisplayName: "Gemini 3.7 Flash"},
		"google:gemini-3.6-flash":       {InputPrice: 1.50, OutputPrice: 7.50, CachedPrice: 0.15, HasCached: true, DisplayName: "Gemini 3.6 Flash"},
		"google:gemini-3.5-flash-lite":  {InputPrice: 0.30, OutputPrice: 2.50, CachedPrice: 0.03, HasCached: true, DisplayName: "Gemini 3.5 Flash-Lite"},
		"google:gemini-3.5-flash":       {InputPrice: 0.30, OutputPrice: 2.50, CachedPrice: 0.03, HasCached: true, DisplayName: "Gemini 3.5 Flash"},
		"google:gemini-3.1-pro-preview": {InputPrice: 2.00, OutputPrice: 12.0, CachedPrice: 0.20, HasCached: true, DisplayName: "Gemini 3.1 Pro Preview"},
		"google:gemini-2.5-flash":       {InputPrice: 0.30, OutputPrice: 2.50, CachedPrice: 0.03, HasCached: true, DisplayName: "Gemini 2.5 Flash"},
		"google:gemini-2.5-pro":         {InputPrice: 1.25, OutputPrice: 10.0, CachedPrice: 0.125, HasCached: true, DisplayName: "Gemini 2.5 Pro"},
		"google:gemini-omni-1.1-flash":  {InputPrice: 1.50, OutputPrice: 9.00, CachedPrice: 0.375, HasCached: true, DisplayName: "Gemini Omni 1.1 Flash"},
		"anthropic:claude-fable-5-1":    {InputPrice: 10.0, OutputPrice: 50.0, CachedPrice: 1.0, HasCached: true, DisplayName: "Claude Fable 5.1"},
		"anthropic:claude-sonnet-5":     {InputPrice: 2.0, OutputPrice: 10.0, CachedPrice: 0.2, HasCached: true, DisplayName: "Claude Sonnet 5"},
		"anthropic:claude-opus-5":       {InputPrice: 5.0, OutputPrice: 25.0, CachedPrice: 0.5, HasCached: true, DisplayName: "Claude Opus 5"},
		"openai:gpt-6-astra":            {InputPrice: 10.0, OutputPrice: 50.0, CachedPrice: 5.0, HasCached: true, DisplayName: "GPT-6 Astra"},
		"openai:gpt-5.6-sol":            {InputPrice: 5.0, OutputPrice: 30.0, CachedPrice: 2.5, HasCached: true, DisplayName: "GPT-5.6 Sol"},
		"openai:gpt-5.6-luna":           {InputPrice: 1.0, OutputPrice: 6.0, CachedPrice: 0.5, HasCached: true, DisplayName: "GPT-5.6 Luna"},
		"openai:gpt-5.6-terra":          {InputPrice: 2.5, OutputPrice: 15.0, CachedPrice: 1.25, HasCached: true, DisplayName: "GPT-5.6 Terra"},
		"openai:gpt-5.5":                {InputPrice: 5.0, OutputPrice: 30.0, CachedPrice: 2.5, HasCached: true, DisplayName: "GPT-5.5"},
		"openai:gpt-5.4":                {InputPrice: 2.5, OutputPrice: 15.0, CachedPrice: 1.25, HasCached: true, DisplayName: "GPT-5.4"},
		"openai:gpt-5.4-mini":           {InputPrice: 0.75, OutputPrice: 4.5, CachedPrice: 0.375, HasCached: true, DisplayName: "GPT-5.4 Mini"},
		"fireworks:deepseek-v4p1-flash": {InputPrice: 0.22, OutputPrice: 0.66, CachedPrice: 0.11, HasCached: true, DisplayName: "DeepSeek V4.1 Flash"},
		"fireworks:glm-5p3-flash":       {InputPrice: 0.15, OutputPrice: 0.50, CachedPrice: 0.075, HasCached: true, DisplayName: "GLM 5.3 Flash"},
	}
	for k, v := range baselinePricing {
		out[k] = v
		// Also populate by bare model ID
		parts := strings.SplitN(k, ":", 2)
		if len(parts) == 2 {
			out[parts[1]] = v
		}
	}

	if s.model != nil {
		providers := []string{"anthropic", "codex", "fireworks", "google", "openai", "openrouter", "copilot"}
		for _, provID := range providers {
			records, err := s.model.ListCatalog(provID, 1000)
			if err != nil {
				continue
			}
			for _, rec := range records {
				prov := strings.ToLower(rec.Provider)
				modelID := strings.ToLower(rec.Model)
				var p struct {
					InputPricePerMillion       *float64 `json:"input_price_per_million_tokens"`
					OutputPricePerMillion      *float64 `json:"output_price_per_million_tokens"`
					CachedInputPricePerMillion *float64 `json:"cached_input_price_per_million_tokens"`
					IsFree                     *bool    `json:"is_free"`
				}
				if len(rec.Pricing) > 0 {
					_ = json.Unmarshal(rec.Pricing, &p)
				}

				var inp, outVal, cachedVal float64
				hasCached := false
				isFree := false
				if p.IsFree != nil && *p.IsFree {
					isFree = true
				}
				if p.InputPricePerMillion != nil {
					inp = *p.InputPricePerMillion
				}
				if p.OutputPricePerMillion != nil {
					outVal = *p.OutputPricePerMillion
				}
				if p.CachedInputPricePerMillion != nil {
					cachedVal = *p.CachedInputPricePerMillion
					hasCached = true
				}

				if inp > 0 || outVal > 0 || isFree {
					item := catalogPricingLookup{
						InputPrice:  inp,
						OutputPrice: outVal,
						CachedPrice: cachedVal,
						HasCached:   hasCached,
						IsFree:      isFree,
						DisplayName: rec.DisplayName,
					}
					if item.DisplayName == "" {
						item.DisplayName = rec.Model
					}

					out[prov+":"+modelID] = item
					out[modelID] = item
				}
			}
		}
	}

	// Codex nominal OpenAI rates
	codexOpenAIEquivalents := map[string]catalogPricingLookup{
		"gpt-6-astra":         {InputPrice: 10.0, OutputPrice: 50.0, CachedPrice: 5.0, HasCached: true, DisplayName: "GPT-6 Astra"},
		"gpt-5.6-sol":         {InputPrice: 5.0, OutputPrice: 30.0, CachedPrice: 2.5, HasCached: true, DisplayName: "GPT-5.6 Sol"},
		"gpt-5.6-luna":        {InputPrice: 1.0, OutputPrice: 6.0, CachedPrice: 0.5, HasCached: true, DisplayName: "GPT-5.6 Luna"},
		"gpt-5.6-terra":       {InputPrice: 2.5, OutputPrice: 15.0, CachedPrice: 1.25, HasCached: true, DisplayName: "GPT-5.6 Terra"},
		"gpt-5.5":             {InputPrice: 5.0, OutputPrice: 30.0, CachedPrice: 2.5, HasCached: true, DisplayName: "GPT-5.5"},
		"gpt-5.4":             {InputPrice: 2.5, OutputPrice: 15.0, CachedPrice: 1.25, HasCached: true, DisplayName: "GPT-5.4"},
		"gpt-5.4-mini":        {InputPrice: 0.75, OutputPrice: 4.5, CachedPrice: 0.375, HasCached: true, DisplayName: "GPT-5.4 Mini"},
		"gpt-5.3-codex-spark": {InputPrice: 1.25, OutputPrice: 5.0, CachedPrice: 0.625, HasCached: true, DisplayName: "GPT-5.3 Codex Spark"},
	}
	for k, v := range codexOpenAIEquivalents {
		out["codex_nominal:"+k] = v
	}

	return out
}

func calculateTurnCost(rec pebblestore.SessionTurnUsageSnapshot, pricing map[string]catalogPricingLookup) (costUSD float64, codexNominalCostUSD float64) {
	prov := strings.ToLower(strings.TrimSpace(rec.Provider))
	model := strings.ToLower(strings.TrimSpace(rec.Model))

	// For Codex (OAuth subscription), direct cost is 0, but calculate nominal OpenAI API cost
	if prov == "codex" {
		nominalLookup, ok := pricing["codex_nominal:"+model]
		if !ok {
			nominalLookup = pricing["openai:"+model]
		}
		if nominalLookup.InputPrice > 0 || nominalLookup.OutputPrice > 0 {
			codexNominalCostUSD = computeTokenPrice(rec, nominalLookup, "openai")
		}
		return 0.0, codexNominalCostUSD
	}

	// If the provider already calculated and stored estimated_cost_usd > 0, trust it!
	if rec.EstimatedCostUSD > 0 {
		return rec.EstimatedCostUSD, 0.0
	}

	// Otherwise, calculate via model pricing catalog
	pInfo, ok := pricing[prov+":"+model]
	if !ok {
		pInfo = pricing[model]
	}
	if pInfo.IsFree {
		return 0.0, 0.0
	}
	if pInfo.InputPrice <= 0 && pInfo.OutputPrice <= 0 {
		return 0.0, 0.0
	}

	costUSD = computeTokenPrice(rec, pInfo, prov)
	return costUSD, 0.0
}

func computeTokenPrice(rec pebblestore.SessionTurnUsageSnapshot, p catalogPricingLookup, provider string) float64 {
	inputRate := p.InputPrice
	outputRate := p.OutputPrice
	cachedRate := p.CachedPrice

	if !p.HasCached || cachedRate <= 0 {
		switch provider {
		case "anthropic":
			cachedRate = inputRate * 0.10 // 90% discount for prompt cache read
		case "google":
			cachedRate = inputRate * 0.25 // 75% discount for cached content
		case "openai", "openrouter":
			cachedRate = inputRate * 0.50 // 50% discount for cached tokens
		default:
			cachedRate = inputRate * 0.50
		}
	}

	var uncachedInput int64
	switch provider {
	case "openai", "google":
		// Prompt tokens include cached tokens; subtract cache reads
		if rec.InputTokens >= rec.CacheReadTokens {
			uncachedInput = rec.InputTokens - rec.CacheReadTokens
		} else {
			uncachedInput = 0
		}
	case "anthropic":
		// In Anthropic API, InputTokens is already uncached tokens
		uncachedInput = rec.InputTokens
	default:
		if rec.InputTokens >= rec.CacheReadTokens && rec.CacheReadTokens > 0 {
			uncachedInput = rec.InputTokens - rec.CacheReadTokens
		} else {
			uncachedInput = rec.InputTokens
		}
	}

	inputCost := (float64(uncachedInput) / 1_000_000.0) * inputRate
	cachedCost := (float64(rec.CacheReadTokens) / 1_000_000.0) * cachedRate
	outputCost := (float64(rec.OutputTokens) / 1_000_000.0) * outputRate

	return inputCost + cachedCost + outputCost
}

func formatProviderDisplayName(prov string) string {
	switch strings.ToLower(prov) {
	case "anthropic":
		return "Anthropic"
	case "codex":
		return "OpenAI Codex (Subscription)"
	case "fireworks":
		return "Fireworks AI"
	case "google":
		return "Google Gemini"
	case "openai":
		return "OpenAI (API)"
	case "openrouter":
		return "OpenRouter"
	case "copilot":
		return "GitHub Copilot"
	default:
		if prov == "" {
			return "Unknown"
		}
		return strings.ToUpper(prov[:1]) + prov[1:]
	}
}

func containsUsageString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
