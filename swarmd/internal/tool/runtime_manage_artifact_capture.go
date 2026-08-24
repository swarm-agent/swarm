package tool

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	captureMaxSourceBytes   = 32 << 20
	captureMaxEntryBytes    = 8 << 20
	captureMaxEntries       = 128
	captureMaxManifestBytes = 16 << 10
)

var (
	captureStateIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	captureScriptPattern  = regexp.MustCompile(`(?is)<script\s+([^>]*)>(.*?)</script\s*>`)
	captureManifestID     = regexp.MustCompile(`(?i)(?:^|\s)id\s*=\s*["']swarm-capture-manifest["'](?:\s|$)`)
	captureManifestType   = regexp.MustCompile(`(?i)(?:^|\s)type\s*=\s*["']application/json["'](?:\s|$)`)
)

type captureManifest struct {
	Version string                 `json:"version"`
	States  []captureManifestState `json:"states"`
}

type captureManifestState struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

func (r *Runtime) exportHTMLStills(ctx context.Context, principal artifact.Principal, callID string, args map[string]any) ([]map[string]any, pebblestore.SessionArtifactSelectionReference, *pebblestore.SessionArtifactOutputRequirements, error) {
	for key := range args {
		switch key {
		case "action", "session_id", "collection_id", "variant_id", "event_seq", "state_ids":
		default:
			return nil, pebblestore.SessionArtifactSelectionReference{}, nil, captureError("capture_source_reference_invalid", fmt.Sprintf("export_html_stills contains unsupported field %q", key))
		}
	}
	if r.htmlCapture == nil {
		return nil, pebblestore.SessionArtifactSelectionReference{}, nil, captureError("capture_renderer_unavailable", "trusted HTML capture renderer is not configured")
	}
	if err := validateArtifactRetrievalIdentity(args, "export_html_stills", true); err != nil {
		return nil, pebblestore.SessionArtifactSelectionReference{}, nil, captureError("capture_source_reference_invalid", "complete exact ready HTML reference is required")
	}
	ref, explicit, err := parseArtifactReadReference(args, strings.TrimSpace(asString(args["variant_id"])))
	if err != nil || !explicit {
		return nil, pebblestore.SessionArtifactSelectionReference{}, nil, captureError("capture_source_reference_invalid", "complete exact ready HTML reference is required")
	}
	variant, err := r.artifactAuthority.GetReference(principal, ref)
	if err != nil {
		return nil, ref, nil, captureError("capture_source_reference_invalid", "exact ready HTML reference could not be authenticated")
	}
	files, entry, err := r.readCaptureSource(ctx, principal, ref, variant)
	if err != nil {
		return nil, ref, nil, err
	}
	manifest, err := parseCaptureManifest(files[entry])
	if err != nil {
		return nil, ref, nil, err
	}
	stateIDs, err := selectCaptureStates(args, manifest.States)
	if err != nil {
		return nil, ref, nil, err
	}
	requirements, err := artifact.ResolveOutputRequirements(&artifact.OutputRequirementsInput{Preset: "landscape_video"})
	if err != nil {
		return nil, ref, nil, captureError("capture_renderer_failed", "canonical landscape video requirements are unavailable")
	}

	captures, err := r.htmlCapture.Capture(ctx, htmlcapture.Request{Entry: entry, Files: files, StateIDs: stateIDs})
	if err != nil {
		return nil, ref, requirements, normalizeCaptureRendererError(err)
	}
	if len(captures) != len(stateIDs) {
		return nil, ref, requirements, captureError("capture_renderer_failed", "renderer returned an inconsistent state count")
	}
	collectionID := captureOpaqueID("collection", principal.SessionID, callID, ref, strings.Join(stateIDs, "\x00"))
	exports := make([]map[string]any, 0, len(captures))
	for index, capture := range captures {
		if capture.StateID != stateIDs[index] {
			return nil, ref, requirements, captureError("capture_renderer_failed", "renderer returned states out of canonical order")
		}
		if err := validateCapturePNG(capture.PNG); err != nil {
			return nil, ref, requirements, err
		}
		stateCallID := callID + ":" + capture.StateID
		variantID := captureOpaqueID("variant", principal.SessionID, callID, ref, capture.StateID)
		input := artifact.CreateInput{
			RequestID:    captureOpaqueID("request-export-html-stills", principal.SessionID, stateCallID, ref, capture.StateID),
			CollectionID: collectionID, VariantID: variantID, Filename: "capture-" + capture.StateID + ".png", MediaType: "image/png",
			Presentation:       pebblestore.SessionArtifactPresentation{Kind: "image", Label: capture.StateID, Previewable: true, Width: htmlcapture.Width, Height: htmlcapture.Height},
			OutputRequirements: requirements,
			SourceSessionID:    ref.SessionID, SourceCollectionID: ref.CollectionID, SourceVariantID: ref.VariantID, SourceEventSeq: ref.EventSeq,
			Body: append([]byte(nil), capture.PNG...), AutoAccept: len(captures) == 1,
		}
		if index == 0 {
			input.CollectionName = "HTML video stills"
		}
		published, createErr := r.artifactAuthority.Create(ctx, principal, input)
		if createErr != nil {
			return nil, ref, requirements, captureError("capture_publish_failed", "captured PNG could not be durably published")
		}
		expectedDigest := sha256.Sum256(capture.PNG)
		if published.Status != pebblestore.SessionArtifactStatusReady || published.MediaType != "image/png" || published.EventSeq == 0 || published.DigestSHA256 != hex.EncodeToString(expectedDigest[:]) {
			return nil, ref, requirements, captureError("capture_idempotency_conflict", "published still metadata conflicts with the trusted capture bytes")
		}
		readyRef := pebblestore.SessionArtifactSelectionReference{SessionID: published.SessionID, CollectionID: published.CollectionID, VariantID: published.ID, EventSeq: published.EventSeq}
		readyBytes, ready, getErr := r.artifactAuthority.ReadReference(ctx, principal, readyRef, htmlcapture.MaxPNGBytes)
		if getErr != nil || ready.Status != pebblestore.SessionArtifactStatusReady || !bytes.Equal(readyBytes, capture.PNG) {
			return nil, ref, requirements, captureError("capture_publish_failed", "published still could not be reread as the exact ready PNG")
		}
		exports = append(exports, map[string]any{"state_id": capture.StateID, "artifact": managedArtifactVariant(ready), "reference": managedArtifactReferenceWithSession(ready.SessionID, ready.CollectionID, ready.ID, ready.EventSeq)})
	}
	return exports, ref, requirements, nil
}

func (r *Runtime) readCaptureSource(ctx context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, variant pebblestore.SessionArtifactVariant) (map[string][]byte, string, error) {
	mediaType := canonicalArtifactMediaType(variant.MediaType)
	switch mediaType {
	case "text/html":
		body, _, err := r.artifactAuthority.ReadReference(ctx, principal, ref, captureMaxSourceBytes)
		if err != nil {
			return nil, "", captureError("capture_source_limit_exceeded", "HTML source exceeds fixed capture bounds")
		}
		if len(body) == 0 || len(body) > captureMaxSourceBytes || !utf8.Valid(body) {
			return nil, "", captureError("capture_source_type_unsupported", "ready source is not valid bounded HTML")
		}
		return map[string][]byte{"index.html": append([]byte(nil), body...)}, "index.html", nil
	case "application/zip":
		body, _, err := r.artifactAuthority.ReadReference(ctx, principal, ref, captureMaxSourceBytes)
		if err != nil {
			return nil, "", captureError("capture_source_limit_exceeded", "HTML package exceeds fixed capture bounds")
		}
		return readCapturePackage(body)
	default:
		return nil, "", captureError("capture_source_type_unsupported", "ready source must be text/html or application/zip")
	}
}

func readCapturePackage(body []byte) (map[string][]byte, string, error) {
	if len(body) == 0 || len(body) > captureMaxSourceBytes {
		return nil, "", captureError("capture_source_limit_exceeded", "HTML package exceeds fixed capture bounds")
	}
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil || len(archive.File) == 0 || len(archive.File) > captureMaxEntries {
		return nil, "", captureError("capture_package_invalid", "HTML package is malformed or exceeds fixed entry bounds")
	}
	files := make(map[string][]byte, len(archive.File))
	total := 0
	for _, file := range archive.File {
		name := file.Name
		if file.FileInfo().IsDir() || name == "" || len(name) > 1024 || pathClean(name) != name || strings.Contains(name, "\\") || !file.Mode().IsRegular() {
			return nil, "", captureError("capture_package_invalid", "HTML package contains an unsafe entry")
		}
		if _, exists := files[name]; exists || file.UncompressedSize64 > captureMaxEntryBytes {
			return nil, "", captureError("capture_package_invalid", "HTML package contains duplicate or oversized entries")
		}
		reader, openErr := file.Open()
		if openErr != nil {
			return nil, "", captureError("capture_package_invalid", "HTML package entry could not be opened")
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, captureMaxEntryBytes+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || len(data) > captureMaxEntryBytes || uint64(len(data)) != file.UncompressedSize64 {
			return nil, "", captureError("capture_package_invalid", "HTML package entry failed bounded validation")
		}
		total += len(data)
		if total > captureMaxSourceBytes {
			return nil, "", captureError("capture_source_limit_exceeded", "HTML package exceeds fixed expanded bounds")
		}
		files[name] = data
	}
	entry := "index.html"
	if _, ok := files[entry]; !ok {
		return nil, "", captureError("capture_package_invalid", "HTML package is missing canonical index.html")
	}
	return files, entry, nil
}

func parseCaptureManifest(html []byte) (captureManifest, error) {
	scripts := captureScriptPattern.FindAllSubmatch(html, -1)
	manifests := make([][]byte, 0, 1)
	for _, script := range scripts {
		if captureManifestID.Match(script[1]) {
			if !captureManifestType.Match(script[1]) {
				return captureManifest{}, captureError("capture_manifest_invalid", "capture manifest has an invalid media type")
			}
			manifests = append(manifests, script[2])
		}
	}
	if len(manifests) == 0 {
		return captureManifest{}, captureError("capture_manifest_missing", "canonical swarm.capture/v1 manifest is missing")
	}
	if len(manifests) != 1 || len(manifests[0]) > captureMaxManifestBytes {
		return captureManifest{}, captureError("capture_manifest_invalid", "capture manifest is duplicated or exceeds fixed bounds")
	}
	decoder := json.NewDecoder(bytes.NewReader(manifests[0]))
	decoder.DisallowUnknownFields()
	var manifest captureManifest
	if err := decoder.Decode(&manifest); err != nil {
		return captureManifest{}, captureError("capture_manifest_invalid", "capture manifest is malformed")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return captureManifest{}, captureError("capture_manifest_invalid", "capture manifest contains trailing data")
	}
	if manifest.Version != "swarm.capture/v1" || len(manifest.States) < 1 || len(manifest.States) > htmlcapture.MaxStates {
		return captureManifest{}, captureError("capture_manifest_invalid", "capture manifest version or state count is invalid")
	}
	seen := map[string]struct{}{}
	for _, state := range manifest.States {
		if !captureStateIDPattern.MatchString(state.ID) {
			return captureManifest{}, captureError("capture_manifest_invalid", "capture manifest contains an invalid state id")
		}
		if _, ok := seen[state.ID]; ok {
			return captureManifest{}, captureError("capture_manifest_invalid", "capture manifest contains duplicate state ids")
		}
		seen[state.ID] = struct{}{}
		if strings.TrimSpace(state.Label) != state.Label || len(state.Label) > 128 || strings.IndexFunc(state.Label, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return captureManifest{}, captureError("capture_manifest_invalid", "capture manifest contains an invalid state label")
		}
	}
	return manifest, nil
}

func selectCaptureStates(args map[string]any, states []captureManifestState) ([]string, error) {
	raw, supplied := args["state_ids"]
	if !supplied {
		out := make([]string, len(states))
		for i, s := range states {
			out[i] = s.ID
		}
		return out, nil
	}
	values, ok := raw.([]any)
	if !ok || len(values) < 1 || len(values) > htmlcapture.MaxStates {
		return nil, captureError("capture_source_limit_exceeded", "state_ids must contain 1 to 16 unique declared ids")
	}
	wanted := map[string]struct{}{}
	for _, value := range values {
		id, ok := value.(string)
		if !ok || !captureStateIDPattern.MatchString(id) {
			return nil, captureError("capture_state_unknown", "state_ids contains an invalid or undeclared id")
		}
		if _, exists := wanted[id]; exists {
			return nil, captureError("capture_manifest_invalid", "state_ids contains duplicates")
		}
		wanted[id] = struct{}{}
	}
	out := make([]string, 0, len(wanted))
	for _, state := range states {
		if _, ok := wanted[state.ID]; ok {
			out = append(out, state.ID)
			delete(wanted, state.ID)
		}
	}
	if len(wanted) > 0 {
		return nil, captureError("capture_state_unknown", "state_ids contains an undeclared state")
	}
	return out, nil
}

func validateCapturePNG(data []byte) error {
	if len(data) == 0 || len(data) > htmlcapture.MaxPNGBytes || len(data) < 8 || !bytes.Equal(data[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return captureError("capture_png_invalid", "renderer returned an invalid bounded PNG")
	}
	reader := bytes.NewReader(data)
	img, err := png.Decode(reader)
	if err != nil || reader.Len() != 0 || img.Bounds().Dx() != htmlcapture.Width || img.Bounds().Dy() != htmlcapture.Height {
		return captureError("capture_png_invalid", "renderer PNG signature, dimensions, or encoding is invalid")
	}
	return nil
}

func captureOpaqueID(kind, sessionID, callID string, ref pebblestore.SessionArtifactSelectionReference, suffix string) string {
	seed := strings.Join([]string{"manage-artifact", kind, strings.TrimSpace(sessionID), strings.TrimSpace(callID), ref.SessionID, ref.CollectionID, ref.VariantID, fmt.Sprint(ref.EventSeq), suffix}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	prefix := strings.Trim(strings.ToLower(kind), "-_")
	return prefix + "-" + hex.EncodeToString(sum[:12])
}

func normalizeCaptureRendererError(err error) error {
	var captureErr *htmlcapture.Error
	if errors.As(err, &captureErr) {
		return captureError(captureErr.Code, captureErr.SafeMessage)
	}
	return captureError("capture_renderer_failed", "trusted HTML capture failed")
}
func captureError(code, message string) error {
	return fmt.Errorf("manage_artifact export_html_stills failed (code=%s): %s", code, message)
}
func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return errors.New("trailing JSON")
}
func pathClean(name string) string {
	if strings.HasPrefix(name, "/") || strings.Contains(name, "//") {
		return ""
	}
	clean := strings.TrimPrefix(strings.ReplaceAll(name, "\\", "/"), "./")
	parts := strings.Split(clean, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return ""
		}
	}
	return strings.Join(parts, "/")
}
