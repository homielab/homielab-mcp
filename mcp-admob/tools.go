package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/daterange"
	"github.com/homielab/homielab-mcp/internal/mcpkit"
	"github.com/homielab/homielab-mcp/internal/render"
)

type emptyArgs = mcpkit.EmptyArgs

func textResult(s string) (*mcp.CallToolResult, any, error) { return mcpkit.TextResult(s) }

func errResult(err error) (*mcp.CallToolResult, any, error) { return mcpkit.ErrResult(auth, err) }

// session is a client plus the resolved account, or an error result.
func session(ctx context.Context, accountID string) (*admobClient, publisherAccount, *mcp.CallToolResult) {
	c, err := newAdmobClient(ctx)
	if err != nil {
		res, _, _ := errResult(err)
		return nil, publisherAccount{}, res
	}
	acc, err := c.resolveAccount(ctx, accountID)
	if err != nil {
		res, _, _ := errResult(err)
		return nil, publisherAccount{}, res
	}
	return c, acc, nil
}

type accountArgs struct {
	AccountID string `json:"accountId,omitempty" jsonschema:"Publisher id like pub-XXXX. Omit for the first accessible account."`
}

func registerTools(s *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcpkit.AddLoginTools(s, auth, mcpkit.LoginTools{
		Description: "Authorize access to Google AdMob: opens the browser at Google's OAuth consent page and stores the refresh token locally. Non-blocking: returns immediately while approval is pending — call admob_login_status afterwards. Required once before any other tool. Scope is read-only.",
		Authorized: func(ctx context.Context) (string, bool) {
			c, err := newAdmobClient(ctx)
			if err != nil {
				return "", false
			}
			accs, err := c.listAccounts(ctx)
			if err != nil || len(accs) == 0 {
				return "", false
			}
			return fmt.Sprintf("Already authorized for %s (%s). No need to log in again — call admob_logout first to re-authorize.",
				accs[0].PublisherID, accs[0].CurrencyCode), true
		},
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "admob_list_accounts",
		Title:       "List AdMob Accounts",
		Description: "List AdMob publisher accounts for the logged-in Google account, with reporting time zone and currency.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		c, err := newAdmobClient(ctx)
		if err != nil {
			return errResult(err)
		}
		accs, err := c.listAccounts(ctx)
		if err != nil {
			return errResult(err)
		}
		if len(accs) == 0 {
			return textResult("No AdMob account accessible for this Google login.")
		}
		var b strings.Builder
		for _, a := range accs {
			fmt.Fprintf(&b, "- %s — time zone %s, currency %s\n", a.PublisherID, a.ReportingTimeZone, a.CurrencyCode)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "admob_list_apps",
		Title:       "List AdMob Apps",
		Description: "List the apps of an AdMob account: name, app id (ca-app-pub-…~…), platform, store id and approval state.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args accountArgs) (*mcp.CallToolResult, any, error) {
		c, acc, res := session(ctx, args.AccountID)
		if res != nil {
			return res, nil, nil
		}
		apps, err := c.listApps(ctx, acc.Name)
		if err != nil {
			return errResult(err)
		}
		if len(apps) == 0 {
			return textResult("No apps in " + acc.PublisherID + ".")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d app(s) in %s:\n\n| App | App id | Platform | Store id | Approval |\n|---|---|---|---|---|\n", len(apps), acc.PublisherID)
		for _, a := range apps {
			store := "-"
			if a.LinkedAppInfo != nil && a.LinkedAppInfo.AppStoreID != "" {
				store = a.LinkedAppInfo.AppStoreID
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", render.MDCell(a.label()), a.AppID, a.Platform, store, a.AppApprovalState)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "admob_list_ad_units",
		Title:       "List AdMob Ad Units",
		Description: "List ad units of an AdMob account (optionally of one app): name, ad unit id, app, format and ad types.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"accountId,omitempty" jsonschema:"Publisher id like pub-XXXX. Omit for the first accessible account."`
		AppID     string `json:"appId,omitempty" jsonschema:"Only ad units of this app id (ca-app-pub-…~…), from admob_list_apps."`
	}) (*mcp.CallToolResult, any, error) {
		c, acc, res := session(ctx, args.AccountID)
		if res != nil {
			return res, nil, nil
		}
		units, err := c.listAdUnits(ctx, acc.Name)
		if err != nil {
			return errResult(err)
		}
		if args.AppID != "" {
			units = slices.DeleteFunc(units, func(u adUnit) bool { return u.AppID != args.AppID })
		}
		if len(units) == 0 {
			return textResult("No ad units found.")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d ad unit(s):\n\n| Ad unit | Ad unit id | App id | Format | Ad types |\n|---|---|---|---|---|\n", len(units))
		for _, u := range units {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", render.MDCell(u.DisplayName), u.AdUnitID, u.AppID, u.AdFormat, strings.Join(u.AdTypes, ", "))
		}
		return textResult(b.String())
	})

	for _, kind := range []string{"network", "mediation"} {
		desc := map[string]string{
			"network": "AdMob Network report: earnings and traffic served by the AdMob network, grouped by dimensions. ",
			"mediation": "AdMob Mediation report: performance per ad source (AdMob Network and third-party networks), " +
				"mediation group and ad source instance — observed eCPM, fill and latency. ",
		}[kind]
		mcp.AddTool(s, &mcp.Tool{
			Name:  "admob_" + kind + "_report",
			Title: "AdMob " + strings.ToUpper(kind[:1]) + kind[1:] + " Report",
			Description: desc + dimsHelp(kind) +
				" Earnings are in the account currency (or currencyCode); rates are shown as percentages. " +
				"Typical: daily trend [DATE], by app [APP], by ad unit [APP, AD_UNIT], by country [COUNTRY].",
			Annotations: readOnly,
		}, func(ctx context.Context, req *mcp.CallToolRequest, args reportArgs) (*mcp.CallToolResult, any, error) {
			c, acc, res := session(ctx, args.AccountID)
			if res != nil {
				return res, nil, nil
			}
			spec, err := buildSpec(kind, args, accountNow(acc))
			if err != nil {
				return errResult(err)
			}
			r, err := c.generateReport(ctx, acc.Name, kind, spec)
			if err != nil {
				return errResult(err)
			}
			if args.SaveToPath != "" {
				msg, err := render.SaveCSV(args.SaveToPath, reportCSV(r))
				if err != nil {
					return errResult(err)
				}
				return textResult(msg)
			}
			return textResult(renderReport(acc.PublisherID, r, 100))
		})
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:  "admob_preset_report",
		Title: "AdMob Preset Reports",
		Description: "Run several ready-made report cuts in ONE call — the fastest way to a broad AdMob picture. Presets: " + presetNames() + ". " +
			"Each preset comes back as its own table; a cut the API rejects is reported inline without aborting the others.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string              `json:"accountId,omitempty" jsonschema:"Publisher id like pub-XXXX. Omit for the first accessible account."`
		Presets   []string            `json:"presets" jsonschema:"Preset names, e.g. [\"daily\", \"apps\", \"ad_units\", \"countries\", \"formats\"]."`
		DateRange string              `json:"dateRange,omitempty" jsonschema:"TODAY, YESTERDAY, LAST_7_DAYS, LAST_30_DAYS (default), MONTH_TO_DATE, LAST_MONTH, YEAR_TO_DATE."`
		StartDate string              `json:"startDate,omitempty" jsonschema:"YYYY-MM-DD, with endDate; overrides dateRange."`
		EndDate   string              `json:"endDate,omitempty" jsonschema:"YYYY-MM-DD, with startDate; overrides dateRange."`
		Filters   map[string][]string `json:"filters,omitempty" jsonschema:"Applied to every preset, e.g. {\"APP\": [\"ca-app-pub-…~…\"]}."`
		MaxRows   int                 `json:"maxRows,omitempty" jsonschema:"Rows per preset shown (default 50)."`
	}) (*mcp.CallToolResult, any, error) {
		if len(args.Presets) == 0 {
			return errResult(fmt.Errorf("presets is required; choose from %s", presetNames()))
		}
		for _, p := range args.Presets {
			if _, ok := presets[p]; !ok {
				return errResult(fmt.Errorf("unknown preset %q; choose from %s", p, presetNames()))
			}
		}
		c, acc, res := session(ctx, args.AccountID)
		if res != nil {
			return res, nil, nil
		}
		show := args.MaxRows
		if show <= 0 {
			show = 50
		}
		now := accountNow(acc)
		var b strings.Builder
		for i, name := range args.Presets {
			p := presets[name]
			if i > 0 {
				b.WriteString("\n---\n\n")
			}
			fmt.Fprintf(&b, "## %s (%s)\n", p.title, name)
			spec, err := buildSpec(p.kind, reportArgs{
				DateRange: args.DateRange, StartDate: args.StartDate, EndDate: args.EndDate,
				Dimensions: p.dims, Filters: args.Filters,
			}, now)
			if err == nil {
				var r report
				if r, err = c.generateReport(ctx, acc.Name, p.kind, spec); err == nil {
					b.WriteString(renderReport(acc.PublisherID, r, show))
					continue
				}
			}
			fmt.Fprintf(&b, "⚠ this preset failed: %v\n", err)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "admob_period_comparison",
		Title: "AdMob Period Comparison",
		Description: "Compare a period with a baseline — by default the equal-length period right before it, or baselineStartDate/baselineEndDate (e.g. last year) — " +
			"per dimension key: earnings, impressions and eCPM with changes, sorted by the largest earnings change. " +
			"Use non-time dimensions, e.g. [APP], [AD_UNIT], [COUNTRY], [FORMAT], or [AD_SOURCE] with report \"mediation\".",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID         string              `json:"accountId,omitempty" jsonschema:"Publisher id like pub-XXXX. Omit for the first accessible account."`
		Report            string              `json:"report,omitempty" jsonschema:"network (default) or mediation."`
		Dimensions        []string            `json:"dimensions,omitempty" jsonschema:"Non-time dimensions to compare by; default [\"APP\"]."`
		DateRange         string              `json:"dateRange,omitempty" jsonschema:"Current period: TODAY, YESTERDAY, LAST_7_DAYS, LAST_30_DAYS (default), MONTH_TO_DATE, LAST_MONTH, YEAR_TO_DATE."`
		StartDate         string              `json:"startDate,omitempty" jsonschema:"YYYY-MM-DD current-period start (with endDate); overrides dateRange."`
		EndDate           string              `json:"endDate,omitempty" jsonschema:"YYYY-MM-DD current-period end (with startDate)."`
		Filters           map[string][]string `json:"filters,omitempty" jsonschema:"Applied to both periods, e.g. {\"APP\": [\"ca-app-pub-…~…\"]}."`
		CurrencyCode      string              `json:"currencyCode,omitempty" jsonschema:"ISO 4217 code for earnings; default the account currency."`
		BaselineStartDate string              `json:"baselineStartDate,omitempty" jsonschema:"YYYY-MM-DD baseline start (with baselineEndDate). Default: the period right before."`
		BaselineEndDate   string              `json:"baselineEndDate,omitempty" jsonschema:"YYYY-MM-DD baseline end (with baselineStartDate)."`
	}) (*mcp.CallToolResult, any, error) {
		kind := strings.ToLower(args.Report)
		if kind == "" {
			kind = "network"
		}
		if kind != "network" && kind != "mediation" {
			return errResult(fmt.Errorf("report must be network or mediation, got %q", args.Report))
		}
		ra := reportArgs{
			AccountID: args.AccountID, DateRange: args.DateRange, StartDate: args.StartDate, EndDate: args.EndDate,
			Dimensions: args.Dimensions, Metrics: comparisonMetrics, Filters: args.Filters,
			CurrencyCode: args.CurrencyCode, MaxRows: 100000,
		}
		if len(ra.Dimensions) == 0 {
			ra.Dimensions = []string{"APP"}
		}
		c, acc, res := session(ctx, ra.AccountID)
		if res != nil {
			return res, nil, nil
		}
		curr, err := buildSpec(kind, ra, accountNow(acc))
		if err != nil {
			return errResult(err)
		}
		if slices.ContainsFunc(curr.Dimensions, func(d string) bool { return slices.Contains(timeDims, d) }) {
			return errResult(fmt.Errorf("use non-time dimensions (APP, AD_UNIT, COUNTRY, ...) so both periods can be matched"))
		}
		curr.SortConditions = nil // merged rows are re-sorted by earnings change
		base := curr
		if args.BaselineStartDate != "" || args.BaselineEndDate != "" {
			s, e, err := daterange.Resolve("", args.BaselineStartDate, args.BaselineEndDate, accountNow(acc))
			if err != nil {
				return errResult(fmt.Errorf("baseline: %w", err))
			}
			base.DateRange.StartDate, base.DateRange.EndDate = toDate(s), toDate(e)
		} else {
			base.DateRange.StartDate, base.DateRange.EndDate = previousWindow(curr.DateRange.StartDate, curr.DateRange.EndDate)
		}
		prevR, err := c.generateReport(ctx, acc.Name, kind, base)
		if err != nil {
			return errResult(fmt.Errorf("baseline period: %w", err))
		}
		currR, err := c.generateReport(ctx, acc.Name, kind, curr)
		if err != nil {
			return errResult(err)
		}
		rows := mergeReports(prevR, currR)
		var b strings.Builder
		fmt.Fprintf(&b, "AdMob %s comparison for %s: current %s → %s vs baseline %s → %s — %d key(s).\n\n",
			kind, acc.PublisherID, curr.DateRange.StartDate, curr.DateRange.EndDate,
			base.DateRange.StartDate, base.DateRange.EndDate, len(rows))
		currency := currR.Currency
		if currency == "" {
			currency = acc.CurrencyCode
		}
		b.WriteString(renderComparison(curr.Dimensions, rows, currency, 50))
		return textResult(b.String())
	})
}
