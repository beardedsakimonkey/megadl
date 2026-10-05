package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"megadl/internal/db"
	"megadl/internal/mega"
)

type deleteDriver struct {
	proc *deleteProc
}

func (d deleteDriver) List(context.Context, string) ([]mega.Node, error) { return nil, nil }
func (d deleteDriver) Start(context.Context, mega.DownloadArgs) (mega.Proc, error) {
	return d.proc, nil
}

type deleteProc struct {
	events  chan mega.Event
	stopped chan struct{}
}

func (p *deleteProc) Events() <-chan mega.Event { return p.events }
func (p *deleteProc) Stop() {
	select {
	case <-p.stopped:
	default:
		close(p.stopped)
	}
}
func (p *deleteProc) RetryNow() {}

func TestDeleteWaitsForExitAndKeepsLock(t *testing.T) {
	database := testDB(t)
	id := insertDownload(t, database)
	p := &deleteProc{events: make(chan mega.Event), stopped: make(chan struct{})}
	e := New(deleteDriver{p}, database)
	lock := &fakeLock{}
	e.SetLock(lock)
	e.maybeStart(context.Background())
	result := make(chan error, 1)
	removed := make(chan struct{})
	go func() {
		result <- e.DeleteDownload(id, func() error {
			if !lock.heldHere() {
				return errors.New("library lock released before deletion")
			}
			close(removed)
			return nil
		})
	}()
	select {
	case <-p.stopped:
	case <-time.After(time.Second):
		t.Fatal("transfer was not stopped")
	}
	select {
	case <-removed:
		t.Fatal("files removed before transfer exit")
	default:
	}
	p.events <- mega.ProgressEvent{Done: 25}
	p.events <- mega.ExitEvent{}
	close(p.events)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("deletion did not finish")
	}
	rows, err := database.Downloads()
	if err != nil || len(rows) != 0 {
		t.Fatalf("downloads = %v, err = %v", rows, err)
	}
	bytes, err := database.BytesSince(time.Now().Add(-time.Hour))
	if err != nil || bytes != 25 {
		t.Fatalf("quota bytes = %d, err = %v", bytes, err)
	}
	if lock.heldHere() {
		t.Fatal("idle engine retained library lock")
	}
	if e.Paused() {
		t.Fatal("deletion paused the queue")
	}
}

func TestDeleteFailureKeepsRecordsAndDequeues(t *testing.T) {
	database := testDB(t)
	id := insertDownload(t, database)
	e := New(nil, database)
	want := errors.New("disk removal failed")
	if err := e.DeleteDownload(id, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if _, err := database.Download(id); err != nil {
		t.Fatal(err)
	}
	waitQueued(t, database, id, false)
}

func TestDeleteRefusesAnotherInstance(t *testing.T) {
	database := testDB(t)
	id := insertDownload(t, database)
	e := New(nil, database)
	e.SetLock(&fakeLock{taken: true})
	err := e.DeleteDownload(id, func() error { t.Fatal("removed files used by another instance"); return nil })
	if err == nil {
		t.Fatal("expected a lock error")
	}
	waitQueued(t, database, id, true)
}

func TestDeleteContinuesWithNextDownload(t *testing.T) {
	database := testDB(t)
	first := insertDownload(t, database)
	driver := newFakeDriver(driverRun{waitForStop: true}, driverRun{waitForStop: true})
	e := New(driver, database)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { defer close(runDone); e.Run(ctx) }()
	defer func() {
		cancel()
		<-runDone
		waitActive(t, e, 0)
	}()
	e.Kick()
	waitActive(t, e, first)
	next, err := database.InsertDownload(&db.Download{
		URL: "next", Handle: "next", LinkType: "file", Name: "next", DestPath: "/next",
	}, []db.File{{NodeHandle: "next", LocalPath: "/next", Queued: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteDownload(first, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	waitActive(t, e, next)
	if got := len(driver.startedArgs()); got != 2 {
		t.Fatalf("starts = %d, want 2", got)
	}
}
