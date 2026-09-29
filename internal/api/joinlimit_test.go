package api_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/vivianobiako/qless/api/internal/httpx"
)

// joinFrom joins as a phone behind Cloudflare at the given address, with an
// optional X-Forwarded-For the caller wrote themselves.
func joinFrom(c *testClient, slug, name, address, forged string) int {
	c.t.Helper()
	headers := []header{{httpx.ConnectingIPHeader, address}}
	if forged != "" {
		headers = append(headers, header{"X-Forwarded-For", forged})
	}
	return c.do(http.MethodPost, "/api/queues/"+slug+"/join", mustJSON(c.t, joinBody{Name: name}), headers...).status
}

// A queue served in order keeps the tight limit: five from one address.
func TestJoinLimitStaysTightInOrder(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Join Limit Barbers")

	for i := range 5 {
		if status := joinFrom(client, created.Queue.Slug, fmt.Sprintf("Guest %d", i), "198.51.100.30", ""); status != http.StatusCreated {
			t.Fatalf("join %d: status %d, want 201", i, status)
		}
	}
	if status := joinFrom(client, created.Queue.Slug, "Sixth", "198.51.100.30", ""); status != http.StatusTooManyRequests {
		t.Fatalf("sixth join from one address: status %d, want 429", status)
	}
}

// A draw is joined by a room on one venue connection. Thirty teams behind
// one address all get a number.
func TestDrawLetsARoomJoinFromOneAddress(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Join Limit Hackathon")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	if res := client.do(http.MethodPatch, "/api/queues/"+created.Queue.Slug,
		[]byte(`{"servingOrder":"RANDOM","maxCapacity":40}`), owner); res.status != http.StatusOK {
		t.Fatalf("make it a draw: %d %s", res.status, res.body)
	}

	for i := range 30 {
		if status := joinFrom(client, created.Queue.Slug, fmt.Sprintf("Team %d", i), "198.51.100.31", ""); status != http.StatusCreated {
			t.Fatalf("team %d from the venue address: status %d, want 201", i, status)
		}
	}
}

// Random numbers are the same room on the same connection, capped by the
// same fixed places, so they get the same limit as a draw.
func TestRandomNumbersLetARoomJoinFromOneAddress(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Join Limit Random Numbers")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	if res := client.do(http.MethodPatch, "/api/queues/"+created.Queue.Slug,
		[]byte(`{"numbering":"RANDOM","maxCapacity":40}`), owner); res.status != http.StatusOK {
		t.Fatalf("give out random numbers: %d %s", res.status, res.body)
	}

	for i := range 30 {
		if status := joinFrom(client, created.Queue.Slug, fmt.Sprintf("Team %d", i), "198.51.100.34", ""); status != http.StatusCreated {
			t.Fatalf("team %d from the venue address: status %d, want 201", i, status)
		}
	}
}

// The bypass the stress test found: writing a new address at the front of
// X-Forwarded-For used to buy a fresh limit. Behind Cloudflare it no longer
// does, and a different real address still has its own.
func TestForgedForwardedForDoesNotResetTheLimit(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Join Limit Forgery")
	slug := created.Queue.Slug

	for i := range 5 {
		joinFrom(client, slug, fmt.Sprintf("Guest %d", i), "198.51.100.32", "")
	}
	if status := joinFrom(client, slug, "Forger", "198.51.100.32", "203.0.113.77"); status != http.StatusTooManyRequests {
		t.Fatalf("forged X-Forwarded-For: status %d, want 429", status)
	}
	if status := joinFrom(client, slug, "Neighbour", "198.51.100.33", ""); status != http.StatusCreated {
		t.Fatalf("a different real address: status %d, want 201", status)
	}
}
