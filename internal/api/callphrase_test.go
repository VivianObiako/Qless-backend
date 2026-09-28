package api_test

import (
	"net/http"
	"testing"
)

type phraseView struct {
	Queue struct {
		CallPhrase string `json:"callPhrase"`
	} `json:"queue"`
	State struct {
		Queue struct {
			CallPhrase string `json:"callPhrase"`
		} `json:"queue"`
	} `json:"state"`
}

// What being called is named reaches the public summary, so the wall, the
// join page and the pass can all say it. A queue says "serving" until its
// owner picks another.
func TestCallPhraseDefaultsAndReachesThePublic(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Phrase Hall")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	path := "/api/queues/" + created.Queue.Slug

	var public phraseView
	decode(t, client.do(http.MethodGet, path, nil), &public)
	if public.State.Queue.CallPhrase != "SERVING" {
		t.Fatalf("a new queue's phrase = %q, want SERVING", public.State.Queue.CallPhrase)
	}

	var settings phraseView
	res := client.do(http.MethodPatch, path, []byte(`{"callPhrase":"PRESENTING"}`), owner)
	if res.status != http.StatusOK {
		t.Fatalf("set the phrase: %d %s", res.status, res.body)
	}
	decode(t, res, &settings)
	if settings.Queue.CallPhrase != "PRESENTING" {
		t.Fatalf("settings answered %q, want PRESENTING", settings.Queue.CallPhrase)
	}

	decode(t, client.do(http.MethodGet, path, nil), &public)
	if public.State.Queue.CallPhrase != "PRESENTING" {
		t.Fatalf("the public summary says %q, want PRESENTING", public.State.Queue.CallPhrase)
	}

	if res := client.do(http.MethodPatch, path, []byte(`{"callPhrase":"PERFORMING"}`), owner); res.status != http.StatusBadRequest {
		t.Fatalf("an unknown phrase: status %d, want 400", res.status)
	}
	if res := client.do(http.MethodPatch, path, []byte(`{"name":"Phrase Hall Renamed"}`), owner); res.status != http.StatusOK {
		t.Fatalf("an unrelated change: status %d", res.status)
	}
	decode(t, client.do(http.MethodGet, path, nil), &public)
	if public.State.Queue.CallPhrase != "PRESENTING" {
		t.Fatalf("an unrelated change reset the phrase to %q", public.State.Queue.CallPhrase)
	}
}

func TestCallPhraseCanBeSetAtCreation(t *testing.T) {
	client := newTestClient(t)

	res := client.do(http.MethodPost, "/api/queues",
		[]byte(`{"name":"Phrase Clinic","averageServiceMinutes":10,"callPhrase":"SEEING"}`))
	if res.status != http.StatusCreated {
		t.Fatalf("create: %d %s", res.status, res.body)
	}
	var created phraseView
	decode(t, res, &created)
	if created.Queue.CallPhrase != "SEEING" {
		t.Fatalf("created with %q, want SEEING", created.Queue.CallPhrase)
	}

	if res := client.do(http.MethodPost, "/api/queues",
		[]byte(`{"name":"Phrase Bad","averageServiceMinutes":10,"callPhrase":"singing"}`)); res.status != http.StatusBadRequest {
		t.Fatalf("an unknown phrase at creation: status %d, want 400", res.status)
	}
}
