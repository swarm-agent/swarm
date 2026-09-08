package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videoproject"
)

type nativeMediaAuthority struct {
	ref           pebblestore.ArtifactV3VideoReference
	account, user string
	reads         int
}

func (a *nativeMediaAuthority) ValidateVideoReference(account, user string, ref pebblestore.ArtifactV3VideoReference) error {
	if account != a.account || user != a.user || ref != a.ref {
		return errors.New("denied")
	}
	return nil
}
func (a *nativeMediaAuthority) ReadVideoReference(_ context.Context, account, user string, ref pebblestore.ArtifactV3VideoReference) ([]byte, error) {
	if err := a.ValidateVideoReference(account, user, ref); err != nil {
		return nil, err
	}
	a.reads++
	return []byte("0123456789"), nil
}

// Requirement: registered V3 media GET/HEAD delivers exact owned bytes with HTTP
// range support, never caller paths or legacy IDs. Threat: cross-session/account
// reads, forged reference fields or malformed JSON. Exercise primary routing and
// service identity, asserting denial returns no bytes and causes no media reads.
func TestSessionsV3NativeVideoMediaAuthority(t *testing.T) {
	server, sessions, _, _, _, _, _ := newArtifactSessionFixture(t, "note.txt", "fixture")
	principal := testPrincipal()
	sessionID := "native-media-session"
	if err := sessions.Store().CreateSession(pebblestore.SessionSnapshot{ID: sessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Title: "Media", WorkspacePath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	ref := pebblestore.ArtifactV3VideoReference{SessionID: sessionID, ArtifactID: "artifact", RevisionID: "revision-a", DerivativeID: "derivative", MediaType: "video/mp4"}
	authority := &nativeMediaAuthority{ref: ref, account: principal.AccountScopeID, user: principal.UserID}
	projects := videoproject.NewService(sessions.Store())
	projects.SetArtifactV3Authority(authority)
	server.SetVideoProjectService(projects)
	for _, tc := range []struct {
		name, method, raw, rangeHeader string
		principal                      identity.Principal
		status                         int
		body                           string
	}{
		{name: "range", method: "GET", rangeHeader: "bytes=2-5", principal: principal, status: 206, body: "2345"},
		{name: "head", method: "HEAD", principal: principal, status: 200},
		{name: "unknown field", method: "GET", raw: `{"unexpected":true}`, principal: principal, status: 400},
		{name: "trailing JSON", method: "GET", raw: `{} {}`, principal: principal, status: 400},
		{name: "foreign session reference", method: "GET", raw: `{"session_id":"foreign"}`, principal: principal, status: 404},
		{name: "foreign account", method: "GET", principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "foreign", UserID: "foreign"}, status: 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.raw
			if raw == "" {
				body, _ := json.Marshal(ref)
				raw = string(body)
			}
			req := httptest.NewRequest(tc.method, "/v3/sessions/"+sessionID+"/video/artifact-v3/media?reference="+url.QueryEscape(raw), nil)
			req.Header.Set("Range", tc.rangeHeader)
			rec := httptest.NewRecorder()
			before := authority.reads
			server.handleSessionV3PrimaryByID(rec, req.WithContext(identity.ContextWithPrincipal(req.Context(), tc.principal)))
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tc.status < 300 {
				if rec.Body.String() != tc.body || rec.Header().Get("Content-Type") != "video/mp4" || rec.Header().Get("Cache-Control") != "private, no-store" {
					t.Fatalf("media response=%v %q", rec.Header(), rec.Body.String())
				}
			} else if authority.reads != before {
				t.Fatal("denied request read media")
			}
		})
	}
}
