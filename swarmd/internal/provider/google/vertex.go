package google

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/defaults"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// VertexProviderID runs Google's Gemini models through Vertex AI with the
// machine's own Google identity (Application Default Credentials), so no
// Gemini API key exists. It shares the google provider's generateContent
// request, response, streaming and retry code and its model catalog.
const VertexProviderID = "vertex"

// VertexDefaultLocation is used when an adc credential names no location.
const VertexDefaultLocation = "global"

const vertexUsageSource = "vertex_api_usage"

// Google Cloud project IDs: 6 to 30 lowercase letters, digits and hyphens,
// starting with a letter and not ending with a hyphen. Domain-scoped legacy
// IDs are not accepted.
var vertexProjectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// Vertex AI regions such as us-central1, europe-west4 or
// northamerica-northeast1. The location becomes part of the request host, so
// nothing else is accepted.
var vertexRegionPattern = regexp.MustCompile(`^[a-z]{2,15}(-[a-z]{2,15}){1,2}[0-9]{1,2}$`)

type vertexSettings struct {
	project  string
	location string
}

// ValidateVertexSettings checks a Vertex AI project and location (empty means
// VertexDefaultLocation) and returns them normalized.
func ValidateVertexSettings(project, location string) (string, string, error) {
	project = strings.TrimSpace(project)
	location = strings.ToLower(strings.TrimSpace(location))
	if project == "" {
		return "", "", errors.New("vertex requires a Google Cloud project (google_project)")
	}
	if !vertexProjectPattern.MatchString(project) {
		return "", "", fmt.Errorf("vertex google_project %q is not a Google Cloud project ID (6-30 lowercase letters, digits and hyphens)", project)
	}
	if location == "" {
		location = VertexDefaultLocation
	}
	if location != VertexDefaultLocation && !vertexRegionPattern.MatchString(location) {
		return "", "", fmt.Errorf("vertex google_location %q must be %q or a region such as us-central1", location, VertexDefaultLocation)
	}
	return project, location, nil
}

func vertexSettingsFromRecord(record pebblestore.AuthCredentialRecord) (vertexSettings, error) {
	if !strings.EqualFold(strings.TrimSpace(record.Type), pebblestore.AuthTypeADC) {
		return vertexSettings{}, fmt.Errorf("vertex credentials must use type %q (machine identity), found %q", pebblestore.AuthTypeADC, record.Type)
	}
	project, location, err := ValidateVertexSettings(record.GoogleProject, record.GoogleLocation)
	if err != nil {
		return vertexSettings{}, err
	}
	return vertexSettings{project: project, location: location}, nil
}

func activeVertexSettings(ctx context.Context, authStore *pebblestore.AuthStore) (vertexSettings, error) {
	if authStore == nil {
		return vertexSettings{}, errors.New("vertex auth store is not configured")
	}
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok {
		return vertexSettings{}, identity.ErrPrincipalRequired
	}
	record, found, err := authStore.GetActiveCredentialForAccount(principal.AccountScopeID, VertexProviderID)
	if err != nil {
		return vertexSettings{}, fmt.Errorf("read vertex settings: %w", err)
	}
	if !found {
		return vertexSettings{}, errors.New("vertex is not set up for this account (swarmctl setup credential --provider vertex --project PROJECT)")
	}
	return vertexSettingsFromRecord(record)
}

// vertexEndpoint builds
// https://{host}/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:{method}
// where host is aiplatform.googleapis.com for "global" and
// {region}-aiplatform.googleapis.com for a region.
func vertexEndpoint(settings vertexSettings, modelID, method string) (string, error) {
	switch method {
	case "generateContent", "streamGenerateContent":
	default:
		return "", fmt.Errorf("unsupported vertex method %q", method)
	}
	project, location, err := ValidateVertexSettings(settings.project, settings.location)
	if err != nil {
		return "", err
	}
	host := "aiplatform.googleapis.com"
	if location != VertexDefaultLocation {
		host = location + "-aiplatform.googleapis.com"
	}
	return "https://" + host + "/v1/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(location) +
		"/publishers/google/models/" + url.PathEscape(modelID) + ":" + method, nil
}

func vertexSurface(settings vertexSettings, credentials *ADCCredentials) generateContentSurface {
	return generateContentSurface{
		providerID:  VertexProviderID,
		usageSource: vertexUsageSource,
		endpoint: func(modelID, method string) (string, error) {
			return vertexEndpoint(settings, modelID, method)
		},
		authorize: func(ctx context.Context, req *http.Request) error {
			token, err := credentials.Token(ctx)
			if err != nil {
				return fmt.Errorf("vertex: Google credentials unavailable: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		},
		refreshAuth: credentials.Invalidate,
		statusDetail: func(statusCode int) string {
			return vertexStatusDetail(settings, statusCode)
		},
	}
}

func vertexStatusDetail(settings vertexSettings, statusCode int) string {
	switch statusCode {
	case http.StatusUnauthorized:
		return "the access token was rejected"
	case http.StatusForbidden:
		return fmt.Sprintf("permission denied: the machine's service account needs roles/aiplatform.user on project %s, the Vertex AI API must be enabled there, and a VM needs the cloud-platform access scope", settings.project)
	case http.StatusNotFound:
		return fmt.Sprintf("not found: check that Vertex AI serves this model in location %s", settings.location)
	case http.StatusTooManyRequests:
		return "rate limited or out of quota"
	}
	return ""
}

func vertexAuthMethods() []provideriface.AuthMethod {
	return []provideriface.AuthMethod{{
		ID:             pebblestore.AuthTypeADC,
		Label:          "Machine identity (Vertex AI)",
		CredentialType: pebblestore.AuthTypeADC,
		Description:    "Use this machine's Google identity: the service account attached to the VM (GCE metadata server) or a key file named by GOOGLE_APPLICATION_CREDENTIALS. Needs roles/aiplatform.user on the project. Swarm stores only the project and location.",
	}}
}

// VertexAdapter reports Vertex AI readiness for the calling account. Each
// account opts in with an active adc credential naming its project and
// location; the provider is ready only after a token fetch succeeds.
type VertexAdapter struct {
	authStore   *pebblestore.AuthStore
	credentials *ADCCredentials
}

func NewVertexAdapter(authStore *pebblestore.AuthStore, credentials *ADCCredentials) *VertexAdapter {
	return &VertexAdapter{authStore: authStore, credentials: credentials}
}

func (a *VertexAdapter) ID() string {
	return VertexProviderID
}

func (a *VertexAdapter) Status(ctx context.Context) (provideriface.Status, error) {
	providerDefaults := defaults.MustLookup(VertexProviderID)
	status := provideriface.Status{
		ID:              VertexProviderID,
		DefaultModel:    providerDefaults.PrimaryModel,
		DefaultThinking: providerDefaults.PrimaryThinking,
		AuthMethods:     vertexAuthMethods(),
	}
	if a == nil || a.authStore == nil {
		status.Reason = "vertex auth store is not configured"
		return status, nil
	}
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok {
		status.Reason = "product identity is required"
		return status, nil
	}
	record, found, err := a.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, VertexProviderID)
	if err != nil {
		return provideriface.Status{}, err
	}
	if !found {
		status.Reason = "missing vertex settings (swarmctl setup credential --provider vertex --project PROJECT)"
		return status, nil
	}
	if _, err := vertexSettingsFromRecord(record); err != nil {
		status.Reason = err.Error()
		return status, nil
	}
	if _, err := a.credentials.Token(ctx); err != nil {
		status.Reason = "Google credentials unavailable: " + err.Error()
		return status, nil
	}
	status.Ready = true
	return status, nil
}

// VerifyCredential checks the project and location and fetches an access
// token. It does not call Vertex AI: permission problems surface on the first
// request with an explanation (see vertexStatusDetail).
func (a *VertexAdapter) VerifyCredential(ctx context.Context, credential provideriface.AuthCredential) (provideriface.AuthVerification, error) {
	failed := provideriface.AuthVerification{Connected: false, Method: pebblestore.AuthTypeADC}
	if !strings.EqualFold(strings.TrimSpace(credential.Type), pebblestore.AuthTypeADC) {
		return failed, fmt.Errorf("vertex supports only the %q credential type (machine identity); it takes no API key", pebblestore.AuthTypeADC)
	}
	if strings.TrimSpace(credential.APIKey) != "" || strings.TrimSpace(credential.AccessToken) != "" || strings.TrimSpace(credential.RefreshToken) != "" {
		return failed, errors.New("vertex credentials carry no secret; remove api_key, access_token and refresh_token")
	}
	project, location, err := ValidateVertexSettings(credential.GoogleProject, credential.GoogleLocation)
	if err != nil {
		return failed, err
	}
	if a == nil || a.credentials == nil {
		return failed, errors.New("google application default credentials are not configured")
	}
	if _, err := a.credentials.Token(ctx); err != nil {
		failed.Message = "Google credentials unavailable: " + err.Error()
		return failed, errors.New(failed.Message)
	}
	return provideriface.AuthVerification{
		Connected: true,
		Method:    pebblestore.AuthTypeADC,
		Message:   fmt.Sprintf("access token obtained from the %s; Vertex AI project %s, location %s", a.credentials.Source(), project, location),
	}, nil
}

// VertexRunner sends Gemini generateContent requests to Vertex AI.
type VertexRunner struct {
	authStore   *pebblestore.AuthStore
	credentials *ADCCredentials
	generateContentTransport
}

func NewVertexRunner(authStore *pebblestore.AuthStore, credentials *ADCCredentials) *VertexRunner {
	return &VertexRunner{
		authStore:                authStore,
		credentials:              credentials,
		generateContentTransport: generateContentTransport{httpClient: &http.Client{Timeout: 10 * time.Minute}},
	}
}

func (r *VertexRunner) ID() string {
	return VertexProviderID
}

func (r *VertexRunner) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{
		ContextMode: provideriface.ExecutionEpochContextStatelessFullInput,
	}
}

func (r *VertexRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	surface, modelID, requestPayload, err := r.prepare(ctx, req)
	if err != nil {
		return provideriface.Response{}, err
	}
	return r.generate(ctx, surface, modelID, requestPayload)
}

func (r *VertexRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	surface, modelID, requestPayload, err := r.prepare(ctx, req)
	if err != nil {
		return provideriface.Response{}, err
	}
	return r.stream(ctx, surface, modelID, requestPayload, onEvent)
}

// prepare resolves the account's project and location and builds the request.
// Vertex has no Gemini API service_tier field, and session media is not
// declared for this surface yet, so either is refused rather than dropped.
// The run attaches a media contract to every request; only media actually in
// the conversation is refused.
func (r *VertexRunner) prepare(ctx context.Context, req provideriface.Request) (generateContentSurface, string, googleRequest, error) {
	if r == nil || r.credentials == nil {
		return generateContentSurface{}, "", googleRequest{}, errors.New("vertex runner credentials are not configured")
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		return generateContentSurface{}, "", googleRequest{}, errors.New("model is required")
	}
	settings, err := activeVertexSettings(ctx, r.authStore)
	if err != nil {
		return generateContentSurface{}, "", googleRequest{}, err
	}
	if _, media, err := buildGoogleContentsWithMedia(req); err != nil {
		return generateContentSurface{}, "", googleRequest{}, err
	} else if len(media) > 0 {
		return generateContentSurface{}, "", googleRequest{}, errors.New("vertex does not accept session media yet")
	}
	requestPayload, err := buildGoogleRequest(req)
	if err != nil {
		return generateContentSurface{}, "", googleRequest{}, err
	}
	if requestPayload.ServiceTier != "" {
		return generateContentSurface{}, "", googleRequest{}, fmt.Errorf("vertex does not support service tier %q", requestPayload.ServiceTier)
	}
	return vertexSurface(settings, r.credentials), modelID, requestPayload, nil
}
