package api_test

import (
	"net/http"
	"testing"
)

type seatRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
	Active   bool   `json:"active"`
	Worker   *struct {
		Type       string `json:"type"`
		OperatorID string `json:"operatorId"`
		Name       string `json:"name"`
	} `json:"worker"`
}

type seatsResult struct {
	Seats []seatRecord `json:"seats"`
}

// The seat endpoints: reading is shared with staff, changing is the owner's,
// and taking a chair is each person's own within the store's rules.
func TestSeatEndpoints(t *testing.T) {
	op := newOperator(t, "Seat Shop")
	base := "/api/queues/" + op.queue.Queue.ID
	hired := op.hire("Ada", op.queue.Queue.ID)
	ada := header{"Authorization", "Bearer " + op.client.signIn(hired.AccessCode).Token}

	// Create, refused to staff, then done by the owner.
	res := op.client.do(http.MethodPost, base+"/seats", []byte(`{"name":"Chair 2"}`), ada)
	if res.status != http.StatusUnauthorized {
		t.Fatalf("staff creating a seat: %d, want 401", res.status)
	}
	res = op.client.do(http.MethodPost, base+"/seats", []byte(`{"name":"Chair 2"}`), op.auth)
	if res.status != http.StatusCreated {
		t.Fatalf("create seat: %d %s", res.status, res.body)
	}
	res = op.client.do(http.MethodPost, base+"/seats", []byte(`{"name":"  "}`), op.auth)
	if res.status != http.StatusBadRequest {
		t.Fatalf("blank seat name: %d, want 400", res.status)
	}

	// Staff read the list.
	var listed seatsResult
	res = op.client.do(http.MethodGet, base+"/seats", nil, ada)
	if res.status != http.StatusOK {
		t.Fatalf("staff listing seats: %d %s", res.status, res.body)
	}
	decode(t, res, &listed)
	if len(listed.Seats) != 2 || listed.Seats[1].Name != "Chair 2" {
		t.Fatalf("seats = %+v", listed.Seats)
	}
	counter, chair2 := listed.Seats[0], listed.Seats[1]

	// The owner assigns Ada from the roster, and the roster shows it.
	res = op.client.do(http.MethodPatch, base+"/seats/"+chair2.ID,
		mustJSON(t, map[string]any{"worker": map[string]string{"type": "OPERATOR", "operatorId": hired.Operator.ID}}), op.auth)
	if res.status != http.StatusOK {
		t.Fatalf("assign: %d %s", res.status, res.body)
	}
	decode(t, res, &listed)
	if listed.Seats[1].Worker == nil || listed.Seats[1].Worker.Name != "Ada" {
		t.Fatalf("chair 2 worker = %+v, want Ada", listed.Seats[1].Worker)
	}
	var roster struct {
		Operators []struct {
			ID    string `json:"id"`
			Seats []struct {
				QueueID  string `json:"queueId"`
				SeatName string `json:"seatName"`
			} `json:"seats"`
		} `json:"operators"`
	}
	res = op.client.do(http.MethodGet, "/api/operators", nil, op.auth)
	decode(t, res, &roster)
	if len(roster.Operators) != 1 || len(roster.Operators[0].Seats) != 1 || roster.Operators[0].Seats[0].SeatName != "Chair 2" {
		t.Fatalf("roster seats = %+v, want Ada on Chair 2", roster.Operators)
	}

	// "worker": null takes her off it; Ada takes the counter herself; a
	// chair that is somebody's is refused to staff.
	res = op.client.do(http.MethodPatch, base+"/seats/"+chair2.ID, []byte(`{"worker":null}`), op.auth)
	decode(t, res, &listed)
	if listed.Seats[1].Worker != nil {
		t.Fatalf("chair 2 after unassigning = %+v, want nobody", listed.Seats[1].Worker)
	}
	res = op.client.do(http.MethodPost, base+"/seats/"+counter.ID+"/take", nil, ada)
	if res.status != http.StatusOK {
		t.Fatalf("Ada taking the counter: %d %s", res.status, res.body)
	}
	decode(t, res, &listed)
	if listed.Seats[0].Worker == nil || listed.Seats[0].Worker.OperatorID != hired.Operator.ID {
		t.Fatalf("counter after Ada took it = %+v", listed.Seats[0].Worker)
	}
	res = op.client.do(http.MethodPost, base+"/seats/"+counter.ID+"/take", nil, op.auth)
	if res.status != http.StatusOK {
		t.Fatalf("owner taking Ada's chair: %d %s", res.status, res.body)
	}
	decode(t, res, &listed)
	if listed.Seats[0].Worker == nil || listed.Seats[0].Worker.Type != "OWNER" {
		t.Fatalf("counter after the owner took it = %+v", listed.Seats[0].Worker)
	}
	res = op.client.do(http.MethodPost, base+"/seats/"+counter.ID+"/take", nil, ada)
	if res.status != http.StatusConflict {
		t.Fatalf("Ada taking the owner's chair: %d, want 409", res.status)
	}

	// Fixed chairs: staff cannot pick; the setting is the owner's.
	res = op.client.do(http.MethodPatch, base, []byte(`{"seatsFixed":true}`), op.auth)
	if res.status != http.StatusOK {
		t.Fatalf("fix seats: %d %s", res.status, res.body)
	}
	res = op.client.do(http.MethodPost, base+"/seats/"+chair2.ID+"/take", nil, ada)
	if res.status != http.StatusConflict {
		t.Fatalf("taking with fixed seats: %d, want 409", res.status)
	}

	// Close, reorder, and remove; an occupied chair refuses both.
	op.joinAll("Vivian")
	res = op.client.do(http.MethodPost, base+"/next", mustJSON(t, map[string]string{"seatId": chair2.ID}), op.auth)
	if res.status != http.StatusOK {
		t.Fatalf("call to chair 2: %d %s", res.status, res.body)
	}
	res = op.client.do(http.MethodPatch, base+"/seats/"+chair2.ID, []byte(`{"active":false}`), op.auth)
	if res.status != http.StatusConflict {
		t.Fatalf("closing an occupied chair: %d, want 409", res.status)
	}
	res = op.client.do(http.MethodDelete, base+"/seats/"+chair2.ID, nil, op.auth)
	if res.status != http.StatusConflict {
		t.Fatalf("removing an occupied chair: %d, want 409", res.status)
	}
	res = op.client.do(http.MethodPatch, base+"/seats/"+chair2.ID, []byte(`{"position":1,"name":"Front chair"}`), op.auth)
	if res.status != http.StatusOK {
		t.Fatalf("reorder: %d %s", res.status, res.body)
	}
	decode(t, res, &listed)
	if listed.Seats[0].Name != "Front chair" || listed.Seats[0].Position != 1 || listed.Seats[1].Position != 2 {
		t.Fatalf("after reorder = %+v", listed.Seats)
	}
	res = op.client.do(http.MethodPatch, base+"/seats/"+counter.ID, []byte(`{"active":false}`), op.auth)
	if res.status != http.StatusOK {
		t.Fatalf("close the counter: %d %s", res.status, res.body)
	}
	res = op.client.do(http.MethodDelete, base+"/seats/"+counter.ID, nil, op.auth)
	if res.status != http.StatusOK {
		t.Fatalf("remove the counter: %d %s", res.status, res.body)
	}
	decode(t, res, &listed)
	if len(listed.Seats) != 1 {
		t.Fatalf("after removal = %+v, want one seat", listed.Seats)
	}
	res = op.client.do(http.MethodDelete, base+"/seats/"+chair2.ID, nil, op.auth)
	if res.status != http.StatusConflict {
		t.Fatalf("removing the last chair: %d, want 409", res.status)
	}
}

// History carries the chair, and staff see only the rows they handled.
func TestHistoryIsPerChairAndPerOperator(t *testing.T) {
	op := newOperator(t, "History Chairs Shop")
	base := "/api/queues/" + op.queue.Queue.ID
	hired := op.hire("Ada", op.queue.Queue.ID)
	ada := header{"Authorization", "Bearer " + op.client.signIn(hired.AccessCode).Token}

	res := op.client.do(http.MethodPost, base+"/seats", []byte(`{"name":"Chair 2"}`), op.auth)
	if res.status != http.StatusCreated {
		t.Fatalf("create seat: %d %s", res.status, res.body)
	}
	var listed seatsResult
	res = op.client.do(http.MethodGet, base+"/seats", nil, op.auth)
	decode(t, res, &listed)
	counter, chair2 := listed.Seats[0], listed.Seats[1]

	before := op.joinAll("Vivian", "John")

	// The owner serves Vivian at the counter; Ada serves John at Chair 2.
	res = op.client.do(http.MethodPost, base+"/entries/"+before.Waiting[0].ID+"/serve",
		mustJSON(t, map[string]string{"seatId": counter.ID}), op.auth)
	if res.status != http.StatusOK {
		t.Fatalf("owner serve: %d %s", res.status, res.body)
	}
	op.mustDo(http.MethodPost, "/entries/"+before.Waiting[0].ID+"/attend", nil)
	res = op.client.do(http.MethodPost, base+"/entries/"+before.Waiting[1].ID+"/serve",
		mustJSON(t, map[string]string{"seatId": chair2.ID}), ada)
	if res.status != http.StatusOK {
		t.Fatalf("staff serve: %d %s", res.status, res.body)
	}
	res = op.client.do(http.MethodPost, base+"/entries/"+before.Waiting[1].ID+"/attend", nil, ada)
	if res.status != http.StatusOK {
		t.Fatalf("staff attend: %d %s", res.status, res.body)
	}

	var history struct {
		Entries []struct {
			Number   int    `json:"number"`
			SeatName string `json:"seatName"`
		} `json:"entries"`
		Seats []seatRecord `json:"seats"`
	}
	res = op.client.do(http.MethodGet, base+"/history", nil, op.auth)
	decode(t, res, &history)
	if len(history.Entries) != 2 || len(history.Seats) != 2 {
		t.Fatalf("owner history = %+v with %d seats, want both rows and both seats", history.Entries, len(history.Seats))
	}
	chairs := map[int]string{}
	for _, entry := range history.Entries {
		chairs[entry.Number] = entry.SeatName
	}
	if chairs[1] != "Counter" || chairs[2] != "Chair 2" {
		t.Fatalf("chairs in history = %v, want #1 at the counter and #2 at Chair 2", chairs)
	}

	res = op.client.do(http.MethodGet, base+"/history", nil, ada)
	decode(t, res, &history)
	if len(history.Entries) != 1 || history.Entries[0].Number != 2 {
		t.Fatalf("staff history = %+v, want only the row Ada handled", history.Entries)
	}

	// The owner's counter sees service measured per chair.
	var view struct {
		MeasuredBySeat []struct {
			SeatName string `json:"seatName"`
			Sample   int    `json:"sample"`
		} `json:"measuredBySeat"`
	}
	res = op.client.do(http.MethodGet, base+"/entries", nil, op.auth)
	decode(t, res, &view)
	if len(view.MeasuredBySeat) != 2 || view.MeasuredBySeat[0].SeatName != "Counter" {
		t.Fatalf("measured by seat = %+v", view.MeasuredBySeat)
	}
}
