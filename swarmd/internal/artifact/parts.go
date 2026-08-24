package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/appstorage"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// StagePart writes one independently owned part revision. It uses a distinct
// storage namespace from complete variants, so complete blobs cannot masquerade
// as part bytes.
func (s *Service) StagePart(ctx context.Context, revision pebblestore.SessionArtifactPartRevision, body io.Reader) (PartStaged, error) {
	if s == nil || body == nil {
		return PartStaged{}, errors.New("artifact part body is required")
	}
	if err := validateID("session", revision.OwnerSessionID); err != nil {
		return PartStaged{}, err
	}
	if err := validateID("chain", revision.ArtifactChainID); err != nil {
		return PartStaged{}, err
	}
	if err := validateID("part", revision.PartID); err != nil {
		return PartStaged{}, err
	}
	if err := validateID("part revision", revision.ID); err != nil {
		return PartStaged{}, err
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(revision.MediaType))
	if err != nil || mediaType == "" {
		return PartStaged{}, errors.New("artifact part media type is required")
	}
	mediaType = strings.ToLower(mediaType)
	limit := s.maxArtifactLimit(mediaType, "")

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PartStaged{}, err
	}
	dir, err := s.partRevisionDir(revision.OwnerSessionID, revision.ArtifactChainID, revision.PartID, revision.ID, true)
	if err != nil {
		return PartStaged{}, err
	}
	usage, err := sessionUsage(s.sessionDir(revision.OwnerSessionID))
	if err != nil {
		return PartStaged{}, err
	}
	finalPath := filepath.Join(dir, "content")
	if finalInfo, finalErr := os.Lstat(finalPath); finalErr == nil && finalInfo.Mode().IsRegular() {
		usage -= finalInfo.Size()
	}
	token, err := randomToken()
	if err != nil {
		return PartStaged{}, err
	}
	stagePath := filepath.Join(dir, ".stage-"+token)
	file, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, appstorage.PrivateFilePerm)
	if err != nil {
		return PartStaged{}, err
	}
	hash := sha256.New()
	_, writeErr := copyBounded(ctx, io.MultiWriter(file, hash), body, limit)
	if syncErr := file.Sync(); writeErr == nil {
		writeErr = syncErr
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(stagePath)
		return PartStaged{}, writeErr
	}
	info, err := os.Lstat(stagePath)
	if err != nil || !info.Mode().IsRegular() {
		_ = os.Remove(stagePath)
		return PartStaged{}, errors.New("artifact part staging file is not regular")
	}
	written := info.Size()
	if written <= 0 || usage > s.limits.MaxSessionBytes-written {
		_ = os.Remove(stagePath)
		return PartStaged{}, ErrQuotaExceeded
	}
	staged := PartStaged{OwnerSessionID: revision.OwnerSessionID, ArtifactChainID: revision.ArtifactChainID, PartID: revision.PartID, RevisionID: revision.ID, MediaType: mediaType, DigestSHA256: hex.EncodeToString(hash.Sum(nil)), Size: written, token: token}
	if finalInfo, err := os.Lstat(finalPath); err == nil {
		if !finalInfo.Mode().IsRegular() || verifyFile(finalPath, staged.DigestSHA256, staged.Size) != nil {
			_ = os.Remove(stagePath)
			return PartStaged{}, ErrConflict
		}
		_ = os.Remove(stagePath)
		staged.existing = true
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(stagePath)
		return PartStaged{}, err
	}
	return staged, nil
}

func (s *Service) FinalizePart(ctx context.Context, staged PartStaged, expectedDigest string, expectedSize int64) (PartBlob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PartBlob{}, err
	}
	expectedDigest = strings.ToLower(strings.TrimSpace(expectedDigest))
	if !validDigest(staged.DigestSHA256) || staged.Size <= 0 || (expectedDigest != "" && expectedDigest != staged.DigestSHA256) || (expectedSize > 0 && expectedSize != staged.Size) {
		return PartBlob{}, errors.New("artifact part digest or size does not match finalization expectation")
	}
	dir, err := s.partRevisionDir(staged.OwnerSessionID, staged.ArtifactChainID, staged.PartID, staged.RevisionID, false)
	if err != nil {
		return PartBlob{}, err
	}
	finalPath := filepath.Join(dir, "content")
	if !staged.existing {
		if staged.token == "" {
			return PartBlob{}, errors.New("artifact part staging handle is invalid")
		}
		stagePath := filepath.Join(dir, ".stage-"+staged.token)
		if err := verifyFile(stagePath, staged.DigestSHA256, staged.Size); err != nil {
			return PartBlob{}, err
		}
		if err := os.Rename(stagePath, finalPath); err != nil {
			return PartBlob{}, fmt.Errorf("finalize artifact part bytes: %w", err)
		}
		if err := os.Chmod(finalPath, appstorage.PrivateFilePerm); err != nil {
			return PartBlob{}, err
		}
	}
	if err := verifyFile(finalPath, staged.DigestSHA256, staged.Size); err != nil {
		return PartBlob{}, err
	}
	return PartBlob{OwnerSessionID: staged.OwnerSessionID, ArtifactChainID: staged.ArtifactChainID, PartID: staged.PartID, RevisionID: staged.RevisionID, MediaType: staged.MediaType, DigestSHA256: staged.DigestSHA256, Size: staged.Size}, nil
}

func (s *Service) ReadPart(ctx context.Context, revision pebblestore.SessionArtifactPartRevision, maxBytes int64) ([]byte, PartBlob, error) {
	if err := ctx.Err(); err != nil {
		return nil, PartBlob{}, err
	}
	if revision.GraphState != pebblestore.SessionArtifactGraphAuthoritative || !validDigest(revision.DigestSHA256) || revision.Size <= 0 {
		return nil, PartBlob{}, ErrNotReady
	}
	if maxBytes <= 0 || maxBytes > s.maxArtifactLimit(revision.MediaType, "") {
		maxBytes = s.maxArtifactLimit(revision.MediaType, "")
	}
	if revision.Size > maxBytes {
		return nil, PartBlob{}, ErrQuotaExceeded
	}
	dir, err := s.partRevisionDir(revision.OwnerSessionID, revision.ArtifactChainID, revision.PartID, revision.ID, false)
	if err != nil {
		return nil, PartBlob{}, err
	}
	path := filepath.Join(dir, "content")
	if err := verifyFile(path, revision.DigestSHA256, revision.Size); err != nil {
		return nil, PartBlob{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, PartBlob{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(data)) != revision.Size {
		return nil, PartBlob{}, errors.New("artifact part bytes changed while reading")
	}
	blob := PartBlob{OwnerSessionID: revision.OwnerSessionID, ArtifactChainID: revision.ArtifactChainID, PartID: revision.PartID, RevisionID: revision.ID, MediaType: revision.MediaType, DigestSHA256: revision.DigestSHA256, Size: revision.Size}
	return data, blob, nil
}

func (s *Service) DeletePartRevision(ownerSessionID, chainID, partID, revisionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.partRevisionDir(ownerSessionID, chainID, partID, revisionID, false)
	if errors.Is(err, ErrNotReady) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.deleteOwnedDirectory(path)
}

func (s *Service) partRevisionDir(ownerSessionID, chainID, partID, revisionID string, create bool) (string, error) {
	for label, value := range map[string]string{"session": ownerSessionID, "chain": chainID, "part": partID, "part revision": revisionID} {
		if err := validateID(label, value); err != nil {
			return "", err
		}
	}
	path := filepath.Join(s.sessionDir(ownerSessionID), "parts", opaqueKey("chain", chainID), opaqueKey("part", partID), opaqueKey("part-revision", revisionID))
	if create {
		if err := mkdirPrivateUnder(s.root, path); err != nil {
			return "", err
		}
	} else if err := requireDirectoryChain(s.root, path); err != nil {
		return "", ErrNotReady
	}
	return path, nil
}
