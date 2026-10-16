package environment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

type CloudBuildLister interface{ List(context.Context) error }

type CloudBuildCheckStorage interface {
	CloudStorage
	BucketMetadata(context.Context, string) error
}

func (g GoogleStorage) BucketMetadata(ctx context.Context, bucket string) error {
	_, err := g.REST.call(ctx, "GET", "https://storage.googleapis.com/storage/v1/b/"+url.PathEscape(bucket), nil, 1<<20)
	var apiErr *googleAPIError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusForbidden {
		return fmt.Errorf("cannot read metadata for bucket %q: grant roles/storage.legacyBucketReader on the bucket to the build's service account (and the configured check identity if different): %w", bucket, err)
	}
	return err
}

// CheckCloudBuild verifies bucket metadata/write/delete and build-list permissions; it
// deliberately never creates a build or modifies any existing landing objects.
func CheckCloudBuild(ctx context.Context, bucket string, storage CloudBuildCheckStorage, builds CloudBuildLister) error {
	if bucket == "" {
		return fmt.Errorf("cloudbuild_bucket is required")
	}
	if err := storage.BucketMetadata(ctx, bucket); err != nil {
		return err
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
