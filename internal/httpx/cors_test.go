package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vivianobiako/qless/api/internal/httpx"
)

// A split deployment has more than one legitimate origin at once: the
// production domain, plus whatever preview URL the host minted for the branch
// being reviewed. Holding only one of them is what makes previews fail.
func TestOriginAllowedAcceptsEveryConfiguredOrigin(t *testing.T) {
	allowed := []string{"https://qless.app", "https://qless-git-abc.vercel.app"}

	for _, origin := range allowed {
		if !httpx.OriginAllowed(allowed, origin) {
			t.Errorf("origin %s was configured but rejected", origin)
		}
	}
}

// The whole point of the list is that everything outside it stays out. A
// prefix of a real origin is the case worth naming: qless.app.evil.test must
// not pass because it starts with the domain that does.
func TestOriginAllowedRejectsAnythingElse(t *testing.T) {
	allowed := []string{"https://qless.app"}

	for _, origin := range []string{
		"https://qless.app.evil.test",
		"https://evil.test",
		"http://qless.app",
		"",
	} {
		if httpx.OriginAllowed(allowed, origin) {
			t.Errorf("origin %q was not configured but was allowed", origin)
		}
	}
}

func TestOriginAllowedTreatsStarAsEverything(t *testing.T) {
	if !httpx.OriginAllowed([]string{"*"}, "https://anywhere.test") {
		t.Error(`"*" did not allow an arbitrary origin`)
	}
}

// The header has to name the origin that actually asked rather than the first
// one configured, or a browser rejects the response as a mismatch.
func TestCORSEchoesTheCallingOrigin(t *testing.T) {
	handler := httpx.Chain(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		httpx.CORS("https://qless.app", "https://preview.vercel.app"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/queues/demo", nil)
	req.Header.Set("Origin", "https://preview.vercel.app")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "https://preview.vercel.app" {
		t.Errorf("allow-origin = %q, want the calling origin", got)
	}
	// Without this a shared cache can hand one origin's response to another.
	if got := res.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
}

func TestCORSStaysSilentForAnUnknownOrigin(t *testing.T) {
	handler := httpx.Chain(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		httpx.CORS("https://qless.app"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/queues/demo", nil)
	req.Header.Set("Origin", "https://evil.test")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("allow-origin = %q, want no header at all", got)
	}
}
