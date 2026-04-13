# logwatcher

Zero-dependency Go library for tailing multiple log files with bounded fan-out to multiple subscribers.

[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## Quick Start

```bash
go get github.com/DojoGenesis/logwatcher
```

Requires Go 1.21+.

```go
package main

import (
    "context"
    "fmt"

    "github.com/DojoGenesis/logwatcher"
)

func main() {
    w := logwatcher.New()
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    ch, unsub := w.Watch(ctx, "/var/log/app.log")
    defer unsub()

    go w.Start(ctx)

    for evt := range ch {
        fmt.Printf("[%s] %s\n", evt.Path, evt.Line)
    }
}
```

## How It Works

`logwatcher` polls each registered file on a 100 ms ticker. On every tick it seeks to the last known offset, reads any new lines with `bufio.Scanner`, and broadcasts each line as an `Event` to all registered subscriber channels.

Key design decisions:

- **Seek-to-EOF on registration** — `Watch` records the current file size before the first poll. Lines that existed before `Watch` was called are never delivered, so late-joining subscribers see only fresh output.
- **Independent offset tracking** — each file maintains its own read position. Adding a new file watcher does not affect other files.
- **Bounded fan-out** — every call to `Watch` on the same path registers a separate buffered channel (capacity 50). When a subscriber's channel is full, the event is dropped silently via a non-blocking `select`. Slow consumers never block fast consumers on the same file.
- **Context-driven lifecycle** — cancelling the context passed to `Watch` closes that subscriber's channel automatically. Cancelling the context passed to `Start` stops all polling goroutines cleanly with no goroutine leaks.
- **Zero external dependencies** — pure stdlib; no fsnotify, no third-party packages.

## Watching Multiple Files

```go
w := logwatcher.New()
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

ch1, unsub1 := w.Watch(ctx, "/var/log/app.log")
ch2, unsub2 := w.Watch(ctx, "/var/log/worker.log")
defer unsub1()
defer unsub2()

go w.Start(ctx)

// Both channels receive independently; one slow file never blocks the other.
go func() {
    for evt := range ch1 {
        fmt.Println("app:", evt.Line)
    }
}()
for evt := range ch2 {
    fmt.Println("worker:", evt.Line)
}
```

## Multiple Subscribers on One File

```go
ch1, _ := w.Watch(ctx, "/var/log/app.log") // subscriber A
ch2, _ := w.Watch(ctx, "/var/log/app.log") // subscriber B — same file

go w.Start(ctx)

// Every new line is broadcast to both ch1 and ch2.
```

## API Reference

### Types

```go
// Watcher polls one or more log files and fans out new lines to subscribers.
type Watcher struct { /* unexported */ }

// Event is emitted for every new line appended to a watched file after Watch
// was called.
type Event struct {
    Path string    // file path as passed to Watch
    Line string    // raw line text (trailing newline stripped)
    Time time.Time // wall-clock time the line was observed
}
```

### Functions

```go
// New creates a new, idle Watcher. Call Start to begin polling.
func New() *Watcher

// Watch registers path for tailing. Returns:
//   - ch: a buffered channel (cap 50) that receives Events for new lines
//   - unsub: a function that closes ch and removes this subscriber
//
// Only lines written after Watch returns are delivered (seek-to-EOF).
// Cancelling ctx automatically calls unsub.
// Safe to call before or after Start.
func (w *Watcher) Watch(ctx context.Context, path string) (<-chan Event, func())

// Start polls all registered files at 100ms intervals until ctx is cancelled.
// Safe to call Watch before or after Start.
func (w *Watcher) Start(ctx context.Context)
```

## Project Structure

```
logwatcher/
  logwatcher.go       Core implementation: Watcher, Watch, Start, Event
  logwatcher_test.go  Test suite (5 tests)
  go.mod              Module: github.com/DojoGenesis/logwatcher, Go 1.21
  llms.txt            LLM/Context7 discoverability index
```

## Test Suite

```bash
go test -race -v ./...
```

Five tests cover: single-file subscriber, multiple subscribers on one file, slow-subscriber drop (fast subscriber unblocked), context cancellation (channel closed), and seek-to-EOF (pre-existing content not replayed).

## Requirements

- Go 1.21 or later
- No external dependencies

## License

MIT
