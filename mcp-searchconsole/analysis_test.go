package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

// TestRetryRepostsBody locks down the bug where the POST payload reader was
// consumed by attempt 1, so attempt 2 sent an empty body.
func TestRetryRepostsBody(t *testing.T) {
	old := googleapi.RetryBackoff
	googleapi.RetryBackoff = time.Millisecond
	t.Cleanup(func() { googleapi.RetryBackoff = old })

	var bodies []string
	var calls int32
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(data))
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"message":"x","status":"INTERNAL"}}`))
			return
		}
		w.Write([]byte(`{}`))
	})
	c := testClient()
	if _, err := c.analyticsQuery(context.Background(), "https://www.example.com/", analyticsQueryBody{
		StartDate: "2026-10-01", EndDate: "2026-10-02", Dimensions: []string{"query"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(bodies))
	}
	if bodies[0] == "" || bodies[0] != bodies[1] {
		t.Errorf("retry must repost the identical body; got %q then %q", bodies[0], bodies[1])
	}
}

func TestValidateDimensions(t *testing.T) {
	if _, err := validateDimensions([]string{"query", "query"}); err == nil {
		t.Error("duplicate dimension should error")
	}
	if _, err := validateDimensions([]string{"keyword"}); err == nil {
		t.Error("unknown dimension should error")
	}
	got, err := validateDimensions([]string{"query", "", "page"})
	if err != nil || len(got) != 2 || got[0] != "query" || got[1] != "page" {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestCanonicalTypeAndDataState(t *testing.T) {
	if got, _ := canonicalType("GoogleNews"); got != "googleNews" {
		t.Errorf("canonicalType = %q", got)
	}
	if got, _ := canonicalType(""); got != "" {
		t.Errorf("empty type should stay empty, got %q", got)
	}
	if _, err := canonicalType("shopping"); err == nil {
		t.Error("unknown type should error")
	}
	if got, _ := canonicalDataState("ALL"); got != "all" {
		t.Errorf("dataState = %q", got)
	}
	if _, err := canonicalDataState("fresh"); err == nil {
		t.Error("unknown dataState should error")
	}
}

func TestAnalyticsBodyValidatedHourAutoDataState(t *testing.T) {
	body, err := analyticsBodyValidated(analyticsArgs{Dimensions: []string{"hour"}, RowLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if body.DataState != "all" {
		t.Errorf("hour dimension should auto-set dataState=all, got %q", body.DataState)
	}
}

func TestAnalyticsBodyValidatedErrors(t *testing.T) {
	if _, err := analyticsBodyValidated(analyticsArgs{Type: "shopping"}); err == nil {
		t.Error("bad type should error")
	}
	if _, err := analyticsBodyValidated(analyticsArgs{DataState: "fresh"}); err == nil {
		t.Error("bad dataState should error")
	}
	if _, err := analyticsBodyValidated(analyticsArgs{Filters: []analyticsFilter{{Dimension: "keyword", Expression: "x"}}}); err == nil {
		t.Error("bad filter dimension should error")
	}
	if _, err := analyticsBodyValidated(analyticsArgs{Filters: []analyticsFilter{{Dimension: "query", Operator: "regex", Expression: "x"}}}); err == nil {
		t.Error("bad filter operator should error")
	}
	if _, err := analyticsBodyValidated(analyticsArgs{Filters: []analyticsFilter{{Dimension: "query", Expression: "  "}}}); err == nil {
		t.Error("empty expression should error")
	}
}

func TestPreflightInspect(t *testing.T) {
	if err := preflightInspect("https://www.example.com/", "https://www.example.com/page"); err != nil {
		t.Errorf("matching host should pass: %v", err)
	}
	if err := preflightInspect("https://www.example.com/", "https://other.org/page"); err == nil {
		t.Error("mismatched host should error before burning quota")
	}
	if err := preflightInspect("sc-domain:example.com", "https://blog.example.com/x"); err != nil {
		t.Errorf("sc-domain covers subdomains: %v", err)
	}
	if err := preflightInspect("sc-domain:example.com", "not a url"); err == nil {
		t.Error("non-URL input should error")
	}
}

func TestMergeAnalyticsSortsByClickDelta(t *testing.T) {
	prev := analyticsResponse{Rows: []analyticsRow{
		{Keys: []string{"a"}, Clicks: 100, Impressions: 1000, Ctr: 0.1, Position: 5},
		{Keys: []string{"b"}, Clicks: 10, Impressions: 100, Ctr: 0.1, Position: 9},
	}}
	curr := analyticsResponse{Rows: []analyticsRow{
		{Keys: []string{"b"}, Clicks: 60, Impressions: 110, Ctr: 0.55, Position: 7},
		{Keys: []string{"a"}, Clicks: 90, Impressions: 900, Ctr: 0.1, Position: 6},
		{Keys: []string{"new"}, Clicks: 5, Impressions: 50, Ctr: 0.1, Position: 11},
	}}
	merged := mergeAnalytics(prev, curr)
	if len(merged) != 3 {
		t.Fatalf("merged %d rows, want 3", len(merged))
	}
	// |Δ|: b=50, a=10, new=5 → b first
	if merged[0].keys[0] != "b" {
		t.Errorf("expected b first (largest delta), got %v", merged[0].keys)
	}
	if merged[0].prevClicks != 10 || merged[0].clicks != 60 {
		t.Errorf("merge mismatched: %+v", merged[0])
	}
	// absent in prev → prevClicks 0
	if merged[2].keys[0] != "new" || merged[2].prevClicks != 0 {
		t.Errorf("new key handling wrong: %+v", merged[2])
	}
	out := renderComparison(merged, []string{"query"}, 2)
	if !strings.Contains(out, "+50") || !strings.Contains(out, "(1 more rows omitted") {
		t.Errorf("render wrong:\n%s", out)
	}
}

func TestQuickReportArgsAggregation(t *testing.T) {
	if cutAggregation([]string{"page"}) != "byPage" {
		t.Error("page cut should aggregate byPage")
	}
	if cutAggregation([]string{"query"}) != "" {
		t.Error("non-page cuts should keep auto aggregation")
	}
}

func TestRenderAnalyticsTotalsAndTruncationHint(t *testing.T) {
	body := analyticsQueryBody{StartDate: "2026-09-27", EndDate: "2026-10-26", Dimensions: []string{"query"}, RowLimit: 2}
	r := analyticsResponse{Rows: []analyticsRow{
		{Keys: []string{"a"}, Clicks: 10, Impressions: 100, Ctr: 0.1, Position: 3},
		{Keys: []string{"b"}, Clicks: 5, Impressions: 100, Ctr: 0.05, Position: 8},
	}}
	out := renderAnalytics("https://www.example.com/", body, r, 100)
	if !strings.Contains(out, "Totals of 2 returned rows: 15 clicks, 200 impressions (avg CTR 7.50%)") {
		t.Errorf("totals line wrong:\n%s", out)
	}
	if !strings.Contains(out, "likely truncated") {
		t.Error("truncation hint missing when rows == rowLimit")
	}
}

func TestRowRefinement(t *testing.T) {
	rows := []analyticsRow{
		{Keys: []string{"a"}, Clicks: 9, Impressions: 50, Ctr: 0.18, Position: 3},
		{Keys: []string{"b"}, Clicks: 5, Impressions: 900, Ctr: 0.005, Position: 14},
		{Keys: []string{"c"}, Clicks: 2, Impressions: 300, Ctr: 0.007, Position: 12},
		{Keys: []string{"d"}, Clicks: 1, Impressions: 40, Ctr: 0.025, Position: 18},
	}
	keys := func(rs []analyticsRow) string {
		var ks []string
		for _, r := range rs {
			ks = append(ks, r.Keys[0])
		}
		return strings.Join(ks, ",")
	}
	cases := []struct {
		f    rowRefinement
		want string
	}{
		{rowRefinement{}, "a,b,c,d"},
		{rowRefinement{SortBy: "impressions"}, "b,c,a,d"},
		{rowRefinement{SortBy: "position"}, "a,c,b,d"}, // best position first by default
		{rowRefinement{SortBy: "position", SortOrder: "desc"}, "d,b,c,a"},
		{rowRefinement{MinPosition: 11, MaxPosition: 20, MinImpressions: 100, SortBy: "impressions"}, "b,c"},
	}
	for _, c := range cases {
		if err := c.f.validate(); err != nil {
			t.Fatalf("%+v: %v", c.f, err)
		}
		if got := keys(c.f.apply(rows)); got != c.want {
			t.Errorf("%+v → %s, want %s", c.f, got, c.want)
		}
	}
	if keys(rows) != "a,b,c,d" {
		t.Error("apply must not reorder the input slice")
	}
	for _, bad := range []rowRefinement{{SortBy: "revenue"}, {SortOrder: "up"}, {MinPosition: 20, MaxPosition: 11}} {
		if bad.validate() == nil {
			t.Errorf("%+v should be rejected", bad)
		}
	}
}

func TestInspectionIssuesIndexedClean(t *testing.T) {
	var r inspectionResult
	if err := json.Unmarshal([]byte(`{"indexStatusResult":{"verdict":"PASS","robotsTxtState":"ALLOWED","pageFetchState":"SUCCESSFUL","googleCanonical":"https://x/a","userCanonical":"https://x/a"},"richResultsResult":{"verdict":"PASS"}}`), &r); err != nil {
		t.Fatal(err)
	}
	if got := inspectionIssues(r); len(got) != 0 {
		t.Errorf("clean indexed URL reported %v", got)
	}
	r.RichResultsResult.Verdict = "FAIL"
	if got := inspectionIssues(r); len(got) != 1 || got[0] != "rich results invalid" {
		t.Errorf("invalid rich results: got %v", got)
	}
}

func TestNormalizeNewProperty(t *testing.T) {
	ok := map[string]string{
		"https://www.example.com/":  "https://www.example.com/",
		"https://www.example.com":   "https://www.example.com/",
		" http://example.com/blog ": "http://example.com/blog/",
		"sc-domain:Example.com":     "sc-domain:example.com",
	}
	for in, want := range ok {
		if got, err := normalizeNewProperty(in); err != nil || got != want {
			t.Errorf("normalizeNewProperty(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "example.com", "ftp://example.com/", "sc-domain:", "sc-domain:https://x", "https://x/?a=1"} {
		if _, err := normalizeNewProperty(bad); err == nil {
			t.Errorf("normalizeNewProperty(%q) should fail", bad)
		}
	}
}

func TestScopeHint(t *testing.T) {
	err := scopeHint(errors.New("Search Console API 403 (PERMISSION_DENIED): Request had insufficient authentication scopes."))
	if !strings.Contains(err.Error(), "gsc_logout, then gsc_login") {
		t.Errorf("scope error should get a re-login hint, got %v", err)
	}
	plain := errors.New("Search Console API 404 (NOT_FOUND): x")
	if scopeHint(plain) != plain {
		t.Error("other errors must pass through")
	}
}

func TestMergeAnalyticsPrevOnlyKeyRenders(t *testing.T) {
	prev := analyticsResponse{Rows: []analyticsRow{
		{Keys: []string{"ghost query"}, Clicks: 40, Impressions: 400, Ctr: 0.1, Position: 8},
	}}
	curr := analyticsResponse{Rows: []analyticsRow{
		{Keys: []string{"live query"}, Clicks: 20, Impressions: 200, Ctr: 0.1, Position: 6},
	}}
	merged := mergeAnalytics(prev, curr)
	out := renderComparison(merged, []string{"query"}, 10)
	// A key present only in the baseline must still render with its key cell,
	// keeping every table row aligned with the header (1 dim + 8 metrics).
	if !strings.Contains(out, "ghost query") {
		t.Errorf("prev-only key vanished from the table:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "|") || strings.Contains(line, "---") {
			continue
		}
		if n := strings.Count(line, "|") - 1; n != 9 {
			t.Errorf("table row has %d columns, want 9: %s", n, line)
		}
	}
}
