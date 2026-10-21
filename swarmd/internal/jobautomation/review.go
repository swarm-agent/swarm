package jobautomation

import (
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// maxFindingsBytes bounds the findings an agent step's outcome keeps and a
// reopen step sends. It is the review record's own bound: findings come from
// the record (store.JobReview.Findings), already cut at a line boundary.
const maxFindingsBytes = pebblestore.MaxJobReviewFindingsBytes

// FindingsOf is the FINDINGS section of a reviewer's report (VERDICT,
// SUMMARY, FINDINGS, TESTS RUN), verbatim; "" when the report has none or it
// says None.
func FindingsOf(report string) string {
	lines := strings.Split(strings.ReplaceAll(report, "\r\n", "\n"), "\n")
	var out []string
	in := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "FINDINGS:"):
			in = true
			if rest := strings.TrimSpace(strings.TrimPrefix(line, "FINDINGS:")); rest != "" {
				out = append(out, rest)
			}
			continue
		case strings.HasPrefix(line, "TESTS RUN:"), strings.HasPrefix(line, "VERDICT:"), strings.HasPrefix(line, "SUMMARY:"):
			in = false
			continue
		}
		if in {
			out = append(out, line)
		}
	}
	findings := strings.TrimSpace(strings.Join(out, "\n"))
	if strings.EqualFold(strings.TrimRight(findings, "."), "none") {
		return ""
	}
	return findings
}
