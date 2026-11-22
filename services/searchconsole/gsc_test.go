package searchconsole

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

func withTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })
}

func testClient() *gscClient {
	return &gscClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "Search Console API")}
}

func TestEncodeSiteURL(t *testing.T) {
	cases := map[string]string{
		"https://www.example.com/": "https:%2F%2Fwww.example.com%2F",
		"sc-domain:example.com":    "sc-domain:example.com",
		"https://xn--v.test/x y/":  "https:%2F%2Fxn--v.test%2Fx%20y%2F",
		"http://example.com/":      "http:%2F%2Fexample.com%2F",
	}
	for in, want := range cases {
		if got := encodeSiteURL(in); got != want {
			t.Errorf("encodeSiteURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func stubSites(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sites" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(`{"siteEntry":[
			{"siteUrl":"https://www.example.com/","permissionLevel":"siteOwner"},
			{"siteUrl":"sc-domain:example.net","permissionLevel":"siteOwner"}
		]}`))
	})
}

func TestResolveSiteVariants(t *testing.T) {
	stubSites(t)
	c := testClient()
	cases := map[string]string{
		"https://www.example.com/": "https://www.example.com/",
		"https://www.example.com":  "https://www.example.com/",
		"www.example.com":          "https://www.example.com/",
		"example.com":              "https://www.example.com/",
		"example.net":              "sc-domain:example.net",
		"sc-domain:example.net":    "sc-domain:example.net",
	}
	for in, want := range cases {
		got, err := c.resolveSite(context.Background(), in)
		if err != nil || got != want {
			t.Errorf("resolveSite(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := c.resolveSite(context.Background(), "unknown.org"); err == nil {
		t.Error("unknown site should error")
	} else if !strings.Contains(err.Error(), "Available:") {
		t.Errorf("error should list available properties: %v", err)
	}
}

func TestResolveSiteSingle(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"siteEntry":[{"siteUrl":"sc-domain:solo.dev","permissionLevel":"siteOwner"}]}`))
	})
	c := testClient()
	got, err := c.resolveSite(context.Background(), "")
	if err != nil || got != "sc-domain:solo.dev" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestResolveSiteEmptyMultiErrors(t *testing.T) {
	stubSites(t)
	c := testClient()
	if _, err := c.resolveSite(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "2 properties") {
		t.Errorf("multi-property with empty site should error with count, got %v", err)
	}
}

func TestAnalyticsQueryBodyAndPath(t *testing.T) {
	var gotPath, gotBody string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		w.Write([]byte(`{"rows":[{"keys":["2026-10-20"],"clicks":120.0,"impressions":4000.0,"ctr":0.03,"position":7.26}],"responseAggregationType":"auto"}`))
	})
	c := testClient()
	r, err := c.analyticsQuery(context.Background(), "https://www.example.com/", analyticsQueryBody{
		StartDate: "2026-10-01", EndDate: "2026-10-20",
		Dimensions: []string{"date"}, RowLimit: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := "/sites/https:%2F%2Fwww.example.com%2F/searchAnalytics/query"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatal(err)
	}
	if sent["startDate"] != "2026-10-01" || sent["rowLimit"] != float64(500) {
		t.Errorf("body = %s", gotBody)
	}
	if len(r.Rows) != 1 || r.Rows[0].Clicks != 120 {
		t.Errorf("decoded %+v", r)
	}
}

func TestAnalyticsBodyDefaultsAndFilters(t *testing.T) {
	body, err := analyticsBody(analyticsArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if body.RowLimit != 1000 || len(body.Dimensions) != 1 || body.Dimensions[0] != "date" {
		t.Errorf("defaults wrong: %+v", body)
	}
	if body.StartDate == "" || body.EndDate == "" {
		t.Error("preset dates must be materialized")
	}
	if body.Type != "" {
		t.Errorf("type should stay empty for default web, got %q", body.Type)
	}

	body, err = analyticsBody(analyticsArgs{
		Dimensions: []string{"query", "page"},
		Filters: []analyticsFilter{
			{Dimension: "device", Expression: "MOBILE", Operator: "equals"},
			{Dimension: "query", Expression: "loan", Operator: "includingRegex"},
		},
		RowLimit: 99999, StartRow: -5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if body.RowLimit != 25000 {
		t.Errorf("rowLimit should clamp to 25000, got %d", body.RowLimit)
	}
	if body.StartRow != 0 {
		t.Errorf("negative startRow should clamp to 0, got %d", body.StartRow)
	}
	if len(body.DimensionFilterGroups) != 1 || body.DimensionFilterGroups[0].GroupType != "and" || len(body.DimensionFilterGroups[0].Filters) != 2 {
		t.Errorf("filter groups wrong: %+v", body.DimensionFilterGroups)
	}
}

func TestAnalyticsBodyDateErrors(t *testing.T) {
	if _, err := analyticsBody(analyticsArgs{StartDate: "2026-03-02", EndDate: "2026-03-01"}); err == nil {
		t.Error("inverted dates should error")
	}
	if _, err := analyticsBody(analyticsArgs{DateRange: "FOREVER"}); err == nil {
		t.Error("invalid preset should error")
	}
}

func TestRenderAnalytics(t *testing.T) {
	body := analyticsQueryBody{StartDate: "2026-09-27", EndDate: "2026-10-26", Dimensions: []string{"query"}}
	r := analyticsResponse{Rows: []analyticsRow{
		{Keys: []string{"vay|tien"}, Clicks: 120, Impressions: 4000, Ctr: 0.03, Position: 7.26},
		{Keys: []string{"homie"}, Clicks: 5, Impressions: 100, Ctr: 0.05, Position: 12},
	}}
	out := renderAnalytics("https://www.example.com/", body, r, 1)
	if !strings.Contains(out, `vay\|tien`) {
		t.Error("pipes in cells must be escaped")
	}
	if !strings.Contains(out, "(1 more rows omitted") {
		t.Error("truncation note missing")
	}
	if !strings.Contains(out, "3.00%") || !strings.Contains(out, "7.3") {
		t.Error("metric formatting wrong")
	}
	empty := renderAnalytics("https://www.example.com/", body, analyticsResponse{}, 100)
	if !strings.Contains(empty, "No data returned") {
		t.Error("empty result message missing")
	}
}

func TestSaveAnalyticsCSVAndFile(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/out/queries.csv"
	body := analyticsQueryBody{StartDate: "2026-09-27", EndDate: "2026-10-26", Dimensions: []string{"query"}}
	r := analyticsResponse{Rows: []analyticsRow{
		{Keys: []string{`say "hi", now`}, Clicks: 3, Impressions: 10, Ctr: 0.3, Position: 4.5},
	}}
	out, err := saveAnalyticsCSV(p, "https://www.example.com/", body, r)
	if err != nil || !strings.Contains(out, "1 data rows + header") || !strings.Contains(out, p) {
		t.Errorf("summary wrong: %s", out)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := "query,clicks,impressions,ctr,position\n\"say \"\"hi\"\", now\",3,10,0.300000,4.50\n"
	if string(data) != want {
		t.Errorf("csv = %q, want %q", data, want)
	}
}

func TestSitemapDecode(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sites/sc-domain:example.net/sitemaps" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`{"sitemap":[{"path":"https://example.net/sitemap.xml","type":"SITEMAP","lastSubmitted":"2026-10-01T10:00:00.000Z","isSitemapsIndexed":true,"warnings":"1","errors":"0","isPending":false,"contents":[{"type":"web","submitted":"100","indexed":80}]}]}`))
	})
	c := testClient()
	maps, err := c.listSitemaps(context.Background(), "sc-domain:example.net", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(maps) != 1 || maps[0].Contents[0].Indexed != 80 || maps[0].Warnings != 1 {
		t.Errorf("decoded %+v", maps)
	}
}

func TestRetryOnServerErrorPOST(t *testing.T) {
	old := googleapi.RetryBackoff
	googleapi.RetryBackoff = time.Millisecond
	t.Cleanup(func() { googleapi.RetryBackoff = old })

	var calls int32
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":{"message":"backend error","status":"UNAVAILABLE"}}`))
			return
		}
		w.Write([]byte(`{"rows":[{"keys":["q"],"clicks":1,"impressions":2,"ctr":0.5,"position":3}]}`))
	})
	c := testClient()
	r, err := c.analyticsQuery(context.Background(), "https://www.example.com/", analyticsQueryBody{StartDate: "2026-10-01", EndDate: "2026-10-02"})
	if err != nil {
		t.Fatalf("retry should have recovered, got %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if len(r.Rows) != 1 {
		t.Errorf("decoded %+v", r)
	}
}

func TestInspectionDecode(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = data
		w.Write([]byte(`{"inspectionResult":{
			"inspectionResultLink":"https://search.google.com/u/0/search-console/inspect?resource_id=...",
			"indexStatusResult":{"verdict":"PASS","coverageState":"Submitted and indexed","robotsTxtState":"ALLOWED","indexingState":"INDEXING_ALLOWED","lastCrawlTime":"2026-10-20T05:06:07.000Z","pageFetchState":"SUCCESSFUL","googleCanonical":"https://www.example.com/a","userCanonical":"https://www.example.com/a","sitemap":["https://www.example.com/sitemap.xml"],"crawledAs":"MOBILE"},
			"mobileUsabilityResult":{"verdict":"PASS"},
			"richResultsResult":{"verdict":"PARTIAL","detectedItems":[{"richResultType":"FAQ","items":[{"name":"What is X?","issues":[{"issueMessage":"Missing field \"acceptedAnswer\"","severity":"ERROR"}]}]}]}
		}}`))
	})
	oldInspect := inspectPath
	inspectPath = apiBase + "/inspect"
	t.Cleanup(func() { inspectPath = oldInspect })
	c := testClient()
	res, err := c.urlInspect(context.Background(), "https://www.example.com/", "https://www.example.com/a", "vi")
	if err != nil {
		t.Fatal(err)
	}
	if res.IndexStatusResult == nil || res.IndexStatusResult.Verdict != "PASS" || res.IndexStatusResult.CoverageState != "Submitted and indexed" {
		t.Errorf("index status decoded wrong: %+v", res.IndexStatusResult)
	}
	if res.RichResultsResult == nil || len(res.RichResultsResult.DetectedItems) != 1 || res.RichResultsResult.DetectedItems[0].RichResultType != "FAQ" {
		t.Errorf("rich results decoded wrong: %+v", res.RichResultsResult)
	}
	out := renderInspection(res)
	if !strings.Contains(out, "Index status: verdict=PASS") || !strings.Contains(out, "Rich results: verdict=PARTIAL") ||
		!strings.Contains(out, `ERROR: Missing field "acceptedAnswer" (What is X?)`) {
		t.Errorf("rendering wrong:\n%s", out)
	}
}

func TestListSitemapsOfIndex(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("sitemapIndex"); got != "https://www.example.com/sitemap_index.xml" {
			t.Errorf("sitemapIndex = %q", got)
		}
		w.Write([]byte(`{"sitemap":[{"path":"https://www.example.com/post-sitemap.xml"}]}`))
	})
	maps, err := testClient().listSitemaps(context.Background(), "https://www.example.com/", "https://www.example.com/sitemap_index.xml")
	if err != nil || len(maps) != 1 || maps[0].Path != "https://www.example.com/post-sitemap.xml" {
		t.Fatalf("listSitemaps = %+v, %v", maps, err)
	}
}

func TestRenderInspections(t *testing.T) {
	var ok, bad inspectionResult
	if err := json.Unmarshal([]byte(`{"indexStatusResult":{"verdict":"PASS","coverageState":"Submitted and indexed","pageFetchState":"SUCCESSFUL"}}`), &ok); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"indexStatusResult":{"verdict":"NEUTRAL","coverageState":"Crawled - currently not indexed","robotsTxtState":"DISALLOWED","pageFetchState":"BLOCKED_ROBOTS_TXT","googleCanonical":"https://x/b","userCanonical":"https://x/a"}}`), &bad); err != nil {
		t.Fatal(err)
	}
	out := renderInspections("https://x/", []urlInspection{
		{url: "https://x/ok", result: ok},
		{url: "https://x/a", result: bad},
		{url: "https://x/err", err: errors.New("quota | exceeded")},
	})
	for _, want := range []string{
		"Inspected 3 URL(s) of https://x/: 1 indexed without issues, 1 blocked by robots.txt, 1 canonical mismatch, 1 fetch, 1 not indexed, 1 failed.",
		"| https://x/ok | PASS | Submitted and indexed | — |",
		"not indexed: Crawled - currently not indexed; canonical mismatch: Google chose https://x/b; blocked by robots.txt; fetch: BLOCKED_ROBOTS_TXT",
		`| https://x/err | ERROR | — | quota \| exceeded |`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestWriteCalls(t *testing.T) {
	var got []string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.EscapedPath())
		w.WriteHeader(http.StatusNoContent) // sites.add/delete return an empty 204
	})
	c, ctx := testClient(), context.Background()
	for _, err := range []error{
		c.addSite(ctx, "https://www.example.com/"),
		c.deleteSite(ctx, "sc-domain:example.com"),
		c.submitSitemap(ctx, "https://www.example.com/", "https://www.example.com/sitemap.xml"),
		c.deleteSitemap(ctx, "https://www.example.com/", "https://www.example.com/sitemap.xml"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"PUT /sites/https:%2F%2Fwww.example.com%2F",
		"DELETE /sites/sc-domain:example.com",
		"PUT /sites/https:%2F%2Fwww.example.com%2F/sitemaps/https:%2F%2Fwww.example.com%2Fsitemap.xml",
		"DELETE /sites/https:%2F%2Fwww.example.com%2F/sitemaps/https:%2F%2Fwww.example.com%2Fsitemap.xml",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
