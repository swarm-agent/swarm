package jobautomation

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"time"
	"unicode/utf8"
)

// Trigger starts work from an external item; it never selects a model.
type Trigger struct {
	Event    TriggerEvent     `json:"event,omitempty"`
	Schedule *TriggerSchedule `json:"schedule,omitempty"`
	Start    TriggerStart     `json:"start"`
}

// MarshalJSON omits the event source for schedule definitions.
func (t Trigger) MarshalJSON() ([]byte, error) {
	type wire struct {
		Event    *TriggerEvent    `json:"event,omitempty"`
		Schedule *TriggerSchedule `json:"schedule,omitempty"`
		Start    TriggerStart     `json:"start"`
	}
	var event *TriggerEvent
	if t.Schedule == nil || t.Event != (TriggerEvent{}) {
		event = &t.Event
	}
	return json.Marshal(wire{event, t.Schedule, t.Start})
}

func (t *Trigger) UnmarshalJSON(data []byte) error {
	type plain Trigger
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, event := fields["event"]; event {
		if _, schedule := fields["schedule"]; schedule {
			return invalid("exactly one of on.event and on.schedule is required")
		}
	}
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*t = Trigger(value)
	return nil
}

type TriggerSchedule struct {
	Cron     string `json:"cron"`
	Timezone string `json:"timezone,omitempty"`
}

type TriggerEvent struct {
	Source       string `json:"source"`
	URL          string `json:"url,omitempty"`
	EverySeconds int    `json:"every_seconds,omitempty"`
	Secret       string `json:"secret,omitempty"`
	Items        string `json:"items,omitempty"`
	ID           string `json:"id"`
}
type TriggerStart struct {
	Worker string `json:"worker"`
	Brief  string `json:"brief"`
}

// InvalidTrigger is a save-time refusal from a deployment or secret authority.
func InvalidTrigger(message string) error { return invalid("%s", message) }

func (a *Automation) validateTrigger() error {
	if a.On == nil {
		return nil
	}
	t := a.On
	if strings.TrimSpace(t.Start.Worker) == "" || len(t.Start.Worker) > 200 || IsBuiltinAgent(t.Start.Worker) {
		return invalid("on.start.worker must be a deployed worker id")
	}
	if strings.TrimSpace(t.Start.Brief) == "" || utf8.RuneCountInString(t.Start.Brief) > maxBriefRunes {
		return invalid("on.start.brief is required (at most %d characters)", maxBriefRunes)
	}
	if a.Scope.Worker != "" && a.Scope.Worker != t.Start.Worker {
		return invalid("scope.worker must equal on.start.worker")
	}
	if a.Scope.Role != "" || a.Scope.Job != "" || a.Scope.Schedule != "" {
		return invalid("a trigger uses scope.folder and optional scope.worker only")
	}
	if t.Schedule != nil {
		if t.Event != (TriggerEvent{}) {
			return invalid("exactly one of on.event and on.schedule is required")
		}
		if _, err := pebblestore.ParseCron(t.Schedule.Cron); err != nil {
			return invalid("on.schedule.cron: %v", err)
		}
		if t.Schedule.Timezone == "" {
			t.Schedule.Timezone = "UTC"
		}
		if t.Schedule.Timezone == "Local" {
			return invalid("on.schedule.timezone must be an IANA timezone")
		}
		if _, err := time.LoadLocation(t.Schedule.Timezone); err != nil {
			return invalid("on.schedule.timezone: %v", err)
		}
		return nil
	}
	if t.Event.ID == "" || len(t.Event.ID) > 200 || len(t.Event.Items) > 200 {
		return invalid("on.event.id is required; item paths are limited to 200 bytes")
	}
	switch t.Event.Source {
	case "pull":
		u, err := url.Parse(t.Event.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return invalid("on.event.url must be an https URL without credentials or fragment")
		}
		if t.Event.EverySeconds < 30 || t.Event.EverySeconds > 86400 {
			return invalid("on.event.every_seconds must be 30 to 86400")
		}
	case "webhook":
		if t.Event.URL != "" || t.Event.EverySeconds != 0 || t.Event.Secret != "" || t.Event.Items != "" {
			return invalid("webhook events do not accept pull options")
		}
	default:
		return invalid("on.event.source must be pull or webhook")
	}
	return nil
}

var eventPlaceholder = regexp.MustCompile(`\{\{event\.([^{}]+)\}\}`)

func eventField(item any, path string) any {
	for _, field := range strings.Split(path, ".") {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil
		}
		item = obj[field]
	}
	return item
}
func eventText(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

// RenderEvent replaces only event placeholders, without recursively interpreting data.
func RenderEvent(brief string, item map[string]any) string {
	return eventPlaceholder.ReplaceAllStringFunc(brief, func(match string) string {
		return eventText(eventField(item, eventPlaceholder.FindStringSubmatch(match)[1]))
	})
}
