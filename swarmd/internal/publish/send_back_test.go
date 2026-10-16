package publish

import (
	"strings"
	"testing"
)

// Purpose: ChecksSendBack must retain a failing check's diagnostics rather than
// a passing tail. The formatter is the narrowest boundary for reproducing lost
// failure context without executing a full landing or external check commands.
func TestChecksSendBackFailureContext(t *testing.T) {
	output := strings.Repeat("PASS earlier suite\n", 2000) +
		"important setup context\n--- FAIL: TestLanding (0.01s)\n    landing_test.go:42: wanted success\n" +
		strings.Repeat("PASS later suite\n", 2000) + "PASS installer artifact integrity\n"
	reason := ChecksSendBack(Base{Branch: "dev"}, []string{"bash scripts/test-vital.sh"}, false, output).Reason
	for _, want := range []string{"important setup context", "--- FAIL: TestLanding", "landing_test.go:42", "PASS installer artifact integrity"} {
		if !strings.Contains(reason, want) {
			t.Errorf("send-back missing %q", want)
		}
	}
}

// Purpose: CheckOutputExcerpt owns tool-marker recognition, context bounds and
// the fallback budget. Direct unit cases prove these without shell/tool variance
// and ensure reformatting stored excerpts cannot diverge from send-back text.
func TestCheckOutputExcerpt(t *testing.T) {
	for _, marker := range []string{"--- FAIL", "FAIL\tpkg", "panic: broken", "✖ test", "not ok 1", "Error: broken", "error: broken", "FAILED test", "exit status 1", "exited with code 2", "non-zero exit"} {
		t.Run(marker, func(t *testing.T) {
			output := strings.Repeat("PASS padding\n", 2000) + "outside-before\n" + strings.Repeat("before\n", 40) + marker + "\n" + strings.Repeat("after\n", 20) + "outside-after\n" + strings.Repeat("PASS padding\n", 2000)
			excerpt := CheckOutputExcerpt(output)
			if !strings.Contains(excerpt, marker) || strings.Count(excerpt, "before\n") != 40 || strings.Count(excerpt, "after\n") != 20 || strings.Contains(excerpt, "outside-") {
				t.Fatalf("incorrect failure window: %q", excerpt)
			}
			if len(excerpt) > 16*1024 || CheckOutputExcerpt(excerpt) != excerpt {
				t.Fatal("excerpt exceeds budget or is not stable")
			}
		})
	}
	output := strings.Repeat("passing output\n", 3000)
	if got := CheckOutputExcerpt(output); got != output[len(output)-16*1024:] {
		t.Fatal("unmatched output must fall back to last 16 KB")
	}
	output = strings.Repeat("--- FAIL: repeated\n", 3000)
	if got := CheckOutputExcerpt(output); len(got) > 16*1024 || !strings.Contains(got, "--- FAIL") {
		t.Fatal("repeated failures must stay bounded")
	}
}
