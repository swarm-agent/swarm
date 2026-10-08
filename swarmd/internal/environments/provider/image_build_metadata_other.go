//go:build !linux

package provider

import (
	"context"
	"errors"
	"os"
)

func (p *LocalDockerProvider) removeStoppedBuildMetadata(ctx context.Context, id, root string, owner buildOwnership, original os.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("stopped build storage cleanup requires Linux no-follow mount-safe removal; ownership retained")
}
