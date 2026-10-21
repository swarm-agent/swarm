package run

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type artifactMediaTestRunner struct{ maxBytes int64 }

func (*artifactMediaTestRunner) ID() string { return "codex" }
func (*artifactMediaTestRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}
func (*artifactMediaTestRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}
func (r *artifactMediaTestRunner) MediaCapabilityDeclaration(context.Context) (provideriface.MediaAdapterDeclaration, error) {
	maxBytes := r.maxBytes
	if maxBytes <= 0 {
		maxBytes = pebblestore.SessionMediaDefaultMaxBytes
	}
	return provideriface.MediaAdapterDeclaration{
		AdapterID: provideriface.MediaAdapterIDCodexChatGPTV1, ProviderID: "codex", ProviderSurface: provideriface.MediaProviderSurfaceCodexChatGPT,
		CredentialSurface: provideriface.MediaCredentialSurfaceCodexOAuth, CredentialFingerprint: "artifact-media-test-credential",
		Inputs: []provideriface.MediaAdapterCapability{{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: maxBytes, MaxCount: 1}},
	}, nil
}
