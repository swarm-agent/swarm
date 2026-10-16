package environment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

type CloudBuildLister interface{ List(context.Context) error }

// CheckCloudBuild verifies bucket write/delete and build-list permissions; it
// deliberately never creates a build or modifies any existing landing objects.
func CheckCloudBuild(ctx context.Context, bucket string, storage CloudStorage, builds CloudBuildLister) error {
	if bucket == "" {
		return fmt.Errorf("cloudbuild_bucket is required")
	}
	id, err := pebblestore.GenerateToken(16)
	if err != nil {
		return err
	}
	prefix := "landings/check-" + id + "/"
	uploadCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	err = storage.Put(uploadCtx, bucket, prefix+"probe", bytes.NewReader([]byte("swarm cloudbuild-check\n")))
	cancel()
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cleanupCancel()
	err = errors.Join(err, storage.DeletePrefix(cleanupCtx, bucket, prefix))
	if err != nil {
		return err
	}
	return builds.List(ctx)
}
