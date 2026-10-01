package pebblestore

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Video operation discriminator constants.
const (
	VideoOperationCreate = "create"
	VideoOperationEdit   = "edit"
	VideoOperationExtend = "extend"
)

// Video transport constants describing provider communication mechanism.
const (
	VideoTransportGooglePredictLongRunning = "google_predict_long_running"
	VideoTransportGoogleInteractions       = "google_interactions"
	VideoTransportOpenRouterVideos         = "openrouter_videos"
)

// VideoSourceLink identifies an exact source video referenced for an edit or extension.
type VideoSourceLink struct {
	SessionID     string `json:"session_id,omitempty"`
	CollectionID  string `json:"collection_id,omitempty"`
	VariantID     string `json:"variant_id,omitempty"`
	EventSeq      uint64 `json:"event_seq,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	DeliverableID string `json:"deliverable_id,omitempty"`
	MediaRefID    string `json:"media_ref_id,omitempty"`
	DigestSHA256  string `json:"digest_sha256,omitempty"`
	URI           string `json:"uri,omitempty"`
}

// Validate checks bounds and structure of a VideoSourceLink.
func (l *VideoSourceLink) Validate() error {
	if l == nil {
		return nil
	}
	for _, val := range []string{l.SessionID, l.CollectionID, l.VariantID, l.ProjectID, l.TaskID, l.DeliverableID, l.MediaRefID} {
		if len(val) > 256 {
			return errors.New("video source link identifier exceeds 256 characters")
		}
	}
	if len(l.URI) > 2048 {
		return errors.New("video source link URI exceeds 2048 characters")
	}
	if l.DigestSHA256 != "" {
		if len(l.DigestSHA256) != 64 {
			return errors.New("video source link digest must be a 64-character sha256 hex string")
		}
		if _, err := hex.DecodeString(l.DigestSHA256); err != nil {
			return errors.New("video source link digest is not valid hex")
		}
	}
	return nil
}

// Normalize trims whitespace and normalizes hex in VideoSourceLink.
func (l *VideoSourceLink) Normalize() {
	if l == nil {
		return
	}
	l.SessionID = strings.TrimSpace(l.SessionID)
	l.CollectionID = strings.TrimSpace(l.CollectionID)
	l.VariantID = strings.TrimSpace(l.VariantID)
	l.ProjectID = strings.TrimSpace(l.ProjectID)
	l.TaskID = strings.TrimSpace(l.TaskID)
	l.DeliverableID = strings.TrimSpace(l.DeliverableID)
	l.MediaRefID = strings.TrimSpace(l.MediaRefID)
	l.DigestSHA256 = strings.ToLower(strings.TrimSpace(l.DigestSHA256))
	l.URI = strings.TrimSpace(l.URI)
}

// Clone creates a shallow copy of VideoSourceLink.
func (l *VideoSourceLink) Clone() *VideoSourceLink {
	if l == nil {
		return nil
	}
	cp := *l
	return &cp
}

// VideoProvenance contains typed server-authored metadata tracking the exact origin,
// operation, credentials, provider interaction, and output properties of a generated,
// edited, or extended video.
type VideoProvenance struct {
	AccountScopeID      string           `json:"account_scope_id"`
	CredentialID        string           `json:"credential_id,omitempty"`
	CredentialVersion   string           `json:"credential_version,omitempty"`
	Provider            string           `json:"provider"`
	Model               string           `json:"model"`
	Transport           string           `json:"transport"`
	Operation           string           `json:"operation"` // "create" | "edit" | "extend"
	SourceLink          *VideoSourceLink `json:"source_link,omitempty"`
	OutputDigestSHA256  string           `json:"output_digest_sha256,omitempty"`
	InteractionID       string           `json:"interaction_id,omitempty"`
	ProviderResource    string           `json:"provider_resource,omitempty"` // e.g. Veo URI or operation name
	CreatedAt           int64            `json:"created_at"`
	ExpiresAt           int64            `json:"expires_at,omitempty"` // known provider reference expiry (Unix ms)
	ObservedDurationMs  int64            `json:"observed_duration_ms,omitempty"`
	ObservedWidth       int              `json:"observed_width,omitempty"`
	ObservedHeight      int              `json:"observed_height,omitempty"`
	ExtensionCount      int              `json:"extension_count,omitempty"`
	ExtensionCountKnown bool             `json:"extension_count_known,omitempty"`
	IsCombinedOutput    bool             `json:"is_combined_output,omitempty"` // true if output already includes full video (e.g. Veo extension)
	AspectRatio         string           `json:"aspect_ratio,omitempty"`
	Resolution          string           `json:"resolution,omitempty"`
	DurationSeconds     int              `json:"duration_seconds,omitempty"`
	HasInteraction      bool             `json:"has_interaction,omitempty"`
	HasProviderResource bool             `json:"has_provider_resource,omitempty"`
}

// Validate checks field constraints and verifies that no raw secrets are stored.
func (p *VideoProvenance) Validate() error {
	if p == nil {
		return nil
	}
	p.AccountScopeID = strings.TrimSpace(p.AccountScopeID)
	if p.AccountScopeID == "" {
		return errors.New("video provenance requires account_scope_id")
	}
	if len(p.AccountScopeID) > 256 {
		return errors.New("video provenance account_scope_id exceeds bounds")
	}
	op := strings.ToLower(strings.TrimSpace(p.Operation))
	if op != "" && op != VideoOperationCreate && op != VideoOperationEdit && op != VideoOperationExtend {
		return fmt.Errorf("video provenance operation %q is invalid; must be create, edit, or extend", p.Operation)
	}
	if isCredentialSecret(p.CredentialID) || isCredentialSecret(p.CredentialVersion) {
		return errors.New("video provenance credential field contains illegal secret or key material")
	}
	if len(p.CredentialID) > 256 {
		return errors.New("video provenance credential_id exceeds 256 characters")
	}
	if len(p.CredentialVersion) > 128 {
		return errors.New("video provenance credential_version exceeds 128 characters")
	}
	if len(p.Provider) > 128 {
		return errors.New("video provenance provider exceeds 128 characters")
	}
	if len(p.Model) > 128 {
		return errors.New("video provenance model exceeds 128 characters")
	}
	if len(p.Transport) > 128 {
		return errors.New("video provenance transport exceeds 128 characters")
	}
	if len(p.InteractionID) > 256 {
		return errors.New("video provenance interaction_id exceeds 256 characters")
	}
	if len(p.ProviderResource) > 2048 {
		return errors.New("video provenance provider_resource exceeds 2048 characters")
	}
	if len(p.AspectRatio) > 32 {
		return errors.New("video provenance aspect_ratio exceeds 32 characters")
	}
	if len(p.Resolution) > 32 {
		return errors.New("video provenance resolution exceeds 32 characters")
	}
	if p.OutputDigestSHA256 != "" {
		if len(p.OutputDigestSHA256) != 64 {
			return errors.New("video provenance output digest must be a 64-character sha256 hex string")
		}
		if _, err := hex.DecodeString(p.OutputDigestSHA256); err != nil {
			return errors.New("video provenance output digest is not valid hex")
		}
	}
	if p.ExtensionCount < 0 {
		return errors.New("video provenance extension_count cannot be negative")
	}
	if p.ObservedDurationMs < 0 || p.ObservedWidth < 0 || p.ObservedHeight < 0 {
		return errors.New("video provenance observed properties cannot be negative")
	}
	if p.DurationSeconds < 0 {
		return errors.New("video provenance duration_seconds cannot be negative")
	}
	if p.SourceLink != nil {
		if err := p.SourceLink.Validate(); err != nil {
			return fmt.Errorf("video provenance source link invalid: %w", err)
		}
	}
	return nil
}

// Normalize normalizes string fields in VideoProvenance.
func (p *VideoProvenance) Normalize() {
	if p == nil {
		return
	}
	p.AccountScopeID = strings.TrimSpace(p.AccountScopeID)
	p.CredentialID = strings.TrimSpace(p.CredentialID)
	p.CredentialVersion = strings.TrimSpace(p.CredentialVersion)
	p.Provider = strings.ToLower(strings.TrimSpace(p.Provider))
	p.Model = strings.TrimSpace(p.Model)
	p.Transport = strings.TrimSpace(p.Transport)
	p.Operation = strings.ToLower(strings.TrimSpace(p.Operation))
	p.OutputDigestSHA256 = strings.ToLower(strings.TrimSpace(p.OutputDigestSHA256))
	p.InteractionID = strings.TrimSpace(p.InteractionID)
	p.ProviderResource = strings.TrimSpace(p.ProviderResource)
	p.AspectRatio = strings.TrimSpace(p.AspectRatio)
	p.Resolution = strings.TrimSpace(p.Resolution)
	p.HasInteraction = false
	p.HasProviderResource = false
	if p.SourceLink != nil {
		p.SourceLink.Normalize()
	}
}

// Clone creates a deep copy of VideoProvenance.
func (p *VideoProvenance) Clone() *VideoProvenance {
	if p == nil {
		return nil
	}
	cp := *p
	if p.SourceLink != nil {
		sl := *p.SourceLink
		cp.SourceLink = &sl
	}
	return &cp
}

// EqualVideoProvenance checks whether two VideoProvenance pointers are deeply equal.
func EqualVideoProvenance(a, b *VideoProvenance) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.AccountScopeID != b.AccountScopeID ||
		a.CredentialID != b.CredentialID ||
		a.CredentialVersion != b.CredentialVersion ||
		a.Provider != b.Provider ||
		a.Model != b.Model ||
		a.Transport != b.Transport ||
		a.Operation != b.Operation ||
		a.OutputDigestSHA256 != b.OutputDigestSHA256 ||
		a.InteractionID != b.InteractionID ||
		a.ProviderResource != b.ProviderResource ||
		a.CreatedAt != b.CreatedAt ||
		a.ExpiresAt != b.ExpiresAt ||
		a.ObservedDurationMs != b.ObservedDurationMs ||
		a.ObservedWidth != b.ObservedWidth ||
		a.ObservedHeight != b.ObservedHeight ||
		a.ExtensionCount != b.ExtensionCount ||
		a.ExtensionCountKnown != b.ExtensionCountKnown ||
		a.IsCombinedOutput != b.IsCombinedOutput ||
		a.AspectRatio != b.AspectRatio ||
		a.Resolution != b.Resolution ||
		a.DurationSeconds != b.DurationSeconds ||
		a.HasInteraction != b.HasInteraction ||
		a.HasProviderResource != b.HasProviderResource {
		return false
	}
	return EqualVideoSourceLink(a.SourceLink, b.SourceLink)
}

// EqualVideoSourceLink checks whether two VideoSourceLink pointers are deeply equal.
func EqualVideoSourceLink(a, b *VideoSourceLink) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func isCredentialSecret(val string) bool {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "AIza") || strings.HasPrefix(trimmed, "sk-") {
		return true
	}
	if len(trimmed) > 128 {
		return true
	}
	return false
}
