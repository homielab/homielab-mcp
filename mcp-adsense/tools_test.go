package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeAccount(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"  ":                     "",
		"pub-123":                "accounts/pub-123",
		"accounts/pub-123":       "accounts/pub-123",
		"accounts/accounts/x":    "accounts/accounts/x",
		"/accounts/pub-9":        "accounts/pub-9",
		" pub-1234567890123456 ": "accounts/pub-1234567890123456",
	}
	for in, want := range cases {
		if got := normalizeAccount(in); got != want {
			t.Errorf("normalizeAccount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeAdClient(t *testing.T) {
	if got := normalizeAdClient("accounts/pub-1", "ca-pub-1"); got != "accounts/pub-1/adclients/ca-pub-1" {
		t.Errorf("got %q", got)
	}
	if got := normalizeAdClient("accounts/pub-1", "accounts/pub-1/adclients/ca-pub-1"); got != "accounts/pub-1/adclients/ca-pub-1" {
		t.Errorf("full name should pass through, got %q", got)
	}
}

// queryClient avoids any network access: an explicit accountId short-circuits
// resolveAccount.
func queryClient() *adsenseClient { return &adsenseClient{} }

func TestReportQueryDefaults(t *testing.T) {
	acc, q, err := reportQuery(t.Context(), queryClient(), reportArgs{AccountID: "pub-123"}, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if acc != "accounts/pub-123" {
		t.Fatalf("account = %q", acc)
	}
	if got := q.Get("dateRange"); got != "LAST_30_DAYS" {
		t.Errorf("dateRange = %q, want LAST_30_DAYS", got)
	}
	if got := q["metrics"]; len(got) != 1 || got[0] != "ESTIMATED_EARNINGS" {
		t.Errorf("default metrics = %v", got)
	}
	if got := q.Get("limit"); got != "100000" {
		t.Errorf("limit = %q", got)
	}
	if got := q.Get("languageCode"); got != "en" {
		t.Errorf("languageCode = %q", got)
	}
}

func TestReportQueryDateRangeVariants(t *testing.T) {
	for _, dr := range []string{"TODAY", "YESTERDAY", "MONTH_TO_DATE", "YEAR_TO_DATE", "LAST_7_DAYS", "last_30_days"} {
		_, q, err := reportQuery(t.Context(), queryClient(), reportArgs{AccountID: "pub-1", DateRange: dr}, 10)
		if err != nil {
			t.Fatalf("%s: %v", dr, err)
		}
		if got := q.Get("dateRange"); got != upper(dr) {
			t.Errorf("dateRange = %q, want %q", got, upper(dr))
		}
	}
}

func upper(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'a' && out[i] <= 'z' {
			out[i] -= 32
		}
	}
	return string(out)
}

func TestReportQueryCustomDates(t *testing.T) {
	_, q, err := reportQuery(t.Context(), queryClient(), reportArgs{
		AccountID: "pub-1", DateRange: "CUSTOM", StartDate: "2026-01-02", EndDate: "2026-03-04",
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"dateRange": "CUSTOM", "startDate.year": "2026", "startDate.month": "01", "startDate.day": "02",
		"endDate.year": "2026", "endDate.month": "03", "endDate.day": "04",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestReportQueryDatesImplyCustom(t *testing.T) {
	_, q, err := reportQuery(t.Context(), queryClient(), reportArgs{
		AccountID: "pub-1", StartDate: "2026-01-02", EndDate: "2026-03-04",
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("dateRange") != "CUSTOM" {
		t.Errorf("dates without dateRange should imply CUSTOM, got %q", q.Get("dateRange"))
	}
}

func TestReportQueryErrors(t *testing.T) {
	if _, _, err := reportQuery(t.Context(), queryClient(), reportArgs{AccountID: "pub-1", DateRange: "CUSTOM"}, 10); err == nil {
		t.Error("CUSTOM without dates should error")
	}
	if _, _, err := reportQuery(t.Context(), queryClient(), reportArgs{AccountID: "pub-1", DateRange: "FOREVER"}, 10); err == nil {
		t.Error("invalid dateRange should error")
	}
}

func TestReportQueryPassthrough(t *testing.T) {
	_, q, err := reportQuery(t.Context(), queryClient(), reportArgs{
		AccountID:         "pub-1",
		DateRange:         "LAST_7_DAYS",
		Dimensions:        []string{"DOMAIN_NAME", "COUNTRY_NAME"},
		Metrics:           []string{"ESTIMATED_EARNINGS", "CLICKS"},
		Filters:           []string{"DOMAIN_NAME==example.com"},
		OrderBy:           []string{"-metric.ESTIMATED_EARNINGS"}, // legacy form is normalized
		Offset:            25,
		CurrencyCode:      "EUR",
		ReportingTimeZone: "GOOGLE_TIME_ZONE",
		LanguageCode:      "vi",
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"dimensions":        "DOMAIN_NAME", // first of two
		"metrics":           "ESTIMATED_EARNINGS",
		"filters":           "DOMAIN_NAME==example.com",
		"orderBy":           "-ESTIMATED_EARNINGS",
		"offset":            "25",
		"currencyCode":      "EUR",
		"reportingTimeZone": "GOOGLE_TIME_ZONE",
		"languageCode":      "vi",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if len(q["dimensions"]) != 2 || len(q["metrics"]) != 2 {
		t.Errorf("repeated params lost: %v %v", q["dimensions"], q["metrics"])
	}
}

func TestRenderReport(t *testing.T) {
	r := reportResult{
		TotalMatchedRows: "3",
		StartDate:        apiDate{2026, 9, 27},
		EndDate:          apiDate{2026, 10, 26},
		Headers: []reportHeader{
			{Name: "DATE", Type: "DIMENSION"},
			{Name: "Estimated earnings", Type: "METRIC_CURRENCY", CurrencyCode: "USD"},
			{Name: "Clicks", Type: "METRIC_TALLY"},
		},
		Rows:     [][]string{{"2026-09-27", "10.34", "883"}, {"2026-09-28", "11.00", "935"}, {"2026-09-29", "10.14", "843"}},
		Totals:   []string{"All", "31.48", "2661"},
		Averages: []string{"Average", "10.49", "887"},
	}
	out := renderReport("accounts/pub-1", r, 2)

	wantHeader := "| DATE | Estimated earnings (USD) | Clicks |"
	if !contains(out, wantHeader) {
		t.Errorf("missing currency-suffixed header %q in:\n%s", wantHeader, out)
	}
	if !contains(out, "(1 more rows omitted") {
		t.Error("truncation note missing")
	}
	if !contains(out, "| 2026-09-27 | 10.34 | 883 |") {
		t.Error("first row missing")
	}
	if contains(out, "2026-09-29") {
		t.Error("row beyond maxRows should be omitted")
	}
	if !contains(out, "**Totals:** All | 31.48 | 2661") {
		t.Error("totals line missing")
	}
	if !contains(out, "**Averages:** Average | 10.49 | 887") {
		t.Error("averages line missing")
	}
}

func TestRenderReportSmallNoTruncation(t *testing.T) {
	r := reportResult{
		Headers: []reportHeader{{Name: "DATE", Type: "DIMENSION"}},
		Rows:    [][]string{{"2026-09-27"}},
	}
	out := renderReport("accounts/pub-1", r, 50)
	if contains(out, "more rows omitted") || contains(out, "**Totals:**") {
		t.Errorf("single-row report should have no truncation note or totals:\n%s", out)
	}
}

func TestPresetTable(t *testing.T) {
	if len(reportPresets) < 15 {
		t.Errorf("expected a rich preset table, got %d", len(reportPresets))
	}
	for name, p := range reportPresets {
		if len(p.dimensions) == 0 || len(p.metrics) == 0 || len(p.orderBy) == 0 || p.title == "" || p.defaultLimit <= 0 {
			t.Errorf("preset %q incompletely defined: %+v", name, p)
		}
	}
	if !contains(presetNames, "sites") || !contains(presetNames, "pages") {
		t.Errorf("presetNames should list core presets, got %q", presetNames)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestSavedReportQuery(t *testing.T) {
	// no overrides → no params (saved report's own default applies)
	q, err := savedReportQuery(savedReportArgs{SavedReportID: "42"})
	if err != nil || len(q) != 0 {
		t.Fatalf("empty overrides should yield no params, got %v (%v)", q, err)
	}
	// explicit date range
	q, err = savedReportQuery(savedReportArgs{DateRange: "LAST_7_DAYS"})
	if err != nil || q.Get("dateRange") != "LAST_7_DAYS" {
		t.Fatalf("got %v (%v)", q, err)
	}
	// dates imply custom
	q, err = savedReportQuery(savedReportArgs{StartDate: "2026-01-02", EndDate: "2026-03-04"})
	if err != nil || q.Get("dateRange") != "CUSTOM" || q.Get("startDate.month") != "01" || q.Get("endDate.day") != "04" {
		t.Fatalf("got %v (%v)", q, err)
	}
	// custom without dates → error
	if _, err = savedReportQuery(savedReportArgs{DateRange: "CUSTOM"}); err == nil {
		t.Fatal("CUSTOM without dates should error")
	}
}

func TestResolveSavedReport(t *testing.T) {
	c := &adsenseClient{}
	name, err := c.resolveSavedReport(t.Context(), "accounts/pub-1", "42")
	if err != nil || name != "accounts/pub-1/reports/saved/42" {
		t.Fatalf("got %q (%v)", name, err)
	}
	if name, err = c.resolveSavedReport(t.Context(), "", "accounts/pub-9/reports/saved/7"); err != nil || name != "accounts/pub-9/reports/saved/7" {
		t.Fatalf("full name passthrough failed: %q (%v)", name, err)
	}
	if _, err = c.resolveSavedReport(t.Context(), "accounts/pub-1", " "); err == nil {
		t.Fatal("empty id should error")
	}
}

func TestFormatRawItems(t *testing.T) {
	out := formatRawItems("2 thing(s)", []json.RawMessage{
		json.RawMessage(`{"name":"a","severity":"SEVERE"}`),
		json.RawMessage(`{"name":"b"}`),
	})
	if !contains(out, `Found 2 thing(s)`) || !contains(out, `{"name":"a","severity":"SEVERE"}`) {
		t.Errorf("unexpected render:\n%s", out)
	}
}

func TestInlineCSV(t *testing.T) {
	rows := func(n int) string {
		var b strings.Builder
		b.WriteString("DATE,EARNINGS\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "2026-01-%02d,1.00\n", i%28+1)
		}
		return b.String()
	}
	if got := inlineCSV(rows(150), 200); strings.Contains(got, "data rows shown") {
		t.Errorf("150 rows must pass through uncapped:\n%s", got)
	}
	got := inlineCSV(rows(500), 200)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 202 { // header + 200 rows + note
		t.Fatalf("capped output should hold header + 200 rows + note, got %d lines", len(lines))
	}
	if !contains(got, "200 of 500 data rows shown") {
		t.Errorf("cap note missing:\n%s", got)
	}
}

func TestReportQueryLastMonth(t *testing.T) {
	_, q, err := reportQuery(t.Context(), queryClient(), reportArgs{AccountID: "pub-1", DateRange: "LAST_MONTH"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("dateRange") != "LAST_MONTH" {
		t.Errorf("dateRange = %q, want LAST_MONTH", q.Get("dateRange"))
	}
}

func TestReportQueryOneSidedDates(t *testing.T) {
	if _, _, err := reportQuery(t.Context(), queryClient(), reportArgs{AccountID: "pub-1", StartDate: "2026-01-02"}, 10); err == nil {
		t.Error("startDate without endDate should error instead of being ignored")
	}
	if _, _, err := reportQuery(t.Context(), queryClient(), reportArgs{AccountID: "pub-1", StartDate: "junk", EndDate: "junk2"}, 10); err == nil {
		t.Error("malformed dates should error instead of being ignored")
	}
	// explicit dates override a preset
	_, q, err := reportQuery(t.Context(), queryClient(), reportArgs{AccountID: "pub-1", DateRange: "LAST_30_DAYS", StartDate: "2026-01-02", EndDate: "2026-03-04"}, 10)
	if err != nil || q.Get("dateRange") != "CUSTOM" {
		t.Fatalf("dates should override dateRange, got %v (%v)", q, err)
	}
}

func TestSavedReportQueryOneSidedDates(t *testing.T) {
	if _, err := savedReportQuery(savedReportArgs{StartDate: "2026-01-02"}); err == nil {
		t.Error("startDate without endDate should error instead of being silently ignored")
	}
}
