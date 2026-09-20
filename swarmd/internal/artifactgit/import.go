package artifactgit

import (
	"context"
	"fmt"
	"strings"
)

// BindImportRequest durably binds an independent destination repository to one
// verified source/request fingerprint before Genesis can write its official
// head. It is not readiness or a second metadata authority. Explicit retries
// revalidate the retained source then finish the canonical session mutation.
func (r *Repository) BindImportRequest(ctx context.Context, fingerprint string) error {
	if len(fingerprint) != 64 || strings.Trim(fingerprint, "0123456789abcdef") != "" {
		return invalid("import fingerprint")
	}
	ref := "refs/swarm/import-request"
	blob, err := r.gitCmd(ctx, []byte(fingerprint), "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	oid := strings.TrimSpace(string(blob))
	existing, err := r.gitCmd(ctx, nil, "rev-parse", "--verify", ref)
	if err == nil {
		if strings.TrimSpace(string(existing)) != oid {
			return ErrConflict
		}
		return nil
	}
	if _, err := r.gitCmd(ctx, []byte(fmt.Sprintf("start\ncreate %s %s\nprepare\ncommit\n", ref, oid)), "update-ref", "--stdin"); err != nil {
		existing, readErr := r.gitCmd(ctx, nil, "rev-parse", "--verify", ref)
		if readErr == nil && strings.TrimSpace(string(existing)) == oid {
			return nil
		}
		return ErrConflict
	}
	return nil
}
