package run

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// PreSessionMediaStagedMetadata is the immutable, byte-derived metadata that a
// staging service supplies after routing has resolved a model. It deliberately
// contains no provider, model, session, or contract selection.
type PreSessionMediaStagedMetadata struct {
	StagingID        string `json:"staging_id"`
	AccountScopeID   string `json:"account_scope_id"`
	Modality         string `json:"modality"`
	DeclaredMIMEType string `json:"declared_mime_type"`
	DetectedMIMEType string `json:"detected_mime_type"`
	FileType         string `json:"file_type,omitempty"`
	Size             int64  `json:"size"`
	DigestSHA256     string `json:"digest_sha256"`
}

// PreSessionMediaBindingInput joins account-scoped staged metadata to an
// already resolved effective-model contract. This helper never resolves a
// model or creates/persists a session.
type PreSessionMediaBindingInput struct {
	AccountScopeID string
	SessionID      string
	WorkspaceScope string
	Contract       provideriface.SessionMediaContract
	Staged         []PreSessionMediaStagedMetadata
}

// PreSessionMediaBinding is a canonical, persistence-ready description of one
// staged item. Reference is suitable for a durable V3 user message after the
// staging service atomically materializes the corresponding session asset.
type PreSessionMediaBinding struct {
	StagingID    string                            `json:"staging_id"`
	AssetID      string                            `json:"asset_id"`
	Metadata     PreSessionMediaStagedMetadata     `json:"metadata"`
	Semantics    string                            `json:"semantics"`
	ContentTypes []string                          `json:"content_types"`
	Provenance   []string                          `json:"provenance"`
	Reference    pebblestore.SessionMediaReference `json:"reference"`
}

// PreSessionMediaBindingPlan captures the immutable resolved-model facts that
// the eventual routed transaction must bind alongside all prepared items.
type PreSessionMediaBindingPlan struct {
	AccountScopeID        string                   `json:"account_scope_id"`
	SessionID             string                   `json:"session_id"`
	WorkspaceScope        string                   `json:"workspace_scope"`
	ProviderID            string                   `json:"provider_id"`
	Model                 string                   `json:"model"`
	ProviderSurface       string                   `json:"provider_surface"`
	CredentialSurface     string                   `json:"credential_surface"`
	CredentialFingerprint string                   `json:"credential_fingerprint"`
	AdapterID             string                   `json:"adapter_id"`
	SnapshotID            string                   `json:"snapshot_id"`
	SnapshotVersion       string                   `json:"snapshot_version"`
	SnapshotSource        string                   `json:"snapshot_source"`
	ExecutionMode         string                   `json:"execution_mode"`
	ContractVersion       int                      `json:"contract_version"`
	ContractHash          string                   `json:"contract_hash"`
	TotalBytes            int64                    `json:"total_bytes"`
	Bindings              []PreSessionMediaBinding `json:"bindings"`
}

// PreparePreSessionMediaBindings validates staged immutable metadata against
// the resolved effective-model media contract and prepares canonical binding
// facts. It fails closed on non-canonical/stale contracts, unsupported types,
// unsafe semantics, ownership mismatch, or any item/aggregate limit breach.
func PreparePreSessionMediaBindings(input PreSessionMediaBindingInput) (PreSessionMediaBindingPlan, error) {
	accountScopeID := strings.TrimSpace(input.AccountScopeID)
	sessionID := strings.TrimSpace(input.SessionID)
	workspaceScope := strings.TrimSpace(input.WorkspaceScope)
	if accountScopeID == "" || sessionID == "" || workspaceScope == "" {
		return PreSessionMediaBindingPlan{}, errors.New("pre-session media binding requires account, session, and workspace scope")
	}
	if len(input.Staged) == 0 {
		return PreSessionMediaBindingPlan{}, errors.New("pre-session media binding requires at least one staged item")
	}
	if len(input.Staged) > pebblestore.SessionMediaDefaultMaxCount {
		return PreSessionMediaBindingPlan{}, errors.New("pre-session media binding count limit exceeded")
	}

	contract := input.Contract
	if err := validatePreSessionMediaContract(contract, sessionID, workspaceScope); err != nil {
		return PreSessionMediaBindingPlan{}, err
	}
	capabilities, err := preSessionAllowedMediaCapabilities(contract)
	if err != nil {
		return PreSessionMediaBindingPlan{}, err
	}

	plan := PreSessionMediaBindingPlan{
		AccountScopeID: accountScopeID, SessionID: sessionID, WorkspaceScope: workspaceScope,
		ProviderID: contract.ProviderID, Model: contract.Model, ProviderSurface: contract.ProviderSurface,
		CredentialSurface: contract.CredentialSurface, CredentialFingerprint: contract.CredentialFingerprint,
		AdapterID: contract.AdapterID, SnapshotID: contract.SnapshotID, SnapshotVersion: contract.SnapshotVersion,
		SnapshotSource: contract.SnapshotSource, ExecutionMode: contract.ExecutionMode,
		ContractVersion: contract.Version, ContractHash: contract.Hash,
		Bindings: make([]PreSessionMediaBinding, 0, len(input.Staged)),
	}
	seenStagingIDs := make(map[string]struct{}, len(input.Staged))
	countsByModality := make(map[string]int)
	for index, raw := range input.Staged {
		staged, err := normalizePreSessionStagedMetadata(raw)
		if err != nil {
			return PreSessionMediaBindingPlan{}, fmt.Errorf("staged media item %d: %w", index, err)
		}
		if staged.AccountScopeID != accountScopeID {
			return PreSessionMediaBindingPlan{}, fmt.Errorf("staged media item %d is outside the authenticated account scope", index)
		}
		if _, duplicate := seenStagingIDs[staged.StagingID]; duplicate {
			return PreSessionMediaBindingPlan{}, fmt.Errorf("staged media item %q appears more than once", staged.StagingID)
		}
		seenStagingIDs[staged.StagingID] = struct{}{}

		capability, ok := capabilities[staged.Modality]
		if !ok || !SessionMediaContractAllows(contract, staged.Modality, staged.DetectedMIMEType, staged.FileType) {
			return PreSessionMediaBindingPlan{}, fmt.Errorf("staged media item %d is not admitted by the resolved model contract", index)
		}
		if capability.Semantics != pebblestore.ModelCatalogMediaSemanticsNative && capability.Semantics != pebblestore.ModelCatalogMediaSemanticsProviderProcessed {
			return PreSessionMediaBindingPlan{}, fmt.Errorf("staged media item %d uses denied processing semantics %q", index, capability.Semantics)
		}
		if capability.MaxBytes <= 0 || staged.Size > capability.MaxBytes {
			return PreSessionMediaBindingPlan{}, fmt.Errorf("staged media item %d exceeds the resolved model per-item byte limit", index)
		}
		countsByModality[staged.Modality]++
		if capability.MaxCount <= 0 || countsByModality[staged.Modality] > capability.MaxCount {
			return PreSessionMediaBindingPlan{}, fmt.Errorf("staged media modality %q exceeds the resolved model count limit", staged.Modality)
		}
		if staged.Size > pebblestore.SessionMediaDefaultMaxBytes {
			return PreSessionMediaBindingPlan{}, fmt.Errorf("staged media item %d exceeds the daemon per-item byte limit", index)
		}
		if plan.TotalBytes > pebblestore.SessionMediaDefaultQuotaBytes-staged.Size {
			return PreSessionMediaBindingPlan{}, errors.New("pre-session media binding total byte limit exceeded")
		}
		plan.TotalBytes += staged.Size

		assetID := preSessionMediaAssetID(staged.DigestSHA256, contract.Hash)
		plan.Bindings = append(plan.Bindings, PreSessionMediaBinding{
			StagingID: staged.StagingID, AssetID: assetID, Metadata: staged,
			Semantics: capability.Semantics, ContentTypes: append([]string(nil), capability.ContentTypes...),
			Provenance: append([]string(nil), capability.Provenance...),
			Reference: pebblestore.SessionMediaReference{
				AssetID: assetID, Modality: staged.Modality, MIMEType: staged.DetectedMIMEType,
				FileType: staged.FileType, Size: staged.Size, DigestSHA256: staged.DigestSHA256,
				ContractHash: contract.Hash,
			},
		})
	}
	return plan, nil
}

func validatePreSessionMediaContract(contract provideriface.SessionMediaContract, sessionID, workspaceScope string) error {
	if contract.Version != SessionMediaContractVersion || strings.TrimSpace(contract.Hash) == "" {
		return errors.New("resolved media contract is empty or has an unsupported version")
	}
	if contract.Hash != hashSessionMediaContract(contract) {
		return errors.New("resolved media contract is stale, non-canonical, or has been modified")
	}
	providerID := strings.ToLower(strings.TrimSpace(contract.ProviderID))
	surface, reviewed := conversationalMediaProviders[providerID]
	if !reviewed {
		return errors.New("resolved media contract provider has no reviewed conversational media surface")
	}
	if strings.TrimSpace(contract.Model) == "" || strings.TrimSpace(contract.SnapshotID) == "" || strings.TrimSpace(contract.SnapshotVersion) == "" {
		return errors.New("resolved media contract is missing model or snapshot provenance")
	}
	if contract.ProviderID != providerID || contract.AdapterID != surface.adapterID || contract.ProviderSurface != surface.providerSurface || contract.CredentialSurface != surface.credentialSurface {
		return errors.New("resolved media contract does not match the reviewed provider surface")
	}
	if strings.TrimSpace(contract.CredentialFingerprint) == "" {
		return errors.New("resolved media contract has no active credential fingerprint")
	}
	if contract.ExecutionMode != "plan" && contract.ExecutionMode != "auto" {
		return errors.New("resolved media contract execution mode is not admitted")
	}
	if contract.WorkspaceScope != workspaceScope || contract.SessionScope != sessionID {
		return errors.New("resolved media contract scope is stale or mismatched")
	}
	if len(contract.DenialReasons) != 0 {
		return errors.New("resolved media contract contains denied prerequisites")
	}
	return nil
}

func preSessionAllowedMediaCapabilities(contract provideriface.SessionMediaContract) (map[string]provideriface.MediaContractCapability, error) {
	allowed := make(map[string]provideriface.MediaContractCapability)
	seen := make(map[string]struct{}, len(contract.Capabilities))
	for _, capability := range contract.Capabilities {
		modality := strings.ToLower(strings.TrimSpace(capability.Modality))
		if modality == "" {
			return nil, errors.New("resolved media contract contains an unnamed capability")
		}
		if _, duplicate := seen[modality]; duplicate {
			return nil, fmt.Errorf("resolved media contract contains duplicate capability %q", modality)
		}
		seen[modality] = struct{}{}
		if capability.State != provideriface.MediaCapabilityStateAllowed {
			continue
		}
		if capability.Modality != modality || capability.MaxBytes <= 0 || capability.MaxCount <= 0 || len(capability.ContentTypes) == 0 {
			return nil, fmt.Errorf("resolved media capability %q is incomplete or non-canonical", modality)
		}
		allowed[modality] = capability
	}
	if len(allowed) == 0 {
		return nil, errors.New("resolved media contract has no allowed capabilities")
	}
	return allowed, nil
}

func normalizePreSessionStagedMetadata(staged PreSessionMediaStagedMetadata) (PreSessionMediaStagedMetadata, error) {
	staged.StagingID = strings.TrimSpace(staged.StagingID)
	staged.AccountScopeID = strings.TrimSpace(staged.AccountScopeID)
	staged.Modality = strings.ToLower(strings.TrimSpace(staged.Modality))
	staged.DeclaredMIMEType = normalizePreSessionMediaMIME(staged.DeclaredMIMEType)
	staged.DetectedMIMEType = normalizePreSessionMediaMIME(staged.DetectedMIMEType)
	staged.FileType = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(staged.FileType), "."))
	staged.DigestSHA256 = strings.ToLower(strings.TrimSpace(staged.DigestSHA256))
	if staged.StagingID == "" || staged.AccountScopeID == "" || staged.Modality == "" || staged.DeclaredMIMEType == "" || staged.DetectedMIMEType == "" {
		return PreSessionMediaStagedMetadata{}, errors.New("immutable staging identity, ownership, modality, and MIME metadata are required")
	}
	if staged.DeclaredMIMEType != staged.DetectedMIMEType {
		return PreSessionMediaStagedMetadata{}, errors.New("declared MIME type does not match detected MIME type")
	}
	if staged.Size <= 0 {
		return PreSessionMediaStagedMetadata{}, errors.New("immutable staged size must be positive")
	}
	if len(staged.DigestSHA256) != sha256.Size*2 {
		return PreSessionMediaStagedMetadata{}, errors.New("immutable staged SHA-256 digest is invalid")
	}
	if _, err := hex.DecodeString(staged.DigestSHA256); err != nil {
		return PreSessionMediaStagedMetadata{}, errors.New("immutable staged SHA-256 digest is invalid")
	}
	return staged, nil
}

func normalizePreSessionMediaMIME(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if parsed, _, err := mime.ParseMediaType(value); err == nil {
		return strings.ToLower(strings.TrimSpace(parsed))
	}
	return value
}

func preSessionMediaAssetID(digest, contractHash string) string {
	sum := sha256.Sum256([]byte(digest + "\x00" + contractHash))
	return "media_" + hex.EncodeToString(sum[:])
}
