// AI assistance is disclosed in docs/m2/ai-usage.md; human review is required.
package satellite

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Frame preserves the SSE data bytes; decoding the assistant's JSON belongs in
// the adapter, whose contract must be verified against an authenticated sample.
var ErrProtocol = errors.New("satellite: invalid stream protocol")

type Frame struct {
	Kind       string
	Offset     int64
	Data       []byte
	ReceivedAt time.Time
}

// ReadSSE is synchronous: apply must durably finish before the next frame is read.
// A committed offset is requested again, so either inclusive or exclusive `from`
// semantics is safe: duplicates <= committed are skipped, gaps stop processing.
func ReadSSE(ctx context.Context, reader io.Reader, committed int64, apply func(context.Context, Frame) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	kind, id := "", ""
	var data []byte
	dispatch := func() error {
		defer func() { kind, id, data = "", "", nil }()
		if len(data) == 0 {
			return nil
		}
		if kind == "" {
			kind = "message"
		}
		offset := int64(-1)
		if id != "" {
			var err error
			offset, err = strconv.ParseInt(id, 10, 64)
			if err != nil || offset < 0 {
				return fmt.Errorf("%w: invalid SSE offset", ErrProtocol)
			}
		}
		if kind != "heartbeat" && offset < 0 {
			return fmt.Errorf("%w: data frame has no offset", ErrProtocol)
		}
		if offset >= 0 {
			if offset <= committed {
				return nil
			}
			if committed >= 0 && offset != committed+1 {
				return fmt.Errorf("%w: offset gap after %d: got %d", ErrProtocol, committed, offset)
			}
		}
		frame := Frame{Kind: kind, Offset: offset, Data: bytes.Clone(bytes.TrimSuffix(data, []byte{'\n'})), ReceivedAt: time.Now()}
		if err := apply(ctx, frame); err != nil {
			return err
		}
		if offset >= 0 {
			committed = offset
		}
		return nil
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Bytes()
		if !utf8.Valid(line) {
			return fmt.Errorf("%w: invalid UTF-8 in SSE", ErrProtocol)
		}
		if len(line) == 0 {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := strings.Cut(string(line), ":")
		if !found {
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			kind = value
		case "id":
			if strings.ContainsRune(value, 0) {
				return fmt.Errorf("%w: invalid SSE id", ErrProtocol)
			}
			id = value
		case "data":
			if len(data)+len(value)+1 > 1<<20 {
				return fmt.Errorf("%w: SSE frame exceeds 1 MiB", ErrProtocol)
			}
			data = append(data, value...)
			data = append(data, '\n')
		}
	}
	// SSE dispatches on a blank line, not EOF: replay an interrupted final frame.
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}

type StreamClient struct {
	base, token string
	http        *http.Client
	idle        time.Duration
}

func NewStreamClient(base, token string) (*StreamClient, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("satellite: expected an HTTPS base URL")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("satellite: token required")
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	return &StreamClient{base: strings.TrimRight(base, "/"), token: token, idle: 30 * time.Second, http: &http.Client{
		Transport: otelhttp.NewTransport(transport), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type idleReader struct {
	reader  io.Reader
	timeout time.Duration
	cancel  context.CancelFunc
}

func (r idleReader) Read(p []byte) (int, error) {
	// Count only blocked network reads, not the time spent committing a batch.
	timer := time.AfterFunc(r.timeout, r.cancel)
	defer timer.Stop()
	return r.reader.Read(p)
}

func (c *StreamClient) once(ctx context.Context, offset int64, apply func(context.Context, []Frame) error) error {
	stream, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(stream, http.MethodGet, c.base+"/v1/stream?from="+strconv.FormatInt(max(offset, 0), 10), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "text/event-stream")
	if offset >= 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(offset, 10))
	}
	response, err := c.http.Do(req)
	if err != nil {
		return errors.New("satellite: stream connection failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &streamHTTPError{response.StatusCode}
	}
	if strings.Split(response.Header.Get("Content-Type"), ";")[0] != "text/event-stream" {
		return fmt.Errorf("%w: response is not SSE", ErrProtocol)
	}
	return readBatches(ctx, stream, idleReader{response.Body, c.idle, cancel}, offset, apply)
}

type streamHTTPError struct{ code int }

func (e *streamHTTPError) Error() string { return fmt.Sprintf("satellite: HTTP %d", e.code) }

// Run reloads the durable offset before every connection. Handler errors stop the
// worker rather than consume past a failed commit. 429/503/disconnects back off.
func (c *StreamClient) Run(ctx context.Context, load func(context.Context) (int64, error), apply func(context.Context, []Frame) error) error {
	delay := time.Second
	for ctx.Err() == nil {
		offset, err := load(ctx)
		if err != nil {
			return err
		}
		var applyErr error
		progressed := false
		err = c.once(ctx, offset, func(ctx context.Context, frames []Frame) error {
			if err := apply(ctx, frames); err != nil {
				applyErr = err
				return err
			}
			if frames[len(frames)-1].Offset > offset {
				progressed = true
			}
			return nil
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if applyErr != nil {
			return applyErr
		}
		if errors.Is(err, ErrProtocol) {
			return err
		}
		var status *streamHTTPError
		if errors.As(err, &status) && status.code != 429 && status.code != 503 {
			return err
		}
		if progressed {
			delay = time.Second
		}
		timer := time.NewTimer(delay/2 + time.Duration(rand.Int64N(int64(delay/2))))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(15*time.Second, 2*delay)
	}
	return ctx.Err()
}

// Session exposes only the two specified fields used for lag calculation.
type Session struct {
	LatestOffset int64
	VirtualNow   time.Time
}

func (c *StreamClient) Session(ctx context.Context) (Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/session", nil)
	if err != nil {
		return Session{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(req)
	if err != nil {
		return Session{}, errors.New("satellite: session request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return Session{}, &streamHTTPError{response.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return Session{}, errors.New("satellite: invalid session response")
	}
	var wire struct {
		LatestOffset *int64    `json:"latest_offset"`
		VirtualNow   time.Time `json:"virtual_now"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.LatestOffset == nil || *wire.LatestOffset < 0 || wire.VirtualNow.IsZero() {
		return Session{}, errors.New("satellite: invalid session fields")
	}
	return Session{LatestOffset: *wire.LatestOffset, VirtualNow: wire.VirtualNow}, nil
}
