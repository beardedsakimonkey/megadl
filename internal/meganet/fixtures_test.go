package meganet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- fixture crypto (the encrypt side of what the driver decrypts) ----

func derive16(seed string) (out [16]byte) {
	sum := sha256.Sum256([]byte("k16:" + seed))
	copy(out[:], sum[:16])
	return
}

func derive8(seed string) (out [8]byte) {
	sum := sha256.Sum256([]byte("n8:" + seed))
	copy(out[:], sum[:8])
	return
}

func ecbEncrypt(t *testing.T, key, data []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(data))
	for off := 0; off < len(data); off += 16 {
		block.Encrypt(out[off:], data[off:])
	}
	return out
}

func attrsBlob(t *testing.T, key []byte, name string) string {
	t.Helper()
	j, err := json.Marshal(map[string]string{"n": name})
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte("MEGA"), j...)
	for len(raw)%16 != 0 {
		raw = append(raw, 0)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	var iv [16]byte
	out := make([]byte, len(raw))
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(out, raw)
	return b64encode(out)
}

func ctrEncrypt(t *testing.T, key [16]byte, nonce [8]byte, data []byte) []byte {
	t.Helper()
	s, err := ctrAt(key, nonce, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(data))
	s.XORKeyStream(out, data)
	return out
}

// packFileKey builds the 32-byte node key for plaintext data: aes key
// and nonce packed with the condensed meta-MAC of the content.
func packFileKey(t *testing.T, aesKey [16]byte, nonce [8]byte, data []byte) [32]byte {
	t.Helper()
	m, err := newChunkedMAC(aesKey, nonce)
	if err != nil {
		t.Fatal(err)
	}
	m.update(data)
	cond := m.finish()
	var k [32]byte
	copy(k[16:24], nonce[:])
	copy(k[24:32], cond[:])
	for i := 0; i < 16; i++ {
		k[i] = aesKey[i] ^ k[16+i]
	}
	return k
}

// ---- fake MEGA server ----

const hashcashEasiness = 255 // ~50% success per attempt, tests stay fast

type fakeMega struct {
	t          *testing.T
	srv        *httptest.Server
	folderKey  []byte
	linkHandle string
	rootHandle string

	fnodes    []map[string]any
	data      map[string][]byte // handle -> encrypted content
	plain     map[string][]byte // handle -> plaintext
	fileAttrs map[string]string // file-link handle -> encrypted attributes

	mu            sync.Mutex
	eagainLeft    int
	quota509Left  int
	slowData      bool
	hashcashToken string // when set, API demands a solved challenge
	hashcashSeen  bool
	dataReqs      []string
}

func newFakeMega(t *testing.T) *fakeMega {
	fk := derive16("folder-master-key")
	m := &fakeMega{
		t:          t,
		folderKey:  fk[:],
		linkHandle: "LINKHND1",
		data:       map[string][]byte{},
		plain:      map[string][]byte{},
		fileAttrs:  map[string]string{},
	}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *fakeMega) driver() *Driver {
	return &Driver{APIURL: m.srv.URL + "/cs", HTTP: m.srv.Client(), RetryBase: 2 * time.Millisecond}
}

func (m *fakeMega) folderURL() string {
	return "https://mega.nz/folder/" + m.linkHandle + "#" + b64encode(m.folderKey)
}

func (m *fakeMega) addDir(handle, parent, name string) {
	if m.rootHandle == "" {
		m.rootHandle = handle
	}
	dirKey := derive16("dir:" + handle)
	m.fnodes = append(m.fnodes, map[string]any{
		"h": handle, "p": parent, "a": attrsBlob(m.t, dirKey[:], name),
		"k": m.rootHandle + ":" + b64encode(ecbEncrypt(m.t, m.folderKey, dirKey[:])),
		"t": 1, "s": 0,
	})
}

func (m *fakeMega) addFile(handle, parent, name string, plain []byte) {
	if m.rootHandle == "" {
		m.rootHandle = handle
	}
	aesKey, nonce := derive16("file:"+handle), derive8("file:"+handle)
	nodeKey := packFileKey(m.t, aesKey, nonce, plain)
	m.fnodes = append(m.fnodes, map[string]any{
		"h": handle, "p": parent, "a": attrsBlob(m.t, aesKey[:], name),
		"k": m.rootHandle + ":" + b64encode(ecbEncrypt(m.t, m.folderKey, nodeKey[:])),
		"t": 0, "s": len(plain),
	})
	m.data[handle] = ctrEncrypt(m.t, aesKey, nonce, plain)
	m.plain[handle] = plain
}

// addFileLink registers a standalone file link and returns its URL.
func (m *fakeMega) addFileLink(handle, name string, plain []byte) string {
	aesKey, nonce := derive16("file:"+handle), derive8("file:"+handle)
	nodeKey := packFileKey(m.t, aesKey, nonce, plain)
	m.fileAttrs[handle] = attrsBlob(m.t, aesKey[:], name)
	m.data[handle] = ctrEncrypt(m.t, aesKey, nonce, plain)
	m.plain[handle] = plain
	return "https://mega.nz/file/" + handle + "#" + b64encode(nodeKey[:])
}

func (m *fakeMega) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/cs"):
		m.handleAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/dl/"):
		m.handleData(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (m *fakeMega) handleAPI(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.hashcashToken != "" {
		if !m.validHashcash(r.Header.Get("X-Hashcash")) {
			w.Header().Set("X-Hashcash", fmt.Sprintf("1:%d:junk:%s", hashcashEasiness, m.hashcashToken))
			w.WriteHeader(402)
			return
		}
		m.hashcashSeen = true
	}
	if m.eagainLeft > 0 {
		m.eagainLeft--
		fmt.Fprint(w, "-3")
		return
	}

	var cmds []map[string]any
	if err := json.NewDecoder(r.Body).Decode(&cmds); err != nil || len(cmds) != 1 {
		m.t.Errorf("bad API request: %v", err)
		fmt.Fprint(w, "-2")
		return
	}
	cmd := cmds[0]
	switch cmd["a"] {
	case "f":
		m.reply(w, map[string]any{"f": m.fnodes})
	case "g":
		if p, ok := cmd["p"].(string); ok { // file link
			m.reply(w, map[string]any{
				"g": m.srv.URL + "/dl/" + p, "s": len(m.plain[p]), "at": m.fileAttrs[p],
			})
			return
		}
		n, _ := cmd["n"].(string)
		if _, ok := m.plain[n]; !ok {
			fmt.Fprint(w, "[-9]")
			return
		}
		m.reply(w, map[string]any{"g": m.srv.URL + "/dl/" + n, "s": len(m.plain[n])})
	default:
		m.t.Errorf("unexpected API command %v", cmd)
		fmt.Fprint(w, "-2")
	}
}

func (m *fakeMega) reply(w http.ResponseWriter, obj any) {
	out, err := json.Marshal([]any{obj})
	if err != nil {
		m.t.Fatal(err)
	}
	w.Write(out)
}

func (m *fakeMega) validHashcash(hdr string) bool {
	parts := strings.Split(hdr, ":")
	if len(parts) != 3 || parts[0] != "1" || parts[1] != m.hashcashToken {
		return false
	}
	prefix, err := b64decode(parts[2])
	if err != nil || len(prefix) != 4 {
		return false
	}
	token, err := b64decode(m.hashcashToken)
	if err != nil || len(token) != 48 {
		return false
	}
	buf := make([]byte, 4+262144*48)
	copy(buf, prefix)
	for i := 0; i < 262144; i++ {
		copy(buf[4+i*48:], token)
	}
	sum := sha256.Sum256(buf)
	threshold := (uint32(hashcashEasiness&63)<<1 + 1) << ((uint32(hashcashEasiness) >> 6) * 7 + 3)
	return binary.BigEndian.Uint32(sum[:4]) <= threshold
}

// handleData serves /dl/<handle>/<from>-<to> ranges of encrypted data.
func (m *fakeMega) handleData(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.dataReqs = append(m.dataReqs, r.URL.Path)
	quota := m.quota509Left > 0
	if quota {
		m.quota509Left--
	}
	slow := m.slowData
	m.mu.Unlock()

	if quota {
		w.WriteHeader(509)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/dl/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	enc := m.data[parts[0]]
	from, to, ok := parseRange(parts[1])
	if enc == nil || !ok || from < 0 || to >= int64(len(enc)) || from > to {
		http.NotFound(w, r)
		return
	}

	if slow { // send a few bytes, then stall until the client goes away
		w.Write(enc[from:min(from+4, to+1)])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}
	w.Write(enc[from : to+1])
}

func parseRange(s string) (from, to int64, ok bool) {
	a, b, found := strings.Cut(s, "-")
	if !found {
		return 0, 0, false
	}
	from, err1 := strconv.ParseInt(a, 10, 64)
	to, err2 := strconv.ParseInt(b, 10, 64)
	return from, to, err1 == nil && err2 == nil
}
