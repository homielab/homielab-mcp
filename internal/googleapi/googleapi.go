// Package googleapi is a small JSON client for Google REST APIs (and other
// JSON APIs via DecodeError): bounded response reads, Google error decoding,
// and retries on 429/5xx and transport failures with Retry-After support.
package googleapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RetryBackoff is the base delay between retries; overridable in tests.
var RetryBackoff = time.Second

const (
	MaxRetries      = 3
	Timeout         = 90 * time.Second
	MaxResponseBody = 32 << 20 // 32 MiB, plenty for CSV exports
)

// Client sends authenticated requests to one Google API.
type Client struct {
	HTTP *http.Client
	API  string // name used in error messages, e.g. "AdSense API"
	// DecodeError, when set, replaces Error for non-2xx responses (APIs
	// whose error bodies are not Google's).
	DecodeError func(status int, body []byte) error
}

// New wraps hc, applying the default Timeout when hc has none.
func New(hc *http.Client, api string) *Client {
	if hc.Timeout == 0 {
		hc.Timeout = Timeout
	}
	return &Client{HTTP: hc, API: api}
}

// Do sends method fullURL with body JSON-encoded (nil for none) and returns
// the response body and HTTP status. Non-2xx responses become errors.
func (c *Client) Do(ctx context.Context, method, fullURL string, body any) ([]byte, int, error) {
	return c.DoHeader(ctx, method, fullURL, body, nil)
}

// DoHeader is Do with extra request headers (e.g. Accept).
func (c *Client) DoHeader(ctx context.Context, method, fullURL string, body any, header http.Header) ([]byte, int, error) {
	// The payload is re-wrapped in a fresh reader for every attempt: a reader
	// consumed by attempt 1 would send an empty body on retry.
	var payload []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		payload = data
	}
	var out []byte
	var status int
	var retryAfter int64
	var err error
	for attempt := 1; attempt <= MaxRetries; attempt++ {
		if attempt > 1 {
			if werr := WaitBackoff(ctx, attempt, retryAfter); werr != nil {
				return nil, 0, werr
			}
			log.Printf("retrying %s %s after error: %v", method, fullURL, err)
		}
		out, status, retryAfter, err = c.attempt(ctx, method, fullURL, payload, header)
		if err == nil || !Retryable(status, err) {
			break
		}
	}
	return out, status, err
}

// DoOnce sends a single request without retrying, for non-idempotent writes
// where a blind repeat could double-apply (e.g. Play edits:insert).
func (c *Client) DoOnce(ctx context.Context, method, fullURL string, body any) ([]byte, int, error) {
	var payload []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		payload = data
	}
	out, status, _, err := c.attempt(ctx, method, fullURL, payload, nil)
	return out, status, err
}

func (c *Client) attempt(ctx context.Context, method, fullURL string, payload []byte, header http.Header) ([]byte, int, int64, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, 0, 0, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, 0, err // transport error — status 0 marks it retryable
	}
	defer resp.Body.Close()
	ra := ParseRetryAfter(resp.Header.Get("Retry-After"))
	// Read one byte past the cap so a larger body is detectable instead of
	// silently truncated (a cut-off CSV would otherwise look complete).
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBody+1))
	if err != nil {
		return nil, resp.StatusCode, ra, err
	}
	if len(data) > MaxResponseBody {
		return nil, resp.StatusCode, ra, fmt.Errorf("%s response exceeds the %d MiB read limit — narrow the report or export it in smaller windows", c.API, MaxResponseBody>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if c.DecodeError != nil {
			return nil, resp.StatusCode, ra, c.DecodeError(resp.StatusCode, data)
		}
		return nil, resp.StatusCode, ra, c.Error(resp.StatusCode, data)
	}
	return data, resp.StatusCode, ra, nil
}

// ListAll GETs fullURL page by page (pageToken / nextPageToken) and collects
// the array stored under itemsKey. q is not modified.
func ListAll[T any](ctx context.Context, c *Client, fullURL string, q url.Values, itemsKey string) ([]T, error) {
	q = maps.Clone(q)
	if q == nil {
		q = url.Values{}
	}
	var out []T
	var next string
	for range maxPages {
		u := fullURL
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		body, _, err := c.Do(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		var page map[string]json.RawMessage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", fullURL, err)
		}
		if raw, ok := page[itemsKey]; ok && string(raw) != "null" {
			var items []T
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, fmt.Errorf("decoding %s: %w", itemsKey, err)
			}
			out = append(out, items...)
		}
		next = ""
		if raw, ok := page["nextPageToken"]; ok {
			_ = json.Unmarshal(raw, &next)
		}
		if next == "" {
			break
		}
		q.Set("pageToken", next)
	}
	if next != "" {
		// Loud failure over silent truncation: the caller would otherwise see
		// a complete-looking list that is missing items.
		return out, fmt.Errorf("%s: gave up after %d pages and the response still has a nextPageToken — the collection is too large to list", fullURL, maxPages)
	}
	return out, nil
}

// maxPages caps ListAll; with page sizes of 1,000+ it is never reached in practice.
const maxPages = 100

// Error turns a non-2xx response into an error, decoding Google's
// {"error": {"message", "status"}} body when present.
func (c *Client) Error(status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("%s %d (%s): %s", c.API, status, e.Error.Status, e.Error.Message)
	}
	return fmt.Errorf("%s %d: %s", c.API, status, Snippet(body))
}

// Retryable reports whether a failed attempt may be repeated: transport
// failures (status 0), 429 and transient 5xx.
func Retryable(status int, err error) bool {
	if err != nil && status == 0 {
		return true
	}
	return status == http.StatusTooManyRequests ||
		status == http.StatusInternalServerError ||
		status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

// WaitBackoff sleeps before retry number attempt (2, 3, ...): linear backoff,
// or Retry-After seconds capped at one minute.
func WaitBackoff(ctx context.Context, attempt int, retryAfter int64) error {
	d := RetryBackoff * time.Duration(attempt-1)
	if retryAfter > 0 {
		d = min(time.Duration(retryAfter)*time.Second, time.Minute)
	}
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// ParseRetryAfter reads a delay-seconds Retry-After header (0 if absent).
func ParseRetryAfter(h string) int64 {
	if n, err := strconv.ParseInt(strings.TrimSpace(h), 10, 64); err == nil && n > 0 {
		return n
	}
	return 0
}

// JoinURL concatenates base and path with exactly one slash between them.
func JoinURL(base, path string) string {
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimLeft(path, "/")
}

// Snippet truncates a response body for error messages.
func Snippet(data []byte) string {
	s := string(data)
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	return s
}
