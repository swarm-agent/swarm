package pebblestore

import (
	"testing"

	"swarm/packages/swarmd/internal/swarmmode"
)

func TestUISettingsMaxSwarmAgentsDefaultsAndNormalizes(t *testing.T) {
	defaults := DefaultUISettingsRecord()
	if defaults.Chat.MaxSwarmAgents == nil || *defaults.Chat.MaxSwarmAgents != swarmmode.DefaultMaxAgents {
		t.Fatalf("default max swarm agents = %#v", defaults.Chat.MaxSwarmAgents)
	}
	for input, want := range map[int]int{-1: 1, 0: 1, 50: 50, 101: 100} {
		record := normalizeUISettingsRecord(UISettingsRecord{Chat: UIChatSettingsRecord{MaxSwarmAgents: intPointer(input)}})
		if record.Chat.MaxSwarmAgents == nil || *record.Chat.MaxSwarmAgents != want {
			t.Fatalf("max swarm agents %d normalized to %#v, want %d", input, record.Chat.MaxSwarmAgents, want)
		}
	}
}
