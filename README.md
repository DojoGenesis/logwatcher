# logwatcher

Zero-dependency Go library for tailing multiple log files with bounded fan-out to multiple subscribers.

## Features

- **Multi-file tail** — watch any number of files simultaneously with independent offset tracking
- **Seek-to-EOF on startup** — only lines written *after* `Watch` is called are delivered; no replay of existing content
- **Bounded fan-out** — multiple subscribers per file; each gets a buffered channel (50 events)
- **Silent drop on slow consumers** — a slow subscriber never blocks the watcher or other subscribers
- **Context-driven lifecycle** — cancel the context and all polling goroutines shut down cleanly with no leaks
- **No external dependencies** — pure stdlib; no fsnotify or third-party packages
- **100ms polling interval** — low latency without busy-looping

## Install

```
go get github.com/DojoGenesis/logwatcher
```

Requires Go 1.21+.

## Usage

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

## API

```go
// New creates a new idle Watcher.
func New() *Watcher

// Watch registers path for tailing. Returns a channel of Events and an unsubscribe func.
// Safe to call before or after Start.
func (w *Watcher) Watch(ctx context.Context, path string) (<-chan Event, func())

// Start polls all registered files at 100ms intervals until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context)

// Event is emitted for every new line appended to a watched file.
type Event struct {
    Path string
    Line string
    Time time.Time
}
```

## License

MIT
