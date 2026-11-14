// mcp-searchconsole is an MCP server exposing the Google Search Console API
// as tools: properties, Search Analytics performance reports, sitemaps, and
// URL inspection, plus adding/removing properties and sitemaps.
//
// Usage:
//
//	mcp-searchconsole           # run the MCP server on stdio (default)
//	mcp-searchconsole login     # interactive OAuth consent in the default browser
//	mcp-searchconsole logout    # delete the stored token
package main

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/googleauth"
	"github.com/homielab/homielab-mcp/internal/mcpkit"
)

var version = "0.1.0"

var auth = &googleauth.Provider{
	Name:       "mcp-searchconsole",
	Title:      "Search Console",
	Scope:      "https://www.googleapis.com/auth/webmasters",
	EnvPrefix:  "GSC_MCP",
	ToolPrefix: "gsc",
	APIName:    "Search Console API",
	Account:    "the Google account that owns the Search Console properties",
}

const serverInstructions = `Google Search Console server: reports, URL inspection, and a few writes
(gsc_add_site, gsc_delete_site, gsc_submit_sitemap, gsc_delete_sitemap).
gsc_delete_site and gsc_delete_sitemap are destructive — confirm with the user first.

- If a tool says "not authenticated", call gsc_login once; the user approves
  in the browser, then check gsc_login_status.
- siteUrl may usually be omitted (single property) or given loosely
  ("example.com" matches https://example.com/ or sc-domain:example.com);
  gsc_list_sites returns the exact values.
- Data lags ~2 days; recent dates can be partial (dataState "all").
- For broad SEO pictures: gsc_search_analytics with dimensions ["date"],
  then ["query"], ["page"], ["device"], ["country"].
- URL inspection has a daily quota (~2,000/property): gsc_inspect_url for one
  URL in full detail, gsc_inspect_urls for up to 10 URLs with an issue summary.
- Page-two opportunities: gsc_search_analytics with minPosition/maxPosition,
  minImpressions and sortBy (the API itself only orders by clicks).`

func main() {
	mcpkit.Main(mcpkit.Server{
		Auth:         auth,
		Version:      version,
		Summary:      "MCP server for the Google Search Console API",
		Instructions: serverInstructions,
		Register: func(s *mcp.Server) {
			registerTools(s)
			registerPrompts(s)
		},
	})
}
