package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRoutedSessionMediaBindFailureLeavesNoFalseSuccessOrOrphanedAuthority(t *testing.T) {
	fixture := newRoutedMediaTestFixture(t)
	staged := fixture.stage(t, fixture.principal.AccountScopeID, "atomic-bind-failure")
	restore := fixture.sessions.Store().SetMediaStagingBindCommitHookForTest(func(string) error {
		return errors.New("injected media bind commit failure")
	})

	response := fixture.post(t, fixture.principal.AccountScopeID, "atomic-bind-failure", staged.ID, map[string]string{"modality": "image", "file_type": "png"})
	if response.Code == http.StatusOK || !strings.Contains(response.Body.String(), "injected media bind commit failure") {
		t.Fatalf("bind failure falsely succeeded status=%d body=%s", response.Code, response.Body.String())
	}
	fixture.assertNoRoutedSession(t, "atomic-bind-failure")
	assertRoutedMediaStagingState(t, fixture, fixture.principal.AccountScopeID, staged.ID, pebblestore.MediaStagingStateDeleted)
	if _, ok, err := fixture.sessions.Store().GetV3SessionOperationIdempotencyRecord(fixture.principal.AccountScopeID, stableSessionsV3PrimarySessionID(fixture.principal, "routed:atomic-bind-failure"), pebblestore.V3SessionMutationCreateSession, "atomic-bind-failure"); err != nil || ok {
		t.Fatalf("bind failure left replay authority exists=%t err=%v", ok, err)
	}

	restore()
	replay := fixture.post(t, fixture.principal.AccountScopeID, "atomic-bind-failure", staged.ID, map[string]string{"modality": "image", "file_type": "png"})
	if replay.Code == http.StatusOK {
		t.Fatalf("failed authority was replayed as success status=%d body=%s", replay.Code, replay.Body.String())
	}
	fixture.assertNoRoutedSession(t, "atomic-bind-failure")
}
