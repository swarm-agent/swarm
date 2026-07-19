package client

import (
	"context"
	"errors"
	"strings"
)

// ModelProfileSelection is one effective provider/model selection in a saved
// model profile. Split profiles use Plan and Auto; single profiles use Single.
type ModelProfileSelection struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	ServiceTier string `json:"service_tier,omitempty"`
	ContextMode string `json:"context_mode,omitempty"`
}

// ModelProfile is an account-owned saved model profile returned by swarmd.
type ModelProfile struct {
	ProfileID string                 `json:"profile_id"`
	Name      string                 `json:"name"`
	ModelMode string                 `json:"model_mode"`
	Single    *ModelProfileSelection `json:"single,omitempty"`
	Plan      *ModelProfileSelection `json:"plan,omitempty"`
	Auto      *ModelProfileSelection `json:"auto,omitempty"`
	CreatedAt int64                  `json:"created_at"`
	UpdatedAt int64                  `json:"updated_at"`
	IsDefault bool                   `json:"is_default"`
}

// ModelProfileState is the daemon-owned saved profile collection and its
// account default. Clients project this state; they do not maintain a second
// local profile store.
type ModelProfileState struct {
	Profiles         []ModelProfile `json:"model_profiles"`
	DefaultProfileID string         `json:"default_profile_id"`
}

func (c *API) ListModelProfiles(ctx context.Context) (ModelProfileState, error) {
	var state ModelProfileState
	if err := c.getJSON(ctx, "/v1/model-profiles", &state, true); err != nil {
		return ModelProfileState{}, err
	}
	for i := range state.Profiles {
		state.Profiles[i].IsDefault = state.Profiles[i].IsDefault || state.Profiles[i].ProfileID == state.DefaultProfileID
	}
	return state, nil
}

// SetDefaultModelProfile selects the saved account profile used for new
// sessions. The daemon remains the authority for both the profile and default.
func (c *API) SetDefaultModelProfile(ctx context.Context, profileID string) (ModelProfile, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return ModelProfile{}, errors.New("profile id is required")
	}
	var response struct {
		Profile ModelProfile `json:"model_profile"`
	}
	if err := c.postJSON(ctx, "/v1/model-profiles/default", map[string]string{"profile_id": profileID}, &response, true); err != nil {
		return ModelProfile{}, err
	}
	response.Profile.IsDefault = true
	return response.Profile, nil
}
