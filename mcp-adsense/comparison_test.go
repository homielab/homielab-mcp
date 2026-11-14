package main

import (
	"strings"
	"testing"
)

func TestParseMetricCell(t *testing.T) {
	cases := map[string]float64{
		"$1,234.56": 1234.56,
		"59.91%":    59.91,
		"0.519":     0.519,
		" 10.34 ":   10.34,
		"395398":    395398,
		"":          0,
		"garbage":   0,
		"-3.5":      -3.5,
	}
	for in, want := range cases {
		if got := parseMetricCell(in); got != want {
			t.Errorf("parseMetricCell(%q) = %v, want %v", in, got, want)
		}
	}
}

func comparisonFixtures() (reportResult, reportResult) {
	prev := reportResult{
		Headers: []reportHeader{
			{Name: "DOMAIN_NAME", Type: "DIMENSION"},
			{Name: "Estimated earnings", Type: "METRIC_CURRENCY"},
			{Name: "Impressions", Type: "METRIC_TALLY"},
		},
		Rows: [][]string{
			{"site-a.com", "$100.00", "1000"},
			{"site-b.com", "$10.00", "100"},
		},
	}
	curr := reportResult{
		Headers: prev.Headers,
		Rows: [][]string{
			{"site-b.com", "$50.00", "300"},  // Δ earnings +40 → biggest change
			{"site-a.com", "$90.00", "1200"}, // Δ earnings -10
			{"site-c.com", "$5.00", "50"},    // new key, prev zeros
		},
	}
	return prev, curr
}

func TestMergeReportPeriods(t *testing.T) {
	prev, curr := comparisonFixtures()
	merged := mergeReportPeriods(prev, curr, 1)
	if len(merged) != 3 {
		t.Fatalf("merged %d rows, want 3", len(merged))
	}
	if merged[0].keys[0] != "site-b.com" {
		t.Errorf("sorted by abs delta: first should be site-b.com, got %v", merged[0].keys)
	}
	if merged[0].prev[0] != 10 || merged[0].curr[0] != 50 {
		t.Errorf("site-b values wrong: %+v", merged[0])
	}
	// site-c absent from prev → zeros
	if merged[1].keys[0] != "site-c.com" && merged[2].keys[0] != "site-c.com" {
		t.Error("new key site-c.com missing")
	}
	for _, r := range merged {
		if r.keys[0] == "site-c.com" && (r.prev[0] != 0 || r.prev[1] != 0) {
			t.Errorf("new key should have zero prev values: %+v", r)
		}
	}
}

func TestRenderPeriodComparison(t *testing.T) {
	prev, curr := comparisonFixtures()
	merged := mergeReportPeriods(prev, curr, 1)
	out := renderPeriodComparison(merged, []string{"Estimated earnings", "Impressions"}, 2)
	if !strings.Contains(out, "| Key |") {
		t.Error("key column header missing")
	}
	if !strings.Contains(out, "Δ Estimated earnings") {
		t.Error("delta column missing")
	}
	if strings.Contains(out, "|") && !strings.Contains(out, "(1 more rows omitted") {
		t.Error("truncation note missing for 3 rows @ maxRows 2")
	}
}

func TestHasDateDimension(t *testing.T) {
	if !hasDateDimension([]string{"domain_name", "DATE"}) {
		t.Error("DATE should be detected")
	}
	if hasDateDimension([]string{"DOMAIN_NAME", "AD_UNIT_NAME"}) {
		t.Error("non-date dims should pass")
	}
}

func TestFmtMetric(t *testing.T) {
	cases := map[float64]string{
		3:     "3",    // used to render "3."
		100:   "100",  // used to render "100."
		59.9:  "59.9", // trailing zero trimmed
		59.91: "59.91",
		0:     "0",
		1234:  "1234",
	}
	for in, want := range cases {
		if got := fmtMetric(in); got != want {
			t.Errorf("fmtMetric(%v) = %q, want %q", in, got, want)
		}
	}
}
