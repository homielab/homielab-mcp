package searchconsole

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPrompts adds MCP prompts — reusable analysis templates the client
// can surface to the user and that expand into a full instruction for the agent.
func registerPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "gsc_seo_analysis",
		Description: "Full SEO analysis from Google Search Console: clicks and impressions trend, top queries and pages, device and country split, sitemap health, and the index status of key pages.",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "date_range",
				Title:       "Date range",
				Description: "LAST_7_DAYS, LAST_30_DAYS (default), MONTH_TO_DATE, YEAR_TO_DATE, LAST_MONTH, or startDate/endDate as YYYY-MM-DD.",
				Required:    false,
			},
			{
				Name:        "site_url",
				Title:       "Property",
				Description: "Search Console property (https://www.example.com/ or sc-domain:example.com). Omit if there is only one property.",
				Required:    false,
			},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		dr := "LAST_30_DAYS"
		if v := req.Params.Arguments["date_range"]; v != "" {
			dr = v
		}
		site := ""
		if v := req.Params.Arguments["site_url"]; v != "" {
			site = fmt.Sprintf(`, "siteUrl": %q`, v)
		}
		text := fmt.Sprintf(`Run a FULL SEO analysis of my Google Search Console property (read it through the gsc_* tools).

Step 1 — Call gsc_list_sites to get the exact siteUrl.

Step 2 — Pull Search Analytics for the range %q%s:
1. gsc_search_analytics {"dimensions": ["date"]} — daily trend (clicks, impressions, CTR, position)
2. gsc_search_analytics {"dimensions": ["query"], "rowLimit": 100} — top queries
3. gsc_search_analytics {"dimensions": ["page"], "rowLimit": 100} — top pages
4. gsc_search_analytics {"dimensions": ["device"]} and {"dimensions": ["country"]} — split
5. gsc_search_analytics {"dimensions": ["query", "page"], "rowLimit": 100} — query → page mapping (to spot cannibalization)

Step 3 — Technical health:
- gsc_list_sitemaps — sitemaps with errors or unindexed URLs
- For the 3-5 most important pages (and 2-3 pages with high impressions but few clicks), call gsc_inspect_urls (up to 10 URLs per call) to see their index status

Step 4 — Write the SEO report in the language I am using in this conversation:
1. **Overview**: total clicks and impressions, CTR, average position; compare the first and second half of the period to show the trend.
2. **Quick wins**: queries with high impressions but low CTR (weak snippet) and pages with high impressions but position > 10 (close to page one).
3. **Core queries and pages**: what drives the traffic; queries that land on several pages (cannibalization: merge the content).
4. **Devices and markets**: mobile vs desktop, main countries; notable CTR or position gaps.
5. **Index health**: URL inspection results, sitemap errors, pages that are "Crawled - currently not indexed".
6. **Recommended actions**: 5-7 concrete actions by priority (titles and meta descriptions for quick wins, internal links, merging content, submitting a sitemap...).
7. **Appendix**: keep the main data tables.

If a tool returns "not authenticated", call gsc_login, wait for me to approve, then call gsc_login_status.`, dr, site)

		return &mcp.GetPromptResult{
			Description: "Full Search Console SEO analysis",
			Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: text}},
			},
		}, nil
	})
}
