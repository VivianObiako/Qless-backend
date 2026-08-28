package api_test

import (
	"bytes"
	"net/http"
	"net/url"
	"testing"
)

// The name nobody but the owner should see. Distinctive enough that finding it
// anywhere is unambiguous, whatever shape the payload takes.
const guardedName = "Zebediah Q Featherstonehaugh"

// staffSurfaces is every response a member of staff can reach that could carry
// a customer name. All three are checked together because the leak only has to
// happen on one of them.
func assertStaffSeesNoNames(t *testing.T, client *testClient, queueID, slug, token string) {
	t.Helper()

	auth := header{"Authorization", "Bearer " + token}

	t.Run("dashboard", func(t *testing.T) {
		res := client.do(http.MethodGet, "/api/queues/"+queueID+"/entries", nil, auth)
		if res.status != http.StatusOK {
			t.Fatalf("status %d, body %s", res.status, res.body)
		}
		if bytes.Contains(res.body, []byte(guardedName)) {
			t.Errorf("the dashboard leaked a customer name to staff: %s", res.body)
		}
	})

	t.Run("history", func(t *testing.T) {
		res := client.do(http.MethodGet, "/api/queues/"+queueID+"/history", nil, auth)
		if res.status != http.StatusOK {
			t.Fatalf("status %d, body %s", res.status, res.body)
		}
		if bytes.Contains(res.body, []byte(guardedName)) {
			t.Errorf("history leaked a customer name to staff: %s", res.body)
		}
	})

	t.Run("socket", func(t *testing.T) {
		_, snapshot := client.dial("/api/queues/" + slug + "/ws?k=" + url.QueryEscape(token))
		if bytes.Contains(snapshot.raw, []byte(guardedName)) {
			t.Errorf("the staff socket frame leaked a customer name: %s", snapshot.raw)
		}
	})
}

// With the toggle off — which is the default — an operator runs a queue of
// numbers. This is the guarantee the setting exists to make, and it is checked
// on every surface a name could reach rather than on the one that was changed.
func TestStaffSeeNoNamesWhileTheToggleIsOff(t *testing.T) {
	op := newOperator(t, "Names Off Shop")

	if _, res := op.client.join(op.queue.Queue.Slug, guardedName, ""); res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}
	if _, res := op.client.join(op.queue.Queue.Slug, "Someone Else", ""); res.status != http.StatusCreated {
		t.Fatalf("second join: status %d, body %s", res.status, res.body)
	}

	// One entry finished, so history has something in it to leak.
	view := op.view()
	op.mustDo(http.MethodPost, "/entries/"+view.Waiting[0].ID+"/skip", nil)

	if view.Queue.ShowNamesToOperators {
		t.Fatal("show names defaulted to true; staff seeing names has to be opt-in")
	}

	hired := op.hire("Ada", op.queue.Queue.ID)
	session := op.client.signIn(hired.AccessCode)

	assertStaffSeesNoNames(t, op.client, op.queue.Queue.ID, op.queue.Queue.Slug, session.Token)
}

// The owner is never subject to the toggle. Checked in the same test file so a
// change that silently hides names from everybody cannot pass as a fix.
func TestOwnerAlwaysSeesNames(t *testing.T) {
	op := newOperator(t, "Owner Names Shop")

	if _, res := op.client.join(op.queue.Queue.Slug, guardedName, ""); res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	view := op.view()
	if !view.ShowsNames {
		t.Error("the owner's dashboard reported that it hides names")
	}
	if view.Waiting[0].Name != guardedName {
		t.Errorf("owner sees %q, want the customer's name", view.Waiting[0].Name)
	}

	_, snapshot := op.client.dial(
		"/api/queues/" + op.queue.Queue.Slug + "/ws?k=" + url.QueryEscape(op.queue.OwnerToken))
	if !bytes.Contains(snapshot.raw, []byte(guardedName)) {
		t.Errorf("the owner's socket frame carries no names: %s", snapshot.raw)
	}
}

// Turning the toggle on is what the setting is for.
func TestStaffSeeNamesOnceTheOwnerTurnsThemOn(t *testing.T) {
	op := newOperator(t, "Names On Shop")

	if _, res := op.client.join(op.queue.Queue.Slug, guardedName, ""); res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	op.mustDo(http.MethodPatch, "", []byte(`{"showNamesToOperators":true}`))

	hired := op.hire("Ada", op.queue.Queue.ID)
	session := op.client.signIn(hired.AccessCode)
	auth := header{"Authorization", "Bearer " + session.Token}

	res := op.client.do(http.MethodGet, "/api/queues/"+op.queue.Queue.ID+"/entries", nil, auth)
	if res.status != http.StatusOK {
		t.Fatalf("dashboard: status %d, body %s", res.status, res.body)
	}
	if !bytes.Contains(res.body, []byte(guardedName)) {
		t.Errorf("staff still cannot see names with the toggle on: %s", res.body)
	}

	var view operatorView
	decode(t, res, &view)
	if !view.ShowsNames {
		t.Error("the payload carries names but says it does not")
	}

	_, snapshot := op.client.dial(
		"/api/queues/" + op.queue.Queue.Slug + "/ws?k=" + url.QueryEscape(session.Token))
	if !bytes.Contains(snapshot.raw, []byte(guardedName)) {
		t.Errorf("the staff socket frame carries no names with the toggle on: %s", snapshot.raw)
	}
}

// Turning it back off has to take effect on the next frame, not the next
// sign-in: an owner who realises staff should not be seeing names expects that
// to stop happening immediately.
func TestTurningNamesOffTakesEffectOnTheNextFrame(t *testing.T) {
	op := newOperator(t, "Names Toggled Back Shop")
	op.mustDo(http.MethodPatch, "", []byte(`{"showNamesToOperators":true}`))

	hired := op.hire("Ada", op.queue.Queue.ID)
	session := op.client.signIn(hired.AccessCode)

	// The socket is opened while names are on and stays open across the change.
	conn, _ := op.client.dial(
		"/api/queues/" + op.queue.Queue.Slug + "/ws?k=" + url.QueryEscape(session.Token))

	op.mustDo(http.MethodPatch, "", []byte(`{"showNamesToOperators":false}`))
	// The PATCH itself broadcasts, so drain that frame before the join.
	readEvent(t, conn)

	if _, res := op.client.join(op.queue.Queue.Slug, guardedName, ""); res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	frame := readEvent(t, conn)
	if bytes.Contains(frame.raw, []byte(guardedName)) {
		t.Errorf("a socket opened before the toggle changed is still receiving names: %s", frame.raw)
	}
}
