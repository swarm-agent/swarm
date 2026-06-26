package privacy

import "testing"

func TestSanitizeTextPreservesNonSecretWhitespace(t *testing.T) {
	input := "  hello token=secret world  "
	got := SanitizeText(input)
	want := "  hello token=[redacted] world  "
	if got != want {
		t.Fatalf("SanitizeText() = %q, want %q", got, want)
	}
}
