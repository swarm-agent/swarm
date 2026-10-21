package google

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// fakeMetadataServer plays the GCE metadata server's token endpoint. Each
// fetch returns a distinct token so tests can tell a cached token from a new one.
type fakeMetadataServer struct {
	t         *testing.T
	mu        sync.Mutex
	calls     int
	status    int
	noFlavor  bool
	expiresIn int64
	server    *httptest.Server
}

func newFakeMetadataServer(t *testing.T) *fakeMetadataServer {
	t.Helper()
	f := &fakeMetadataServer{t: t, status: http.StatusOK, expiresIn: 3600}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path != "/computeMetadata/v1/instance/service-accounts/default/token" || r.Method != http.MethodGet {
			t.Errorf("metadata request %s %s, want GET of the default service account token", r.Method, r.URL.Path)
		}
		if r.Header.Get("Metadata-Flavor") != "Google" {
			t.Errorf("metadata request without Metadata-Flavor: Google")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		f.calls++
		if !f.noFlavor {
			w.Header().Set("Metadata-Flavor", "Google")
		}
		if f.status != http.StatusOK {
			w.WriteHeader(f.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"meta-token-%d","expires_in":%d,"token_type":"Bearer"}`, f.calls, f.expiresIn)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeMetadataServer) tokenURL() string {
	return f.server.URL + "/computeMetadata/v1/instance/service-accounts/default/token"
}

func (f *fakeMetadataServer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeMetadataServer) credentials(now func() time.Time) *ADCCredentials {
	c := newMetadataCredentials(f.tokenURL(), newMetadataClient())
	if now != nil {
		c.now = now
	}
	return c
}

func TestADCMetadataTokenIsCachedAndRefreshedBeforeExpiry(t *testing.T) {
	// Purpose:
	// - Requirement: the metadata-server token is fetched once, reused until
	//   adcTokenRefreshMargin before it expires, then fetched again; Invalidate
	//   forces a new fetch. Every fetch sends Metadata-Flavor: Google.
	// - Threat/regression: fetching per request would add a metadata round trip
	//   to every model call; never refreshing would fail every run an hour after
	//   start with expired-token 401s.
	// - Boundary/authority: ADCCredentials.Token / Invalidate and
	//   metadataTokenFetcher in vertex_credentials.go.
	// - Narrowest test layer: the credential type against an httptest metadata
	//   server with an injected clock; no runner is needed.
	meta := newFakeMetadataServer(t)
	clock := time.Unix(1_800_000_000, 0)
	creds := meta.credentials(func() time.Time { return clock })
	ctx := context.Background()

	first, err := creds.Token(ctx)
	if err != nil || first != "meta-token-1" {
		t.Fatalf("first token = %q, %v", first, err)
	}
	clock = clock.Add(50 * time.Minute)
	if again, err := creds.Token(ctx); err != nil || again != first || meta.callCount() != 1 {
		t.Fatalf("token at 50m = %q (%v), calls=%d; want the cached token and one fetch", again, err, meta.callCount())
	}
	clock = clock.Add(9 * time.Minute) // 59m: inside the refresh margin of a 60m token
	refreshed, err := creds.Token(ctx)
	if err != nil || refreshed != "meta-token-2" || meta.callCount() != 2 {
		t.Fatalf("token at 59m = %q (%v), calls=%d; want a fresh token", refreshed, err, meta.callCount())
	}
	creds.Invalidate()
	if after, err := creds.Token(ctx); err != nil || after != "meta-token-3" {
		t.Fatalf("token after Invalidate = %q (%v); want a new fetch", after, err)
	}
}

func TestADCMetadataFailuresExplainWhyAndLeakNoToken(t *testing.T) {
	// Purpose:
	// - Requirement: a missing metadata server, a VM without a service account,
	//   and a response that is not from a metadata server each fail with a
	//   reason the operator can act on; a failed fetch caches nothing.
	// - Threat/regression: a vague "auth failed" hides that the container cannot
	//   reach 169.254.169.254; trusting a response without Metadata-Flavor would
	//   accept a token from a stray listener; echoing a body could print a token.
	// - Boundary/authority: metadataTokenFetcher and ADCCredentials.Token.
	// - Narrowest test layer: credential type against httptest servers.
	ctx := context.Background()

	meta := newFakeMetadataServer(t)
	meta.status = http.StatusNotFound
	if _, err := meta.credentials(nil).Token(ctx); err == nil || !strings.Contains(err.Error(), "no service account") {
		t.Fatalf("404 err = %v, want the no-service-account reason", err)
	}

	meta = newFakeMetadataServer(t)
	meta.noFlavor = true
	_, err := meta.credentials(nil).Token(ctx)
	if err == nil || !strings.Contains(err.Error(), "did not come from a GCE metadata server") || strings.Contains(err.Error(), "meta-token") {
		t.Fatalf("no-flavor err = %v, want refusal without the token", err)
	}

	gone := newFakeMetadataServer(t)
	url := gone.tokenURL()
	gone.server.Close()
	creds := newMetadataCredentials(url, newMetadataClient())
	if _, err := creds.Token(ctx); err == nil || !strings.Contains(err.Error(), "no GCE metadata server reachable") {
		t.Fatalf("unreachable err = %v, want the unreachable reason", err)
	}
	if creds.token != "" {
		t.Fatalf("a failed fetch left a cached token")
	}
}

func TestADCServiceAccountFileSignsRS256JWTAndFailsClosed(t *testing.T) {
	// Purpose:
	// - Requirement: with GOOGLE_APPLICATION_CREDENTIALS set, the token comes
	//   from an RS256-signed JWT exchanged at the key file's token_uri for the
	//   cloud-platform scope; a file that is not a service-account key fails and
	//   never falls back to the metadata server.
	// - Threat/regression: an unsigned or wrongly scoped assertion is rejected by
	//   Google; a silent fallback to another identity would run under the wrong
	//   service account.
	// - Boundary/authority: NewApplicationDefaultCredentials and
	//   serviceAccountTokenFetcher in vertex_credentials.go.
	// - Narrowest test layer: credential type with a key generated in the test
	//   and an httptest token endpoint that verifies the signature.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	exchanges := 0
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("grant_type = %q", got)
		}
		claims := verifyTestRS256(t, r.PostForm.Get("assertion"), &key.PublicKey)
		if claims["scope"] != googleCloudPlatformScope || claims["iss"] != "runner@example-project.iam.gserviceaccount.com" {
			t.Errorf("claims = %v, want cloud-platform scope and the key's client_email", claims)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"sa-token","expires_in":3600,"token_type":"Bearer"}`)
	}))
	t.Cleanup(tokenServer.Close)

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	file := map[string]string{
		"type": "service_account", "project_id": "example-project", "private_key_id": "test-key-id",
		"private_key": string(keyPEM), "client_email": "runner@example-project.iam.gserviceaccount.com",
		"token_uri": tokenServer.URL,
	}
	raw, _ := json.Marshal(file)
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(googleCredentialsEnv, path)
	creds := NewApplicationDefaultCredentials()
	if !strings.Contains(creds.Source(), googleCredentialsEnv) {
		t.Fatalf("source = %q, want the key file", creds.Source())
	}
	token, err := creds.Token(context.Background())
	if err != nil || token != "sa-token" || exchanges != 1 {
		t.Fatalf("token = %q, err = %v, exchanges = %d", token, err, exchanges)
	}

	userFile := filepath.Join(t.TempDir(), "user.json")
	if err := os.WriteFile(userFile, []byte(`{"type":"authorized_user","client_id":"x","client_secret":"y","refresh_token":"z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(googleCredentialsEnv, userFile)
	bad := NewApplicationDefaultCredentials()
	if _, err := bad.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "not a service account key") {
		t.Fatalf("authorized_user file err = %v, want refusal", err)
	}
	if strings.Contains(bad.Source(), "metadata") {
		t.Fatalf("a set key file fell back to the metadata server")
	}
}

func verifyTestRS256(t *testing.T, assertion string, public *rsa.PublicKey) map[string]any {
	t.Helper()
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion is not a JWT")
	}
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if !strings.Contains(string(header), `"RS256"`) {
		t.Fatalf("JWT header %s, want RS256", header)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("JWT signature does not verify: %v", err)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	claims := map[string]any{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	return claims
}

// vertexCapture forwards Vertex requests to an httptest server standing in for
// aiplatform.googleapis.com and records what the runner sent.
type vertexCapture struct {
	mu       sync.Mutex
	requests []capturedVertexRequest
}

type capturedVertexRequest struct {
	url    string
	header http.Header
	body   string
}

func (c *vertexCapture) all() []capturedVertexRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedVertexRequest(nil), c.requests...)
}

func newVertexTestRunner(t *testing.T, location string, handler http.HandlerFunc) (*VertexRunner, *fakeMetadataServer, *vertexCapture, context.Context) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "vertex.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	authStore := pebblestore.NewAuthStore(store)
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{AccountScopeID: "account-1", Provider: VertexProviderID, Type: pebblestore.AuthTypeADC, GoogleProject: "example-project", GoogleLocation: location, SetActive: true}); err != nil {
		t.Fatalf("seed vertex settings: %v", err)
	}
	vertex := httptest.NewServer(handler)
	t.Cleanup(vertex.Close)
	target, _ := url.Parse(vertex.URL)
	capture := &vertexCapture{}
	meta := newFakeMetadataServer(t)
	runner := NewVertexRunner(authStore, meta.credentials(nil))
	runner.retryDelay = func(int) time.Duration { return 0 }
	runner.httpClient = &http.Client{Transport: googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		capture.mu.Lock()
		capture.requests = append(capture.requests, capturedVertexRequest{url: req.URL.String(), header: req.Header.Clone(), body: string(body)})
		capture.mu.Unlock()
		forwarded := req.Clone(req.Context())
		forwarded.URL.Scheme, forwarded.URL.Host, forwarded.Host = target.Scheme, target.Host, target.Host
		forwarded.Body = io.NopCloser(strings.NewReader(string(body)))
		forwarded.RequestURI = ""
		return http.DefaultTransport.RoundTrip(forwarded)
	})}
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"})
	return runner, meta, capture, ctx
}

const vertexOKReply = `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"hello from vertex"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7}}`

func vertexReply(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+vertexOKReply+"\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, vertexOKReply)
}

func TestVertexRequestsUseRegionalOrGlobalHostAndBearerToken(t *testing.T) {
	// Purpose:
	// - Requirement: Vertex requests go to
	//   https://{host}/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:{method},
	//   host aiplatform.googleapis.com for "global" and {region}-aiplatform.googleapis.com
	//   for a region, with Authorization: Bearer <metadata token>, never an API
	//   key header or key= query and never the Gemini API service_tier field;
	//   usage is reported as vertex_api_usage.
	// - Threat/regression: a wrong host or path is a 404 on every visitor
	//   message; sending x-goog-api-key or service_tier is refused by Vertex.
	// - Boundary/authority: vertexEndpoint, vertexSurface and the shared
	//   generateContentTransport (generate_content.go).
	// - Narrowest test layer: VertexRunner round trip against an httptest Vertex
	//   endpoint and an httptest metadata server.
	for _, tc := range []struct {
		location, host string
	}{
		{"", "aiplatform.googleapis.com"},
		{"global", "aiplatform.googleapis.com"},
		{"europe-west4", "europe-west4-aiplatform.googleapis.com"},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/streaming=%v", tc.host, streaming), func(t *testing.T) {
				runner, meta, capture, ctx := newVertexTestRunner(t, tc.location, vertexReply)
				request := provideriface.Request{Model: "gemini-3.5-flash-lite", Instructions: "Be brief.", Input: []map[string]any{{"role": "user", "content": "hello"}}}
				var resp provideriface.Response
				var err error
				if streaming {
					resp, err = runner.CreateResponseStreaming(ctx, request, func(provideriface.StreamEvent) {})
				} else {
					resp, err = runner.CreateResponse(ctx, request)
				}
				if err != nil || resp.Text != "hello from vertex" {
					t.Fatalf("resp = %q, err = %v", resp.Text, err)
				}
				if resp.Usage.Source != vertexUsageSource || resp.Usage.TotalTokens != 7 {
					t.Fatalf("usage = %+v, want vertex_api_usage with 7 tokens", resp.Usage)
				}
				location := tc.location
				if location == "" {
					location = "global"
				}
				method, query := "generateContent", ""
				if streaming {
					method, query = "streamGenerateContent", "?alt=sse"
				}
				want := "https://" + tc.host + "/v1/projects/example-project/locations/" + location + "/publishers/google/models/gemini-3.5-flash-lite:" + method + query
				sent := capture.all()
				if len(sent) != 1 || sent[0].url != want {
					t.Fatalf("requests = %+v, want one to %s", sent, want)
				}
				if got := sent[0].header.Get("Authorization"); got != "Bearer meta-token-1" {
					t.Fatalf("Authorization = %q, want the metadata token", got)
				}
				if sent[0].header.Get(googleAPIKeyHeader) != "" || strings.Contains(sent[0].url, "key=") {
					t.Fatalf("an API key was sent to Vertex: %+v", sent[0])
				}
				if strings.Contains(sent[0].body, "service_tier") || !strings.Contains(sent[0].body, `"contents"`) || !strings.Contains(sent[0].body, `"systemInstruction"`) {
					t.Fatalf("body = %s, want generateContent schema without service_tier", sent[0].body)
				}
				if meta.callCount() != 1 {
					t.Fatalf("metadata calls = %d, want 1", meta.callCount())
				}
			})
		}
	}
}

func TestVertexStatusErrorsRetryLikeGoogleAndExplainPermissions(t *testing.T) {
	// Purpose:
	// - Requirement: 429 and 5xx retry like the Gemini API runner; 401 drops the
	//   cached token and retries once with a fresh one; 403 and 404 return at
	//   once with the real status and a reason naming roles/aiplatform.user or
	//   the location; nothing secret appears in errors.
	// - Threat/regression: an expired or revoked token would fail every request
	//   until restart; a bare "403" leaves the operator guessing which IAM role
	//   or scope is missing; retrying 403 would cost four calls per message.
	// - Boundary/authority: generateContentTransport.generate / stream,
	//   generateContentSurface.retryUnauthorized, vertexStatusDetail.
	// - Narrowest test layer: VertexRunner round trip with httptest fakes.
	for _, streaming := range []bool{false, true} {
		call := func(runner *VertexRunner, ctx context.Context) (provideriface.Response, error) {
			request := provideriface.Request{Model: "gemini-3.5-flash-lite", Input: []map[string]any{{"role": "user", "content": "hello"}}}
			if streaming {
				return runner.CreateResponseStreaming(ctx, request, nil)
			}
			return runner.CreateResponse(ctx, request)
		}
		for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
			t.Run(fmt.Sprintf("transient %d/streaming=%v", status, streaming), func(t *testing.T) {
				calls := 0
				runner, _, _, ctx := newVertexTestRunner(t, "us-central1", func(w http.ResponseWriter, r *http.Request) {
					calls++
					if calls < 3 {
						w.WriteHeader(status)
						fmt.Fprint(w, `{"error":{"code":429,"message":"try later"}}`)
						return
					}
					vertexReply(w, r)
				})
				resp, err := call(runner, ctx)
				if err != nil || calls != 3 || resp.Text != "hello from vertex" {
					t.Fatalf("calls = %d, text = %q, err = %v; want recovery on the third call", calls, resp.Text, err)
				}
			})
		}
		t.Run(fmt.Sprintf("401 refreshes the token once/streaming=%v", streaming), func(t *testing.T) {
			var auths []string
			runner, meta, _, ctx := newVertexTestRunner(t, "global", func(w http.ResponseWriter, r *http.Request) {
				auths = append(auths, r.Header.Get("Authorization"))
				if len(auths) == 1 {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				vertexReply(w, r)
			})
			resp, err := call(runner, ctx)
			if err != nil || resp.Text != "hello from vertex" {
				t.Fatalf("resp = %q, err = %v", resp.Text, err)
			}
			if fmt.Sprint(auths) != "[Bearer meta-token-1 Bearer meta-token-2]" || meta.callCount() != 2 {
				t.Fatalf("authorizations = %v, metadata calls = %d; want a fresh token on the retry", auths, meta.callCount())
			}
		})
		t.Run(fmt.Sprintf("repeated 401 fails/streaming=%v", streaming), func(t *testing.T) {
			calls := 0
			runner, _, _, ctx := newVertexTestRunner(t, "global", func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(http.StatusUnauthorized)
			})
			_, err := call(runner, ctx)
			if err == nil || !strings.Contains(err.Error(), "status=401") || calls != 2 || strings.Contains(err.Error(), "meta-token") {
				t.Fatalf("err = %v, calls = %d; want status=401 after one refresh and no token in the error", err, calls)
			}
		})
		for _, tc := range []struct {
			status int
			reason string
		}{
			{http.StatusForbidden, "roles/aiplatform.user on project example-project"},
			{http.StatusNotFound, "location global"},
			{http.StatusBadRequest, ""},
		} {
			t.Run(fmt.Sprintf("permanent %d/streaming=%v", tc.status, streaming), func(t *testing.T) {
				calls := 0
				runner, _, _, ctx := newVertexTestRunner(t, "global", func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.WriteHeader(tc.status)
					fmt.Fprint(w, `{"error":{"message":"denied"}}`)
				})
				_, err := call(runner, ctx)
				if err == nil || calls != 1 || !strings.Contains(err.Error(), fmt.Sprintf("vertex %s failed status=%d", map[bool]string{false: "generateContent", true: "streamGenerateContent"}[streaming], tc.status)) || !strings.Contains(err.Error(), tc.reason) {
					t.Fatalf("err = %v, calls = %d; want one call and status=%d with %q", err, calls, tc.status, tc.reason)
				}
			})
		}
	}
}

func TestVertexRefusesWhatItCannotSend(t *testing.T) {
	// Purpose:
	// - Requirement: a Vertex run fails closed without a token, with a
	//   session media contract, or with a requested service tier; nothing is sent.
	// - Threat/regression: silently dropping media or a paid tier would answer
	//   a different request than the one asked for.
	// - Boundary/authority: VertexRunner.prepare and vertexSurface.authorize.
	// - Narrowest test layer: VertexRunner with httptest fakes counting calls.
	calls := 0
	runner, meta, _, ctx := newVertexTestRunner(t, "global", func(w http.ResponseWriter, r *http.Request) {
		calls++
		vertexReply(w, r)
	})
	request := provideriface.Request{Model: "gemini-3.5-flash-lite", Input: []map[string]any{{"role": "user", "content": "hello"}}}

	// Every run attaches a media contract; a conversation without media must
	// still be answered (only media actually present is refused).
	contractOnly := request
	contractOnly.MediaContract = provideriface.SessionMediaContract{Hash: "contract"}
	media := contractOnly
	media.Input = []map[string]any{{"role": "user", "content": []any{map[string]any{"type": "session_media", "media": provideriface.SessionMediaPayload{AssetID: "a", Modality: "image", MIMEType: "image/png", Size: 1, Bytes: []byte{1}}}}}}
	if _, err := runner.CreateResponse(ctx, media); err == nil || !strings.Contains(err.Error(), "media") {
		t.Fatalf("media err = %v", err)
	}
	tier := request
	tier.ServiceTier = "priority"
	tier.ModelCatalog = pebblestore.ModelCatalogRecord{ServiceTierMappings: []pebblestore.ModelCatalogServiceTierMapping{{Tier: "priority", ProviderParameter: "service_tier", ProviderValue: "priority"}}}
	if _, err := runner.CreateResponse(ctx, tier); err == nil || !strings.Contains(err.Error(), "service tier") {
		t.Fatalf("service tier err = %v", err)
	}
	meta.mu.Lock()
	meta.status = http.StatusNotFound
	meta.mu.Unlock()
	if _, err := runner.CreateResponse(ctx, request); err == nil || !strings.Contains(err.Error(), "Google credentials unavailable") {
		t.Fatalf("no-token err = %v", err)
	}
	if calls != 0 {
		t.Fatalf("vertex calls = %d, want none", calls)
	}
	meta.mu.Lock()
	meta.status = http.StatusOK
	meta.mu.Unlock()
	if _, err := runner.CreateResponse(ctx, contractOnly); err != nil || calls != 1 {
		t.Fatalf("contract without media: err = %v calls = %d", err, calls)
	}
}

func TestVertexStatusIsReadyOnlyAfterATokenFetch(t *testing.T) {
	// Purpose:
	// - Requirement: provider status is ready only when the account has adc
	//   settings and a token fetch succeeds, and otherwise says why; credential
	//   verification accepts only the secret-free adc type with a valid project
	//   and location (the location becomes part of the request host).
	// - Threat/regression: "ready" on a machine without a service account sends
	//   a sealed agent's visitors into failing runs; an unvalidated location
	//   could point requests at another host.
	// - Boundary/authority: VertexAdapter.Status, VertexAdapter.VerifyCredential,
	//   ValidateVertexSettings and the adc branch of AuthStore.UpsertCredential.
	// - Narrowest test layer: adapter over a temp Pebble store and an httptest
	//   metadata server.
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "vertex-status.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	authStore := pebblestore.NewAuthStore(store)
	meta := newFakeMetadataServer(t)
	adapter := NewVertexAdapter(authStore, meta.credentials(nil))
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"})

	status, err := adapter.Status(ctx)
	if err != nil || status.Ready || !strings.Contains(status.Reason, "missing vertex settings") || meta.callCount() != 0 {
		t.Fatalf("status without settings = %+v, %v (metadata calls %d)", status, err, meta.callCount())
	}
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{AccountScopeID: "account-1", Provider: VertexProviderID, Type: pebblestore.AuthTypeADC, SetActive: true}); err == nil {
		t.Fatalf("adc credential without google_project was stored")
	}
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{AccountScopeID: "account-1", Provider: VertexProviderID, Type: pebblestore.AuthTypeADC, GoogleProject: "example-project", SetActive: true}); err != nil {
		t.Fatal(err)
	}
	meta.mu.Lock()
	meta.status = http.StatusNotFound
	meta.mu.Unlock()
	if status, _ := adapter.Status(ctx); status.Ready || !strings.Contains(status.Reason, "no service account") {
		t.Fatalf("status without a service account = %+v", status)
	}
	meta.mu.Lock()
	meta.status = http.StatusOK
	meta.mu.Unlock()
	if status, _ := adapter.Status(ctx); !status.Ready || status.ID != VertexProviderID {
		t.Fatalf("status after token fetch = %+v, want ready", status)
	}

	for _, bad := range []provideriface.AuthCredential{
		{Provider: VertexProviderID, Type: pebblestore.AuthTypeAPI, APIKey: "k"},
		{Provider: VertexProviderID, Type: pebblestore.AuthTypeADC, GoogleProject: "example-project", APIKey: "k"},
		{Provider: VertexProviderID, Type: pebblestore.AuthTypeADC, GoogleProject: "Bad_Project"},
		{Provider: VertexProviderID, Type: pebblestore.AuthTypeADC, GoogleProject: "example-project", GoogleLocation: "evil.example.com/x"},
		{Provider: VertexProviderID, Type: pebblestore.AuthTypeADC, GoogleProject: "example-project", GoogleLocation: "us-central1.example.com"},
	} {
		if result, err := adapter.VerifyCredential(ctx, bad); err == nil || result.Connected {
			t.Fatalf("VerifyCredential(%+v) = %+v, %v; want refusal", bad, result, err)
		}
	}
	result, err := adapter.VerifyCredential(ctx, provideriface.AuthCredential{Provider: VertexProviderID, Type: pebblestore.AuthTypeADC, GoogleProject: "example-project", GoogleLocation: "us-central1"})
	if err != nil || !result.Connected || !strings.Contains(result.Message, "us-central1") || strings.Contains(result.Message, "meta-token") {
		t.Fatalf("VerifyCredential = %+v, %v", result, err)
	}
}
