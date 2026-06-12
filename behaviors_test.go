package logwatcher_test

import (
	"context"
	"testing"
	"time"
)

// TestEmptyLineSkip verifies that pollFile skips blank lines and delivers only
// non-blank lines to the subscriber (logwatcher.go:181 `if line == "" { continue }`).
func TestEmptyLineSkip(t *testing.T) {
	path := tmpFile(t)
	w, _, cancel := startWatcher(t)
	defer cancel()

	ctx := context.Background()
	ch, unsub := w.Watch(ctx, path)
	defer unsub()

	// Allow one full poll cycle to register the watch and record the current EOF.
	time.Sleep(150 * time.Millisecond)

	// Append a blank line, then a non-blank line.
	appendLine(t, path, "")
	appendLine(t, path, "real-line")

	// The subscriber must receive the non-blank line.
	evt := receiveWithTimeout(t, ch, 2*time.Second)
	if evt.Line != "real-line" {
		t.Errorf("got line %q, want %q", evt.Line, "real-line")
	}

	// No second event should arrive: the blank line must have been skipped.
	select {
	case extra, ok := <-ch:
		if ok {
			t.Errorf("unexpected extra event with line %q (blank line was not skipped)", extra.Line)
		}
	case <-time.After(300 * time.Millisecond):
		// No extra event — correct.
	}
}

// TestUnsubscribeIdempotency verifies that calling the unsubscribe function
// returned by Watch multiple times does not panic (it is guarded by sync.Once
// per the Watch docstring).  A double-close of an unguarded channel would panic.
func TestUnsubscribeIdempotency(t *testing.T) {
	path := tmpFile(t)
	w, _, cancel := startWatcher(t)
	defer cancel()

	ctx := context.Background()
	ch, unsub := w.Watch(ctx, path)

	time.Sleep(150 * time.Millisecond)

	// Call unsub three times; none should panic.
	unsub()
	unsub()
	unsub()

	// The channel must be closed after the first unsub.
	select {
	case _, ok := <-ch:
		if ok {
			// Drain any buffered events and then confirm it closes.
			for range ch {
			}
		}
		// Channel is closed — correct.
	case <-time.After(2 * time.Second):
		t.Error("channel was not closed within 2s after unsub()")
	}
}

// TestPerFileIndependentOffset verifies that each watched file maintains its own
// read offset: appending to file A does not deliver an event to file B's
// subscriber, and vice versa.
func TestPerFileIndependentOffset(t *testing.T) {
	pathA := tmpFile(t)
	pathB := tmpFile(t)

	w, _, cancel := startWatcher(t)
	defer cancel()

	ctx := context.Background()
	chA, unsubA := w.Watch(ctx, pathA)
	defer unsubA()
	chB, unsubB := w.Watch(ctx, pathB)
	defer unsubB()

	// Allow one full poll cycle to register both watches.
	time.Sleep(150 * time.Millisecond)

	appendLine(t, pathA, "line-from-A")
	appendLine(t, pathB, "line-from-B")

	// Subscriber A must receive its own line with the correct path.
	evtA := receiveWithTimeout(t, chA, 2*time.Second)
	if evtA.Line != "line-from-A" {
		t.Errorf("chA: got line %q, want %q", evtA.Line, "line-from-A")
	}
	if evtA.Path != pathA {
		t.Errorf("chA: got path %q, want %q", evtA.Path, pathA)
	}

	// Subscriber B must receive its own line with the correct path.
	evtB := receiveWithTimeout(t, chB, 2*time.Second)
	if evtB.Line != "line-from-B" {
		t.Errorf("chB: got line %q, want %q", evtB.Line, "line-from-B")
	}
	if evtB.Path != pathB {
		t.Errorf("chB: got path %q, want %q", evtB.Path, pathB)
	}

	// chA must NOT have received file B's line.
	select {
	case extra, ok := <-chA:
		if ok {
			t.Errorf("chA received unexpected cross-file event: path=%q line=%q", extra.Path, extra.Line)
		}
	case <-time.After(300 * time.Millisecond):
		// No cross-file bleed — correct.
	}

	// chB must NOT have received file A's line.
	select {
	case extra, ok := <-chB:
		if ok {
			t.Errorf("chB received unexpected cross-file event: path=%q line=%q", extra.Path, extra.Line)
		}
	case <-time.After(300 * time.Millisecond):
		// No cross-file bleed — correct.
	}
}
