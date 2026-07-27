package openai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type Runner struct {
	authStore *pebblestore.AuthStore
	client    *codex.Client
}

func NewRunner(authStore *pebblestore.AuthStore, client *codex.Client) *Runner {
	if client == nil {
		client = codex.NewClient(authStore)
	}
	return &Runner{authStore: authStore, client: client}
}

func (r *Runner) ID() string {
	return "openai"
}

func (r *Runner) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{
		ContextMode:                provideriface.ExecutionEpochContextResponsesChain,
		EpochScopedCacheKey:        true,
		EpochScopedSessionAffinity: true,
		TransportReusable:          true,
	}
}

func (r *Runner) MediaCapabilityDeclaration(ctx context.Context) (provideriface.MediaAdapterDeclaration, error) {
	record, err := r.openAIAuthRecord(ctx)
	if err != nil {
		return provideriface.MediaAdapterDeclaration{}, err
	}
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{record.AccountScopeID, record.Provider, record.ID, record.Type}, "\x00")))
	return provideriface.MediaAdapterDeclaration{
		AdapterID:             provideriface.MediaAdapterIDOpenAIResponsesV1,
		ProviderID:            "openai",
		ProviderSurface:       provideriface.MediaProviderSurfaceOpenAIResponses,
		CredentialSurface:     provideriface.MediaCredentialSurfaceOpenAIAPIKey,
		CredentialFingerprint: hex.EncodeToString(fingerprint[:16]),
		Inputs: []provideriface.MediaAdapterCapability{
			{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/gif", "image/jpeg", "image/png", "image/webp"}, ContentTypes: []string{"input_image"}, MaxBytes: 20 << 20, MaxCount: 20},
			// PDF is the first exact document transport. Generic office/text file
			// categories remain undeclared until immutable assets preserve a safe,
			// exact filename/extension contract for those formats.
			{Modality: "pdf", Semantics: pebblestore.ModelCatalogMediaSemanticsProviderProcessed, MIMETypes: []string{"application/pdf"}, FileTypes: []string{"pdf"}, ContentTypes: []string{"input_file"}, MaxBytes: 20 << 20, MaxCount: 8},
		},
	}, nil
}

func (r *Runner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	if r == nil || r.client == nil {
		return provideriface.Response{}, errors.New("openai runner client is not configured")
	}
	record, err := r.openAIAuthRecord(ctx)
	if err != nil {
		return provideriface.Response{}, err
	}
	if err := validateOpenAIMediaSurface(req.MediaContract); err != nil {
		return provideriface.Response{}, err
	}
	out, err := r.client.CreateResponseWithAuth(ctx, record, codex.ToRequest(req))
	if err != nil {
		return provideriface.Response{}, err
	}
	return codex.FromResponse(out), nil
}

func (r *Runner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r == nil || r.client == nil {
		return provideriface.Response{}, errors.New("openai runner client is not configured")
	}
	record, err := r.openAIAuthRecord(ctx)
	if err != nil {
		return provideriface.Response{}, err
	}
	if err := validateOpenAIMediaSurface(req.MediaContract); err != nil {
		return provideriface.Response{}, err
	}
	out, err := r.client.CreateResponseStreamingWithAuth(ctx, record, codex.ToRequest(req), codex.ToProviderStreamEventCallback(onEvent))
	if err != nil {
		return provideriface.Response{}, err
	}
	return codex.FromResponse(out), nil
}

func validateOpenAIMediaSurface(contract provideriface.SessionMediaContract) error {
	if strings.TrimSpace(contract.Hash) == "" {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(contract.ProviderID), "openai") || contract.ProviderSurface != provideriface.MediaProviderSurfaceOpenAIResponses || contract.CredentialSurface != provideriface.MediaCredentialSurfaceOpenAIAPIKey || contract.AdapterID != provideriface.MediaAdapterIDOpenAIResponsesV1 {
		return errors.New("media contract does not match the active OpenAI API-key Responses surface")
	}
	return nil
}

func (r *Runner) openAIAuthRecord(ctx context.Context) (pebblestore.AuthCredentialRecord, error) {
	principal, principalOK := identity.PrincipalFromContext(ctx)
	if !principalOK || !principal.Valid() {
		return pebblestore.AuthCredentialRecord{}, identity.ErrPrincipalRequired
	}
	if r == nil || r.authStore == nil {
		return pebblestore.AuthCredentialRecord{}, errors.New("openai runner auth store is not configured")
	}
	record, ok, err := r.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "openai")
	if err != nil {
		return pebblestore.AuthCredentialRecord{}, err
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return pebblestore.AuthCredentialRecord{}, errors.New("openai api key is not configured")
	}
	record.Provider = "openai"
	record.Type = pebblestore.AuthTypeAPI
	return record, nil
}
