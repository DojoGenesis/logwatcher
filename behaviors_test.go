package logwatcher_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// TestSeekToEOFMissingFileDeliversFromStart exercises the seekToEOF error branch
// (logwatcher.go:150-153, documented as "or 0 if the file cannot be opened"):
// when Watch is called on a path that does not yet exist, seekToEOF cannot open
// it and returns offset 0. When the file is later created and written, pollFile
// reads from offset 0, so the very FIRST line is delivered. This is the inverse
// of TestSeekToEOF, where the file exists at Watch time so the offset starts at
// EOF and pre-Watch content is correctly skipped.
func TestSeekToEOFMissingFileDeliversFromStart(t *testing.T) {
	// A path inside a real temp dir, but the file itself does not exist yet.
	path := filepath.Join(t.TempDir(), "created-after-watch.log")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("precondition: %q must not exist before Watch (stat err: %v)", path, err)
	}

	w, _, cancel := startWatcher(t)
	defer cancel()

	ctx := context.Background()
	ch, unsub := w.Watch(ctx, path) // seekToEOF fails to open -> offset 0
	defer unsub()

	// Let the watcher poll at least once against the still-missing file
	// (pollFile's os.Open also fails and returns early, leaving offset at 0).
	time.Sleep(150 * time.Millisecond)

	// Now create the file and write its first line.
	if err := os.WriteFile(path, []byte("first-line\n"), 0o644); err != nil {
		t.Fatalf("create file: %v", err)
	}

	// Because the offset started at 0 (file was absent at Watch time), the very
	// first line must be delivered — not skipped as pre-existing content.
	evt := receiveWithTimeout(t, ch, 2*time.Second)
	if evt.Line != "first-line" {
		t.Errorf("got line %q, want %q (offset must start at 0 for a file absent at Watch time)", evt.Line, "first-line")
	}
	if evt.Path != path {
		t.Errorf("got path %q, want %q", evt.Path, path)
	}
}

// TestSlowSubscriberRecoversAfterDrain locks the drop-then-RECOVER contract for
// a slow subscriber. TestSlowSubscriberDoesNotBlockFast (logwatcher_test.go)
// proves a saturated, never-drained subscriber does not block a fast sibling —
// but it never proves the slow subscriber itself comes back to life. broadcast
// (logwatcher.go:202-211) silently drops on a full channel; this test proves
// that once the subscriber drains its buffer, it resumes receiving fresh
// events rather than staying wedged or replaying stale/dropped ones.
//
// Determinism: rather than sleeping and hoping the buffer is full, we busy-wait
// on len(chSlow) == cap(chSlow) (50) with a bounded deadline. Because pollFile
// (logwatcher.go:162-199) scans to EOF and advances the offset in a single
// call, reaching len==cap==50 deterministically proves both (a) the channel is
// saturated and (b) the offset is already past all 60 written lines, so no
// further old-line broadcasts are pending when we drain. The recovery line is
// appended only after the drain completes, so it can only reach chSlow via a
// poll that runs after the append.
func TestSlowSubscriberRecoversAfterDrain(t *testing.T) {
	path := tmpFile(t)
	w, _, cancel := startWatcher(t)
	defer cancel()

	ctx := context.Background()

	// Slow subscriber: never read during setup, so its buffer fills and drops.
	chSlow, unsubSlow := w.Watch(ctx, path)
	defer unsubSlow()

	// Give the watcher one poll cycle to register the file.
	time.Sleep(150 * time.Millisecond)

	// Write more lines than the buffer (50) to guarantee saturation + drops.
	for i := 0; i < 60; i++ {
		appendLine(t, path, fmt.Sprintf("line-%d", i))
	}

	// Saturation sync: busy-wait with a deadline until the channel is full.
	deadline := time.Now().Add(3 * time.Second)
	for len(chSlow) != cap(chSlow) {
		if time.Now().After(deadline) {
			t.Fatalf("channel never saturated: len=%d cap=%d after 3s", len(chSlow), cap(chSlow))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Drain fully — non-blocking loop until the channel is empty.
drainLoop:
	for {
		select {
		case <-chSlow:
		default:
			break drainLoop
		}
	}

	// Recovery: append a fresh line only after the drain, so it can only be
	// delivered by a poll that runs after this append.
	appendLine(t, path, "recovered-line")

	evt := receiveWithTimeout(t, chSlow, 2*time.Second)
	if evt.Line != "recovered-line" {
		t.Errorf("got line %q, want %q (slow subscriber did not recover after drain)", evt.Line, "recovered-line")
	}
}
