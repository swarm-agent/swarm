package session

import "strings"

// BuildPlanDiffLines returns a simple line-oriented diff using prefixes:
// "  " for unchanged, "- " for removed, and "+ " for added lines.
func BuildPlanDiffLines(oldText, newText string) []string {
	oldLines := splitPlanLines(oldText)
	newLines := splitPlanLines(newText)
	m := len(oldLines)
	n := len(newLines)
	if m == 0 && n == 0 {
		return nil
	}
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	out := make([]string, 0, m+n)
	i, j := 0, 0
	for i < m && j < n {
		switch {
		case oldLines[i] == newLines[j]:
			out = append(out, "  "+oldLines[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, "- "+oldLines[i])
			i++
		default:
			out = append(out, "+ "+newLines[j])
			j++
		}
	}
	for ; i < m; i++ {
		out = append(out, "- "+oldLines[i])
	}
	for ; j < n; j++ {
		out = append(out, "+ "+newLines[j])
	}
	return out
}

func splitPlanLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
