package imagegen

import "strings"

func modalityTokens(usage map[string]any, key, modality string) int64 {
	var total int64
	add := func(m map[string]any) {
		if value, _ := m["modality"].(string); strings.EqualFold(value, modality) {
			count, _ := toInt64(m["tokenCount"])
			total += max(0, count)
		}
	}
	switch details := usage[key].(type) {
	case []any:
		for _, item := range details {
			if m, ok := item.(map[string]any); ok {
				add(m)
			}
		}
	case []map[string]any:
		for _, m := range details {
			add(m)
		}
	}
	return total
}
