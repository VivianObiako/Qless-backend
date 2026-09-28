package httpx_test

import (
	"net/http/httptest"
	"testing"

	"github.com/vivianobiako/qless/api/internal/httpx"
)

// The address every limit is keyed on. Cloudflare's header wins because a
// caller can write anything at the front of X-Forwarded-For, and Render
// passes it through.
func TestClientIP(t *testing.T) {
	for _, tc := range []struct {
		name       string
		connecting string
		forwarded  string
		remote     string
		want       string
	}{
		{"Cloudflare's header wins over a forged forward", "198.51.100.20", "203.0.113.99, 198.51.100.20", "10.0.0.1:443", "198.51.100.20"},
		{"Cloudflare's header is trimmed", " 198.51.100.20 ", "", "10.0.0.1:443", "198.51.100.20"},
		{"Without Cloudflare, the first forwarded entry", "", "192.0.2.9, 10.0.0.1", "10.0.0.1:443", "192.0.2.9"},
		{"A single forwarded entry", "", " 192.0.2.9 ", "10.0.0.1:443", "192.0.2.9"},
		{"Straight in, the socket address", "", "", "192.0.2.50:5123", "192.0.2.50"},
		{"A socket address with no port", "", "", "192.0.2.50", "192.0.2.50"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			if tc.connecting != "" {
				r.Header.Set(httpx.ConnectingIPHeader, tc.connecting)
			}
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if got := httpx.ClientIP(r); got != tc.want {
				t.Errorf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
