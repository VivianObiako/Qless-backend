package api_test

import (
	"net/http"
	"testing"
)

func (c *testClient) newRecoveryCode(sessionToken string) (string, response) {
	c.t.Helper()
	res := c.do(http.MethodPost, "/api/me/recovery-code", nil, header{"Authorization", "Bearer " + sessionToken})
	var body struct {
		RecoveryCode string `json:"recoveryCode"`
	}
	if res.status == http.StatusOK {
		decode(c.t, res, &body)
	}
	return body.RecoveryCode, res
}

// A signed-in owner who has lost their code gets a new one. Once they say
// they have saved it, it is the only one that works.
func TestSignedInOwnerCanReplaceALostCode(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Lost Code Shop")

	fresh, res := client.newRecoveryCode(created.OwnerToken)
	if res.status != http.StatusOK || fresh == "" {
		t.Fatalf("new code: status %d, body %s", res.status, res.body)
	}
	if fresh == created.RecoveryCode {
		t.Fatal("the new code is the old one")
	}

	res = client.do(http.MethodPost, "/api/access/recovery-code/acknowledge", nil,
		header{"Authorization", "Bearer " + created.OwnerToken})
	if res.status != http.StatusNoContent {
		t.Fatalf("acknowledge: status %d, body %s", res.status, res.body)
	}

	if _, res := client.redeem(created.RecoveryCode, header{"X-Forwarded-For", "198.51.100.61"}); res.status != http.StatusUnauthorized {
		t.Errorf("the replaced code: status %d, want 401", res.status)
	}
	recovered, res := client.redeem(fresh, header{"X-Forwarded-For", "198.51.100.62"})
	if res.status != http.StatusOK || recovered.Role != "OWNER" {
		t.Fatalf("the new code: status %d, role %q", res.status, recovered.Role)
	}
	if !client.canOperate(created.Queue.ID, recovered.Token) {
		t.Error("the new code signed in to a business that cannot open its own queue")
	}
}

// Until the owner confirms they have saved it, the old code keeps working: a
// tab closed on the save screen must not lock anyone out.
func TestAnUnsavedNewCodeLeavesTheOldOneWorking(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Unsaved Code Shop")

	fresh, res := client.newRecoveryCode(created.OwnerToken)
	if res.status != http.StatusOK {
		t.Fatalf("new code: status %d", res.status)
	}

	if _, res := client.redeem(created.RecoveryCode, header{"X-Forwarded-For", "198.51.100.63"}); res.status != http.StatusOK {
		t.Errorf("the old code before saving the new one: status %d, want 200", res.status)
	}
	if fresh == "" {
		t.Fatal("no new code came back")
	}
}

func (c *testClient) acknowledge(sessionToken, code string) response {
	c.t.Helper()
	return c.do(http.MethodPost, "/api/access/recovery-code/acknowledge",
		mustJSON(c.t, map[string]string{"code": code}),
		header{"Authorization", "Bearer " + sessionToken})
}

// The race a browser test found: a new code is waiting on the Profile save
// screen when somebody signs in with the old code on another device, which
// stages its own replacement. Saving the first one must not make live a code
// the owner never saw. It is refused, and the old code still gets them in.
func TestSavingACodeReplacedElsewhereChangesNothing(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Raced Code Shop")

	fresh, res := client.newRecoveryCode(created.OwnerToken)
	if res.status != http.StatusOK {
		t.Fatalf("new code: status %d", res.status)
	}
	if _, res := client.redeem(created.RecoveryCode, header{"X-Forwarded-For", "198.51.100.65"}); res.status != http.StatusOK {
		t.Fatalf("sign in elsewhere with the old code: status %d", res.status)
	}

	if res := client.acknowledge(created.OwnerToken, fresh); res.status != http.StatusConflict {
		t.Fatalf("saving a replaced code: status %d, want 409; body %s", res.status, res.body)
	}
	if _, res := client.redeem(created.RecoveryCode, header{"X-Forwarded-For", "198.51.100.66"}); res.status != http.StatusOK {
		t.Errorf("the old code after a refused save: status %d, want 200", res.status)
	}
}

// Pressing Done twice on a slow connection is fine: the second press finds
// the code already live.
func TestSavingTheSameCodeTwiceIsFine(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Double Save Shop")

	fresh, _ := client.newRecoveryCode(created.OwnerToken)
	for i := range 2 {
		if res := client.acknowledge(created.OwnerToken, fresh); res.status != http.StatusNoContent {
			t.Fatalf("save %d: status %d, body %s", i+1, res.status, res.body)
		}
	}
	if _, res := client.redeem(fresh, header{"X-Forwarded-For", "198.51.100.67"}); res.status != http.StatusOK {
		t.Errorf("the saved code: status %d, want 200", res.status)
	}
}

// Only an owner can ask. Staff have their own access codes, and a request
// with no session gets nothing.
func TestOnlyAnOwnerCanGetANewCode(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Staff Code Shop")

	res := client.do(http.MethodPost, "/api/operators",
		[]byte(`{"displayName":"Ada","queueIds":["`+created.Queue.ID+`"]}`),
		header{"Authorization", "Bearer " + created.OwnerToken})
	if res.status != http.StatusCreated {
		t.Fatalf("create operator: status %d, body %s", res.status, res.body)
	}
	var hired struct {
		AccessCode string `json:"accessCode"`
	}
	decode(t, res, &hired)
	staff, res := client.redeem(hired.AccessCode, header{"X-Forwarded-For", "198.51.100.64"})
	if res.status != http.StatusOK {
		t.Fatalf("redeem the operator code: status %d", res.status)
	}

	if _, res := client.newRecoveryCode(staff.Token); res.status != http.StatusUnauthorized {
		t.Errorf("an operator asking: status %d, want 401", res.status)
	}
	if res := client.do(http.MethodPost, "/api/me/recovery-code", nil); res.status != http.StatusUnauthorized {
		t.Errorf("no session: status %d, want 401", res.status)
	}
}
