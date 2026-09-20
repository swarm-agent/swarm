package api

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Purpose: issueDesktopLocalSession/productSessionTokenFromRequest/withAuth must
// keep browser-owned media requests authenticated while another daemon on the
// same hostname bootstraps. Cookies do not isolate ports. An in-memory browser
// jar plus two independent signing stores is the narrowest transport/auth proof:
// HEAD, full GET and Range must retain exact bytes and principal without refresh.
// This does not claim browser decoding or full artifact-handler coverage.
func TestDesktopSessionCookiesIsolateMediaAcrossOrigins(t *testing.T) {
	first, _, _, closeFirst := newDesktopJWTTestServer(t, true)
	defer closeFirst()
	second, _, _, closeSecond := newDesktopJWTTestServer(t, true)
	defer closeSecond()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	origins := []string{"http://127.0.0.1:5555", "http://127.0.0.1:5655"}
	servers := []*Server{first, second}
	request := func(origin, method, path string) *http.Request {
		r := httptest.NewRequest(method, origin+path, nil)
		r.RemoteAddr = "127.0.0.1:43210"
		r.Header.Set("Referer", origin+"/app")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		for _, cookie := range jar.Cookies(r.URL) {
			r.AddCookie(cookie)
		}
		return r
	}
	var names [2]string
	for round := 0; round < 3; round++ {
		for index, server := range servers {
			r := request(origins[index], http.MethodGet, "/v1/auth/desktop/session")
			rec := httptest.NewRecorder()
			server.DesktopHandler().ServeHTTP(rec, r)
			if rec.Code != http.StatusOK {
				t.Fatalf("bootstrap status=%d", rec.Code)
			}
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name == desktopLocalSessionCookieName {
				t.Fatal("bootstrap must issue only an origin-scoped cookie")
			}
			names[index] = cookies[0].Name
			jar.SetCookies(r.URL, cookies)
		}
		if names[0] == names[1] {
			t.Fatal("different ports share a session cookie name")
		}
		// An older daemon may still write the old shared cookie; it must not
		// shadow either upgraded origin's credential.
		legacyURL, _ := url.Parse(origins[0])
		jar.SetCookies(legacyURL, []*http.Cookie{{Name: desktopLocalSessionCookieName, Value: "obsolete", Path: "/"}})
		for index, server := range servers {
			for _, mediaType := range []string{"video/mp4", "audio/wav"} {
				const body = "0123456789"
				calls := 0
				media := server.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					principal, ok := PrincipalFromRequest(r)
					if !ok || principal.UserID != "user_desktop_jwt_test" || principal.AccountScopeID != "acct_desktop_jwt_test" {
						t.Fatal("media request lost its authenticated principal")
					}
					w.Header().Set("Content-Type", mediaType)
					http.ServeContent(w, r, "media", time.Time{}, strings.NewReader(body))
				}))
				for _, method := range []string{http.MethodHead, http.MethodGet} {
					if method == http.MethodGet {
						// Reproduce the preflight/playback race: the other instance
						// bootstraps after HEAD but before the native media GET.
						other := 1 - index
						refresh := request(origins[other], http.MethodGet, "/v1/auth/desktop/session")
						refreshed := httptest.NewRecorder()
						servers[other].DesktopHandler().ServeHTTP(refreshed, refresh)
						if refreshed.Code != http.StatusOK {
							t.Fatalf("other origin refresh status=%d", refreshed.Code)
						}
						jar.SetCookies(refresh.URL, refreshed.Result().Cookies())
					}
					r := request(origins[index], method, "/v3/sessions/media-session/artifacts/media-variant")
					rec := httptest.NewRecorder()
					media.ServeHTTP(rec, r)
					want := body
					if method == http.MethodHead {
						want = ""
					}
					if rec.Code != http.StatusOK || rec.Body.String() != want || rec.Header().Get("Content-Type") != mediaType {
						t.Fatalf("%s %s status=%d body=%q", mediaType, method, rec.Code, rec.Body.String())
					}
				}
				r := request(origins[index], http.MethodGet, "/v3/sessions/media-session/artifacts/media-variant")
				r.Header.Set("Range", "bytes=3-6")
				rec := httptest.NewRecorder()
				media.ServeHTTP(rec, r)
				if rec.Code != http.StatusPartialContent || rec.Body.String() != "3456" || rec.Header().Get("Content-Range") != "bytes 3-6/10" || calls != 3 {
					t.Fatalf("seek status=%d body=%q calls=%d", rec.Code, rec.Body.String(), calls)
				}
			}
			// The same cookie also protects ordinary JSON APIs, not just media.
			rec := httptest.NewRecorder()
			server.DesktopHandler().ServeHTTP(rec, request(origins[index], http.MethodGet, "/v1/me"))
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"user_desktop_jwt_test"`) {
				t.Fatalf("identity request status=%d body=%s", rec.Code, rec.Body.String())
			}
		}
	}
}

// Purpose: origin cookie selection is not an auth exemption. withAuth must reject
// missing, retired, wrong-origin and invalid credentials without calling protected
// media handlers or issuing replacement cookies; header auth must keep precedence.
func TestDesktopSessionOriginCookiesFailClosed(t *testing.T) {
	server, _, _, cleanup := newDesktopJWTTestServer(t, true)
	defer cleanup()
	issued, err := server.identitySessions.IssueForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://127.0.0.1:5555"
	other := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5655/", nil)
	current := httptest.NewRequest(http.MethodGet, origin+"/", nil)
	valid := buildDesktopLocalSessionCookie(current, issued.Token, issued.ExpiresAt)
	foreignServer, _, _, closeForeign := newDesktopJWTTestServer(t, true)
	defer closeForeign()
	foreignSession, err := foreignServer.identitySessions.IssueForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	foreign := buildDesktopLocalSessionCookie(current, foreignSession.Token, foreignSession.ExpiresAt)
	legacy := &http.Cookie{Name: desktopLocalSessionCookieName, Value: issued.Token}
	wrongOrigin := buildDesktopLocalSessionCookie(other, issued.Token, issued.ExpiresAt)
	invalid := buildDesktopLocalSessionCookie(current, "invalid", issued.ExpiresAt)
	for _, tc := range []struct {
		name    string
		cookies []*http.Cookie
		header  string
		want    int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "legacy only", cookies: []*http.Cookie{legacy}, want: http.StatusUnauthorized},
		{name: "wrong origin only", cookies: []*http.Cookie{wrongOrigin}, want: http.StatusUnauthorized},
		{name: "invalid cannot fall back", cookies: []*http.Cookie{invalid, legacy, wrongOrigin}, want: http.StatusUnauthorized},
		{name: "foreign signature in correct name", cookies: []*http.Cookie{foreign}, want: http.StatusUnauthorized},
		{name: "valid scoped", cookies: []*http.Cookie{wrongOrigin, legacy, valid}, want: http.StatusOK},
		{name: "valid header wins", cookies: []*http.Cookie{invalid}, header: issued.Token, want: http.StatusOK},
		{name: "invalid header cannot fall back", cookies: []*http.Cookie{valid}, header: "invalid", want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, origin+"/v3/sessions/media-session/artifacts/media-variant", nil)
			r.Header.Set("Range", "bytes=0-3")
			for _, cookie := range tc.cookies {
				r.AddCookie(cookie)
			}
			if tc.header != "" {
				r.Header.Set("X-Swarm-Token", tc.header)
			}
			called := false
			rec := httptest.NewRecorder()
			server.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if _, ok := PrincipalFromRequest(r); !ok {
					t.Fatal("authenticated request missing principal")
				}
				_, _ = w.Write([]byte("protected-media"))
			})).ServeHTTP(rec, r)
			if rec.Code != tc.want || called != (tc.want == http.StatusOK) {
				t.Fatalf("status=%d called=%v", rec.Code, called)
			}
			if tc.want == http.StatusUnauthorized && strings.Contains(rec.Body.String(), "protected-media") {
				t.Fatal("rejected request disclosed media")
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Fatal("protected resource must not bootstrap a session")
			}
		})
	}
}

// Purpose: the cookie builder must normalize equivalent default-port origins,
// isolate scheme/host/port, ignore untrusted forwarded authority, and preserve
// HttpOnly, Strict, host-only, root-path and TLS-only Secure attributes.
func TestDesktopSessionOriginCookieContract(t *testing.T) {
	name := func(raw string) string {
		return desktopLocalSessionCookieNameForRequest(httptest.NewRequest(http.MethodGet, raw, nil))
	}
	for _, pair := range [][2]string{
		{"http://localhost/", "http://LOCALHOST:80/"},
		{"https://localhost/", "https://localhost:443/"},
		{"http://[::1]/", "http://[::1]:80/"},
	} {
		if name(pair[0]) != name(pair[1]) {
			t.Fatalf("equivalent origins differ: %v", pair)
		}
	}
	// Trusted Serve admission, unlike a raw forwarding header, supplies the
	// external HTTPS scheme even when its hop to the daemon uses HTTP.
	proxied := httptest.NewRequest(http.MethodGet, "http://node.tailnet.ts.net/", nil)
	proxied.Header.Set("X-Forwarded-Proto", "https")
	proxied = proxied.WithContext(context.WithValue(proxied.Context(), desktopAdmittedOriginKey, desktopAdmission{origin: "https://node.tailnet.ts.net", tailscaleServe: true}))
	proxiedCookie := buildDesktopLocalSessionCookie(proxied, "fixture", time.Now().Add(time.Hour))
	if !proxiedCookie.Secure || proxiedCookie.Name != name("https://node.tailnet.ts.net/") {
		t.Fatal("admitted HTTPS origin lost secure cookie authority")
	}
	seen := map[string]bool{}
	for _, raw := range []string{"http://localhost:5555/", "http://localhost:5655/", "https://localhost:5555/", "http://127.0.0.1:5555/", "http://[::1]:5555/"} {
		r := httptest.NewRequest(http.MethodGet, raw, nil)
		cookie := buildDesktopLocalSessionCookie(r, "fixture", time.Now().Add(time.Hour))
		if seen[cookie.Name] || cookie.Name == desktopLocalSessionCookieName {
			t.Fatalf("origin name collision for %s", raw)
		}
		seen[cookie.Name] = true
		if cookie.Domain != "" || cookie.Path != "/" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge <= 0 || cookie.Secure != (r.TLS != nil) {
			t.Fatalf("cookie attributes changed for %s", raw)
		}
		r.Header.Set("X-Forwarded-Host", "untrusted.example:9999")
		r.Header.Set("X-Forwarded-Proto", "https")
		if desktopLocalSessionCookieNameForRequest(r) != cookie.Name {
			t.Fatal("untrusted forwarding headers changed cookie authority")
		}
	}
}
