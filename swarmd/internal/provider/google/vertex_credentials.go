package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	googleoauth "golang.org/x/oauth2/google"
)

const (
	googleCloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

	// gceMetadataTokenURL is the metadata server's token endpoint for the
	// attached service account. The link-local address is the same server as
	// metadata.google.internal and needs no DNS, so it works from a container
	// whose resolver is not the VPC resolver.
	gceMetadataTokenURL = "http://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token"

	// googleCredentialsEnv names a service-account key file (Application
	// Default Credentials). When set it is the only credential source.
	googleCredentialsEnv = "GOOGLE_APPLICATION_CREDENTIALS"

	// adcTokenRefreshMargin renews a cached token this long before it expires.
	adcTokenRefreshMargin = 2 * time.Minute
	// adcTokenFetchTimeout bounds one token fetch (metadata server or token
	// exchange), so an unreachable source fails fast instead of holding a run.
	adcTokenFetchTimeout = 5 * time.Second

	maxADCTokenResponseBytes = 64 << 10
)

// ADCCredentials yields OAuth access tokens for the machine's own Google
// identity: the service account attached to a GCE VM or GKE workload (through
// the metadata server), or a service-account key file named by
// GOOGLE_APPLICATION_CREDENTIALS. Tokens are cached in memory until shortly
// before they expire and are never stored or logged. One instance serves the
// whole daemon, because the identity belongs to the machine.
type ADCCredentials struct {
	source string
	fetch  func(ctx context.Context) (string, time.Duration, error)
	now    func() time.Time

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// NewApplicationDefaultCredentials selects the credential source once, at
// daemon start: the key file when GOOGLE_APPLICATION_CREDENTIALS is set,
// otherwise the GCE/GKE metadata server. A set but unusable key file fails
// every fetch; it never falls back to the metadata server.
func NewApplicationDefaultCredentials() *ADCCredentials {
	if path := strings.TrimSpace(os.Getenv(googleCredentialsEnv)); path != "" {
		return newServiceAccountFileCredentials(path, newServiceAccountTokenClient())
	}
	return newMetadataCredentials(gceMetadataTokenURL, newMetadataClient())
}

func newMetadataCredentials(tokenURL string, client *http.Client) *ADCCredentials {
	return &ADCCredentials{
		source: "GCE metadata server",
		fetch:  metadataTokenFetcher(tokenURL, client),
		now:    time.Now,
	}
}

func newServiceAccountFileCredentials(path string, client *http.Client) *ADCCredentials {
	return &ADCCredentials{
		source: "service account key file (" + googleCredentialsEnv + ")",
		fetch:  serviceAccountTokenFetcher(path, client),
		now:    time.Now,
	}
}

// newMetadataClient never uses a proxy: the token must not leave the host,
// and the metadata server is link-local.
func newMetadataClient() *http.Client {
	return &http.Client{
		Timeout: adcTokenFetchTimeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			ResponseHeaderTimeout: adcTokenFetchTimeout,
			MaxIdleConns:          1,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func newServiceAccountTokenClient() *http.Client {
	return &http.Client{
		Timeout:       adcTokenFetchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Source names where tokens come from, for status messages.
func (c *ADCCredentials) Source() string {
	if c == nil {
		return ""
	}
	return c.source
}

// Token returns a valid access token, fetching a new one when none is cached
// or the cached one is within adcTokenRefreshMargin of expiry. Concurrent
// callers share one fetch.
func (c *ADCCredentials) Token(ctx context.Context) (string, error) {
	if c == nil || c.fetch == nil {
		return "", errors.New("google application default credentials are not configured")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.token != "" && now.Add(adcTokenRefreshMargin).Before(c.expiry) {
		return c.token, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	fetchCtx, cancel := context.WithTimeout(ctx, adcTokenFetchTimeout)
	defer cancel()
	token, lifetime, err := c.fetch(fetchCtx)
	if err != nil {
		c.token, c.expiry = "", time.Time{}
		return "", err
	}
	if token == "" || lifetime <= 0 {
		c.token, c.expiry = "", time.Time{}
		return "", fmt.Errorf("%s returned no usable access token", c.source)
	}
	c.token, c.expiry = token, now.Add(lifetime)
	return token, nil
}

// Invalidate drops the cached token, so the next Token call fetches a new
// one. Runners call it after a provider answers 401.
func (c *ADCCredentials) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.token, c.expiry = "", time.Time{}
	c.mu.Unlock()
}

type metadataTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

func metadataTokenFetcher(tokenURL string, client *http.Client) func(context.Context) (string, time.Duration, error) {
	return func(ctx context.Context) (string, time.Duration, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
		if err != nil {
			return "", 0, errors.New("build GCE metadata token request")
		}
		req.Header.Set("Metadata-Flavor", "Google")
		resp, err := client.Do(req)
		if err != nil {
			return "", 0, fmt.Errorf("no GCE metadata server reachable (not on Google Cloud, or this container cannot reach 169.254.169.254): %s", sanitizeGoogleText(err.Error()))
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxADCTokenResponseBytes))
		if err != nil {
			return "", 0, errors.New("read GCE metadata token response")
		}
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return "", 0, errors.New("the GCE metadata server has no service account for this machine (attach one that has roles/aiplatform.user)")
		case resp.StatusCode == http.StatusForbidden:
			return "", 0, errors.New("the GCE metadata server refused the token request (status=403)")
		case resp.StatusCode != http.StatusOK:
			return "", 0, fmt.Errorf("GCE metadata token request failed status=%d", resp.StatusCode)
		}
		// A real metadata server always answers with this header; anything
		// else (a captive proxy, a stray listener) is not trusted.
		if resp.Header.Get("Metadata-Flavor") != "Google" {
			return "", 0, errors.New("the token response did not come from a GCE metadata server")
		}
		var decoded metadataTokenResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			// The body may hold a token: never echo it.
			return "", 0, errors.New("decode GCE metadata token response")
		}
		if strings.TrimSpace(decoded.AccessToken) == "" || decoded.ExpiresIn <= 0 || !strings.EqualFold(strings.TrimSpace(decoded.TokenType), "Bearer") {
			return "", 0, errors.New("the GCE metadata server returned an incomplete token response")
		}
		return strings.TrimSpace(decoded.AccessToken), time.Duration(decoded.ExpiresIn) * time.Second, nil
	}
}

// serviceAccountTokenFetcher signs a JWT with the key file's private key
// (RS256) and exchanges it at the key file's token_uri. The file is read on
// every fetch (about hourly), so a rotated key takes effect without a restart.
func serviceAccountTokenFetcher(path string, client *http.Client) func(context.Context) (string, time.Duration, error) {
	return func(ctx context.Context) (string, time.Duration, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", 0, fmt.Errorf("read the %s key file: %s", googleCredentialsEnv, sanitizeGoogleText(err.Error()))
		}
		config, err := googleoauth.JWTConfigFromJSON(data, googleCloudPlatformScope)
		if err != nil {
			return "", 0, fmt.Errorf("the %s file is not a service account key: %s", googleCredentialsEnv, sanitizeGoogleText(err.Error()))
		}
		token, err := config.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, client)).Token()
		if err != nil {
			var retrieve *oauth2.RetrieveError
			if errors.As(err, &retrieve) && retrieve.Response != nil {
				code := strings.TrimSpace(retrieve.ErrorCode)
				if code == "" {
					code = "rejected"
				}
				return "", 0, fmt.Errorf("the service account token exchange failed status=%d (%s)", retrieve.Response.StatusCode, sanitizeGoogleText(code))
			}
			return "", 0, fmt.Errorf("the service account token exchange failed: %s", sanitizeGoogleText(err.Error()))
		}
		if token == nil || strings.TrimSpace(token.AccessToken) == "" || token.Expiry.IsZero() {
			return "", 0, errors.New("the service account token exchange returned no usable token")
		}
		return strings.TrimSpace(token.AccessToken), time.Until(token.Expiry), nil
	}
}
