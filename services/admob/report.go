package admob

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homielab/homielab-mcp/internal/daterange"
	"github.com/homielab/homielab-mcp/internal/render"
)

// ---- dimensions and metrics per report kind ----

var reportDims = map[string][]string{
	"network": {"DATE", "MONTH", "WEEK", "AD_UNIT", "APP", "AD_TYPE", "COUNTRY", "FORMAT", "PLATFORM",
		"MOBILE_OS_VERSION", "GMA_SDK_VERSION", "APP_VERSION_NAME", "SERVING_RESTRICTION"},
	"mediation": {"DATE", "MONTH", "WEEK", "AD_SOURCE", "AD_SOURCE_INSTANCE", "AD_UNIT", "APP", "MEDIATION_GROUP",
		"COUNTRY", "FORMAT", "PLATFORM", "MOBILE_OS_VERSION", "GMA_SDK_VERSION", "APP_VERSION_NAME", "SERVING_RESTRICTION"},
}

var reportMetrics = map[string][]string{
	"network": {"AD_REQUESTS", "CLICKS", "ESTIMATED_EARNINGS", "IMPRESSIONS", "IMPRESSION_CTR",
		"IMPRESSION_RPM", "MATCHED_REQUESTS", "MATCH_RATE", "SHOW_RATE"},
	"mediation": {"AD_REQUESTS", "CLICKS", "ESTIMATED_EARNINGS", "IMPRESSIONS", "IMPRESSION_CTR",
		"MATCHED_REQUESTS", "MATCH_RATE", "OBSERVED_ECPM", "AD_LOAD_LATENCY"},
}

var defaultMetrics = map[string][]string{
	"network":   {"ESTIMATED_EARNINGS", "IMPRESSIONS", "CLICKS", "IMPRESSION_RPM", "AD_REQUESTS", "MATCH_RATE", "SHOW_RATE"},
	"mediation": {"ESTIMATED_EARNINGS", "IMPRESSIONS", "CLICKS", "OBSERVED_ECPM", "AD_REQUESTS", "MATCH_RATE"},
}

var timeDims = []string{"DATE", "WEEK", "MONTH"}

// rateMetrics are 0–1 ratios shown as percentages; additiveMetrics can be summed.
var (
	rateMetrics     = []string{"IMPRESSION_CTR", "MATCH_RATE", "SHOW_RATE"}
	additiveMetrics = []string{"ESTIMATED_EARNINGS", "IMPRESSIONS", "CLICKS", "AD_REQUESTS", "MATCHED_REQUESTS"}
	moneyMetrics    = []string{"ESTIMATED_EARNINGS", "IMPRESSION_RPM", "OBSERVED_ECPM"}
)

func dimsHelp(kind string) string {
	return fmt.Sprintf("Dimensions: %s. Metrics: %s.", strings.Join(reportDims[kind], ", "), strings.Join(reportMetrics[kind], ", "))
}

// ---- tool arguments → reportSpec ----

type reportArgs struct {
	AccountID    string              `json:"accountId,omitempty" jsonschema:"Publisher id like pub-XXXX. Omit for the first accessible account."`
	DateRange    string              `json:"dateRange,omitempty" jsonschema:"TODAY, YESTERDAY, LAST_7_DAYS, LAST_30_DAYS (default), MONTH_TO_DATE, LAST_MONTH, YEAR_TO_DATE — in the account's reporting time zone."`
	StartDate    string              `json:"startDate,omitempty" jsonschema:"YYYY-MM-DD, with endDate; overrides dateRange."`
	EndDate      string              `json:"endDate,omitempty" jsonschema:"YYYY-MM-DD, with startDate; overrides dateRange."`
	Dimensions   []string            `json:"dimensions,omitempty" jsonschema:"Grouping dimensions (see tool description); default [\"DATE\"]. At most one of DATE, WEEK, MONTH."`
	Metrics      []string            `json:"metrics,omitempty" jsonschema:"Metrics (see tool description); default earnings, impressions, clicks, eCPM/RPM, requests, match rate."`
	Filters      map[string][]string `json:"filters,omitempty" jsonschema:"Keep rows whose dimension value is in the list, e.g. {\"COUNTRY\": [\"US\", \"VN\"], \"APP\": [\"ca-app-pub-…~…\"]}."`
	SortBy       string              `json:"sortBy,omitempty" jsonschema:"Metric or dimension to sort by; default the time dimension ascending, else ESTIMATED_EARNINGS descending."`
	SortOrder    string              `json:"sortOrder,omitempty" jsonschema:"asc or desc (default desc for metrics, asc for dimensions)."`
	MaxRows      int                 `json:"maxRows,omitempty" jsonschema:"1-100000 rows from the API (default 1000)."`
	CurrencyCode string              `json:"currencyCode,omitempty" jsonschema:"ISO 4217 code to convert earnings into; default the account currency."`
	SaveToPath   string              `json:"saveToPath,omitempty" jsonschema:"Write the full result as CSV to this file and return a preview (recommended above ~200 rows)."`
}

// buildSpec validates args for kind ("network"/"mediation") and returns the
// API spec. now is the current time in the account's reporting time zone.
func buildSpec(kind string, args reportArgs, now time.Time) (reportSpec, error) {
	var spec reportSpec
	start, end, err := daterange.Resolve(args.DateRange, args.StartDate, args.EndDate, now)
	if err != nil {
		return spec, err
	}
	spec.DateRange.StartDate, spec.DateRange.EndDate = toDate(start), toDate(end)

	dims, err := normalizeEnums("dimension", args.Dimensions, reportDims[kind])
	if err != nil {
		return spec, err
	}
	if len(args.Dimensions) == 0 {
		dims = []string{"DATE"}
	}
	var timeDim string
	for _, d := range dims {
		if slices.Contains(timeDims, d) {
			if timeDim != "" {
				return spec, fmt.Errorf("use only one of DATE, WEEK, MONTH (got %s and %s)", timeDim, d)
			}
			timeDim = d
		}
	}
	metrics, err := normalizeEnums("metric", args.Metrics, reportMetrics[kind])
	if err != nil {
		return spec, err
	}
	if len(metrics) == 0 {
		metrics = slices.Clone(defaultMetrics[kind])
	}
	if slices.Contains(dims, "AD_TYPE") {
		before := len(metrics)
		metrics = slices.DeleteFunc(metrics, func(m string) bool {
			return m == "AD_REQUESTS" || m == "MATCH_RATE" || m == "IMPRESSION_RPM"
		})
		if len(args.Metrics) > 0 && len(metrics) < before {
			return spec, fmt.Errorf("AD_TYPE cannot be combined with AD_REQUESTS, MATCH_RATE or IMPRESSION_RPM")
		}
	}
	spec.Dimensions, spec.Metrics = dims, metrics

	filterDims := make([]string, 0, len(args.Filters))
	for d := range args.Filters {
		filterDims = append(filterDims, d)
	}
	sort.Strings(filterDims)
	for _, d := range filterDims {
		nd, err := normalizeEnums("filter dimension", []string{d}, reportDims[kind])
		if err != nil {
			return spec, err
		}
		if len(args.Filters[d]) == 0 {
			return spec, fmt.Errorf("filter on %s needs at least one value", nd[0])
		}
		f := dimensionFilter{Dimension: nd[0]}
		f.MatchesAny.Values = args.Filters[d]
		spec.DimensionFilters = append(spec.DimensionFilters, f)
	}

	sc, err := sortFor(args.SortBy, args.SortOrder, dims, metrics, timeDim)
	if err != nil {
		return spec, err
	}
	spec.SortConditions = sc

	switch {
	case args.MaxRows < 0 || args.MaxRows > 100000:
		return spec, fmt.Errorf("maxRows must be 1-100000, got %d", args.MaxRows)
	case args.MaxRows == 0:
		spec.MaxReportRows = 1000
	default:
		spec.MaxReportRows = args.MaxRows
	}
	if cc := strings.ToUpper(strings.TrimSpace(args.CurrencyCode)); cc != "" {
		if len(cc) != 3 {
			return spec, fmt.Errorf("currencyCode must be a 3-letter ISO 4217 code, got %q", args.CurrencyCode)
		}
		spec.Localization = &localization{CurrencyCode: cc}
	}
	return spec, nil
}

// normalizeEnums upper-cases values and checks them against allowed.
func normalizeEnums(what string, in, allowed []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, v := range in {
		u := strings.ToUpper(strings.TrimSpace(v))
		if !slices.Contains(allowed, u) {
			return nil, fmt.Errorf("unknown %s %q; valid: %s", what, v, strings.Join(allowed, ", "))
		}
		if slices.Contains(out, u) {
			return nil, fmt.Errorf("duplicate %s %s", what, u)
		}
		out = append(out, u)
	}
	return out, nil
}

func sortFor(by, order string, dims, metrics []string, timeDim string) ([]sortCondition, error) {
	order = strings.ToLower(strings.TrimSpace(order))
	if order != "" && order != "asc" && order != "desc" {
		return nil, fmt.Errorf("sortOrder must be asc or desc, got %q", order)
	}
	by = strings.ToUpper(strings.TrimSpace(by))
	var sc sortCondition
	switch {
	case by == "" && timeDim != "":
		sc = sortCondition{Dimension: timeDim, Order: "ASCENDING"}
	case by == "":
		sc = sortCondition{Metric: "ESTIMATED_EARNINGS", Order: "DESCENDING"}
		if !slices.Contains(metrics, "ESTIMATED_EARNINGS") {
			return nil, nil // undefined order is fine without earnings
		}
	case slices.Contains(metrics, by):
		sc = sortCondition{Metric: by, Order: "DESCENDING"}
	case slices.Contains(dims, by):
		sc = sortCondition{Dimension: by, Order: "ASCENDING"}
	default:
		return nil, fmt.Errorf("sortBy %q must be one of the requested dimensions or metrics", by)
	}
	switch order {
	case "asc":
		sc.Order = "ASCENDING"
	case "desc":
		sc.Order = "DESCENDING"
	}
	return []sortCondition{sc}, nil
}

func toDate(t time.Time) apiDate { return apiDate{t.Year(), int(t.Month()), t.Day()} }

func (d apiDate) time() time.Time {
	return time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, time.UTC)
}

// previousWindow is the equal-length range right before [start, end].
func previousWindow(start, end apiDate) (apiDate, apiDate) {
	s, e := daterange.Previous(start.time(), end.time())
	return toDate(s), toDate(e)
}

// accountNow is the current time in the account's reporting time zone.
func accountNow(acc publisherAccount) time.Time {
	if loc, err := time.LoadLocation(acc.ReportingTimeZone); err == nil && acc.ReportingTimeZone != "" {
		return time.Now().In(loc)
	}
	return time.Now()
}

// ---- rendering ----

// dimCell shows a dimension value with its label when they differ, e.g.
// "My App (ca-app-pub-…~…)".
func dimCell(v dimensionValue) string {
	if v.DisplayLabel != "" && v.DisplayLabel != v.Value {
		return v.DisplayLabel + " (" + v.Value + ")"
	}
	return v.Value
}

func formatMetric(name string, v float64) string {
	switch {
	case slices.Contains(rateMetrics, name):
		return strconv.FormatFloat(v*100, 'f', 2, 64) + "%"
	case slices.Contains(moneyMetrics, name):
		return strconv.FormatFloat(v, 'f', 2, 64)
	case v == math.Trunc(v):
		return strconv.FormatFloat(v, 'f', 0, 64)
	default:
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
}

func metricHeader(name, currency string) string {
	if currency != "" && slices.Contains(moneyMetrics, name) {
		return name + " (" + currency + ")"
	}
	return name
}

// renderReport renders up to maxRows rows as a markdown table plus totals.
func renderReport(acc string, r report, maxRows int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "AdMob %s report for %s (%s → %s) — %d row(s)", r.Kind, acc,
		r.Spec.DateRange.StartDate, r.Spec.DateRange.EndDate, len(r.Rows))
	if r.Matching != "" && r.Matching != strconv.Itoa(len(r.Rows)) {
		fmt.Fprintf(&b, " of %s matching (raise maxRows for more)", r.Matching)
	}
	b.WriteString(".\n\n")
	if len(r.Rows) == 0 {
		b.WriteString("No data for this range and filters (today's data can lag a few hours).")
		return b.String()
	}
	headers := slices.Clone(r.Spec.Dimensions)
	for _, m := range r.Spec.Metrics {
		headers = append(headers, metricHeader(m, r.Currency))
	}
	b.WriteString("| " + strings.Join(headers, " | ") + " |\n|" + strings.Repeat("---|", len(headers)) + "\n")
	shown := min(len(r.Rows), maxRows)
	for _, row := range r.Rows[:shown] {
		cells := make([]string, 0, len(headers))
		for _, d := range r.Spec.Dimensions {
			cells = append(cells, render.MDCell(dimCell(row.DimensionValues[d])))
		}
		for _, m := range r.Spec.Metrics {
			cells = append(cells, formatMetric(m, row.MetricValues[m].number()))
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	if shown < len(r.Rows) {
		fmt.Fprintf(&b, "\n(%d more rows not shown — pass saveToPath to export everything as CSV.)\n", len(r.Rows)-shown)
	}
	if len(r.Rows) > 1 {
		b.WriteString("\n**Totals:** " + totalsLine(r) + "\n")
	}
	return b.String()
}

// totalsLine sums additive metrics and derives eCPM/CTR/match rate from them.
func totalsLine(r report) string {
	sums := map[string]float64{}
	for _, row := range r.Rows {
		for _, m := range additiveMetrics {
			if v, ok := row.MetricValues[m]; ok {
				sums[m] += v.number()
			}
		}
	}
	var parts []string
	for _, m := range r.Spec.Metrics {
		v, ok := sums[m]
		switch {
		case ok:
		case (m == "IMPRESSION_RPM" || m == "OBSERVED_ECPM") && sums["IMPRESSIONS"] > 0:
			v = sums["ESTIMATED_EARNINGS"] / sums["IMPRESSIONS"] * 1000
		case m == "IMPRESSION_CTR" && sums["IMPRESSIONS"] > 0:
			v = sums["CLICKS"] / sums["IMPRESSIONS"]
		case m == "MATCH_RATE" && sums["AD_REQUESTS"] > 0:
			v = sums["MATCHED_REQUESTS"] / sums["AD_REQUESTS"]
		default:
			continue
		}
		parts = append(parts, metricHeader(m, r.Currency)+" "+formatMetric(m, v))
	}
	return strings.Join(parts, " · ")
}

// reportCSV renders every row: dimension values and labels, then metrics in
// natural units (rates as 0–1 fractions).
func reportCSV(r report) string {
	var b strings.Builder
	var headers []string
	for _, d := range r.Spec.Dimensions {
		headers = append(headers, d, d+"_LABEL")
	}
	for _, m := range r.Spec.Metrics {
		headers = append(headers, metricHeader(m, r.Currency))
	}
	b.WriteString(render.CSVLine(headers))
	for _, row := range r.Rows {
		cells := make([]string, 0, len(headers))
		for _, d := range r.Spec.Dimensions {
			v := row.DimensionValues[d]
			cells = append(cells, v.Value, v.DisplayLabel)
		}
		for _, m := range r.Spec.Metrics {
			cells = append(cells, strconv.FormatFloat(row.MetricValues[m].number(), 'f', -1, 64))
		}
		b.WriteString(render.CSVLine(cells))
	}
	return b.String()
}

// ---- presets: several cuts in one call ----

type preset struct {
	title string
	kind  string
	dims  []string
}

var presets = map[string]preset{
	"daily":            {"Daily trend", "network", []string{"DATE"}},
	"weekly":           {"Weekly trend", "network", []string{"WEEK"}},
	"monthly":          {"Monthly trend", "network", []string{"MONTH"}},
	"apps":             {"By app", "network", []string{"APP"}},
	"ad_units":         {"By ad unit", "network", []string{"APP", "AD_UNIT"}},
	"formats":          {"By ad format", "network", []string{"FORMAT"}},
	"countries":        {"By country", "network", []string{"COUNTRY"}},
	"platforms":        {"By platform", "network", []string{"PLATFORM"}},
	"app_versions":     {"By app version", "network", []string{"APP", "APP_VERSION_NAME"}},
	"sdk_versions":     {"By GMA SDK version", "network", []string{"GMA_SDK_VERSION"}},
	"os_versions":      {"By OS version", "network", []string{"PLATFORM", "MOBILE_OS_VERSION"}},
	"ad_sources":       {"Mediation: by ad source", "mediation", []string{"AD_SOURCE"}},
	"mediation_groups": {"Mediation: by mediation group", "mediation", []string{"MEDIATION_GROUP"}},
}

func presetNames() string {
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ---- period comparison ----

type comparisonRow struct {
	key        string
	prev, curr map[string]float64
}

var comparisonMetrics = []string{"ESTIMATED_EARNINGS", "IMPRESSIONS", "CLICKS", "AD_REQUESTS"}

// mergeReports joins two reports on their dimension values and sorts by the
// absolute change in earnings.
func mergeReports(prev, curr report) []comparisonRow {
	rows := map[string]*comparisonRow{}
	var order []string
	add := func(r report, current bool) {
		for _, row := range r.Rows {
			parts := make([]string, len(r.Spec.Dimensions))
			for i, d := range r.Spec.Dimensions {
				parts[i] = dimCell(row.DimensionValues[d])
			}
			k := strings.Join(parts, " / ")
			cr := rows[k]
			if cr == nil {
				cr = &comparisonRow{key: k, prev: map[string]float64{}, curr: map[string]float64{}}
				rows[k] = cr
				order = append(order, k)
			}
			target := cr.prev
			if current {
				target = cr.curr
			}
			for _, m := range comparisonMetrics {
				target[m] += row.MetricValues[m].number()
			}
		}
	}
	add(prev, false)
	add(curr, true)
	out := make([]comparisonRow, 0, len(order))
	for _, k := range order {
		out = append(out, *rows[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		di := math.Abs(out[i].curr["ESTIMATED_EARNINGS"] - out[i].prev["ESTIMATED_EARNINGS"])
		dj := math.Abs(out[j].curr["ESTIMATED_EARNINGS"] - out[j].prev["ESTIMATED_EARNINGS"])
		return di > dj
	})
	return out
}

func ecpm(m map[string]float64) float64 {
	if m["IMPRESSIONS"] == 0 {
		return 0
	}
	return m["ESTIMATED_EARNINGS"] / m["IMPRESSIONS"] * 1000
}

func pctChange(prev, curr float64) string {
	if prev == 0 {
		if curr == 0 {
			return "0%"
		}
		return "new"
	}
	return fmt.Sprintf("%+.1f%%", (curr-prev)/prev*100)
}

func renderComparison(dims []string, rows []comparisonRow, currency string, maxRows int) string {
	var b strings.Builder
	cur := ""
	if currency != "" {
		cur = " (" + currency + ")"
	}
	fmt.Fprintf(&b, "| %s | Earnings%s base → current | Δ | Impressions base → current | Δ | eCPM%s base → current |\n|---|---|---|---|---|---|\n",
		strings.Join(dims, " / "), cur, cur)
	var tp, tc = map[string]float64{}, map[string]float64{}
	for i, r := range rows {
		for _, m := range comparisonMetrics {
			tp[m] += r.prev[m]
			tc[m] += r.curr[m]
		}
		if i >= maxRows {
			continue
		}
		fmt.Fprintf(&b, "| %s | %.2f → %.2f | %s | %.0f → %.0f | %s | %.2f → %.2f |\n", render.MDCell(r.key),
			r.prev["ESTIMATED_EARNINGS"], r.curr["ESTIMATED_EARNINGS"], pctChange(r.prev["ESTIMATED_EARNINGS"], r.curr["ESTIMATED_EARNINGS"]),
			r.prev["IMPRESSIONS"], r.curr["IMPRESSIONS"], pctChange(r.prev["IMPRESSIONS"], r.curr["IMPRESSIONS"]),
			ecpm(r.prev), ecpm(r.curr))
	}
	if len(rows) > maxRows {
		fmt.Fprintf(&b, "\n(%d smaller changes not shown.)\n", len(rows)-maxRows)
	}
	fmt.Fprintf(&b, "\n**Total:** earnings %.2f → %.2f (%s), impressions %.0f → %.0f (%s), eCPM %.2f → %.2f, clicks %.0f → %.0f\n",
		tp["ESTIMATED_EARNINGS"], tc["ESTIMATED_EARNINGS"], pctChange(tp["ESTIMATED_EARNINGS"], tc["ESTIMATED_EARNINGS"]),
		tp["IMPRESSIONS"], tc["IMPRESSIONS"], pctChange(tp["IMPRESSIONS"], tc["IMPRESSIONS"]),
		ecpm(tp), ecpm(tc), tp["CLICKS"], tc["CLICKS"])
	return b.String()
}
