package api_test

import (
	"net/http"
	"testing"

	"github.com/vivianobiako/qless/api/internal/httpx"
)

type leaveView struct {
	State struct {
		Queue struct {
			CustomersCanLeave bool `json:"customersCanLeave"`
		} `json:"queue"`
	} `json:"state"`
	Entry *struct {
		Status         string `json:"status"`
		RemovedByStaff bool   `json:"removedByStaff"`
	} `json:"entry"`
}

// A queue lets customers cancel until its owner turns that off, and then the
// API refuses, so a hidden button is not the only thing in the way.
func TestOwnerCanStopCustomersCancelling(t *testing.T) {
	op := newOperator(t, "No Cancel Clinic")
	slug := op.queue.Queue.Slug
	joined, res := op.client.join(slug, "Ada", "")
	if res.status != http.StatusCreated {
		t.Fatalf("join: %d %s", res.status, res.body)
	}
	customer := header{httpx.CustomerTokenHeader, joined.CustomerToken}

	var public leaveView
	decode(t, op.client.do(http.MethodGet, "/api/queues/"+slug, nil), &public)
	if !public.State.Queue.CustomersCanLeave {
		t.Fatal("a new queue should let customers cancel")
	}

	op.mustDo(http.MethodPatch, "", []byte(`{"customersCanLeave":false}`))
	decode(t, op.client.do(http.MethodGet, "/api/queues/"+slug, nil), &public)
	if public.State.Queue.CustomersCanLeave {
		t.Fatal("the public summary still says customers can cancel")
	}

	res = op.client.do(http.MethodPost, "/api/queues/"+slug+"/leave", nil, customer)
	if res.status != http.StatusForbidden {
		t.Fatalf("cancel with the setting off: status %d, want 403; body %s", res.status, res.body)
	}
	var body httpx.ErrorBody
	decode(t, res, &body)
	if body.Error != "leave_disabled" {
		t.Errorf("error code = %q, want leave_disabled", body.Error)
	}
	if view := op.view(); len(view.Waiting) != 1 {
		t.Fatalf("refused cancel still removed them: %d waiting", len(view.Waiting))
	}

	// An unrelated change must not switch it back on.
	op.mustDo(http.MethodPatch, "", []byte(`{"name":"No Cancel Clinic Two"}`))
	decode(t, op.client.do(http.MethodGet, "/api/queues/"+slug, nil), &public)
	if public.State.Queue.CustomersCanLeave {
		t.Fatal("an unrelated change turned cancelling back on")
	}

	op.mustDo(http.MethodPatch, "", []byte(`{"customersCanLeave":true}`))
	if res := op.client.do(http.MethodPost, "/api/queues/"+slug+"/leave", nil, customer); res.status != http.StatusOK {
		t.Fatalf("cancel with the setting back on: status %d; body %s", res.status, res.body)
	}
}

// Staff removing someone ends them as LEFT with a record of who did it, and
// their phone is told it was staff rather than that they cancelled.
func TestStaffRemoveIsToldApartFromACustomerCancel(t *testing.T) {
	op := newOperator(t, "Remove Clinic")
	slug := op.queue.Queue.Slug
	removed, _ := op.client.join(slug, "Ada", "")
	cancelled, _ := op.client.join(slug, "Chidi", "")

	view := op.view()
	op.mustDo(http.MethodPost, "/entries/"+view.Waiting[0].ID+"/remove", nil)
	op.client.do(http.MethodPost, "/api/queues/"+slug+"/leave", nil, header{httpx.CustomerTokenHeader, cancelled.CustomerToken})

	if view := op.view(); len(view.Waiting) != 0 {
		t.Fatalf("%d still waiting, want none", len(view.Waiting))
	}

	me := func(token string) leaveView {
		var v leaveView
		decode(t, op.client.do(http.MethodGet, "/api/queues/"+slug+"/me", nil, header{httpx.CustomerTokenHeader, token}), &v)
		return v
	}
	if v := me(removed.CustomerToken); v.Entry == nil || v.Entry.Status != "LEFT" || !v.Entry.RemovedByStaff {
		t.Errorf("removed customer sees %+v, want LEFT and removedByStaff", v.Entry)
	}
	if v := me(cancelled.CustomerToken); v.Entry == nil || v.Entry.Status != "LEFT" || v.Entry.RemovedByStaff {
		t.Errorf("customer who cancelled sees %+v, want LEFT and not removedByStaff", v.Entry)
	}

	for _, entry := range op.history() {
		if entry.Status != "LEFT" {
			t.Errorf("%s ended %s, want LEFT", entry.Name, entry.Status)
		}
	}

	// A second remove is a stale counter, answered like any other.
	if res := op.do(http.MethodPost, "/entries/"+view.Waiting[0].ID+"/remove", nil); res.status != http.StatusConflict {
		t.Errorf("removing twice: status %d, want 409", res.status)
	}
}
