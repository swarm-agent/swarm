package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func newUpdateCommandTestApp() *App {
	return &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}
}

func TestLocalContainerUpdateWarningLinesIncludeContractCountsAndTarget(t *testing.T) {
	plan := client.LocalContainerUpdatePlan{
		DevMode: true,
		Target: client.LocalContainerUpdateTarget{
			Fingerprint:            "old-fingerprint",
			PostRebuildFingerprint: "rebuilt-fingerprint",
		},
		Summary: client.LocalContainerUpdateSummary{
			Total:       4,
			Affected:    3,
			NeedsUpdate: 2,
			Unknown:     1,
			Errors:      0,
		},
		Contract: client.LocalContainerUpdateContract{
			WarningCopy:      "This will also update your local containers.",
			FailureSemantics: "Container update failures are reported as resumable follow-up work.",
		},
	}

	lines := localContainerUpdateWarningLines(plan)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"This will also update your local containers.",
		"local containers: total=4 affected=3 needs_update=2 unknown=1 errors=0",
		"target dev fingerprint: rebuilt-fingerprint",
		"Container update failures are reported as resumable follow-up work.",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warning lines missing %q in:\n%s", want, joined)
		}
	}
}

func TestConfirmLocalContainerUpdateSkipsWhenNoLocalContainersAffected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/update/local-containers" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(client.LocalContainerUpdatePlan{
			Summary: client.LocalContainerUpdateSummary{Total: 0},
		})
	}))
	defer server.Close()

	a := newUpdateCommandTestApp()
	a.api = client.New(server.URL)

	if ok := a.confirmLocalContainerUpdate(false, "v1.2.3"); !ok {
		t.Fatalf("confirmLocalContainerUpdate() = false, want true")
	}
	if a.pendingLocalContainerUpdate != nil {
		t.Fatalf("pendingLocalContainerUpdate = %+v, want nil", a.pendingLocalContainerUpdate)
	}
	if lines := a.home.CommandOverlayLines(); len(lines) != 0 {
		t.Fatalf("overlay lines = %v, want empty", lines)
	}
}

func TestConfirmLocalContainerUpdateRequiresConfirmWhenAffected(t *testing.T) {
	var rawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/update/local-containers" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rawQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(client.LocalContainerUpdatePlan{
			DevMode: true,
			Target:  client.LocalContainerUpdateTarget{PostRebuildFingerprint: "rebuilt-fingerprint"},
			Summary: client.LocalContainerUpdateSummary{
				Total:       2,
				Affected:    1,
				NeedsUpdate: 1,
			},
			Contract: client.LocalContainerUpdateContract{WarningCopy: "This will also update your local containers."},
		})
	}))
	defer server.Close()

	a := newUpdateCommandTestApp()
	a.api = client.New(server.URL)

	if ok := a.confirmLocalContainerUpdate(true, "v1.2.3"); ok {
		t.Fatalf("confirmLocalContainerUpdate() = true, want false")
	}
	if a.pendingLocalContainerUpdate == nil {
		t.Fatalf("pendingLocalContainerUpdate = nil, want pending confirmation")
	}
	joined := strings.Join(a.home.CommandOverlayLines(), "\n")
	for _, want := range []string{
		"This will also update your local containers.",
		"local containers: total=2 affected=1 needs_update=1 unknown=0 errors=0",
		"Run /update confirm to continue once",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("overlay missing %q in:\n%s", want, joined)
		}
	}
	if got := a.home.Status(); got != "local container update confirmation required" {
		t.Fatalf("status = %q", got)
	}
	for _, want := range []string{"dev_mode=true", "post_rebuild_check=true"} {
		if !strings.Contains(rawQuery, want) {
			t.Fatalf("query %q missing %q", rawQuery, want)
		}
	}
}

func TestConfirmPendingLocalContainerUpdateCanCancelConfirmOrDismiss(t *testing.T) {
	a := newUpdateCommandTestApp()
	a.pendingLocalContainerUpdate = &localContainerUpdateConfirmation{DevMode: false}

	a.handleUpdateCommand([]string{"cancel"})
	if a.pendingLocalContainerUpdate != nil {
		t.Fatalf("pendingLocalContainerUpdate after cancel = %+v, want nil", a.pendingLocalContainerUpdate)
	}
	if a.releaseUpdateRequested || a.devUpdateRequested || a.quitRequested {
		t.Fatalf("cancel requested update: release=%v dev=%v quit=%v", a.releaseUpdateRequested, a.devUpdateRequested, a.quitRequested)
	}
	if got := a.home.Status(); got != "update cancelled" {
		t.Fatalf("cancel status = %q", got)
	}

	a.pendingLocalContainerUpdate = &localContainerUpdateConfirmation{DevMode: false}
	a.handleUpdateCommand([]string{"confirm"})
	if !a.releaseUpdateRequested || !a.quitRequested {
		t.Fatalf("confirm did not request release update: release=%v quit=%v", a.releaseUpdateRequested, a.quitRequested)
	}

	var saved client.UISettings
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ui/settings" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&saved); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(saved)
	}))
	defer server.Close()

	a = newUpdateCommandTestApp()
	a.api = client.New(server.URL)
	a.pendingLocalContainerUpdate = &localContainerUpdateConfirmation{DevMode: true}
	a.handleUpdateCommand([]string{"dismiss"})
	if !a.config.Updates.LocalContainerWarningDismissed || !saved.Updates.LocalContainerWarningDismissed {
		t.Fatalf("dismissal was not persisted: config=%v saved=%v", a.config.Updates.LocalContainerWarningDismissed, saved.Updates.LocalContainerWarningDismissed)
	}
	if !a.devUpdateRequested || !a.quitRequested {
		t.Fatalf("dismiss did not request dev update: dev=%v quit=%v", a.devUpdateRequested, a.quitRequested)
	}
}

func TestUpdateSettingsRoundTripThroughAppConfig(t *testing.T) {
	settings := client.UISettings{Updates: client.UIUpdateSettings{LocalContainerWarningDismissed: true}}

	cfg := appConfigFromUISettings(settings)
	if !cfg.Updates.LocalContainerWarningDismissed {
		t.Fatalf("cfg.Updates.LocalContainerWarningDismissed = false, want true")
	}

	saved := uiSettingsFromAppConfig(cfg)
	if !saved.Updates.LocalContainerWarningDismissed {
		t.Fatalf("saved.Updates.LocalContainerWarningDismissed = false, want true")
	}
}

func TestClientLocalContainerPlanPostRebuildQuery(t *testing.T) {
	var rawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/update/local-containers" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rawQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(client.LocalContainerUpdatePlan{})
	}))
	defer server.Close()

	api := client.New(server.URL)
	devMode := true
	if _, err := api.GetLocalContainerUpdatePlanWithPostRebuild(context.Background(), &devMode, "v1.2.3", true); err != nil {
		t.Fatalf("GetLocalContainerUpdatePlanWithPostRebuild() error = %v", err)
	}
	for _, want := range []string{"dev_mode=true", "target_version=v1.2.3", "post_rebuild_check=true"} {
		if !strings.Contains(rawQuery, want) {
			t.Fatalf("query %q missing %q", rawQuery, want)
		}
	}
}
