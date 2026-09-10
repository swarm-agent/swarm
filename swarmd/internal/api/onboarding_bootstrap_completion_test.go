package api

import (
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

// Requirement: initial identity creation must preserve the provider/workspace
// wizard across subsequent reads and restart. updateOnboarding owns persistence;
// a real fresh identity store catches legacy unset-flag dismissal after bootstrap.
func TestOnboardingFreshBootstrapKeepsWizardRequired(t *testing.T) {
	s := newLocalAuthTestServer(t)
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "fresh.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s.SetUISettingsService(uisettings.NewService(pebblestore.NewUISettingsStore(store)))
	identities := pebblestore.NewIdentityStore(store)
	s.SetIdentityService(identity.NewService(identities))
	s.SetIdentitySessionService(identity.NewSessionService(identities, pebblestore.NewIdentitySessionStore(store)))
	name := "fresh-test"
	response, _, err := s.updateOnboarding(onboardingUpdateRequest{Username: &name, SwarmName: &name}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !response.NeedsOnboarding {
		t.Fatal("identity submission dismissed provider/workspace onboarding")
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DesktopOnboardingCompleteSet || cfg.DesktopOnboardingComplete {
		t.Fatal("incomplete onboarding was not persisted")
	}
	if !shouldShowOnboarding(cfg, true) {
		t.Fatal("reloaded config dismisses onboarding")
	}
	complete := true
	if _, _, err := s.updateOnboarding(onboardingUpdateRequest{DesktopOnboardingComplete: &complete}, false); err != nil {
		t.Fatal(err)
	}
	cfg, err = s.loadStartupConfig()
	if err != nil {
		t.Fatal(err)
	}
	if shouldShowOnboarding(cfg, true) {
		t.Fatal("explicit completion did not dismiss onboarding")
	}
}
