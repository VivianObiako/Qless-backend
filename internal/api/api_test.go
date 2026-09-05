package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vivianobiako/qless/api/internal/api"
	"github.com/vivianobiako/qless/api/internal/config"
	"github.com/vivianobiako/qless/api/internal/database"
	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/storage"
)

type testClient struct {
	t      *testing.T
	server *httptest.Server
}

func newTestClient(t *testing.T) *testClient {
	t.Helper()

	url := config.TestDatabaseURL()
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; run `make up` and copy .env.example to .env")
	}
	if err := database.Migrate(url); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	store, err := storage.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(store.Close)

	server := httptest.NewServer(api.NewServer(store).Routes("*"))
	t.Cleanup(server.Close)

	return &testClient{t: t, server: server}
}

type response struct {
	status int
	body   []byte
}

func decode[T any](t *testing.T, r response, dst *T) {
	t.Helper()
	if err := json.Unmarshal(r.body, dst); err != nil {
		t.Fatalf("decode response %s: %v", r.body, err)
	}
}

func mustJSON[T any](t *testing.T, value T) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	return encoded
}

type createQueueBody struct {
	Name                  string `json:"name"`
	AverageServiceMinutes int    `json:"averageServiceMinutes"`
}

type joinBody struct {
	Name string `json:"name"`
}

type header struct{ key, value string }

func (c *testClient) do(method, path string, body []byte, headers ...header) response {
	c.t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, c.server.URL+path, reader)
	if err != nil {
		c.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, h := range headers {
		req.Header.Set(h.key, h.value)
	}

	res, err := c.server.Client().Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()

	responseBody, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	return response{status: res.StatusCode, body: responseBody}
}

type createdQueue struct {
	Queue struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	} `json:"queue"`
	OwnerToken   string `json:"ownerToken"`
	RecoveryCode string `json:"recoveryCode"`
}

func (c *testClient) createQueue(name string) createdQueue {
	c.t.Helper()

	created := c.createQueueAs(name, "")
	if created.RecoveryCode == "" {
		c.t.Fatal("create queue returned no recovery code for a new owner")
	}
	return created
}

// createQueueAs creates a queue as an existing owner when given their session
// token, and as a new business when given none.
func (c *testClient) createQueueAs(name, ownerToken string) createdQueue {
	c.t.Helper()

	headers := []header{}
	if ownerToken != "" {
		headers = append(headers, header{"Authorization", "Bearer " + ownerToken})
	}

	res := c.do(http.MethodPost, "/api/queues", mustJSON(c.t, createQueueBody{Name: name, AverageServiceMinutes: 15}), headers...)
	if res.status != http.StatusCreated {
		c.t.Fatalf("create queue: status %d, body %s", res.status, res.body)
	}

	var created createdQueue
	decode(c.t, res, &created)
	if created.OwnerToken == "" {
		c.t.Fatal("create queue returned no owner token")
	}
	return created
}

type joinResult struct {
	CustomerToken string `json:"customerToken"`
	AlreadyJoined bool   `json:"alreadyJoined"`
	PeopleAhead   int    `json:"peopleAhead"`
	Entry         struct {
		Number int    `json:"number"`
		Status string `json:"status"`
	} `json:"entry"`
}

func (c *testClient) join(slug, name string, customerToken string) (joinResult, response) {
	c.t.Helper()

	headers := []header{}
	if customerToken != "" {
		headers = append(headers, header{httpx.CustomerTokenHeader, customerToken})
	}

	res := c.do(http.MethodPost, "/api/queues/"+slug+"/join", mustJSON(c.t, joinBody{Name: name}), headers...)

	var result joinResult
	if res.status == http.StatusCreated || res.status == http.StatusOK {
		decode(c.t, res, &result)
	}
	return result, res
}

// Every administrative endpoint must reject a caller who does not hold the
// owner token. Hiding buttons in the frontend is not authorization.
func TestOperatorEndpointsRejectNonOwners(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Authorization Test Shop")

	// A queue id says which queue, never who the caller is. A perfectly valid
	// session token belonging to a different business must not open this one.
	stranger := client.createQueue("Someone Else's Shop")

	// A plausible-looking entry id. Authorization runs before anything is
	// looked up, so these must answer 401 rather than 404 — an unauthorized
	// caller should not learn whether an entry exists.
	const someEntry = "11111111-2222-3333-4444-555555555555"

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/queues/" + created.Queue.ID + "/entries"},
		{http.MethodGet, "/api/queues/" + created.Queue.ID + "/history"},
		{http.MethodPatch, "/api/queues/" + created.Queue.ID},
		{http.MethodPost, "/api/queues/" + created.Queue.ID + "/next"},
		{http.MethodPost, "/api/queues/" + created.Queue.ID + "/entries/" + someEntry + "/serve"},
		{http.MethodPost, "/api/queues/" + created.Queue.ID + "/entries/" + someEntry + "/attend"},
		{http.MethodPost, "/api/queues/" + created.Queue.ID + "/entries/" + someEntry + "/skip"},
		{http.MethodPost, "/api/queues/" + created.Queue.ID + "/pause"},
		{http.MethodPost, "/api/queues/" + created.Queue.ID + "/resume"},
		{http.MethodPost, "/api/queues/" + created.Queue.ID + "/close"},
		{http.MethodPost, "/api/queues/" + created.Queue.ID + "/reset"},
	}

	credentials := []struct {
		name    string
		headers []header
	}{
		{name: "no token", headers: nil},
		{name: "wrong token", headers: []header{{"Authorization", "Bearer not-the-owner-token"}}},
		{name: "malformed header", headers: []header{{"Authorization", created.OwnerToken}}},
		{name: "customer token instead of owner token", headers: []header{{"Authorization", "Bearer " + strings.Repeat("a", 43)}}},
		{name: "another owner's valid token", headers: []header{{"Authorization", "Bearer " + stranger.OwnerToken}}},
	}

	for _, endpoint := range endpoints {
		for _, credential := range credentials {
			t.Run(endpoint.method+" "+endpoint.path+" with "+credential.name, func(t *testing.T) {
				res := client.do(endpoint.method, endpoint.path, nil, credential.headers...)
				if res.status != http.StatusUnauthorized {
					t.Errorf("status = %d, want 401; body %s", res.status, res.body)
				}
			})
		}
	}
}

func TestOperatorEndpointsAcceptOwner(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Owner Access Shop")
	auth := header{"Authorization", "Bearer " + created.OwnerToken}

	if _, res := client.join(created.Queue.Slug, "Vivian", ""); res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	res := client.do(http.MethodGet, "/api/queues/"+created.Queue.ID+"/entries", nil, auth)
	if res.status != http.StatusOK {
		t.Fatalf("list entries: status %d, body %s", res.status, res.body)
	}

	res = client.do(http.MethodPost, "/api/queues/"+created.Queue.ID+"/next", nil, auth)
	if res.status != http.StatusOK {
		t.Fatalf("serve next: status %d, body %s", res.status, res.body)
	}

	var view struct {
		Serving *struct {
			Number int    `json:"number"`
			Name   string `json:"customerName"`
		} `json:"serving"`
	}
	decode(t, res, &view)
	if view.Serving == nil || view.Serving.Number != 1 {
		t.Fatalf("serving = %+v, want number 1", view.Serving)
	}
}

// The public payload is what reaches every customer phone and the lobby
// display. A name appearing here would leak one customer's identity to
// everyone else in the shop.
func TestPublicQueueStateContainsNoCustomerNames(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Privacy Test Shop")

	const secretName = "Zebediah Q Featherstonehaugh"
	if _, res := client.join(created.Queue.Slug, secretName, ""); res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	res := client.do(http.MethodGet, "/api/queues/"+created.Queue.Slug, nil)
	if res.status != http.StatusOK {
		t.Fatalf("get queue: status %d, body %s", res.status, res.body)
	}

	var view struct {
		State json.RawMessage `json:"state"`
		Entry json.RawMessage `json:"entry"`
	}
	decode(t, res, &view)

	if bytes.Contains(view.State, []byte(secretName)) {
		t.Errorf("public state leaked a customer name: %s", view.State)
	}
	if string(view.Entry) != "null" {
		t.Errorf("entry = %s, want null for a caller with no customer token", view.Entry)
	}
}

// A customer presenting their own token sees their own entry, and only theirs.
func TestCustomerRecoversOwnEntryWithToken(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Recovery Test Shop")

	first, res := client.join(created.Queue.Slug, "Vivian", "")
	if res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	// A second customer joins from a different browser.
	if _, res := client.join(created.Queue.Slug, "John", ""); res.status != http.StatusCreated {
		t.Fatalf("second join: status %d, body %s", res.status, res.body)
	}

	// Vivian closes her browser and returns with only her stored token.
	res = client.do(http.MethodGet, "/api/queues/"+created.Queue.Slug+"/me", nil,
		header{httpx.CustomerTokenHeader, first.CustomerToken})
	if res.status != http.StatusOK {
		t.Fatalf("recover entry: status %d, body %s", res.status, res.body)
	}

	var view struct {
		Entry struct {
			Number int    `json:"number"`
			Name   string `json:"customerName"`
		} `json:"entry"`
	}
	decode(t, res, &view)

	if view.Entry.Number != first.Entry.Number {
		t.Errorf("recovered number %d, want %d", view.Entry.Number, first.Entry.Number)
	}
	if view.Entry.Name != "Vivian" {
		t.Errorf("recovered name %q, want Vivian", view.Entry.Name)
	}
}

func TestRejoinReturnsExistingPositionRatherThanADuplicate(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Duplicate Test Shop")

	first, res := client.join(created.Queue.Slug, "Vivian", "")
	if res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	second, res := client.join(created.Queue.Slug, "Vivian", first.CustomerToken)
	if res.status != http.StatusOK {
		t.Fatalf("rejoin: status %d, want 200; body %s", res.status, res.body)
	}
	if !second.AlreadyJoined {
		t.Error("alreadyJoined = false, want true")
	}
	if second.Entry.Number != first.Entry.Number {
		t.Errorf("rejoin number %d, want the original %d", second.Entry.Number, first.Entry.Number)
	}
}

func TestJoinValidatesName(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Validation Test Shop")

	for _, name := range []string{"", "   ", "\t\n"} {
		res := client.do(http.MethodPost, "/api/queues/"+created.Queue.Slug+"/join", mustJSON(t, joinBody{Name: name}))
		if res.status != http.StatusBadRequest {
			t.Errorf("join with name %q: status %d, want 400", name, res.status)
		}

		var body httpx.ErrorBody
		decode(t, res, &body)
		if body.Message == "" {
			t.Error("validation error carried no message for the customer")
		}
	}
}

func TestUnknownQueueReturnsFriendlyNotFound(t *testing.T) {
	client := newTestClient(t)

	res := client.do(http.MethodGet, "/api/queues/no-such-barbershop", nil)
	if res.status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.status)
	}

	var body httpx.ErrorBody
	decode(t, res, &body)
	if body.Error != "queue_not_found" {
		t.Errorf("error code = %q, want queue_not_found", body.Error)
	}
	if !strings.Contains(body.Message, "couldn't find") {
		t.Errorf("message = %q, want something a customer can read", body.Message)
	}
}

// A customer can say where they are, the counter sees it, and the public
// state never does: presence is about one person and rides on entries only.
func TestCustomerPresenceReachesTheCounterButNotThePublic(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Presence Shop")
	slug := created.Queue.Slug

	joined, res := client.join(slug, "Ngozi", "")
	if res.status != http.StatusCreated {
		t.Fatalf("join: %d %s", res.status, res.body)
	}

	// Nobody has said anything yet.
	var entries struct {
		Waiting []struct {
			Number   int     `json:"number"`
			Presence *string `json:"presence"`
		} `json:"waiting"`
	}
	res = client.do(http.MethodGet, "/api/queues/"+slug+"/entries", nil,
		header{"Authorization", "Bearer " + created.OwnerToken})
	if res.status != http.StatusOK {
		t.Fatalf("entries: %d %s", res.status, res.body)
	}
	decode(t, res, &entries)
	if len(entries.Waiting) != 1 || entries.Waiting[0].Presence != nil {
		t.Fatalf("expected one silent waiting row, got %+v", entries.Waiting)
	}

	// Saying "here" lands on the entry and comes back on the customer's view.
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/presence",
		mustJSON(t, map[string]string{"presence": "here"}),
		header{httpx.CustomerTokenHeader, joined.CustomerToken})
	if res.status != http.StatusOK {
		t.Fatalf("set presence: %d %s", res.status, res.body)
	}
	var view struct {
		Entry struct {
			Presence *string `json:"presence"`
		} `json:"entry"`
	}
	decode(t, res, &view)
	if view.Entry.Presence == nil || *view.Entry.Presence != "HERE" {
		t.Fatalf("expected HERE on the customer's entry, got %v", view.Entry.Presence)
	}

	// The counter sees it.
	res = client.do(http.MethodGet, "/api/queues/"+slug+"/entries", nil,
		header{"Authorization", "Bearer " + created.OwnerToken})
	decode(t, res, &entries)
	if entries.Waiting[0].Presence == nil || *entries.Waiting[0].Presence != "HERE" {
		t.Fatalf("expected the counter to see HERE, got %v", entries.Waiting[0].Presence)
	}

	// The public state does not carry it, or anything else per person.
	res = client.do(http.MethodGet, "/api/queues/"+slug, nil)
	if res.status != http.StatusOK {
		t.Fatalf("public state: %d %s", res.status, res.body)
	}
	if bytes.Contains(res.body, []byte(`"presence"`)) {
		t.Fatalf("public state leaked presence: %s", res.body)
	}

	// Nonsense is refused, and so is a customer with no place in the queue.
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/presence",
		mustJSON(t, map[string]string{"presence": "teleporting"}),
		header{httpx.CustomerTokenHeader, joined.CustomerToken})
	if res.status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown presence, got %d %s", res.status, res.body)
	}
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/presence",
		mustJSON(t, map[string]string{"presence": "here"}))
	if res.status == http.StatusOK {
		t.Fatalf("expected a customer with no token to be refused, got %d", res.status)
	}
}

// Staff can put a person in the queue from the counter. They hold a number
// like anyone else, the counter can tell they came from the counter, and no
// customer token was ever handed out for them.
func TestWalkInIsAddedFromTheCounter(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Walk-in Shop")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}

	res := client.do(http.MethodPost, "/api/queues/"+created.Queue.Slug+"/entries",
		mustJSON(t, map[string]string{"name": "Kofi"}), owner)
	if res.status != http.StatusOK {
		t.Fatalf("add walk-in: %d %s", res.status, res.body)
	}
	if bytes.Contains(res.body, []byte("customerToken")) {
		t.Fatalf("a walk-in must not be handed a customer token: %s", res.body)
	}

	var view struct {
		Waiting []struct {
			Number int    `json:"number"`
			Name   string `json:"customerName"`
			WalkIn bool   `json:"walkIn"`
		} `json:"waiting"`
	}
	decode(t, res, &view)
	if len(view.Waiting) != 1 || view.Waiting[0].Name != "Kofi" || !view.Waiting[0].WalkIn {
		t.Fatalf("expected one walk-in named Kofi, got %+v", view.Waiting)
	}

	// Nobody but staff can do this, and a name is required.
	res = client.do(http.MethodPost, "/api/queues/"+created.Queue.Slug+"/entries",
		mustJSON(t, map[string]string{"name": "Anyone"}))
	if res.status != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a session, got %d", res.status)
	}
	res = client.do(http.MethodPost, "/api/queues/"+created.Queue.Slug+"/entries",
		mustJSON(t, map[string]string{"name": "  "}), owner)
	if res.status != http.StatusBadRequest {
		t.Fatalf("expected 400 for a blank name, got %d", res.status)
	}
}

// A skipped customer who walks up a minute later is called back with the
// number they had, and shows on the dashboard as recallable until then.
func TestSkippedCustomerCanBeRecalled(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Recall Shop")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	slug := created.Queue.Slug

	first, _ := client.join(slug, "Amara", "")
	second, _ := client.join(slug, "Kofi", "")

	var view struct {
		Serving *struct {
			Number int `json:"number"`
		} `json:"serving"`
		Waiting []struct {
			ID     string `json:"id"`
			Number int    `json:"number"`
		} `json:"waiting"`
		Skipped []struct {
			ID     string `json:"id"`
			Number int    `json:"number"`
		} `json:"skipped"`
	}
	res := client.do(http.MethodGet, "/api/queues/"+slug+"/entries", nil, owner)
	decode(t, res, &view)
	if len(view.Waiting) != 2 {
		t.Fatalf("expected two waiting, got %+v", view.Waiting)
	}
	amara := view.Waiting[0].ID

	// Skip Amara: she leaves the line and appears among the recallable.
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/entries/"+amara+"/skip", nil, owner)
	if res.status != http.StatusOK {
		t.Fatalf("skip: %d %s", res.status, res.body)
	}
	decode(t, res, &view)
	if len(view.Waiting) != 1 || len(view.Skipped) != 1 || view.Skipped[0].Number != first.Entry.Number {
		t.Fatalf("expected Amara among the skipped, got waiting %+v skipped %+v", view.Waiting, view.Skipped)
	}

	// Call her back: she is at the counter with her old number, and nobody
	// else moved.
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/entries/"+amara+"/serve", nil, owner)
	if res.status != http.StatusOK {
		t.Fatalf("recall: %d %s", res.status, res.body)
	}
	decode(t, res, &view)
	if view.Serving == nil || view.Serving.Number != first.Entry.Number {
		t.Fatalf("expected Amara at the counter, got %+v", view.Serving)
	}
	if len(view.Waiting) != 1 || view.Waiting[0].Number != second.Entry.Number || len(view.Skipped) != 0 {
		t.Fatalf("expected Kofi still waiting and nobody skipped, got waiting %+v skipped %+v", view.Waiting, view.Skipped)
	}
}
