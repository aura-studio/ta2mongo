// Package source is the data-source layer: each subpackage adapts an external
// origin of log lines (HTTP request body, file tailing, stdin, the ThinkingData
// API) into the common Source contract consumed by the process uploaders.
//
// A Source is anything that, when Run, streams log lines onto a channel and
// closes it when finished (or when the context is cancelled). The process
// package's three upload strategies (single / batch / pipeline) all consume a
// Source; the gateway role wraps each HTTP request body via source/httpbody,
// while the daemon role tails files via source/tailer.
package source

import "context"

// Source streams log lines onto a channel. Run returns a receive-only channel
// that the caller drains; the implementation closes it when the source is
// exhausted (finite sources such as httpbody) or when ctx is cancelled
// (long-running sources such as tailer).
type Source interface {
	Run(ctx context.Context) <-chan string
}
