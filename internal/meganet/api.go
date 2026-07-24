package meganet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const defaultAPIURL = "https://g.api.mega.co.nz/cs"

const srvEAGAIN = -3

var srvErrorNames = map[int]string{
	-1: "EINTERNAL", -2: "EARGS", -3: "EAGAIN", -4: "ERATELIMIT",
	-5: "EFAILED", -6: "ETOOMANY", -7: "ERANGE", -8: "EEXPIRED",
	-9: "ENOENT", -10: "ECIRCULAR", -11: "EACCESS", -12: "EEXIST",
	-13: "EINCOMPLETE", -14: "EKEY", -15: "ESID", -16: "EBLOCKED",
	-17: "EOVERQUOTA", -18: "ETEMPUNAVAIL", -19: "ETOOMANYCONNECTIONS",
}

// srvError is a negative status code returned by the MEGA API.
type srvError int

func (e srvError) Error() string {
	if name, ok := srvErrorNames[int(e)]; ok {
		return "server returned error " + name
	}
	return fmt.Sprintf("server returned error EUNKNOWN (%d)", int(e))
}

// apiClient posts commands to the MEGA API for one session.
type apiClient struct {
	url    string
	folder string // link handle, sent as n= on folder-link sessions
	hc     *http.Client
	seq    atomic.Uint32
}

// call posts one command and decodes the first element of the reply,
// retrying EAGAIN (and dropped connections / 500s, which the server
// uses interchangeably) with exponential backoff.
func (c *apiClient) call(ctx context.Context, cmd, out any) error {
	body, err := json.Marshal([]any{cmd})
	if err != nil {
		return err
	}
	delay := 250 * time.Millisecond
	for {
		data, retryErr, err := c.post(ctx, body)
		if err != nil {
			return err
		}
		if retryErr == nil {
			again, cerr := decodeAPIResponse(data, out)
			if !again {
				return cerr
			}
			retryErr = srvError(srvEAGAIN)
		}
		if delay > 256*time.Second {
			return fmt.Errorf("server keeps asking us to retry, giving up: %w", retryErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}

// post performs one HTTP round trip, solving X-Hashcash challenges
// in-line. retryErr is set for responses that should be treated as
// EAGAIN; err is terminal.
func (c *apiClient) post(ctx context.Context, body []byte) (data []byte, retryErr, err error) {
	u := fmt.Sprintf("%s?id=%d", c.url, c.seq.Add(1))
	if c.folder != "" {
		u += "&n=" + c.folder
	}
	hashcash := ""
	for try := 0; try < 4; try++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		if hashcash != "" {
			req.Header.Set("X-Hashcash", hashcash)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, err, nil
		}
		switch {
		case resp.StatusCode == 402:
			challenge := resp.Header.Get("X-Hashcash")
			drain(resp)
			hashcash, err = solveHashcash(ctx, challenge)
			if err != nil {
				return nil, nil, fmt.Errorf("hashcash challenge: %w", err)
			}
			continue
		case resp.StatusCode == 500:
			drain(resp)
			return nil, fmt.Errorf("server returned 500 (probably busy)"), nil
		case resp.StatusCode != 200 && resp.StatusCode != 201:
			drain(resp)
			return nil, nil, fmt.Errorf("server returned %d", resp.StatusCode)
		}
		data, err = io.ReadAll(io.LimitReader(resp.Body, 256<<20))
		resp.Body.Close()
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, err, nil
		}
		return data, nil, nil
	}
	return nil, nil, fmt.Errorf("too many hashcash retries")
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
}

// decodeAPIResponse handles the API's reply shapes: a bare negative
// number (request-level error), or an array whose first element is the
// result object or a negative number. again is true for EAGAIN.
func decodeAPIResponse(data []byte, out any) (again bool, err error) {
	trim := bytes.TrimSpace(data)
	var code int
	if json.Unmarshal(trim, &code) == nil {
		if code == srvEAGAIN {
			return true, nil
		}
		if code < 0 {
			return false, srvError(code)
		}
		return false, fmt.Errorf("unexpected API response %s", trim)
	}
	var elems []json.RawMessage
	if json.Unmarshal(trim, &elems) != nil || len(elems) == 0 {
		return false, fmt.Errorf("invalid API response")
	}
	first := bytes.TrimSpace(elems[0])
	if json.Unmarshal(first, &code) == nil {
		if code < 0 {
			return false, srvError(code)
		}
		return false, fmt.Errorf("unexpected API response %s", trim)
	}
	if err := json.Unmarshal(first, out); err != nil {
		return false, fmt.Errorf("invalid API response: %w", err)
	}
	return false, nil
}

// solveHashcash answers MEGA's X-Hashcash v1 proof-of-work challenge
// ("1:<easiness>:<junk>:<token>"): find a 4-byte prefix such that the
// leading 32 bits of sha256(prefix || token×262144) are under the
// easiness threshold. Response header is "1:<token>:<b64 prefix>".
func solveHashcash(ctx context.Context, challenge string) (string, error) {
	parts := strings.Split(challenge, ":")
	if len(parts) != 4 || parts[0] != "1" {
		return "", fmt.Errorf("unsupported challenge %q", challenge)
	}
	easiness, err := strconv.Atoi(parts[1])
	if err != nil || easiness < 0 || easiness > 255 {
		return "", fmt.Errorf("bad easiness in challenge %q", challenge)
	}
	token, err := b64decode(parts[3])
	if err != nil || len(token) != 48 {
		return "", fmt.Errorf("bad token in challenge %q", challenge)
	}

	threshold := (uint32(easiness&63)<<1 + 1) << ((uint32(easiness)>>6)*7 + 3)
	buf := make([]byte, 4+262144*48)
	for i := 0; i < 262144; i++ {
		copy(buf[4+i*48:], token)
	}
	for prefix := uint32(1); prefix != 0; prefix++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		binary.LittleEndian.PutUint32(buf[:4], prefix)
		sum := sha256.Sum256(buf)
		if binary.BigEndian.Uint32(sum[:4]) <= threshold {
			return "1:" + parts[3] + ":" + b64encode(buf[:4]), nil
		}
	}
	return "", fmt.Errorf("hashcash search exhausted")
}
