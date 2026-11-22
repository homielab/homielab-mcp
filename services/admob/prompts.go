package admob

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPrompts adds MCP prompts — reusable analysis templates the client
// can surface to the user and that expand into a full instruction for the agent.
func registerPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "admob_analysis",
		Description: "Full analysis of AdMob revenue: trend, by app / ad unit / format / country, mediation performance by ad source, and comparison with the previous period.",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "date_range",
				Title:       "Date range",
				Description: "LAST_7_DAYS, LAST_30_DAYS (default), MONTH_TO_DATE, LAST_MONTH, YEAR_TO_DATE.",
			},
			{
				Name:        "account_id",
				Title:       "Account",
				Description: "Publisher id (pub-XXXX). Omit to use the first account.",
			},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		dr := "LAST_30_DAYS"
		if v := req.Params.Arguments["date_range"]; v != "" {
			dr = v
		}
		acc := ""
		if v := req.Params.Arguments["account_id"]; v != "" {
			acc = fmt.Sprintf(`, "accountId": %q`, v)
		}
		text := fmt.Sprintf(`Run a FULL analysis of my Google AdMob revenue (read it through the admob_* tools).

Step 1 — One combined call:
admob_preset_report {"presets": ["daily", "apps", "ad_units", "formats", "countries", "platforms"], "dateRange": %q%s}

Step 2 — Mediation and period comparison:
- admob_preset_report {"presets": ["ad_sources", "mediation_groups"], "dateRange": %q%s}
- admob_period_comparison {"dimensions": ["APP"], "dateRange": %q%s}
- admob_period_comparison {"dimensions": ["COUNTRY"], "dateRange": %q%s}

Step 3 — If one app earns most of the revenue, go deeper: admob_preset_report {"presets": ["app_versions", "sdk_versions"], "filters": {"APP": ["<app id>"]}}

Step 4 — Write the report in the language I am using in this conversation:
1. **Overview**: total earnings, impressions, eCPM, match rate, show rate; daily trend and change versus the previous period.
2. **By app and ad unit**: the top earners, units with many impressions but low eCPM, units with a low match rate or show rate (lost revenue).
3. **By format and country**: which format (banner, interstitial, rewarded, native, app open) contributes most; main markets, eCPM by country.
4. **Mediation**: which ad sources win most, observed eCPM, fill rate; weak networks to review.
5. **Changes**: apps and countries that rose or fell most versus the previous period, with likely causes (app version, SDK, seasonality).
6. **Recommended actions**: 3-5 concrete actions (add a format, tune mediation or bidding, update the SDK, fix units with a low match rate...).
7. **Appendix**: keep the main data tables.

If a tool returns "not authenticated", call admob_login, wait for me to approve, then call admob_login_status.`, dr, acc, dr, acc, dr, acc, dr, acc)

		return &mcp.GetPromptResult{
			Description: "Full AdMob analysis",
			Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: text}},
			},
		}, nil
	})
}
