// Package engine runs the download queue: one active download,
// byte accounting for the quota view, stop/resume and status upkeep.
package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"megadl/internal/db"
	"megadl/internal/mega"
)

const flushInterval = 5 * time.Second

// The two rate meters read the same bytes over different windows because they
// answer different questions. The short one is the speed the transfer is
// running at right now, which is what the rate column reports. The long one is
// what finish times are projected from: a number that swings with every slow
// second would drag the estimates around with it.
const (
	rateWindow = 5 * time.Second
	avgWindow  = 30 * time.Second
)

// Snapshot is the UI's view of the active download.
type Snapshot struct {
	ActiveID    int64
	CurrentFile string // base name of the file being fetched
	CurrentPath string // exact local path of the file being fetched
	FileSize    int64
	FileDone    int64 // bytes of the current file present locally
	OverallDone int64 // completed files + current file, this download
	Rate        float64
	// AvgRate is the same speed measured over a longer window, for projecting
	// how much longer things take. With nothing running it is the speed the
	// last run was managing when it stopped, so a held queue still projects a
	// finish for the file it is holding — from a rate that was, rather than one
	// that is. It is zero until a transfer has actually measured one.
	AvgRate      float64
	QuotaStalled bool
	// Retry describes the wait between a failed chunk and the next attempt.
	// Its zero value means nothing is waiting.
	Retry RetryWait

	// Paused holds for the queue as a whole, so it is reported whether or not
	// a download is active. PauseReason is empty when the user did it.
	Paused      bool
	PauseReason string
}

// RetryWait is a driver backoff in progress: why the chunk failed, which
// attempt is next, and when it is due. It is held as a deadline rather than a
// remaining duration so the UI can count it down between engine events, on its
// own clock, without the engine having to tick anything.
type RetryWait struct {
	Reason  string
	Attempt int
	Until   time.Time
}

// Waiting reports whether a backoff is in progress. It stays true once the
// deadline passes: the attempt it is waiting on has started but not yet said
// anything, and "retrying" is still what is happening.
func (r RetryWait) Waiting() bool { return !r.Until.IsZero() }

// Remaining is how much of the wait is left, floored at zero.
func (r RetryWait) Remaining() time.Duration {
	if !r.Waiting() {
		return 0
	}
	return max(0, time.Until(r.Until))
}

type active struct {
	id      int64
	proc    mega.Proc
	sizes   map[string]int64 // local path -> size
	doneSet map[string]bool  // local paths already done/skipped
	handles map[string]bool  // node handles selected for this process

	currentFile  string
	currentPath  string
	fileSize     int64
	sessionTotal int64 // bytes remaining at transfer start
	sessionDone  int64 // last raw progress value
	completed    int64 // bytes of finished/skipped files
	quotaStalled bool
	retry        RetryWait
	stopping     bool
	paused       bool // held by the user or by quota; stays in the queue
	requeue      bool // restart with a changed file selection after exit
	lastError    string
	fileFailed   bool
	endStatus    int
	gotEnd       bool

	rate rateMeter // what the transfer is doing now
	avg  rateMeter // ...smoothed, for projecting finish times
}

// restarting is what a download keeps of itself between the process a changed
// selection stopped and the one that replaces it. The download is still the one
// running as far as anything outside the engine is concerned, so reporting it
// through the gap keeps the running file from blinking back to "waiting" for
// the length of a process restart.
type restarting struct {
	id   int64
	file string
	path string
}

type Engine struct {
	drv mega.Driver
	db  *db.DB

	// Notify coalesces "state changed" signals for the UI.
	Notify chan struct{}

	mu          sync.Mutex
	act         *active
	restart     *restarting // set only between a requeue's two processes
	pending     int64       // unflushed downloaded-bytes delta
	kick        chan struct{}
	paused      bool // mirrors queue_state, which is the durable copy
	pauseReason string
	// lastAvg is the smoothed rate the last run was managing when it ended,
	// kept so a held queue can still say how long the file it stopped on has
	// left. It is a rate that was, not one that is: the longer the queue sits,
	// the more the estimate it feeds is a guess about the connection the next
	// run will get. Only in memory, so a restart starts over with no guess.
	lastAvg float64
}

func New(drv mega.Driver, database *db.DB) *Engine {
	e := &Engine{
		drv:    drv,
		db:     database,
		Notify: make(chan struct{}, 1),
		kick:   make(chan struct{}, 1),
	}
	// A pause outlives the process that made it, so the queue comes back up
	// held rather than pulling on a link the user (or MEGA) stopped.
	if database != nil {
		e.paused, e.pauseReason, _ = database.Paused()
	}
	return e
}

func (e *Engine) notify() {
	select {
	case e.Notify <- struct{}{}:
	default:
	}
}

// Kick asks the engine to (re)check the queue.
func (e *Engine) Kick() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// Run drives the queue until ctx is cancelled. Call in a goroutine.
func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			e.mu.Lock()
			if e.act != nil {
				e.act.stopping = true
				e.act.proc.Stop()
			}
			e.mu.Unlock()
			e.flush()
			return
		case <-e.kick:
			e.maybeStart(ctx)
		case <-ticker.C:
			e.flush()
			e.notify()
		}
	}
}

func (e *Engine) maybeStart(ctx context.Context) {
	e.mu.Lock()
	blocked := e.act != nil || e.paused
	e.mu.Unlock()
	if blocked {
		return
	}
	// Whatever this pass settles on — a new process or an empty queue —
	// supersedes any restart the snapshot is still standing in for.
	defer func() {
		e.mu.Lock()
		e.restart = nil
		e.mu.Unlock()
	}()

	dl, err := e.db.NextQueued()
	if err != nil || dl == nil {
		return
	}

	files, err := e.db.Files(dl.ID)
	if err != nil {
		e.db.SetStatus(dl.ID, db.StatusError, err.Error())
		e.notify()
		return
	}

	// Exactly the files that put this download at the head of the queue. An
	// empty set would tell the driver to fetch the whole folder, so bail out
	// rather than download more than was asked for.
	var handles []string
	for _, f := range files {
		if f.Queued && f.Status == db.FilePending {
			handles = append(handles, f.NodeHandle)
		}
	}
	if len(handles) == 0 {
		return
	}

	args := mega.DownloadArgs{URL: dl.URL, Path: dl.DestPath}
	if dl.LinkType == "folder" {
		args.SelectHandles = handles
	}

	a := &active{
		id:      dl.ID,
		sizes:   map[string]int64{},
		doneSet: map[string]bool{},
		handles: map[string]bool{},
		rate:    rateMeter{window: rateWindow},
		avg:     rateMeter{window: avgWindow},
	}
	for _, f := range files {
		a.sizes[f.LocalPath] = f.Size
		if f.Queued && f.Status == db.FilePending {
			a.handles[f.NodeHandle] = true
		}
		if f.Status == db.FileDone || f.Status == db.FileSkipped {
			a.doneSet[f.LocalPath] = true
			a.completed += f.Size
		}
	}

	proc, err := e.drv.Start(ctx, args)
	if err != nil {
		e.db.SetStatus(dl.ID, db.StatusError, err.Error())
		e.notify()
		return
	}
	a.proc = proc

	e.db.MarkStarted(dl.ID)
	e.mu.Lock()
	e.act = a
	e.mu.Unlock()
	e.notify()

	go e.consume(a)
}

func (e *Engine) consume(a *active) {
	for ev := range a.proc.Events() {
		e.mu.Lock()
		switch ev := ev.(type) {
		case mega.FileStartEvent:
			a.currentFile = filepath.Base(ev.Path)
			a.currentPath = ev.Path
			if size, ok := a.sizes[ev.Path]; ok {
				a.fileSize = size
			} else {
				a.fileSize = ev.Size
			}
			a.sessionTotal, a.sessionDone = 0, 0
			a.retry = RetryWait{} // whatever the last file was waiting on is over

		case mega.ProgressEvent:
			switch {
			case ev.Done == -1:
				a.sessionTotal = ev.Total
				a.sessionDone = 0
			case ev.Done == -2:
				if left := a.sessionTotal - a.sessionDone; left > 0 {
					e.pending += left
				}
				a.sessionDone = a.sessionTotal
			case ev.Done >= 0:
				if delta := ev.Done - a.sessionDone; delta > 0 {
					e.pending += delta
					a.rate.add(delta)
					a.avg.add(delta)
					// Bytes are landing again, so the stall is over: the
					// banner — and the pause finish would impose — describe a
					// throttle that is current, not one a retry already got past.
					a.quotaStalled = false
					a.retry = RetryWait{}
				}
				// regression = chunk retry; re-fetched bytes count as
				// they stream in again, so just re-baseline
				a.sessionDone = ev.Done
			}

		case mega.FileDoneEvent:
			e.db.SetFileStatusByLocalPath(a.id, ev.Path, db.FileDone)
			e.creditFile(a, ev.Path)

		case mega.FileSkipEvent:
			e.db.SetFileStatusByLocalPath(a.id, ev.Path, db.FileSkipped)
			e.creditFile(a, ev.Path)

		case mega.FileErrorEvent:
			e.db.SetFileStatusByLocalPath(a.id, ev.Path, db.FileError)
			a.fileFailed = true
			a.lastError = ev.Message

		case mega.WarnEvent:
			if ev.Code == "handle_not_found" {
				e.db.SetFileStatusByHandle(a.id, ev.Handle, db.FileError)
				a.fileFailed = true
				a.lastError = "remote file no longer exists"
			}

		case mega.ErrorEvent:
			a.lastError = ev.Message

		case mega.RetryEvent:
			a.retry = RetryWait{
				Reason:  ev.Reason,
				Attempt: ev.Attempt,
				Until:   time.Now().Add(ev.Delay),
			}
			// Worth keeping as the failure message: if the run gives up
			// without reaching a file error, this is what went wrong.
			if ev.Detail != "" {
				a.lastError = ev.Detail
			}
			if ev.Status == 509 {
				// The driver keeps retrying inside its own window; the banner
				// says so. Only an exit turns this into a paused queue.
				a.quotaStalled = true
			}

		case mega.EndEvent:
			a.endStatus = ev.Status
			a.gotEnd = true

		case mega.ExitEvent:
			e.finishLocked(a, ev.Err)
		}
		e.mu.Unlock()
		e.notify()
	}
}

func (e *Engine) creditFile(a *active, path string) {
	if !a.doneSet[path] {
		a.doneSet[path] = true
		a.completed += a.sizes[path]
	}
	a.currentFile = ""
	a.currentPath = ""
	a.fileSize, a.sessionTotal, a.sessionDone = 0, 0, 0
}

// finishLocked records whatever outcome the run reached. Called with e.mu
// held. Nothing here decides what runs next: queue membership does that, so a
// download the user removed stays out and one the app was killed mid-way
// through comes back on the next launch.
func (e *Engine) finishLocked(a *active, exitErr error) {
	e.flushLocked() // credit remaining bytes while e.act still points here

	switch {
	case a.stopping, a.paused, a.requeue:
		// no outcome to record: the queue already says what happens next
	case a.gotEnd && a.endStatus == 0 && !a.fileFailed:
		if !e.hasQueuedFilesAddedAfterStart(a) {
			e.db.MarkCompleted(a.id, db.StatusDone)
			e.db.DequeuePendingFiles(a.id)
		}
	case a.quotaStalled:
		// hold the queue here rather than hammering a closed door; the
		// download keeps its place at the head
		e.setPausedLocked(true, "daily transfer quota exceeded")
	default:
		msg := a.lastError
		if msg == "" && exitErr != nil {
			msg = exitErr.Error()
		}
		if msg == "" {
			msg = fmt.Sprintf("download ended with status %d", a.endStatus)
		}
		e.db.SetStatus(a.id, db.StatusError, msg)
		// Whatever this run didn't reach leaves the queue with it, so a link
		// that fails every time isn't started again the moment it exits.
		e.db.DequeuePendingFiles(a.id)
	}

	if a.requeue && !a.stopping && !a.paused {
		// The download is still the one running; only its file selection
		// changed. Hold on to what it was fetching so the Kick below can hand
		// the spinner straight over to the replacement process.
		e.restart = &restarting{id: a.id, file: a.currentFile, path: a.currentPath}
	}

	// Hold on to the speed this run reached before its meter goes with it: the
	// rate decays with the clock once bytes stop landing, so it has to be read
	// here rather than whenever the strip next asks for it.
	if r := a.avg.rate(); r > 0 {
		e.lastAvg = r
	}

	e.act = nil
	e.Kick()
}

// hasQueuedFilesAddedAfterStart reports whether this process completed its
// fixed selection while files queued during the run still need fetching. They
// stay in the queue for the next process instead of interrupting this one.
func (e *Engine) hasQueuedFilesAddedAfterStart(a *active) bool {
	files, err := e.db.Files(a.id)
	if err != nil {
		return false
	}
	for _, f := range files {
		if f.Queued && f.Status == db.FilePending && !a.handles[f.NodeHandle] {
			return true
		}
	}
	return false
}

func (e *Engine) flush() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.flushLocked()
}

// flushLocked persists accumulated byte deltas. Called with e.mu held.
func (e *Engine) flushLocked() {
	if e.pending <= 0 {
		return
	}
	n := e.pending
	e.pending = 0
	e.db.LogTransfer(n)
	if e.act != nil {
		e.db.AddDoneBytes(e.act.id, n)
	}
}

// Dequeue takes a download out of the queue, cancelling it first if it is the
// one running. Its partial files stay on disk and stay resumable.
func (e *Engine) Dequeue(id int64) {
	// Out of the queue before the process exits, so the Kick that follows
	// doesn't pick this download straight back up.
	e.db.SetDownloadQueued(id, false)

	e.mu.Lock()
	if e.act != nil && e.act.id == id {
		e.act.stopping = true
		e.act.proc.Stop()
	}
	e.mu.Unlock()
	e.notify()
}

// RetryNow cuts short the backoff the active download is waiting out, so the
// next attempt starts immediately. The wait is cleared here rather than waited
// on: the driver says nothing when an attempt begins, so the countdown has to
// stop when the user skips it or it would sit at zero until the attempt either
// lands bytes or fails again.
func (e *Engine) RetryNow() {
	e.mu.Lock()
	a := e.act
	if a != nil {
		a.retry = RetryWait{}
	}
	e.mu.Unlock()
	if a == nil {
		return
	}
	a.proc.RetryNow()
	e.notify()
}

// Enqueue puts a download at the back of the queue, taking everything in it
// that is not already on disk.
func (e *Engine) Enqueue(id int64) {
	if err := e.db.SetDownloadQueued(id, true); err != nil {
		return
	}
	e.Kick()
	e.notify()
}

// DequeueFile drops one file from the queue. If the running process is
// fetching from its download, that process restarts without it. The partial
// stays resumable.
func (e *Engine) DequeueFile(fileID int64) { e.DequeueFiles([]int64{fileID}) }

// DequeueFiles drops a set of files from the queue — a whole folder at a time,
// as the file pane hands it over. The active process is restarted once for the
// set rather than once per file, and files already on disk are left alone
// since there is nothing of theirs left to drop.
func (e *Engine) DequeueFiles(fileIDs []int64) {
	touched := map[int64]bool{} // downloads whose queued set changed
	pending := map[int64]bool{} // ...where an unfetched file left it
	for _, id := range fileIDs {
		f, err := e.db.File(id)
		if err != nil || !f.Queued {
			continue
		}
		if f.Status == db.FileDone || f.Status == db.FileSkipped {
			continue // already on disk; nothing to drop
		}
		e.db.SetFileQueued(id, false)
		touched[f.DownloadID] = true
		if f.Status == db.FilePending {
			pending[f.DownloadID] = true
		}
	}
	if len(touched) == 0 {
		return
	}
	for id := range touched {
		e.db.RecalcTotalBytes(id)
	}

	e.mu.Lock()
	if e.act != nil && pending[e.act.id] && !e.act.stopping {
		e.act.requeue = true
		e.act.proc.Stop()
	}
	e.mu.Unlock()
	e.notify()
}

// QueueFile adds one file to the queue, which puts its download back in too.
func (e *Engine) QueueFile(fileID int64) { e.QueueFiles([]int64{fileID}) }

// QueueFiles adds a set of files to the queue, which puts their downloads back
// in too. Files added to the active download wait for its current fixed
// selection to finish, then run in a follow-up process.
func (e *Engine) QueueFiles(fileIDs []int64) {
	touched := map[int64]bool{} // downloads the set reaches into
	changed := map[int64]bool{} // ...where a file's queue membership moved
	for _, id := range fileIDs {
		f, err := e.db.File(id)
		if err != nil {
			continue
		}
		if f.Status == db.FileDone || f.Status == db.FileSkipped {
			continue // nothing to fetch
		}
		touched[f.DownloadID] = true
		if !f.Queued || f.Status == db.FileError {
			e.db.SetFileQueued(id, true)
			changed[f.DownloadID] = true
		}
	}
	if len(touched) == 0 {
		return
	}
	for id := range changed {
		e.db.RecalcTotalBytes(id)
	}

	e.mu.Lock()
	act := e.act != nil && touched[e.act.id]
	e.mu.Unlock()

	if !act {
		e.Kick()
	}
	e.notify()
}

// Paused reports whether the queue is held.
func (e *Engine) Paused() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.paused
}

// SetPaused holds or releases the queue. Pausing cancels the download in
// flight without taking it out of the queue, so it keeps its place at the head
// and its partial file; releasing starts it again from where it stopped.
func (e *Engine) SetPaused(paused bool) {
	e.mu.Lock()
	e.setPausedLocked(paused, "")
	e.mu.Unlock()
	if !paused {
		e.Kick()
	}
	e.notify()
}

// setPausedLocked persists the pause and cancels any active download. Called
// with e.mu held.
func (e *Engine) setPausedLocked(paused bool, reason string) {
	e.paused, e.pauseReason = paused, reason
	if e.db != nil {
		e.db.SetPaused(paused, reason)
	}
	if paused {
		// A restart in flight isn't coming back until the pause lifts, so let
		// go of the download it was standing in for.
		e.restart = nil
		if e.act != nil && !e.act.stopping {
			e.act.paused = true
			e.act.proc.Stop()
		}
	}
}

// ActiveID returns the running download's id, or 0. A download between the two
// processes of a restart still counts as running: it is coming straight back,
// and it is no more removable now than it was a moment ago.
func (e *Engine) ActiveID() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.act == nil {
		if e.restart != nil {
			return e.restart.id
		}
		return 0
	}
	return e.act.id
}

func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.act == nil {
		// Nothing is moving, but the last run's speed is still the best answer
		// there is to how long what it stopped on takes, so a held queue keeps
		// projecting from it rather than falling silent.
		s := Snapshot{AvgRate: e.lastAvg, Paused: e.paused, PauseReason: e.pauseReason}
		if e.restart != nil {
			// No byte counts to report mid-restart; the on-disk partial keeps
			// the row's progress where it was.
			s.ActiveID = e.restart.id
			s.CurrentFile = e.restart.file
			s.CurrentPath = e.restart.path
		}
		return s
	}
	a := e.act
	fileDone := a.fileSize - a.sessionTotal + a.sessionDone
	if a.sessionTotal == 0 && a.sessionDone == 0 && a.currentFile != "" {
		fileDone = 0 // transfer not started yet
	}
	return Snapshot{
		ActiveID:     a.id,
		CurrentFile:  a.currentFile,
		CurrentPath:  a.currentPath,
		FileSize:     a.fileSize,
		FileDone:     fileDone,
		OverallDone:  a.completed + fileDone,
		Rate:         a.rate.rate(),
		AvgRate:      a.avg.rate(),
		QuotaStalled: a.quotaStalled,
		Retry:        a.retry,
		Paused:       e.paused,
		PauseReason:  e.pauseReason,
	}
}

// rateMeter is a sliding-window byte-rate estimator. window is how far back it
// looks; the zero value looks back rateWindow.
type rateMeter struct {
	window  time.Duration
	samples []rateSample
}

type rateSample struct {
	t time.Time
	n int64
}

func (r *rateMeter) span() time.Duration {
	if r.window <= 0 {
		return rateWindow
	}
	return r.window
}

func (r *rateMeter) add(n int64) {
	now := time.Now()
	r.samples = append(r.samples, rateSample{now, n})
	cutoff := now.Add(-r.span())
	i := 0
	for i < len(r.samples) && r.samples[i].t.Before(cutoff) {
		i++
	}
	r.samples = r.samples[i:]
}

func (r *rateMeter) rate() float64 {
	if len(r.samples) < 2 {
		return 0
	}
	var total int64
	for _, s := range r.samples {
		total += s.n
	}
	dur := time.Since(r.samples[0].t).Seconds()
	if dur <= 0 {
		return 0
	}
	return float64(total) / dur
}
