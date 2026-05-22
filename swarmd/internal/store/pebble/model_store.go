package pebblestore

import "time"

const (
	DefaultModelProvider = ""
	DefaultModelName     = ""
	DefaultThinkingLevel = "xhigh"
)

type ModelPreference struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Thinking       string `json:"thinking"`
	ServiceTier    string `json:"service_tier,omitempty"`
	ContextMode    string `json:"context_mode,omitempty"`
	AccountScopeID string `json:"account_scope_id,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	UpdatedAt      int64  `json:"updated_at"`
}

type ModelStore struct {
	store *Store
}

func NewModelStore(store *Store) *ModelStore {
	return &ModelStore{store: store}
}

func (s *ModelStore) SetGlobalPreference(provider, model, thinking string, codexRuntime ...string) (ModelPreference, error) {
	return s.SetPreferenceForAccount("", "", provider, model, thinking, codexRuntime...)
}

func (s *ModelStore) SetPreferenceForAccount(accountScopeID, userID, provider, model, thinking string, codexRuntime ...string) (ModelPreference, error) {
	serviceTier := ""
	contextMode := ""
	if len(codexRuntime) > 0 {
		serviceTier = codexRuntime[0]
	}
	if len(codexRuntime) > 1 {
		contextMode = codexRuntime[1]
	}
	pref := ModelPreference{
		Provider:       provider,
		Model:          model,
		Thinking:       thinking,
		ServiceTier:    serviceTier,
		ContextMode:    contextMode,
		AccountScopeID: accountScopeID,
		UserID:         userID,
		UpdatedAt:      time.Now().UnixMilli(),
	}
	key := KeyModelPrefGlobal
	if accountScopeID != "" {
		key = KeyModelPreferenceForAccount(accountScopeID)
	}
	if err := s.store.PutJSON(key, pref); err != nil {
		return ModelPreference{}, err
	}
	return pref, nil
}

func (s *ModelStore) GetGlobalPreference() (ModelPreference, bool, error) {
	return s.GetPreferenceForAccount("")
}

func (s *ModelStore) GetPreferenceForAccount(accountScopeID string) (ModelPreference, bool, error) {
	var pref ModelPreference
	key := KeyModelPrefGlobal
	if accountScopeID != "" {
		key = KeyModelPreferenceForAccount(accountScopeID)
	}
	ok, err := s.store.GetJSON(key, &pref)
	if err != nil {
		return ModelPreference{}, false, err
	}
	if !ok {
		return ModelPreference{
			Provider:       DefaultModelProvider,
			Model:          DefaultModelName,
			Thinking:       DefaultThinkingLevel,
			ServiceTier:    "",
			ContextMode:    "",
			AccountScopeID: accountScopeID,
			UpdatedAt:      0,
		}, false, nil
	}
	if pref.AccountScopeID == "" {
		pref.AccountScopeID = accountScopeID
	}
	return pref, true, nil
}

func (s *ModelStore) ClearGlobalPreference() error {
	return s.ClearPreferenceForAccount("")
}

func (s *ModelStore) ClearPreferenceForAccount(accountScopeID string) error {
	key := KeyModelPrefGlobal
	if accountScopeID != "" {
		key = KeyModelPreferenceForAccount(accountScopeID)
	}
	return s.store.Delete(key)
}
