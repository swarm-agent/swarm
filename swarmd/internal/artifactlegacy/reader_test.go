package artifactlegacy

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: historical ready artifacts remain viewable through one adapter
// whose method set cannot express any V1/V2 write or Video Studio conversion.
// Threat: a compatibility dependency could grow a mutation escape hatch.
func TestReaderMethodSetIsStructurallyReadOnly(t *testing.T) {
	typeOf := reflect.TypeOf((*Reader)(nil))
	allowed := map[string]bool{"GetReady": true, "ReadReady": true, "ReadPackageEntry": true, "MaterializeReady": true}
	for index := 0; index < typeOf.NumMethod(); index++ {
		method := typeOf.Method(index)
		if !allowed[method.Name] {
			t.Fatalf("legacy reader exposes non-read capability %q", method.Name)
		}
		delete(allowed, method.Name)
	}
	if len(allowed) != 0 {
		t.Fatalf("legacy reader missing bounded methods: %v", allowed)
	}
	forbidden := []string{"Create", "Mutate", "Validate", "Select", "Publish", "Convert", "Allocate", "Reserve", "Delete", "Promote", "ArtifactV2"}
	source := reflect.TypeOf((*Source)(nil)).Elem()
	for index := 0; index < source.NumMethod(); index++ {
		name := source.Method(index).Name
		for _, token := range forbidden {
			if containsFold(name, token) {
				t.Fatalf("legacy source exposes forbidden capability %q", name)
			}
		}
	}
}

// Requirement: rejected legacy reads/materialization leave all observable state
// unchanged. Threat: staging, stale, or overwrite-like input could trigger a
// write before rejection.
func TestReaderRejectsNonReadyAndInvalidBoundsBeforeSourceMutation(t *testing.T) {
	fake := &sourceFake{variant: pebblestore.SessionArtifactVariant{Status: pebblestore.SessionArtifactStatusStaging}}
	reader := NewReader(fake)
	principal := artifact.Principal{SessionID: "session", AccountScopeID: "account", UserID: "user"}
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "collection", VariantID: "variant", EventSeq: 1}
	if _, _, err := reader.ReadReady(context.Background(), principal, ref, 0); err == nil {
		t.Fatal("zero-byte bound read succeeded")
	}
	if fake.calls != 0 {
		t.Fatalf("invalid bound reached source: calls=%d", fake.calls)
	}
	if _, err := reader.MaterializeReady(context.Background(), principal, ref, "/workspace", "out"); err == nil {
		t.Fatal("staging materialization succeeded")
	}
	if fake.materialized != 0 || fake.reads != 0 {
		t.Fatalf("rejected legacy operation changed source state: %+v", fake)
	}
}

type sourceFake struct {
	variant      pebblestore.SessionArtifactVariant
	calls        int
	reads        int
	materialized int
}

func (f *sourceFake) GetReference(artifact.Principal, pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error) {
	f.calls++
	if f.variant.ID == "error" {
		return pebblestore.SessionArtifactVariant{}, errors.New("unavailable")
	}
	return f.variant, nil
}
func (f *sourceFake) ReadReference(context.Context, artifact.Principal, pebblestore.SessionArtifactSelectionReference, int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	f.reads++
	return []byte("ready"), f.variant, nil
}
func (f *sourceFake) ReadPackageReference(context.Context, artifact.Principal, pebblestore.SessionArtifactSelectionReference, string, int64) ([]artifact.PackageManifestEntry, []byte, pebblestore.SessionArtifactVariant, error) {
	f.reads++
	return nil, nil, f.variant, nil
}
func (f *sourceFake) MaterializeReference(context.Context, artifact.Principal, pebblestore.SessionArtifactSelectionReference, string, string, bool) (artifact.Materialized, error) {
	f.materialized++
	return artifact.Materialized{}, nil
}

func containsFold(value, token string) bool {
	for i := 0; i+len(token) <= len(value); i++ {
		match := true
		for j := range token {
			a, b := value[i+j], token[j]
			if 'A' <= a && a <= 'Z' {
				a += 'a' - 'A'
			}
			if 'A' <= b && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
