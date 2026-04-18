package pebblestore

import "time"

const (
	DefaultModelProvider = ""
	DefaultModelName     = ""
	DefaultThinkingLevel = "xhigh"
)

type ModelPreference struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	ServiceTier string `json:"service_tier,omitempty"`
	ContextMode string `json:"context_mode,omitempty"`
	UpdatedAt   int64  `json:"updated_at"`
}

type ModelStore struct {
	store *Store
}

func NewModelStore(store *Store) *ModelStore {
	return &ModelStore{store: store}
}

func (s *ModelStore) SetGlobalPreference(provider, model, thinking string, codexRuntime ...string) (ModelPreference, error) {
	serviceTier := ""
	contextMode := ""
	if len(codexRuntime) > 0 {
		serviceTier = codexRuntime[0]
	}
	if len(codexRuntime) > 1 {
		contextMode = codexRuntime[1]
	}
	pref := ModelPreference{
		Provider:    provider,
		Model:       model,
		Thinking:    thinking,
		ServiceTier: serviceTier,
		ContextMode: contextMode,
		UpdatedAt:   time.Now().UnixMilli(),
	}
	if err := s.store.PutJSON(KeyModelPrefGlobal, pref); err != nil {
		return ModelPreference{}, err
	}
	return pref, nil
}

func (s *ModelStore) GetGlobalPreference() (ModelPreference, bool, error) {
	var pref ModelPreference
	ok, err := s.store.GetJSON(KeyModelPrefGlobal, &pref)
	if err != nil {
		return ModelPreference{}, false, err
	}
	if !ok {
		return ModelPreference{
			Provider:    DefaultModelProvider,
			Model:       DefaultModelName,
			Thinking:    DefaultThinkingLevel,
			ServiceTier: "",
			ContextMode: "",
			UpdatedAt:   0,
		}, false, nil
	}
	return pref, true, nil
}
