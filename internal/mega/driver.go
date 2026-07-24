package mega

import "context"

// Driver lists public MEGA links and starts downloads.
type Driver interface {
	List(ctx context.Context, url string) ([]Node, error)
	Start(ctx context.Context, args DownloadArgs) (Proc, error)
}

// Proc is a running download. ExitEvent is always the final event before
// the event channel closes.
type Proc interface {
	Events() <-chan Event
	// Stop asks the download to terminate. Partial files stay resumable.
	Stop()
}

// DownloadArgs describes a download request.
type DownloadArgs struct {
	URL           string
	Path          string   // destination directory (folder links) or exact file name (file links)
	SelectHandles []string // folder links: node handles to fetch
}
