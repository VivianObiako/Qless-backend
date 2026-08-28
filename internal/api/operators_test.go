package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

type operatorRecord struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName"`
	Status      string   `json:"status"`
	QueueIDs    []string `json:"queueIds"`
}

type operatorResult struct {
	Operator   operatorRecord `json:"operator"`
	AccessCode string         `json:"accessCode"`
}

// hire creates an operator assigned to the given queues and returns them with
// their access code, which the API hands over exactly once.
func (o *operator) hire(name string, queueIDs ...string) operatorResult {
	o.client.t.Helper()

	if queueIDs == nil {
		queueIDs = []string{}
	}
	body := mustJSON(o.client.t, map[string]any{"displayName": name, "queueIds": queueIDs})

	res := o.client.do(http.MethodPost, "/api/operators", body, o.auth)
	if res.status != http.StatusCreated {
		o.client.t.Fatalf("create operator: status %d, body %s", res.status, res.body)
	}

	var result operatorResult
	decode(o.client.t, res, &result)
	if result.AccessCode == "" {
		o.client.t.Fatal("create operator returned no access code")
	}
	return result
}

// signIn redeems a code and returns the session token it produces.
func (c *testClient) signIn(code string) redeemResult {
	c.t.Helper()
	return c.mustRedeem(code)
}

// An operator's code is not a recovery code: it is reusable, it does not
// rotate, and redeeming it hands back only the queues they are assigned to.
func TestOperatorRedeemsTheirCodeAndSeesOnlyTheirQueues(t *testing.T) {
	op := newOperator(t, "Roster Shop")
	second := op.client.createQueueAs("Roster Shop Two", op.queue.OwnerToken)

	hired := op.hire("Ada", op.queue.Queue.ID)

	session := op.client.signIn(hired.AccessCode)
	if session.Role != "OPERATOR" {
		t.Errorf("role = %q, want OPERATOR", session.Role)
	}
	if session.RecoveryCode != "" {
		t.Error("an operator was handed a recovery code; only owners rotate codes")
	}
	if len(session.Queues) != 1 || session.Queues[0].ID != op.queue.Queue.ID {
		t.Fatalf("operator sees %d queues, want only the one they are assigned", len(session.Queues))
	}

	// Reusable: the same code works again, on a second device.
	again := op.client.signIn(hired.AccessCode)
	if again.Token == session.Token {
		t.Error("redeeming twice returned the same token; each device gets its own session")
	}

	// And the queue they are not assigned to stays shut.
	if op.client.canOperate(second.Queue.ID, session.Token) {
		t.Error("operator opened a queue they were never assigned to")
	}
}

// The permission table, enforced. An operator works the counter; they do not
// end the day or reconfigure the business.
func TestOperatorMayWorkTheCounterButNotRunTheBusiness(t *testing.T) {
	op := newOperator(t, "Permission Shop")
	op.joinAll("Vivian", "John")

	hired := op.hire("Ada", op.queue.Queue.ID)
	session := op.client.signIn(hired.AccessCode)
	staff := header{"Authorization", "Bearer " + session.Token}

	base := "/api/queues/" + op.queue.Queue.ID

	view := op.view()
	entry := view.Waiting[0].ID

	allowed := []struct {
		name   string
		method string
		path   string
	}{
		{"read the dashboard", http.MethodGet, base + "/entries"},
		{"read history", http.MethodGet, base + "/history"},
		{"pause", http.MethodPost, base + "/pause"},
		{"resume", http.MethodPost, base + "/resume"},
		{"serve a specific customer", http.MethodPost, base + "/entries/" + entry + "/serve"},
		{"serve next", http.MethodPost, base + "/next"},
	}

	for _, action := range allowed {
		t.Run("may "+action.name, func(t *testing.T) {
			res := op.client.do(action.method, action.path, nil, staff)
			if res.status != http.StatusOK {
				t.Errorf("status = %d, want 200; body %s", res.status, res.body)
			}
		})
	}

	refused := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{"close the queue", http.MethodPost, base + "/close", nil},
		{"reset the queue", http.MethodPost, base + "/reset", nil},
		{"change settings", http.MethodPatch, base, []byte(`{"name":"Renamed By Staff"}`)},
		{"list the roster", http.MethodGet, "/api/operators", nil},
		{"hire someone", http.MethodPost, "/api/operators", []byte(`{"displayName":"Mole","queueIds":[]}`)},
		{"sign out other devices", http.MethodPost, "/api/sessions/revoke-others", nil},
	}

	for _, action := range refused {
		t.Run("may not "+action.name, func(t *testing.T) {
			res := op.client.do(action.method, action.path, action.body, staff)
			if res.status != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401; body %s", res.status, res.body)
			}
		})
	}

	// The queue is untouched by everything that was refused.
	after := op.view()
	if after.Queue.Name != "Permission Shop" || after.Queue.Status == "CLOSED" {
		t.Errorf("queue was changed by a refused request: %+v", after.Queue)
	}
}

// Revoking has to bite on the next request, not the next sign-in.
func TestRevokedOperatorLosesAccessImmediately(t *testing.T) {
	op := newOperator(t, "Revoke Shop")
	hired := op.hire("Ada", op.queue.Queue.ID)
	session := op.client.signIn(hired.AccessCode)

	if !op.client.canOperate(op.queue.Queue.ID, session.Token) {
		t.Fatal("operator could not open their queue before being revoked")
	}

	res := op.client.do(http.MethodPost, "/api/operators/"+hired.Operator.ID+"/revoke", nil, op.auth)
	if res.status != http.StatusOK {
		t.Fatalf("revoke: status %d, body %s", res.status, res.body)
	}

	var revoked operatorResult
	decode(t, res, &revoked)
	if revoked.Operator.Status != "REVOKED" {
		t.Errorf("status = %q, want REVOKED", revoked.Operator.Status)
	}

	if op.client.canOperate(op.queue.Queue.ID, session.Token) {
		t.Error("a revoked operator's existing session still works")
	}

	// And their code is dead, not merely their session.
	if _, res := op.client.redeem(hired.AccessCode); res.status != http.StatusUnauthorized {
		t.Errorf("redeeming a revoked code: status %d, want 401; body %s", res.status, res.body)
	}
}

// Regenerating retires the old code at once. Unlike an owner's recovery code
// there is no staged rotation: whoever hands out the code can hand out another.
func TestRegeneratingAnOperatorCodeRetiresTheOldOne(t *testing.T) {
	op := newOperator(t, "Regenerate Shop")
	hired := op.hire("Ada", op.queue.Queue.ID)

	res := op.client.do(http.MethodPost, "/api/operators/"+hired.Operator.ID+"/code", nil, op.auth)
	if res.status != http.StatusOK {
		t.Fatalf("regenerate: status %d, body %s", res.status, res.body)
	}

	var reissued operatorResult
	decode(t, res, &reissued)
	if reissued.AccessCode == "" || reissued.AccessCode == hired.AccessCode {
		t.Fatalf("regenerate returned %q, want a different code", reissued.AccessCode)
	}

	if _, res := op.client.redeem(hired.AccessCode); res.status != http.StatusUnauthorized {
		t.Errorf("the old code still works: status %d, body %s", res.status, res.body)
	}
	op.client.signIn(reissued.AccessCode)
}

// Unassigning is not revoking. The person keeps their code and their place on
// the roster; they simply have nothing to open.
func TestUnassigningLeavesTheOperatorSignedInWithNothingToOpen(t *testing.T) {
	op := newOperator(t, "Unassign Shop")
	hired := op.hire("Ada", op.queue.Queue.ID)
	session := op.client.signIn(hired.AccessCode)

	res := op.client.do(http.MethodPatch, "/api/operators/"+hired.Operator.ID,
		[]byte(`{"queueIds":[]}`), op.auth)
	if res.status != http.StatusOK {
		t.Fatalf("unassign: status %d, body %s", res.status, res.body)
	}

	if op.client.canOperate(op.queue.Queue.ID, session.Token) {
		t.Error("operator still reaches a queue they are no longer assigned to")
	}

	// Their session is still valid — it simply has an empty list behind it.
	mine := op.client.do(http.MethodGet, "/api/me/queues", nil,
		header{"Authorization", "Bearer " + session.Token})
	if mine.status != http.StatusOK {
		t.Fatalf("my queues: status %d, body %s", mine.status, mine.body)
	}

	var body struct {
		Role   string `json:"role"`
		Queues []struct {
			ID string `json:"id"`
		} `json:"queues"`
	}
	decode(t, mine, &body)
	if body.Role != "OPERATOR" {
		t.Errorf("role = %q, want OPERATOR", body.Role)
	}
	if len(body.Queues) != 0 {
		t.Errorf("unassigned operator sees %d queues, want none", len(body.Queues))
	}
}

// One owner must not be able to reach another's staff by guessing an id.
func TestOperatorsAreScopedToTheirOwner(t *testing.T) {
	op := newOperator(t, "Mine Roster Shop")
	hired := op.hire("Ada", op.queue.Queue.ID)

	stranger := op.client.createQueue("Their Roster Shop")
	strangerAuth := header{"Authorization", "Bearer " + stranger.OwnerToken}

	for _, attempt := range []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{"rename", http.MethodPatch, "/api/operators/" + hired.Operator.ID, []byte(`{"displayName":"Stolen"}`)},
		{"regenerate", http.MethodPost, "/api/operators/" + hired.Operator.ID + "/code", nil},
		{"revoke", http.MethodPost, "/api/operators/" + hired.Operator.ID + "/revoke", nil},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			res := op.client.do(attempt.method, attempt.path, attempt.body, strangerAuth)
			if res.status != http.StatusNotFound {
				t.Errorf("status = %d, want 404; body %s", res.status, res.body)
			}
		})
	}

	// The roster of one owner never contains another's staff.
	res := op.client.do(http.MethodGet, "/api/operators", nil, strangerAuth)
	if res.status != http.StatusOK {
		t.Fatalf("list: status %d, body %s", res.status, res.body)
	}

	var listed operatorsList
	decode(t, res, &listed)
	if len(listed.Operators) != 0 {
		t.Errorf("stranger's roster holds %d operators, want none", len(listed.Operators))
	}
}

// An owner must not be able to assign their staff to somebody else's queue.
func TestOperatorsCannotBeAssignedToAnotherOwnersQueue(t *testing.T) {
	op := newOperator(t, "Assign Scope Shop")
	stranger := op.client.createQueue("Not Yours Shop")

	hired := op.hire("Ada", stranger.Queue.ID)
	if len(hired.Operator.QueueIDs) != 0 {
		t.Errorf("assigned %d queues, want none — that queue belongs to someone else", len(hired.Operator.QueueIDs))
	}

	session := op.client.signIn(hired.AccessCode)
	if op.client.canOperate(stranger.Queue.ID, session.Token) {
		t.Error("operator reached a queue belonging to another business")
	}
}

type operatorsList struct {
	Operators []operatorRecord `json:"operators"`
}

// History has to keep resolving to a name after the person has gone, which is
// the whole reason revoking is a soft delete.
func TestHistoryNamesWhoActedEvenAfterTheyAreRevoked(t *testing.T) {
	op := newOperator(t, "Attribution Shop")
	before := op.joinAll("Vivian", "John")

	hired := op.hire("Ada", op.queue.Queue.ID)
	session := op.client.signIn(hired.AccessCode)
	staff := header{"Authorization", "Bearer " + session.Token}

	// Ada serves one customer; the owner deals with the other.
	res := op.client.do(http.MethodPost,
		"/api/queues/"+op.queue.Queue.ID+"/entries/"+before.Waiting[0].ID+"/skip", nil, staff)
	if res.status != http.StatusOK {
		t.Fatalf("operator skip: status %d, body %s", res.status, res.body)
	}
	op.mustDo(http.MethodPost, "/entries/"+before.Waiting[1].ID+"/skip", nil)

	if res := op.client.do(http.MethodPost,
		"/api/operators/"+hired.Operator.ID+"/revoke", nil, op.auth); res.status != http.StatusOK {
		t.Fatalf("revoke: status %d, body %s", res.status, res.body)
	}

	history := op.historyWithActors()
	if len(history) != 2 {
		t.Fatalf("history holds %d entries, want 2", len(history))
	}

	byName := map[string]string{}
	for _, entry := range history {
		if entry.ActedBy == nil {
			t.Fatalf("entry #%d records nobody as having acted", entry.Number)
		}
		byName[entry.Name] = entry.ActedBy.Type + ":" + entry.ActedBy.OperatorName
	}

	if byName["Vivian"] != "OPERATOR:Ada" {
		t.Errorf("Vivian was dealt with by %q, want OPERATOR:Ada", byName["Vivian"])
	}
	if byName["John"] != "OWNER:" {
		t.Errorf("John was dealt with by %q, want the owner", byName["John"])
	}
}

type attributedEntry struct {
	Number  int    `json:"number"`
	Name    string `json:"customerName"`
	Status  string `json:"status"`
	ActedBy *struct {
		Type         string `json:"type"`
		OperatorName string `json:"operatorName"`
	} `json:"actedBy"`
}

func (o *operator) historyWithActors() []attributedEntry {
	o.client.t.Helper()

	res := o.do(http.MethodGet, "/history", nil)
	if res.status != http.StatusOK {
		o.client.t.Fatalf("history: status %d, body %s", res.status, res.body)
	}

	var body struct {
		Entries []attributedEntry `json:"entries"`
	}
	decode(o.client.t, res, &body)
	return body.Entries
}

func TestOperatorValidation(t *testing.T) {
	op := newOperator(t, "Operator Validation Shop")

	for _, body := range []string{
		`{"displayName":"","queueIds":[]}`,
		`{"displayName":"   ","queueIds":[]}`,
		fmt.Sprintf(`{"displayName":%q,"queueIds":[]}`, string(make([]byte, 0))+longName()),
		`{"displayName":"Ada","queueIds":["not-a-uuid"]}`,
		`{"displayName":"Ada","queues":[]}`,
	} {
		res := op.client.do(http.MethodPost, "/api/operators", []byte(body), op.auth)
		if res.status != http.StatusBadRequest {
			t.Errorf("POST %s: status %d, want 400; body %s", body, res.status, res.body)
		}
	}
}

func longName() string {
	name := make([]byte, 61)
	for i := range name {
		name[i] = 'a'
	}
	return string(name)
}
