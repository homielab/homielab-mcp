package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

func withTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := apiBase
	apiBase = srv.URL + "/v1"
	t.Cleanup(func() { apiBase = old })
}

func testClient() *admobClient {
	return &admobClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "AdMob API")}
}

var now = time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)

func TestNormalizeAccount(t *testing.T) {
	for in, want := range map[string]string{"": "", "pub-1": "accounts/pub-1", "accounts/pub-1": "accounts/pub-1", " /pub-2/ ": "accounts/pub-2"} {
		if got := normalizeAccount(in); got != want {
			t.Errorf("normalizeAccount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListAccountsPaginates(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/accounts" || r.URL.Query().Get("pageSize") != "20000" {
			t.Errorf("unexpected request %s", r.URL)
		}
		if r.URL.Query().Get("pageToken") == "" {
			w.Write([]byte(`{"account":[{"name":"accounts/pub-1","publisherId":"pub-1","reportingTimeZone":"America/Los_Angeles","currencyCode":"USD"}],"nextPageToken":"p2"}`))
			return
		}
		w.Write([]byte(`{"account":[{"name":"accounts/pub-2","publisherId":"pub-2"}]}`))
	})
	accs, err := testClient().listAccounts(context.Background())
	if err != nil || len(accs) != 2 || accs[0].CurrencyCode != "USD" || accs[1].PublisherID != "pub-2" {
		t.Fatalf("listAccounts = %+v, %v", accs, err)
	}
}

func TestAppLabel(t *testing.T) {
	var a app
	json.Unmarshal([]byte(`{"appId":"ca-app-pub-1~2","manualAppInfo":{"displayName":"Manual"},"linkedAppInfo":{"appStoreId":"com.x","displayName":"Store Name"}}`), &a)
	if a.label() != "Store Name" {
		t.Errorf("label = %q", a.label())
	}
	a.LinkedAppInfo = nil
	if a.label() != "Manual" {
		t.Errorf("label = %q", a.label())
	}
}

func TestBuildSpecDefaults(t *testing.T) {
	spec, err := buildSpec("network", reportArgs{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if spec.DateRange.StartDate.String() != "2026-09-28" || spec.DateRange.EndDate.String() != "2026-10-27" {
		t.Errorf("default range = %s → %s", spec.DateRange.StartDate, spec.DateRange.EndDate)
	}
	if strings.Join(spec.Dimensions, ",") != "DATE" || len(spec.Metrics) != len(defaultMetrics["network"]) || spec.MaxReportRows != 1000 {
		t.Errorf("spec = %+v", spec)
	}
	if len(spec.SortConditions) != 1 || spec.SortConditions[0] != (sortCondition{Dimension: "DATE", Order: "ASCENDING"}) {
		t.Errorf("sort = %+v", spec.SortConditions)
	}
}

func TestBuildSpecCustom(t *testing.T) {
	spec, err := buildSpec("mediation", reportArgs{
		StartDate: "2026-10-01", EndDate: "2026-10-07",
		Dimensions: []string{"ad_source", "country"}, Metrics: []string{"observed_ecpm", "estimated_earnings"},
		Filters: map[string][]string{"country": {"US", "VN"}}, SortBy: "observed_ecpm", SortOrder: "asc",
		MaxRows: 50, CurrencyCode: "vnd",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(spec)
	for _, want := range []string{
		`"startDate":{"year":2026,"month":10,"day":1}`,
		`"dimensions":["AD_SOURCE","COUNTRY"]`,
		`"metrics":["OBSERVED_ECPM","ESTIMATED_EARNINGS"]`,
		`"dimensionFilters":[{"dimension":"COUNTRY","matchesAny":{"values":["US","VN"]}}]`,
		`"sortConditions":[{"metric":"OBSERVED_ECPM","order":"ASCENDING"}]`,
		`"localizationSettings":{"currencyCode":"VND"}`,
		`"maxReportRows":50`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("spec JSON missing %s:\n%s", want, body)
		}
	}
}

func TestBuildSpecErrors(t *testing.T) {
	cases := map[string]reportArgs{
		"unknown dimension":       {Dimensions: []string{"AD_SOURCE"}}, // mediation-only
		"two time dims":           {Dimensions: []string{"DATE", "MONTH"}},
		"AD_TYPE incompatibility": {Dimensions: []string{"AD_TYPE"}, Metrics: []string{"AD_REQUESTS"}},
		"bad sort":                {SortBy: "COUNTRY"},
		"bad order":               {SortOrder: "up"},
		"too many rows":           {MaxRows: 100001},
		"bad dates":               {StartDate: "2026-10-05", EndDate: "2026-10-01"},
		"bad preset":              {DateRange: "LAST_90_DAYS"},
		"empty filter":            {Filters: map[string][]string{"COUNTRY": nil}},
		"bad currency":            {CurrencyCode: "DOLLAR"},
	}
	for name, args := range cases {
		if _, err := buildSpec("network", args, now); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// AD_TYPE with default metrics silently drops the incompatible ones.
	spec, err := buildSpec("network", reportArgs{Dimensions: []string{"AD_TYPE"}}, now)
	if err != nil || strings.Contains(strings.Join(spec.Metrics, ","), "AD_REQUESTS") {
		t.Errorf("AD_TYPE defaults: %v, %v", spec.Metrics, err)
	}
}

func TestPreviousWindow(t *testing.T) {
	s, e := previousWindow(apiDate{2026, 10, 1}, apiDate{2026, 10, 7})
	if s.String() != "2026-09-24" || e.String() != "2026-09-30" {
		t.Errorf("previousWindow = %s → %s", s, e)
	}
}

const sampleReport = `[
 {"header":{"dateRange":{},"localizationSettings":{"currencyCode":"USD"}}},
 {"row":{"dimensionValues":{"APP":{"value":"ca-app-pub-1~1","displayLabel":"Game | One"}},
   "metricValues":{"ESTIMATED_EARNINGS":{"microsValue":"6500000"},"IMPRESSIONS":{"integerValue":"1000"},"CLICKS":{"integerValue":"10"},"IMPRESSION_RPM":{"doubleValue":6.5},"MATCH_RATE":{"doubleValue":0.9},"AD_REQUESTS":{"integerValue":"2000"}}}},
 {"row":{"dimensionValues":{"APP":{"value":"ca-app-pub-1~2","displayLabel":"App Two"}},
   "metricValues":{"ESTIMATED_EARNINGS":{"microsValue":"1500000"},"IMPRESSIONS":{"integerValue":"1000"},"CLICKS":{"integerValue":"30"},"IMPRESSION_RPM":{"doubleValue":1.5},"MATCH_RATE":{"doubleValue":0.5},"AD_REQUESTS":{"integerValue":"2000"}}}},
 {"footer":{"matchingRowCount":"5"}}
]`

func TestReportDates(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"header":{}},{"row":{"dimensionValues":{"DATE":{"value":"20261022"},"MONTH":{"value":"202610"}},"metricValues":{}}},{"footer":{"matchingRowCount":"1"}}]`))
	})
	r, err := testClient().generateReport(context.Background(), "accounts/pub-1", "network", reportSpec{})
	if err != nil || r.Rows[0].DimensionValues["DATE"].Value != "2026-10-22" || r.Rows[0].DimensionValues["MONTH"].Value != "2026-10" {
		t.Errorf("dates = %+v, %v", r.Rows, err)
	}
}

func TestGenerateReport(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/accounts/pub-1/networkReport:generate" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"reportSpec":{"dateRange"`) {
			t.Errorf("body = %s", body)
		}
		w.Write([]byte(sampleReport))
	})
	spec, _ := buildSpec("network", reportArgs{Dimensions: []string{"APP"}, Metrics: []string{"ESTIMATED_EARNINGS", "IMPRESSIONS", "IMPRESSION_RPM", "MATCH_RATE"}}, now)
	r, err := testClient().generateReport(context.Background(), "accounts/pub-1", "network", spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 2 || r.Currency != "USD" || r.Matching != "5" {
		t.Fatalf("report = %+v", r)
	}
	out := renderReport("pub-1", r, 100)
	for _, want := range []string{
		"2 row(s) of 5 matching",
		"| APP | ESTIMATED_EARNINGS (USD) | IMPRESSIONS | IMPRESSION_RPM (USD) | MATCH_RATE |",
		`| Game \| One (ca-app-pub-1~1) | 6.50 | 1000 | 6.50 | 90.00% |`,
		"**Totals:** ESTIMATED_EARNINGS (USD) 8.00 · IMPRESSIONS 2000 · IMPRESSION_RPM (USD) 4.00",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	csv := reportCSV(r)
	if !strings.HasPrefix(csv, "APP,APP_LABEL,ESTIMATED_EARNINGS (USD),IMPRESSIONS,IMPRESSION_RPM (USD),MATCH_RATE\nca-app-pub-1~1,Game | One,6.5,1000,6.5,0.9\n") {
		t.Errorf("csv:\n%s", csv)
	}
}

func TestComparison(t *testing.T) {
	mk := func(rows ...[3]string) report {
		r := report{Spec: reportSpec{Dimensions: []string{"COUNTRY"}}}
		for _, x := range rows {
			r.Rows = append(r.Rows, reportRow{
				DimensionValues: map[string]dimensionValue{"COUNTRY": {Value: x[0]}},
				MetricValues:    map[string]metricValue{"ESTIMATED_EARNINGS": {MicrosValue: x[1]}, "IMPRESSIONS": {IntegerValue: x[2]}},
			})
		}
		return r
	}
	prev := mk([3]string{"US", "10000000", "2000"}, [3]string{"VN", "1000000", "1000"})
	curr := mk([3]string{"US", "8000000", "2000"}, [3]string{"VN", "1500000", "1000"}, [3]string{"JP", "5000000", "500"})
	rows := mergeReports(prev, curr)
	if len(rows) != 3 || rows[0].key != "JP" || rows[1].key != "US" || rows[2].key != "VN" {
		t.Fatalf("order = %+v", rows)
	}
	out := renderComparison([]string{"COUNTRY"}, rows, "USD", 50)
	for _, want := range []string{
		"| JP | 0.00 → 5.00 | new | 0 → 500 | new | 0.00 → 10.00 |",
		"| US | 10.00 → 8.00 | -20.0% | 2000 → 2000 | +0.0% | 5.00 → 4.00 |",
		"**Total:** earnings 11.00 → 14.50 (+31.8%)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestPresetsValid(t *testing.T) {
	for name, p := range presets {
		if _, err := buildSpec(p.kind, reportArgs{Dimensions: p.dims}, now); err != nil {
			t.Errorf("preset %s: %v", name, err)
		}
	}
}

func TestServerBuilds(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	registerTools(s)
	registerPrompts(s)
}
