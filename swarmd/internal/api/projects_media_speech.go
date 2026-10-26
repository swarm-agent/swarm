package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/audiogen"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func speechRequest(task *pebblestore.ProjectTaskRecord, p identity.Principal) audiogen.SpeechRequest {
	r := audiogen.SpeechRequest{Principal: p, Model: task.Model}
	if task.Speech != nil {
		r.Text, r.Voice, r.Style, r.Language, r.Format = task.Speech.Text, task.Speech.Voice, task.Speech.Style, task.Speech.Language, task.Speech.Format
	}
	return r
}
func validateSpeechTask(task *pebblestore.ProjectTaskRecord) error {
	if task.Operation != "speech" || task.Speech == nil || (task.Agent != "sound" && task.Agent != "audio") {
		return errors.New("speech requires sound kind, speech operation and speech settings")
	}
	if task.DurationSeconds != 0 || task.AspectRatio != "" || task.Resolution != "" || task.VariantCount < 0 || task.VariantCount > 1 || len(task.AttachedMedia) != 0 {
		return errors.New("speech does not accept duration, aspect ratio, resolution, multiple outputs or media inputs")
	}
	return audiogen.ValidateSpeech(speechRequest(task, identity.Principal{}))
}

func (s *Server) generateTaskSpeech(p identity.Principal, task *pebblestore.ProjectTaskRecord) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db := s.sessions.Store()
	fail := func(err error) {
		_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status, t.LastError, t.ActionNeeded = "failed", err.Error(), "Speech failed; inspect retained audio before retrying."
			for i := range t.Deliverables {
				if t.Deliverables[i].Status != "ready" {
					t.Deliverables[i].Status = "failed"
				}
			}
			return nil
		})
	}
	if err := validateSpeechTask(task); err != nil {
		fail(err)
		return
	}
	if s.audioGen == nil {
		fail(errors.New("speech service unavailable"))
		return
	}
	if _, err := s.audioGen.SpeechAlignmentModel(p.AccountScopeID); err != nil && !errors.Is(err, audiogen.ErrNoSpeechTranscriptionModel) {
		fail(err)
		return
	}
	request := speechRequest(task, p)
	// Retained output prevents an alignment retry from repeating paid synthesis.
	var audio audiogen.SpeechResult
	if len(task.Deliverables) > 0 {
		if mime, data, ok := decodeMediaDataURL(task.Deliverables[0].MediaURL); ok && mime == "audio/wav" {
			audio = audiogen.SpeechResult{Bytes: data, MediaType: mime, Model: task.Model}
		}
	}
	if len(audio.Bytes) == 0 {
		if len(task.Deliverables) > 0 && task.Deliverables[0].PriceStatus != "" {
			fail(errors.New("speech generation already incurred usage; create a new job to authorize another paid attempt"))
			return
		}
		generated, generationErr := s.audioGen.GenerateSpeech(ctx, request)
		audio = generated
		var estimate pebblestore.MediaCostEstimate
		if audio.Model != "" {
			estimate = s.recordGeneratedTaskMedia(p, task, 0, pebblestore.MediaCostEstimateOptions{Provider: "google", Model: audio.Model, Kind: "audio", Count: 1, Speech: true, PromptTokens: audio.PromptTokens, AudioOutputTokens: audio.AudioTokens, InputCharacters: int64(utf8.RuneCountInString(request.Text)), DurationSeconds: int(audio.DurationSeconds)}, audio.MediaType, len(audio.Bytes))
		}
		if generationErr != nil {
			if audio.Model != "" {
				_, err := updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
					t.Deliverables = []pebblestore.ProjectTaskDeliverable{{ID: "deliv_speech", Kind: "audio", Status: "failed", Model: audio.Model, MediaCostEstimate: estimate}}
					return nil
				})
				if err != nil {
					fail(err)
					return
				}
			}
			fail(generationErr)
			return
		}
		saved, err := updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Deliverables = []pebblestore.ProjectTaskDeliverable{{ID: "deliv_speech", Title: t.Title, Kind: "audio", Status: "ready", Model: audio.Model, MediaURL: "data:" + audio.MediaType + ";base64," + base64.StdEncoding.EncodeToString(audio.Bytes), MediaCostEstimate: estimate}, {ID: "deliv_timings", Title: "Speech timings", Kind: "json", Status: "pending"}}
			return nil
		})
		if err != nil {
			fail(err)
			return
		}
		task = saved
	}
	if len(task.Deliverables) > 1 && task.Deliverables[1].Status == "ready" {
		_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error { t.Status = "needs_review"; return nil })
		return
	}
	if len(task.Deliverables) > 1 && task.Deliverables[1].PriceStatus != "" {
		fail(errors.New("timing generation already incurred usage; create a new job to authorize another paid attempt"))
		return
	}
	var timings audiogen.SpeechTimings
	var timingModel string
	var estimate pebblestore.MediaCostEstimate
	if audio.Timings != nil {
		timings = *audio.Timings
	} else {
		aligned, err := s.audioGen.AlignSpeech(ctx, request, audio)
		if errors.Is(err, audiogen.ErrNoSpeechTranscriptionModel) {
			_, saveErr := updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
				t.Deliverables = t.Deliverables[:1]
				t.Status, t.LastError, t.ActionNeeded = "needs_review", "", err.Error()
				t.WhatDidDo = []string{"Generated speech", err.Error()}
				return nil
			})
			if saveErr != nil {
				fail(saveErr)
			}
			return
		}
		timingModel = aligned.Model
		if aligned.Model != "" {
			estimate = s.recordGeneratedTaskMedia(p, task, 1, pebblestore.MediaCostEstimateOptions{Provider: "google", Model: aligned.Model, Kind: "audio", Count: 1, Transcription: true, PromptTokens: aligned.PromptTokens, AudioInputTokens: aligned.AudioInputTokens, TextOutputTokens: aligned.OutputTokens}, "application/json", 0)
		}
		if err != nil {
			if estimate.PriceStatus != "" {
				_, _ = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
					if len(t.Deliverables) > 1 {
						t.Deliverables[1].MediaCostEstimate = estimate
					}
					return nil
				})
			}
			fail(err)
			return
		}
		timings = aligned.Timings
	}
	data, err := json.Marshal(timings)
	if err != nil {
		fail(err)
		return
	}
	_, err = updateProjectTaskWithRetry(db, p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
		timingFile := pebblestore.ProjectTaskDeliverable{ID: "deliv_timings", Title: "Speech timings", Kind: "json", Status: "ready", Model: timingModel, MediaURL: "data:application/json;base64," + base64.StdEncoding.EncodeToString(data), MediaCostEstimate: estimate}
		if len(t.Deliverables) == 1 {
			t.Deliverables = append(t.Deliverables, timingFile)
		} else {
			t.Deliverables[1] = timingFile
		}
		t.Status, t.LastError, t.ActionNeeded = "needs_review", "", "Speech audio and timing JSON ready for review."
		t.WhatDidDo = []string{"Generated speech", "Transcribed generated audio for " + timings.Granularity + " timings"}
		return nil
	})
	if err != nil {
		fail(err)
	}
}
