package api_test

import (
	"net/http"
	"testing"

	"github.com/vivianobiako/qless/api/internal/httpx"
)

type operatorView struct {
	Queue struct {
		ID                    string `json:"id"`
		Name                  string `json:"name"`
		Description           string `json:"description"`
		Status                string `json:"status"`
		NextNumber            int    `json:"nextNumber"`
		AverageServiceMinutes int    `json:"averageServiceMinutes"`
		MaxCapacity           *int   `json:"maxCapacity"`
		ShowNamesToOperators  bool   `json:"showNamesToOperators"`
	} `json:"queue"`
	Serving *struct {
		ID     string `json:"id"`
		Number int    `json:"number"`
		Name   string `json:"customerName"`
	} `json:"serving"`
	Waiting []struct {
		ID     string `json:"id"`
		Number int    `json:"number"`
		Name   string `json:"customerName"`
	} `json:"waiting"`
	WaitingCount int  `json:"waitingCount"`
	ShowsNames   bool `json:"showsNames"`
}

// operator wraps a queue and its session token, since every action below needs
// both and reads back the same view shape.
type operator struct {
	client *testClient
	queue  createdQueue
	auth   header
}

func newOperator(t *testing.T, name string) *operator {
	t.Helper()

	client := newTestClient(t)
	created := client.createQueue(name)
	return &operator{
		client: client,
		queue:  created,
		auth:   header{"Authorization", "Bearer " + created.OwnerToken},
	}
}

func (o *operator) do(method, path string, body []byte) response {
	o.client.t.Helper()
	return o.client.do(method, "/api/queues/"+o.queue.Queue.ID+path, body, o.auth)
}

func (o *operator) mustDo(method, path string, body []byte) operatorView {
	o.client.t.Helper()

	res := o.do(method, path, body)
	if res.status != http.StatusOK {
		o.client.t.Fatalf("%s %s: status %d, body %s", method, path, res.status, res.body)
	}

	var view operatorView
	decode(o.client.t, res, &view)
	return view
}

func (o *operator) view() operatorView {
	o.client.t.Helper()
	return o.mustDo(http.MethodGet, "/entries", nil)
}

// joinAll puts customers in the queue in order and returns the view.
func (o *operator) joinAll(names ...string) operatorView {
	o.client.t.Helper()

	for _, name := range names {
		if _, res := o.client.join(o.queue.Queue.Slug, name, ""); res.status != http.StatusCreated {
			o.client.t.Fatalf("join %s: status %d, body %s", name, res.status, res.body)
		}
	}
	return o.view()
}

// Calling one customer by name pulls them out without reordering anyone else.
// Positions have to stay stable, or the numbers on other people's phones start
// lying to them.
func TestServeSpecificCustomerLeavesEveryoneElseInPlace(t *testing.T) {
	op := newOperator(t, "Serve Specific Shop")
	before := op.joinAll("Vivian", "John", "Sarah")

	third := before.Waiting[2]
	if third.Name != "Sarah" {
		t.Fatalf("waiting[2] = %q, want Sarah", third.Name)
	}

	after := op.mustDo(http.MethodPost, "/entries/"+third.ID+"/serve", nil)

	if after.Serving == nil || after.Serving.ID != third.ID {
		t.Fatalf("serving = %+v, want Sarah", after.Serving)
	}
	if after.WaitingCount != 2 {
		t.Fatalf("waiting count = %d, want 2", after.WaitingCount)
	}
	for i, want := range []string{"Vivian", "John"} {
		if after.Waiting[i].Name != want {
			t.Errorf("waiting[%d] = %q, want %q", i, after.Waiting[i].Name, want)
		}
		if after.Waiting[i].Number != before.Waiting[i].Number {
			t.Errorf("%s moved from number %d to %d", want, before.Waiting[i].Number, after.Waiting[i].Number)
		}
	}
}

// There is one counter, and the database enforces it. Calling a second customer
// closes out the first rather than putting two people at it.
func TestServingASecondCustomerAttendsTheFirst(t *testing.T) {
	op := newOperator(t, "One Counter Shop")
	before := op.joinAll("Vivian", "John")

	op.mustDo(http.MethodPost, "/entries/"+before.Waiting[0].ID+"/serve", nil)
	after := op.mustDo(http.MethodPost, "/entries/"+before.Waiting[1].ID+"/serve", nil)

	if after.Serving == nil || after.Serving.Name != "John" {
		t.Fatalf("serving = %+v, want John", after.Serving)
	}
	if after.WaitingCount != 0 {
		t.Errorf("waiting count = %d, want 0", after.WaitingCount)
	}

	history := op.history()
	if len(history) != 1 || history[0].Name != "Vivian" || history[0].Status != "ATTENDED" {
		t.Errorf("history = %+v, want Vivian attended", history)
	}
}

// A skipped customer is not deleted. They keep their record, and because a
// skipped entry falls outside the active-entry index they can rejoin and take a
// fresh number.
func TestSkippedCustomerKeepsTheirRecordAndCanRejoin(t *testing.T) {
	op := newOperator(t, "Skip Shop")
	before := op.joinAll("Vivian", "John")

	first, res := op.client.join(op.queue.Queue.Slug, "Rejoiner", "")
	if res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	view := op.view()
	target := view.Waiting[len(view.Waiting)-1]
	if target.Name != "Rejoiner" {
		t.Fatalf("last waiting = %q, want Rejoiner", target.Name)
	}

	after := op.mustDo(http.MethodPost, "/entries/"+target.ID+"/skip", nil)
	if after.WaitingCount != 2 {
		t.Errorf("waiting count = %d, want the other two", after.WaitingCount)
	}

	history := op.history()
	if len(history) != 1 || history[0].Status != "SKIPPED" {
		t.Fatalf("history = %+v, want one skipped entry", history)
	}

	// The same browser, coming back with the token it already holds.
	rejoined, res := op.client.join(op.queue.Queue.Slug, "Rejoiner", first.CustomerToken)
	if res.status != http.StatusCreated {
		t.Fatalf("rejoin: status %d, want 201; body %s", res.status, res.body)
	}
	if rejoined.AlreadyJoined {
		t.Error("rejoin was treated as a duplicate; a skipped customer must get a fresh place")
	}
	if rejoined.Entry.Number <= target.Number {
		t.Errorf("rejoined with number %d, want a new one above %d", rejoined.Entry.Number, target.Number)
	}

	_ = before
}

// Acting on a row someone else already dealt with is a stale dashboard, not a
// broken one, and it says so.
func TestActingOnAFinishedEntryIsRefusedClearly(t *testing.T) {
	op := newOperator(t, "Stale Click Shop")
	before := op.joinAll("Vivian")
	target := before.Waiting[0].ID

	op.mustDo(http.MethodPost, "/entries/"+target+"/skip", nil)

	for _, action := range []string{"serve", "attend", "skip"} {
		res := op.do(http.MethodPost, "/entries/"+target+"/"+action, nil)
		if res.status != http.StatusConflict {
			t.Errorf("%s on a finished entry: status %d, want 409; body %s", action, res.status, res.body)
		}

		var body httpx.ErrorBody
		decode(t, res, &body)
		if body.Error != "entry_not_active" {
			t.Errorf("%s: error code = %q, want entry_not_active", action, body.Error)
		}
	}
}

// An entry belonging to someone else's queue must not be reachable through this
// one, even by an operator who is perfectly entitled to their own.
func TestEntriesFromAnotherQueueAreNotReachable(t *testing.T) {
	op := newOperator(t, "Mine Shop")
	op.joinAll("Vivian")

	stranger := op.client.createQueue("Theirs Shop")
	if _, res := op.client.join(stranger.Queue.Slug, "Someone", ""); res.status != http.StatusCreated {
		t.Fatalf("join stranger queue: status %d, body %s", res.status, res.body)
	}

	strangerView := operatorView{}
	res := op.client.do(http.MethodGet, "/api/queues/"+stranger.Queue.ID+"/entries", nil,
		header{"Authorization", "Bearer " + stranger.OwnerToken})
	decode(t, res, &strangerView)
	theirEntry := strangerView.Waiting[0].ID

	if res := op.do(http.MethodPost, "/entries/"+theirEntry+"/serve", nil); res.status != http.StatusNotFound {
		t.Errorf("serving another queue's entry: status %d, want 404; body %s", res.status, res.body)
	}
}

// Reset clears the line and starts the numbering again. History survives it —
// that is the difference between resetting a queue and deleting one.
func TestResetClearsTheLineAndRestartsNumbering(t *testing.T) {
	op := newOperator(t, "Reset Shop")
	before := op.joinAll("Vivian", "John", "Sarah")
	op.mustDo(http.MethodPost, "/entries/"+before.Waiting[0].ID+"/serve", nil)

	after := op.mustDo(http.MethodPost, "/reset", nil)

	if after.WaitingCount != 0 || after.Serving != nil {
		t.Errorf("after reset: %d waiting, serving %+v; want an empty queue", after.WaitingCount, after.Serving)
	}
	if after.Queue.NextNumber != 1 {
		t.Errorf("next number = %d, want 1", after.Queue.NextNumber)
	}

	if history := op.history(); len(history) != 3 {
		t.Errorf("history holds %d entries, want all 3 preserved", len(history))
	}

	// And the next customer of the new day is number 1.
	joined, res := op.client.join(op.queue.Queue.Slug, "Tomorrow", "")
	if res.status != http.StatusCreated {
		t.Fatalf("join after reset: status %d, body %s", res.status, res.body)
	}
	if joined.Entry.Number != 1 {
		t.Errorf("first number after reset = %d, want 1", joined.Entry.Number)
	}
}

// Pause and close both stop new joins and both are reversible. Neither disturbs
// the customers already in line.
func TestPauseAndCloseBlockJoinsWithoutDisturbingTheQueue(t *testing.T) {
	op := newOperator(t, "Lifecycle Shop")
	op.joinAll("Vivian", "John")

	for _, step := range []struct {
		action   string
		status   string
		joinCode string
	}{
		{"pause", "PAUSED", "queue_paused"},
		{"close", "CLOSED", "queue_closed"},
	} {
		view := op.mustDo(http.MethodPost, "/"+step.action, nil)
		if view.Queue.Status != step.status {
			t.Fatalf("after %s: status %q, want %q", step.action, view.Queue.Status, step.status)
		}
		if view.WaitingCount != 2 {
			t.Errorf("after %s: %d waiting, want the queue left alone", step.action, view.WaitingCount)
		}

		_, res := op.client.join(op.queue.Queue.Slug, "Latecomer", "")
		if res.status != http.StatusConflict {
			t.Errorf("join while %s: status %d, want 409; body %s", step.status, res.status, res.body)
		}

		var body httpx.ErrorBody
		decode(t, res, &body)
		if body.Error != step.joinCode {
			t.Errorf("join while %s: error = %q, want %q", step.status, body.Error, step.joinCode)
		}

		resumed := op.mustDo(http.MethodPost, "/resume", nil)
		if resumed.Queue.Status != "OPEN" {
			t.Errorf("after resuming from %s: status %q, want OPEN", step.status, resumed.Queue.Status)
		}
	}

	if _, res := op.client.join(op.queue.Queue.Slug, "Welcome Back", ""); res.status != http.StatusCreated {
		t.Errorf("join after resuming: status %d, want 201; body %s", res.status, res.body)
	}
}

func TestSettingsUpdateOnlyWhatWasSent(t *testing.T) {
	op := newOperator(t, "Settings Shop")

	updated := op.mustDo(http.MethodPatch, "", []byte(`{"name":"Renamed Shop","maxCapacity":5}`))
	if updated.Queue.Name != "Renamed Shop" {
		t.Errorf("name = %q, want Renamed Shop", updated.Queue.Name)
	}
	if updated.Queue.MaxCapacity == nil || *updated.Queue.MaxCapacity != 5 {
		t.Fatalf("max capacity = %v, want 5", updated.Queue.MaxCapacity)
	}
	if updated.Queue.AverageServiceMinutes != 15 {
		t.Errorf("average service = %d, want the original 15", updated.Queue.AverageServiceMinutes)
	}

	// A field left out is a field left alone.
	again := op.mustDo(http.MethodPatch, "", []byte(`{"averageServiceMinutes":30}`))
	if again.Queue.Name != "Renamed Shop" {
		t.Errorf("name = %q after an unrelated update, want it untouched", again.Queue.Name)
	}
	if again.Queue.MaxCapacity == nil || *again.Queue.MaxCapacity != 5 {
		t.Errorf("max capacity = %v after an unrelated update, want it untouched", again.Queue.MaxCapacity)
	}

	// An explicit null is how "no limit" is expressed, and it has to be
	// distinguishable from not mentioning capacity at all.
	cleared := op.mustDo(http.MethodPatch, "", []byte(`{"maxCapacity":null}`))
	if cleared.Queue.MaxCapacity != nil {
		t.Errorf("max capacity = %v, want no limit", cleared.Queue.MaxCapacity)
	}
}

// Renaming the business must not move its URL: that address is printed on a
// sheet taped to a door, and every code already in the wild points at it.
func TestRenamingDoesNotChangeTheSlug(t *testing.T) {
	op := newOperator(t, "Original Name Shop")
	before := op.queue.Queue.Slug

	op.mustDo(http.MethodPatch, "", []byte(`{"name":"Completely Different Name"}`))

	res := op.client.do(http.MethodGet, "/api/queues/"+before, nil)
	if res.status != http.StatusOK {
		t.Fatalf("old slug after rename: status %d, want it to still resolve; body %s", res.status, res.body)
	}
}

func TestSettingsRejectInvalidValues(t *testing.T) {
	op := newOperator(t, "Settings Validation Shop")

	for _, body := range []string{
		`{"name":"   "}`,
		`{"averageServiceMinutes":0}`,
		`{"averageServiceMinutes":481}`,
		`{"maxCapacity":0}`,
		`{"maxCapacity":1001}`,
		`{"nmae":"typo"}`,
	} {
		res := op.do(http.MethodPatch, "", []byte(body))
		if res.status != http.StatusBadRequest {
			t.Errorf("PATCH %s: status %d, want 400; body %s", body, res.status, res.body)
		}
	}
}

// The names toggle is settable from phase 3; phase 5 is what makes it change a
// payload. Storing it now means the queues that predate operators already carry
// the setting.
func TestNamesToggleRoundTrips(t *testing.T) {
	op := newOperator(t, "Names Toggle Shop")

	if view := op.view(); view.Queue.ShowNamesToOperators {
		t.Error("show names defaulted to true; staff seeing names has to be opt-in")
	}

	updated := op.mustDo(http.MethodPatch, "", []byte(`{"showNamesToOperators":true}`))
	if !updated.Queue.ShowNamesToOperators {
		t.Error("show names did not stick")
	}
}

type historyEntry struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Name   string `json:"customerName"`
	Status string `json:"status"`
}

func (o *operator) history() []historyEntry {
	o.client.t.Helper()

	res := o.do(http.MethodGet, "/history", nil)
	if res.status != http.StatusOK {
		o.client.t.Fatalf("history: status %d, body %s", res.status, res.body)
	}

	var body struct {
		Entries []historyEntry `json:"entries"`
	}
	decode(o.client.t, res, &body)
	return body.Entries
}

func TestHistoryHoldsOnlyFinishedEntries(t *testing.T) {
	op := newOperator(t, "History Shop")
	before := op.joinAll("Vivian", "John", "Sarah")

	op.mustDo(http.MethodPost, "/entries/"+before.Waiting[0].ID+"/attend", nil)
	op.mustDo(http.MethodPost, "/entries/"+before.Waiting[1].ID+"/skip", nil)

	history := op.history()
	if len(history) != 2 {
		t.Fatalf("history holds %d entries, want the 2 finished ones", len(history))
	}

	byName := map[string]string{}
	for _, entry := range history {
		byName[entry.Name] = entry.Status
	}
	if byName["Vivian"] != "ATTENDED" {
		t.Errorf("Vivian = %q, want ATTENDED", byName["Vivian"])
	}
	if byName["John"] != "SKIPPED" {
		t.Errorf("John = %q, want SKIPPED", byName["John"])
	}
	if _, present := byName["Sarah"]; present {
		t.Error("Sarah is still waiting and must not appear in history")
	}
}

func TestHistoryRejectsANonsenseLimit(t *testing.T) {
	op := newOperator(t, "History Limit Shop")

	for _, limit := range []string{"0", "-1", "201", "many"} {
		res := op.do(http.MethodGet, "/history?limit="+limit, nil)
		if res.status != http.StatusBadRequest {
			t.Errorf("limit=%s: status %d, want 400; body %s", limit, res.status, res.body)
		}
	}
}
