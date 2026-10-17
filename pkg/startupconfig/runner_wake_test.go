package startupconfig

import (
	"strings"
	"testing"
)

// Purpose: parseEntries, Format and missingKeyLines must retain the opt-in wake
// command and reject shell syntax. This parser-level test is the narrowest
// boundary preventing unsafe startup commands and lost settings on rewrites.
func TestRunnerWakeConfig(t *testing.T) {
	const key = "environment_runner_wake_command"
	for _, value := range []string{"", "/usr/bin/wake runner-a --zone=west"} {
		cfg, seen, err := parseEntries(key+" = "+value, Default(""))
		if err != nil {
			t.Fatal(err)
		}
		line := key + " = " + value
		if !strings.Contains(Format(cfg), line+"\n") {
			t.Fatalf("render lost %q", line)
		}
		again, _, err := parseEntries(Format(cfg), Default(""))
		if err != nil || Format(again) != Format(cfg) {
			t.Fatalf("round trip: %v", err)
		}
		if strings.Contains(strings.Join(missingKeyLines(cfg, seen), "\n"), key) {
			t.Fatal("rewrite duplicates command")
		}
		if !strings.Contains(strings.Join(missingKeyLines(cfg, map[string]struct{}{}), "\n"), line) {
			t.Fatal("migration omits command")
		}
	}
	for _, value := range []string{"wake", "./wake", "/bin/wake;id", "/bin/wake|id", "/bin/wake&id", "/bin/wake$X", "/bin/wake<x", "/bin/wake>x", "/bin/wake`id`", "/bin/wake 'x'", "/bin/wake \"x\"", "/bin/wake\rarg", "/bin/wake\narg"} {
		if err := ValidateRunnerWakeCommand(value); err == nil {
			t.Fatalf("validator accepted %q", value)
		}
		if _, _, err := parseEntries(key+" = "+value, Default("")); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if _, _, err := parseEntries(key+" =\n"+key+" =", Default("")); err == nil {
		t.Fatal("accepted duplicate")
	}
	if !strings.Contains(Format(Default("")), key+" = \n") {
		t.Fatal("wake must default off")
	}
}
