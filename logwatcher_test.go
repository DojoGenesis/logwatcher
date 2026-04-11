package logwatcher_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/DojoGenesis/logwatcher"
)

// startWatcher creates a new Watcher, starts it in the background, and returns
// a cancel function that stops it.
func startWatcher(t *testing.T) (*logwatcher.Watcher, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := logwatcher.New()
	go w.Start(ctx)
	return w, ctx, cancel
}

// tmpFile creates a temporary file and returns its path. The file is removed
// when the test ends.
func tmpFile(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "logwatcher-test-*.log")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

// appendLine appends a newline-terminated string to path.
func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, line); err != nil {
		t.Fatalf("append line: %v", err)
	}
}

// receiveWithTimeout waits up to d for one event on ch, or fails the test.
func receiveWithTimeout(t *testing.T, ch <-chan logwatcher.Event, d time.Duration) logwatcher.Event {
	t.Helper()
	select {
	case evt := <-ch:
		return evt
	case <-time.After(d):
		t.Fatalf("timed out waiting for event after %v", d)
		return logwatcher.Event{}
	}
}

// TestSingleFileSubscriber verifies that lines appended after Watch are received.
func TestSingleFileSubscriber(t *testing.T) {
	path := tmpFile(t)
	w, _, cancel := startWatcher(t)
	defer cancel()

	ctx := context.Background()
	ch, unsub := w.Watch(ctx, path)
	defer unsub()

	// Give the watcher one poll cycle to register.
	time.Sleep(150 * time.Millisecond)

	appendLine(t, path, "hello world")

	evt := receiveWithTimeout(t, ch, 2*time.Second)
	if evt.Line != "hello world" {
		t.Errorf("got line %q, want %q", evt.Line, "hello world")
	}
	if evt.Path != path {
		t.Errorf("got path %q, want %q", evt.Path, path)
	}
	if evt.Time.IsZero() {
		t.Error("Event.Time must not be zero")
	}
}

// TestMultipleSubscribersSameFile verifies that all subscribers receive every line.
func TestMultipleSubscribersSameFile(t *testing.T) {
	path := tmpFile(t)
	w, _, cancel := startWatcher(t)
	defer cancel()

	ctx := context.Background()
	ch1, unsub1 := w.Watch(ctx, path)
	defer unsub1()
	ch2, unsub2 := w.Watch(ctx, path)
	defer unsub2()

	time.Sleep(150 * time.Millisecond)

	appendLine(t, path, "broadcast line")

	e1 := receiveWithTimeout(t, ch1, 2*time.Second)
	e2 := receiveWithTimeout(t, ch2, 2*time.Second)

	if e1.Line != "broadcast line" {
		t.Errorf("subscriber 1: got %q, want %q", e1.Line, "broadcast line")
	}
	if e2.Line != "broadcast line" {
		t.Errorf("subscriber 2: got %q, want %q", e2.Line, "broadcast line")
	}
}

// TestSlowSubscriberDoesNotBlockFast verifies that a full channel causes a drop
// rather than stalling delivery to other subscribers.
func TestSlowSubscriberDoesNotBlockFast(t *testing.T) {
	path := tmpFile(t)
	w, _, cancel := startWatcher(t)
	defer cancel()

	ctx := context.Background()

	// Fast subscriber: reads immediately.
	chFast, unsubFast := w.Watch(ctx, path)
	defer unsubFast()

	// Slow subscriber: never reads; channel will fill and drop.
	chSlow, unsubSlow := w.Watch(ctx, path)
	defer unsubSlow()
	_ = chSlow // intentionally not read

	time.Sleep(150 * time.Millisecond)

	// Write more lines than the slow subscriber's buffer (50).
	for i := 0; i < 60; i++ {
		appendLine(t, path, fmt.Sprintf("line-%d", i))
	}

	// The fast subscriber should still receive events promptly.
	deadline := time.Now().Add(3 * time.Second)
	received := 0
	for time.Now().Before(deadline) && received < 60 {
		select {
		case <-chFast:
			received++
		case <-time.After(500 * time.Millisecond):
			// No more events within half a second — stop waiting.
			goto done
		}
	}
done:
	if received == 0 {
		t.Error("fast subscriber received no events; slow subscriber likely blocked delivery")
	}
}

// TestContextCancellationStopsWatcher verifies that cancelling ctx closes the
// subscriber channel, allowing a range loop to terminate.
func TestContextCancellationStopsWatcher(t *testing.T) {
	path := tmpFile(t)
	w, watcherCtx, cancelWatcher := startWatcher(t)
	_ = watcherCtx

	// Use a separate, shorter-lived context for this subscriber.
	subCtx, cancelSub := context.WithCancel(context.Background())

	ch, _ := w.Watch(subCtx, path)

	time.Sleep(150 * time.Millisecond)

	cancelSub()
	cancelWatcher()

	// ch should be closed shortly after cancellation.
	select {
	case _, ok := <-ch:
		if ok {
			// Drain any buffered events and wait for close.
			for range ch {
			}
		}
		// Channel closed — pass.
	case <-time.After(2 * time.Second):
		t.Error("channel was not closed within 2s after context cancellation")
	}
}

// TestSeekToEOF verifies that lines written before Watch is called are not
// delivered to the subscriber.
func TestSeekToEOF(t *testing.T) {
	path := tmpFile(t)

	// Write content before registering.
	appendLine(t, path, "pre-existing line")

	w, _, cancel := startWatcher(t)
	defer cancel()

	ctx := context.Background()
	ch, unsub := w.Watch(ctx, path)
	defer unsub()

	time.Sleep(150 * time.Millisecond)

	// Write a new line after Watch.
	appendLine(t, path, "post-watch line")

	evt := receiveWithTimeout(t, ch, 2*time.Second)
	if evt.Line != "post-watch line" {
		t.Errorf("expected only post-watch line, got %q", evt.Line)
	}

	// Ensure no further events arrive (the pre-existing line must not appear).
	select {
	case extra, ok := <-ch:
		if ok {
			t.Errorf("unexpected extra event: %q", extra.Line)
		}
	case <-time.After(300 * time.Millisecond):
		// No extra event — correct.
	}
}
