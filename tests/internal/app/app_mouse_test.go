package app

import "testing"

func TestEnvMouseEnabled(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "default-empty-disabled", raw: "", want: false},
		{name: "explicit-enabled-1", raw: "1", want: true},
		{name: "explicit-enabled-true", raw: "true", want: true},
		{name: "explicit-enabled-yes", raw: "yes", want: true},
		{name: "explicit-enabled-on", raw: "on", want: true},
		{name: "explicit-disabled-0", raw: "0", want: false},
		{name: "explicit-disabled-false", raw: "false", want: false},
		{name: "explicit-disabled-no", raw: "no", want: false},
		{name: "explicit-disabled-off", raw: "off", want: false},
		{name: "unknown-disabled", raw: "maybe", want: false},
		{name: "trim-case-insensitive-enabled", raw: "  TrUe  ", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := envMouseEnabled(tt.raw); got != tt.want {
				t.Fatalf("envMouseEnabled(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}
