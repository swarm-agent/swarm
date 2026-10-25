package api

import (
	"errors"
	"fmt"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// mediaSpeechSettings is shared by MCP and the in-process media bridge.
// Validate before model defaulting or admission so speech always requires an
// explicit model and uses the canonical task validator's limits and errors.
func mediaSpeechSettings(args map[string]any, kind string) (*pebblestore.ProjectSpeechSettings, error) {
	keys := []string{"text", "voice", "style", "language", "format"}
	if controlMCPString(args, "operation") != "speech" {
		for _, key := range keys {
			if controlMCPString(args, key) != "" {
				return nil, fmt.Errorf("%s requires operation speech", key)
			}
		}
		return nil, nil
	}
	speech := &pebblestore.ProjectSpeechSettings{Text: controlMCPString(args, "text"), Voice: controlMCPString(args, "voice"), Style: controlMCPString(args, "style"), Language: controlMCPString(args, "language"), Format: controlMCPString(args, "format")}
	task := &pebblestore.ProjectTaskRecord{Agent: kind, Operation: "speech", Speech: speech, Model: controlMCPString(args, "model"), DurationSeconds: controlMCPInt(args, "duration_seconds", 0), AspectRatio: controlMCPString(args, "aspect_ratio"), Resolution: controlMCPString(args, "resolution"), VariantCount: controlMCPInt(args, "count", 0)}
	if len(controlMCPList(args["inputs"])) > 0 {
		task.AttachedMedia = []pebblestore.ProjectTaskMediaRef{{}}
	}
	if err := validateSpeechTask(task); err != nil {
		return nil, err
	}
	return speech, nil
}

// admitSpeechTask is the common HTTP and native creation preflight.
func (s *Server) admitSpeechTask(account string, task *pebblestore.ProjectTaskRecord, hasExecution bool) error {
	if task.Speech == nil && task.Operation != "speech" {
		return nil
	}
	if err := validateSpeechTask(task); err != nil {
		return err
	}
	if s.audioGen == nil {
		return errors.New("speech service unavailable")
	}
	if _, err := s.audioGen.SpeechAlignmentModel(account); err != nil {
		return err
	}
	if hasExecution {
		return errors.New("speech cannot carry source execution, deliverables or prompt enhancement")
	}
	return nil
}
