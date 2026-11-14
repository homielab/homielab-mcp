// mcp-admob is an MCP server exposing the Google AdMob API v1 (read-only) as
// tools: accounts, apps, ad units, and network / mediation reports.
//
// Usage:
//
//	mcp-admob           # run the MCP server on stdio (default)
//	mcp-admob login     # interactive OAuth consent in the default browser
//	mcp-admob logout    # delete the stored token
package main

import (
	_ "time/tzdata" // account reporting time zones on hosts without zoneinfo

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/googleauth"
	"github.com/homielab/homielab-mcp/internal/mcpkit"
)

var version = "0.1.0"

var auth = &googleauth.Provider{
	Name:       "mcp-admob",
	Title:      "AdMob",
	Scope:      "https://www.googleapis.com/auth/admob.readonly",
	EnvPrefix:  "ADMOB_MCP",
	ToolPrefix: "admob",
	APIName:    "AdMob API",
	Account:    "the Google account that has access to the AdMob account",
}

const serverInstructions = `Google AdMob reporting server (read-only).

- If a tool says "not authenticated", call admob_login once; the user approves
  in the browser, then check admob_login_status.
- accountId may be omitted everywhere: the first accessible account is used.
- For a broad picture call admob_preset_report once with several presets,
  e.g. ["daily","apps","ad_units","formats","countries"]; add
  ["ad_sources","mediation_groups"] for mediation.
- admob_network_report = AdMob Network earnings; admob_mediation_report = per
  ad source (incl. third-party networks), observed eCPM and latency.
- admob_period_comparison shows what changed versus the previous period or a
  custom baseline (e.g. last year).
- Dates follow the account's reporting time zone; today's data is partial.
- Large exports: pass saveToPath to write CSV.`

func main() {
	mcpkit.Main(mcpkit.Server{
		Auth:         auth,
		Version:      version,
		Summary:      "MCP server for the Google AdMob API (read-only)",
		Instructions: serverInstructions,
		Register: func(s *mcp.Server) {
			registerTools(s)
			registerPrompts(s)
		},
	})
}
