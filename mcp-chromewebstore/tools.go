package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/daterange"
	"github.com/homielab/homielab-mcp/internal/mcpkit"
	"github.com/homielab/homielab-mcp/internal/render"
)

func textResult(s string) (*mcp.CallToolResult, any, error) { return mcpkit.TextResult(s) }

func errResult(err error) (*mcp.CallToolResult, any, error) { return mcpkit.ErrResult(auth, err) }

// errRes is errResult as a bare result, for helpers that return early.
func errRes(err error) *mcp.CallToolResult {
	res, _, _ := errResult(err)
	return res
}

// itemArgs are the arguments every item tool takes.
type itemArgs struct {
	PublisherID string `json:"publisherId,omitempty" jsonschema:"Publisher ID (Developer Dashboard → Publisher → Settings). Omit to use CWS_MCP_PUBLISHER_ID or the last one used."`
	ItemID      string `json:"itemId,omitempty" jsonschema:"32-letter extension id, or its Chrome Web Store URL. Omit to use the last one used."`
}

// withItem returns an API client and the resolved item, or an error result.
func withItem(ctx context.Context, a itemArgs) (*cwsClient, item, *mcp.CallToolResult) {
	it, err := resolveItem(a.PublisherID, a.ItemID)
	if err != nil {
		return nil, it, errRes(err)
	}
	c, err := newCWSClient(ctx)
	if err != nil {
		return nil, it, errRes(err)
	}
	return c, it, nil
}

var (
	metricNames    = []string{"installs", "installs-7d", "uninstalls", "users", "users-7d", "impressions", "page-views"}
	dimensionNames = []string{"os", "app-version", "country", "install-source", "install-type", "language", "enabled",
		"utm-campaign", "utm-content", "utm-medium", "utm-source", "utm-term"}
)

func registerTools(s *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	notDestructive, openWorld := false, true
	writeLike := &mcp.ToolAnnotations{DestructiveHint: &notDestructive}
	releaseLike := &mcp.ToolAnnotations{DestructiveHint: &notDestructive, OpenWorldHint: &openWorld}

	mcpkit.AddLoginTools(s, auth, mcpkit.LoginTools{
		Description: "Authorize access to the Chrome Web Store: opens the browser at Google's OAuth consent page and stores the refresh token locally. Non-blocking: returns immediately while approval is pending — call cws_login_status afterwards. Required once before any other tool. Scope: chromewebstore (read, upload and publish items).",
		ReadyNote:   " (needs publisherId and itemId once)",
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "cws_status",
		Title: "Item Status",
		Description: "Status of an item: published version and rollout %, the submitted revision under review (PENDING_REVIEW, STAGED, REJECTED, …), " +
			"the last async upload state, and policy flags (warned, taken down). Use after cws_upload / cws_publish.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args itemArgs) (*mcp.CallToolResult, any, error) {
		c, it, res := withItem(ctx, args)
		if res != nil {
			return res, nil, nil
		}
		st, err := c.fetchStatus(ctx, it)
		if err != nil {
			return errResult(err)
		}
		rememberItem(it)
		return textResult(renderStatus(it, st))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "cws_upload",
		Title: "Upload Package",
		Description: "Upload a new package to an existing item, from a .zip file or an extension folder (zipped in memory; dot files such as .git are left out; manifest.json must be at the root). " +
			"The manifest version must be higher than the published one. Does not publish: call cws_publish next. If the state is IN_PROGRESS, poll cws_status.",
		Annotations: writeLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		itemArgs
		Path string `json:"path" jsonschema:"Local path of the .zip package or of the extension folder (the one containing manifest.json)."`
	}) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Path) == "" {
			return errResult(fmt.Errorf("path is required: a .zip package or the extension folder"))
		}
		pkg, ver, err := loadPackage(args.Path)
		if err != nil {
			return errResult(err)
		}
		c, it, res := withItem(ctx, args.itemArgs)
		if res != nil {
			return res, nil, nil
		}
		up, err := c.upload(ctx, it, pkg)
		if err != nil {
			return errResult(fmt.Errorf("upload of version %s failed: %w", ver, err))
		}
		rememberItem(it)
		var b strings.Builder
		fmt.Fprintf(&b, "Uploaded %s (%s, manifest version %s) to item %s.\nUpload state: %s\n",
			args.Path, humanBytes(len(pkg)), ver, it.ItemID, up.UploadState)
		if up.CrxVersion != "" {
			fmt.Fprintf(&b, "Store version: %s\n", up.CrxVersion)
		}
		switch up.UploadState {
		case "SUCCEEDED":
			b.WriteString("Next: cws_publish to submit it for review.")
		case "IN_PROGRESS":
			b.WriteString("Processing continues in the background: poll cws_status (lastAsyncUploadState) before cws_publish.")
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "cws_publish",
		Title: "Publish Item",
		Description: "Submit the uploaded draft for review and publish it when approved, with the item's current visibility. " +
			"skipReview tries to skip review (fails if the item needs it). staged:true holds the approved version (state STAGED) until you call cws_publish again. " +
			"deployPercentage sets the initial rollout (default: the dashboard value). Warnings come back in the result; blockOnWarnings:true fails instead. " +
			"Confirm with the user first: this releases to users.",
		Annotations: releaseLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		itemArgs
		SkipReview       bool `json:"skipReview,omitempty" jsonschema:"Try to skip review; the API rejects the call if the item requires review."`
		Staged           bool `json:"staged,omitempty" jsonschema:"Stage on approval instead of publishing; publish later with another cws_publish."`
		DeployPercentage *int `json:"deployPercentage,omitempty" jsonschema:"Initial rollout percentage, 0-100."`
		BlockOnWarnings  bool `json:"blockOnWarnings,omitempty" jsonschema:"Fail instead of publishing when validation finds warnings."`
	}) (*mcp.CallToolResult, any, error) {
		body := map[string]any{}
		if args.SkipReview {
			body["skipReview"] = true
		}
		if args.Staged {
			body["publishType"] = "STAGED_PUBLISH"
		}
		if args.BlockOnWarnings {
			body["blockOnWarnings"] = true
		}
		if args.DeployPercentage != nil {
			if err := checkPercent(*args.DeployPercentage); err != nil {
				return errResult(err)
			}
			body["deployInfos"] = []map[string]int{{"deployPercentage": *args.DeployPercentage}}
		}
		c, it, res := withItem(ctx, args.itemArgs)
		if res != nil {
			return res, nil, nil
		}
		var out publishResponse
		if err := c.call(ctx, http.MethodPost, it.path()+":publish", nil, body, &out); err != nil {
			return errResult(err)
		}
		rememberItem(it)
		var b strings.Builder
		fmt.Fprintf(&b, "Submitted item %s. State: %s\n", it.ItemID, out.State)
		switch out.State {
		case "PENDING_REVIEW":
			b.WriteString("In review: check cws_status for the result.\n")
		case "STAGED":
			b.WriteString("Approved and staged: call cws_publish again to release it.\n")
		}
		if w := out.WarningInfo.Warnings; len(w) > 0 {
			fmt.Fprintf(&b, "\n%d warning(s):\n", len(w))
			for _, x := range w {
				fmt.Fprintf(&b, "- %s: %s\n", x.Reason, x.Description)
			}
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "cws_set_deploy_percentage",
		Title: "Set Rollout Percentage",
		Description: "Raise the rollout percentage of the published version, without a new review. Only for items with over 10,000 seven-day active users; " +
			"the new value must be higher than the current one (see cws_status).",
		Annotations: releaseLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		itemArgs
		DeployPercentage int `json:"deployPercentage" jsonschema:"New rollout percentage, 1-100, higher than the current one."`
	}) (*mcp.CallToolResult, any, error) {
		if err := checkPercent(args.DeployPercentage); err != nil {
			return errResult(err)
		}
		c, it, res := withItem(ctx, args.itemArgs)
		if res != nil {
			return res, nil, nil
		}
		body := map[string]int{"deployPercentage": args.DeployPercentage}
		if err := c.call(ctx, http.MethodPost, it.path()+":setPublishedDeployPercentage", nil, body, nil); err != nil {
			return errResult(err)
		}
		rememberItem(it)
		return textResult(fmt.Sprintf("Rollout of item %s set to %d%%.", it.ItemID, args.DeployPercentage))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "cws_cancel_submission",
		Title:       "Cancel Submission",
		Description: "Cancel the item's active submission (e.g. pending review). The uploaded draft stays; the published version is unchanged. Confirm with the user first.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, args itemArgs) (*mcp.CallToolResult, any, error) {
		c, it, res := withItem(ctx, args)
		if res != nil {
			return res, nil, nil
		}
		if err := c.call(ctx, http.MethodPost, it.path()+":cancelSubmission", nil, map[string]any{}, nil); err != nil {
			return errResult(err)
		}
		rememberItem(it)
		return textResult(fmt.Sprintf("Cancelled the active submission of item %s.", it.ItemID))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "cws_rollback",
		Title: "Roll Back Item",
		Description: "Republish a previously published package to all users under a new, higher version number (\"roll forward\"): " +
			"e.g. published 1.1 is broken → targetVersion 1.0, newVersion 1.1.1. Run with validateOnly:true first, and confirm with the user.",
		Annotations: releaseLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		itemArgs
		TargetVersion string `json:"targetVersion,omitempty" jsonschema:"Previously published version to restore. Omit for the last published one before the current."`
		NewVersion    string `json:"newVersion" jsonschema:"Version to give the restored package; must be higher than the current published version."`
		Reason        string `json:"reason" jsonschema:"Why: shown in the developer dashboard and kept for auditing."`
		ValidateOnly  bool   `json:"validateOnly,omitempty" jsonschema:"Dry run: validate without rolling back."`
	}) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.NewVersion) == "" || strings.TrimSpace(args.Reason) == "" {
			return errResult(fmt.Errorf("newVersion and reason are required"))
		}
		if !versionRE.MatchString(args.NewVersion) || (args.TargetVersion != "" && !versionRE.MatchString(args.TargetVersion)) {
			return errResult(fmt.Errorf("versions are 1-4 dot-separated integers (e.g. 1.2.3), got newVersion %q, targetVersion %q", args.NewVersion, args.TargetVersion))
		}
		c, it, res := withItem(ctx, args.itemArgs)
		if res != nil {
			return res, nil, nil
		}
		body := map[string]any{"newVersion": args.NewVersion, "rollbackReason": args.Reason}
		if args.TargetVersion != "" {
			body["targetCrxVersion"] = args.TargetVersion
		}
		if args.ValidateOnly {
			body["validateOnly"] = true
		}
		if err := c.call(ctx, http.MethodPost, it.path()+":rollback", nil, body, nil); err != nil {
			return errResult(err)
		}
		rememberItem(it)
		target := args.TargetVersion
		if target == "" {
			target = "the previous published version"
		}
		if args.ValidateOnly {
			return textResult(fmt.Sprintf("Validation passed: rollback of item %s to %s as %s is allowed. Call again without validateOnly to do it.", it.ItemID, target, args.NewVersion))
		}
		return textResult(fmt.Sprintf("Rolled back item %s to %s, published to all users as version %s. Check cws_status.", it.ItemID, target, args.NewVersion))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "cws_rating_summary",
		Title:       "Rating Summary",
		Description: "Store rating of an item: average, rating count, reviews with text, and the count per star.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args itemArgs) (*mcp.CallToolResult, any, error) {
		c, it, res := withItem(ctx, args)
		if res != nil {
			return res, nil, nil
		}
		var r ratingSummary
		if err := c.call(ctx, http.MethodGet, it.path()+"/ratingSummary", nil, nil, &r); err != nil {
			return errResult(err)
		}
		rememberItem(it)
		return textResult(fmt.Sprintf("Item %s: %.2f★ from %s ratings (%s with text)\n5★ %s · 4★ %s · 3★ %s · 2★ %s · 1★ %s",
			it.ItemID, r.Rating, orZero(r.RatingCount), orZero(r.NonEmptyReviewCount),
			orZero(r.FiveStar), orZero(r.FourStar), orZero(r.ThreeStar), orZero(r.TwoStar), orZero(r.OneStar)))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "cws_reviews",
		Title: "User Reviews",
		Description: "User reviews of an item: date, rating, author, text. Default order: newest first. " +
			"Filter by rating; sort by lowest or highest rating. Paginate with pageToken. The API cannot reply to reviews.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		itemArgs
		Rating    int    `json:"rating,omitempty" jsonschema:"Only reviews with this rating, 1-5."`
		OrderBy   string `json:"orderBy,omitempty" jsonschema:"newest (default), lowest or highest."`
		PageSize  int    `json:"pageSize,omitempty" jsonschema:"Reviews per call, 1-200 (default 50)."`
		PageToken string `json:"pageToken,omitempty" jsonschema:"nextPageToken from the previous call."`
	}) (*mcp.CallToolResult, any, error) {
		q := url.Values{}
		switch strings.ToLower(strings.TrimSpace(args.OrderBy)) {
		case "", "newest":
			q.Set("orderBy", "create_time desc")
		case "lowest":
			q.Set("orderBy", "rating asc, create_time desc")
		case "highest":
			q.Set("orderBy", "rating desc, create_time desc")
		default:
			return errResult(fmt.Errorf("orderBy %q must be newest, lowest or highest", args.OrderBy))
		}
		if args.Rating != 0 {
			if args.Rating < 1 || args.Rating > 5 {
				return errResult(fmt.Errorf("rating must be 1-5, got %d", args.Rating))
			}
			q.Set("filter", "rating = "+strconv.Itoa(args.Rating))
		}
		if err := setPageSize(q, args.PageSize, 200); err != nil {
			return errResult(err)
		}
		if args.PageToken != "" {
			q.Set("pageToken", args.PageToken)
		}
		c, it, res := withItem(ctx, args.itemArgs)
		if res != nil {
			return res, nil, nil
		}
		var out reviewsPage
		if err := c.call(ctx, http.MethodGet, it.path()+":fetchReviews", q, nil, &out); err != nil {
			return errResult(err)
		}
		rememberItem(it)
		return textResult(renderReviews(out))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "cws_metrics",
		Title: "Item Metrics",
		Description: "Daily metrics of an item: " + strings.Join(metricNames, ", ") + ". " +
			"Optionally split by one dimension: " + strings.Join(dimensionNames, ", ") + ". " +
			"Range: dateRange preset (" + daterange.Presets + "; default LAST_30_DAYS) or startDate + endDate. Paginate with pageToken.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		itemArgs
		Metrics   []string `json:"metrics,omitempty" jsonschema:"Metrics to return (default installs, uninstalls, users)."`
		Dimension string   `json:"dimension,omitempty" jsonschema:"At most one dimension to split by, e.g. country, os, app-version."`
		DateRange string   `json:"dateRange,omitempty" jsonschema:"Preset such as LAST_7_DAYS, LAST_30_DAYS (default), LAST_MONTH."`
		StartDate string   `json:"startDate,omitempty" jsonschema:"YYYY-MM-DD; use with endDate instead of dateRange."`
		EndDate   string   `json:"endDate,omitempty" jsonschema:"YYYY-MM-DD, inclusive."`
		PageSize  int      `json:"pageSize,omitempty" jsonschema:"Rows per call, 1-1000 (default 100)."`
		PageToken string   `json:"pageToken,omitempty" jsonschema:"nextPageToken from the previous call."`
	}) (*mcp.CallToolResult, any, error) {
		q, err := metricsQuery(args.Metrics, args.Dimension, args.DateRange, args.StartDate, args.EndDate, time.Now())
		if err != nil {
			return errResult(err)
		}
		if err := setPageSize(q, args.PageSize, 1000); err != nil {
			return errResult(err)
		}
		if args.PageToken != "" {
			q.Set("pageToken", args.PageToken)
		}
		c, it, res := withItem(ctx, args.itemArgs)
		if res != nil {
			return res, nil, nil
		}
		var out metricsPage
		if err := c.call(ctx, http.MethodGet, it.path()+":queryMetrics", q, nil, &out); err != nil {
			return errResult(err)
		}
		rememberItem(it)
		return textResult(renderMetrics(out, q["metrics"], args.Dimension != ""))
	})
}

// ---- argument helpers ----

var versionRE = regexp.MustCompile(`^\d{1,9}(\.\d{1,9}){0,3}$`)

func checkPercent(p int) error {
	if p < 0 || p > 100 {
		return fmt.Errorf("deployPercentage must be 0-100, got %d", p)
	}
	return nil
}

func setPageSize(q url.Values, n, max int) error {
	if n == 0 {
		return nil
	}
	if n < 1 || n > max {
		return fmt.Errorf("pageSize must be 1-%d, got %d", max, n)
	}
	q.Set("pageSize", strconv.Itoa(n))
	return nil
}

// metricsQuery builds the queryMetrics parameters from tool arguments.
func metricsQuery(metrics []string, dimension, preset, startDate, endDate string, now time.Time) (url.Values, error) {
	q := url.Values{}
	if len(metrics) == 0 {
		metrics = []string{"installs", "uninstalls", "users"}
	}
	for _, m := range metrics {
		v, err := oneOf("metric", m, metricNames)
		if err != nil {
			return nil, err
		}
		q.Add("metrics", v)
	}
	if dimension != "" {
		v, err := oneOf("dimension", dimension, dimensionNames)
		if err != nil {
			return nil, err
		}
		q.Set("dimensions", v)
	}
	start, end, err := daterange.Resolve(preset, startDate, endDate, now)
	if err != nil {
		return nil, err
	}
	for prefix, d := range map[string]time.Time{"timeSpec.firstDate": start, "timeSpec.lastDate": end} {
		q.Set(prefix+".year", strconv.Itoa(d.Year()))
		q.Set(prefix+".month", strconv.Itoa(int(d.Month())))
		q.Set(prefix+".day", strconv.Itoa(d.Day()))
	}
	return q, nil
}

// oneOf normalizes v ("Page_Views" → "page-views") and checks it is allowed.
func oneOf(name, v string, allowed []string) (string, error) {
	n := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(v)), "_", "-")
	for _, a := range allowed {
		if a == n {
			return a, nil
		}
	}
	return "", fmt.Errorf("%s %q must be one of %s", name, v, strings.Join(allowed, ", "))
}

// ---- rendering ----

func renderStatus(it item, st itemStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Item %s (publisher %s)\n", it.ItemID, it.PublisherID)
	rev := func(label string, r *revisionStatus) {
		if r == nil {
			fmt.Fprintf(&b, "%s: none\n", label)
			return
		}
		fmt.Fprintf(&b, "%s: %s", label, r.State)
		for _, ch := range r.DistributionChannels {
			fmt.Fprintf(&b, " · version %s", render.OrDash(ch.CrxVersion))
			if ch.DeployPercentage != nil {
				fmt.Fprintf(&b, " at %d%%", *ch.DeployPercentage)
			}
		}
		b.WriteString("\n")
	}
	rev("Published", st.PublishedItemRevisionStatus)
	rev("Submitted", st.SubmittedItemRevisionStatus)
	if st.LastAsyncUploadState != "" {
		fmt.Fprintf(&b, "Last upload (24 h): %s\n", st.LastAsyncUploadState)
	}
	if st.Warned {
		b.WriteString("WARNED for a policy violation: fix it or the item will be taken down (see the Developer Dashboard).\n")
	}
	if st.TakenDown {
		b.WriteString("TAKEN DOWN for a policy violation (see the Developer Dashboard).\n")
	}
	return b.String()
}

func renderReviews(out reviewsPage) string {
	if len(out.Reviews) == 0 {
		return "No reviews match."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d review(s):\n\n| date | rating | author | review |\n|---|---|---|---|\n", len(out.Reviews))
	for _, r := range out.Reviews {
		date := r.CreateTime
		if len(date) >= 10 {
			date = date[:10]
		}
		fmt.Fprintf(&b, "| %s | %d★ | %s | %s |\n", date, r.Rating, render.MDCell(r.AuthorInfo.DisplayName), render.MDCell(r.Comment))
	}
	if out.NextPageToken != "" {
		fmt.Fprintf(&b, "\nMore: pageToken %q\n", out.NextPageToken)
	}
	return b.String()
}

func renderMetrics(out metricsPage, metrics []string, withDim bool) string {
	if len(out.MetricRows) == 0 {
		return "No metric rows for this range (new items and small counts may have no data)."
	}
	var b strings.Builder
	b.WriteString("| date |")
	if withDim {
		b.WriteString(" dimension |")
	}
	for _, m := range metrics {
		b.WriteString(" " + m + " |")
	}
	b.WriteString("\n|---|")
	if withDim {
		b.WriteString("---|")
	}
	b.WriteString(strings.Repeat("---:|", len(metrics)) + "\n")
	for _, r := range out.MetricRows {
		vals := map[string]string{}
		for _, v := range r.MetricValues {
			vals[v.Metric] = v.Value
		}
		fmt.Fprintf(&b, "| %s |", r.Date)
		if withDim {
			fmt.Fprintf(&b, " %s |", render.MDCell(strings.Join(r.DimensionValues, " / ")))
		}
		for _, m := range metrics {
			fmt.Fprintf(&b, " %s |", render.OrDash(vals[m]))
		}
		b.WriteString("\n")
	}
	if out.NextPageToken != "" {
		fmt.Fprintf(&b, "\nMore: pageToken %q\n", out.NextPageToken)
	}
	return b.String()
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
