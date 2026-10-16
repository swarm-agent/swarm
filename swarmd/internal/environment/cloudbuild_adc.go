package environment

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// NewGoogleREST resolves machine ADC, never a credential path from runner config.
// Its credential transport remains bounded for both startup and later refreshes.
func NewGoogleREST(ctx context.Context, project, region string) (GoogleREST, error) {
	credentialContext := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: 30 * time.Second})
	source, err := google.DefaultTokenSource(credentialContext, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return GoogleREST{}, fmt.Errorf("Google Application Default Credentials missing (attached service account or GOOGLE_APPLICATION_CREDENTIALS): %w", err)
	}
	if err := ctx.Err(); err != nil {
		return GoogleREST{}, err
	}
	token, err := source.Token()
	if err != nil {
		return GoogleREST{}, fmt.Errorf("Google Application Default Credentials unavailable (attached service account or GOOGLE_APPLICATION_CREDENTIALS): %w", err)
	}
	client := oauth2.NewClient(credentialContext, oauth2.ReuseTokenSource(token, source))
	client.Timeout = 2 * time.Minute
	return GoogleREST{Client: client, Project: project, Region: region}, nil
}

// List verifies Cloud Build access without starting a build.
func (g GoogleREST) List(ctx context.Context) error {
	_, err := g.call(ctx, http.MethodGet, g.buildsURL()+"?pageSize=1", nil, 1<<20)
	return err
}
