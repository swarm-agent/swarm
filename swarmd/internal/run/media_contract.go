package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const SessionMediaContractVersion = 1

type conversationalMediaSurface struct {
	adapterID         string
	providerSurface   string
	credentialSurface string
}

// conversationalMediaProviders is the closed registry shared by declaration
// resolution and contract compilation. Provider adapters still must declare the
// exact registered surface; membership alone never grants a capability.
var conversationalMediaProviders = map[string]conversationalMediaSurface{
	"anthropic":  {provideriface.MediaAdapterIDAnthropicMessagesV1, provideriface.MediaProviderSurfaceAnthropicMessages, provideriface.MediaCredentialSurfaceAnthropicAPIKey},
	"codex":      {provideriface.MediaAdapterIDCodexChatGPTV1, provideriface.MediaProviderSurfaceCodexChatGPT, provideriface.MediaCredentialSurfaceCodexOAuth},
	"fireworks":  {provideriface.MediaAdapterIDFireworksChatCompletionsV1, provideriface.MediaProviderSurfaceFireworksChatCompletions, provideriface.MediaCredentialSurfaceFireworksAPIKey},
	"google":     {provideriface.MediaAdapterIDGoogleGenerateContentV1, provideriface.MediaProviderSurfaceGoogleGenerateContent, provideriface.MediaCredentialSurfaceGoogleAPIKey},
	"openai":     {provideriface.MediaAdapterIDOpenAIResponsesV1, provideriface.MediaProviderSurfaceOpenAIResponses, provideriface.MediaCredentialSurfaceOpenAIAPIKey},
	"openrouter": {provideriface.MediaAdapterIDOpenRouterChatCompletionsV1, provideriface.MediaProviderSurfaceOpenRouterChatCompletions, provideriface.MediaCredentialSurfaceOpenRouterAPIKey},
}

// SessionMediaProviderEnabled reports membership in the closed reviewed registry.
// A provider still needs matching catalog facts and an exact adapter declaration.
func SessionMediaProviderEnabled(providerID string) bool {
	_, ok := conversationalMediaProviders[strings.ToLower(strings.TrimSpace(providerID))]
	return ok
}

func conversationalMediaSurfaceMatches(providerID string, declaration provideriface.MediaAdapterDeclaration) bool {
	surface, ok := conversationalMediaProviders[strings.ToLower(strings.TrimSpace(providerID))]
	return ok && strings.TrimSpace(declaration.AdapterID) == surface.adapterID &&
		strings.TrimSpace(declaration.ProviderSurface) == surface.providerSurface &&
		strings.TrimSpace(declaration.CredentialSurface) == surface.credentialSurface
}

// SessionMediaContractInput contains only backend-resolved facts. Credential
// material is deliberately absent; adapters expose a non-secret fingerprint.
type SessionMediaContractInput struct {
	ProviderID      string
	Model           string
	Catalog         *pebblestore.ModelCatalogRecord
	CatalogMeta     *pebblestore.ModelCatalogMeta
	Adapter         provideriface.MediaAdapterDeclaration
	AgentAuthorized bool
	ExecutionMode   string
	WorkspaceScope  string
	SessionScope    string
}

func ResolveMediaAdapterDeclaration(ctx context.Context, providerID string, runner provideriface.Runner) provideriface.MediaAdapterDeclaration {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if !SessionMediaProviderEnabled(providerID) {
		return provideriface.MediaAdapterDeclaration{}
	}
	declared, ok := runner.(provideriface.MediaCapabilityRunner)
	if !ok || declared == nil {
		return provideriface.MediaAdapterDeclaration{}
	}
	declaration, err := declared.MediaCapabilityDeclaration(ctx)
	if err != nil || !strings.EqualFold(strings.TrimSpace(declaration.ProviderID), providerID) || !conversationalMediaSurfaceMatches(providerID, declaration) {
		return provideriface.MediaAdapterDeclaration{}
	}
	return declaration
}

// CompileSessionMediaContract intersects every required authority. Any absent,
// unknown, contradictory, or cross-surface fact produces a deterministic empty
// admission contract with diagnostic reasons.
func CompileSessionMediaContract(input SessionMediaContractInput) provideriface.SessionMediaContract {
	contract := provideriface.SessionMediaContract{
		Version:               SessionMediaContractVersion,
		ProviderID:            strings.ToLower(strings.TrimSpace(input.ProviderID)),
		Model:                 strings.TrimSpace(input.Model),
		ProviderSurface:       strings.TrimSpace(input.Adapter.ProviderSurface),
		CredentialSurface:     strings.TrimSpace(input.Adapter.CredentialSurface),
		CredentialFingerprint: strings.TrimSpace(input.Adapter.CredentialFingerprint),
		AdapterID:             strings.TrimSpace(input.Adapter.AdapterID),
		ExecutionMode:         strings.ToLower(strings.TrimSpace(input.ExecutionMode)),
		WorkspaceScope:        strings.TrimSpace(input.WorkspaceScope),
		SessionScope:          strings.TrimSpace(input.SessionScope),
	}
	if input.Catalog != nil {
		contract.SnapshotID = strings.TrimSpace(input.Catalog.SourceSnapshotID)
		contract.SnapshotVersion = strings.TrimSpace(input.Catalog.SourceSnapshotVersion)
		contract.SnapshotSource = strings.TrimSpace(input.Catalog.Source)
	}

	deny := func(reason string) {
		reason = strings.TrimSpace(reason)
		if reason != "" {
			contract.DenialReasons = append(contract.DenialReasons, reason)
		}
	}
	if !SessionMediaProviderEnabled(contract.ProviderID) {
		deny("provider has no reviewed conversational media surface")
	}
	if contract.Model == "" {
		deny("resolved model is missing")
	}
	if input.Catalog == nil || input.Catalog.Media == nil {
		deny("catalog media facts are unavailable")
	} else {
		if !strings.EqualFold(strings.TrimSpace(input.Catalog.Provider), contract.ProviderID) || strings.TrimSpace(input.Catalog.Model) != contract.Model {
			deny("catalog provider/model does not match the resolved run")
		}
		if input.Catalog.Media.State != pebblestore.ModelCatalogMediaStateSupported {
			deny("catalog media state is not supported")
		}
		if !strings.EqualFold(strings.TrimSpace(input.Catalog.Media.ProviderSurface), contract.ProviderSurface) {
			deny("catalog and adapter provider surfaces do not match")
		}
		if !strings.EqualFold(strings.TrimSpace(input.Catalog.Media.CredentialSurface), contract.CredentialSurface) {
			deny("catalog and adapter credential surfaces do not match")
		}
	}
	if input.CatalogMeta == nil || contract.SnapshotID == "" || contract.SnapshotVersion == "" {
		deny("catalog snapshot provenance is unavailable")
	} else if strings.TrimSpace(input.CatalogMeta.SnapshotID) != contract.SnapshotID || strings.TrimSpace(input.CatalogMeta.SnapshotVersion) != contract.SnapshotVersion {
		deny("catalog record and snapshot provenance do not match")
	}
	if !strings.EqualFold(strings.TrimSpace(input.Adapter.ProviderID), contract.ProviderID) || contract.AdapterID == "" {
		deny("adapter does not implement the resolved provider")
	} else if !conversationalMediaSurfaceMatches(contract.ProviderID, input.Adapter) {
		deny("adapter does not match the reviewed provider media surface")
	}
	if contract.CredentialFingerprint == "" {
		deny("active credential surface is unresolved")
	}
	if !input.AgentAuthorized {
		deny("effective agent tool contract denies media inspection")
	}
	if contract.ExecutionMode != "plan" && contract.ExecutionMode != "auto" {
		deny("execution mode does not admit conversational media")
	}
	if contract.WorkspaceScope == "" || contract.SessionScope == "" {
		deny("workspace or session scope is unresolved")
	}

	catalogInputs := map[string]pebblestore.ModelCatalogMediaDirection{}
	if input.Catalog != nil && input.Catalog.Media != nil {
		for _, capability := range input.Catalog.Media.Inputs {
			catalogInputs[strings.ToLower(strings.TrimSpace(capability.Modality))] = capability
		}
	}
	adapterInputs := map[string]provideriface.MediaAdapterCapability{}
	for _, capability := range input.Adapter.Inputs {
		adapterInputs[strings.ToLower(strings.TrimSpace(capability.Modality))] = capability
	}
	modalities := make(map[string]struct{}, len(catalogInputs)+len(adapterInputs))
	for modality := range catalogInputs {
		modalities[modality] = struct{}{}
	}
	for modality := range adapterInputs {
		modalities[modality] = struct{}{}
	}
	for modality := range modalities {
		catalogCapability, catalogOK := catalogInputs[modality]
		adapterCapability, adapterOK := adapterInputs[modality]
		capability := provideriface.MediaContractCapability{Modality: modality, State: provideriface.MediaCapabilityStateDenied}
		switch {
		case len(contract.DenialReasons) > 0:
			capability.Reason = "run media contract prerequisites are denied"
		case !catalogOK || catalogCapability.State != pebblestore.ModelCatalogMediaStateSupported:
			capability.Reason = "catalog does not affirm support"
		case !adapterOK:
			capability.Reason = "adapter transport is not implemented"
		case !strings.EqualFold(strings.TrimSpace(catalogCapability.Semantics), strings.TrimSpace(adapterCapability.Semantics)):
			capability.Reason = "catalog and adapter processing semantics do not match"
		default:
			capability.MIMETypes = intersectNormalizedStrings(catalogCapability.MIMETypes, adapterCapability.MIMETypes)
			capability.FileTypes = intersectNormalizedStrings(catalogCapability.FileTypes, adapterCapability.FileTypes)
			capability.ContentTypes = normalizeMediaStrings(adapterCapability.ContentTypes)
			if len(catalogCapability.MIMETypes) > 0 && len(capability.MIMETypes) == 0 || len(catalogCapability.FileTypes) > 0 && len(capability.FileTypes) == 0 || len(capability.ContentTypes) == 0 {
				capability.Reason = "catalog and adapter have no exact transport type intersection"
			} else {
				capability.State = provideriface.MediaCapabilityStateAllowed
				capability.Reason = "all contract layers affirm support"
				capability.Semantics = strings.TrimSpace(adapterCapability.Semantics)
				capability.MaxBytes = adapterCapability.MaxBytes
				capability.MaxCount = adapterCapability.MaxCount
				capability.Provenance = normalizeMediaStrings(append([]string{"catalog:" + contract.SnapshotID, "adapter:" + contract.AdapterID, "agent:authorized", "mode:" + contract.ExecutionMode}, input.Catalog.Media.SourceIDs...))
			}
		}
		contract.Capabilities = append(contract.Capabilities, capability)
	}
	normalizeSessionMediaContract(&contract)
	contract.Hash = hashSessionMediaContract(contract)
	return contract
}

func AgentProfileAuthorizesMedia(profile pebblestore.AgentProfile) bool {
	const toolName = mediaInspectToolName
	if profile.ToolContract != nil {
		if config, ok := profile.ToolContract.Tools[toolName]; ok && config.Enabled != nil {
			return *config.Enabled
		}
		// Media is a dynamic privileged capability. Presets and unrelated tools do
		// not imply authorization; saved/custom and utility profiles must opt in.
		return false
	}
	if profile.ToolScope != nil {
		for _, denied := range profile.ToolScope.DenyTools {
			if strings.EqualFold(strings.TrimSpace(denied), toolName) {
				return false
			}
		}
		if len(profile.ToolScope.AllowTools) > 0 {
			for _, allowed := range profile.ToolScope.AllowTools {
				if strings.EqualFold(strings.TrimSpace(allowed), toolName) {
					return true
				}
			}
			return false
		}
	}
	return false
}

func SessionMediaContractAllows(contract provideriface.SessionMediaContract, modality, mimeType, fileType string) bool {
	for _, capability := range contract.Capabilities {
		if capability.State != provideriface.MediaCapabilityStateAllowed || capability.Modality != strings.ToLower(strings.TrimSpace(modality)) {
			continue
		}
		if value := strings.ToLower(strings.TrimSpace(mimeType)); value != "" && !containsMediaString(capability.MIMETypes, value) {
			return false
		}
		if value := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(fileType), ".")); value != "" && len(capability.FileTypes) > 0 && !containsMediaString(capability.FileTypes, value) {
			return false
		}
		return true
	}
	return false
}

func normalizeSessionMediaContract(contract *provideriface.SessionMediaContract) {
	contract.DenialReasons = normalizeMediaStrings(contract.DenialReasons)
	for i := range contract.Capabilities {
		capability := &contract.Capabilities[i]
		capability.Modality = strings.ToLower(strings.TrimSpace(capability.Modality))
		capability.MIMETypes = normalizeMediaStrings(capability.MIMETypes)
		capability.FileTypes = normalizeMediaStrings(capability.FileTypes)
		capability.ContentTypes = normalizeMediaStrings(capability.ContentTypes)
		capability.Provenance = normalizeMediaStrings(capability.Provenance)
	}
	sort.Slice(contract.Capabilities, func(i, j int) bool { return contract.Capabilities[i].Modality < contract.Capabilities[j].Modality })
}

func hashSessionMediaContract(contract provideriface.SessionMediaContract) string {
	contract.Hash = ""
	payload, _ := json.Marshal(contract)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func intersectNormalizedStrings(left, right []string) []string {
	if len(left) == 0 {
		return normalizeMediaStrings(right)
	}
	if len(right) == 0 {
		return normalizeMediaStrings(left)
	}
	set := make(map[string]struct{}, len(right))
	for _, value := range normalizeMediaStrings(right) {
		set[value] = struct{}{}
	}
	out := make([]string, 0)
	for _, value := range normalizeMediaStrings(left) {
		if _, ok := set[value]; ok {
			out = append(out, value)
		}
	}
	return out
}

func normalizeMediaStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "."))
		if value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func containsMediaString(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), expected) {
			return true
		}
	}
	return false
}
