package permission

import (
	"strings"
	"testing"
)

func TestPermissionPersistenceHelpersRedactCredentialsAndOmitOutputByDefault(t *testing.T) {
	arguments := permissionStoredArguments(`{"command":"curl https://example.invalid?token=supersecret","api_key":"sk-abcdefghijklmnop","nested":{"client-secret":"nested-secret","apiKey":"camel-secret"}}`)
	if strings.Contains(arguments, "supersecret") || strings.Contains(arguments, "sk-abcdefghijklmnop") || strings.Contains(arguments, "nested-secret") || strings.Contains(arguments, "camel-secret") {
		t.Fatalf("stored arguments contain credential material: %s", arguments)
	}
	if got := permissionStoredOutput("provider prompt and full tool output", "bash", false); got != "bash executed; detailed output omitted for privacy" {
		t.Fatalf("permissionStoredOutput() = %q", got)
	}
	if got := permissionStoredError("authorization: Bearer supersecret"); strings.Contains(got, "supersecret") {
		t.Fatalf("stored error contains credential material: %s", got)
	}
}
