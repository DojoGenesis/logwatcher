// Package logwatcher provides a zero-dependency, multi-file log tailer with
// bounded fan-out to multiple subscribers.
//
// Key properties:
//   - Seek-to-EOF on Watch: lines written before Watch is called are never replayed.
//   - Per-file independent offset tracking across polls.
//   - Buffered subscriber channels (50 events); slow consumers drop events silently
//     and never block fast consumers on the same file.
//   - Context-driven lifecycle: cancelling the context passed to Start stops all
//     polling goroutines with no leaks.
//   - 100 ms polling interval; no external dependencies (e.g. fsnotify).
//
// Typical usage:
//
//	w := logwatcher.New()
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//
//	ch, unsub := w.Watch(ctx, "/var/log/app.log")
//	defer unsub()
//
//	go w.Start(ctx)
//
//	for evt := range ch {
//	    fmt.Println(evt.Path, evt.Line)
//	}
package logwatcher

import (
	"bufio"
	"context"
	"os"
	"sync"
	"time"
)

// Event is emitted for every new line appended to a watched file after Watch
// was called.
type Event struct {
	// Path is the absolute or relative path of the file that produced this event,
	// exactly as passed to Watch.
	Path string

	// Line is the raw text of the new line (trailing newline stripped).
	Line string

	// Time is the wall-clock time at which the line was observed.
	Time time.Time
}

// fileState tracks the tail position for a single watched file.
type fileState struct {
	path      string
	offset    int64
	listeners []chan Event
	mu        sync.Mutex
}

// Watcher polls one or more log files and fans out new lines to all
// registered subscribers.
type Watcher struct {
	mu    sync.Mutex
	files map[string]*fileState
}

// New creates a new, idle Watcher. Call Start to begin polling.
func New() *Watcher {
	return &Watcher{
		files: make(map[string]*fileState),
	}
}

// Watch registers path for tailing and returns a channel that receives Events
// and an unsubscribe function. The channel is buffered to 50 events; if the
// consumer falls behind, excess events are dropped silently.
//
// Only lines written after Watch returns are delivered — existing file content
// is skipped by seeking to EOF at registration time.
//
// Calling the returned unsubscribe function closes the channel and removes the
// subscriber. It is safe to call multiple times.
func (w *Watcher) Watch(ctx context.Context, path string) (<-chan Event, func()) {
	ch := make(chan Event, 50)

	w.mu.Lock()
	fs, ok := w.files[path]
	if !ok {
		offset := seekToEOF(path)
		fs = &fileState{
			path:   path,
			offset: offset,
		}
		w.files[path] = fs
	}
	fs.mu.Lock()
	fs.listeners = append(fs.listeners, ch)
	fs.mu.Unlock()
	w.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			fs.mu.Lock()
			for i, l := range fs.listeners {
				if l == ch {
					fs.listeners = append(fs.listeners[:i], fs.listeners[i+1:]...)
					break
				}
			}
			fs.mu.Unlock()
			close(ch)
		})
	}

	// Auto-unsub when ctx is cancelled.
	go func() {
		<-ctx.Done()
		unsub()
	}()

	return ch, unsub
}

// Start polls all registered files at 100 ms intervals until ctx is cancelled.
// It is safe to call Watch before or after Start.
func (w *Watcher) Start(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.mu.Lock()
			files := make([]*fileState, 0, len(w.files))
			for _, fs := range w.files {
				files = append(files, fs)
			}
			w.mu.Unlock()

			for _, fs := range files {
				pollFile(fs)
			}
		}
	}
}

// seekToEOF returns the current EOF offset of path, or 0 if the file cannot
// be opened.
func seekToEOF(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	off, _ := f.Seek(0, 2)
	return off
}

// pollFile reads any new lines from fs since the last poll and broadcasts them
// to all current listeners.
func pollFile(fs *fileState) {
	f, err := os.Open(fs.path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	fs.mu.Lock()
	offset := fs.offset
	fs.mu.Unlock()

	if _, err := f.Seek(offset, 0); err != nil {
		return
	}

	now := time.Now()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		evt := Event{
			Path: fs.path,
			Line: line,
			Time: now,
		}
		broadcast(fs, evt)
	}

	// Advance offset to current read position.
	pos, err := f.Seek(0, 1)
	if err == nil {
		fs.mu.Lock()
		fs.offset = pos
		fs.mu.Unlock()
	}
}

// broadcast delivers evt to all current listeners, dropping on full channels.
func broadcast(fs *fileState, evt Event) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, ch := range fs.listeners {
		select {
		case ch <- evt:
		default: // slow consumer — drop silently
		}
	}
}
