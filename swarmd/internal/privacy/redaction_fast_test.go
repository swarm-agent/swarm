package privacy

import (
	"regexp"
	"strings"
	"testing"
)

func sanitizeTextReference(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	out := redactJSONSecretPattern.ReplaceAllString(raw, `$1[redacted]$3`)
	out = redactQuerySecretPattern.ReplaceAllString(out, `$1=[redacted]`)
	out = redactBearerPattern.ReplaceAllString(out, `$1[redacted]`)
	out = redactJWTPattern.ReplaceAllString(out, `[redacted.jwt]`)
	return redactSKPattern.ReplaceAllString(out, `[redacted.api_key]`)
}

// Purpose: SanitizeText must preserve the original redaction contract, including
// Unicode folding and nonmatches; direct equivalence is the narrowest layer that
// detects a pre-check accidentally leaking credentials or changing plain text.
func TestSanitizeTextFastChecksEquivalent(t *testing.T) {
	inputs := []string{"", " \t\n", "package main\nfunc main() { println(42) }", "Bearer [redacted]", "bEaReR abc_DEF-123", strings.Repeat("a", 20) + "." + strings.Repeat("b", 20) + "." + strings.Repeat("c", 20), "sk-abcdefghijklmnop", "SK-abcdefghijklmnop", `{"ſecret":"value","toKen":"value"}`, "ſecret=value&toKen=value", "plain café", "token no delimiter", "x:y=z", "a.b", "sk-short"}
	for _, key := range []string{"access_token", "refresh_token", "id_token", "api_key", "authorization", "token", "secret", "password", "passwd", "private_key", "client_secret"} {
		mixed := strings.ToUpper(key[:1]) + key[1:]
		inputs = append(inputs, `{"`+mixed+`":"value"}`, mixed+"=value&other=ok", mixed+` : "value"`)
	}
	// Construct synthetic lowercase-letter/digit tokens rather than embedding
	// credential-looking literals that output sanitizers might replace.
	token := strings.Repeat("a1b2c3", 4)
	for _, input := range []string{
		"Bearer " + token,
		"authorization: bearer " + token,
		"prefix sk-" + token + " suffix",
	} {
		if sanitizeTextReference(input) == input {
			t.Fatal("positive credential fixture did not redact")
		}
		inputs = append(inputs, input)
	}
	for _, pattern := range []struct {
		name        string
		re          *regexp.Regexp
		replacement string
	}{
		{"json", redactJSONSecretPattern, `$1[redacted]$3`},
		{"query", redactQuerySecretPattern, `$1=[redacted]`},
		{"bearer", redactBearerPattern, `$1[redacted]`},
		{"jwt", redactJWTPattern, `[redacted.jwt]`},
		{"sk", redactSKPattern, `[redacted.api_key]`},
	} {
		rewritten := false
		for _, input := range inputs {
			if pattern.re.ReplaceAllString(input, pattern.replacement) != input && sanitizeTextReference(input) != input {
				rewritten = true
				break
			}
		}
		if !rewritten {
			t.Fatalf("no positive fixture for %s pattern", pattern.name)
		}
	}
	inputs = append(inputs, strings.Join(inputs, "\n"))
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			if got, want := SanitizeText(input), sanitizeTextReference(input); got != want {
				t.Fatalf("got %q; want %q", got, want)
			}
		})
	}
}

// Purpose: measure the actual old and new SanitizeText implementations on the
// same ~50 KB secret-free Go source; this is a microbenchmark, not an agent run.
func BenchmarkSanitizeTextGoSource(b *testing.B) {
	source := strings.Repeat("func sum(values []int) int { total := 0; for _, v := range values { total += v }; return total }\n", 540)
	for _, tc := range []struct {
		name string
		fn   func(string) string
	}{{"before", sanitizeTextReference}, {"after", SanitizeText}} {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(source)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				tc.fn(source)
			}
		})
	}
}
