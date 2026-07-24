package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"megadl/internal/db"
	"megadl/internal/mega"
)

type driverRun struct {
	events      []mega.Event
	waitForStop bool
}

type fakeDriver struct {
	mu      sync.Mutex
	runs    []driverRun
	started []mega.DownloadArgs
}

var _ mega.Driver = (*fakeDriver)(nil)

func newFakeDriver(runs ...driverRun) *fakeDriver {
	return &fakeDriver{runs: runs}
}

func (d *fakeDriver) List(context.Context, string) ([]mega.Node, error) {
	return nil, fmt.Errorf("list is not implemented by the engine test driver")
}

func (d *fakeDriver) Start(ctx context.Context, args mega.DownloadArgs) (mega.Proc, error) {
	d.mu.Lock()
	index := len(d.started)
	args.SelectHandles = append([]string(nil), args.SelectHandles...)
	d.started = append(d.started, args)
	if index >= len(d.runs) {
		d.mu.Unlock()
		return nil, fmt.Errorf("unexpected download start %d", index+1)
	}
	run := d.runs[index]
	d.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	proc := &fakeProc{events: make(chan mega.Event, 64), cancel: cancel}
	go func() {
		defer close(proc.events)
		for _, ev := range run.events {
			select {
			case proc.events <- ev:
			case <-ctx.Done():
				proc.events <- mega.ExitEvent{Err: ctx.Err()}
				return
			}
		}
		if run.waitForStop {
			<-ctx.Done()
			proc.events <- mega.ExitEvent{Err: ctx.Err()}
			return
		}
		proc.events <- mega.ExitEvent{}
	}()
	return proc, nil
}

func (d *fakeDriver) startedArgs() []mega.DownloadArgs {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]mega.DownloadArgs, len(d.started))
	for i, args := range d.started {
		out[i] = args
		out[i].SelectHandles = append([]string(nil), args.SelectHandles...)
	}
	return out
}

type fakeProc struct {
	events chan mega.Event
	cancel context.CancelFunc
}

func (p *fakeProc) Events() <-chan mega.Event { return p.events }
func (p *fakeProc) Stop()                     { p.cancel() }

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func insertDownload(t *testing.T, d *db.DB) int64 {
	t.Helper()
	id, err := d.InsertDownload(&db.Download{
		URL:    "https://mega.nz/folder/AAAAAAAA#kkkkkkkkkkkkkkkkkkkkkk",
		Handle: "AAAAAAAA", LinkType: "folder", Name: "Show",
		DestPath: "/fake", Selection: "h1,h2", TotalBytes: 150,
	}, []db.File{
		{NodeHandle: "h1", RemotePath: "/Root/a.mkv", LocalPath: "/fake/a.mkv", Size: 100, Wanted: true},
		{NodeHandle: "h2", RemotePath: "/Root/b.mkv", LocalPath: "/fake/b.mkv", Size: 50, Wanted: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func waitStatus(t *testing.T, d *db.DB, id int64, want string) *db.Download {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		dl, err := d.Download(id)
		if err != nil {
			t.Fatal(err)
		}
		if dl.Status == want {
			return dl
		}
		time.Sleep(20 * time.Millisecond)
	}
	dl, _ := d.Download(id)
	t.Fatalf("status = %s, want %s (dl=%+v)", dl.Status, want, dl)
	return nil
}

func TestEngineCompletesAndAccounts(t *testing.T) {
	// a.mkv: 60 counted live + 40 credited at the -2 marker = 100.
	// b.mkv: skipped (already on disk), no bytes counted.
	drv := newFakeDriver(driverRun{events: []mega.Event{
		mega.FileStartEvent{Path: "/fake/a.mkv", Remote: "/Root/a.mkv", Size: 100},
		mega.ProgressEvent{Done: -1, Total: 100},
		mega.ProgressEvent{Done: 60, Total: 100},
		mega.ProgressEvent{Done: -2},
		mega.FileDoneEvent{Path: "/fake/a.mkv"},
		mega.FileSkipEvent{Path: "/fake/b.mkv"},
		mega.EndEvent{Status: 0},
	}})

	d := testDB(t)
	id := insertDownload(t, d)

	eng := New(drv, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick()

	dl := waitStatus(t, d, id, db.StatusDone)
	if dl.DoneBytes != 100 {
		t.Errorf("done_bytes = %d, want 100", dl.DoneBytes)
	}
	if n, _ := d.BytesSince(time.Now().Add(-time.Minute)); n != 100 {
		t.Errorf("transfer_log = %d, want 100", n)
	}
	files, _ := d.Files(id)
	if files[0].Status != db.FileDone || files[1].Status != db.FileSkipped {
		t.Errorf("file statuses = %+v", files)
	}
}

func TestEngineResumeAccountingIgnoresSeededBytes(t *testing.T) {
	// Resumed file: 70 of 100 already local, session fetches remaining 30.
	// Counted: 10 (0->10), 0 (regression 10->4), 21 (4->25),
	// 5 (-2 credit) = 36.
	drv := newFakeDriver(driverRun{events: []mega.Event{
		mega.FileStartEvent{Path: "/fake/a.mkv", Remote: "/Root/a.mkv", Size: 100},
		mega.ProgressEvent{Done: -1, Total: 30},
		mega.ProgressEvent{Done: 10, Total: 30},
		mega.ProgressEvent{Done: 4, Total: 30},
		mega.ProgressEvent{Done: 25, Total: 30},
		mega.ProgressEvent{Done: -2},
		mega.FileDoneEvent{Path: "/fake/a.mkv"},
		mega.FileSkipEvent{Path: "/fake/b.mkv"},
		mega.EndEvent{Status: 0},
	}})

	d := testDB(t)
	id := insertDownload(t, d)

	eng := New(drv, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick()

	dl := waitStatus(t, d, id, db.StatusDone)
	if dl.DoneBytes != 36 {
		t.Errorf("done_bytes = %d, want 36 (seeded bytes must not count)", dl.DoneBytes)
	}
}

func TestEngineStopLeavesResumableState(t *testing.T) {
	drv := newFakeDriver(driverRun{
		events: []mega.Event{
			mega.FileStartEvent{Path: "/fake/a.mkv", Remote: "/Root/a.mkv", Size: 100},
		},
		waitForStop: true,
	})

	d := testDB(t)
	id := insertDownload(t, d)

	eng := New(drv, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick()

	waitStatus(t, d, id, db.StatusRunning)
	for eng.ActiveID() != id {
		time.Sleep(10 * time.Millisecond)
	}
	eng.Stop(id)
	waitStatus(t, d, id, db.StatusStopped)

	// Resume re-queues it.
	eng.Resume(id)
	dl, _ := d.Download(id)
	if dl.Status != db.StatusQueued && dl.Status != db.StatusRunning {
		t.Errorf("after resume: %s", dl.Status)
	}
}

func TestEngineDetectsQuotaStall(t *testing.T) {
	line := "Server returned 509 (over quota)"
	drv := newFakeDriver(driverRun{
		events: []mega.Event{
			mega.FileStartEvent{Path: "/fake/a.mkv", Remote: "/Root/a.mkv", Size: 100},
			mega.QuotaEvent{Line: line},
			mega.StderrEvent{Line: line},
		},
		waitForStop: true,
	})

	d := testDB(t)
	id := insertDownload(t, d)

	eng := New(drv, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick()

	waitStatus(t, d, id, db.StatusQuota)
	if !eng.Snapshot().QuotaStalled {
		t.Error("snapshot should report quota stall")
	}
	eng.Stop(id)
	waitStatus(t, d, id, db.StatusStopped)
}

func TestEngineStopFileRestartsWithReducedSelection(t *testing.T) {
	// First run starts a.mkv and waits; stopping that file must restart
	// the native driver with only h2 selected. Second run finishes.
	drv := newFakeDriver(
		driverRun{
			events: []mega.Event{
				mega.FileStartEvent{Path: "/fake/a.mkv", Remote: "/Root/a.mkv", Size: 100},
			},
			waitForStop: true,
		},
		driverRun{events: []mega.Event{
			mega.FileSkipEvent{Path: "/fake/b.mkv"},
			mega.EndEvent{Status: 0},
		}},
	)

	d := testDB(t)
	id := insertDownload(t, d)

	eng := New(drv, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick()

	deadline := time.Now().Add(10 * time.Second)
	for eng.Snapshot().CurrentFile != "a.mkv" {
		if time.Now().After(deadline) {
			t.Fatal("first run never reached file_start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := eng.Snapshot().CurrentPath; got != "/fake/a.mkv" {
		t.Fatalf("current path = %q, want /fake/a.mkv", got)
	}

	files, _ := d.Files(id)
	if files[0].NodeHandle != "h1" {
		t.Fatalf("unexpected file order: %+v", files)
	}
	eng.StopFile(files[0].ID)

	dl := waitStatus(t, d, id, db.StatusDone)
	if dl.TotalBytes != 50 {
		t.Errorf("total_bytes = %d, want 50 (rebased on wanted files)", dl.TotalBytes)
	}

	started := drv.startedArgs()
	if len(started) != 2 {
		t.Fatalf("driver started %d times, want 2: %+v", len(started), started)
	}
	if want := []string{"h1", "h2"}; !reflect.DeepEqual(started[0].SelectHandles, want) {
		t.Errorf("first run selection = %v, want %v", started[0].SelectHandles, want)
	}
	if want := []string{"h2"}; !reflect.DeepEqual(started[1].SelectHandles, want) {
		t.Errorf("second run selection = %v, want %v", started[1].SelectHandles, want)
	}

	files, _ = d.Files(id)
	if files[0].Wanted || files[0].Status != db.FilePending {
		t.Errorf("stopped file = %+v", files[0])
	}
}

func TestEngineResumeFileRequeuesStoppedDownload(t *testing.T) {
	d := testDB(t)
	id := insertDownload(t, d)
	files, _ := d.Files(id)

	d.SetFileWanted(files[1].ID, false)
	d.RecalcTotalBytes(id)
	d.SetStatus(id, db.StatusDone, "")

	eng := New(newFakeDriver(), d) // no Run loop needed
	eng.ResumeFile(files[1].ID)

	dl, _ := d.Download(id)
	if dl.Status != db.StatusQueued {
		t.Errorf("status = %s, want queued", dl.Status)
	}
	if dl.TotalBytes != 150 {
		t.Errorf("total_bytes = %d, want 150", dl.TotalBytes)
	}
	if f, _ := d.File(files[1].ID); !f.Wanted {
		t.Error("file should be wanted again")
	}
}

func TestEngineResumeFetchesFilesMergedAfterEnqueue(t *testing.T) {
	// The stored selection reflects enqueue time (here a deep link to
	// a.mkv only). A file merged in by a listing refresh and then
	// resumed must be selected too, not silently skipped.
	drv := newFakeDriver(driverRun{events: []mega.Event{
		mega.FileSkipEvent{Path: "/fake/a.mkv"},
		mega.FileStartEvent{Path: "/fake/b.mkv", Remote: "/Root/b.mkv", Size: 50},
		mega.ProgressEvent{Done: -1, Total: 50},
		mega.ProgressEvent{Done: -2},
		mega.FileDoneEvent{Path: "/fake/b.mkv"},
		mega.EndEvent{Status: 0},
	}})

	d := testDB(t)
	id, err := d.InsertDownload(&db.Download{
		URL:    "https://mega.nz/folder/AAAAAAAA#kkkkkkkkkkkkkkkkkkkkkk",
		Handle: "AAAAAAAA", LinkType: "folder", Name: "Show",
		DestPath: "/fake", Selection: "h1", TotalBytes: 100,
	}, []db.File{
		{NodeHandle: "h1", RemotePath: "/Root/a.mkv", LocalPath: "/fake/a.mkv", Size: 100, Wanted: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.MergeFiles(id, []db.File{
		{NodeHandle: "h2", RemotePath: "/Root/b.mkv", LocalPath: "/fake/b.mkv", Size: 50},
	}); err != nil {
		t.Fatal(err)
	}
	d.SetStatus(id, db.StatusDone, "")

	eng := New(drv, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	files, _ := d.Files(id)
	if len(files) != 2 || files[1].NodeHandle != "h2" {
		t.Fatalf("unexpected files: %+v", files)
	}
	eng.ResumeFile(files[1].ID)

	deadline := time.Now().Add(10 * time.Second)
	for {
		if files, _ = d.Files(id); files[1].Status == db.FileDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("merged file never finished: %+v", files[1])
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitStatus(t, d, id, db.StatusDone)

	started := drv.startedArgs()
	if len(started) != 1 {
		t.Fatalf("driver started %d times, want 1: %+v", len(started), started)
	}
	if want := []string{"h1", "h2"}; !reflect.DeepEqual(started[0].SelectHandles, want) {
		t.Errorf("selection = %v, want %v", started[0].SelectHandles, want)
	}
}

func TestEngineAllFilesStoppedMarksStopped(t *testing.T) {
	d := testDB(t)
	id := insertDownload(t, d)
	files, _ := d.Files(id)

	drv := newFakeDriver() // Start must never be called.
	eng := New(drv, d)
	eng.StopFile(files[0].ID)
	eng.StopFile(files[1].ID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick()

	dl := waitStatus(t, d, id, db.StatusStopped)
	if dl.TotalBytes != 0 {
		t.Errorf("total_bytes = %d, want 0", dl.TotalBytes)
	}
	if started := drv.startedArgs(); len(started) != 0 {
		t.Errorf("driver unexpectedly started: %+v", started)
	}
}

func TestEngineErrorStatus(t *testing.T) {
	drv := newFakeDriver(driverRun{events: []mega.Event{
		mega.FileErrorEvent{Path: "/fake/a.mkv", Message: "Download failed for /Root/a.mkv: boom"},
		mega.EndEvent{Status: 1},
	}})

	d := testDB(t)
	id := insertDownload(t, d)

	eng := New(drv, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick()

	dl := waitStatus(t, d, id, db.StatusError)
	if dl.Error == "" {
		t.Error("error message should be recorded")
	}
	files, _ := d.Files(id)
	if files[0].Status != db.FileError {
		t.Errorf("file status = %s", files[0].Status)
	}
}
