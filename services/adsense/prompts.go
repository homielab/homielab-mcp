package adsense

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPrompts adds MCP prompts — reusable analysis templates the client
// can surface to the user (e.g. via a slash menu) and that expand into a full
// instruction for the agent.
func registerPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "adsense_deep_analysis",
		Description: "Full analysis of an AdSense account: every dimension (site, page, ad unit, country, platform, format, trend), policy issues and alerts, and recommended actions.",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "date_range",
				Title:       "Date range",
				Description: "TODAY, YESTERDAY, MONTH_TO_DATE, YEAR_TO_DATE, LAST_7_DAYS, LAST_30_DAYS (default) or CUSTOM with startDate/endDate as YYYY-MM-DD.",
				Required:    false,
			},
			{
				Name:        "account",
				Title:       "Account",
				Description: "AdSense account id (pub-XXXX). Omit to use the first account.",
				Required:    false,
			},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		dr := "LAST_30_DAYS"
		if v := req.Params.Arguments["date_range"]; v != "" {
			dr = v
		}
		acc := ""
		if v := req.Params.Arguments["account"]; v != "" {
			acc = fmt.Sprintf(`, "accountId": %q`, v)
		}
		text := fmt.Sprintf(`Run a FULL analysis of my Google AdSense account (read it through the adsense_* tools).

Step 1 — Run the combined report in ONE call:
adsense_preset_report {"presets": ["sites", "pages", "ad_units", "countries", "site_countries", "platforms", "ad_formats", "bid_types", "traffic_sources", "daily"]%s, "dateRange": %q}

Step 2 — Check account health:
- adsense_list_alerts (payment and policy alerts)
- adsense_list_policy_issues (policy violations by site)
- adsense_list_sites (sites that are not READY or need review)

Step 3 — If a site is active, go one level deeper:
- adsense_preset_report {"presets": ["pages"], "limit": 50} for the top site (filterDomain if needed)

Step 4 — Write the analysis in the language I am using in this conversation:
1. **Overview**: total earnings, page views, impressions, clicks, CTR, RPM; compare the first and second half of the period to show the trend.
2. **By site and page**: which sites and pages earn the most; pages with many views but low RPM (room to optimise).
3. **By ad unit and format**: the best units; which format (display, in-article, anchor, vignette) contributes most.
4. **By country and platform**: main markets, RPM by country, desktop vs mobile; RPM gaps between platforms.
5. **Viewability**: average Active View viewability; days with unusual viewability.
6. **Risks and actions**: from alerts, policy issues and site status; list 3-5 concrete actions (e.g. fix low-RPM units, try a new format, resolve a policy issue).
7. **Data tables**: keep each preset's markdown table in an appendix.

If a tool returns "not authenticated", call adsense_login, wait for me to approve, then call adsense_login_status.`, acc, dr)

		return &mcp.GetPromptResult{
			Description: "Full AdSense analysis",
			Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: text}},
			},
		}, nil
	})
}
