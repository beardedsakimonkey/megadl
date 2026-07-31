package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"megadl/internal/db"
	"megadl/internal/mega"
)

type driverRun struct {
	events      []mega.Event
	waitForStop bool
	waitAfter   int
	release     <-chan struct{}
}

type fakeDriver struct {
	mu      sync.Mutex
	runs    []driverRun
	started []mega.DownloadArgs
	stops   int

	// beforeStart, when set, runs as the numbered process is about to start,
	// which is where a test can hold the engine between two runs.
	beforeStart func(index int)
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
	hook := d.beforeStart
	d.mu.Unlock()

	if hook != nil {
		hook(index)
	}

	ctx, cancel := context.WithCancel(ctx)
	proc := &fakeProc{
		events: make(chan mega.Event, 64),
		cancel: cancel,
		onStop: func() {
			d.mu.Lock()
			d.stops++
			d.mu.Unlock()
		},
	}
	go func() {
		defer close(proc.events)
		for i, ev := range run.events {
			select {
			case proc.events <- ev:
			case <-ctx.Done():
				proc.events <- mega.ExitEvent{Err: ctx.Err()}
				return
			}
			if run.release != nil && i+1 == run.waitAfter {
				select {
				case <-run.release:
				case <-ctx.Done():
					proc.events <- mega.ExitEvent{Err: ctx.Err()}
					return
				}
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

func (d *fakeDriver) stopCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stops
}

type fakeProc struct {
	events chan mega.Event
	cancel context.CancelFunc
	onStop func()
}

func (p *fakeProc) Events() <-chan mega.Event { return p.events }
func (p *fakeProc) Stop() {
	p.onStop()
	p.cancel()
}

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
		{NodeHandle: "h1", RemotePath: "/Root/a.mkv", LocalPath: "/fake/a.mkv", Size: 100, Queued: true},
		{NodeHandle: "h2", RemotePath: "/Root/b.mkv", LocalPath: "/fake/b.mkv", Size: 50, Queued: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// waitQueued blocks until id is in the queue, or out of it, as asked.
func waitQueued(t *testing.T, d *db.DB, id int64, want bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		queue, err := d.Queue()
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(queue, id) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	queue, _ := d.Queue()
	t.Fatalf("queue = %v, want %d in it = %v", queue, id, want)
}

// waitActive blocks until the engine is fetching id.
func waitActive(t *testing.T, eng *Engine, id int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if eng.ActiveID() == id {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("engine never started download %d", id)
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

func TestEngineDequeueLeavesResumableState(t *testing.T) {
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

	waitActive(t, eng, id)
	eng.Dequeue(id)
	waitQueued(t, d, id, false)

	// No outcome is recorded for a download the user took out of the queue:
	// its files are still pending, so putting it back resumes them.
	dl, _ := d.Download(id)
	if dl.Status != db.StatusPending {
		t.Errorf("status = %q, want nothing terminal recorded", dl.Status)
	}
	files, _ := d.Files(id)
	for _, f := range files {
		if f.Queued || f.Status != db.FilePending {
			t.Errorf("dequeued file = %+v, want unqueued and still pending", f)
		}
	}

	eng.Enqueue(id)
	waitQueued(t, d, id, true)
}

// Pausing holds the queue where it is: the download in flight is cancelled but
// keeps its place at the head, and nothing else starts in its stead.
func TestEnginePauseHoldsTheQueue(t *testing.T) {
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

	waitActive(t, eng, id)
	eng.SetPaused(true)

	deadline := time.Now().Add(10 * time.Second)
	for eng.ActiveID() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("pause never cancelled the download in flight")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Still at the head, so a resume picks it up again rather than moving on.
	if next, _ := d.NextQueued(); next == nil || next.ID != id {
		t.Fatalf("paused download left the queue: %+v", next)
	}
	if paused, _, _ := d.Paused(); !paused {
		t.Error("pause should be recorded in the database")
	}
	if !eng.Snapshot().Paused {
		t.Error("snapshot should report the pause")
	}

	// A pause blocks starts even when the queue is kicked again.
	eng.Kick()
	time.Sleep(50 * time.Millisecond)
	if eng.ActiveID() != 0 {
		t.Error("kick started a download while paused")
	}
}

// A pause survives the process, so the engine comes back up holding the queue
// instead of pulling on a link that was deliberately stopped.
func TestEnginePauseSurvivesRestart(t *testing.T) {
	d := testDB(t)
	id := insertDownload(t, d)
	if err := d.SetPaused(true, ""); err != nil {
		t.Fatal(err)
	}

	drv := newFakeDriver() // Start must never be called
	eng := New(drv, d)
	if !eng.Paused() {
		t.Fatal("engine did not come up paused")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick()
	time.Sleep(50 * time.Millisecond)

	if started := drv.startedArgs(); len(started) != 0 {
		t.Errorf("driver started while paused: %+v", started)
	}
	if next, _ := d.NextQueued(); next == nil || next.ID != id {
		t.Fatalf("head of the queue = %+v, want %d", next, id)
	}
}

// Quota is not a per-download outcome: it holds the whole queue, with a reason
// that outlives the process so the UI can still explain the pause.
func TestEngineQuotaPausesTheQueue(t *testing.T) {
	line := "Server returned 509 (over quota)"
	drv := newFakeDriver(driverRun{
		events: []mega.Event{
			mega.FileStartEvent{Path: "/fake/a.mkv", Remote: "/Root/a.mkv", Size: 100},
			mega.QuotaEvent{Line: line},
			mega.StderrEvent{Line: line},
		},
	})

	d := testDB(t)
	id := insertDownload(t, d)

	eng := New(drv, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick()

	deadline := time.Now().Add(10 * time.Second)
	for !eng.Paused() {
		if time.Now().After(deadline) {
			t.Fatal("quota never paused the queue")
		}
		time.Sleep(20 * time.Millisecond)
	}
	paused, reason, _ := d.Paused()
	if !paused || reason == "" {
		t.Fatalf("database pause = %v, %q, want a recorded reason", paused, reason)
	}
	if next, _ := d.NextQueued(); next == nil || next.ID != id {
		t.Fatalf("quota-paused download left the queue: %+v", next)
	}
	if dl, _ := d.Download(id); dl.Status != db.StatusPending {
		t.Errorf("status = %q, want quota kept out of the download's status", dl.Status)
	}
	if started := drv.startedArgs(); len(started) != 1 {
		t.Errorf("driver started %d times, want 1", len(started))
	}
}

// A 509 the driver's own retry gets past is not a stall any more: the banner
// has to go out once bytes land again, and the run must finish normally rather
// than parking the queue on a throttle that already lifted.
func TestEngineQuotaClearsWhenBytesResume(t *testing.T) {
	drv := newFakeDriver(driverRun{
		events: []mega.Event{
			mega.FileStartEvent{Path: "/fake/a.mkv", Remote: "/Root/a.mkv", Size: 100},
			mega.ProgressEvent{Done: -1, Total: 100},
			mega.QuotaEvent{Line: "Server returned 509 (over quota)"},
			mega.ProgressEvent{Done: 40, Total: 100},
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
	waitActive(t, eng, id)

	deadline := time.Now().Add(10 * time.Second)
	for eng.Snapshot().QuotaStalled || eng.Snapshot().FileDone == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("snapshot still stalled after bytes resumed: %+v", eng.Snapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if eng.Paused() {
		t.Fatal("a stall that cleared must not pause the queue")
	}
}

func TestEngineDequeueFileRestartsWithReducedSelection(t *testing.T) {
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
	eng.DequeueFile(files[0].ID)

	dl := waitStatus(t, d, id, db.StatusDone)
	if dl.TotalBytes != 50 {
		t.Errorf("total_bytes = %d, want 50 (rebased on queued files)", dl.TotalBytes)
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
	if files[0].Queued || files[0].Status != db.FilePending {
		t.Errorf("dequeued file = %+v", files[0])
	}
}

// Queueing an earlier file while another file is active must not preempt the
// transfer in progress. The driver's selection is fixed, so the new file runs
// in a second pass after the first one completes.
func TestEngineQueueFileDoesNotPreemptActiveFile(t *testing.T) {
	releaseFirst := make(chan struct{})
	drv := newFakeDriver(
		driverRun{
			events: []mega.Event{
				mega.FileStartEvent{Path: "/fake/b.mkv", Remote: "/Root/b.mkv", Size: 50},
				mega.FileDoneEvent{Path: "/fake/b.mkv"},
				mega.EndEvent{Status: 0},
			},
			waitAfter: 1,
			release:   releaseFirst,
		},
		driverRun{events: []mega.Event{
			mega.FileStartEvent{Path: "/fake/a.mkv", Remote: "/Root/a.mkv", Size: 100},
			mega.FileDoneEvent{Path: "/fake/a.mkv"},
			mega.EndEvent{Status: 0},
		}},
	)

	d := testDB(t)
	id := insertDownload(t, d)
	files, _ := d.Files(id)
	d.SetFileQueued(files[0].ID, false) // a.mkv sits above the active b.mkv
	d.RecalcTotalBytes(id)

	eng := New(drv, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick()

	deadline := time.Now().Add(10 * time.Second)
	for eng.Snapshot().CurrentFile != "b.mkv" {
		if time.Now().After(deadline) {
			t.Fatal("first run never reached file_start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	eng.QueueFile(files[0].ID)
	if got := drv.stopCount(); got != 0 {
		t.Fatalf("queueing a.mkv stopped the active process %d time(s)", got)
	}
	if snap := eng.Snapshot(); snap.ActiveID != id || snap.CurrentFile != "b.mkv" || snap.CurrentPath != "/fake/b.mkv" {
		t.Errorf("snapshot after queueing a.mkv = %+v, want b.mkv still active under %d", snap, id)
	}

	close(releaseFirst)
	waitStatus(t, d, id, db.StatusDone)

	started := drv.startedArgs()
	if len(started) != 2 {
		t.Fatalf("driver started %d times, want 2: %+v", len(started), started)
	}
	if want := []string{"h2"}; !reflect.DeepEqual(started[0].SelectHandles, want) {
		t.Errorf("first run selection = %v, want %v", started[0].SelectHandles, want)
	}
	if want := []string{"h1"}; !reflect.DeepEqual(started[1].SelectHandles, want) {
		t.Errorf("second run selection = %v, want %v", started[1].SelectHandles, want)
	}
}

// Queueing another file while the queue is paused must not make the stopped
// download appear active again.
func TestEngineQueueFileWhilePausedKeepsActiveClear(t *testing.T) {
	drv := newFakeDriver(driverRun{
		events: []mega.Event{
			mega.FileStartEvent{Path: "/fake/a.mkv", Remote: "/Root/a.mkv", Size: 100},
		},
		waitForStop: true,
	})

	d := testDB(t)
	id := insertDownload(t, d)
	files, _ := d.Files(id)
	d.SetFileQueued(files[1].ID, false)
	d.RecalcTotalBytes(id)

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

	eng.SetPaused(true)
	eng.QueueFile(files[1].ID)

	deadline = time.Now().Add(10 * time.Second)
	for eng.ActiveID() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("paused queue still reports %d active", eng.ActiveID())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snap := eng.Snapshot(); snap.CurrentFile != "" {
		t.Errorf("paused snapshot = %+v, want no current file", snap)
	}
}

// Queueing one file from a download that had already finished puts the
// download back in the queue on its own — there is no status to juggle.
func TestEngineQueueFilePutsFinishedDownloadBack(t *testing.T) {
	d := testDB(t)
	id := insertDownload(t, d)
	files, _ := d.Files(id)

	// b.mkv was left out of the queue and a.mkv landed, so the run finished.
	d.SetFileQueued(files[1].ID, false)
	d.SetFileStatusByHandle(id, "h1", db.FileDone)
	d.RecalcTotalBytes(id)
	d.MarkCompleted(id, db.StatusDone)
	if next, _ := d.NextQueued(); next != nil {
		t.Fatalf("finished download still queued: %+v", next)
	}

	eng := New(newFakeDriver(), d) // no Run loop needed
	eng.QueueFile(files[1].ID)

	if next, _ := d.NextQueued(); next == nil || next.ID != id {
		t.Fatalf("queueing a file did not put its download back: %+v", next)
	}
	if dl, _ := d.Download(id); dl.TotalBytes != 150 {
		t.Errorf("total_bytes = %d, want 150", dl.TotalBytes)
	}
	if f, _ := d.File(files[1].ID); !f.Queued {
		t.Error("file should be queued again")
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
		{NodeHandle: "h1", RemotePath: "/Root/a.mkv", LocalPath: "/fake/a.mkv", Size: 100, Queued: true},
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
	eng.QueueFile(files[1].ID)

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

// The file pane toggles a folder as one unit, so it hands the engine the whole
// set of files at once. Every eligible file has to move, and the download's
// total has to end up rebased on all of them rather than on the first.
func TestEngineQueuesAndDequeuesFilesInSets(t *testing.T) {
	d := testDB(t)
	id := insertDownload(t, d)
	files, _ := d.Files(id)
	ids := []int64{files[0].ID, files[1].ID}
	// a.mkv is already on disk, so nothing the set does may touch it
	d.SetFileStatusByHandle(id, "h1", db.FileDone)

	eng := New(newFakeDriver(), d) // no Run loop needed
	eng.DequeueFiles(ids)

	if f, _ := d.File(files[0].ID); !f.Queued || f.Status != db.FileDone {
		t.Errorf("downloaded file = %+v, want it left queued and done", f)
	}
	if f, _ := d.File(files[1].ID); f.Queued {
		t.Errorf("file = %+v, want it out of the queue", f)
	}
	if next, _ := d.NextQueued(); next != nil {
		t.Errorf("download still queued with nothing pending in it: %+v", next)
	}

	eng.QueueFiles(ids)

	if f, _ := d.File(files[1].ID); !f.Queued {
		t.Errorf("file = %+v, want it back in the queue", f)
	}
	if dl, _ := d.Download(id); dl.TotalBytes != 150 {
		t.Errorf("total_bytes = %d, want 150 — the whole set", dl.TotalBytes)
	}
	if next, _ := d.NextQueued(); next == nil || next.ID != id {
		t.Errorf("queueing a set did not put its download back: %+v", next)
	}
}

// Taking every file out leaves the download out of the queue. Starting it with
// nothing selected would tell the driver to fetch the whole folder, so the
// engine must not start it at all.
func TestEngineDequeuingEveryFileLeavesTheQueue(t *testing.T) {
	d := testDB(t)
	id := insertDownload(t, d)
	files, _ := d.Files(id)

	drv := newFakeDriver() // Start must never be called.
	eng := New(drv, d)
	eng.DequeueFile(files[0].ID)
	eng.DequeueFile(files[1].ID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Kick()
	time.Sleep(50 * time.Millisecond)

	waitQueued(t, d, id, false)
	if dl, _ := d.Download(id); dl.TotalBytes != 0 {
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

	// b.mkv never got its turn, but a failed run takes the whole download out
	// of the queue: otherwise the file still pending would put it straight back
	// and a link that always fails would be retried in a tight loop.
	waitQueued(t, d, id, false)
	time.Sleep(50 * time.Millisecond)
	if started := drv.startedArgs(); len(started) != 1 {
		t.Errorf("driver started %d times, want 1 — the failed run was retried", len(started))
	}
}

// The rate column and the finish estimates read the same bytes over different
// windows, so a lull that drops the reported speed to nothing still leaves the
// estimates something to project from.
func TestRateMetersDifferInHowFarBackTheyLook(t *testing.T) {
	stale := rateSample{t: time.Now().Add(-20 * time.Second), n: 1 << 20}
	now, avg := rateMeter{window: rateWindow}, rateMeter{window: avgWindow}
	now.samples = append(now.samples, stale)
	avg.samples = append(avg.samples, stale)

	now.add(1 << 20)
	avg.add(1 << 20)

	if len(now.samples) != 1 {
		t.Errorf("rate meter kept %d samples, want the 20s-old one dropped", len(now.samples))
	}
	if len(avg.samples) != 2 {
		t.Errorf("avg meter kept %d samples, want the 20s-old one held", len(avg.samples))
	}
	// One sample is a single instant, which is no rate at all; the pair spans
	// 20 seconds of transfer.
	if got := now.rate(); got != 0 {
		t.Errorf("rate over one sample = %v, want 0", got)
	}
	if got := avg.rate(); got < 90<<10 || got > 110<<10 {
		t.Errorf("avg rate = %v, want ~100 KiB/s (2 MiB over 20s)", got)
	}
}

// Estimates are projected from AvgRate, so it has to be reported alongside the
// speed the rate column shows rather than only after the transfer ends.
func TestSnapshotReportsBothRatesWhileBytesLand(t *testing.T) {
	release := make(chan struct{})
	drv := newFakeDriver(driverRun{
		events: []mega.Event{
			mega.FileStartEvent{Path: "/fake/a.mkv", Remote: "/Root/a.mkv", Size: 100},
			mega.ProgressEvent{Done: -1, Total: 100},
			mega.ProgressEvent{Done: 20, Total: 100},
			mega.ProgressEvent{Done: 60, Total: 100},
			mega.FileDoneEvent{Path: "/fake/a.mkv"},
			mega.EndEvent{Status: 0},
		},
		waitAfter: 4,
		release:   release,
	})

	d := testDB(t)
	id := insertDownload(t, d)

	eng := New(drv, d)
	go eng.Run(t.Context())
	eng.Kick()
	waitActive(t, eng, id)

	deadline := time.Now().Add(10 * time.Second)
	for {
		snap := eng.Snapshot()
		if snap.Rate > 0 && snap.AvgRate > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot = %+v, want both rates reported", snap)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	waitStatus(t, d, id, db.StatusDone)
}

// Pausing cancels the transfer the estimates were being measured from, so the
// speed it had reached has to outlive it: a held queue is holding a file
// part-way through, and how long that file has left is the thing the strip is
// holding it in front of.
func TestPauseKeepsTheRateItsEstimatesComeFrom(t *testing.T) {
	drv := newFakeDriver(driverRun{
		events: []mega.Event{
			mega.FileStartEvent{Path: "/fake/a.mkv", Remote: "/Root/a.mkv", Size: 100},
			mega.ProgressEvent{Done: -1, Total: 100},
			mega.ProgressEvent{Done: 20, Total: 100},
			mega.ProgressEvent{Done: 60, Total: 100},
		},
		waitForStop: true,
	})

	d := testDB(t)
	id := insertDownload(t, d)

	eng := New(drv, d)
	go eng.Run(t.Context())
	eng.Kick()
	waitActive(t, eng, id)

	deadline := time.Now().Add(10 * time.Second)
	for eng.Snapshot().AvgRate == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for a rate to be measured")
		}
		time.Sleep(5 * time.Millisecond)
	}
	eng.SetPaused(true)
	for eng.ActiveID() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("pause never cancelled the download in flight")
		}
		time.Sleep(5 * time.Millisecond)
	}

	snap := eng.Snapshot()
	if snap.AvgRate <= 0 {
		t.Errorf("snapshot = %+v, want the rate the run reached kept for the estimate", snap)
	}
	// The live column is a different question — nothing is moving — and it says
	// so by staying blank while the pause holds.
	if snap.Rate != 0 {
		t.Errorf("rate = %v, want nothing reported as moving while held", snap.Rate)
	}
}

// Nothing has been measured before the first transfer, so there is no speed to
// project from and the estimate says nothing rather than making one up.
func TestSnapshotHasNoRateBeforeAnythingRuns(t *testing.T) {
	eng := New(newFakeDriver(), testDB(t))
	if snap := eng.Snapshot(); snap.AvgRate != 0 {
		t.Errorf("snapshot = %+v, want no rate before a transfer has measured one", snap)
	}
}
