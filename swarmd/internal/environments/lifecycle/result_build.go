package lifecycle

import (
	"context"
	"errors"
	"path/filepath"

	"swarm-refactor/swarmtui/pkg/environments"
)

// ResolvedBuildProduct is supplied only by an authenticated project resolver.
// It is not a wire DTO: callers cannot provide a filesystem path as authority.
type ResolvedBuildProduct struct {
	Source  environments.CommittedBuildSource
	Root    string
	Binding string
}

type BuildProductResolver func(context.Context) (ResolvedBuildProduct, error)
type buildProductContextKey struct{}

func withBuildProduct(ctx context.Context, resolver BuildProductResolver) context.Context {
	if resolver == nil {
		return ctx
	}
	return context.WithValue(ctx, buildProductContextKey{}, resolver)
}

// applyBuildProduct retains the saved, separately authorized recipe and replaces
// only the product with the exact isolated result. Re-resolve at each admission
// and execution boundary; never export from the configured source checkout.
func applyBuildProduct(ctx context.Context, env *environments.Environment) (*ResolvedBuildProduct, error) {
	resolver, _ := ctx.Value(buildProductContextKey{}).(BuildProductResolver)
	if resolver == nil {
		return nil, nil
	}
	p, err := resolver(ctx)
	if err != nil {
		return nil, err
	}
	if env == nil || env.Build == nil || p.Binding == "" || !filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root || env.Build.Product.WorkspaceID != p.Source.WorkspaceID || env.Build.Product.WorkspaceGeneration != p.Source.WorkspaceGeneration {
		return nil, errors.New("resolved task product does not match the managed environment source")
	}
	b := *env.Build
	b.Product = p.Source
	if err := b.Validate(); err != nil {
		return nil, err
	}
	env.Build = &b
	return &p, nil
}
