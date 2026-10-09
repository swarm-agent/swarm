package runtime

import "testing"

// An older Tailscale Serve may forward a caller's own app-capability header,
// so tailnet identity turns on only for 1.92 and newer.
func TestTailscaleVersionAtLeast(t *testing.T) {
	for out, want := range map[string]bool{
		"1.92.0\n  tailscale commit: abc\n": true,
		"1.92.3-t1234abcd":                  true,
		"1.100.1":                           true,
		"2.0.0":                             true,
		"1.91.9":                            false,
		"1.8.0":                             false,
		"":                                  false,
		"garbage":                           false,
	} {
		if got := tailscaleVersionAtLeast(out, minServeAppCapsVersion); got != want {
			t.Errorf("%q: got %v, want %v", out, got, want)
		}
	}
}
