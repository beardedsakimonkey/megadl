// Package mega defines the link and download abstractions shared by the
// queue engine, UI, and native MEGA protocol implementation.
package mega

import "time"

// Node is one entry in a public link.
type Node struct {
	Index  int
	Path   string
	Name   string
	Type   string // "file" | "folder"
	Size   int64
	Handle string
	Parent string
}

func (n Node) IsDir() bool { return n.Type == "folder" }

// Event is a state change from a running download.
type Event interface{ isEvent() }

// FileStartEvent precedes the transfer of one file.
type FileStartEvent struct {
	Path   string // local path
	Remote string
	Size   int64
}

// ProgressEvent carries raw transfer progress. Done is bytes fetched this
// session (-1 = transfer start, -2 = finished; it may regress on a chunk
// retry), and Total is bytes remaining this session after resume seeding.
type ProgressEvent struct {
	Done  int64
	Total int64
}

type FileDoneEvent struct{ Path string }
type FileSkipEvent struct{ Path string }

type FileErrorEvent struct {
	Path    string
	Message string
}

// WarnEvent reports a non-fatal problem such as a selected remote handle
// that no longer exists.
type WarnEvent struct {
	Code   string
	Handle string
}

// ErrorEvent is a link-level failure.
type ErrorEvent struct{ Message string }

// EndEvent reports the result of a completed download.
type EndEvent struct{ Status int }

// RetryEvent reports a chunk that failed and the wait before the next
// attempt. It carries what the driver knows in fields rather than a formatted
// line, so the UI can count the wait down, say why it is waiting, and offer to
// cut it short. Status is the HTTP status behind the failure, 0 when it wasn't
// one; 509 is MEGA's transfer limit.
type RetryEvent struct {
	Reason  string        // short phrase: "server busy", "transfer quota exceeded"
	Detail  string        // the underlying error, for the failure message
	Status  int           // HTTP status behind it, 0 when it wasn't one
	Attempt int           // 1-based; back to 1 whenever bytes land
	Delay   time.Duration // until the next attempt
}

// ExitEvent is the final event when a download terminates.
type ExitEvent struct{ Err error }

func (FileStartEvent) isEvent() {}
func (ProgressEvent) isEvent()  {}
func (FileDoneEvent) isEvent()  {}
func (FileSkipEvent) isEvent()  {}
func (FileErrorEvent) isEvent() {}
func (WarnEvent) isEvent()      {}
func (ErrorEvent) isEvent()     {}
func (EndEvent) isEvent()       {}
func (RetryEvent) isEvent()     {}
func (ExitEvent) isEvent()      {}
