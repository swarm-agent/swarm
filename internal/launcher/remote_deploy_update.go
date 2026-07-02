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

type RemoteDeployUpdateJobResult = client.RemoteDeployUpdateJobResult

type remoteDeployUpdateJobResponse struct {
	OK     bool                               `json:"ok"`
	PathID string                             `json:"path_id,omitempty"`
	Result client.RemoteDeployUpdateJobResult `json:"result"`
	Error  string                             `json:"error,omitempty"`
}

func RunRemoteDeployUpdateJob(profile Profile, devMode bool, postRebuildCheck bool) (client.RemoteDeployUpdateJobResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	payload := map[string]any{
		"dev_mode":           devMode,
		"post_rebuild_check": postRebuildCheck,
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

func RunDevRemoteDeployUpdateJob(profile Profile) (client.RemoteDeployUpdateJobResult, error) {
	return RunRemoteDeployUpdateJob(profile, true, true)
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

func runReleaseRemoteDeployUpdateJobAfterRestart(profile Profile) (client.RemoteDeployUpdateJobResult, error) {
	if strings.TrimSpace(profile.DataDir) == "" || strings.TrimSpace(profile.URL) == "" {
		return client.RemoteDeployUpdateJobResult{}, nil
	}
	fmt.Fprintln(os.Stdout, "Updating active remote SSH sessions onto release image...")
	result, err := RunRemoteDeployUpdateJob(profile, false, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Remote SSH update needs attention: %v\n", err)
		return result, err
	}
	if result.Summary.Total == 0 {
		fmt.Fprintln(os.Stdout, "No active remote SSH sessions need release replacement.")
		return result, nil
	}
	fmt.Fprintf(os.Stdout, "Remote SSH sessions updated: replaced=%d skipped=%d failed=%d\n", result.Summary.Replaced, result.Summary.Skipped, result.Summary.Failed)
	return result, nil
}
