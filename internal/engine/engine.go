// Package engine runs the download queue: one active download,
// byte accounting for the quota view, stop/resume and status upkeep.
package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"megadl/internal/db"
	"megadl/internal/mega"
)

const flushInterval = 5 * time.Second

// Snapshot is the UI's view of the active download.
type Snapshot struct {
	ActiveID     int64
	CurrentFile  string // base name of the file being fetched
	CurrentPath  string // exact local path of the file being fetched
	FileSize     int64
	FileDone     int64 // bytes of the current file present locally
	OverallDone  int64 // completed files + current file, this download
	Rate         float64
	QuotaStalled bool
	StderrTail   []string
}

type active struct {
	id      int64
	proc    mega.Proc
	sizes   map[string]int64 // local path -> size
	doneSet map[string]bool  // local paths already done/skipped

	currentFile  string
	currentPath  string
	fileSize     int64
	sessionTotal int64 // bytes remaining at transfer start
	sessionDone  int64 // last raw progress value
	completed    int64 // bytes of finished/skipped files
	quotaStalled bool
	stopping     bool
	requeue      bool // restart with a changed file selection after exit
	lastError    string
	fileFailed   bool
	endStatus    int
	gotEnd       bool
	stderrTail   []string

	rate rateMeter
}

type Engine struct {
	drv mega.Driver
	db  *db.DB

	// Notify coalesces "state changed" signals for the UI.
	Notify chan struct{}

	mu      sync.Mutex
	act     *active
	pending int64 // unflushed downloaded-bytes delta
	kick    chan struct{}
}

func New(drv mega.Driver, database *db.DB) *Engine {
	return &Engine{
		drv:    drv,
		db:     database,
		Notify: make(chan struct{}, 1),
		kick:   make(chan struct{}, 1),
	}
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
	busy := e.act != nil
	e.mu.Unlock()
	if busy {
		return
	}

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

	var handles []string
	for _, f := range files {
		if f.Wanted {
			handles = append(handles, f.NodeHandle)
		}
	}
	if len(files) > 0 && len(handles) == 0 {
		// every file was individually stopped; nothing to fetch
		e.db.SetStatus(dl.ID, db.StatusStopped, "")
		e.notify()
		e.Kick() // give the next queued download a turn
		return
	}

	args := mega.DownloadArgs{URL: dl.URL, Path: dl.DestPath}
	if dl.LinkType == "folder" {
		if len(handles) > 0 {
			// the enqueue-time selection goes stale once a listing
			// refresh merges remotely added files, so always select
			// the current wanted set
			args.SelectHandles = handles
		} else if dl.Selection != "" {
			// no file rows recorded; fall back to the stored selection
			args.SelectHandles = strings.Split(dl.Selection, ",")
		}
	}

	a := &active{
		id:      dl.ID,
		sizes:   map[string]int64{},
		doneSet: map[string]bool{},
	}
	for _, f := range files {
		a.sizes[f.LocalPath] = f.Size
		if f.Status == db.FileDone || f.Status == db.FileSkipped {
			a.doneSet[f.LocalPath] = true
			a.completed += f.Size
		}
	}

	e.db.ResetPendingFiles(dl.ID)
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

		case mega.QuotaEvent:
			if !a.quotaStalled {
				a.quotaStalled = true
				e.db.SetStatus(a.id, db.StatusQuota, "")
			}

		case mega.StderrEvent:
			a.stderrTail = append(a.stderrTail, ev.Line)
			if len(a.stderrTail) > 30 {
				a.stderrTail = a.stderrTail[len(a.stderrTail)-30:]
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

// finishLocked decides the download's final status. Called with e.mu held.
func (e *Engine) finishLocked(a *active, exitErr error) {
	e.flushLocked() // credit remaining bytes while e.act still points here

	switch {
	case a.stopping:
		e.db.SetStatus(a.id, db.StatusStopped, "")
	case a.requeue:
		// per-file stop/resume changed the selection; run again with it
		e.db.SetStatus(a.id, db.StatusQueued, "")
	case a.gotEnd && a.endStatus == 0 && !a.fileFailed:
		e.db.MarkCompleted(a.id, db.StatusDone)
	case a.quotaStalled:
		e.db.SetStatus(a.id, db.StatusQuota, "daily transfer quota exceeded")
	default:
		msg := a.lastError
		if msg == "" && len(a.stderrTail) > 0 {
			msg = a.stderrTail[len(a.stderrTail)-1]
		}
		if msg == "" && exitErr != nil {
			msg = exitErr.Error()
		}
		if msg == "" {
			msg = fmt.Sprintf("download ended with status %d", a.endStatus)
		}
		e.db.SetStatus(a.id, db.StatusError, msg)
	}

	e.act = nil
	e.Kick()
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

// Stop cancels the active download or un-queues a waiting one.
func (e *Engine) Stop(id int64) {
	e.mu.Lock()
	if e.act != nil && e.act.id == id {
		e.act.stopping = true
		e.act.proc.Stop()
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()

	if dl, err := e.db.Download(id); err == nil && dl.Status == db.StatusQueued {
		e.db.SetStatus(id, db.StatusStopped, "")
		e.notify()
	}
}

// Resume re-queues a stopped/errored/quota download.
func (e *Engine) Resume(id int64) {
	dl, err := e.db.Download(id)
	if err != nil || dl == nil {
		return
	}
	switch dl.Status {
	case db.StatusStopped, db.StatusError, db.StatusQuota:
		e.db.SetStatus(id, db.StatusQueued, "")
		e.Kick()
		e.notify()
	}
}

// StopFile drops one file from its download's wanted set. If the file
// is part of the running process's selection, that process is restarted
// without it. Partial files remain resumable.
func (e *Engine) StopFile(fileID int64) {
	f, err := e.db.File(fileID)
	if err != nil || !f.Wanted {
		return
	}
	if f.Status == db.FileDone || f.Status == db.FileSkipped {
		return // already on disk; nothing to stop
	}
	e.db.SetFileWanted(fileID, false)
	e.db.RecalcTotalBytes(f.DownloadID)

	e.mu.Lock()
	if e.act != nil && e.act.id == f.DownloadID && !e.act.stopping && f.Status == db.FilePending {
		e.act.requeue = true
		e.act.proc.Stop()
	}
	e.mu.Unlock()
	e.notify()
}

// ResumeFile re-adds one file to the wanted set and makes sure it gets
// fetched: a finished/stopped/failed download is re-queued, an active
// process is restarted when its selection has to change.
func (e *Engine) ResumeFile(fileID int64) {
	f, err := e.db.File(fileID)
	if err != nil {
		return
	}
	if f.Status == db.FileDone || f.Status == db.FileSkipped {
		return // nothing to fetch
	}
	changed := !f.Wanted
	if changed {
		e.db.SetFileWanted(fileID, true)
		e.db.RecalcTotalBytes(f.DownloadID)
	}

	e.mu.Lock()
	act := e.act != nil && e.act.id == f.DownloadID
	if act && !e.act.stopping && (changed || f.Status == db.FileError) {
		e.act.requeue = true
		e.act.proc.Stop()
	}
	e.mu.Unlock()

	if !act {
		if dl, err := e.db.Download(f.DownloadID); err == nil {
			switch dl.Status {
			case db.StatusStopped, db.StatusError, db.StatusQuota, db.StatusDone:
				e.db.SetStatus(dl.ID, db.StatusQueued, "")
			}
		}
		e.Kick()
	}
	e.notify()
}

// ActiveID returns the running download's id, or 0.
func (e *Engine) ActiveID() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.act == nil {
		return 0
	}
	return e.act.id
}

func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.act == nil {
		return Snapshot{}
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
		QuotaStalled: a.quotaStalled,
		StderrTail:   append([]string(nil), a.stderrTail...),
	}
}

// rateMeter is a sliding-window byte-rate estimator.
type rateMeter struct {
	samples []rateSample
}

type rateSample struct {
	t time.Time
	n int64
}

func (r *rateMeter) add(n int64) {
	now := time.Now()
	r.samples = append(r.samples, rateSample{now, n})
	cutoff := now.Add(-5 * time.Second)
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
