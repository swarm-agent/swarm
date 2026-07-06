package openai

import (
	"context"
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

func (r *Runner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	if r == nil || r.client == nil {
		return provideriface.Response{}, errors.New("openai runner client is not configured")
	}
	record, err := r.openAIAuthRecord(ctx)
	if err != nil {
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
	out, err := r.client.CreateResponseStreamingWithAuth(ctx, record, codex.ToRequest(req), codex.ToProviderStreamEventCallback(onEvent))
	if err != nil {
		return provideriface.Response{}, err
	}
	return codex.FromResponse(out), nil
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
