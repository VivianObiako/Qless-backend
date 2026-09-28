package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type drawSettingsView struct {
	Queue struct {
		ServingOrder string `json:"servingOrder"`
		MaxCapacity  *int   `json:"maxCapacity"`
		PersonNoun   string `json:"personNoun"`
		PeopleNoun   string `json:"peopleNoun"`
	} `json:"queue"`
	Waiting []struct {
		Number   int     `json:"number"`
		DrawnAt  *string `json:"drawnAt"`
		Estimate *struct {
			Label string `json:"label"`
		} `json:"estimate"`
	} `json:"waiting"`
}

type drawCustomerView struct {
	State struct {
		Queue struct {
			ServingOrder string `json:"servingOrder"`
			PeopleNoun   string `json:"peopleNoun"`
		} `json:"queue"`
		UpNextNumber   *int  `json:"upNextNumber"`
		PlacesTaken    int   `json:"placesTaken"`
		IsFull         bool  `json:"isFull"`
		WaitingNumbers []int `json:"waitingNumbers"`
		Estimates      []any `json:"estimates"`
	} `json:"state"`
	PeopleAhead  int  `json:"peopleAhead"`
	Estimate     *any `json:"estimate"`
	JoinEstimate *any `json:"joinEstimate"`
}

// A draw and no capacity is refused whichever half of it a request changes,
// and allowed when one request sets both.
func TestDrawSettingsNeedAFixedNumberOfPlaces(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Draw Settings Hackathon")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	path := "/api/queues/" + created.Queue.Slug

	res := client.do(http.MethodPatch, path, []byte(`{"servingOrder":"RANDOM"}`), owner)
	if res.status != http.StatusBadRequest {
		t.Fatalf("draw with no capacity: status %d, want 400: %s", res.status, res.body)
	}
	if !strings.Contains(string(res.body), "fixed number of places") {
		t.Errorf("refusal does not say why: %s", res.body)
	}

	res = client.do(http.MethodPatch, path, []byte(`{"servingOrder":"SHUFFLE"}`), owner)
	if res.status != http.StatusBadRequest {
		t.Fatalf("unknown serving order: status %d, want 400", res.status)
	}

	res = client.do(http.MethodPatch, path, []byte(`{"servingOrder":"RANDOM","maxCapacity":30}`), owner)
	if res.status != http.StatusOK {
		t.Fatalf("draw with capacity: status %d: %s", res.status, res.body)
	}
	var view drawSettingsView
	decode(t, res, &view)
	if view.Queue.ServingOrder != "RANDOM" || view.Queue.MaxCapacity == nil || *view.Queue.MaxCapacity != 30 {
		t.Fatalf("queue after the change = %+v", view.Queue)
	}

	res = client.do(http.MethodPatch, path, []byte(`{"maxCapacity":null}`), owner)
	if res.status != http.StatusBadRequest {
		t.Fatalf("clearing a draw's capacity: status %d, want 400", res.status)
	}

	res = client.do(http.MethodPatch, path, []byte(`{"maxCapacity":32}`), owner)
	if res.status != http.StatusOK {
		t.Fatalf("raising a draw's capacity: status %d: %s", res.status, res.body)
	}

	res = client.do(http.MethodPost, "/api/queues", []byte(`{"name":"Draw At Birth","averageServiceMinutes":10,"servingOrder":"RANDOM"}`))
	if res.status != http.StatusBadRequest {
		t.Fatalf("create a draw with no capacity: status %d, want 400", res.status)
	}
}

func TestNounsAreTrimmedDefaultedAndBounded(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Noun Hall")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	path := "/api/queues/" + created.Queue.Slug

	var view drawSettingsView
	res := client.do(http.MethodPatch, path, []byte(`{"personNoun":"  participant ","peopleNoun":"participants"}`), owner)
	if res.status != http.StatusOK {
		t.Fatalf("set nouns: %d %s", res.status, res.body)
	}
	decode(t, res, &view)
	if view.Queue.PersonNoun != "participant" || view.Queue.PeopleNoun != "participants" {
		t.Fatalf("nouns = %q/%q", view.Queue.PersonNoun, view.Queue.PeopleNoun)
	}

	var public drawCustomerView
	decode(t, client.do(http.MethodGet, path, nil), &public)
	if public.State.Queue.PeopleNoun != "participants" {
		t.Errorf("the public summary says %q", public.State.Queue.PeopleNoun)
	}

	res = client.do(http.MethodPatch, path, []byte(`{"personNoun":"  "}`), owner)
	decode(t, res, &view)
	if view.Queue.PersonNoun != "customer" {
		t.Errorf("an emptied noun = %q, want the default back", view.Queue.PersonNoun)
	}

	long := strings.Repeat("a", 31)
	res = client.do(http.MethodPatch, path, []byte(`{"peopleNoun":"`+long+`"}`), owner)
	if res.status != http.StatusBadRequest {
		t.Fatalf("31-character noun: status %d, want 400", res.status)
	}
}

// What a phone and the wall receive in a draw: the drawn number, how many
// places are gone, no wait quoted, no position, and still no names.
func TestDrawPublicStateAndCounter(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Draw State Hackathon")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	path := "/api/queues/" + created.Queue.Slug

	if res := client.do(http.MethodPatch, path, []byte(`{"servingOrder":"RANDOM","maxCapacity":4}`), owner); res.status != http.StatusOK {
		t.Fatalf("make it a draw: %d %s", res.status, res.body)
	}

	var tokens []string
	for i := range 4 {
		joined, res := client.join(created.Queue.Slug, fmt.Sprintf("Team Secret %d", i), "")
		if res.status != http.StatusCreated {
			t.Fatalf("join %d: %d %s", i, res.status, res.body)
		}
		tokens = append(tokens, joined.CustomerToken)
	}
	if _, res := client.join(created.Queue.Slug, "Team Five", ""); res.status != http.StatusConflict {
		t.Fatalf("fifth join: status %d, want 409 full", res.status)
	}

	if res := client.do(http.MethodPost, path+"/next", nil, owner); res.status != http.StatusOK {
		t.Fatalf("first call: %d %s", res.status, res.body)
	}

	res := client.do(http.MethodGet, path, nil)
	if strings.Contains(string(res.body), "Team Secret") {
		t.Fatal("a name reached the public payload")
	}
	var public drawCustomerView
	decode(t, res, &public)
	if public.State.Queue.ServingOrder != "RANDOM" {
		t.Errorf("servingOrder = %q", public.State.Queue.ServingOrder)
	}
	if public.State.UpNextNumber == nil {
		t.Fatal("nothing drawn after the first call")
	}
	if public.State.PlacesTaken != 4 || !public.State.IsFull {
		t.Errorf("placesTaken = %d, isFull = %v; want 4 and full", public.State.PlacesTaken, public.State.IsFull)
	}
	if len(public.State.WaitingNumbers) != 3 {
		t.Errorf("waitingNumbers = %v, want three", public.State.WaitingNumbers)
	}
	if public.State.Estimates == nil || len(public.State.Estimates) != 0 {
		t.Errorf("estimates = %v, want an empty list", public.State.Estimates)
	}
	if public.JoinEstimate != nil {
		t.Error("a draw quoted a wait to join")
	}

	for _, token := range tokens {
		res := client.do(http.MethodGet, path, nil, header{"X-Customer-Token", token})
		var mine drawCustomerView
		decode(t, res, &mine)
		if mine.PeopleAhead != 0 || mine.Estimate != nil {
			t.Errorf("a customer in a draw was told %d ahead and an estimate", mine.PeopleAhead)
		}
	}

	var counter drawSettingsView
	decode(t, client.do(http.MethodGet, path+"/entries", nil, owner), &counter)
	drawn := 0
	for _, row := range counter.Waiting {
		if row.Estimate != nil {
			t.Errorf("waiting row %d carries an estimate", row.Number)
		}
		if row.DrawnAt != nil {
			drawn++
			if row.Number != *public.State.UpNextNumber {
				t.Errorf("the counter marks %d drawn, the wall says %d", row.Number, *public.State.UpNextNumber)
			}
		}
	}
	if drawn != 1 {
		t.Fatalf("%d waiting rows marked drawn, want exactly one", drawn)
	}
}
