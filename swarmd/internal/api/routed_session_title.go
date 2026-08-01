package api

import (
	"errors"
	"strings"
	"unicode/utf8"

	routerruntime "swarm/packages/swarmd/internal/router"
)

const routedSessionTitleSourceRouter = "router"

// applyRoutedSessionRouterTitle transfers the Router's validated title into the
// durable create request and records that Router, rather than Compact, owns it.
// The canonical bool values intentionally replace JSON/string-shaped caller
// metadata so every title-generation path sees an unambiguous lock.
func applyRoutedSessionRouterTitle(req *sessionCreateRequest, title string) error {
	if req == nil {
		return errors.New("routed session create request is required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("Router title is required")
	}
	if utf8.RuneCountInString(title) > routerruntime.MaxTitleRunes {
		return errors.New("Router title exceeds the maximum length")
	}
	metadata := authoritativeSessionTitleMetadata(req.Metadata, routedSessionTitleSourceRouter)
	req.Title = title
	req.Metadata = metadata
	return nil
}

func authoritativeSessionTitleMetadata(existing map[string]any, source string) map[string]any {
	metadata := cloneSessionsV3Metadata(existing)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["title_locked"] = true
	metadata["title_pending"] = false
	metadata["title_source"] = strings.ToLower(strings.TrimSpace(source))
	return metadata
}
