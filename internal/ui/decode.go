package ui

import (
	"encoding/base64"
	"math"
	"math/rand/v2"
	"strings"
	"unicode/utf8"
)

// maxDecodeDepth bounds nested decoding; links in the wild show up encoded
// once or twice, so allow a little headroom without decoding forever.
const maxDecodeDepth = 3

// decodeBase64Text reports whether s is base64-encoded UTF-8 text and returns
// the innermost decoded value. What that text represents is deliberately left
// for submission after the user has reviewed it.
func decodeBase64Text(s string) (string, bool) {
	cur := s
	for depth := 0; depth < maxDecodeDepth; depth++ {
		decoded, ok := base64Decode(cur)
		if !ok {
			if depth == 0 {
				return "", false
			}
			return strings.TrimSpace(cur), true
		}
		cur = strings.TrimSpace(decoded)
	}
	return cur, true
}

var base64Encodings = []*base64.Encoding{
	base64.StdEncoding, base64.RawStdEncoding,
	base64.URLEncoding, base64.RawURLEncoding,
}

// base64Decode tries the standard and URL-safe alphabets, padded and raw.
// Whitespace is stripped first so wrapped pastes still decode.
func base64Decode(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), "")
	if s == "" {
		return "", false
	}
	for _, enc := range base64Encodings {
		b, err := enc.DecodeString(s)
		if err != nil || !utf8.Valid(b) {
			continue
		}
		return string(b), true
	}
	return "", false
}

var decodeCharset = []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=!#$%&@")

// decodeAnimFrame renders one frame of the decode scramble: the left part of
// target is revealed while the rest flickers with random base64-ish runes,
// and the overall length drifts from len(src) to len(target).
func decodeAnimFrame(src, target string, frame, total int) string {
	if frame >= total {
		return target
	}
	progress := float64(frame) / float64(total)
	tr := []rune(target)
	srcLen, dstLen := utf8.RuneCountInString(src), len(tr)
	length := srcLen + int(math.Round(progress*float64(dstLen-srcLen)))
	revealed := min(decodeRevealCount(target, frame, total), length)
	out := make([]rune, length)
	copy(out, tr[:revealed])
	for i := revealed; i < length; i++ {
		out[i] = decodeCharset[rand.IntN(len(decodeCharset))]
	}
	return string(out)
}

func decodeRevealCount(target string, frame, total int) int {
	if frame <= 0 || total <= 0 {
		return 0
	}
	runes := utf8.RuneCountInString(target)
	if frame >= total {
		return runes
	}
	return min(int(float64(frame)/float64(total)*float64(runes)), runes)
}
