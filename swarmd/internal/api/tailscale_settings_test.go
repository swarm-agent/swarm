package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tailscale"
)

type tailscaleSettingsDetectorStub struct {
	snapshot tailscale.Snapshot
	err      error
	modes    []tailscale.RefreshMode
	invalid  int
}

func (d *tailscaleSettingsDetectorStub) Snapshot(_ context.Context, mode tailscale.RefreshMode) (tailscale.Snapshot, error) {
	d.modes = append(d.modes, mode)
	return d.snapshot, d.err
}

func (d *tailscaleSettingsDetectorStub) Invalidate() { d.invalid++ }

func (d *tailscaleSettingsDetectorStub) EffectiveTarget() string {
	return "http://127.0.0.1:8118"
}

func (d *tailscaleSettingsDetectorStub) RemediationCommand() string {
	return "tailscale serve --bg " + d.EffectiveTarget()
}

func TestTailscaleSettingsApproveRequiresFreshVerifiedRoute(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "tailscale-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	detector := &tailscaleSettingsDetectorStub{snapshot: tailscale.Snapshot{
		SelfDNSName: "node.tailnet.ts.net",
		Routes: []tailscale.Route{{
			Origin:         "https://node.tailnet.ts.net",
			Authority:      "node.tailnet.ts.net:443",
			Classification: tailscale.ClassificationVerifiedSwarmDesktop,
		}},
	}}
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	policy := pebblestore.NewTailscaleServeAllowlistStore(store)
	server.SetTailscaleServePolicy(policy, detector)

	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, TailscaleSettingsApprovePath, bytes.NewBufferString(`{"origin":"https://NODE.tailnet.ts.net/"}`)), "user-a", "account-a")
	rec := httptest.NewRecorder()
	server.handleTailscaleSettingsApprove(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(detector.modes) != 1 || detector.modes[0] != tailscale.RequireFresh || detector.invalid != 2 {
		t.Fatalf("detector calls modes=%v invalidations=%d", detector.modes, detector.invalid)
	}
	record, ok, err := policy.Get()
	if err != nil || !ok || len(record.Origins) != 1 || record.Origins[0] != "https://node.tailnet.ts.net" {
		t.Fatalf("stored policy ok=%t err=%v record=%+v", ok, err, record)
	}
}

func TestTailscaleSettingsRejectsWrongTargetAndRevokesWhenUnavailable(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "tailscale-revoke.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	policy := pebblestore.NewTailscaleServeAllowlistStore(store)
	if _, _, err := policy.Add("https://node.tailnet.ts.net"); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	detector := &tailscaleSettingsDetectorStub{snapshot: tailscale.Snapshot{Routes: []tailscale.Route{{
		Origin:         "https://other.tailnet.ts.net",
		Authority:      "other.tailnet.ts.net:443",
		Classification: tailscale.ClassificationWrongTarget,
	}}}}
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.SetTailscaleServePolicy(policy, detector)

	approveReq := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, TailscaleSettingsApprovePath, bytes.NewBufferString(`{"origin":"https://other.tailnet.ts.net"}`)), "user-a", "account-a")
	approveRec := httptest.NewRecorder()
	server.handleTailscaleSettingsApprove(approveRec, approveReq)
	if approveRec.Code != http.StatusConflict {
		t.Fatalf("approve status=%d body=%s", approveRec.Code, approveRec.Body.String())
	}

	detector.err = errors.New("tailscale unavailable")
	revokeReq := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, TailscaleSettingsRevokePath, bytes.NewBufferString(`{"origin":"https://node.tailnet.ts.net"}`)), "user-a", "account-a")
	revokeRec := httptest.NewRecorder()
	server.handleTailscaleSettingsRevoke(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", revokeRec.Code, revokeRec.Body.String())
	}
	record, _, err := policy.Get()
	if err != nil || len(record.Origins) != 0 {
		t.Fatalf("policy after revoke err=%v record=%+v", err, record)
	}
}
