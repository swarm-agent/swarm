package run

import (
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: TaskReviewModel must never turn missing resolved identity into a
// slash sentinel. This pure formatting boundary is the narrowest layer proving
// incomplete preferences cannot manufacture a reviewer model.
func TestTaskReviewModel(t *testing.T) {
	for _, tc := range []struct{ provider, model, want string }{
		{"test-provider", "test-model", "test-provider/test-model"},
		{"", "", ""},
		{"test-provider", "", ""},
		{"", "test-model", ""},
		{" ", " ", ""},
	} {
		t.Run(tc.provider+"-"+tc.model, func(t *testing.T) {
			got := TaskReviewModel(store.SessionSnapshot{Preference: store.ModelPreference{Provider: tc.provider, Model: tc.model}})
			if got != tc.want {
				t.Fatalf("model %q want %q", got, tc.want)
			}
		})
	}
}
