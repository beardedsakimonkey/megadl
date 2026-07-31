// Package meganet is a native Go implementation of the slice of the
// MEGA protocol needed to list and download exported (public) file and
// folder links. It implements mega.Driver directly, with no external
// downloader or subprocess.
package meganet

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"megadl/internal/mega"
)

// Driver downloads from MEGA directly. The zero value is ready to use;
// the fields exist so tests can point it at a fake server and shrink
// retry delays.
type Driver struct {
	APIURL    string        // MEGA API endpoint; default https://g.api.mega.co.nz/cs
	HTTP      *http.Client  // HTTP client for API and data transfers
	RetryBase time.Duration // unit for data-chunk retry backoff; default 1s
}

var defaultHTTPClient = &http.Client{}

func (d *Driver) httpClient() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return defaultHTTPClient
}

func (d *Driver) retryBase() time.Duration {
	if d.RetryBase > 0 {
		return d.RetryBase
	}
	return time.Second
}

func (d *Driver) client(folder string) *apiClient {
	u := d.APIURL
	if u == "" {
		u = defaultAPIURL
	}
	return &apiClient{url: u, folder: folder, hc: d.httpClient()}
}

// link is a parsed mega.nz URL.
type link struct {
	typ      string // "file" | "folder"
	handle   string
	key      string
	specific string // folder deep links: handle of the node to rebase to
}

var linkRes = []struct {
	re  *regexp.Regexp
	typ string
}{
	{regexp.MustCompile(`(?i)^https?://mega(?:\.co)?\.nz/#!([a-z0-9_-]{8})!([a-z0-9_=-]{43}={0,2})$`), "file"},
	{regexp.MustCompile(`(?i)^https?://mega\.nz/file/([a-z0-9_-]{8})#([a-z0-9_-]{43}={0,2})$`), "file"},
	{regexp.MustCompile(`(?i)^https?://mega(?:\.co)?\.nz/#F!([a-z0-9_-]{8})!([a-z0-9_-]{22})(?:[!?]([a-z0-9_-]{8}))?$`), "folder"},
	{regexp.MustCompile(`(?i)^https?://mega\.nz/folder/([a-z0-9_-]{8})#([a-z0-9_-]{22})/file/([a-z0-9_-]{8})$`), "folder"},
	{regexp.MustCompile(`(?i)^https?://mega\.nz/folder/([a-z0-9_-]{8})#([a-z0-9_-]{22})/folder/([a-z0-9_-]{8})$`), "folder"},
	{regexp.MustCompile(`(?i)^https?://mega\.nz/folder/([a-z0-9_-]{8})#([a-z0-9_-]{22})$`), "folder"},
}

func parseLink(raw string) (link, error) {
	if u, err := url.PathUnescape(raw); err == nil {
		raw = u
	}
	raw = strings.TrimSpace(raw)
	for _, lr := range linkRes {
		if m := lr.re.FindStringSubmatch(raw); m != nil {
			l := link{typ: lr.typ, handle: m[1], key: m[2]}
			if len(m) > 3 {
				l.specific = m[3]
			}
			return l, nil
		}
	}
	return link{}, fmt.Errorf("invalid mega download link: %s", raw)
}

// fileInfo describes a prepared file-link download.
type fileInfo struct {
	name string
	size int64
	url  string
	key  fileKey
}

// prepareFileLink resolves a file link: API "g" for url/size/attributes,
// key from the URL fragment.
func (d *Driver) prepareFileLink(ctx context.Context, l link) (*fileInfo, error) {
	keyRaw, err := b64decode(l.key)
	if err != nil || len(keyRaw) != 32 {
		return nil, fmt.Errorf("can't retrieve file key")
	}
	key, err := unpackFileKey(keyRaw)
	if err != nil {
		return nil, err
	}

	var res struct {
		G  string `json:"g"`
		S  int64  `json:"s"`
		At string `json:"at"`
	}
	if err := d.client("").call(ctx, map[string]any{"a": "g", "g": 1, "ssl": 0, "p": l.handle}, &res); err != nil {
		return nil, err
	}
	if res.S < 0 || res.G == "" || res.At == "" {
		return nil, fmt.Errorf("incomplete file info from server")
	}
	name, err := decryptAttrs(key.aes[:], res.At)
	if err != nil {
		return nil, fmt.Errorf("invalid key")
	}
	if name, err = sanitizeName(name); err != nil {
		return nil, err
	}
	return &fileInfo{name: name, size: res.S, url: res.G, key: key}, nil
}

// List fetches a link's contents without downloading anything.
func (d *Driver) List(ctx context.Context, rawURL string) ([]mega.Node, error) {
	l, err := parseLink(rawURL)
	if err != nil {
		return nil, err
	}
	if l.typ == "file" {
		info, err := d.prepareFileLink(ctx, l)
		if err != nil {
			return nil, err
		}
		return []mega.Node{{
			Index: 1, Path: "/" + info.name, Name: info.name,
			Type: "file", Size: info.size, Handle: l.handle,
		}}, nil
	}
	fs, err := d.openFolder(ctx, l)
	if err != nil {
		return nil, err
	}
	return fs.listing(), nil
}

// proc is a running native download.
type proc struct {
	events chan mega.Event
	cancel context.CancelFunc
	nudge  chan struct{}
}

func (p *proc) Events() <-chan mega.Event { return p.events }
func (p *proc) Stop()                     { p.cancel() }

// RetryNow drops a nudge for whatever backoff is waiting. Nothing waiting
// means the buffered slot simply holds it until the next wait drains it, so
// pressing it between attempts costs nothing.
func (p *proc) RetryNow() {
	select {
	case p.nudge <- struct{}{}:
	default:
	}
}

// Start launches a download and streams file/progress/completion events.
// Partial files persist as .megatmp.<handle> and resume.
func (d *Driver) Start(ctx context.Context, args mega.DownloadArgs) (mega.Proc, error) {
	ctx, cancel := context.WithCancel(ctx)
	p := &proc{
		events: make(chan mega.Event, 64),
		cancel: cancel,
		nudge:  make(chan struct{}, 1),
	}
	go func() {
		status := d.runLink(ctx, args, session{events: p.events, nudge: p.nudge})
		if err := ctx.Err(); err != nil {
			p.events <- mega.ExitEvent{Err: err}
		} else {
			p.events <- mega.EndEvent{Status: status}
			p.events <- mega.ExitEvent{}
		}
		close(p.events)
	}()
	return p, nil
}

func (d *Driver) runLink(ctx context.Context, args mega.DownloadArgs, s session) int {
	l, err := parseLink(args.URL)
	if err != nil {
		s.events <- mega.ErrorEvent{Message: err.Error()}
		return 1
	}

	if l.typ == "file" {
		info, err := d.prepareFileLink(ctx, l)
		if err != nil {
			if ctx.Err() != nil {
				return 1
			}
			s.events <- mega.ErrorEvent{Message: "Can't get file info: " + err.Error()}
			return 1
		}
		// a non-directory path is the exact local file name
		path := args.Path
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			path = filepath.Join(path, info.name)
		}
		job := fileJob{
			localPath: path, remotePath: "/" + info.name,
			size: info.size, handle: l.handle, key: info.key,
			getURL: func(context.Context) (string, int64, error) { return info.url, info.size, nil },
		}
		if !d.syncFile(ctx, s, job) {
			return 1
		}
		return 0
	}

	fs, err := d.openFolder(ctx, l)
	if err != nil {
		if ctx.Err() != nil {
			return 1
		}
		s.events <- mega.ErrorEvent{Message: "Can't open folder: " + err.Error()}
		return 1
	}
	if err := os.MkdirAll(args.Path, 0o755); err != nil {
		s.events <- mega.ErrorEvent{Message: err.Error()}
		return 1
	}
	jobs, status := planJobs(fs, args, s.events)
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		if !d.syncFile(ctx, s, job) {
			status = 1
		}
	}
	return status
}

// fileJob is one file to place at localPath.
type fileJob struct {
	localPath  string
	remotePath string
	size       int64
	handle     string
	key        fileKey
	getURL     func(ctx context.Context) (url string, size int64, err error)
}

// planJobs resolves selected handles: unknown handles warn,
// duplicates and nodes covered by a chosen ancestor are pruned, chosen
// folders expand to their files, and everything lands under args.Path
// relative to the link root.
func planJobs(fs *folderFS, args mega.DownloadArgs, events chan<- mega.Event) ([]fileJob, int) {
	status := 0
	var chosen []*fnode
	if len(args.SelectHandles) > 0 {
		seen := map[string]bool{}
		for _, h := range args.SelectHandles {
			if h == "" || seen[h] {
				continue
			}
			seen[h] = true
			n := fs.byHandle[h]
			if n == nil {
				events <- mega.WarnEvent{Code: "handle_not_found", Handle: h}
				status = 1
				continue
			}
			chosen = append(chosen, n)
		}
		sort.Slice(chosen, func(i, j int) bool { return sortKey(chosen[i]) < sortKey(chosen[j]) })
		chosen = pruneChildren(chosen)
	} else {
		chosen = []*fnode{fs.root}
	}

	var jobs []fileJob
	added := map[string]bool{}
	for _, n := range chosen {
		for _, f := range fs.filesUnder(n) {
			if added[f.handle] {
				continue
			}
			added[f.handle] = true
			local := filepath.Join(args.Path, filepath.FromSlash(relToRoot(fs, f)))
			jobs = append(jobs, fs.job(f, local))
		}
	}
	return jobs, status
}

// relToRoot maps a node to its local path relative to the link root:
// the root file itself keeps just its name.
func relToRoot(fs *folderFS, n *fnode) string {
	if n == fs.root {
		return n.name
	}
	return strings.TrimPrefix(n.path, fs.root.path+"/")
}

// pruneChildren drops nodes that have an ancestor in the list.
func pruneChildren(nodes []*fnode) []*fnode {
	var out []*fnode
	for _, n := range nodes {
		covered := false
		for p := n.parent; p != nil && !covered; p = p.parent {
			for _, m := range nodes {
				if m == p {
					covered = true
					break
				}
			}
		}
		if !covered {
			out = append(out, n)
		}
	}
	return out
}

func (fs *folderFS) job(n *fnode, local string) fileJob {
	key, _ := unpackFileKey(n.key) // length checked at parse time
	return fileJob{
		localPath: local, remotePath: n.path,
		size: n.size, handle: n.handle, key: key,
		getURL: func(ctx context.Context) (string, int64, error) {
			return fs.downloadURL(ctx, n.handle)
		},
	}
}
