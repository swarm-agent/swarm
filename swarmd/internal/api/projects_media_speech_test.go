package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"swarm/packages/swarmd/internal/audiogen"
	"swarm/packages/swarmd/internal/uisettings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: speech discovery must retain catalog token rates (without leaking
// batch pricing), and admission must agree on voice and format.
// The MCP model view and HTTP task preflight are the narrowest shared boundaries
// proving supported speech is listed/accepted and unsupported voices refused.
func TestSpeechModelAdmission(t *testing.T) {
	record := pebblestore.ModelCatalogRecord{Provider: "google", Model: "gemini-2.5-flash-preview-tts"}
	options := extractModelGenerationOptions(record)
	if options == nil {
		t.Fatal("speech hidden")
	}
	pricing := json.RawMessage(`{"billing":{"status":"verified","lines":[{"billable":"text_input","unit":"million_tokens","price_usd":0.5},{"billable":"audio_output","unit":"million_tokens","price_usd":10},{"billable":"audio_output","unit":"million_tokens","price_usd":5,"variant":"batch"}]}}`)
	view := controlMCPMediaModelView(mediaCatalogOption{ID: record.Model, Provider: "google", Ready: true, GenerationOptions: options, Pricing: pricing})
	prices := view["pricing"].(map[string]any)["prices"].([]map[string]any)
	if len(prices) != 2 || prices[0]["billable"] != "text_input" || prices[0]["price_usd"] != 0.5 || prices[1]["billable"] != "audio_output" || prices[1]["price_usd"] != float64(10) {
		t.Fatalf("prices: %+v", prices)
	}
	if len(conciseMediaPricing(pricing)["prices"].([]map[string]any)) != 0 {
		t.Fatal("speech prices leaked into non-speech view")
	}
	if len(view["voices"].([]string)) == 0 || view["formats"].([]string)[0] != "wav" {
		t.Fatalf("%+v", view)
	}
	task := &pebblestore.ProjectTaskRecord{Agent: "sound", Operation: "speech", Model: record.Model, Speech: &pebblestore.ProjectSpeechSettings{Text: "Hello", Voice: "Kore"}}
	if err := validateProjectMediaTaskSettings(nil, task); err != nil {
		t.Fatal(err)
	}
	task.Speech.Voice = "cloned"
	if err := validateProjectMediaTaskSettings(nil, task); err == nil {
		t.Fatal("unsupported voice accepted")
	}
	task.Speech.Voice = "Kore"
	task.DurationSeconds = 10
	if err := validateProjectMediaTaskSettings(nil, task); err == nil {
		t.Fatal("unsupported duration accepted")
	}
	task.DurationSeconds = 0
	task.Agent = "video"
	if err := validateProjectMediaTaskSettings(nil, task); err == nil {
		t.Fatal("speech on video accepted")
	}
}

// Purpose: generation and alignment receipts must be independently idempotent
// and principal-scoped. Real temporary storage proves duplicate receipt writes
// cannot double-charge and rejected cross-account writes leave no receipts.
func TestSpeechReceipts(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	record := pebblestore.ModelCatalogRecord{Provider: "google", Model: "speech-test", Pricing: json.RawMessage(`{"billing":{"status":"verified","lines":[{"billable":"text_input","unit":"million_tokens","price_usd":1},{"billable":"audio_output","unit":"million_tokens","price_usd":10}]}}`)}
	if err := pebblestore.NewModelCatalogStore(db).SetRecord(record); err != nil {
		t.Fatal(err)
	}
	server := &Server{sessions: session.NewService(store, nil)}
	task := &pebblestore.ProjectTaskRecord{ID: "speech-task", ProjectID: "project", AccountID: "account", Title: "Speech", Agent: "sound"}
	if err := store.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	principal := identity.Principal{AccountScopeID: "account", UserID: "user"}
	opts := pebblestore.MediaCostEstimateOptions{Provider: "google", Model: record.Model, Kind: "audio", Speech: true, PromptTokens: 100, AudioOutputTokens: 100}
	for i := 0; i < 2; i++ {
		for slot := 0; slot < 2; slot++ {
			if _, err := server.recordTaskMedia(principal, task, slot, opts, "audio/wav", 100); err != nil {
				t.Fatal(err)
			}
		}
	}
	receipts, err := store.ListMediaUsage("account", 10)
	if err != nil || len(receipts) != 2 {
		t.Fatalf("%+v %v", receipts, err)
	}
	principal.AccountScopeID = "other"
	if _, err := server.recordTaskMedia(principal, task, 0, opts, "audio/wav", 100); err == nil {
		t.Fatal("cross-account receipt accepted")
	}
	receipts, err = store.ListMediaUsage("other", 10)
	if err != nil || len(receipts) != 0 {
		t.Fatal("unauthorized receipt")
	}
}

// Purpose: the actual HTTP decoder must persist speech settings without Router
// spend and reject unsupported voices without creating a task. Temporary store
// with no provider credentials exercises admission without a live provider. Untitled
// requests exercise handleProjects/CreateProjectTask with no Router configured,
// proving speech retains its default title even when a prompt is supplied.
func TestSpeechHTTPAdmission(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	if err := store.PutProject("account", &pebblestore.ProjectRecord{ID: "project", Name: "Speech"}); err != nil {
		t.Fatal(err)
	}
	settings := uisettings.NewService(pebblestore.NewUISettingsStore(db))
	ui, err := settings.GetForAccount("account")
	if err != nil {
		t.Fatal(err)
	}
	ui.Media.TranscriptionModel = "transcription-test"
	if _, err := settings.SetForAccount("account", ui); err != nil {
		t.Fatal(err)
	}
	var record pebblestore.ModelCatalogRecord
	if err := json.Unmarshal([]byte(`{"provider":"google","model":"transcription-test","catalog_modalities":{"inputs":["audio"],"outputs":["text"]}}`), &record); err != nil {
		t.Fatal(err)
	}
	server := &Server{sessions: session.NewService(store, nil), audioGen: audiogen.NewService(nil, settings, billingCatalog{record})}
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	for _, tc := range []struct{ id, fields, title string }{
		{"untitled", "", "Speech narration"},
		{"prompt", `"prompt":"Read this aloud",`, "Speech narration"},
		{"description", `"description":"Read this aloud",`, "Speech narration"},
		{"explicit", `"title":"My narration",`, "My narration"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			body := `{"id":"` + tc.id + `",` + tc.fields + `"agent":"sound","operation":"speech","model":"gemini-2.5-flash-preview-tts","speech":{"text":"Hello","voice":"Kore"}}`
			req := httptest.NewRequest(http.MethodPost, ProjectsPath+"/project/tasks", strings.NewReader(body))
			req = req.WithContext(identity.ContextWithPrincipal(req.Context(), p))
			response := httptest.NewRecorder()
			server.handleProjects(response, req)
			if response.Code != http.StatusCreated {
				t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
			}
			task, found, err := store.GetProjectTask("account", "project", tc.id)
			if err != nil || !found || task.Title != tc.title || task.Speech == nil || task.Speech.Text != "Hello" {
				t.Fatalf("task %+v %v", task, err)
			}
		})
	}
	for _, voice := range []string{"Kore", "cloned"} {
		body := `{"id":"speech-` + voice + `","title":"Narration","agent":"sound","operation":"speech","model":"gemini-2.5-flash-preview-tts","speech":{"text":"Hello","voice":"` + voice + `"}}`
		req := httptest.NewRequest(http.MethodPost, ProjectsPath+"/project/tasks", strings.NewReader(body))
		req = req.WithContext(identity.ContextWithPrincipal(req.Context(), p))
		response := httptest.NewRecorder()
		server.handleProjects(response, req)
		if voice == "Kore" {
			if response.Code != http.StatusCreated {
				t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
			}
			task, found, err := store.GetProjectTask("account", "project", "speech-Kore")
			if err != nil || !found || task.Speech == nil || task.Speech.Text != "Hello" {
				t.Fatalf("task %+v %v", task, err)
			}
		} else {
			if response.Code != http.StatusBadRequest {
				t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
			}
			if _, found, err := store.GetProjectTask("account", "project", "speech-cloned"); err != nil || found {
				t.Fatal("rejected request persisted")
			}
		}
	}
}

// Purpose: generateTaskSpeech must persist a receipt for paid rejected output
// and refuse a retry before dispatch. A real temporary store plus fake HTTP
// exercises the orchestration/receipt boundary and durable postconditions.
func TestSpeechRejectedGenerationReceipt(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	auth := pebblestore.NewAuthStore(db)
	if _, err := auth.UpsertCredential(pebblestore.AuthCredentialInput{Provider: "google", AccountScopeID: "account", Type: "api_key", APIKey: "test-key", SetActive: true}); err != nil {
		t.Fatal(err)
	}
	settings := uisettings.NewService(pebblestore.NewUISettingsStore(db))
	ui, err := settings.GetForAccount("account")
	if err != nil {
		t.Fatal(err)
	}
	ui.Media.TranscriptionModel = "transcription-test"
	if _, err := settings.SetForAccount("account", ui); err != nil {
		t.Fatal(err)
	}
	var record pebblestore.ModelCatalogRecord
	if err := json.Unmarshal([]byte(`{"provider":"google","model":"transcription-test","catalog_modalities":{"inputs":["audio"],"outputs":["text"]}}`), &record); err != nil {
		t.Fatal(err)
	}
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":25}}`))
	}))
	defer provider.Close()
	audio := audiogen.NewService(auth, settings, billingCatalog{record})
	audio.SetBaseURL(provider.URL)
	server := &Server{sessions: session.NewService(store, nil), audioGen: audio}
	task := &pebblestore.ProjectTaskRecord{ID: "speech-task", ProjectID: "project", AccountID: "account", Title: "Speech", Agent: "sound", Operation: "speech", Model: "gemini-2.5-flash-preview-tts", Speech: &pebblestore.ProjectSpeechSettings{Text: "Hello", Voice: "Kore"}}
	if err := store.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{AccountScopeID: "account", UserID: "user"}
	server.generateTaskSpeech(p, task)
	saved, found, err := store.GetProjectTask("account", "project", task.ID)
	if err != nil || !found || saved.Status != "failed" || len(saved.Deliverables) != 1 || saved.Deliverables[0].PriceStatus == "" {
		t.Fatalf("task %+v %v", saved, err)
	}
	server.generateTaskSpeech(p, saved)
	receipts, err := store.ListMediaUsage("account", 10)
	if err != nil || len(receipts) != 1 || calls != 1 {
		t.Fatalf("receipts=%+v calls=%d err=%v", receipts, calls, err)
	}
}

// Purpose: unset timing preferences must not dead-end speech at admission or
// generation. The shared admitSpeechTask/generateTaskSpeech boundary, a temporary
// durable store and local HTTP provider prove fallback dispatch/receipts and the
// negative no-capable-model case without live provider calls.
func TestSpeechUnsetTranscription(t *testing.T) {
	for _, available := range []bool{true, false} {
		t.Run(map[bool]string{true: "catalog-default", false: "no-model"}[available], func(t *testing.T) {
			db, err := pebblestore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := pebblestore.NewSessionStore(db)
			auth := pebblestore.NewAuthStore(db)
			if _, err := auth.UpsertCredential(pebblestore.AuthCredentialInput{Provider: "google", AccountScopeID: "account", Type: "api_key", APIKey: "test-key", SetActive: true}); err != nil {
				t.Fatal(err)
			}
			settings := uisettings.NewService(pebblestore.NewUISettingsStore(db))
			var record pebblestore.ModelCatalogRecord
			if available {
				if err := json.Unmarshal([]byte(`{"provider":"google","model":"catalog-transcriber","catalog_modalities":{"inputs":["audio"],"outputs":["text"]}}`), &record); err != nil {
					t.Fatal(err)
				}
			}
			generationCalls, timingCalls := 0, 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1beta/models/gemini-2.5-flash-preview-tts:generateContent":
					generationCalls++
					_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"inlineData":{"mimeType":"audio/L16;codec=pcm;rate=24000","data":"AAA="}}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":25}}`))
				case "/v1beta/models/catalog-transcriber:generateContent":
					timingCalls++
					text := `{"duration_ms":1000,"segments":[{"start_ms":0,"end_ms":1000,"speech":"Hello"}],"words":[{"text":"Hello","start_ms":0,"end_ms":1000}]}`
					_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]string{"text": text}}}}}, "usageMetadata": map[string]any{"promptTokenCount": 20, "candidatesTokenCount": 10}})
				default:
					t.Errorf("unexpected dispatch %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer provider.Close()
			audio := audiogen.NewService(auth, settings, billingCatalog{record})
			audio.SetBaseURL(provider.URL)
			server := &Server{sessions: session.NewService(store, nil), audioGen: audio}
			task := &pebblestore.ProjectTaskRecord{ID: "speech-task", ProjectID: "project", AccountID: "account", Title: "Speech", Agent: "sound", Operation: "speech", Model: "gemini-2.5-flash-preview-tts", Speech: &pebblestore.ProjectSpeechSettings{Text: "Hello", Voice: "Charon"}}
			if err := server.admitSpeechTask("account", task, false); err != nil {
				t.Fatal(err)
			}
			if err := store.PutProjectTask("account", task); err != nil {
				t.Fatal(err)
			}
			server.generateTaskSpeech(identity.Principal{AccountScopeID: "account", UserID: "user"}, task)
			saved, found, err := store.GetProjectTask("account", "project", task.ID)
			if err != nil || !found || saved.Status != "needs_review" || saved.LastError != "" || len(saved.Deliverables) == 0 {
				t.Fatalf("task %+v err=%v", saved, err)
			}
			mime, data, ok := decodeMediaDataURL(saved.Deliverables[0].MediaURL)
			if !ok || mime != "audio/wav" || len(data) != 46 || saved.Deliverables[0].Status != "ready" || generationCalls != 1 {
				t.Fatalf("missing speech: %+v calls=%d", saved, generationCalls)
			}
			wantReceipts := 1
			if available {
				wantReceipts = 2
				if timingCalls != 1 || len(saved.Deliverables) != 2 {
					t.Fatalf("missing timings: %+v calls=%d", saved, timingCalls)
				}
				_, timingData, ok := decodeMediaDataURL(saved.Deliverables[1].MediaURL)
				var timings audiogen.SpeechTimings
				if !ok || json.Unmarshal(timingData, &timings) != nil || len(timings.Words) != 1 || timings.Words[0].Word != "Hello" || saved.Deliverables[1].PriceStatus == "" {
					t.Fatalf("timings/receipt missing: %+v", saved.Deliverables[1])
				}
			} else if timingCalls != 0 || len(saved.Deliverables) != 1 || saved.ActionNeeded != "timings unavailable: no transcription model" {
				t.Fatalf("no-model result %+v calls=%d", saved, timingCalls)
			}
			receipts, err := store.ListMediaUsage("account", 10)
			if err != nil || len(receipts) != wantReceipts {
				t.Fatalf("receipts %+v err=%v", receipts, err)
			}
			if available {
				foundModel := false
				for _, receipt := range receipts {
					if receipt.Model == "catalog-transcriber" {
						foundModel = true
					}
				}
				if !foundModel {
					t.Fatalf("missing resolved model receipt: %+v", receipts)
				}
			}
		})
	}
}
