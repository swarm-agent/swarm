package client

import (
	"encoding/json"
	"testing"
)

func TestUISettingsShowTipsJSONContract(t *testing.T) {
	disabled := false
	payload, err := json.Marshal(UISettingsPatch{
		Chat: &UIChatSettingsPatch{ShowTips: &disabled},
	})
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	if string(payload) != `{"chat":{"show_tips":false}}` {
		t.Fatalf("patch JSON = %s, want exact show_tips field", payload)
	}

	var settings UISettings
	if err := json.Unmarshal([]byte(`{"chat":{"show_tips":true}}`), &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	if !settings.Chat.ShowTips {
		t.Fatal("show tips = false after decoding true")
	}
}
