package client

// Repository state comes from the authenticated daemon, not terminal-local Git.
type OnboardingRepository struct {
	State        string `json:"state"`
	Path         string `json:"path"`
	HeadCommit   string `json:"head_commit"`
	CanSetup     bool   `json:"can_setup"`
	ContentReady bool   `json:"content_ready"`
	NeedsReview  bool   `json:"needs_review"`
	Message      string `json:"message"`
}
type OnboardingReviewFile struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Selectable bool   `json:"selectable"`
}
type OnboardingReview struct {
	Repository OnboardingRepository   `json:"repository"`
	Digest     string                 `json:"digest"`
	Files      []OnboardingReviewFile `json:"files"`
	Warning    string                 `json:"warning"`
}
type OnboardingBaseline struct {
	Path                 string   `json:"path"`
	ExpectedResolvedPath string   `json:"expected_resolved_path"`
	ReviewDigest         string   `json:"review_digest"`
	SelectedPaths        []string `json:"selected_paths"`
	ConfirmBaseline      bool     `json:"confirm_baseline"`
	ConfirmOmissions     bool     `json:"confirm_omissions"`
}
