package meganet

import (
	"bytes"
	"crypto/aes"
	"math/rand"
	"testing"
)

func TestUnpackFileKey(t *testing.T) {
	nodeKey := make([]byte, 32)
	for i := range nodeKey {
		nodeKey[i] = byte(i)
	}
	k, err := unpackFileKey(nodeKey)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		if k.aes[i] != byte(i)^byte(i+16) {
			t.Fatalf("aes[%d] = %x", i, k.aes[i])
		}
	}
	if !bytes.Equal(k.nonce[:], nodeKey[16:24]) || !bytes.Equal(k.metaMAC[:], nodeKey[24:32]) {
		t.Fatalf("nonce/mac unpack wrong: %+v", k)
	}
	if _, err := unpackFileKey(nodeKey[:16]); err == nil {
		t.Fatal("short key must fail")
	}
}

func TestAttrsRoundTrip(t *testing.T) {
	key := derive16("attr-key")
	blob := attrsBlob(t, key[:], "Some Video (2024).mkv")
	name, err := decryptAttrs(key[:], blob)
	if err != nil || name != "Some Video (2024).mkv" {
		t.Fatalf("decryptAttrs = %q, %v", name, err)
	}
	bad := derive16("wrong-key")
	if _, err := decryptAttrs(bad[:], blob); err == nil {
		t.Fatal("wrong key must fail the MEGA prefix check")
	}
}

func TestSanitizeName(t *testing.T) {
	if n, err := sanitizeName("a/b/c"); err != nil || n != "a_b_c" {
		t.Fatalf("got %q, %v", n, err)
	}
	for _, bad := range []string{"", ".", ".."} {
		if _, err := sanitizeName(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

// TestCTRSeek verifies that a stream positioned at an arbitrary offset
// produces the same keystream as the suffix of a stream from zero.
func TestCTRSeek(t *testing.T) {
	key, nonce := derive16("ctr"), derive8("ctr")
	data := make([]byte, 1000)
	rand.New(rand.NewSource(1)).Read(data)

	full := ctrEncrypt(t, key, nonce, data)
	for _, off := range []int64{0, 1, 15, 16, 17, 512, 999} {
		s, err := ctrAt(key, nonce, off)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]byte, int64(len(data))-off)
		s.XORKeyStream(got, data[off:])
		if !bytes.Equal(got, full[off:]) {
			t.Fatalf("offset %d: keystream mismatch", off)
		}
	}
}

// refCondensedMAC is an independent non-streaming implementation:
// per-chunk
// CBC-MAC over explicit chunk boundaries, folded and condensed.
func refCondensedMAC(t *testing.T, key [16]byte, nonce [8]byte, data []byte) [8]byte {
	t.Helper()
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	var iv [16]byte
	copy(iv[:8], nonce[:])
	copy(iv[8:], nonce[:])

	var meta [16]byte
	for idx, off := 0, int64(0); off < int64(len(data)); idx++ {
		end := min(int64(len(data)), off+macChunkSize(idx))
		chunk := data[off:end]
		mac := iv
		for len(chunk) > 0 {
			n := min(16, len(chunk))
			for i := 0; i < n; i++ {
				mac[i] ^= chunk[i]
			}
			block.Encrypt(mac[:], mac[:])
			chunk = chunk[n:]
		}
		for i := range meta {
			meta[i] ^= mac[i]
		}
		block.Encrypt(meta[:], meta[:])
		off = end
	}

	var out [8]byte
	for i := 0; i < 4; i++ {
		out[i] = meta[i] ^ meta[i+4]
		out[i+4] = meta[i+8] ^ meta[i+12]
	}
	return out
}

func TestChunkedMACMatchesReference(t *testing.T) {
	key, nonce := derive16("mac"), derive8("mac")
	rng := rand.New(rand.NewSource(7))

	// sizes around block and chunk boundaries (128K, 384K, ...)
	for _, size := range []int{0, 5, 16, 31, 131072, 131073, 393216, 500000, 1310721} {
		data := make([]byte, size)
		rng.Read(data)

		m, err := newChunkedMAC(key, nonce)
		if err != nil {
			t.Fatal(err)
		}
		// feed in uneven pieces to exercise the streaming paths
		for off := 0; off < len(data); {
			n := min(len(data)-off, 1+rng.Intn(70000))
			m.update(data[off : off+n])
			off += n
		}
		if got, want := m.finish(), refCondensedMAC(t, key, nonce, data); got != want {
			t.Errorf("size %d: streaming MAC %x != reference %x", size, got, want)
		}
	}
}

func TestParseLink(t *testing.T) {
	cases := []struct {
		url               string
		typ, handle, spec string
	}{
		{"https://mega.nz/file/AbCd0189#" + strings43(), "file", "AbCd0189", ""},
		{"https://mega.nz/#!AbCd0189!" + strings43(), "file", "AbCd0189", ""},
		{"https://mega.co.nz/#!AbCd0189!" + strings43(), "file", "AbCd0189", ""},
		{"https://mega.nz/folder/AbCd0189#" + strings22(), "folder", "AbCd0189", ""},
		{"https://mega.nz/folder/AbCd0189#" + strings22() + "/file/EfGh2345", "folder", "AbCd0189", "EfGh2345"},
		{"https://mega.nz/folder/AbCd0189#" + strings22() + "/folder/EfGh2345", "folder", "AbCd0189", "EfGh2345"},
		{"https://mega.nz/#F!AbCd0189!" + strings22() + "!EfGh2345", "folder", "AbCd0189", "EfGh2345"},
	}
	for _, c := range cases {
		l, err := parseLink(c.url)
		if err != nil {
			t.Errorf("%s: %v", c.url, err)
			continue
		}
		if l.typ != c.typ || l.handle != c.handle || l.specific != c.spec {
			t.Errorf("%s parsed as %+v", c.url, l)
		}
	}
	for _, bad := range []string{"https://mega.nz/", "https://example.com/file/x#y", "not a url"} {
		if _, err := parseLink(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func strings43() string { return "A2345678901234567890123456789012345678901_-" }
func strings22() string { return "A234567890123456789012" }
