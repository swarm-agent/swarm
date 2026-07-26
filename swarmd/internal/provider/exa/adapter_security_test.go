package exa

import (
	"strings"
	"testing"
)

func TestExaVerifyErrorMessageDoesNotExposeUpstreamDetail(t *testing.T) {
	secret := "customer-secret-marker"
	cases := [][]byte{
		[]byte(`{"message":"` + secret + `"}`),
		[]byte(`{"error":"` + secret + `"}`),
		[]byte(`<html>` + secret + `</html>`),
	}
	for _, raw := range cases {
		if got := exaVerifyErrorMessage(raw); strings.Contains(got, secret) {
			t.Fatalf("error message exposed upstream detail: %q", got)
		}
	}
}
