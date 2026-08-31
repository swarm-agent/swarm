package swarm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"
)

const (
	MintReportURL              = "https://swarmagent.dev/api/mint"
	mintIdentifierVersion      = 1
	mintReportTimeout          = 5 * time.Second
	mintReportMaxResponseBytes = 256
)

type mintReportPayload struct {
	Version    int    `json:"version"`
	Identifier string `json:"identifier"`
}

type MintReporter struct {
	service  *Service
	client   *http.Client
	endpoint string
}

func NewMintReporter(service *Service) *MintReporter {
	return newMintReporter(service, MintReportURL, nil)
}

func newMintReporter(service *Service, endpoint string, client *http.Client) *MintReporter {
	if client == nil {
		client = &http.Client{}
	}
	boundedClient := *client
	boundedClient.Timeout = mintReportTimeout
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &MintReporter{service: service, client: &boundedClient, endpoint: endpoint}
}

func (r *MintReporter) ReportPending(ctx context.Context) error {
	if r == nil || r.service == nil || r.client == nil {
		return errors.New("mint reporter is not configured")
	}
	endpoint, err := validateMintReportEndpoint(r.endpoint)
	if err != nil {
		return err
	}
	swarmID, pending, err := r.service.PendingMintReport()
	if err != nil || !pending {
		return err
	}
	payload, err := json.Marshal(mintReportPayload{
		Version:    mintIdentifierVersion,
		Identifier: mintReportIdentifier(swarmID),
	})
	if err != nil {
		return fmt.Errorf("encode mint report: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create mint report request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := r.client.Do(request)
	if err != nil {
		return fmt.Errorf("send mint report: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, mintReportMaxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read mint report response: %w", err)
	}
	if len(body) > mintReportMaxResponseBytes {
		return errors.New("mint report response exceeded size limit")
	}
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("mint report returned status %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || mediaType != "application/json" {
			return errors.New("mint report response content type was not application/json")
		}
	}
	var accepted struct {
		Accepted   bool   `json:"accepted"`
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal(body, &accepted); err != nil || !accepted.Accepted {
		return errors.New("mint report response was not accepted")
	}
	if err := r.service.CompleteMintReportWithCredential(swarmID, accepted.Credential); err != nil {
		return fmt.Errorf("complete mint report: %w", err)
	}
	return nil
}

func validateMintReportEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse mint report endpoint: %w", err)
	}
	if endpoint.Scheme != "https" || endpoint.Hostname() != "swarmagent.dev" || endpoint.Port() != "" || endpoint.Path != "/api/mint" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil {
		return nil, errors.New("mint report endpoint is not the canonical HTTPS endpoint")
	}
	return endpoint, nil
}

func mintReportIdentifier(swarmID string) string {
	sum := sha256.Sum256([]byte("swarm-mint-v1\x00" + swarmID))
	return hex.EncodeToString(sum[:])
}
