package api

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestApplyRoutedSessionRouterTitleSetsAuthoritativeOwnership(t *testing.T) {
	req := sessionCreateRequest{
		Title: "stale title",
		Metadata: map[string]any{
			"title_locked":  "false",
			"title_pending": "true",
			"title_source":  "compact",
			"preserved":     "value",
		},
	}

	if err := applyRoutedSessionRouterTitle(&req, "  Router Owned Title  "); err != nil {
		t.Fatalf("apply Router title: %v", err)
	}
	if req.Title != "Router Owned Title" {
		t.Fatalf("title = %q, want Router Owned Title", req.Title)
	}
	if got, ok := req.Metadata["title_locked"].(bool); !ok || !got {
		t.Fatalf("title_locked = %#v, want bool true", req.Metadata["title_locked"])
	}
	if got, ok := req.Metadata["title_pending"].(bool); !ok || got {
		t.Fatalf("title_pending = %#v, want bool false", req.Metadata["title_pending"])
	}
	if req.Metadata["title_source"] != routedSessionTitleSourceRouter || req.Metadata["preserved"] != "value" {
		t.Fatalf("metadata = %#v", req.Metadata)
	}
}

func TestApplyRoutedSessionRouterTitleRejectsMissingTitleWithoutMutation(t *testing.T) {
	req := sessionCreateRequest{Title: "existing", Metadata: map[string]any{"title_pending": true}}
	if err := applyRoutedSessionRouterTitle(&req, "   "); err == nil {
		t.Fatal("expected missing Router title to fail")
	}
	if req.Title != "existing" || req.Metadata["title_pending"] != true {
		t.Fatalf("request mutated after rejection: %#v", req)
	}
}

func TestAuthoritativeSessionTitleMetadataNormalizesManualAndCompactOwnership(t *testing.T) {
	for _, source := range []string{" manual ", " COMPACT "} {
		metadata := authoritativeSessionTitleMetadata(map[string]any{
			"title_locked": "false", "title_pending": "true", "preserved": 1,
		}, source)
		if metadata["title_locked"] != true || metadata["title_pending"] != false {
			t.Fatalf("source %q title state = %#v", source, metadata)
		}
		if metadata["title_source"] != strings.ToLower(strings.TrimSpace(source)) || metadata["preserved"] != 1 {
			t.Fatalf("source %q metadata = %#v", source, metadata)
		}
	}
}

func TestRouterOwnedTitleSuppressesAPITitleGenerationForBoolAndStringMetadata(t *testing.T) {
	for _, metadata := range []map[string]any{
		{"title_locked": true, "title_pending": false, "title_source": "router"},
		{"title_locked": "true", "title_pending": "false", "title_source": "router"},
		{"title_locked": "false", "title_source": " ROUTER "},
	} {
		if shouldGenerateSessionV3Title(testSessionSnapshot(sessionV3TitleDefault, metadata)) {
			t.Fatalf("Router-owned metadata should suppress API Compact title generation: %#v", metadata)
		}
	}
}

func testSessionSnapshot(title string, metadata map[string]any) pebblestore.SessionSnapshot {
	return pebblestore.SessionSnapshot{Title: title, Metadata: metadata}
}
