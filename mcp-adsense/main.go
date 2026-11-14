// mcp-adsense is an MCP server exposing the Google AdSense Management API v2
// (read-only) as tools: account/site/ad-unit listings and arbitrary reports.
//
// Usage:
//
//	mcp-adsense           # run the MCP server on stdio (default)
//	mcp-adsense login     # interactive OAuth consent in the default browser
//	mcp-adsense logout    # delete the stored token
package main

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/googleauth"
	"github.com/homielab/homielab-mcp/internal/mcpkit"
)

var version = "0.1.0"

var auth = &googleauth.Provider{
	Name:       "mcp-adsense",
	Title:      "AdSense",
	Scope:      "https://www.googleapis.com/auth/adsense.readonly",
	EnvPrefix:  "ADSENSE_MCP",
	ToolPrefix: "adsense",
	APIName:    "AdSense Management API",
	Account:    "the Google account that owns the AdSense account",
}

const serverInstructions = `Google AdSense reporting server (read-only).

- If a tool says "not authenticated", call adsense_login once; the user approves
  in the browser, then check adsense_login_status.
- For a broad performance picture call adsense_preset_report once with several
  presets, e.g. ["sites","pages","ad_units","countries","platforms","daily"].
- For very large exports use adsense_generate_report_csv with saveToPath.
- Custom reports saved in the AdSense UI run via adsense_list_saved_reports +
  adsense_generate_saved_report.
- accountId may be omitted everywhere: the first accessible account is used.`

func main() {
	mcpkit.Main(mcpkit.Server{
		Auth:         auth,
		Version:      version,
		Summary:      "MCP server for the Google AdSense Management API (read-only)",
		Instructions: serverInstructions,
		Register: func(s *mcp.Server) {
			registerTools(s)
			registerPrompts(s)
		},
	})
}
