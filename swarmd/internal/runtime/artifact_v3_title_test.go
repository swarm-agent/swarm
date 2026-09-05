package runtime

import (
	"strings"
	"testing"
)

// Requirement: authored titles are bounded plain display text; script/comment
// lookalikes and control characters must not impersonate document metadata.
// Boundary: artifactV3DocumentTitle, called on the verified Git entrypoint.
func TestArtifactV3DocumentTitle(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`<TITLE> Launch &amp; narration </TITLE>`, "Launch & narration"},
		{`<!-- <title>Fake</title> --><script>let x='<title>Fake</title>'</script><title>Real</title>`, "Real"},
		{"<title>Hi\nthere\u202e</title>", "Hi there"},
		{`<main>No title</main>`, "Untitled artifact"},
		{`<title> </title>`, "Untitled artifact"},
		{`<title>&lt;script&gt;plain&lt;/script&gt;</title>`, "<script>plain</script>"},
		{"<title>" + strings.Repeat("界", 300) + "</title>", strings.Repeat("界", 255) + "…"},
	} {
		if got := artifactV3DocumentTitle([]byte(tc.body)); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}
