package meganet

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"megadl/internal/mega"
)

// fnode is one decrypted node of an exported folder.
type fnode struct {
	handle       string
	parentHandle string
	name         string
	isDir        bool
	size         int64
	key          []byte // 32 bytes for files, 16 for folders
	parent       *fnode
	path         string // "/<root>/<...>/<name>"
}

// folderFS is the decrypted tree of an exported folder link.
type folderFS struct {
	api      *apiClient
	root     *fnode
	nodes    []*fnode // nodes reachable from root, sorted by remote path
	byHandle map[string]*fnode
}

type rawFNode struct {
	H  string `json:"h"`
	P  string `json:"p"`
	A  string `json:"a"`
	K  string `json:"k"`
	SK string `json:"sk"`
	T  int    `json:"t"`
	S  int64  `json:"s"`
}

// openFolder fetches and decrypts an exported folder's node tree
// (API call "f" with the link handle as the n= session parameter).
func (d *Driver) openFolder(ctx context.Context, l link) (*folderFS, error) {
	masterKey, err := b64decode(l.key)
	if err != nil || len(masterKey) != 16 {
		return nil, fmt.Errorf("invalid folder key")
	}
	api := d.client(l.handle)

	var res struct {
		F []json.RawMessage `json:"f"`
	}
	if err := api.call(ctx, map[string]any{"a": "f", "c": 1, "r": 1}, &res); err != nil {
		return nil, err
	}
	if len(res.F) == 0 {
		return nil, fmt.Errorf("folder listing returned no nodes")
	}

	// The first node is the link root; its handle unlocks every node key.
	shareKeys := map[string][]byte{}
	var parsed []*fnode
	for i, rawNode := range res.F {
		var rn rawFNode
		if err := json.Unmarshal(rawNode, &rn); err != nil {
			continue
		}
		if i == 0 {
			shareKeys[rn.H] = masterKey
		}
		n := parseFNode(&rn, shareKeys, masterKey)
		if n == nil {
			continue
		}
		if i == 0 {
			n.parentHandle = ""
		}
		parsed = append(parsed, n)
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("no decryptable nodes in folder")
	}

	byHandle := map[string]*fnode{}
	for _, n := range parsed {
		byHandle[n.handle] = n
	}
	for _, n := range parsed {
		if n.parentHandle != "" {
			n.parent = byHandle[n.parentHandle]
		}
	}

	root := parsed[0]
	if l.specific != "" { // deep link into the folder: rebase to that node
		root = byHandle[l.specific]
		if root == nil {
			return nil, fmt.Errorf("node not found: %s", l.specific)
		}
		root.parent = nil
		root.parentHandle = ""
	}

	// paths for everything reachable from root; the rest is dropped
	children := map[string][]*fnode{}
	for _, n := range parsed {
		if n != root && n.parent != nil {
			children[n.parent.handle] = append(children[n.parent.handle], n)
		}
	}
	root.path = "/" + root.name
	reach := []*fnode{root}
	for i := 0; i < len(reach); i++ {
		for _, c := range children[reach[i].handle] {
			c.path = reach[i].path + "/" + c.name
			reach = append(reach, c)
		}
	}
	sort.Slice(reach, func(i, j int) bool { return sortKey(reach[i]) < sortKey(reach[j]) })

	reachable := map[string]*fnode{}
	for _, n := range reach {
		reachable[n.handle] = n
	}
	return &folderFS{api: api, root: root, nodes: reach, byHandle: reachable}, nil
}

// sortKey provides byte-wise path ordering, with a trailing '/' on
// containers.
func sortKey(n *fnode) string {
	if n.isDir {
		return n.path + "/"
	}
	return n.path
}

// parseFNode decrypts and validates one raw node.
// Undecryptable or malformed nodes are skipped (nil).
func parseFNode(rn *rawFNode, shareKeys map[string][]byte, masterKey []byte) *fnode {
	if rn.H == "" || rn.A == "" || rn.K == "" {
		return nil
	}
	if rn.T != 0 && rn.T != 1 {
		return nil // only files and folders
	}
	isDir := rn.T == 1

	// a node can carry its own share key (AES variant only; no RSA here)
	if rn.SK != "" && len(rn.SK) <= 22 {
		if sk, err := decryptECBb64(masterKey, rn.SK); err == nil && len(sk) == 16 {
			shareKeys[rn.H] = sk
		}
	}

	// k is "keyhandle:key[/keyhandle:key...]"; use the first share key we know
	var shareKey []byte
	var encKey string
	for _, part := range strings.Split(rn.K, "/") {
		kh, kv, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		if sk := shareKeys[kh]; sk != nil {
			shareKey, encKey = sk, kv
			break
		}
	}
	if encKey == "" || len(encKey) >= 46 { // >=46 chars would be an RSA key
		return nil
	}
	nodeKey, err := decryptECBb64(shareKey, encKey)
	if err != nil {
		return nil
	}
	if (isDir && len(nodeKey) != 16) || (!isDir && len(nodeKey) != 32) {
		return nil
	}

	attrKey := nodeKey
	if !isDir {
		fk, _ := unpackFileKey(nodeKey)
		attrKey = fk.aes[:]
	}
	name, err := decryptAttrs(attrKey, rn.A)
	if err != nil {
		return nil
	}
	if name, err = sanitizeName(name); err != nil {
		return nil
	}

	return &fnode{
		handle:       rn.H,
		parentHandle: rn.P,
		name:         name,
		isDir:        isDir,
		size:         rn.S,
		key:          nodeKey,
	}
}

// listing converts the tree to the mega.Node form used by the UI picker.
func (fs *folderFS) listing() []mega.Node {
	out := make([]mega.Node, 0, len(fs.nodes))
	for i, n := range fs.nodes {
		typ := "file"
		if n.isDir {
			typ = "folder"
		}
		parent := n.parentHandle
		if n == fs.root {
			parent = ""
		}
		out = append(out, mega.Node{
			Index: i + 1, Path: n.path, Name: n.name, Type: typ,
			Size: n.size, Handle: n.handle, Parent: parent,
		})
	}
	return out
}

// downloadURL asks the API for a node's transfer URL and size.
func (fs *folderFS) downloadURL(ctx context.Context, handle string) (string, int64, error) {
	var res struct {
		G string `json:"g"`
		S int64  `json:"s"`
	}
	err := fs.api.call(ctx, map[string]any{"a": "g", "g": 1, "ssl": 0, "n": handle}, &res)
	if err != nil {
		return "", 0, err
	}
	if res.G == "" || res.S < 0 {
		return "", 0, fmt.Errorf("can't determine download url")
	}
	return res.G, res.S, nil
}

// filesUnder returns every file in n's subtree (n included if a file),
// in listing order.
func (fs *folderFS) filesUnder(n *fnode) []*fnode {
	var out []*fnode
	prefix := n.path + "/"
	for _, c := range fs.nodes {
		if c.isDir {
			continue
		}
		if c == n || strings.HasPrefix(c.path, prefix) {
			out = append(out, c)
		}
	}
	return out
}
