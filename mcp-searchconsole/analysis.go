package main

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/homielab/homielab-mcp/internal/render"
)

// ---- input validation (fail fast without spending API quota) ----

var validDimensions = map[string]bool{
	"date": true, "hour": true, "query": true, "page": true,
	"country": true, "device": true, "searchAppearance": true,
}

var validFilterDimensions = map[string]bool{
	"country": true, "device": true, "page": true,
	"query": true, "searchAppearance": true,
}

var validOperators = map[string]bool{
	"contains": true, "equals": true, "notContains": true,
	"notEquals": true, "includingRegex": true, "excludingRegex": true,
}

var validTypes = map[string]string{ // lowercase input → canonical value
	"web": "web", "image": "image", "video": "video",
	"news": "news", "discover": "discover", "googlenews": "googleNews",
}

var validDataStates = map[string]bool{"all": true, "final": true, "hourly_all": true}

// validateDimensions rejects unknown or duplicated dimensions and nudges the
// dataState when the "hour" dimension is requested (hourly rows only exist
// with dataState all / hourly_all).
func validateDimensions(dims []string) ([]string, error) {
	if len(dims) == 0 {
		return dims, nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(dims))
	for _, d := range dims {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if !validDimensions[d] {
			return nil, fmt.Errorf("unknown dimension %q — valid: date, hour, query, page, country, device, searchAppearance", d)
		}
		if seen[d] {
			return nil, fmt.Errorf("dimension %q repeated — the API rejects duplicates", d)
		}
		seen[d] = true
		out = append(out, d)
	}
	return out, nil
}

// canonicalType maps a user-supplied search type to the API's canonical form
// ("googlenews" → "googleNews"); "" stays "" (API default: web).
func canonicalType(t string) (string, error) {
	t = strings.TrimSpace(t)
	if t == "" {
		return "", nil
	}
	if c, ok := validTypes[strings.ToLower(t)]; ok {
		return c, nil
	}
	return "", fmt.Errorf("unknown type %q — valid: web, image, video, news, discover, googleNews", t)
}

// canonicalDataState validates and normalizes dataState.
func canonicalDataState(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	lc := strings.ToLower(s)
	if !validDataStates[lc] {
		return "", fmt.Errorf("unknown dataState %q — valid: all, final, hourly_all", s)
	}
	return lc, nil
}

// validateFilters checks filter dimensions/operators/expressions.
func validateFilters(filters []analyticsFilter) error {
	for i, f := range filters {
		if !validFilterDimensions[strings.TrimSpace(f.Dimension)] {
			return fmt.Errorf("filter %d: unknown dimension %q — valid: country, device, page, query, searchAppearance", i, f.Dimension)
		}
		if strings.TrimSpace(f.Operator) != "" && !validOperators[strings.TrimSpace(f.Operator)] {
			return fmt.Errorf("filter %d: unknown operator %q — valid: contains, equals, notContains, notEquals, includingRegex, excludingRegex", i, f.Operator)
		}
		if strings.TrimSpace(f.Expression) == "" {
			return fmt.Errorf("filter %d: expression must not be empty", i)
		}
	}
	return nil
}

// analyticsBodyValidated builds the query body with all client-side checks.
func analyticsBodyValidated(args analyticsArgs) (analyticsQueryBody, error) {
	typ, err := canonicalType(args.Type)
	if err != nil {
		return analyticsQueryBody{}, err
	}
	state, err := canonicalDataState(args.DataState)
	if err != nil {
		return analyticsQueryBody{}, err
	}
	dims, err := validateDimensions(args.Dimensions)
	if err != nil {
		return analyticsQueryBody{}, err
	}
	if len(dims) == 0 {
		dims = []string{"date"}
	}
	if hasDim(dims, "hour") && state == "" {
		state = "all" // hourly rows only exist with all/hourly_all
	}
	if err := validateFilters(args.Filters); err != nil {
		return analyticsQueryBody{}, err
	}
	body, err := analyticsBody(analyticsArgs{
		SiteURL:         args.SiteURL,
		DateRange:       args.DateRange,
		StartDate:       args.StartDate,
		EndDate:         args.EndDate,
		Dimensions:      dims,
		Filters:         args.Filters,
		Type:            typ,
		AggregationType: args.AggregationType,
		DataState:       state,
		RowLimit:        args.RowLimit,
		StartRow:        args.StartRow,
	})
	return body, err
}

func hasDim(dims []string, d string) bool {
	for _, x := range dims {
		if x == d {
			return true
		}
	}
	return false
}

// ---- URL inspection pre-flight (protect the 2k/day quota) ----

// preflightInspect checks that inspectionURL plausibly belongs to the
// property before spending an inspection-API call on it.
func preflightInspect(site, inspectionURL string) error {
	return urlInProperty("inspectionUrl", site, inspectionURL)
}

// urlInProperty checks that rawURL (named field in errors) is absolute and on
// the host of a URL-prefix property.
func urlInProperty(field, site, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s must be a fully qualified URL like https://www.example.com/page, got %q", field, rawURL)
	}
	if !strings.HasPrefix(site, "sc-domain:") {
		pu, err := url.Parse(site)
		if err == nil && pu.Host != "" && !strings.EqualFold(u.Host, pu.Host) {
			return fmt.Errorf("%s host %q does not belong to property %q (host %q) — the API would reject it; check gsc_list_sites for the right property", field, u.Host, site, pu.Host)
		}
	}
	// sc-domain properties cover any subdomain of the domain — nothing to check.
	return nil
}

// ---- quick report: several dimension cuts in one tool call ----

type analyticsCut struct {
	title string
	dims  []string
	limit int
}

var quickReportCuts = []analyticsCut{
	{"Daily trend (clicks, impressions, CTR, position)", []string{"date"}, 400},
	{"Top queries by clicks", []string{"query"}, 200},
	{"Top pages by clicks", []string{"page"}, 200},
	{"Device split", []string{"device"}, 10},
	{"Country split", []string{"country"}, 60},
	{"Query → page mapping (cannibalization check)", []string{"query", "page"}, 200},
}

// ---- period comparison ----

type comparisonRow struct {
	keys               []string
	prevClicks, clicks float64
	prevImpr, impr     float64
	prevCtr, ctr       float64
	prevPos, pos       float64
}

// mergeAnalytics joins two analytics responses on their dimension keys.
func mergeAnalytics(prev, curr analyticsResponse) []comparisonRow {
	type value struct{ p, c analyticsRow }
	index := map[string]*value{}
	order := []string{}
	add := func(rows []analyticsRow, isCurr bool) {
		for _, r := range rows {
			k := strings.Join(r.Keys, "\x1f")
			v, ok := index[k]
			if !ok {
				v = &value{}
				index[k] = v
				order = append(order, k)
			}
			if isCurr {
				v.c = r
			} else {
				v.p = r
			}
		}
	}
	add(prev.Rows, false)
	add(curr.Rows, true)
	out := make([]comparisonRow, 0, len(order))
	for _, k := range order {
		v := index[k]
		out = append(out, comparisonRow{
			// Split the joined key, not v.c.Keys: keys present only in the
			// baseline period have an empty curr row, and nil keys would
			// render a table row with missing leading cells.
			keys:       strings.Split(k, "\x1f"),
			prevClicks: v.p.Clicks, clicks: v.c.Clicks,
			prevImpr: v.p.Impressions, impr: v.c.Impressions,
			prevCtr: v.p.Ctr, ctr: v.c.Ctr,
			prevPos: v.p.Position, pos: v.c.Position,
		})
	}
	// biggest absolute click change first
	sort.SliceStable(out, func(i, j int) bool {
		return abs(out[i].clicks-out[i].prevClicks) > abs(out[j].clicks-out[j].prevClicks)
	})
	return out
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// renderComparison formats the period-over-period table.
func renderComparison(rows []comparisonRow, dims []string, maxRows int) string {
	if len(dims) == 0 {
		dims = []string{"date"}
	}
	var b strings.Builder
	headers := append([]string{}, dims...)
	headers = append(headers,
		"Clicks prev", "Clicks now", "Δ clicks",
		"Impr prev", "Impr now", "Δ impr",
		"CTR now", "Position now (Δ)")
	b.WriteString("| " + strings.Join(headers, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat(" --- |", len(headers)) + "\n")
	shown := len(rows)
	if shown > maxRows {
		shown = maxRows
	}
	for _, r := range rows[:shown] {
		cells := make([]string, 0, len(headers))
		for _, k := range r.keys {
			cells = append(cells, render.MDCell(k))
		}
		cells = append(cells,
			fmt.Sprintf("%.0f", r.prevClicks),
			fmt.Sprintf("%.0f", r.clicks),
			fmt.Sprintf("%+.0f", r.clicks-r.prevClicks),
			fmt.Sprintf("%.0f", r.prevImpr),
			fmt.Sprintf("%.0f", r.impr),
			fmt.Sprintf("%+.0f", r.impr-r.prevImpr),
			fmt.Sprintf("%.2f%%", r.ctr*100),
			fmt.Sprintf("%.1f (%+.1f)", r.pos, r.pos-r.prevPos),
		)
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	if len(rows) > shown {
		fmt.Fprintf(&b, "\n(%d more rows omitted — sorted by absolute click change.)\n", len(rows)-shown)
	}
	return b.String()
}

// ---- client-side row refinement (the API orders by clicks only) ----

// rowRefinement sorts and filters rows after the API call.
type rowRefinement struct {
	SortBy         string  `json:"sortBy,omitempty" jsonschema:"Re-sort returned rows by clicks, impressions, ctr or position (the API itself orders by clicks)."`
	SortOrder      string  `json:"sortOrder,omitempty" jsonschema:"desc or asc; default desc, but asc (best first) for position."`
	MinImpressions float64 `json:"minImpressions,omitempty" jsonschema:"Keep only rows with at least this many impressions."`
	MinPosition    float64 `json:"minPosition,omitempty" jsonschema:"Keep only rows with average position >= this (e.g. 11 for page two)."`
	MaxPosition    float64 `json:"maxPosition,omitempty" jsonschema:"Keep only rows with average position <= this (e.g. 20)."`
}

var sortMetrics = map[string]func(analyticsRow) float64{
	"clicks":      func(r analyticsRow) float64 { return r.Clicks },
	"impressions": func(r analyticsRow) float64 { return r.Impressions },
	"ctr":         func(r analyticsRow) float64 { return r.Ctr },
	"position":    func(r analyticsRow) float64 { return r.Position },
}

func (f rowRefinement) validate() error {
	if f.SortBy != "" && sortMetrics[f.SortBy] == nil {
		return fmt.Errorf("sortBy %q must be clicks, impressions, ctr or position", f.SortBy)
	}
	if f.SortOrder != "" && f.SortOrder != "asc" && f.SortOrder != "desc" {
		return fmt.Errorf("sortOrder %q must be asc or desc", f.SortOrder)
	}
	if f.MinPosition > 0 && f.MaxPosition > 0 && f.MinPosition > f.MaxPosition {
		return fmt.Errorf("minPosition %.1f is greater than maxPosition %.1f", f.MinPosition, f.MaxPosition)
	}
	return nil
}

// apply filters, then sorts (stable, so ties keep the API's click order).
func (f rowRefinement) apply(rows []analyticsRow) []analyticsRow {
	out := rows[:0:0]
	for _, r := range rows {
		if r.Impressions < f.MinImpressions ||
			(f.MinPosition > 0 && r.Position < f.MinPosition) ||
			(f.MaxPosition > 0 && r.Position > f.MaxPosition) {
			continue
		}
		out = append(out, r)
	}
	if metric := sortMetrics[f.SortBy]; metric != nil {
		asc := f.SortOrder == "asc" || (f.SortOrder == "" && f.SortBy == "position")
		sort.SliceStable(out, func(i, j int) bool {
			if asc {
				return metric(out[i]) < metric(out[j])
			}
			return metric(out[i]) > metric(out[j])
		})
	}
	return out
}

// ---- URL inspection classification ----

// inspectionIssues lists what needs attention for one inspected URL; empty
// means indexed with no detected problem.
func inspectionIssues(r inspectionResult) []string {
	var out []string
	if s := r.IndexStatusResult; s != nil {
		if s.Verdict != "PASS" {
			out = append(out, "not indexed: "+render.OrDash(s.CoverageState))
		}
		if s.GoogleCanonical != "" && s.UserCanonical != "" && s.GoogleCanonical != s.UserCanonical {
			out = append(out, "canonical mismatch: Google chose "+s.GoogleCanonical)
		}
		if s.RobotsTxtState == "DISALLOWED" {
			out = append(out, "blocked by robots.txt")
		}
		if s.PageFetchState != "" && s.PageFetchState != "SUCCESSFUL" && s.PageFetchState != "PAGE_FETCH_STATE_UNSPECIFIED" {
			out = append(out, "fetch: "+s.PageFetchState)
		}
	} else {
		out = append(out, "no index status returned")
	}
	if rr := r.RichResultsResult; rr != nil && rr.Verdict == "FAIL" {
		out = append(out, "rich results invalid")
	}
	return out
}

// ---- write helpers ----

// normalizeNewProperty validates a property to add: "sc-domain:example.com"
// or an http(s) URL prefix, which gets a trailing slash.
func normalizeNewProperty(site string) (string, error) {
	site = strings.TrimSpace(site)
	if d, ok := strings.CutPrefix(site, "sc-domain:"); ok {
		if d == "" || strings.ContainsAny(d, "/: ") {
			return "", fmt.Errorf("domain property must look like sc-domain:example.com, got %q", site)
		}
		return "sc-domain:" + strings.ToLower(d), nil
	}
	u, err := url.Parse(site)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("siteUrl must be a URL prefix like https://www.example.com/ or a domain property like sc-domain:example.com, got %q", site)
	}
	if !strings.HasSuffix(site, "/") {
		site += "/"
	}
	return site, nil
}

// scopeHint tells the user to re-authorize when a token predates the write
// scope (older versions requested webmasters.readonly).
func scopeHint(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "insufficient authentication scopes") || strings.Contains(msg, "ACCESS_TOKEN_SCOPE_INSUFFICIENT") {
		return fmt.Errorf("%w\n\nThe stored token is read-only (from an older version). Call gsc_logout, then gsc_login and approve the new Search Console access.", err)
	}
	return err
}
