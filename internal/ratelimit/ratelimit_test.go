package ratelimit

import (
	"testing"
	"time"
)

func TestLimiterBurstThenRefill(t *testing.T) {
	l := NewLimiter()

	// Burst of 3 allowed immediately (burst == perHour).
	for i := 0; i < 3; i++ {
		if !l.Allow(1, 3) {
			t.Fatalf("message %d of burst rejected", i+1)
		}
	}
	if l.Allow(1, 3) {
		t.Fatal("bucket should be empty after burst")
	}

	// A different app has its own bucket.
	if !l.Allow(2, 3) {
		t.Fatal("second app should have its own bucket")
	}

	// Invalid perHour is unlimited.
	for i := 0; i < 50; i++ {
		if !l.Allow(3, 0) {
			t.Fatal("perHour 0 should be unlimited")
		}
	}
}

func TestLimiterUpdateAndRemove(t *testing.T) {
	l := NewLimiter()
	for i := 0; i < 5; i++ {
		l.Allow(1, 5)
	}
	if l.Allow(1, 5) {
		t.Fatal("bucket should be exhausted")
	}

	// Reconfiguration resets the bucket.
	l.Update(1, 10)
	if !l.Allow(1, 10) {
		t.Fatal("updated bucket should allow again")
	}

	l.Remove(1)
	// Removed bucket: a stale Allow call recreates it with the passed rate.
	if !l.Allow(1, 2) {
		t.Fatal("removed bucket should be re-created lazily")
	}
}

func TestLockoutBansAfterFailures(t *testing.T) {
	l := NewLockout(3, time.Minute, time.Hour)

	for i := 0; i < 3; i++ {
		if l.Banned("10.0.0.1") {
			t.Fatalf("banned before limit reached (failure %d)", i+1)
		}
		l.RecordFailure("10.0.0.1")
	}
	if !l.Banned("10.0.0.1") {
		t.Fatal("IP should be banned after limit reached")
	}
	if l.Banned("10.0.0.2") {
		t.Fatal("other IPs must not be affected")
	}

	l.Reset("10.0.0.1")
	if l.Banned("10.0.0.1") {
		t.Fatal("Reset should clear the ban")
	}
}

func TestLockoutWindowReset(t *testing.T) {
	// A failure older than the window must not count toward the limit.
	l := NewLockout(2, 50*time.Millisecond, time.Hour)
	l.RecordFailure("10.0.0.1")
	time.Sleep(80 * time.Millisecond)
	l.RecordFailure("10.0.0.1")
	if l.Banned("10.0.0.1") {
		t.Fatal("failures across the window boundary should not ban")
	}
}
