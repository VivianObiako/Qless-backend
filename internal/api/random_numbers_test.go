package api_test

import (
	"net/http"
	"testing"
)

type numberingView struct {
	Queue struct {
		Numbering    string `json:"numbering"`
		ServingOrder string `json:"servingOrder"`
	} `json:"queue"`
	State struct {
		Queue struct {
			Numbering string `json:"numbering"`
		} `json:"queue"`
		Estimates []any `json:"estimates"`
	} `json:"state"`
}

// The three ways a queue can run, as the settings screen offers them, and the
// combinations the API refuses.
func TestRandomNumbersSettings(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Random Numbers Hall")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	path := "/api/queues/" + created.Queue.Slug

	if res := client.do(http.MethodPatch, path, []byte(`{"numbering":"RANDOM"}`), owner); res.status != http.StatusBadRequest {
		t.Fatalf("random numbers with no places: status %d, want 400", res.status)
	}
	if res := client.do(http.MethodPatch, path, []byte(`{"numbering":"SHUFFLED"}`), owner); res.status != http.StatusBadRequest {
		t.Fatalf("an unknown numbering: status %d, want 400", res.status)
	}

	var view numberingView
	res := client.do(http.MethodPatch, path, []byte(`{"numbering":"RANDOM","maxCapacity":24}`), owner)
	if res.status != http.StatusOK {
		t.Fatalf("random numbers with places: status %d, body %s", res.status, res.body)
	}
	decode(t, res, &view)
	if view.Queue.Numbering != "RANDOM" {
		t.Fatalf("numbering = %q, want RANDOM", view.Queue.Numbering)
	}

	if res := client.do(http.MethodPatch, path, []byte(`{"servingOrder":"RANDOM"}`), owner); res.status != http.StatusBadRequest {
		t.Fatalf("a random call on random numbers: status %d, want 400", res.status)
	}
	if res := client.do(http.MethodPatch, path, []byte(`{"maxCapacity":null}`), owner); res.status != http.StatusBadRequest {
		t.Fatalf("clearing the places of random numbers: status %d, want 400", res.status)
	}

	// Switching to the draw in one request, the way the settings screen
	// sends it, is fine.
	if res := client.do(http.MethodPatch, path, []byte(`{"numbering":"SEQUENTIAL","servingOrder":"RANDOM"}`), owner); res.status != http.StatusOK {
		t.Fatalf("random numbers to a draw in one request: status %d, body %s", res.status, res.body)
	}
	if res := client.do(http.MethodPatch, path, []byte(`{"numbering":"RANDOM","servingOrder":"IN_ORDER"}`), owner); res.status != http.StatusOK {
		t.Fatalf("a draw back to random numbers in one request: status %d, body %s", res.status, res.body)
	}

	res = client.do(http.MethodPost, "/api/queues", []byte(`{"name":"Random At Birth","averageServiceMinutes":10,"numbering":"RANDOM"}`))
	if res.status != http.StatusBadRequest {
		t.Fatalf("create with random numbers and no places: status %d, want 400", res.status)
	}
}

// Phones and the wall learn the queue hands out random numbers, and because
// it is still called in order, the wait estimates stay.
func TestRandomNumbersPublicState(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Random Numbers Public")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	path := "/api/queues/" + created.Queue.Slug

	if res := client.do(http.MethodPatch, path, []byte(`{"numbering":"RANDOM","maxCapacity":10}`), owner); res.status != http.StatusOK {
		t.Fatalf("set random numbers: %d %s", res.status, res.body)
	}
	for _, name := range []string{"Alpha", "Beta", "Gamma"} {
		if _, res := client.join(created.Queue.Slug, name, ""); res.status != http.StatusCreated {
			t.Fatalf("join %s: %d %s", name, res.status, res.body)
		}
	}

	var public numberingView
	decode(t, client.do(http.MethodGet, path, nil), &public)
	if public.State.Queue.Numbering != "RANDOM" {
		t.Errorf("the public summary says %q, want RANDOM", public.State.Queue.Numbering)
	}
	if len(public.State.Estimates) == 0 {
		t.Error("a queue called in order lost its wait estimates")
	}
}
