package api_test

import (
	"net/http"
	"testing"
)

type settingsView struct {
	Queue struct {
		ID          string  `json:"id"`
		Slug        string  `json:"slug"`
		Status      string  `json:"status"`
		HoldMinutes int     `json:"holdMinutes"`
		PauseNote   string  `json:"pauseNote"`
		ArchivedAt  *string `json:"archivedAt"`
	} `json:"queue"`
	Waiting []struct {
		ID     string `json:"id"`
		Number int    `json:"number"`
	} `json:"waiting"`
	Skipped []struct {
		Number int `json:"number"`
	} `json:"skipped"`
	Measured struct {
		Minutes int `json:"minutes"`
		Sample  int `json:"sample"`
	} `json:"measured"`
	LastActivityAt *string `json:"lastActivityAt"`
}

// Hold time is one number with three jobs; this covers the one the server
// enforces. With no hold, a skip is final: nothing to recall and nobody
// listed as recallable.
func TestNoHoldTimeMakesASkipFinal(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("No Hold Shop")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	slug := created.Queue.Slug

	var view settingsView
	res := client.do(http.MethodPatch, "/api/queues/"+slug, mustJSON(t, map[string]int{"holdMinutes": 0}), owner)
	if res.status != http.StatusOK {
		t.Fatalf("set hold time: %d %s", res.status, res.body)
	}
	decode(t, res, &view)
	if view.Queue.HoldMinutes != 0 {
		t.Fatalf("expected hold time 0, got %d", view.Queue.HoldMinutes)
	}

	client.join(slug, "Amara", "")
	res = client.do(http.MethodGet, "/api/queues/"+slug+"/entries", nil, owner)
	decode(t, res, &view)
	amara := view.Waiting[0].ID

	res = client.do(http.MethodPost, "/api/queues/"+slug+"/entries/"+amara+"/skip", nil, owner)
	decode(t, res, &view)
	if len(view.Skipped) != 0 {
		t.Fatalf("a queue with no hold should list nobody as recallable, got %+v", view.Skipped)
	}

	res = client.do(http.MethodPost, "/api/queues/"+slug+"/entries/"+amara+"/serve", nil, owner)
	if res.status != http.StatusConflict {
		t.Fatalf("expected recall to be refused with 409, got %d %s", res.status, res.body)
	}

	res = client.do(http.MethodPatch, "/api/queues/"+slug, mustJSON(t, map[string]int{"holdMinutes": 121}), owner)
	if res.status != http.StatusBadRequest {
		t.Fatalf("expected 400 for a hold time over the limit, got %d", res.status)
	}
}

// A pause can say when the queue is back, and the line does not outlive the
// pause it described.
func TestPauseNoteShowsToCustomersAndClearsOnResume(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Lunch Shop")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	slug := created.Queue.Slug

	res := client.do(http.MethodPost, "/api/queues/"+slug+"/pause", mustJSON(t, map[string]string{"note": "  Back at 2:30 "}), owner)
	if res.status != http.StatusOK {
		t.Fatalf("pause with note: %d %s", res.status, res.body)
	}

	var public struct {
		State struct {
			Queue struct {
				Status    string `json:"status"`
				PauseNote string `json:"pauseNote"`
			} `json:"queue"`
		} `json:"state"`
	}
	res = client.do(http.MethodGet, "/api/queues/"+slug, nil)
	decode(t, res, &public)
	if public.State.Queue.Status != "PAUSED" || public.State.Queue.PauseNote != "Back at 2:30" {
		t.Fatalf("expected a paused queue carrying the note, got %+v", public.State.Queue)
	}

	// Pausing with no body at all still pauses.
	client.do(http.MethodPost, "/api/queues/"+slug+"/resume", nil, owner)
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/pause", nil, owner)
	if res.status != http.StatusOK {
		t.Fatalf("pause without a body: %d %s", res.status, res.body)
	}

	res = client.do(http.MethodPost, "/api/queues/"+slug+"/resume", nil, owner)
	decode(t, res, &public)
	res = client.do(http.MethodGet, "/api/queues/"+slug, nil)
	decode(t, res, &public)
	if public.State.Queue.Status != "OPEN" || public.State.Queue.PauseNote != "" {
		t.Fatalf("expected an open queue with no note, got %+v", public.State.Queue)
	}
}

// Archiving is the nearest thing to deleting: the queue leaves the list,
// closes, and refuses to be reopened until it is restored. Its history stays.
func TestArchivedQueueLeavesTheListAndKeepsItsHistory(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Old Location")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	slug := created.Queue.Slug

	client.join(slug, "Amara", "")
	client.do(http.MethodPost, "/api/queues/"+slug+"/next", nil, owner)
	client.do(http.MethodPost, "/api/queues/"+slug+"/next", nil, owner)

	res := client.do(http.MethodPost, "/api/queues/"+slug+"/archive", nil, owner)
	if res.status != http.StatusOK {
		t.Fatalf("archive: %d %s", res.status, res.body)
	}
	var view settingsView
	decode(t, res, &view)
	if view.Queue.Status != "CLOSED" || view.Queue.ArchivedAt == nil {
		t.Fatalf("expected a closed, archived queue, got %+v", view.Queue)
	}

	var mine struct {
		Queues   []struct{ ID string } `json:"queues"`
		Archived []struct{ ID string } `json:"archived"`
	}
	res = client.do(http.MethodGet, "/api/me/queues", nil, owner)
	decode(t, res, &mine)
	if len(mine.Queues) != 0 || len(mine.Archived) != 1 {
		t.Fatalf("expected the queue under archived only, got %+v", mine)
	}

	if res = client.do(http.MethodPost, "/api/queues/"+slug+"/resume", nil, owner); res.status != http.StatusBadRequest {
		t.Fatalf("an archived queue must not reopen from the counter, got %d %s", res.status, res.body)
	}
	if _, res := client.join(slug, "Kofi", ""); res.status != http.StatusConflict {
		t.Fatalf("expected joins to be refused, got %d", res.status)
	}

	var history struct {
		Entries []struct{ Number int } `json:"entries"`
	}
	res = client.do(http.MethodGet, "/api/queues/"+slug+"/history", nil, owner)
	decode(t, res, &history)
	if len(history.Entries) != 1 {
		t.Fatalf("history should survive archiving, got %+v", history.Entries)
	}

	res = client.do(http.MethodPost, "/api/queues/"+slug+"/unarchive", nil, owner)
	decode(t, res, &view)
	if view.Queue.ArchivedAt != nil || view.Queue.Status != "CLOSED" {
		t.Fatalf("restoring should leave the queue closed and unarchived, got %+v", view.Queue)
	}
	res = client.do(http.MethodGet, "/api/me/queues", nil, owner)
	decode(t, res, &mine)
	if len(mine.Queues) != 1 || len(mine.Archived) != 0 {
		t.Fatalf("expected the queue back in the list, got %+v", mine)
	}
}

// An owner can have a name, given at create or later, and it reaches the
// screens that would otherwise say "the owner".
func TestOwnerNameIsOptionalAndReachesHistory(t *testing.T) {
	client := newTestClient(t)

	res := client.do(http.MethodPost, "/api/queues",
		mustJSON(t, map[string]any{"name": "Named Shop", "averageServiceMinutes": 10, "ownerName": " Ade "}))
	if res.status != http.StatusCreated {
		t.Fatalf("create with owner name: %d %s", res.status, res.body)
	}
	var created createdQueue
	decode(t, res, &created)
	owner := header{"Authorization", "Bearer " + created.OwnerToken}

	var mine struct {
		DisplayName string `json:"displayName"`
		Queues      []struct {
			ServingNumber *int `json:"servingNumber"`
			WaitingCount  int  `json:"waitingCount"`
		} `json:"queues"`
	}
	res = client.do(http.MethodGet, "/api/me/queues", nil, owner)
	decode(t, res, &mine)
	if mine.DisplayName != "Ade" {
		t.Fatalf("expected the trimmed name, got %q", mine.DisplayName)
	}

	res = client.do(http.MethodPatch, "/api/me", mustJSON(t, map[string]string{"displayName": "Ade O."}), owner)
	if res.status != http.StatusOK {
		t.Fatalf("rename owner: %d %s", res.status, res.body)
	}

	slug := created.Queue.Slug
	client.join(slug, "Amara", "")
	client.join(slug, "Kofi", "")
	client.do(http.MethodPost, "/api/queues/"+slug+"/next", nil, owner)

	res = client.do(http.MethodGet, "/api/me/queues", nil, owner)
	decode(t, res, &mine)
	if mine.DisplayName != "Ade O." || len(mine.Queues) != 1 ||
		mine.Queues[0].ServingNumber == nil || *mine.Queues[0].ServingNumber != 1 || mine.Queues[0].WaitingCount != 1 {
		t.Fatalf("expected the new name and live figures, got %+v", mine)
	}

	client.do(http.MethodPost, "/api/queues/"+slug+"/next", nil, owner)
	var history struct {
		OwnerName string `json:"ownerName"`
	}
	res = client.do(http.MethodGet, "/api/queues/"+slug+"/history", nil, owner)
	decode(t, res, &history)
	if history.OwnerName != "Ade O." {
		t.Fatalf("expected history to carry the owner's name, got %q", history.OwnerName)
	}
}

// Once the day has produced enough real service times, the estimate is built
// from them rather than from the number the owner typed once.
func TestEstimateSwitchesToMeasuredServiceTimes(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Measured Shop")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	slug := created.Queue.Slug

	for _, name := range []string{"A", "B", "C", "D", "E", "F", "G"} {
		client.join(slug, name, "")
	}

	var public struct {
		State struct {
			ServiceMinutes int `json:"serviceMinutes"`
		} `json:"state"`
	}
	res := client.do(http.MethodGet, "/api/queues/"+slug, nil)
	decode(t, res, &public)
	if public.State.ServiceMinutes != 15 {
		t.Fatalf("expected the setting before any service, got %d", public.State.ServiceMinutes)
	}

	// Six presses: five people are called and finished in well under a minute
	// each, which is a measured average of one minute against a setting of 15.
	for range 6 {
		client.do(http.MethodPost, "/api/queues/"+slug+"/next", nil, owner)
	}

	var view settingsView
	res = client.do(http.MethodGet, "/api/queues/"+slug+"/entries", nil, owner)
	decode(t, res, &view)
	if view.Measured.Sample != 5 || view.Measured.Minutes != 1 {
		t.Fatalf("expected five measured services of one minute, got %+v", view.Measured)
	}
	if view.LastActivityAt == nil {
		t.Fatal("expected last activity to be set")
	}

	res = client.do(http.MethodGet, "/api/queues/"+slug, nil)
	decode(t, res, &public)
	if public.State.ServiceMinutes != 1 {
		t.Fatalf("expected the measured figure to drive the estimate, got %d", public.State.ServiceMinutes)
	}
}
