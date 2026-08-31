package artifactlegacy

import (
	"context"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Source is the complete capability visible to the bounded legacy adapter.
// Deliberately absent are create, reserve, update, fail, select, publish,
// promote, delete, mutation, V2, video, allocation, and conversion methods.
type Source interface {
	GetReference(artifact.Principal, pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error)
	ReadReference(context.Context, artifact.Principal, pebblestore.SessionArtifactSelectionReference, int64) ([]byte, pebblestore.SessionArtifactVariant, error)
	ReadPackageReference(context.Context, artifact.Principal, pebblestore.SessionArtifactSelectionReference, string, int64) ([]artifact.PackageManifestEntry, []byte, pebblestore.SessionArtifactVariant, error)
	MaterializeReference(context.Context, artifact.Principal, pebblestore.SessionArtifactSelectionReference, string, string, bool) (artifact.Materialized, error)
}

// Reader keeps historical ready artifacts viewable without exposing a managed
// creative-write authority. It accepts exact immutable V1 references only.
type Reader struct{ source Source }

func NewReader(source Source) *Reader { return &Reader{source: source} }

func (r *Reader) GetReady(principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error) {
	if r == nil || r.source == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("legacy artifact reader is unavailable")
	}
	variant, err := r.source.GetReference(principal, normalizedReference(ref))
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if variant.Status != pebblestore.SessionArtifactStatusReady {
		return pebblestore.SessionArtifactVariant{}, errors.New("legacy artifact reader accepts only ready artifacts")
	}
	return variant, nil
}

func (r *Reader) ReadReady(ctx context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, maxBytes int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	if maxBytes <= 0 {
		return nil, pebblestore.SessionArtifactVariant{}, errors.New("legacy artifact read requires a positive byte bound")
	}
	if _, err := r.GetReady(principal, ref); err != nil {
		return nil, pebblestore.SessionArtifactVariant{}, err
	}
	return r.source.ReadReference(ctx, principal, normalizedReference(ref), maxBytes)
}

func (r *Reader) ReadPackageEntry(ctx context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, entry string, maxBytes int64) ([]artifact.PackageManifestEntry, []byte, pebblestore.SessionArtifactVariant, error) {
	if maxBytes <= 0 {
		return nil, nil, pebblestore.SessionArtifactVariant{}, errors.New("legacy artifact package read requires a positive byte bound")
	}
	if _, err := r.GetReady(principal, ref); err != nil {
		return nil, nil, pebblestore.SessionArtifactVariant{}, err
	}
	return r.source.ReadPackageReference(ctx, principal, normalizedReference(ref), strings.TrimSpace(entry), maxBytes)
}

func (r *Reader) MaterializeReady(ctx context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, workspaceRoot, destination string) (artifact.Materialized, error) {
	if _, err := r.GetReady(principal, ref); err != nil {
		return artifact.Materialized{}, err
	}
	return r.source.MaterializeReference(ctx, principal, normalizedReference(ref), strings.TrimSpace(workspaceRoot), strings.TrimSpace(destination), false)
}

func normalizedReference(ref pebblestore.SessionArtifactSelectionReference) pebblestore.SessionArtifactSelectionReference {
	ref.SessionID = strings.TrimSpace(ref.SessionID)
	ref.CollectionID = strings.TrimSpace(ref.CollectionID)
	ref.VariantID = strings.TrimSpace(ref.VariantID)
	return ref
}
