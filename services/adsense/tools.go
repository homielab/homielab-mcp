package adsense

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/daterange"
	"github.com/homielab/homielab-mcp/internal/mcpkit"
	"github.com/homielab/homielab-mcp/internal/render"
)

const reportDimsAndMetrics = `
Dimensions (grouping): DATE, WEEK, MONTH, DOMAIN_NAME, OWNED_SITE_DOMAIN_NAME, PAGE_URL,
AD_UNIT_ID, AD_UNIT_NAME, AD_UNIT_SIZE_NAME, AD_FORMAT_NAME, AD_PLACEMENT_NAME,
PRODUCT_NAME, PRODUCT_CODE, CUSTOM_CHANNEL_NAME, URL_CHANNEL_NAME, COUNTRY_NAME, COUNTRY_CODE,
PLATFORM_TYPE_NAME, OS_TYPE_NAME, BROWSER_TYPE_NAME, CONTENT_PLATFORM_NAME, TARGETING_TYPE_NAME,
BID_TYPE_NAME, CREATIVE_SIZE_NAME, BUYER_NETWORK_NAME, TRAFFIC_SOURCE_NAME, REQUESTED_AD_TYPE_NAME,
SERVED_AD_TYPE_NAME, WEBSEARCH_QUERY_STRING, ACCOUNT_NAME, AD_CLIENT_ID.

Metrics (values): ESTIMATED_EARNINGS, PAGE_VIEWS, PAGE_VIEWS_RPM, IMPRESSIONS, IMPRESSIONS_RPM,
INDIVIDUAL_AD_IMPRESSIONS, INDIVIDUAL_AD_IMPRESSIONS_RPM, CLICKS, COST_PER_CLICK, IMPRESSIONS_CTR,
PAGE_VIEWS_CTR, AD_REQUESTS, MATCHED_AD_REQUESTS, AD_REQUESTS_COVERAGE, AD_REQUESTS_RPM,
MATCHED_AD_REQUESTS_RPM, ACTIVE_VIEW_MEASURABILITY, ACTIVE_VIEW_VIEWABILITY, ACTIVE_VIEW_TIME,
ADS_PER_IMPRESSION, TOTAL_EARNINGS.`

type emptyArgs = mcpkit.EmptyArgs

func textResult(s string) (*mcp.CallToolResult, any, error) { return mcpkit.TextResult(s) }

func errResult(err error) (*mcp.CallToolResult, any, error) { return mcpkit.ErrResult(auth, err) }

// mustClient returns an API client or an error result.
func mustClient(ctx context.Context) (*adsenseClient, *mcp.CallToolResult) {
	c, err := newAdsenseClient(ctx)
	if err != nil {
		res, _, _ := errResult(err)
		return nil, res
	}
	return c, nil
}

func registerTools(s *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcpkit.AddLoginTools(s, auth, mcpkit.LoginTools{
		Description: "Authorize access to the Google AdSense account: opens the browser at Google's OAuth consent page and stores the refresh token locally. Non-blocking: returns immediately while approval is pending — call adsense_login_status afterwards. Required once before any other tool. Scope is read-only.",
		Authorized: func(ctx context.Context) (string, bool) {
			c, err := newAdsenseClient(ctx)
			if err != nil {
				return "", false
			}
			accs, err := c.listAccounts(ctx)
			if err != nil || len(accs) == 0 {
				return "", false
			}
			return fmt.Sprintf("Already authorized as %s (%s). No need to log in again — call adsense_logout first to re-authorize.",
				accs[0].DisplayName, accs[0].Name), true
		},
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_list_accounts",
		Title:       "List AdSense Accounts",
		Description: "List AdSense accounts accessible to the logged-in Google account. Returns account resource names (accounts/pub-XXXX) to pass to other tools.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		accs, err := c.listAccounts(ctx)
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d account(s):\n", len(accs))
		for _, a := range accs {
			fmt.Fprintf(&b, "- %s  (%s)%s\n", a.Name, a.DisplayName, premiumSuffix(a.Premium))
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_get_account",
		Title:       "Get AdSense Account",
		Description: "Get one AdSense account's details (display name, time zone, premium status).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"accountId,omitempty" jsonschema:"Account like pub-1234567890123456 or accounts/pub-1234567890123456. Omit to use the first accessible account."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		var out account
		if err := c.getJSON(ctx, "/"+acc, nil, &out); err != nil {
			return errResult(err)
		}
		return textResult(fmt.Sprintf("%s\n  displayName: %s\n  timeZone: %s\n  premium: %v",
			out.Name, out.DisplayName, out.TimeZone.ID, out.Premium))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "adsense_generate_report",
		Title: "Generate AdSense Report",
		Description: "Generate an AdSense performance report for any date range, grouped by dimensions, with metrics. " +
			"Typical combos: sites performance [dimensions=DOMAIN_NAME], top pages [dimensions=PAGE_URL], " +
			"ad units [dimensions=AD_UNIT_NAME,AD_UNIT_ID], countries [dimensions=COUNTRY_NAME], " +
			"daily trend [dimensions=DATE], platforms [dimensions=PLATFORM_TYPE_NAME], " +
			"ad formats [dimensions=AD_FORMAT_NAME], bid types [dimensions=BID_TYPE_NAME], " +
			"traffic sources [dimensions=TRAFFIC_SOURCE_NAME]. " +
			reportDimsAndMetrics +
			"\nfilters: repeated conditions like \"DOMAIN_NAME==example.com\" or \"AD_UNIT_ID==ca-pub-XXX:123456\". " +
			"orderBy: a dimension or metric, + ascending / - descending, e.g. \"-ESTIMATED_EARNINGS\" or \"+DATE\". " +
			"For common analyses prefer adsense_preset_report.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args reportArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, q, err := reportQuery(ctx, c, args, 100000)
		if err != nil {
			return errResult(err)
		}
		r, _, err := c.generateReport(ctx, acc, q, false)
		if err != nil {
			return errResult(err)
		}
		return textResult(renderReport(acc, r, 50))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_generate_report_csv",
		Title:       "Export AdSense Report CSV",
		Description: "Same as adsense_generate_report but returns the raw CSV export (up to 1,000,000 rows). Use for full-detail exports.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args reportArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, q, err := reportQuery(ctx, c, args, 1000000)
		if err != nil {
			return errResult(err)
		}
		_, csv, err := c.generateReport(ctx, acc, q, true)
		if err != nil {
			return errResult(err)
		}
		if args.SaveToPath != "" {
			msg, err := render.SaveCSV(args.SaveToPath, csv)
			if err != nil {
				return errResult(err)
			}
			return textResult(msg)
		}
		return textResult(inlineCSV(csv, 200))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "adsense_preset_report",
		Title: "Run AdSense Preset Reports",
		Description: "Run one or more predefined AdSense reports in a single call — the fastest way to build a full performance picture. " +
			"Available presets: " + presetNames + ". " +
			"Each section comes back as a markdown table with earnings, views, impressions, clicks and RPM, sorted by earnings (or by date for daily/weekly/monthly). " +
			"A preset whose dimension combination the API rejects is reported inline without aborting the other presets.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args presetArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		if len(args.Presets) == 0 {
			return errResult(fmt.Errorf("no presets given — pick one or more of: %s", presetNames))
		}
		var b strings.Builder
		for i, name := range args.Presets {
			p, ok := reportPresets[strings.ToLower(strings.TrimSpace(name))]
			if !ok {
				fmt.Fprintf(&b, "\n## Unknown preset %q\nValid presets: %s\n", name, presetNames)
				continue
			}
			if i > 0 {
				b.WriteString("\n\n---\n\n")
			}
			fmt.Fprintf(&b, "## %s\n", p.title)
			ra := reportArgs{
				AccountID:  acc,
				DateRange:  args.DateRange,
				StartDate:  args.StartDate,
				EndDate:    args.EndDate,
				Dimensions: p.dimensions,
				Metrics:    p.metrics,
				OrderBy:    p.orderBy,
				Limit:      args.Limit,
			}
			if args.FilterDomain != "" && p.acceptsDomainFilter {
				ra.Filters = []string{"DOMAIN_NAME==" + args.FilterDomain}
			}
			acc2, q, err := reportQuery(ctx, c, ra, p.defaultLimit)
			if err != nil {
				fmt.Fprintf(&b, "⚠ %v\n", err)
				continue
			}
			r, _, err := c.generateReport(ctx, acc2, q, false)
			if err != nil {
				fmt.Fprintf(&b, "⚠ this preset failed (%v) — try adsense_generate_report with dimensions=%s instead\n", err, strings.Join(p.dimensions, ","))
				continue
			}
			b.WriteString(renderReport(acc2, r, presetRenderRows))
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_list_ad_clients",
		Title:       "List Ad Clients",
		Description: "List ad clients of the account (AFC = AdSense for Content, AFS = AdSense for Search, ...). Pass one as adClientId to tools like adsense_list_ad_units or adsense_list_channels.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		acs, err := c.listAdClients(ctx, acc)
		if err != nil {
			return errResult(err)
		}
		if len(acs) == 0 {
			return textResult("No ad clients in " + acc + ".")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d ad client(s):\n", len(acs))
		for _, a := range acs {
			fmt.Fprintf(&b, "- %s  product=%s  state=%s\n", a.id(), render.OrDash(a.ProductCode), a.State)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_list_sites",
		Title:       "List AdSense Sites",
		Description: "List sites (domains) registered in the AdSense account with their review state and auto-ads status.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		sites, err := c.listSites(ctx, acc)
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d site(s):\n", len(sites))
		for _, s := range sites {
			fmt.Fprintf(&b, "- %s  state=%s  autoAds=%v  id=%s\n", s.Domain, s.State, s.AutoAdsEnabled, s.ReportingDimensionID)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_list_ad_units",
		Title:       "List Ad Units",
		Description: "List ad units of an ad client (AFC = AdSense for Content).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID  string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
		AdClientID string `json:"adClientId,omitempty" jsonschema:"Ad client like ca-pub-XXXX. Omit to auto-pick the content ad client."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		client := args.AdClientID
		if client == "" {
			var err error
			if client, err = firstContentAdClient(ctx, c, acc); err != nil {
				return errResult(err)
			}
			if client == "" {
				return errResult(fmt.Errorf("no ad clients found in %s", acc))
			}
		} else {
			client = normalizeAdClient(acc, client)
		}
		units, err := c.listAdUnits(ctx, client)
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d ad unit(s) in %s:\n", len(units), client)
		for _, u := range units {
			fmt.Fprintf(&b, "- %s  code=%s  id=%s  %s %s  state=%s\n", u.DisplayName, u.code(), u.ReportingDimensionID,
				u.ContentAdsSettings.Type, u.ContentAdsSettings.Size, u.State)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_list_payments",
		Title:       "List Payments",
		Description: "List the latest AdSense payment records (dates, amounts, currency).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		pays, err := c.listPayments(ctx, acc)
		if err != nil {
			return errResult(err)
		}
		if len(pays) == 0 {
			return textResult("No payment records yet.")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d payment record(s):\n", len(pays))
		for _, p := range pays {
			if p.Date != nil {
				fmt.Fprintf(&b, "- %s paid %s\n", p.Amount, p.Date)
			} else {
				fmt.Fprintf(&b, "- %s %s (not yet paid)\n", p.Amount, p.Name[strings.LastIndex(p.Name, "/")+1:])
			}
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_list_alerts",
		Title:       "List Account Alerts",
		Description: "List active AdSense account alerts (policy notices, payment issues, severity).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		alerts, err := c.listAlerts(ctx, acc)
		if err != nil {
			return errResult(err)
		}
		if len(alerts) == 0 {
			return textResult("No active alerts.")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d alert(s):\n", len(alerts))
		for _, a := range alerts {
			fmt.Fprintf(&b, "- [%s] %s\n", a.Severity, a.Message)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "adsense_period_comparison",
		Description: "Period-over-period comparison of AdSense performance: runs the same report for the given window and the immediately preceding window of equal length, then reports per-key deltas (per metric) sorted by absolute change of the first metric. " +
			"Best with grouping dimensions like DOMAIN_NAME, AD_UNIT_NAME, PAGE_URL, COUNTRY_NAME, PLATFORM_TYPE_NAME, AD_FORMAT_NAME — not DATE (rows never match across periods).",
		Title:       "Compare AdSense Periods",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args comparisonArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		if len(args.Dimensions) == 0 {
			args.Dimensions = []string{"DOMAIN_NAME"}
		}
		if hasDateDimension(args.Dimensions) {
			return errResult(fmt.Errorf("use non-date dimensions (DOMAIN_NAME, AD_UNIT_NAME, ...) so both periods can be matched"))
		}
		if len(args.Metrics) == 0 {
			args.Metrics = []string{"ESTIMATED_EARNINGS", "IMPRESSIONS", "CLICKS"}
		}
		currArgs := reportArgs{
			AccountID:  acc,
			DateRange:  args.DateRange,
			StartDate:  args.StartDate,
			EndDate:    args.EndDate,
			Dimensions: args.Dimensions,
			Metrics:    args.Metrics,
			Filters:    args.Filters,
			Limit:      args.Limit,
		}
		acc2, currQ, err := reportQuery(ctx, c, currArgs, 10000)
		if err != nil {
			return errResult(err)
		}
		// resolveDateRange materialized the window; recover it from the query
		currStart, currEnd := currQ.Get("startDate.year"), currQ.Get("endDate.year")
		if currQ.Get("dateRange") != "CUSTOM" {
			// materialize preset windows locally to derive the previous window
			ps, pe, serr := daterange.Preset(currArgs.DateRange, time.Now())
			if serr != nil {
				return errResult(serr)
			}
			currStart, currEnd = ps.Format(daterange.Layout), pe.Format(daterange.Layout)
		} else {
			currStart = fmt.Sprintf("%s-%s-%s", currStart, currQ.Get("startDate.month"), currQ.Get("startDate.day"))
			currEnd = fmt.Sprintf("%s-%s-%s", currEnd, currQ.Get("endDate.month"), currQ.Get("endDate.day"))
		}
		prevStart, prevEnd, err := daterange.PreviousStrings(currStart, currEnd)
		if err != nil {
			return errResult(err)
		}
		prevArgs := currArgs
		prevArgs.DateRange = "CUSTOM"
		prevArgs.StartDate, prevArgs.EndDate = prevStart, prevEnd
		_, prevQ, err := reportQuery(ctx, c, prevArgs, 10000)
		if err != nil {
			return errResult(err)
		}
		prevRes, _, err := c.generateReport(ctx, acc2, prevQ, false)
		if err != nil {
			return errResult(fmt.Errorf("previous period (%s → %s): %w", prevStart, prevEnd, err))
		}
		currRes, _, err := c.generateReport(ctx, acc2, currQ, false)
		if err != nil {
			return errResult(err)
		}
		merged := mergeReportPeriods(prevRes, currRes, len(args.Dimensions))
		var b strings.Builder
		fmt.Fprintf(&b, "Period comparison for %s: current %s → %s vs previous %s → %s — %d matched key(s).\n\n",
			acc2, currStart, currEnd, prevStart, prevEnd, len(merged))
		metricNames := make([]string, 0, len(currRes.Headers)-len(args.Dimensions))
		for i, h := range currRes.Headers {
			if i >= len(args.Dimensions) {
				metricNames = append(metricNames, h.Name)
			}
		}
		b.WriteString(renderPeriodComparison(merged, metricNames, 50))
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_list_saved_reports",
		Title:       "List Saved Reports",
		Description: "List the reports you saved in the AdSense UI (custom reports with your own dimensions/metrics/filters). Use adsense_generate_saved_report to run one.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		reports, err := c.listSavedReports(ctx, acc)
		if err != nil {
			return errResult(err)
		}
		if len(reports) == 0 {
			return textResult("No saved reports. Save one in the AdSense UI (Reports → ⋮ on a report → Save) and list again.")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d saved report(s):\n", len(reports))
		for _, r := range reports {
			id := r.ID
			if id == "" {
				id = r.Name
			}
			fmt.Fprintf(&b, "- %s  id=%s\n", r.Title, id)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_generate_saved_report",
		Title:       "Run Saved Report",
		Description: "Run one of your saved AdSense reports (from the UI) for a date range. Optionally returns CSV. Only the date range can be overridden — dimensions/metrics/filters come from the saved definition.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args savedReportArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		name, err := c.resolveSavedReport(ctx, acc, args.SavedReportID)
		if err != nil {
			return errResult(err)
		}
		q, err := savedReportQuery(args)
		if err != nil {
			return errResult(err)
		}
		if args.Csv {
			_, csv, err := c.generateSavedReport(ctx, name, q, true)
			if err != nil {
				return errResult(err)
			}
			return textResult(inlineCSV(csv, 200))
		}
		r, _, err := c.generateSavedReport(ctx, name, q, false)
		if err != nil {
			return errResult(err)
		}
		return textResult(renderReport(name, r, 50))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_list_policy_issues",
		Title:       "List Policy Issues",
		Description: "List AdSense policy issues for the account (Policy center: sites with violations that must be fixed before ads can serve).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		items, err := c.rawItems(ctx, acc+"/policyIssues", "policyIssues", url.Values{"pageSize": {"1000"}})
		if err != nil {
			return errResult(err)
		}
		if len(items) == 0 {
			return textResult("No policy issues reported.")
		}
		return textResult(formatRawItems(fmt.Sprintf("%d policy issue(s)", len(items)), items))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_list_channels",
		Title:       "List Channels",
		Description: "List custom channels or URL channels of an ad client (reporting groupings you defined in the UI).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID  string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
		AdClientID string `json:"adClientId,omitempty" jsonschema:"Ad client like ca-pub-XXXX. Omit to auto-pick the content ad client."`
		Type       string `json:"type,omitempty" jsonschema:"\"custom\" (default) or \"url\"."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		client := args.AdClientID
		if client == "" {
			client, err = firstContentAdClient(ctx, c, acc)
			if err != nil {
				return errResult(err)
			}
		} else {
			client = normalizeAdClient(acc, client)
		}
		chType := strings.ToLower(strings.TrimSpace(args.Type))
		if chType == "" {
			chType = "custom"
		}
		var chans []channel
		switch chType {
		case "custom":
			chans, err = c.listCustomChannels(ctx, client)
		case "url":
			chans, err = c.listURLChannels(ctx, client)
		default:
			return errResult(fmt.Errorf("type must be \"custom\" or \"url\", got %q", chType))
		}
		if err != nil {
			return errResult(err)
		}
		if len(chans) == 0 {
			return textResult(fmt.Sprintf("No %s channels in %s.", chType, client))
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d %s channel(s):\n", len(chans), chType)
		for _, ch := range chans {
			line := "- " + ch.Name
			if ch.DisplayName != "" {
				line += "  (" + ch.DisplayName + ")"
			}
			if ch.URIPattern != "" {
				line += "  pattern=" + ch.URIPattern
			}
			b.WriteString(line + "\n")
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_get_ad_code",
		Title:       "Get Ad Code",
		Description: "Get the HTML ad code snippet of one ad unit (by ad unit code, e.g. 1234567890, by display name, or full resource name). Paste it into a site to serve that unit.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID   string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
		AdUnitCode  string `json:"adUnitCode,omitempty" jsonschema:"Ad unit code (the numeric part) or full ad unit resource name."`
		AdClientID  string `json:"adClientId,omitempty" jsonschema:"Ad client like ca-pub-XXXX; omit to auto-pick the content ad client."`
		DisplayName string `json:"displayName,omitempty" jsonschema:"Alternatively match an ad unit by its display name."`
		ClientLevel bool   `json:"clientLevel,omitempty" jsonschema:"true to return the client-level (account-wide) ad code instead of a specific ad unit's."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		if args.ClientLevel {
			client := args.AdClientID
			if client == "" {
				if client, err = firstContentAdClient(ctx, c, acc); err != nil {
					return errResult(err)
				}
			} else {
				client = normalizeAdClient(acc, client)
			}
			code, err := c.adCode(ctx, client)
			if err != nil {
				return errResult(err)
			}
			return textResult("Client-level ad code for " + client + ":\n\n```html\n" + code + "\n```")
		}
		var unitName string
		switch {
		case strings.HasPrefix(args.AdUnitCode, "accounts/"):
			unitName = args.AdUnitCode
		case args.AdUnitCode != "":
			client := args.AdClientID
			if client == "" {
				if client, err = firstContentAdClient(ctx, c, acc); err != nil {
					return errResult(err)
				}
			} else {
				client = normalizeAdClient(acc, client)
			}
			unitName = client + "/adunits/" + args.AdUnitCode
		case args.DisplayName != "":
			client := args.AdClientID
			if client == "" {
				if client, err = firstContentAdClient(ctx, c, acc); err != nil {
					return errResult(err)
				}
			} else {
				client = normalizeAdClient(acc, client)
			}
			units, uerr := c.listAdUnits(ctx, client)
			if uerr != nil {
				return errResult(uerr)
			}
			for _, u := range units {
				if strings.EqualFold(u.DisplayName, args.DisplayName) {
					unitName = u.Name
					break
				}
			}
			if unitName == "" {
				return errResult(fmt.Errorf("no ad unit named %q found", args.DisplayName))
			}
		default:
			return errResult(fmt.Errorf("provide adUnitCode (numeric code) or displayName"))
		}
		code, err := c.adCode(ctx, unitName)
		if err != nil {
			return errResult(err)
		}
		return textResult("Ad code for " + unitName + ":\n\n```html\n" + code + "\n```")
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_get_ad_blocking_recovery_tag",
		Title:       "Get Ad Blocking Recovery Tag",
		Description: "Get the ad-blocking recovery tag script for the account (shown to ad-blocker users to allow ads).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		body, _, err := c.get(ctx, acc+"/adBlockingRecoveryTag", nil)
		if err != nil {
			return errResult(err)
		}
		return textResult("Ad blocking recovery tag response:\n\n```html\n" + string(body) + "\n```")
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_list_child_accounts",
		Title:       "List Child Accounts",
		Description: "List child (sub-)accounts of an AdSense account, if any.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		kids, err := c.listChildAccounts(ctx, acc)
		if err != nil {
			return errResult(err)
		}
		if len(kids) == 0 {
			return textResult("No child accounts.")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d child account(s):\n", len(kids))
		for _, k := range kids {
			fmt.Fprintf(&b, "- %s  (%s)%s\n", k.Name, k.DisplayName, premiumSuffix(k.Premium))
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "adsense_list_channel_links",
		Title:       "List Channel Links",
		Description: "Show the link between ad units and custom channels: either which custom channels an ad unit belongs to, or which ad units a custom channel contains.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID     string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
		AdClientID    string `json:"adClientId,omitempty" jsonschema:"Ad client like ca-pub-XXXX. Omit to auto-pick the content ad client."`
		AdUnitCode    string `json:"adUnitCode,omitempty" jsonschema:"Ad unit code (or full resource name) — list the custom channels it belongs to."`
		CustomChannel string `json:"customChannel,omitempty" jsonschema:"Custom channel resource name (from adsense_list_channels) — list the ad units it contains."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		acc, err := c.resolveAccount(ctx, args.AccountID)
		if err != nil {
			return errResult(err)
		}
		switch {
		case args.AdUnitCode != "":
			var unitName string
			if strings.HasPrefix(args.AdUnitCode, "accounts/") {
				unitName = args.AdUnitCode
			} else {
				client := args.AdClientID
				if client == "" {
					if client, err = firstContentAdClient(ctx, c, acc); err != nil {
						return errResult(err)
					}
				} else {
					client = normalizeAdClient(acc, client)
				}
				unitName = client + "/adunits/" + args.AdUnitCode
			}
			chans, err := c.listLinkedCustomChannels(ctx, unitName)
			if err != nil {
				return errResult(err)
			}
			if len(chans) == 0 {
				return textResult(unitName + " belongs to no custom channels.")
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%s belongs to %d custom channel(s):\n", unitName, len(chans))
			for _, ch := range chans {
				b.WriteString("- " + ch.Name)
				if ch.DisplayName != "" {
					b.WriteString("  (" + ch.DisplayName + ")")
				}
				b.WriteString("\n")
			}
			return textResult(b.String())
		case args.CustomChannel != "":
			cc := args.CustomChannel
			if !strings.HasPrefix(cc, "accounts/") {
				client := args.AdClientID
				if client == "" {
					if client, err = firstContentAdClient(ctx, c, acc); err != nil {
						return errResult(err)
					}
				} else {
					client = normalizeAdClient(acc, client)
				}
				cc = client + "/customchannels/" + cc
			}
			units, err := c.listLinkedAdUnits(ctx, cc)
			if err != nil {
				return errResult(err)
			}
			if len(units) == 0 {
				return textResult(cc + " contains no ad units.")
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%s contains %d ad unit(s):\n", cc, len(units))
			for _, u := range units {
				fmt.Fprintf(&b, "- %s  (%s)\n", u.Name, u.DisplayName)
			}
			return textResult(b.String())
		default:
			return errResult(fmt.Errorf("provide adUnitCode or customChannel"))
		}
	})
}

// firstContentAdClient picks the AdSense for Content (AFC) ad client.
func firstContentAdClient(ctx context.Context, c *adsenseClient, acc string) (string, error) {
	acs, err := c.listAdClients(ctx, acc)
	if err != nil {
		return "", err
	}
	for _, a := range acs {
		if a.ProductCode == "AFC" {
			return a.Name, nil
		}
	}
	if len(acs) > 0 {
		return acs[0].Name, nil
	}
	return "", fmt.Errorf("no ad clients found in %s", acc)
}

// formatRawItems renders generic JSON items compactly, one bullet per item.
func formatRawItems(heading string, items []json.RawMessage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Found %s:\n", heading)
	for _, it := range items {
		var buf bytes.Buffer
		if err := json.Compact(&buf, it); err != nil {
			b.WriteString("- " + string(it) + "\n")
			continue
		}
		b.WriteString("- " + buf.String() + "\n")
	}
	return b.String()
}

func premiumSuffix(p bool) string {
	if p {
		return "  [premium]"
	}
	return ""
}

func normalizeAdClient(acc, id string) string {
	id = strings.TrimSpace(id)
	if strings.HasPrefix(id, "accounts/") {
		return id
	}
	return acc + "/adclients/" + id
}

// normalizeOrderBy accepts "-metric.X" / "+dimension.X" (a common guess) and
// returns the API form "-X" / "+X".
func normalizeOrderBy(o string) string {
	o = strings.TrimSpace(o)
	sign := ""
	if strings.HasPrefix(o, "+") || strings.HasPrefix(o, "-") {
		sign, o = o[:1], o[1:]
	}
	o = strings.TrimPrefix(strings.TrimPrefix(o, "metric."), "dimension.")
	return sign + o
}

// ---- preset reports ----

type presetDef struct {
	title               string
	dimensions          []string
	metrics             []string
	orderBy             []string
	defaultLimit        int
	acceptsDomainFilter bool
}

const presetRenderRows = 100

var moneyAndEngagement = []string{"ESTIMATED_EARNINGS", "PAGE_VIEWS", "IMPRESSIONS", "CLICKS", "PAGE_VIEWS_RPM", "IMPRESSIONS_RPM"}
var engagementNoPageViews = []string{"ESTIMATED_EARNINGS", "IMPRESSIONS", "CLICKS", "IMPRESSIONS_RPM"}

var reportPresets = map[string]presetDef{
	"sites":            {"Estimated earnings by site", []string{"DOMAIN_NAME"}, moneyAndEngagement, []string{"-ESTIMATED_EARNINGS"}, 1000, false},
	"pages":            {"Top pages by estimated earnings", []string{"PAGE_URL"}, engagementNoPageViews, []string{"-ESTIMATED_EARNINGS"}, 100, false},
	"ad_units":         {"Estimated earnings by ad unit", []string{"AD_UNIT_NAME", "AD_UNIT_ID"}, engagementNoPageViews, []string{"-ESTIMATED_EARNINGS"}, 1000, false},
	"countries":        {"Estimated earnings by country", []string{"COUNTRY_NAME"}, engagementNoPageViews, []string{"-ESTIMATED_EARNINGS"}, 1000, false},
	"platforms":        {"Estimated earnings by platform (Desktop/Mobile/Tablet)", []string{"PLATFORM_TYPE_NAME"}, engagementNoPageViews, []string{"-ESTIMATED_EARNINGS"}, 100, false},
	"ad_formats":       {"Estimated earnings by ad format", []string{"AD_FORMAT_NAME"}, engagementNoPageViews, []string{"-ESTIMATED_EARNINGS"}, 100, false},
	"ad_sizes":         {"Estimated earnings by ad unit size", []string{"AD_UNIT_SIZE_NAME"}, engagementNoPageViews, []string{"-ESTIMATED_EARNINGS"}, 100, false},
	"bid_types":        {"Estimated earnings by bid type", []string{"BID_TYPE_NAME"}, []string{"ESTIMATED_EARNINGS", "IMPRESSIONS", "CLICKS", "COST_PER_CLICK", "IMPRESSIONS_RPM"}, []string{"-ESTIMATED_EARNINGS"}, 100, false},
	"traffic_sources":  {"Estimated earnings by traffic source", []string{"TRAFFIC_SOURCE_NAME"}, engagementNoPageViews, []string{"-ESTIMATED_EARNINGS"}, 100, false},
	"products":         {"Estimated earnings by product", []string{"PRODUCT_NAME"}, []string{"ESTIMATED_EARNINGS", "IMPRESSIONS", "CLICKS"}, []string{"-ESTIMATED_EARNINGS"}, 100, false},
	"content_platform": {"Estimated earnings by content platform (Web/AMP)", []string{"CONTENT_PLATFORM_NAME"}, engagementNoPageViews, []string{"-ESTIMATED_EARNINGS"}, 100, false},
	"os":               {"Estimated earnings by operating system", []string{"OS_TYPE_NAME"}, engagementNoPageViews, []string{"-ESTIMATED_EARNINGS"}, 100, false},
	"browsers":         {"Estimated earnings by browser", []string{"BROWSER_TYPE_NAME"}, engagementNoPageViews, []string{"-ESTIMATED_EARNINGS"}, 100, false},
	"daily":            {"Daily account trend", []string{"DATE"}, moneyAndEngagement, []string{"+DATE"}, 1000, false},
	"weekly":           {"Weekly account trend", []string{"WEEK"}, moneyAndEngagement, []string{"+WEEK"}, 1000, false},
	"monthly":          {"Monthly account trend", []string{"MONTH"}, moneyAndEngagement, []string{"+MONTH"}, 1000, false},
	"site_countries":   {"Earnings by site and country", []string{"DOMAIN_NAME", "COUNTRY_NAME"}, engagementNoPageViews, []string{"-ESTIMATED_EARNINGS"}, 1000, false},
	"site_daily":       {"Daily earnings by site", []string{"DOMAIN_NAME", "DATE"}, []string{"ESTIMATED_EARNINGS", "IMPRESSIONS", "CLICKS"}, []string{"+DATE", "-ESTIMATED_EARNINGS"}, 1000, true},
}

var presetNames = func() string {
	names := make([]string, 0, len(reportPresets))
	for n := range reportPresets {
		names = append(names, n)
	}
	for i := 1; i < len(names); i++ { // insertion sort: deterministic order for the tool description
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return strings.Join(names, ", ")
}()

type presetArgs struct {
	AccountID    string   `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
	Presets      []string `json:"presets" jsonschema:"Preset names to run, see tool description. Example: [\"sites\",\"pages\",\"countries\",\"daily\"]."`
	DateRange    string   `json:"dateRange,omitempty" jsonschema:"TODAY, YESTERDAY, MONTH_TO_DATE, YEAR_TO_DATE, LAST_7_DAYS, LAST_30_DAYS, LAST_MONTH or CUSTOM (default LAST_30_DAYS)."`
	StartDate    string   `json:"startDate,omitempty" jsonschema:"YYYY-MM-DD, with endDate, overrides dateRange."`
	EndDate      string   `json:"endDate,omitempty" jsonschema:"YYYY-MM-DD, with startDate, overrides dateRange."`
	FilterDomain string   `json:"filterDomain,omitempty" jsonschema:"Optional domain filter for presets that accept it, e.g. example.com."`
	Limit        int      `json:"limit,omitempty" jsonschema:"Max rows per preset (defaults: 1000 per preset, 100 for pages)."`
}

// ---- saved reports ----

type savedReportArgs struct {
	AccountID         string `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
	SavedReportID     string `json:"savedReportId" jsonschema:"Saved report id (or full resource name) from adsense_list_saved_reports."`
	DateRange         string `json:"dateRange,omitempty" jsonschema:"TODAY, YESTERDAY, MONTH_TO_DATE, YEAR_TO_DATE, LAST_7_DAYS, LAST_30_DAYS or CUSTOM. Omit to use the report's own default."`
	StartDate         string `json:"startDate,omitempty" jsonschema:"YYYY-MM-DD, with endDate, overrides dateRange."`
	EndDate           string `json:"endDate,omitempty" jsonschema:"YYYY-MM-DD, with startDate, overrides dateRange."`
	CurrencyCode      string `json:"currencyCode,omitempty" jsonschema:"ISO-4217 currency, defaults to the account currency."`
	ReportingTimeZone string `json:"reportingTimeZone,omitempty" jsonschema:"ACCOUNT_TIME_ZONE (default) or GOOGLE_TIME_ZONE."`
	LanguageCode      string `json:"languageCode,omitempty" jsonschema:"BCP-47 language for localized names, default en."`
	Csv               bool   `json:"csv,omitempty" jsonschema:"true to return the raw CSV export instead of a markdown table."`
}

// savedReportQuery builds the override params accepted by saved:generate.
func savedReportQuery(args savedReportArgs) (url.Values, error) {
	q := url.Values{}
	dr := strings.ToUpper(strings.TrimSpace(args.DateRange))
	// Explicit dates override the preset; one-sided or malformed dates are an
	// error instead of being silently ignored.
	switch {
	case daterange.Valid(args.StartDate) && daterange.Valid(args.EndDate):
		dr = "CUSTOM"
	case args.StartDate != "" || args.EndDate != "":
		return nil, fmt.Errorf("startDate and endDate must both be YYYY-MM-DD dates, got %q and %q", args.StartDate, args.EndDate)
	}
	switch {
	case dr == "":
		// omit → the saved report's own default applies
	case dr == "LAST_30_DAYS":
		q.Set("dateRange", dr)
	case dr == "CUSTOM":
		if !daterange.Valid(args.StartDate) || !daterange.Valid(args.EndDate) {
			return nil, fmt.Errorf("dateRange=CUSTOM requires startDate and endDate as YYYY-MM-DD")
		}
		q.Set("dateRange", "CUSTOM")
		q.Set("startDate.year", args.StartDate[:4])
		q.Set("startDate.month", args.StartDate[5:7])
		q.Set("startDate.day", args.StartDate[8:10])
		q.Set("endDate.year", args.EndDate[:4])
		q.Set("endDate.month", args.EndDate[5:7])
		q.Set("endDate.day", args.EndDate[8:10])
	case dr == "TODAY" || dr == "YESTERDAY" || dr == "MONTH_TO_DATE" || dr == "YEAR_TO_DATE" || dr == "LAST_7_DAYS":
		q.Set("dateRange", dr)
	default:
		return nil, fmt.Errorf("invalid dateRange %q", dr)
	}
	if args.CurrencyCode != "" {
		q.Set("currencyCode", args.CurrencyCode)
	}
	if args.ReportingTimeZone != "" {
		q.Set("reportingTimeZone", args.ReportingTimeZone)
	}
	if args.LanguageCode != "" {
		q.Set("languageCode", args.LanguageCode)
	}
	return q, nil
}

// ---- report plumbing shared by generate/generateCsv/presets ----

type reportArgs struct {
	AccountID         string   `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
	DateRange         string   `json:"dateRange,omitempty" jsonschema:"TODAY, YESTERDAY, MONTH_TO_DATE, YEAR_TO_DATE, LAST_7_DAYS, LAST_30_DAYS, LAST_MONTH or CUSTOM (default LAST_30_DAYS)."`
	StartDate         string   `json:"startDate,omitempty" jsonschema:"YYYY-MM-DD inclusive; required when dateRange=CUSTOM."`
	EndDate           string   `json:"endDate,omitempty" jsonschema:"YYYY-MM-DD inclusive; required when dateRange=CUSTOM."`
	Dimensions        []string `json:"dimensions,omitempty" jsonschema:"Dimension enum names, see tool description."`
	Metrics           []string `json:"metrics,omitempty" jsonschema:"Metric enum names, see tool description; defaults to ESTIMATED_EARNINGS."`
	Filters           []string `json:"filters,omitempty" jsonschema:"Filters like DOMAIN_NAME==example.com."`
	OrderBy           []string `json:"orderBy,omitempty" jsonschema:"Like -ESTIMATED_EARNINGS or +DATE."`
	Limit             int      `json:"limit,omitempty" jsonschema:"Max rows to return (default/max 100000 for JSON, 1000000 for CSV)."`
	Offset            int      `json:"offset,omitempty" jsonschema:"Row offset for pagination."`
	CurrencyCode      string   `json:"currencyCode,omitempty" jsonschema:"ISO-4217 currency, defaults to the account currency."`
	ReportingTimeZone string   `json:"reportingTimeZone,omitempty" jsonschema:"ACCOUNT_TIME_ZONE (default) or GOOGLE_TIME_ZONE."`
	LanguageCode      string   `json:"languageCode,omitempty" jsonschema:"BCP-47 language for localized names, default en."`
	SaveToPath        string   `json:"saveToPath,omitempty" jsonschema:"CSV tool only: write the export to this file instead of returning it in the response (recommended for large exports); returns the file path and a preview."`
}

type comparisonArgs struct {
	AccountID  string   `json:"accountId,omitempty" jsonschema:"Account like pub-XXXX. Omit for the first accessible account."`
	DateRange  string   `json:"dateRange,omitempty" jsonschema:"Preset applied to the current window (previous window is derived): TODAY, YESTERDAY, LAST_7_DAYS, LAST_30_DAYS, MONTH_TO_DATE, YEAR_TO_DATE, LAST_MONTH or CUSTOM (default LAST_30_DAYS)."`
	StartDate  string   `json:"startDate,omitempty" jsonschema:"YYYY-MM-DD current-window start (with endDate). Overrides dateRange."`
	EndDate    string   `json:"endDate,omitempty" jsonschema:"YYYY-MM-DD current-window end (with startDate). Overrides dateRange."`
	Dimensions []string `json:"dimensions,omitempty" jsonschema:"Grouping dimensions across periods, e.g. DOMAIN_NAME, AD_UNIT_NAME, COUNTRY_NAME (default DOMAIN_NAME). Not DATE."`
	Metrics    []string `json:"metrics,omitempty" jsonschema:"Metrics to compare (default ESTIMATED_EARNINGS, IMPRESSIONS, CLICKS)."`
	Filters    []string `json:"filters,omitempty" jsonschema:"Filters applied to both periods, all ANDed."`
	Limit      int      `json:"limit,omitempty" jsonschema:"Rows fetched per period (default 10000)."`
}

func hasDateDimension(dims []string) bool {
	for _, d := range dims {
		switch strings.ToUpper(strings.TrimSpace(d)) {
		case "DATE", "WEEK", "MONTH":
			return true
		}
	}
	return false
}

func reportQuery(ctx context.Context, c *adsenseClient, args reportArgs, defaultLimit int) (string, url.Values, error) {
	acc, err := c.resolveAccount(ctx, args.AccountID)
	if err != nil {
		return "", nil, err
	}
	dr := strings.ToUpper(strings.TrimSpace(args.DateRange))
	// Explicit dates override the preset; one-sided or malformed dates are an
	// error instead of being silently ignored.
	switch {
	case daterange.Valid(args.StartDate) && daterange.Valid(args.EndDate):
		dr = "CUSTOM"
	case args.StartDate != "" || args.EndDate != "":
		return "", nil, fmt.Errorf("startDate and endDate must both be YYYY-MM-DD dates, got %q and %q", args.StartDate, args.EndDate)
	}
	q := url.Values{}
	switch {
	case dr == "" || dr == "LAST_30_DAYS":
		q.Set("dateRange", "LAST_30_DAYS")
	case dr == "CUSTOM":
		if !daterange.Valid(args.StartDate) || !daterange.Valid(args.EndDate) {
			return "", nil, fmt.Errorf("dateRange=CUSTOM requires startDate and endDate as YYYY-MM-DD")
		}
		q.Set("dateRange", "CUSTOM")
		y, m, d := args.StartDate[:4], args.StartDate[5:7], args.StartDate[8:10]
		q.Set("startDate.year", y)
		q.Set("startDate.month", m)
		q.Set("startDate.day", d)
		y, m, d = args.EndDate[:4], args.EndDate[5:7], args.EndDate[8:10]
		q.Set("endDate.year", y)
		q.Set("endDate.month", m)
		q.Set("endDate.day", d)
	case dr == "TODAY" || dr == "YESTERDAY" || dr == "MONTH_TO_DATE" || dr == "YEAR_TO_DATE" || dr == "LAST_7_DAYS" || dr == "LAST_MONTH":
		q.Set("dateRange", dr)
	default:
		return "", nil, fmt.Errorf("invalid dateRange %q: use TODAY, YESTERDAY, MONTH_TO_DATE, YEAR_TO_DATE, LAST_7_DAYS, LAST_30_DAYS, LAST_MONTH or CUSTOM", dr)
	}
	for _, d := range args.Dimensions {
		q.Add("dimensions", d)
	}
	metrics := args.Metrics
	if len(metrics) == 0 {
		metrics = []string{"ESTIMATED_EARNINGS"}
	}
	for _, m := range metrics {
		q.Add("metrics", m)
	}
	for _, f := range args.Filters {
		q.Add("filters", f)
	}
	for _, o := range args.OrderBy {
		q.Add("orderBy", normalizeOrderBy(o))
	}
	limit := args.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	q.Set("limit", fmt.Sprint(limit))
	if args.Offset > 0 {
		q.Set("offset", fmt.Sprint(args.Offset))
	}
	if args.CurrencyCode != "" {
		q.Set("currencyCode", args.CurrencyCode)
	}
	if args.ReportingTimeZone != "" {
		q.Set("reportingTimeZone", args.ReportingTimeZone)
	}
	if args.LanguageCode != "" {
		q.Set("languageCode", args.LanguageCode)
	} else {
		q.Set("languageCode", "en")
	}
	return acc, q, nil
}

// inlineCSV caps a CSV export returned directly in the tool result: the full
// export belongs in a file via saveToPath, not in the model's context.
func inlineCSV(csv string, maxDataRows int) string {
	lines := strings.Split(strings.TrimRight(csv, "\n"), "\n")
	if len(lines) <= maxDataRows+1 { // +1 for the header line
		return csv
	}
	return strings.Join(lines[:maxDataRows+1], "\n") + "\n" +
		fmt.Sprintf("... (%d of %d data rows shown — pass saveToPath to write the full export to a file.)\n", maxDataRows, len(lines)-1)
}

// renderReport formats a ReportResult as a markdown table for the model/user,
// capping displayed rows and pointing at the CSV export for the rest.
func renderReport(acc string, r reportResult, maxRows int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Report for %s (%s to %s) — %s row(s) matched.\n\n",
		acc, r.StartDate, r.EndDate, r.TotalMatchedRows)

	names := make([]string, len(r.Headers))
	for i, h := range r.Headers {
		names[i] = h.Name
		if h.CurrencyCode != "" {
			names[i] += " (" + h.CurrencyCode + ")"
		}
	}
	b.WriteString("| " + strings.Join(names, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat(" --- |", len(names)) + "\n")

	shown := len(r.Rows)
	truncated := false
	if shown > maxRows {
		shown = maxRows
		truncated = true
	}
	for _, row := range r.Rows[:shown] {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = render.MDCell(c)
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	if truncated {
		fmt.Fprintf(&b, "\n(%d more rows omitted from this preview — use adsense_generate_report_csv, with saveToPath for large exports, for the complete result.)\n", len(r.Rows)-shown)
	}
	if len(r.Totals) == len(names) && len(r.Rows) > 1 {
		b.WriteString("\n**Totals:** " + strings.Join(r.Totals, " | ") + "\n")
	}
	if len(r.Averages) == len(names) && len(r.Rows) > 1 {
		b.WriteString("\n**Averages:** " + strings.Join(r.Averages, " | ") + "\n")
	}
	return b.String()
}
