package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

type LocalContainerUpdateJobResult = client.LocalContainerUpdateJobResult
type RemoteDeployUpdateJobResult = client.RemoteDeployUpdateJobResult

type localContainerUpdateJobResponse struct {
	OK     bool                                 `json:"ok"`
	PathID string                               `json:"path_id,omitempty"`
	Result client.LocalContainerUpdateJobResult `json:"result"`
	Error  string                               `json:"error,omitempty"`
}

type remoteDeployUpdateJobResponse struct {
	OK     bool                               `json:"ok"`
	PathID string                             `json:"path_id,omitempty"`
	Result client.RemoteDeployUpdateJobResult `json:"result"`
	Error  string                             `json:"error,omitempty"`
}

func RunDevLocalContainerUpdateJob(profile Profile) (client.LocalContainerUpdateJobResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	payload := map[string]any{
		"dev_mode":           true,
		"post_rebuild_check": true,
	}
	body, status, err := httpRequest(ctx, profile, http.MethodPost, profile.URL+"/v1/swarm/containers/local/update-job", map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}, payload)
	if err != nil {
		return client.LocalContainerUpdateJobResult{}, err
	}
	var response localContainerUpdateJobResponse
	if len(body) > 0 {
		if decodeErr := json.Unmarshal(body, &response); decodeErr != nil {
			return client.LocalContainerUpdateJobResult{}, fmt.Errorf("decode local container update job response: %w", decodeErr)
		}
	}
	if status < 200 || status >= 300 {
		message := strings.TrimSpace(response.Error)
		if message == "" {
			message = responseErrorMessage(body)
		}
		return response.Result, fmt.Errorf("local container update job failed (%d): %s", status, message)
	}
	return response.Result, nil
}

func RunDevRemoteDeployUpdateJob(profile Profile) (client.RemoteDeployUpdateJobResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	devMode := true
	payload := map[string]any{
		"dev_mode":           devMode,
		"post_rebuild_check": true,
	}
	body, status, err := httpRequest(ctx, profile, http.MethodPost, profile.URL+"/v1/deploy/remote/session/update-job", map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}, payload)
	if err != nil {
		return client.RemoteDeployUpdateJobResult{}, err
	}
	var response remoteDeployUpdateJobResponse
	if len(body) > 0 {
		if decodeErr := json.Unmarshal(body, &response); decodeErr != nil {
			return client.RemoteDeployUpdateJobResult{}, fmt.Errorf("decode remote SSH update job response: %w", decodeErr)
		}
	}
	if status < 200 || status >= 300 {
		message := strings.TrimSpace(response.Error)
		if message == "" {
			message = responseErrorMessage(body)
		}
		return response.Result, fmt.Errorf("remote SSH update job failed (%d): %s", status, message)
	}
	return response.Result, nil
}

func runDevLocalContainerUpdateJobAfterRestart(profile Profile) error {
	if strings.TrimSpace(profile.DataDir) == "" || strings.TrimSpace(profile.URL) == "" {
		return nil
	}
	fmt.Fprintln(os.Stdout, "Updating local containers onto rebuilt dev image...")
	result, err := RunDevLocalContainerUpdateJob(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Local container update needs attention: %v\n", err)
		return nil
	}
	if result.Summary.Total == 0 {
		fmt.Fprintln(os.Stdout, "No local containers need dev image replacement.")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Local containers updated: replaced=%d skipped=%d failed=%d\n", result.Summary.Replaced, result.Summary.Skipped, result.Summary.Failed)
	return nil
}

func runDevRemoteDeployUpdateJobAfterRestart(profile Profile) error {
	if strings.TrimSpace(profile.DataDir) == "" || strings.TrimSpace(profile.URL) == "" {
		return nil
	}
	fmt.Fprintln(os.Stdout, "Updating active remote SSH dev sessions onto rebuilt image...")
	result, err := RunDevRemoteDeployUpdateJob(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Remote SSH dev update needs attention: %v\n", err)
		return nil
	}
	if result.Summary.Total == 0 {
		fmt.Fprintln(os.Stdout, "No active remote SSH dev sessions need replacement.")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Remote SSH dev sessions updated: replaced=%d skipped=%d failed=%d\n", result.Summary.Replaced, result.Summary.Skipped, result.Summary.Failed)
	return nil
}
