package googleapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fastRetries(t *testing.T) {
	old := RetryBackoff
	RetryBackoff = time.Millisecond
	t.Cleanup(func() { RetryBackoff = old })
}

func TestDoRetriesAndResendsBody(t *testing.T) {
	fastRetries(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"a":1}` {
			t.Errorf("attempt %d body = %q", atomic.LoadInt32(&calls)+1, body)
		}
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(srv.Client(), "Test API")
	out, status, err := c.Do(context.Background(), http.MethodPost, srv.URL, map[string]int{"a": 1})
	if err != nil || status != 200 || string(out) != `{"ok":true}` {
		t.Fatalf("Do = %q, %d, %v", out, status, err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestDoClientErrorNotRetried(t *testing.T) {
	fastRetries(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"nope","status":"PERMISSION_DENIED"}}`))
	}))
	defer srv.Close()

	_, status, err := New(srv.Client(), "Test API").Do(context.Background(), http.MethodGet, srv.URL, nil)
	if status != 403 || err == nil || err.Error() != "Test API 403 (PERMISSION_DENIED): nope" {
		t.Errorf("Do = %d, %v", status, err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestErrorFallsBackToSnippet(t *testing.T) {
	c := &Client{API: "Test API"}
	err := c.Error(502, []byte(strings.Repeat("x", 500)))
	if !strings.HasPrefix(err.Error(), "Test API 502: xxx") || !strings.HasSuffix(err.Error(), "...") {
		t.Errorf("err = %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := map[string]int64{"": 0, "0": 0, "5": 5, "abc": 0, " 12 ": 12, "-3": 0}
	for in, want := range cases {
		if got := ParseRetryAfter(in); got != want {
			t.Errorf("ParseRetryAfter(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestJoinURL(t *testing.T) {
	cases := [][3]string{
		{"https://api/v2", "/accounts/x", "https://api/v2/accounts/x"},
		{"https://api/v2/", "accounts/x", "https://api/v2/accounts/x"},
		{"https://api", "sites", "https://api/sites"},
		{"http://127.0.0.1:1", "/accounts/x", "http://127.0.0.1:1/accounts/x"},
	}
	for _, c := range cases {
		if got := JoinURL(c[0], c[1]); got != c[2] {
			t.Errorf("JoinURL(%q,%q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestListAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pageSize") != "2" {
			t.Errorf("query lost: %s", r.URL)
		}
		switch r.URL.Query().Get("pageToken") {
		case "":
			w.Write([]byte(`{"items":[1,2],"nextPageToken":"p2"}`))
		case "p2":
			w.Write([]byte(`{"items":[3]}`))
		}
	}))
	defer srv.Close()
	q := url.Values{"pageSize": {"2"}}
	got, err := ListAll[int](context.Background(), New(srv.Client(), "Test API"), srv.URL+"/x", q, "items")
	if err != nil || len(got) != 3 || got[2] != 3 {
		t.Fatalf("ListAll = %v, %v", got, err)
	}
	if q.Get("pageToken") != "" {
		t.Error("ListAll must not modify q")
	}
}

func TestDoHeaderAndDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/a-gzip" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"errors":[{"code":"X"}]}`))
	}))
	defer srv.Close()

	c := New(srv.Client(), "Test API")
	c.DecodeError = func(status int, body []byte) error { return fmt.Errorf("custom %d %s", status, body) }
	_, status, err := c.DoHeader(context.Background(), http.MethodGet, srv.URL, nil, http.Header{"Accept": {"application/a-gzip"}})
	if status != http.StatusConflict || err == nil || err.Error() != `custom 409 {"errors":[{"code":"X"}]}` {
		t.Errorf("DoHeader = %d, %v", status, err)
	}
}
