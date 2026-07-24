// Package mega defines the link and download abstractions shared by the
// queue engine, UI, and native MEGA protocol implementation.
package mega

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

// QuotaEvent reports that MEGA's transfer limit has stalled a download.
type QuotaEvent struct{ Line string }

// StderrEvent carries a diagnostic line for display in the UI.
type StderrEvent struct{ Line string }

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
func (QuotaEvent) isEvent()     {}
func (StderrEvent) isEvent()    {}
func (ExitEvent) isEvent()      {}
