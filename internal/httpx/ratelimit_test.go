package httpx_test

import (
	"testing"
	"time"

	"github.com/vivianobiako/qless/api/internal/httpx"
)

// A limiter paces an attacker but never stops one, because it refills. The
// lockout is what turns sustained guessing into a closed door.
func TestLockoutShutsAKeyOutAfterEnoughFailures(t *testing.T) {
	lockout := httpx.NewLockout(3, time.Minute, time.Minute)

	for attempt := 1; attempt < 3; attempt++ {
		lockout.Fail("198.51.100.4")
		if lockout.Locked("198.51.100.4") {
			t.Fatalf("locked out after %d failures, want 3", attempt)
		}
	}

	lockout.Fail("198.51.100.4")
	if !lockout.Locked("198.51.100.4") {
		t.Error("still open after reaching the failure threshold")
	}

	if lockout.Locked("203.0.113.9") {
		t.Error("one address's failures locked out a different address")
	}
}

// Someone mistyping a code and then getting it right walks away with nothing
// held against them.
func TestLockoutForgetsFailuresAfterASuccess(t *testing.T) {
	lockout := httpx.NewLockout(3, time.Minute, time.Minute)

	lockout.Fail("198.51.100.4")
	lockout.Fail("198.51.100.4")
	lockout.Reset("198.51.100.4")

	lockout.Fail("198.51.100.4")
	lockout.Fail("198.51.100.4")
	if lockout.Locked("198.51.100.4") {
		t.Error("failures from before a success still counted toward the lockout")
	}
}

// Failures spread thinly enough are someone with a bad memory, not an attack.
func TestLockoutForgetsFailuresOlderThanTheWindow(t *testing.T) {
	lockout := httpx.NewLockout(3, 20*time.Millisecond, time.Minute)

	lockout.Fail("198.51.100.4")
	lockout.Fail("198.51.100.4")
	time.Sleep(30 * time.Millisecond)

	lockout.Fail("198.51.100.4")
	lockout.Fail("198.51.100.4")
	if lockout.Locked("198.51.100.4") {
		t.Error("failures from outside the window counted toward the lockout")
	}
}

func TestLockoutReopensAfterThePenalty(t *testing.T) {
	lockout := httpx.NewLockout(2, time.Minute, 20*time.Millisecond)

	lockout.Fail("198.51.100.4")
	lockout.Fail("198.51.100.4")
	if !lockout.Locked("198.51.100.4") {
		t.Fatal("not locked out after reaching the threshold")
	}

	time.Sleep(30 * time.Millisecond)
	if lockout.Locked("198.51.100.4") {
		t.Error("still locked out after the penalty expired")
	}
}
