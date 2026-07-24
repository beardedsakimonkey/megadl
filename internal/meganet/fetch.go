package meganet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"megadl/internal/mega"
)

const (
	fetchChunkSize   = 256 << 20 // ranged request size
	retryWindow      = 640 * time.Minute
	progressInterval = 250 * time.Millisecond
	idleTimeout      = 2 * time.Minute
)

// syncFile downloads one file and emits lifecycle events around it.
// Existing files are skipped.
func (d *Driver) syncFile(ctx context.Context, events chan<- mega.Event, job fileJob) bool {
	if _, err := os.Lstat(job.localPath); err == nil {
		events <- mega.FileSkipEvent{Path: job.localPath}
		return true
	}
	if err := os.MkdirAll(filepath.Dir(job.localPath), 0o755); err != nil {
		events <- mega.FileErrorEvent{Path: job.localPath, Message: err.Error()}
		return false
	}
	events <- mega.FileStartEvent{Path: job.localPath, Remote: job.remotePath, Size: job.size}
	if err := d.fetchFile(ctx, events, job); err != nil {
		if ctx.Err() != nil {
			return false // cancelled mid-file; no error event, stays resumable
		}
		events <- mega.FileErrorEvent{Path: job.localPath, Message: err.Error()}
		return false
	}
	events <- mega.FileDoneEvent{Path: job.localPath}
	return true
}

// fatalErr marks failures that must not be retried (local I/O).
type fatalErr struct{ err error }

func (e fatalErr) Error() string { return e.err.Error() }

// httpStatusErr is a non-2xx status from a download server.
type httpStatusErr int

func (e httpStatusErr) Error() string {
	switch int(e) {
	case 509:
		return "Server returned 509 (over quota)"
	case 500:
		return "Server returned 500 (probably busy)"
	}
	return fmt.Sprintf("Server returned %d", int(e))
}

// fetchFile downloads into .megatmp.<handle> next to the target,
// resuming any previous partial data, verifies the meta-MAC and renames
// the finished file into place.
func (d *Driver) fetchFile(ctx context.Context, events chan<- mega.Event, job fileJob) error {
	url, size, err := job.getURL(ctx)
	if err != nil {
		return err
	}

	tmpPath := filepath.Join(filepath.Dir(job.localPath), ".megatmp."+job.handle)
	f, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	mac, err := newChunkedMAC(job.key.aes, job.key.nonce)
	if err != nil {
		return err
	}

	// resume: feed already-downloaded plaintext through the MAC
	var pos int64
	buf := make([]byte, 1<<20)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			mac.update(buf[:n])
			pos += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("can't read %s for resume: %w", tmpPath, rerr)
		}
	}
	if pos > size {
		return fmt.Errorf("unfinished download %s is larger than the remote file (remove it to fix this)", tmpPath)
	}

	sessionStart := pos
	events <- mega.ProgressEvent{Done: -1, Total: size - pos}
	prog := progressReporter{events: events, sessionStart: sessionStart, sessionTotal: size - sessionStart}

	deadline := time.Now().Add(retryWindow)
	tries := 0
	for pos < size {
		before := pos
		err := d.fetchRange(ctx, job, url, f, mac, &pos, min(size, pos+fetchChunkSize), &prog)
		if err == nil {
			tries = 0
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var fe fatalErr
		if errors.As(err, &fe) {
			return fe.err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("data download failed: %w", err)
		}
		if pos > before {
			tries = 0 // progress was made; don't escalate the backoff
		}
		if tries < 8 {
			tries++
		}
		msg := fmt.Sprintf("WARNING: chunk download failed (%s), re-trying after %d seconds", err, 1<<tries)
		var hs httpStatusErr
		if errors.As(err, &hs) && int(hs) == 509 {
			events <- mega.QuotaEvent{Line: msg}
		}
		events <- mega.StderrEvent{Line: msg}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(1<<tries) * d.retryBase()):
		}
	}

	events <- mega.ProgressEvent{Done: -2, Total: 0}

	if got := mac.finish(); got != job.key.metaMAC {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("MAC mismatch")
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, job.localPath)
}

// fetchRange streams [*pos, end) of the encrypted file, decrypting,
// MAC-ing and persisting as it goes; *pos tracks every byte safely on
// disk so a retry continues exactly where this attempt stopped.
func (d *Driver) fetchRange(ctx context.Context, job fileJob, url string, f *os.File,
	mac *chunkedMAC, pos *int64, end int64, prog *progressReporter) error {

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchdog := time.AfterFunc(idleTimeout, cancel)
	defer watchdog.Stop()

	from := *pos
	req, err := http.NewRequestWithContext(rctx, http.MethodPost,
		fmt.Sprintf("%s/%d-%d", url, from, end-1), http.NoBody)
	if err != nil {
		return fatalErr{err}
	}
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return httpStatusErr(resp.StatusCode)
	}

	stream, err := ctrAt(job.key.aes, job.key.nonce, from)
	if err != nil {
		return fatalErr{err}
	}
	buf := make([]byte, 128<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			watchdog.Reset(idleTimeout)
			stream.XORKeyStream(buf[:n], buf[:n])
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fatalErr{fmt.Errorf("write %s: %w", f.Name(), werr)}
			}
			mac.update(buf[:n])
			*pos += int64(n)
			prog.report(*pos)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if rctx.Err() != nil && ctx.Err() == nil {
				return fmt.Errorf("no data received for %s", idleTimeout)
			}
			return rerr
		}
	}
	if *pos != end {
		return fmt.Errorf("server closed the connection early (%d of %d bytes)", *pos-from, end-from)
	}
	return nil
}

// progressReporter throttles progress events: done is the
// bytes fetched this session, total the session's remaining size.
type progressReporter struct {
	events       chan<- mega.Event
	sessionStart int64
	sessionTotal int64
	last         time.Time
}

func (p *progressReporter) report(pos int64) {
	if time.Since(p.last) < progressInterval {
		return
	}
	p.last = time.Now()
	p.events <- mega.ProgressEvent{Done: pos - p.sessionStart, Total: p.sessionTotal}
}
