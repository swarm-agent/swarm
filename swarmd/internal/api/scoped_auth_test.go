package api

import (
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/security"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func setupScopedAuthTestServer(t *testing.T) (*Server, string, *security.Service, func()) {
	t.Helper()
	root := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}

	authStore := pebblestore.NewClientAuthStore(store)
	secSvc := security.NewService(authStore, events)
	attachStatus, err := secSvc.EnsureAttachAuth()
	if err != nil {
		t.Fatal(err)
	}
	attachToken, err := secSvc.RevealAttachToken()
	if err != nil {
		t.Fatal(err)
	}
	_ = attachStatus

	identityStore := pebblestore.NewIdentityStore(store)
	sessionStore := pebblestore.NewIdentitySessionStore(store)
	identitySvc := identity.NewService(identityStore)
	identitySessions := identity.NewSessionService(identityStore, sessionStore)

	// Bootstrap default user & account
	_, err = identitySvc.BootstrapFirstIdentity("test-admin")
	if err != nil {
		t.Fatal(err)
	}

	sessionRecordStore := pebblestore.NewSessionStore(store)
	sessionsSvc := sessionruntime.NewService(sessionRecordStore, nil)

	server := NewServer(
		auth.NewService(pebblestore.NewAuthStore(store), events),
		nil, nil, nil,
		sessionsSvc,
		nil, nil,
		secSvc,
		nil, nil, nil,
		events,
		stream.NewHub(events),
	)
	server.SetIdentityService(identitySvc)
	server.SetIdentitySessionService(identitySessions)

	cleanup := func() {
		_ = store.Close()
	}
	return server, attachToken, secSvc, cleanup
}
