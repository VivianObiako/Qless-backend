package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vivianobiako/qless/api/internal/api"
	"github.com/vivianobiako/qless/api/internal/config"
	"github.com/vivianobiako/qless/api/internal/database"
	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/push"
	"github.com/vivianobiako/qless/api/internal/storage"
)

// A server that has never been given keys offers no push at all, and the
// pass reads that as "keep the in-page nudge".
func TestPushIsUnavailableWithoutKeys(t *testing.T) {
	client := newTestClient(t)
	res := client.do(http.MethodGet, "/api/push/key", nil)
	if res.status != http.StatusNotFound {
		t.Fatalf("expected 404 without VAPID keys, got %d %s", res.status, res.body)
	}
}

// A fake push service: it records what it is sent, which is how the test
// sees the nudges leave.
type pushService struct {
	mu       sync.Mutex
	received int
}

func (p *pushService) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		p.mu.Lock()
		p.received++
		p.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
}

func (p *pushService) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.received
}

func (p *pushService) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p.count() >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected %d pushes, got %d", n, p.count())
}

// Subscribing binds the phone to its entry, and being called sends exactly
// one nudge per rung climbed: the customer is second in line (close), then
// next, then up, and never hears about a frame that changed nothing.
func TestPushNudgesFollowTheLadder(t *testing.T) {
	url := config.TestDatabaseURL()
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	if err := database.Migrate(url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(store.Close)

	private, public, err := push.GenerateKeys()
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	service := &pushService{}
	fake := httptest.NewServer(service.handler())
	t.Cleanup(fake.Close)

	sender := push.New(public, private, "mailto:test@example.com")
	server := httptest.NewServer(api.NewServer(store).WithPush(sender, "http://web.test").Routes("*"))
	t.Cleanup(server.Close)
	client := &testClient{t: t, server: server}

	created := client.createQueue("Push Shop")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	slug := created.Queue.Slug

	var key struct {
		PublicKey string `json:"publicKey"`
	}
	res := client.do(http.MethodGet, "/api/push/key", nil)
	decode(t, res, &key)
	if key.PublicKey != public {
		t.Fatalf("expected the configured public key, got %q", key.PublicKey)
	}

	client.join(slug, "Ahead", "")
	me, _ := client.join(slug, "Me", "")

	// Real-looking keys: the library encrypts the payload against them, so
	// they must be a valid P-256 point and a 16-byte auth secret.
	body := map[string]any{
		"endpoint": fake.URL + "/send",
		"keys": map[string]string{
			"p256dh": "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM",
			"auth":   "tBHItJI5svbpez7KI4CCXg",
		},
	}
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/push", mustJSON(t, body),
		header{httpx.CustomerTokenHeader, me.CustomerToken})
	if res.status != http.StatusNoContent {
		t.Fatalf("subscribe: %d %s", res.status, res.body)
	}
	if res = client.do(http.MethodPost, "/api/queues/"+slug+"/push", mustJSON(t, body)); res.status != http.StatusNotFound {
		t.Fatalf("expected a subscription without a token to be refused, got %d", res.status)
	}

	// One person ahead: that is "close", and the subscription came after the
	// join, so the first frame to say so is the next one.
	client.do(http.MethodPost, "/api/queues/"+slug+"/presence",
		mustJSON(t, map[string]string{"presence": "ON_THE_WAY"}),
		header{httpx.CustomerTokenHeader, me.CustomerToken})
	service.waitFor(t, 1)

	// Ahead is called: "Me" is next. A second nudge, and only one.
	client.do(http.MethodPost, "/api/queues/"+slug+"/next", nil, owner)
	service.waitFor(t, 2)

	// Nothing about the ladder changed: no nudge.
	client.do(http.MethodPost, "/api/queues/"+slug+"/pause", nil, owner)
	time.Sleep(200 * time.Millisecond)
	if service.count() != 2 {
		t.Fatalf("a pause should not nudge anybody, got %d pushes", service.count())
	}

	// Called: the third and last.
	client.do(http.MethodPost, "/api/queues/"+slug+"/next", nil, owner)
	service.waitFor(t, 3)

	res = client.do(http.MethodDelete, "/api/queues/"+slug+"/push",
		mustJSON(t, map[string]string{"endpoint": fake.URL + "/send"}))
	if res.status != http.StatusNoContent {
		t.Fatalf("unsubscribe: %d %s", res.status, res.body)
	}
	_ = json.Valid
}
