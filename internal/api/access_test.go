package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/vivianobiako/qless/api/internal/httpx"
)

type redeemBody struct {
	Code string `json:"code"`
}

type redeemResult struct {
	Role         string `json:"role"`
	Token        string `json:"token"`
	RecoveryCode string `json:"recoveryCode"`
	Queues       []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"queues"`
}

// redeem posts a code from the given address, so a test can prove which key the
// limiter is counting on.
func (c *testClient) redeem(code string, headers ...header) (redeemResult, response) {
	c.t.Helper()

	res := c.do(http.MethodPost, "/api/access/redeem", mustJSON(c.t, redeemBody{Code: code}), headers...)

	var result redeemResult
	if res.status == http.StatusOK {
		decode(c.t, res, &result)
	}
	return result, res
}

func (c *testClient) mustRedeem(code string) redeemResult {
	c.t.Helper()

	result, res := c.redeem(code)
	if res.status != http.StatusOK {
		c.t.Fatalf("redeem: status %d, body %s", res.status, res.body)
	}
	if result.Token == "" {
		c.t.Fatal("redeem returned no session token")
	}
	return result
}

// canOperate reports whether a session token still opens a queue's dashboard.
func (c *testClient) canOperate(queueID, sessionToken string) bool {
	c.t.Helper()

	res := c.do(http.MethodGet, "/api/queues/"+queueID+"/entries", nil,
		header{"Authorization", "Bearer " + sessionToken})

	switch res.status {
	case http.StatusOK:
		return true
	case http.StatusUnauthorized:
		return false
	default:
		c.t.Fatalf("list entries: unexpected status %d, body %s", res.status, res.body)
		return false
	}
}

// Recovering on a new device must not sign out the counter tablet. This is the
// whole reason session tokens are rows rather than a single column: the common
// case is a new phone, not a stolen one.
func TestRecoveringOnANewDeviceLeavesOtherSessionsAlone(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Recovery Shop")

	recovered := client.mustRedeem(created.RecoveryCode)

	if recovered.Role != "OWNER" {
		t.Errorf("role = %q, want OWNER", recovered.Role)
	}
	if len(recovered.Queues) != 1 || recovered.Queues[0].ID != created.Queue.ID {
		t.Errorf("recovered %d queues, want the one this owner holds", len(recovered.Queues))
	}
	if recovered.Token == created.OwnerToken {
		t.Error("recovery reissued the same token; the new device must get its own row")
	}

	if !client.canOperate(created.Queue.ID, created.OwnerToken) {
		t.Error("the original device was signed out by a recovery it had nothing to do with")
	}
	if !client.canOperate(created.Queue.ID, recovered.Token) {
		t.Error("the recovered device cannot open the queue it was just handed")
	}
}

// Signing out other devices is a separate, deliberate act — and it keeps the
// device that asked.
func TestRevokeOtherSessionsSignsOutEveryOtherDevice(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Revoke Shop")
	recovered := client.mustRedeem(created.RecoveryCode)

	res := client.do(http.MethodPost, "/api/sessions/revoke-others", nil,
		header{"Authorization", "Bearer " + recovered.Token})
	if res.status != http.StatusOK {
		t.Fatalf("revoke others: status %d, body %s", res.status, res.body)
	}

	var body struct {
		Revoked int `json:"revoked"`
	}
	decode(t, res, &body)
	if body.Revoked != 1 {
		t.Errorf("revoked = %d, want 1", body.Revoked)
	}

	if client.canOperate(created.Queue.ID, created.OwnerToken) {
		t.Error("the old device still works after being signed out")
	}
	if !client.canOperate(created.Queue.ID, recovered.Token) {
		t.Error("revoking other sessions signed out the device that asked")
	}
}

func TestRevokeOtherSessionsRejectsAnUnknownToken(t *testing.T) {
	client := newTestClient(t)

	res := client.do(http.MethodPost, "/api/sessions/revoke-others", nil,
		header{"Authorization", "Bearer not-a-session"})
	if res.status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body %s", res.status, res.body)
	}
}

// A redeemed code is single-use: once its replacement has been acknowledged,
// replaying it gets nothing.
func TestAcknowledgedRecoveryCodeCannotBeReplayed(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Replay Shop")

	recovered := client.mustRedeem(created.RecoveryCode)
	if recovered.RecoveryCode == "" {
		t.Fatal("recovery returned no replacement code; the owner would have nothing left to recover with")
	}
	if recovered.RecoveryCode == created.RecoveryCode {
		t.Fatal("recovery returned the same code it consumed")
	}

	res := client.do(http.MethodPost, "/api/access/recovery-code/acknowledge", nil,
		header{"Authorization", "Bearer " + recovered.Token})
	if res.status != http.StatusNoContent {
		t.Fatalf("acknowledge: status %d, body %s", res.status, res.body)
	}

	if _, res := client.redeem(created.RecoveryCode); res.status != http.StatusUnauthorized {
		t.Errorf("replaying the used code: status %d, want 401; body %s", res.status, res.body)
	}

	// And the replacement is what works now.
	client.mustRedeem(recovered.RecoveryCode)
}

// Until the replacement is acknowledged, the old code still works. Rotating in
// place would mean a response lost in flight — a dropped connection, a closed
// tab — locks the owner out of their own business permanently.
func TestUnacknowledgedRecoveryLeavesTheOldCodeUsable(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Interrupted Recovery Shop")

	first := client.mustRedeem(created.RecoveryCode)

	// The owner never saw first.RecoveryCode: the response did not reach them.
	// Their code has to still work.
	second := client.mustRedeem(created.RecoveryCode)

	if !client.canOperate(created.Queue.ID, first.Token) {
		t.Error("the first recovered session was invalidated by the second")
	}
	if !client.canOperate(created.Queue.ID, second.Token) {
		t.Error("the second recovery produced a token that does not work")
	}
}

// Every failure looks the same from outside, whatever went wrong. Redeem is the
// one endpoint where guessing wins something, so it must not tell an attacker
// that a code was close.
func TestRedeemAnswersEveryBadCodeIdentically(t *testing.T) {
	client := newTestClient(t)

	codes := []string{"", "   ", "not-a-code", "ZZZZ-ZZZZ-ZZZZ-ZZZZ", "1234"}

	var first []byte
	for _, code := range codes {
		// Its own address, so this test is measuring the answers rather than
		// racing the limiter that paces them.
		_, res := client.redeem(code, header{"X-Forwarded-For", "192.0.2.9"})
		if res.status != http.StatusUnauthorized {
			t.Fatalf("redeem %q: status %d, want 401; body %s", code, res.status, res.body)
		}

		var body httpx.ErrorBody
		decode(t, res, &body)
		if body.Error != "invalid_code" {
			t.Errorf("redeem %q: error code = %q, want invalid_code", code, body.Error)
		}

		normalised, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("re-encode error body: %v", err)
		}
		if first == nil {
			first = normalised
			continue
		}
		if string(normalised) != string(first) {
			t.Errorf("redeem %q answered %s, but an earlier bad code answered %s", code, normalised, first)
		}
	}
}

// Guessing is paced per address, whatever codes are being tried.
func TestRedeemIsRateLimitedByAddress(t *testing.T) {
	client := newTestClient(t)

	var sawRateLimit bool
	for attempt := 0; attempt < 8 && !sawRateLimit; attempt++ {
		// Distinct first groups, so this cannot be the code limiter answering.
		code := fmt.Sprintf("%04d-0000-0000-0000", attempt)
		_, res := client.redeem(code, header{"X-Forwarded-For", "203.0.113.7"})
		sawRateLimit = res.status == http.StatusTooManyRequests
	}

	if !sawRateLimit {
		t.Error("eight wrong codes from one address were never rate limited")
	}
}

// And per code, so spreading the guessing across a botnet does not buy an
// attacker a free sweep through codes that begin alike.
func TestRedeemIsRateLimitedByCodePrefix(t *testing.T) {
	client := newTestClient(t)

	var sawRateLimit bool
	for attempt := 0; attempt < 8 && !sawRateLimit; attempt++ {
		// One shared first group, each attempt from a different address.
		code := fmt.Sprintf("ABCD-0000-0000-%04d", attempt)
		address := fmt.Sprintf("198.51.100.%d", attempt+1)
		_, res := client.redeem(code, header{"X-Forwarded-For", address})
		sawRateLimit = res.status == http.StatusTooManyRequests
	}

	if !sawRateLimit {
		t.Error("eight guesses at one code prefix were never rate limited")
	}
}

// A code is read off paper and typed by a person, so it matches however they
// space and case it.
func TestRedeemAcceptsACodeAsItWasTyped(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Typing Shop")

	typed := ""
	for _, r := range created.RecoveryCode {
		if r == '-' {
			typed += " "
			continue
		}
		typed += string(r)
	}

	client.mustRedeem(" " + typed + " ")
}

// A second queue belongs to the business that asked for it, which is what makes
// "my queues" a list rather than a pile of unrelated links.
func TestCreatingASecondQueueAttachesItToTheSameOwner(t *testing.T) {
	client := newTestClient(t)

	first := client.createQueue("First Chair")
	second := client.createQueueAs("Second Chair", first.OwnerToken)

	if second.RecoveryCode != "" {
		t.Error("adding a queue reissued a recovery code; the owner already has one and this one would be a lie")
	}

	res := client.do(http.MethodGet, "/api/me/queues", nil,
		header{"Authorization", "Bearer " + first.OwnerToken})
	if res.status != http.StatusOK {
		t.Fatalf("my queues: status %d, body %s", res.status, res.body)
	}

	var mine struct {
		Role   string `json:"role"`
		Queues []struct {
			ID string `json:"id"`
		} `json:"queues"`
	}
	decode(t, res, &mine)

	if mine.Role != "OWNER" {
		t.Errorf("role = %q, want OWNER", mine.Role)
	}

	held := map[string]bool{}
	for _, q := range mine.Queues {
		held[q.ID] = true
	}
	if !held[first.Queue.ID] || !held[second.Queue.ID] {
		t.Errorf("my queues returned %d queues, want both of this owner's", len(mine.Queues))
	}

	// The original session opens the queue it never saw created.
	if !client.canOperate(second.Queue.ID, first.OwnerToken) {
		t.Error("the owner cannot operate their own second queue")
	}
}

// A session that has expired or been revoked must not quietly become a second
// business: that would scatter one person's queues across two owners, invisibly
// and with no way back.
func TestCreatingAQueueWithAnUnknownTokenIsRefused(t *testing.T) {
	client := newTestClient(t)

	res := client.do(http.MethodPost, "/api/queues",
		mustJSON(t, createQueueBody{Name: "Ghost Shop", AverageServiceMinutes: 15}),
		header{"Authorization", "Bearer not-a-session"})

	if res.status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body %s", res.status, res.body)
	}
}

func TestMyQueuesRequiresASession(t *testing.T) {
	client := newTestClient(t)

	res := client.do(http.MethodGet, "/api/me/queues", nil)
	if res.status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body %s", res.status, res.body)
	}
}
