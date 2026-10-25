package claudecode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	claudeCodeImageMaxBytes = int64(7_864_320)
	claudeCodeImageMaxCount = 100
)

func (r *Runner) MediaCapabilityDeclaration(ctx context.Context) (provideriface.MediaAdapterDeclaration, error) {
	if r == nil || r.authStore == nil {
		return provideriface.MediaAdapterDeclaration{}, errors.New("claude code auth store is not configured")
	}
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok {
		return provideriface.MediaAdapterDeclaration{}, identity.ErrPrincipalRequired
	}
	record, enabled, err := activeCLICredential(r.authStore, principal.AccountScopeID)
	if err != nil {
		return provideriface.MediaAdapterDeclaration{}, err
	}
	if !enabled {
		return provideriface.MediaAdapterDeclaration{}, errors.New("claude code CLI credential is not active")
	}
	return claudeCodeMediaDeclaration(record), nil
}

func claudeCodeMediaDeclaration(record pebblestore.AuthCredentialRecord) provideriface.MediaAdapterDeclaration {
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{record.AccountScopeID, record.Provider, record.ID, record.Type}, "\x00")))
	return provideriface.MediaAdapterDeclaration{
		AdapterID:             provideriface.MediaAdapterIDClaudeCodeMCPV1,
		ProviderID:            ProviderID,
		ProviderSurface:       provideriface.MediaProviderSurfaceClaudeCodeMCP,
		CredentialSurface:     provideriface.MediaCredentialSurfaceClaudeCodeCLI,
		CredentialFingerprint: hex.EncodeToString(fingerprint[:16]),
		Inputs:                []provideriface.MediaAdapterCapability{{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/gif", "image/jpeg", "image/png", "image/webp"}, ContentTypes: []string{"image"}, MaxBytes: claudeCodeImageMaxBytes, MaxCount: claudeCodeImageMaxCount}},
	}
}

func validateClaudeCodeImage(contract provideriface.SessionMediaContract, payload provideriface.SessionMediaPayload, count int64) error {
	if contract.Hash == "" || contract.ProviderID != ProviderID || contract.AdapterID != provideriface.MediaAdapterIDClaudeCodeMCPV1 || contract.ProviderSurface != provideriface.MediaProviderSurfaceClaudeCodeMCP || contract.CredentialSurface != provideriface.MediaCredentialSurfaceClaudeCodeCLI {
		return errors.New("media contract does not match the Claude Code MCP surface")
	}
	mime := strings.ToLower(strings.TrimSpace(payload.MIMEType))
	if payload.Modality != "image" || !mediaContains([]string{"image/gif", "image/jpeg", "image/png", "image/webp"}, mime) || strings.TrimSpace(payload.AssetID) == "" || payload.Size <= 0 || payload.Size != int64(len(payload.Bytes)) || payload.Size > claudeCodeImageMaxBytes {
		return errors.New("Claude Code image identity, type or size is invalid")
	}
	digest := sha256.Sum256(payload.Bytes)
	if !strings.EqualFold(payload.DigestSHA256, hex.EncodeToString(digest[:])) {
		return errors.New("Claude Code image digest is invalid")
	}
	for _, capability := range contract.Capabilities {
		if capability.Modality == "image" && capability.State == provideriface.MediaCapabilityStateAllowed && capability.Semantics == pebblestore.ModelCatalogMediaSemanticsNative && mediaContains(capability.MIMETypes, mime) && mediaContains(capability.ContentTypes, "image") && capability.MaxBytes >= payload.Size && count > 0 && count <= int64(capability.MaxCount) && count <= claudeCodeImageMaxCount {
			return nil
		}
	}
	return errors.New("Claude Code image is outside the authorized media capability")
}

func mediaContains(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}
