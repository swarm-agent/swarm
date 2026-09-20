package pebblestore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Import recovery data lives in the canonical destination Git repository. The
// receipt blob, exact transaction ref, and initial head become reachable in one
// update-ref transaction before the single atomic session publication.
type artifactV3ImportReceipt struct {
	Owner         ArtifactV3Owner    `json:"owner"`
	TransactionID string             `json:"transaction_id"`
	Fingerprint   string             `json:"fingerprint"`
	Now           int64              `json:"now"`
	Mutation      ArtifactV3Mutation `json:"mutation"`
}

func (r *ArtifactV3Repository) commitImportReceipt(ctx context.Context, receipt artifactV3ImportReceipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return ErrArtifactV3Quota
	}
	blob, err := r.gitCommand(ctx, raw, "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	oid := strings.TrimSpace(string(blob))
	commit := receipt.Mutation.Repository.HeadCommitOID
	refs := fmt.Sprintf("start\ncreate refs/heads/artifact %s\ncreate %s %s\ncreate refs/swarm/imports/%s %s\nprepare\ncommit\n", commit, artifactV3TransactionRef(receipt.TransactionID), commit, receipt.TransactionID, oid)
	if _, err := r.gitCommand(ctx, []byte(refs), "update-ref", "--stdin"); err != nil {
		return fmt.Errorf("%w: import refs: %v", ErrArtifactV3Conflict, err)
	}
	return nil
}

func (r *ArtifactV3Repository) readImportReceipt(ctx context.Context, tx string) (artifactV3ImportReceipt, error) {
	var receipt artifactV3ImportReceipt
	if !artifactV3IDPattern.MatchString(tx) {
		return receipt, ErrArtifactV3Invalid
	}
	ref := "refs/swarm/imports/" + tx
	sizeRaw, err := r.gitCommand(ctx, nil, "cat-file", "-s", ref)
	if err != nil {
		return receipt, ErrArtifactV3NotFound
	}
	var size int64
	if _, err := fmt.Sscan(string(sizeRaw), &size); err != nil || size < 1 || size > 4<<20 {
		return receipt, ErrArtifactV3Integrity
	}
	raw, err := r.gitCommand(ctx, nil, "cat-file", "blob", ref)
	if err != nil {
		return receipt, ErrArtifactV3Integrity
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.TransactionID != tx || receipt.Mutation.Repository == nil || receipt.Mutation.Revision == nil || receipt.Mutation.Turn == nil || receipt.Mutation.Candidate == nil || len(receipt.Fingerprint) != 64 {
		return receipt, ErrArtifactV3Integrity
	}
	return receipt, nil
}

func (s *ArtifactV3Service) applyImportReceipt(ctx context.Context, repo *ArtifactV3Repository, receipt artifactV3ImportReceipt) (ArtifactV3Projection, error) {
	m := receipt.Mutation
	if m.Repository == nil || m.Revision == nil || m.Turn == nil || m.Candidate == nil || m.Repository.Lineage == nil || m.Revision.Lineage == nil {
		return ArtifactV3Projection{}, ErrArtifactV3Integrity
	}
	if receipt.Owner != repo.owner || m.Repository.ArtifactID != repo.id || m.Repository.RepositoryID != repo.id || m.Revision.ArtifactID != repo.id || m.Revision.RepositoryID != repo.id || m.Revision.CommitOID != m.Repository.HeadCommitOID || m.Candidate.TransactionID != receipt.TransactionID {
		return ArtifactV3Projection{}, ErrArtifactV3Integrity
	}
	// Read all imported bytes before publishing recovery, not only the manifest.
	project, err := repo.ReadProject(ctx, m.Repository.HeadCommitOID)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	if err := validateArtifactV3SceneEvidence(project, m.Revision.Preview, s.limits); err != nil {
		return ArtifactV3Projection{}, err
	}
	rev, err := repo.ReadRevision(ctx, m.Repository.HeadCommitOID)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	if rev.TreeOID != m.Revision.TreeOID || rev.ManifestBlobOID != m.Revision.ManifestBlobOID {
		return ArtifactV3Projection{}, ErrArtifactV3Integrity
	}
	tx, err := repo.Transaction(ctx, receipt.TransactionID)
	if err != nil || tx.CommitOID != rev.CommitOID {
		return ArtifactV3Projection{}, ErrArtifactV3Integrity
	}
	for _, e := range []ArtifactV3EvidenceProjection{m.Revision.Build, m.Revision.Preview} {
		if !artifactV3EvidenceReady(e, rev.CommitOID) || e.InheritedFrom == nil || e.InheritedFrom.TreeOID != rev.TreeOID || e.InheritedFrom.Owner.AccountScopeID != receipt.Owner.AccountScopeID || e.InheritedFrom.Owner.UserID != receipt.Owner.UserID {
			return ArtifactV3Projection{}, ErrArtifactV3Integrity
		}
	}
	return s.apply(receipt.Owner, receipt.TransactionID, V3SessionMutationArtifactV3Imported, m, receipt.Now)
}
