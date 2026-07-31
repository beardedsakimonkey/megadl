package meganet

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"megadl/internal/mega"
)

// standardTree builds root/Season 1/e01.mkv plus root/readme.txt.
func standardTree(t *testing.T) (*fakeMega, []byte, []byte) {
	m := newFakeMega(t)
	episode := make([]byte, 300000) // spans two MAC chunk boundaries
	rand.New(rand.NewSource(42)).Read(episode)
	readme := []byte("hello from mega")

	m.addDir("ROOTHND1", "", "My Show")
	m.addDir("DIRHND01", "ROOTHND1", "Season 1")
	m.addFile("FILEHND1", "DIRHND01", "e01.mkv", episode)
	m.addFile("FILEHND2", "ROOTHND1", "readme.txt", readme)
	return m, episode, readme
}

func collect(t *testing.T, p mega.Proc) []mega.Event {
	t.Helper()
	var evs []mega.Event
	timeout := time.After(30 * time.Second)
	for {
		select {
		case ev, ok := <-p.Events():
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-timeout:
			t.Fatalf("timed out; events so far: %#v", evs)
		}
	}
}

func endStatus(t *testing.T, evs []mega.Event) int {
	t.Helper()
	status := -1
	for _, ev := range evs {
		if e, ok := ev.(mega.EndEvent); ok {
			status = e.Status
		}
	}
	if status == -1 {
		t.Fatalf("no end event in %#v", evs)
	}
	return status
}

func eventsOf[T mega.Event](evs []mega.Event) []T {
	var out []T
	for _, ev := range evs {
		if e, ok := ev.(T); ok {
			out = append(out, e)
		}
	}
	return out
}

func run(t *testing.T, d *Driver, args mega.DownloadArgs) []mega.Event {
	t.Helper()
	p, err := d.Start(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	return collect(t, p)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestListFolder(t *testing.T) {
	m, episode, readme := standardTree(t)
	nodes, err := m.driver().List(context.Background(), m.folderURL())
	if err != nil {
		t.Fatal(err)
	}

	want := []mega.Node{
		{Index: 1, Path: "/My Show", Name: "My Show", Type: "folder", Handle: "ROOTHND1", Parent: ""},
		{Index: 2, Path: "/My Show/Season 1", Name: "Season 1", Type: "folder", Handle: "DIRHND01", Parent: "ROOTHND1"},
		{Index: 3, Path: "/My Show/Season 1/e01.mkv", Name: "e01.mkv", Type: "file", Size: int64(len(episode)), Handle: "FILEHND1", Parent: "DIRHND01"},
		{Index: 4, Path: "/My Show/readme.txt", Name: "readme.txt", Type: "file", Size: int64(len(readme)), Handle: "FILEHND2", Parent: "ROOTHND1"},
	}
	if len(nodes) != len(want) {
		t.Fatalf("got %d nodes: %+v", len(nodes), nodes)
	}
	for i := range want {
		if nodes[i] != want[i] {
			t.Errorf("node %d = %+v, want %+v", i, nodes[i], want[i])
		}
	}
}

func TestListFileLink(t *testing.T) {
	m := newFakeMega(t)
	url := m.addFileLink("FLNKHND1", "video.mp4", []byte("mp4 bytes"))
	nodes, err := m.driver().List(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Name != "video.mp4" || nodes[0].Size != 9 || nodes[0].Type != "file" {
		t.Fatalf("nodes = %+v", nodes)
	}
}

func TestDownloadFolder(t *testing.T) {
	m, episode, readme := standardTree(t)
	dest := filepath.Join(t.TempDir(), "My Show")

	evs := run(t, m.driver(), mega.DownloadArgs{URL: m.folderURL(), Path: dest})

	if s := endStatus(t, evs); s != 0 {
		t.Fatalf("end status %d; events %#v", s, evs)
	}
	if got := mustRead(t, filepath.Join(dest, "Season 1", "e01.mkv")); !bytes.Equal(got, episode) {
		t.Error("episode content mismatch")
	}
	if got := mustRead(t, filepath.Join(dest, "readme.txt")); !bytes.Equal(got, readme) {
		t.Error("readme content mismatch")
	}

	starts := eventsOf[mega.FileStartEvent](evs)
	dones := eventsOf[mega.FileDoneEvent](evs)
	if len(starts) != 2 || len(dones) != 2 {
		t.Fatalf("starts=%v dones=%v", starts, dones)
	}
	if starts[0].Path != filepath.Join(dest, "Season 1", "e01.mkv") ||
		starts[0].Remote != "/My Show/Season 1/e01.mkv" || starts[0].Size != int64(len(episode)) {
		t.Errorf("first start = %+v", starts[0])
	}
	// each file: a -1 start marker and a -2 completion marker
	var openers, closers int
	for _, p := range eventsOf[mega.ProgressEvent](evs) {
		switch p.Done {
		case -1:
			openers++
		case -2:
			closers++
		}
	}
	if openers != 2 || closers != 2 {
		t.Errorf("progress markers: %d openers, %d closers", openers, closers)
	}
}

func TestSelectHandlesAndWarn(t *testing.T) {
	m, _, readme := standardTree(t)
	dest := filepath.Join(t.TempDir(), "sel")

	evs := run(t, m.driver(), mega.DownloadArgs{
		URL: m.folderURL(), Path: dest, SelectHandles: []string{"FILEHND2", "BOGUSHN1"},
	})

	warns := eventsOf[mega.WarnEvent](evs)
	if len(warns) != 1 || warns[0].Code != "handle_not_found" || warns[0].Handle != "BOGUSHN1" {
		t.Fatalf("warns = %+v", warns)
	}
	if s := endStatus(t, evs); s != 1 {
		t.Errorf("end status = %d, want 1 (missing handle)", s)
	}
	if got := mustRead(t, filepath.Join(dest, "readme.txt")); !bytes.Equal(got, readme) {
		t.Error("readme content mismatch")
	}
	if _, err := os.Stat(filepath.Join(dest, "Season 1")); !os.IsNotExist(err) {
		t.Error("unselected subtree should not exist")
	}
}

func TestSelectFolderHandle(t *testing.T) {
	m, episode, _ := standardTree(t)
	dest := filepath.Join(t.TempDir(), "sel")

	evs := run(t, m.driver(), mega.DownloadArgs{
		URL: m.folderURL(), Path: dest, SelectHandles: []string{"DIRHND01"},
	})
	if s := endStatus(t, evs); s != 0 {
		t.Fatalf("end status %d", s)
	}
	if got := mustRead(t, filepath.Join(dest, "Season 1", "e01.mkv")); !bytes.Equal(got, episode) {
		t.Error("episode content mismatch")
	}
	if _, err := os.Stat(filepath.Join(dest, "readme.txt")); !os.IsNotExist(err) {
		t.Error("readme should not be downloaded")
	}
}

func TestResumePartialFile(t *testing.T) {
	m, episode, _ := standardTree(t)
	dest := filepath.Join(t.TempDir(), "resume")

	// simulate an interrupted download: 150000 plaintext bytes on disk
	const seeded = 150000
	dir := filepath.Join(dest, "Season 1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".megatmp.FILEHND1"), episode[:seeded], 0o644); err != nil {
		t.Fatal(err)
	}

	evs := run(t, m.driver(), mega.DownloadArgs{
		URL: m.folderURL(), Path: dest, SelectHandles: []string{"FILEHND1"},
	})
	if s := endStatus(t, evs); s != 0 {
		t.Fatalf("end status %d; events %#v", s, evs)
	}
	if got := mustRead(t, filepath.Join(dir, "e01.mkv")); !bytes.Equal(got, episode) {
		t.Error("resumed content mismatch (MAC over seeded bytes must hold)")
	}

	// the server must only have been asked for the remainder
	m.mu.Lock()
	reqs := append([]string(nil), m.dataReqs...)
	m.mu.Unlock()
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0], "/150000-299999") {
		t.Errorf("data requests = %v, want a single 150000-299999 range", reqs)
	}
	// session total reported to the engine excludes seeded bytes
	for _, p := range eventsOf[mega.ProgressEvent](evs) {
		if p.Done == -1 && p.Total != int64(len(episode)-seeded) {
			t.Errorf("session total = %d, want %d", p.Total, len(episode)-seeded)
		}
	}
}

func TestSkipExisting(t *testing.T) {
	m, _, _ := standardTree(t)
	dest := filepath.Join(t.TempDir(), "skip")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "readme.txt"), []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	evs := run(t, m.driver(), mega.DownloadArgs{
		URL: m.folderURL(), Path: dest, SelectHandles: []string{"FILEHND2"},
	})
	if s := endStatus(t, evs); s != 0 {
		t.Fatalf("end status %d", s)
	}
	skips := eventsOf[mega.FileSkipEvent](evs)
	if len(skips) != 1 || skips[0].Path != filepath.Join(dest, "readme.txt") {
		t.Fatalf("skips = %+v", skips)
	}
	if got := mustRead(t, filepath.Join(dest, "readme.txt")); string(got) != "local edit" {
		t.Error("existing file must not be touched")
	}
}

// A 509 has to arrive as a retry the UI can describe and count down — the
// status, the attempt, and the exact wait — rather than as a line of text.
func TestQuota509EmitsRetryEventAndRetries(t *testing.T) {
	m, _, readme := standardTree(t)
	m.quota509Left = 1
	dest := filepath.Join(t.TempDir(), "quota")

	drv := m.driver()
	evs := run(t, drv, mega.DownloadArgs{
		URL: m.folderURL(), Path: dest, SelectHandles: []string{"FILEHND2"},
	})
	if s := endStatus(t, evs); s != 0 {
		t.Fatalf("end status %d; events %#v", s, evs)
	}
	retries := eventsOf[mega.RetryEvent](evs)
	if len(retries) != 1 {
		t.Fatalf("retry events = %+v", retries)
	}
	r := retries[0]
	if r.Status != 509 || r.Reason != "transfer quota exceeded" {
		t.Errorf("retry = %q / status %d, want the quota reason and 509", r.Reason, r.Status)
	}
	if r.Attempt != 1 || r.Delay != 2*drv.RetryBase {
		t.Errorf("attempt %d after %s, want attempt 1 after %s", r.Attempt, r.Delay, 2*drv.RetryBase)
	}
	if !strings.Contains(r.Detail, "509") {
		t.Errorf("detail = %q, want the underlying error", r.Detail)
	}
	if got := mustRead(t, filepath.Join(dest, "readme.txt")); !bytes.Equal(got, readme) {
		t.Error("content mismatch after quota retry")
	}
}

// The backoff has to be interruptible: once it has climbed into the minutes, a
// user who can see the throttle has lifted should not have to sit it out. The
// base here is long enough that only the nudge can end the wait.
func TestRetryNowEndsTheBackoff(t *testing.T) {
	m, _, readme := standardTree(t)
	m.quota509Left = 1
	dest := filepath.Join(t.TempDir(), "retrynow")

	d := m.driver()
	d.RetryBase = time.Hour

	p, err := d.Start(context.Background(), mega.DownloadArgs{
		URL: m.folderURL(), Path: dest, SelectHandles: []string{"FILEHND2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan []mega.Event, 1)
	go func() {
		var evs []mega.Event
		for ev := range p.Events() {
			evs = append(evs, ev)
			if _, ok := ev.(mega.RetryEvent); ok {
				p.RetryNow()
			}
		}
		done <- evs
	}()

	var evs []mega.Event
	select {
	case evs = <-done:
	case <-time.After(30 * time.Second):
		p.Stop()
		t.Fatal("retry now did not cut the backoff short")
	}
	if s := endStatus(t, evs); s != 0 {
		t.Fatalf("end status %d; events %#v", s, evs)
	}
	if got := mustRead(t, filepath.Join(dest, "readme.txt")); !bytes.Equal(got, readme) {
		t.Error("content mismatch after the skipped wait")
	}
}

// A nudge that arrives with nothing waiting must not carry over and swallow the
// next wait, which would put the download back to hammering a closed door.
func TestRetryNowBeforeAnyWaitDoesNotCarryOver(t *testing.T) {
	nudge := make(chan struct{}, 1)
	s := session{events: make(chan mega.Event, 4), nudge: nudge}
	nudge <- struct{}{} // pressed while nothing was retrying

	start := time.Now()
	const delay = 60 * time.Millisecond
	if !s.waitRetry(context.Background(), mega.RetryEvent{Delay: delay}) {
		t.Fatal("wait reported cancellation")
	}
	if elapsed := time.Since(start); elapsed < delay {
		t.Errorf("wait lasted %s, want the full %s: a stale nudge skipped it", elapsed, delay)
	}
}

func TestRetryReason(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason string
		wantStatus int
	}{
		{"quota", httpStatusErr(509), "transfer quota exceeded", 509},
		{"busy", httpStatusErr(500), "server busy", 500},
		{"gateway", httpStatusErr(503), "server busy", 503},
		{"other status", httpStatusErr(403), "server returned 403", 403},
		{"stalled", errStalled, "no data from server", 0},
		{"network", context.DeadlineExceeded, "connection failed", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, status := retryReason(tt.err)
			if reason != tt.wantReason || status != tt.wantStatus {
				t.Errorf("retryReason(%v) = %q, %d; want %q, %d",
					tt.err, reason, status, tt.wantReason, tt.wantStatus)
			}
		})
	}
}

func TestMACMismatch(t *testing.T) {
	m, _, _ := standardTree(t)
	m.data["FILEHND2"][0] ^= 0xff // corrupt the ciphertext
	dest := filepath.Join(t.TempDir(), "mac")

	evs := run(t, m.driver(), mega.DownloadArgs{
		URL: m.folderURL(), Path: dest, SelectHandles: []string{"FILEHND2"},
	})
	if s := endStatus(t, evs); s != 1 {
		t.Fatalf("end status %d, want 1", s)
	}
	errs := eventsOf[mega.FileErrorEvent](evs)
	if len(errs) != 1 || !strings.Contains(errs[0].Message, "MAC mismatch") {
		t.Fatalf("errors = %+v", errs)
	}
	if _, err := os.Stat(filepath.Join(dest, ".megatmp.FILEHND2")); !os.IsNotExist(err) {
		t.Error("corrupt temp file must be removed")
	}
	if _, err := os.Stat(filepath.Join(dest, "readme.txt")); !os.IsNotExist(err) {
		t.Error("corrupt file must not be renamed into place")
	}
}

func TestFileLinkDownloadExactPath(t *testing.T) {
	m := newFakeMega(t)
	content := []byte("standalone file content")
	url := m.addFileLink("FLNKHND1", "video.mp4", content)

	// A non-directory path is the exact target name.
	target := filepath.Join(t.TempDir(), "renamed.mp4")
	evs := run(t, m.driver(), mega.DownloadArgs{URL: url, Path: target})
	if s := endStatus(t, evs); s != 0 {
		t.Fatalf("end status %d; events %#v", s, evs)
	}
	if got := mustRead(t, target); !bytes.Equal(got, content) {
		t.Error("content mismatch")
	}
}

func TestStopMidDownloadKeepsPartial(t *testing.T) {
	m, _, _ := standardTree(t)
	m.slowData = true
	dest := filepath.Join(t.TempDir(), "stop")

	p, err := m.driver().Start(context.Background(), mega.DownloadArgs{
		URL: m.folderURL(), Path: dest, SelectHandles: []string{"FILEHND1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var evs []mega.Event
	timeout := time.After(30 * time.Second)
	stopped := false
	for {
		var ev mega.Event
		var ok bool
		select {
		case ev, ok = <-p.Events():
		case <-timeout:
			t.Fatalf("timeout; events %#v", evs)
		}
		if !ok {
			break
		}
		evs = append(evs, ev)
		if _, isStart := ev.(mega.FileStartEvent); isStart && !stopped {
			stopped = true
			p.Stop()
		}
	}

	if len(eventsOf[mega.EndEvent](evs)) != 0 {
		t.Errorf("no end event expected on stop; got %#v", evs)
	}
	exits := eventsOf[mega.ExitEvent](evs)
	if len(exits) != 1 || exits[0].Err == nil {
		t.Fatalf("exit = %+v, want context error", exits)
	}
	if _, err := os.Stat(filepath.Join(dest, "Season 1", "e01.mkv")); !os.IsNotExist(err) {
		t.Error("final file must not exist after stop")
	}
}

func TestHashcashChallenge(t *testing.T) {
	m, _, readme := standardTree(t)
	token := make([]byte, 48)
	rand.New(rand.NewSource(9)).Read(token)
	m.hashcashToken = b64encode(token)
	dest := filepath.Join(t.TempDir(), "hashcash")

	evs := run(t, m.driver(), mega.DownloadArgs{
		URL: m.folderURL(), Path: dest, SelectHandles: []string{"FILEHND2"},
	})
	if s := endStatus(t, evs); s != 0 {
		t.Fatalf("end status %d; events %#v", s, evs)
	}
	m.mu.Lock()
	seen := m.hashcashSeen
	m.mu.Unlock()
	if !seen {
		t.Error("server never saw a valid solved hashcash header")
	}
	if got := mustRead(t, filepath.Join(dest, "readme.txt")); !bytes.Equal(got, readme) {
		t.Error("content mismatch")
	}
}

func TestAPIEagainRetries(t *testing.T) {
	m, _, _ := standardTree(t)
	m.eagainLeft = 2

	nodes, err := m.driver().List(context.Background(), m.folderURL())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 4 {
		t.Fatalf("got %d nodes", len(nodes))
	}
}

func TestDeepFileLinkIntoFolder(t *testing.T) {
	m, episode, _ := standardTree(t)
	dest := filepath.Join(t.TempDir(), "deep")

	// folder link rebased to a single file: /folder/<h>#<key>/file/<h2>
	url := m.folderURL() + "/file/FILEHND1"
	nodes, err := m.driver().List(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Type != "file" || nodes[0].Name != "e01.mkv" {
		t.Fatalf("nodes = %+v", nodes)
	}

	evs := run(t, m.driver(), mega.DownloadArgs{URL: url, Path: dest, SelectHandles: []string{"FILEHND1"}})
	if s := endStatus(t, evs); s != 0 {
		t.Fatalf("end status %d; events %#v", s, evs)
	}
	if got := mustRead(t, filepath.Join(dest, "e01.mkv")); !bytes.Equal(got, episode) {
		t.Error("content mismatch")
	}
}
