package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/artifactv3video"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// migrateArtifactV3VideoReceipts upgrades only references already recorded in
// immutable owned video revisions. It never invents provenance from a filename
// or caller input, changes source/cut identity, or regenerates media. Old exact
// event sequences remain in receipts; new conversions use revision event seq.
func migrateArtifactV3VideoReceipts(ctx context.Context, adapter *artifactV3RuntimeAdapter, derivatives *artifactV3DerivativeStore) error {
	seen := map[string]bool{}
	return adapter.sessions.VisitNativeVideoDerivativeReceipts(func(account, user string, ref pebblestore.ArtifactV3VideoReference) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := account + "/" + user + "/" + artifactV3ReferenceKey(ref)
		if seen[key] {
			return nil
		}
		seen[key] = true
		if _, err := derivatives.Read(ctx, ref.SessionID, ref.ArtifactID, ref); err == nil {
			return nil
		}
		if !validArtifactV3DerivativeID(ref.DerivativeID) || ref.DigestSHA256 != strings.TrimPrefix(ref.DerivativeID, "av3der_") || ref.EventSeq == 0 || (ref.MediaType != "image/png" && ref.MediaType != "video/mp4") {
			return errors.New("historical native derivative reference is invalid")
		}
		project, err := adapter.ReadImmutableRevision(ctx, account, artifactv3video.Selection{AccountScopeID: account, UserID: user, SessionID: ref.SessionID, ArtifactID: ref.ArtifactID, RevisionID: ref.RevisionID, CommitOID: ref.CommitOID, TreeOID: ref.TreeOID})
		if err != nil {
			return err
		}
		if project.ManifestDigestSHA256 != ref.ManifestDigestSHA256 || project.BuildID != ref.BuildID || project.ValidationID != ref.ValidationID || project.AnimationProfile != ref.AnimationProfile {
			return errors.New("historical native derivative source evidence mismatch")
		}
		parent := filepath.Join(derivatives.root, artifactV3StorageKey(ref.SessionID, ref.ArtifactID))
		entries, err := os.ReadDir(parent)
		if err != nil {
			return err
		}
		if len(entries) > 1000 {
			return errors.New("native derivative migration set bound exceeded")
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			set := filepath.Join(parent, entry.Name())
			// Only the old receipt-less storage format is eligible. A corrupt/new
			// receipt set must not be blessed by reconstructing it from requests.
			if _, err := os.Lstat(filepath.Join(set, "references.json")); !errors.Is(err, os.ErrNotExist) {
				continue
			}
			marker, err := os.ReadFile(filepath.Join(set, "complete"))
			if err != nil {
				return err
			}
			ids := strings.Split(strings.TrimSuffix(string(marker), "\n"), "\n")
			if len(ids) != 2 || entry.Name() != artifactV3StorageKey(ids...) {
				continue
			}
			found := false
			for _, id := range ids {
				if id == ref.DerivativeID {
					found = true
				}
			}
			if !found {
				continue
			}
			filename := filepath.Join(set, ref.DerivativeID)
			info, err := os.Lstat(filename)
			if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
				return errors.New("historical native derivative file is invalid")
			}
			body, err := os.ReadFile(filename)
			if err != nil {
				return err
			}
			if sha256Hex(body) != ref.DigestSHA256 {
				return errors.New("historical native derivative digest mismatch")
			}
			return derivatives.PutAtomic(ctx, ref.SessionID, ref.ArtifactID, []artifactv3video.Derivative{{ID: ref.DerivativeID, MediaType: ref.MediaType, DigestSHA256: ref.DigestSHA256, Bytes: body, Reference: ref}})
		}
		return errors.New("historical native derivative is missing its exact published bytes")
	})
}
