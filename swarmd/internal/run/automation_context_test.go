package run

import (
	"errors"
	"strings"
	"testing"
)

// Purpose: durableRunStateInstructions must include fresh automation evidence on
// every turn, including turns with no plan, and fail closed on evidence errors.
// This narrow prompt-composition test does not claim authorization integration.
func TestAutomationContextEveryTurn(t *testing.T) {
	calls := 0
	s := &Service{automationContext: func(string) (string, error) { calls++; return "\nUNTRUSTED AUTOMATION EVIDENCE\n", nil }}
	for i := 0; i < 2; i++ {
		text, err := s.durableRunStateInstructions("chat", "auto", "run", RunOptions{})
		if err != nil || !strings.Contains(text, "UNTRUSTED AUTOMATION EVIDENCE") {
			t.Fatalf("missing context: %q %v", text, err)
		}
	}
	if calls != 2 {
		t.Fatal("cached stale evidence")
	}
	denied := errors.New("ownership denied")
	s.automationContext = func(string) (string, error) { return "", denied }
	text, err := s.durableRunStateInstructions("chat", "auto", "run", RunOptions{})
	if !errors.Is(err, denied) || text != "" {
		t.Fatal("evidence failure was hidden")
	}
}
