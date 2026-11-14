package main

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/daterange"
	"github.com/homielab/homielab-mcp/internal/mcpkit"
	"github.com/homielab/homielab-mcp/internal/render"
)

type emptyArgs = mcpkit.EmptyArgs

func textResult(s string) (*mcp.CallToolResult, any, error) { return mcpkit.TextResult(s) }

func errResult(err error) (*mcp.CallToolResult, any, error) { return mcpkit.ErrResult(auth, err) }

// mustClient returns an API client or an error result.
func mustClient(ctx context.Context) (*gscClient, *mcp.CallToolResult) {
	c, err := newGSCClient(ctx)
	if err != nil {
		res, _, _ := errResult(err)
		return nil, res
	}
	return c, nil
}

// resolveSiteArg resolves siteUrl (possibly empty) for tools, returning an
// error result on failure.
func resolveSiteArg(ctx context.Context, c *gscClient, site string) (string, *mcp.CallToolResult) {
	resolved, err := c.resolveSite(ctx, site)
	if err != nil {
		res, _, _ := errResult(err)
		return "", res
	}
	return resolved, nil
}

const gscDimensions = `
Dimensions: date, hour, query, page, country (ISO-3166 alpha-3), device
(DESKTOP/MOBILE/TABLET), searchAppearance.
Search type (type): web (default), image, video, news, discover, googleNews.
Aggregation: auto (default), byPage, byProperty.
Filter operators: contains, equals (default), notContains, notEquals,
includingRegex (RE2), excludingRegex (RE2).`

func registerTools(s *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcpkit.AddLoginTools(s, auth, mcpkit.LoginTools{
		Description: "Authorize access to Google Search Console: opens the browser at Google's OAuth consent page and stores the refresh token locally. Non-blocking: returns immediately while approval is pending — call gsc_login_status afterwards. Required once before any other tool. Scope: webmasters (read, plus adding/removing properties and sitemaps).",
		Authorized: func(ctx context.Context) (string, bool) {
			c, err := newGSCClient(ctx)
			if err != nil {
				return "", false
			}
			sites, err := c.listSites(ctx)
			if err != nil || len(sites) == 0 {
				return "", false
			}
			return fmt.Sprintf("Already authorized — %d Search Console property(ies) accessible (e.g. %s). No need to log in again.",
				len(sites), siteList(sites)), true
		},
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gsc_list_sites",
		Title:       "List Search Console Properties",
		Description: "List Search Console properties (URL-prefix like https://www.example.com/ or domain like sc-domain:example.com) with your permission level. The returned exact siteUrl values should be passed to other tools.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		sites, err := c.listSites(ctx)
		if err != nil {
			return errResult(err)
		}
		if len(sites) == 0 {
			return textResult("No Search Console properties accessible for this Google login.")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d property(ies):\n", len(sites))
		for _, s := range sites {
			fmt.Fprintf(&b, "- %s  (%s)\n", s.SiteURL, s.PermissionLevel)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "gsc_search_analytics",
		Title: "Search Analytics Report",
		Description: "Search Analytics (performance) report: clicks, impressions, CTR and average position grouped by dimensions, for a property and date range. " +
			"Typical combos: daily trend [dimensions=[\"date\"]], top queries [dimensions=[\"query\"]], top pages [dimensions=[\"page\"]], " +
			"queries on one page [dimensions=[\"query\"], filters=[{dimension:\"page\",expression:\"...\",operator:\"equals\"}]], " +
			"device split [dimensions=[\"device\"]], country split [dimensions=[\"country\"]], query×page [dimensions=[\"query\",\"page\"]]. " +
			gscDimensions +
			"\nData lags ~2 days (dataState \"all\" includes fresher partial data). Max 25,000 rows per call; " +
			" paginate with startRow. Results are top-N, sorted by clicks (by date when grouping by date); " +
			"sortBy/sortOrder re-sort and minImpressions/minPosition/maxPosition filter the returned rows, " +
			"e.g. page-two opportunities: dimensions [\"query\",\"page\"], rowLimit 5000, minPosition 11, maxPosition 20, minImpressions 100, sortBy impressions.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args searchAnalyticsArgs) (*mcp.CallToolResult, any, error) {
		if err := args.rowRefinement.validate(); err != nil {
			return errResult(err)
		}
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		site, res := resolveSiteArg(ctx, c, args.SiteURL)
		if res != nil {
			return res, nil, nil
		}
		body, err := analyticsBodyValidated(args.analyticsArgs)
		if err != nil {
			return errResult(err)
		}
		r, err := c.analyticsQuery(ctx, site, body)
		if err != nil {
			return errResult(err)
		}
		r.Rows = args.rowRefinement.apply(r.Rows)
		if args.SaveToPath != "" {
			msg, err := saveAnalyticsCSV(args.SaveToPath, site, body, r)
			if err != nil {
				return errResult(err)
			}
			return textResult(msg)
		}
		return textResult(renderAnalytics(site, body, r, 100))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "gsc_quick_report",
		Title: "Search Analytics Quick Report",
		Description: "Run several Search Analytics cuts in ONE call — the fastest way to a broad SEO picture: daily trend, top queries, top pages, device split, country split, and query→page mapping. " +
			"Each cut comes back as its own markdown table; a cut the API rejects is reported inline without aborting the others. " +
			"Use gsc_period_comparison for period-over-period deltas and gsc_inspect_url for index status.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args quickReportArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		site, res := resolveSiteArg(ctx, c, args.SiteURL)
		if res != nil {
			return res, nil, nil
		}
		var b strings.Builder
		for i, cut := range quickReportCuts {
			if i > 0 {
				b.WriteString("\n\n---\n\n")
			}
			fmt.Fprintf(&b, "## %s\n", cut.title)
			aa := analyticsArgs{
				SiteURL:         site,
				DateRange:       args.DateRange,
				StartDate:       args.StartDate,
				EndDate:         args.EndDate,
				Dimensions:      cut.dims,
				Filters:         args.Filters,
				Type:            args.Type,
				DataState:       args.DataState,
				AggregationType: cutAggregation(cut.dims),
				RowLimit:        cut.limit,
			}
			if args.MaxRows > 0 && args.MaxRows < cut.limit {
				aa.RowLimit = args.MaxRows
			}
			body, err := analyticsBodyValidated(aa)
			if err != nil {
				fmt.Fprintf(&b, "⚠ %v\n", err)
				continue
			}
			r, err := c.analyticsQuery(ctx, site, body)
			if err != nil {
				fmt.Fprintf(&b, "⚠ this cut failed (%v)\n", err)
				continue
			}
			b.WriteString(renderAnalytics(site, body, r, 50))
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "gsc_period_comparison",
		Title: "Compare Analytics Periods",
		Description: "Period-over-period comparison of Search Analytics: runs the same query for the given window and a baseline window — by default the immediately preceding window of equal length, or baselineStartDate/baselineEndDate (e.g. the same window last year) — then reports per-key deltas (current minus baseline: clicks, impressions, CTR, position) sorted by absolute click change. " +
			"Best with query / page / device / country dimensions — not with date. " +
			gscDimensions,
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args comparisonArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		site, res := resolveSiteArg(ctx, c, args.SiteURL)
		if res != nil {
			return res, nil, nil
		}
		if len(args.Dimensions) == 0 {
			args.Dimensions = []string{"query"}
		}
		if args.RowLimit <= 0 {
			args.RowLimit = 2000
		}
		curr, err := analyticsBodyValidated(analyticsArgs{
			SiteURL: site, DateRange: args.DateRange, StartDate: args.StartDate, EndDate: args.EndDate,
			Dimensions: args.Dimensions, Filters: args.Filters, Type: args.Type,
			DataState: args.DataState, RowLimit: args.RowLimit,
		})
		if err != nil {
			return errResult(err)
		}
		if hasDim(curr.Dimensions, "date") || hasDim(curr.Dimensions, "hour") {
			return errResult(fmt.Errorf("use non-date dimensions (query, page, device, country, ...) so both periods can be matched"))
		}
		prevStart, prevEnd := args.BaselineStartDate, args.BaselineEndDate
		switch {
		case prevStart == "" && prevEnd == "":
			if prevStart, prevEnd, err = daterange.PreviousStrings(curr.StartDate, curr.EndDate); err != nil {
				return errResult(err)
			}
		case !daterange.Valid(prevStart) || !daterange.Valid(prevEnd) || prevStart > prevEnd:
			return errResult(fmt.Errorf("baselineStartDate/baselineEndDate must both be YYYY-MM-DD with start <= end, got %q → %q", prevStart, prevEnd))
		}
		prev := curr
		prev.StartDate, prev.EndDate = prevStart, prevEnd
		prevResp, err := c.analyticsQuery(ctx, site, prev)
		if err != nil {
			return errResult(fmt.Errorf("baseline period (%s → %s): %w", prevStart, prevEnd, err))
		}
		currResp, err := c.analyticsQuery(ctx, site, curr)
		if err != nil {
			return errResult(err)
		}
		merged := mergeAnalytics(prevResp, currResp)
		var b strings.Builder
		fmt.Fprintf(&b, "Period comparison for %s: current %s → %s vs baseline %s → %s — %d matched key(s).\n\n",
			site, curr.StartDate, curr.EndDate, prevStart, prevEnd, len(merged))
		b.WriteString(renderComparison(merged, curr.Dimensions, 100))
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gsc_list_sitemaps",
		Title:       "List Sitemaps",
		Description: "List sitemaps submitted for a Search Console property with submission dates, indexability and per-content-type submitted/indexed counts. Pass sitemapIndex to list the child sitemaps of one sitemap index.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		SiteURL      string `json:"siteUrl,omitempty" jsonschema:"Property like https://www.example.com/ or sc-domain:example.com. Omit if the account has exactly one property."`
		SitemapIndex string `json:"sitemapIndex,omitempty" jsonschema:"Full URL of a sitemap index; lists its child sitemaps instead of the property's top-level ones."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		site, res := resolveSiteArg(ctx, c, args.SiteURL)
		if res != nil {
			return res, nil, nil
		}
		maps, err := c.listSitemaps(ctx, site, args.SitemapIndex)
		if err != nil {
			return errResult(err)
		}
		scope := site
		if args.SitemapIndex != "" {
			scope = "index " + args.SitemapIndex
		}
		if len(maps) == 0 {
			return textResult("No sitemaps found for " + scope + ".")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d sitemap(s) for %s:\n", len(maps), scope)
		for _, sm := range maps {
			fmt.Fprintf(&b, "- %s\n    type=%s lastSubmitted=%s indexed=%v errors=%d warnings=%d pending=%v\n",
				sm.Path, sm.Type, sm.LastSubmitted, sm.IsSitemapsIndexed, sm.Errors, sm.Warnings, sm.IsPending)
			for _, ct := range sm.Contents {
				fmt.Fprintf(&b, "    · %s: submitted=%d indexed=%d\n", ct.Type, ct.Submitted, ct.Indexed)
			}
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gsc_get_sitemap",
		Title:       "Get Sitemap Details",
		Description: "Get details of one submitted sitemap (feedpath) for a property: status, contents and error counts.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		SiteURL  string `json:"siteUrl,omitempty" jsonschema:"Property like https://www.example.com/ or sc-domain:example.com. Omit if the account has exactly one property."`
		Feedpath string `json:"feedpath" jsonschema:"Full sitemap URL, e.g. https://www.example.com/sitemap.xml"`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		site, res := resolveSiteArg(ctx, c, args.SiteURL)
		if res != nil {
			return res, nil, nil
		}
		sm, err := c.getSitemap(ctx, site, args.Feedpath)
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Sitemap %s (%s):\n  type=%s lastSubmitted=%s indexed=%v errors=%d warnings=%d pending=%v\n",
			sm.Path, site, sm.Type, sm.LastSubmitted, sm.IsSitemapsIndexed, sm.Errors, sm.Warnings, sm.IsPending)
		for _, ct := range sm.Contents {
			fmt.Fprintf(&b, "  · %s: submitted=%d indexed=%d\n", ct.Type, ct.Submitted, ct.Indexed)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "gsc_inspect_url",
		Title: "Inspect URL Index Status",
		Description: "URL Inspection: Google-index status of one URL (verdict, coverage state, robots.txt state, canonicals, last crawl, sitemaps) plus AMP / mobile usability / rich results when available. " +
			"The URL must belong to the given property. Quota is limited (~2,000 queries/property/day) — do not loop it over many URLs.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		SiteURL       string `json:"siteUrl,omitempty" jsonschema:"Property owning the URL (https://www.example.com/ or sc-domain:example.com). Omit if the account has exactly one property."`
		InspectionURL string `json:"inspectionUrl" jsonschema:"Fully qualified URL to inspect, must belong to the property."`
		LanguageCode  string `json:"languageCode,omitempty" jsonschema:"BCP-47 code for translated issue messages, e.g. vi; default en-US."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		site, res := resolveSiteArg(ctx, c, args.SiteURL)
		if res != nil {
			return res, nil, nil
		}
		if err := preflightInspect(site, args.InspectionURL); err != nil {
			return errResult(err)
		}
		r, err := c.urlInspect(ctx, site, args.InspectionURL, args.LanguageCode)
		if err != nil {
			return errResult(err)
		}
		return textResult(renderInspection(r))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "gsc_inspect_urls",
		Title: "Inspect Several URLs",
		Description: fmt.Sprintf("URL Inspection for up to %d URLs of one property in one call, run concurrently. ", maxInspectURLs) +
			"Returns a summary (indexed / not indexed / canonical mismatch / robots.txt block / fetch error / invalid rich results) and one row per URL. " +
			"Each URL costs one inspection from the ~2,000/property/day quota. Use gsc_inspect_url for the full detail of one URL.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		SiteURL string   `json:"siteUrl,omitempty" jsonschema:"Property owning the URLs (https://www.example.com/ or sc-domain:example.com). Omit if the account has exactly one property."`
		URLs    []string `json:"urls" jsonschema:"Fully qualified URLs to inspect (1-10), all in the property."`
	}) (*mcp.CallToolResult, any, error) {
		urls := dedupe(args.URLs)
		if len(urls) == 0 || len(urls) > maxInspectURLs {
			return errResult(fmt.Errorf("urls must hold 1-%d distinct URLs, got %d", maxInspectURLs, len(urls)))
		}
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		site, res := resolveSiteArg(ctx, c, args.SiteURL)
		if res != nil {
			return res, nil, nil
		}
		for _, u := range urls {
			if err := preflightInspect(site, u); err != nil {
				return errResult(err)
			}
		}
		results := make([]urlInspection, len(urls))
		var wg sync.WaitGroup
		for i, u := range urls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, err := c.urlInspect(ctx, site, u, "")
				results[i] = urlInspection{url: u, result: r, err: err}
			}()
		}
		wg.Wait()
		return textResult(renderInspections(site, results))
	})

	// ---- writes ----

	notDestructive := false
	addLike := &mcp.ToolAnnotations{DestructiveHint: &notDestructive, IdempotentHint: true}
	deleteLike := &mcp.ToolAnnotations{IdempotentHint: true}

	mcp.AddTool(s, &mcp.Tool{
		Name:  "gsc_add_site",
		Title: "Add Search Console Property",
		Description: "WRITE: add a property to the logged-in account — a URL prefix (https://www.example.com/) or a domain property (sc-domain:example.com). " +
			"It stays unverified (no data) until ownership is verified in Search Console; adding an existing property is a no-op.",
		Annotations: addLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		SiteURL string `json:"siteUrl" jsonschema:"Property to add: https://www.example.com/ or sc-domain:example.com."`
	}) (*mcp.CallToolResult, any, error) {
		site, err := normalizeNewProperty(args.SiteURL)
		if err != nil {
			return errResult(err)
		}
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		if err := c.addSite(ctx, site); err != nil {
			return errResult(scopeHint(err))
		}
		return textResult("Added property " + site + ". If it is new to this account it is unverified: verify ownership in Search Console " +
			"(Settings → Ownership verification) before any data or other tools work on it. gsc_list_sites shows its permission level.")
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "gsc_delete_site",
		Title: "Remove Search Console Property",
		Description: "WRITE, DESTRUCTIVE: remove a property from the logged-in account (other owners and the site itself are unaffected; re-adding needs verification again). " +
			"siteUrl must match gsc_list_sites exactly. Confirm with the user before calling.",
		Annotations: deleteLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		SiteURL string `json:"siteUrl" jsonschema:"Exact property from gsc_list_sites, e.g. https://www.example.com/ or sc-domain:example.com."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		sites, err := c.listSites(ctx)
		if err != nil {
			return errResult(err)
		}
		site := strings.TrimSpace(args.SiteURL)
		if !slices.ContainsFunc(sites, func(e siteEntry) bool { return e.SiteURL == site }) {
			return errResult(fmt.Errorf("%q is not an exact property of this account (no fuzzy matching for deletes). Available: %s", site, siteList(sites)))
		}
		if err := c.deleteSite(ctx, site); err != nil {
			return errResult(scopeHint(err))
		}
		return textResult("Removed property " + site + " from this account.")
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "gsc_submit_sitemap",
		Title: "Submit Sitemap",
		Description: "WRITE: submit (or resubmit) a sitemap URL for a property so Google fetches it. " +
			"Processing is asynchronous — check gsc_get_sitemap later for status and counts.",
		Annotations: addLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		SiteURL  string `json:"siteUrl,omitempty" jsonschema:"Property like https://www.example.com/ or sc-domain:example.com. Omit if the account has exactly one property."`
		Feedpath string `json:"feedpath" jsonschema:"Full sitemap URL on the property, e.g. https://www.example.com/sitemap.xml"`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		site, res := resolveSiteArg(ctx, c, args.SiteURL)
		if res != nil {
			return res, nil, nil
		}
		if err := urlInProperty("feedpath", site, args.Feedpath); err != nil {
			return errResult(err)
		}
		if err := c.submitSitemap(ctx, site, args.Feedpath); err != nil {
			return errResult(scopeHint(err))
		}
		return textResult(fmt.Sprintf("Submitted %s for %s. Google processes it asynchronously; check gsc_get_sitemap later.", args.Feedpath, site))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "gsc_delete_sitemap",
		Title: "Delete Sitemap",
		Description: "WRITE, DESTRUCTIVE: remove a submitted sitemap from a property's Sitemaps report (the file on the site is untouched; Google may still crawl it). " +
			"Confirm with the user before calling.",
		Annotations: deleteLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		SiteURL  string `json:"siteUrl,omitempty" jsonschema:"Property like https://www.example.com/ or sc-domain:example.com. Omit if the account has exactly one property."`
		Feedpath string `json:"feedpath" jsonschema:"Full sitemap URL exactly as listed by gsc_list_sitemaps."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		site, res := resolveSiteArg(ctx, c, args.SiteURL)
		if res != nil {
			return res, nil, nil
		}
		if err := urlInProperty("feedpath", site, args.Feedpath); err != nil {
			return errResult(err)
		}
		if err := c.deleteSitemap(ctx, site, args.Feedpath); err != nil {
			return errResult(scopeHint(err))
		}
		return textResult(fmt.Sprintf("Deleted sitemap %s from %s.", args.Feedpath, site))
	})
}

// ---- search analytics plumbing ----

type analyticsArgs struct {
	SiteURL         string            `json:"siteUrl,omitempty" jsonschema:"Property like https://www.example.com/ or sc-domain:example.com. Omit if the account has exactly one property."`
	DateRange       string            `json:"dateRange,omitempty" jsonschema:"TODAY, YESTERDAY, LAST_7_DAYS, LAST_30_DAYS, MONTH_TO_DATE, YEAR_TO_DATE, LAST_MONTH (default LAST_30_DAYS) — sugar over explicit dates."`
	StartDate       string            `json:"startDate,omitempty" jsonschema:"YYYY-MM-DD (PT). Overrides dateRange when both dates are given."`
	EndDate         string            `json:"endDate,omitempty" jsonschema:"YYYY-MM-DD (PT). Overrides dateRange when both dates are given."`
	Dimensions      []string          `json:"dimensions,omitempty" jsonschema:"Grouping dimensions, see tool description; default [\"date\"]."`
	Filters         []analyticsFilter `json:"filters,omitempty" jsonschema:"Filters, all ANDed: {dimension, expression, operator?}."`
	Type            string            `json:"type,omitempty" jsonschema:"Search type: web (default), image, video, news, discover, googleNews."`
	AggregationType string            `json:"aggregationType,omitempty" jsonschema:"auto (default), byPage, byProperty."`
	DataState       string            `json:"dataState,omitempty" jsonschema:"all (fresher, partial) or final (default)."`
	RowLimit        int               `json:"rowLimit,omitempty" jsonschema:"1-25000, default 1000."`
	StartRow        int               `json:"startRow,omitempty" jsonschema:"Zero-based row offset for pagination."`
	SaveToPath      string            `json:"saveToPath,omitempty" jsonschema:"Write the result as CSV to this file instead of returning a table (recommended above ~200 rows); returns the path and a preview."`
}

type quickReportArgs struct {
	SiteURL   string            `json:"siteUrl,omitempty" jsonschema:"Property like https://www.example.com/ or sc-domain:example.com. Omit if the account has exactly one property."`
	DateRange string            `json:"dateRange,omitempty" jsonschema:"TODAY, YESTERDAY, LAST_7_DAYS, LAST_30_DAYS, MONTH_TO_DATE, YEAR_TO_DATE, LAST_MONTH (default LAST_30_DAYS)."`
	StartDate string            `json:"startDate,omitempty" jsonschema:"YYYY-MM-DD (PT). Overrides dateRange when both dates are given."`
	EndDate   string            `json:"endDate,omitempty" jsonschema:"YYYY-MM-DD (PT). Overrides dateRange when both dates are given."`
	Filters   []analyticsFilter `json:"filters,omitempty" jsonschema:"Filters applied to every cut, all ANDed: {dimension, expression, operator?}."`
	Type      string            `json:"type,omitempty" jsonschema:"Search type: web (default), image, video, news, discover, googleNews."`
	DataState string            `json:"dataState,omitempty" jsonschema:"all (fresher, partial) or final (default)."`
	MaxRows   int               `json:"maxRows,omitempty" jsonschema:"Optional cap applied to every cut (cuts default to 200-400 rows)."`
}

func cutAggregation(dims []string) string {
	if len(dims) == 1 && dims[0] == "page" {
		return "byPage"
	}
	return ""
}

type comparisonArgs struct {
	SiteURL    string            `json:"siteUrl,omitempty" jsonschema:"Property like https://www.example.com/ or sc-domain:example.com. Omit if the account has exactly one property."`
	DateRange  string            `json:"dateRange,omitempty" jsonschema:"Preset applied to the current window (previous window is derived); default LAST_30_DAYS."`
	StartDate  string            `json:"startDate,omitempty" jsonschema:"YYYY-MM-DD current-window start (with endDate). Overrides dateRange."`
	EndDate    string            `json:"endDate,omitempty" jsonschema:"YYYY-MM-DD current-window end (with startDate). Overrides dateRange."`
	Dimensions []string          `json:"dimensions,omitempty" jsonschema:"Matching dimensions across periods: query, page, device, country, searchAppearance. Default ['query']."`
	Filters    []analyticsFilter `json:"filters,omitempty" jsonschema:"Filters applied to both periods, all ANDed."`
	Type       string            `json:"type,omitempty" jsonschema:"Search type: web (default), image, video, news, discover, googleNews."`
	DataState  string            `json:"dataState,omitempty" jsonschema:"all (fresher, partial) or final (default)."`
	RowLimit   int               `json:"rowLimit,omitempty" jsonschema:"Rows fetched per period (1-25000, default 2000)."`

	BaselineStartDate string `json:"baselineStartDate,omitempty" jsonschema:"YYYY-MM-DD baseline start (with baselineEndDate), e.g. the same window last year. Default: the window right before the current one."`
	BaselineEndDate   string `json:"baselineEndDate,omitempty" jsonschema:"YYYY-MM-DD baseline end (with baselineStartDate)."`
}

// searchAnalyticsArgs adds client-side sorting and filtering to a query.
type searchAnalyticsArgs struct {
	analyticsArgs
	rowRefinement
}

func analyticsBody(args analyticsArgs) (analyticsQueryBody, error) {
	s, e, err := daterange.Resolve(args.DateRange, args.StartDate, args.EndDate, time.Now())
	if err != nil {
		return analyticsQueryBody{}, err
	}
	start, end := s.Format(daterange.Layout), e.Format(daterange.Layout)
	dims := args.Dimensions
	if len(dims) == 0 {
		dims = []string{"date"}
	}
	body := analyticsQueryBody{
		StartDate:  start,
		EndDate:    end,
		Dimensions: dims,
		Type:       strings.TrimSpace(args.Type),
	}
	if len(args.Filters) > 0 {
		body.DimensionFilterGroups = []dimensionFilterGroup{{GroupType: "and", Filters: args.Filters}}
	}
	if args.AggregationType != "" {
		body.AggregationType = args.AggregationType
	}
	if args.DataState != "" {
		body.DataState = args.DataState
	}
	body.RowLimit = args.RowLimit
	if body.RowLimit <= 0 {
		body.RowLimit = 1000
	}
	if body.RowLimit > 25000 {
		body.RowLimit = 25000
	}
	body.StartRow = args.StartRow
	if body.StartRow < 0 {
		body.StartRow = 0
	}
	return body, nil
}

func renderAnalytics(site string, body analyticsQueryBody, r analyticsResponse, maxRows int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Search Analytics for %s (%s → %s, type=%s) — %d row(s).\n\n",
		site, body.StartDate, body.EndDate, typeLabel(body.Type), len(r.Rows))
	if len(r.Rows) == 0 {
		b.WriteString("No data returned (check the date range — data lags ~2 days — or the filters).")
		return b.String()
	}
	dims := body.Dimensions
	if len(dims) == 0 {
		dims = []string{"date"}
	}
	headers := append([]string{}, dims...)
	headers = append(headers, "Clicks", "Impressions", "CTR", "Position")
	b.WriteString("| " + strings.Join(headers, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat(" --- |", len(headers)) + "\n")
	shown := len(r.Rows)
	if shown > maxRows {
		shown = maxRows
	}
	for _, row := range r.Rows[:shown] {
		cells := make([]string, 0, len(headers))
		for _, k := range row.Keys {
			cells = append(cells, render.MDCell(k))
		}
		cells = append(cells,
			fmt.Sprintf("%.0f", row.Clicks),
			fmt.Sprintf("%.0f", row.Impressions),
			fmt.Sprintf("%.2f%%", row.Ctr*100),
			fmt.Sprintf("%.1f", row.Position),
		)
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	if len(r.Rows) > shown {
		fmt.Fprintf(&b, "\n(%d more rows omitted — paginate with startRow or use saveToPath for the full export.)\n", len(r.Rows)-shown)
	}
	var totClicks, totImpr float64
	for _, row := range r.Rows {
		totClicks += row.Clicks
		totImpr += row.Impressions
	}
	fmt.Fprintf(&b, "\nTotals of %d returned rows: %.0f clicks, %.0f impressions (avg CTR %.2f%%).\n",
		len(r.Rows), totClicks, totImpr, map[bool]float64{true: totClicks / totImpr * 100, false: 0}[totImpr > 0])
	if len(r.Rows) == body.RowLimit {
		b.WriteString("Note: exactly rowLimit rows returned — results are likely truncated; increase rowLimit or paginate with startRow.\n")
	}
	return b.String()
}

func typeLabel(t string) string {
	if t == "" {
		return "web"
	}
	return t
}

// saveAnalyticsCSV writes rows as CSV and returns a summary with preview.
func saveAnalyticsCSV(path, site string, body analyticsQueryBody, r analyticsResponse) (string, error) {
	dims := body.Dimensions
	if len(dims) == 0 {
		dims = []string{"date"}
	}
	var sb strings.Builder
	sb.WriteString(strings.Join(dims, ",") + ",clicks,impressions,ctr,position\n")
	for _, row := range r.Rows {
		cells := make([]string, 0, len(dims)+4)
		for _, k := range row.Keys {
			cells = append(cells, render.CSVCell(k))
		}
		cells = append(cells,
			fmt.Sprintf("%.0f", row.Clicks),
			fmt.Sprintf("%.0f", row.Impressions),
			fmt.Sprintf("%.6f", row.Ctr),
			fmt.Sprintf("%.2f", row.Position),
		)
		sb.WriteString(strings.Join(cells, ",") + "\n")
	}
	return render.SaveCSV(path, sb.String())
}

// ---- URL inspection rendering ----

// maxInspectURLs caps gsc_inspect_urls; the API allows 600 inspections per
// minute per property, so this many in flight is safe.
const maxInspectURLs = 10

type urlInspection struct {
	url    string
	result inspectionResult
	err    error
}

// renderInspections summarizes several inspections: counts per problem,
// then one table row per URL in input order.
func renderInspections(site string, rs []urlInspection) string {
	var indexed, failed int
	counts := map[string]int{}
	rows := make([][]string, 0, len(rs))
	for _, r := range rs {
		if r.err != nil {
			failed++
			rows = append(rows, []string{r.url, "ERROR", "—", r.err.Error()})
			continue
		}
		issues := inspectionIssues(r.result)
		if len(issues) == 0 {
			indexed++
		}
		for _, iss := range issues {
			counts[strings.SplitN(iss, ":", 2)[0]]++
		}
		verdict, coverage := "—", "—"
		if s := r.result.IndexStatusResult; s != nil {
			verdict, coverage = render.OrDash(s.Verdict), render.OrDash(s.CoverageState)
		}
		rows = append(rows, []string{r.url, verdict, coverage, render.OrDash(strings.Join(issues, "; "))})
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Inspected %d URL(s) of %s: %d indexed without issues", len(rs), site, indexed)
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, ", %d %s", counts[k], k)
	}
	if failed > 0 {
		fmt.Fprintf(&b, ", %d failed", failed)
	}
	b.WriteString(".\n\n| URL | Verdict | Coverage | Issues |\n|---|---|---|---|\n")
	for _, row := range rows {
		for i, cell := range row {
			row[i] = render.MDCell(cell)
		}
		b.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	return b.String()
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func renderInspection(r inspectionResult) string {
	var b strings.Builder
	b.WriteString("URL inspection result:\n")
	if r.InspectionResultLink != "" {
		fmt.Fprintf(&b, "  full report: %s\n", r.InspectionResultLink)
	}
	if s := r.IndexStatusResult; s != nil {
		fmt.Fprintf(&b, "\nIndex status: verdict=%s\n", s.Verdict)
		fmt.Fprintf(&b, "  coverage: %s\n", s.CoverageState)
		if s.RobotsTxtState != "" {
			fmt.Fprintf(&b, "  robots.txt: %s\n", s.RobotsTxtState)
		}
		if s.IndexingState != "" {
			fmt.Fprintf(&b, "  indexing: %s\n", s.IndexingState)
		}
		if s.PageFetchState != "" {
			fmt.Fprintf(&b, "  page fetch: %s\n", s.PageFetchState)
		}
		if s.LastCrawlTime != "" {
			fmt.Fprintf(&b, "  last crawl: %s", s.LastCrawlTime)
			if s.CrawledAs != "" {
				b.WriteString(" (crawled as " + s.CrawledAs + ")")
			}
			b.WriteString("\n")
		}
		if s.GoogleCanonical != "" || s.UserCanonical != "" {
			fmt.Fprintf(&b, "  canonical: user=%s | google=%s\n", s.UserCanonical, s.GoogleCanonical)
		}
		if len(s.Sitemap) > 0 {
			fmt.Fprintf(&b, "  sitemaps: %s\n", strings.Join(s.Sitemap, ", "))
		}
		if len(s.ReferringRobotsTxtUserAgents) > 0 {
			fmt.Fprintf(&b, "  referring robots.txt: %s\n", strings.Join(s.ReferringRobotsTxtUserAgents, ", "))
		}
	}
	if a := r.AmpResult; a != nil {
		fmt.Fprintf(&b, "\nAMP: verdict=%s url=%s index=%s\n", a.Verdict, a.AmpURL, a.AMPIndexStatusVerdict)
	}
	if m := r.MobileUsabilityResult; m != nil {
		fmt.Fprintf(&b, "\nMobile usability: verdict=%s", m.Verdict)
		if len(m.Issues) > 0 {
			names := make([]string, len(m.Issues))
			for i, iss := range m.Issues {
				names[i] = iss.IssueType
			}
			b.WriteString("\n  issues: " + strings.Join(names, ", "))
		}
		b.WriteString("\n")
	}
	if rr := r.RichResultsResult; rr != nil {
		fmt.Fprintf(&b, "\nRich results: verdict=%s", rr.Verdict)
		for _, di := range rr.DetectedItems {
			names := make([]string, len(di.Items))
			for i, it := range di.Items {
				names[i] = it.Name
			}
			fmt.Fprintf(&b, "\n  · %s: %s", di.RichResultType, strings.Join(names, ", "))
			for _, it := range di.Items {
				for _, iss := range it.Issues {
					fmt.Fprintf(&b, "\n      %s: %s", iss.Severity, iss.IssueMessage)
					if it.Name != "" {
						b.WriteString(" (" + it.Name + ")")
					}
				}
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
