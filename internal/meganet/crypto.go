package meganet

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
)

// b64decode decodes MEGA's url-safe base64 (no padding; stray '=' tolerated).
func b64decode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

func b64encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// fileKey is the unpacked form of a 32-byte file node key.
type fileKey struct {
	aes     [16]byte // key[0:16] ^ key[16:32]
	nonce   [8]byte  // key[16:24]
	metaMAC [8]byte  // key[24:32], the condensed integrity MAC
}

func unpackFileKey(nodeKey []byte) (fileKey, error) {
	var k fileKey
	if len(nodeKey) != 32 {
		return k, fmt.Errorf("file key is %d bytes, want 32", len(nodeKey))
	}
	for i := range k.aes {
		k.aes[i] = nodeKey[i] ^ nodeKey[i+16]
	}
	copy(k.nonce[:], nodeKey[16:24])
	copy(k.metaMAC[:], nodeKey[24:32])
	return k, nil
}

// decryptECBb64 base64-decodes and AES-128-ECB-decrypts a key blob.
func decryptECBb64(key []byte, s string) ([]byte, error) {
	data, err := b64decode(s)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("cipher length %d not a block multiple", len(data))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	for off := 0; off < len(data); off += aes.BlockSize {
		block.Decrypt(out[off:], data[off:])
	}
	return out, nil
}

// decryptAttrs decrypts a node attribute blob (AES-128-CBC, zero IV,
// "MEGA{json}" plaintext) and returns the node name.
func decryptAttrs(key []byte, at string) (string, error) {
	data, err := b64decode(at)
	if err != nil {
		return "", err
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return "", fmt.Errorf("attribute length %d not a block multiple", len(data))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	var iv [aes.BlockSize]byte
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(data, data)

	if !bytes.HasPrefix(data, []byte("MEGA")) {
		return "", fmt.Errorf("malformed attributes")
	}
	j := data[4:]
	if i := bytes.IndexByte(j, 0); i >= 0 { // zero padding after the JSON
		j = j[:i]
	}
	var attrs struct {
		N string `json:"n"`
	}
	if err := json.Unmarshal(j, &attrs); err != nil {
		return "", fmt.Errorf("malformed attributes: %w", err)
	}
	if attrs.N == "" {
		return "", fmt.Errorf("attributes are missing the node name")
	}
	return attrs.N, nil
}

// sanitizeName makes a remote node name safe as one local path component.
func sanitizeName(name string) (string, error) {
	name = strings.ReplaceAll(name, "/", "_")
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("invalid remote file name %q", name)
	}
	return name, nil
}

// ctrAt returns the AES-128-CTR keystream positioned at byte offset off
// (iv = nonce || big-endian block counter, mid-block offsets consumed).
func ctrAt(key [16]byte, nonce [8]byte, off int64) (cipher.Stream, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	var iv [16]byte
	copy(iv[:8], nonce[:])
	binary.BigEndian.PutUint64(iv[8:], uint64(off)/16)
	s := cipher.NewCTR(block, iv[:])
	if pad := off % 16; pad > 0 {
		var scratch [16]byte
		s.XORKeyStream(scratch[:pad], scratch[:pad])
	}
	return s, nil
}

// macChunkSize returns the size of the idx-th MAC chunk: 128 KiB, 256 KiB,
// ... growing to 1 MiB, then 1 MiB forever.
func macChunkSize(idx int) int64 {
	if idx < 8 {
		return int64(idx+1) * 128 * 1024
	}
	return 8 * 128 * 1024
}

// chunkedMAC is MEGA's chunked CBC-MAC over the file plaintext: a
// CBC-MAC (iv = nonce||nonce) per chunk, chunk MACs folded into a
// meta-MAC that condenses to the 8 bytes stored in the node key.
type chunkedMAC struct {
	block        cipher.Block
	chunkIdx     int
	nextBoundary int64
	pos          int64
	iv           [16]byte
	chunk        [16]byte
	meta         [16]byte
}

func newChunkedMAC(key [16]byte, nonce [8]byte) (*chunkedMAC, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	m := &chunkedMAC{block: block, nextBoundary: macChunkSize(0)}
	copy(m.iv[:8], nonce[:])
	copy(m.iv[8:], nonce[:])
	m.chunk = m.iv
	return m, nil
}

func (m *chunkedMAC) closeChunk() {
	for i := range m.meta {
		m.meta[i] ^= m.chunk[i]
	}
	m.block.Encrypt(m.meta[:], m.meta[:])
	m.chunk = m.iv
	m.chunkIdx++
	m.nextBoundary += macChunkSize(m.chunkIdx)
}

func (m *chunkedMAC) update(data []byte) {
	for len(data) > 0 {
		// fast path: an aligned whole block that stays inside the chunk
		if m.pos%16 == 0 && len(data) >= 16 && m.pos+16 <= m.nextBoundary {
			for i := 0; i < 16; i++ {
				m.chunk[i] ^= data[i]
			}
			m.block.Encrypt(m.chunk[:], m.chunk[:])
			m.pos += 16
			data = data[16:]
		} else {
			m.chunk[m.pos%16] ^= data[0]
			m.pos++
			if m.pos%16 == 0 {
				m.block.Encrypt(m.chunk[:], m.chunk[:])
			}
			data = data[1:]
		}
		if m.pos == m.nextBoundary {
			m.closeChunk()
		}
	}
}

// finish zero-pads the last block, folds any unfinished chunk and
// condenses the meta-MAC to the 8 bytes kept in the node key.
func (m *chunkedMAC) finish() (out [8]byte) {
	if m.pos%16 != 0 {
		m.pos += 16 - m.pos%16
		m.block.Encrypt(m.chunk[:], m.chunk[:])
	}
	if m.pos > m.nextBoundary-macChunkSize(m.chunkIdx) {
		m.closeChunk()
	}
	for i := 0; i < 4; i++ {
		out[i] = m.meta[i] ^ m.meta[i+4]
		out[i+4] = m.meta[i+8] ^ m.meta[i+12]
	}
	return out
}
