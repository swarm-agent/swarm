package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

func TestSaveThinkingTagsSettingPreservesUnrelatedUISettings(t *testing.T) {
	var getCalls int
	var postBody client.UISettings
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getCalls++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.UISettings{
				Theme: client.UIThemeSettings{ActiveID: "midnight"},
				Input: client.UIInputSettings{MouseEnabled: false},
				Chat: client.UIChatSettings{
					ShowHeader:            false,
					ThinkingTags:          true,
					DefaultNewSessionMode: "plan",
					ToolStream:            client.UIChatToolStreamSettings{ShowAnchor: true},
				},
				Swarm: client.UISwarmSettings{Name: "Desk"},
			})
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&postBody); err != nil {
				t.Fatalf("decode post body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(postBody)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	api := client.New(server.URL)
	if err := saveThinkingTagsSetting(api, false); err != nil {
		t.Fatalf("saveThinkingTagsSetting: %v", err)
	}
	if getCalls != 1 {
		t.Fatalf("GET calls = %d, want 1", getCalls)
	}
	if postBody.Chat.ThinkingTags {
		t.Fatal("posted thinking tags = true, want false")
	}
	if postBody.Theme.ActiveID != "midnight" {
		t.Fatalf("posted theme active id = %q, want midnight", postBody.Theme.ActiveID)
	}
	if postBody.Swarm.Name != "Desk" {
		t.Fatalf("posted swarm name = %q, want Desk", postBody.Swarm.Name)
	}
	if postBody.Chat.DefaultNewSessionMode != "plan" {
		t.Fatalf("posted default mode = %q, want plan", postBody.Chat.DefaultNewSessionMode)
	}
}

func TestSaveSwarmNameSettingPreservesThinkingTags(t *testing.T) {
	var postBody client.UISettings
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.UISettings{
				Theme: client.UIThemeSettings{ActiveID: "crimson"},
				Chat: client.UIChatSettings{
					ShowHeader:            true,
					ThinkingTags:          false,
					DefaultNewSessionMode: "auto",
					ToolStream:            client.UIChatToolStreamSettings{ShowAnchor: true},
				},
				Swarm: client.UISwarmSettings{Name: "Local"},
			})
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&postBody); err != nil {
				t.Fatalf("decode post body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(postBody)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	api := client.New(server.URL)
	if err := saveSwarmNameSetting(api, "Desk"); err != nil {
		t.Fatalf("saveSwarmNameSetting: %v", err)
	}
	if postBody.Chat.ThinkingTags {
		t.Fatal("posted thinking tags = true, want preserved false")
	}
	if postBody.Swarm.Name != "Desk" {
		t.Fatalf("posted swarm name = %q, want Desk", postBody.Swarm.Name)
	}
}

func TestUpdateUISettingsReturnsGetFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer server.Close()

	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	api := client.New(server.URL)
	err := updateUISettings(api, func(settings *client.UISettings) {
		settings.Chat.ThinkingTags = false
	})
	if err == nil {
		t.Fatal("updateUISettings error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "load ui settings") {
		t.Fatalf("error %q does not contain load ui settings", err)
	}
}

func TestUpdateUISettingsMutateNilIsAllowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.UISettings{})
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.UISettings{})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	api := client.New(server.URL)
	if err := updateUISettings(api, nil); err != nil {
		t.Fatalf("updateUISettings nil mutate: %v", err)
	}
}
