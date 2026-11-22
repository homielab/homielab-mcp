// Package chromewebstore is the mcp-homielab service exposing the Chrome Web Store API v2 as
// tools: item status, package upload, publish (with review skip, staged
// publish and percentage rollout), cancel submission, rollback, user reviews,
// rating summary and install/user metrics.
package chromewebstore

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/googleauth"
	"github.com/homielab/homielab-mcp/internal/mcpkit"
)

var auth = &googleauth.Provider{
	Name:       "chromewebstore",
	Title:      "Chrome Web Store",
	Scope:      "https://www.googleapis.com/auth/chromewebstore",
	EnvPrefix:  "CWS_MCP",
	ToolPrefix: "cws",
	APIName:    "Chrome Web Store API",
	Account:    "the Google account that is a member of the Chrome Web Store publisher",
}

const serverInstructions = `Chrome Web Store server — status, upload, publish, rollback, reviews and metrics
of your extensions. Tools can WRITE: a publish submits the item for review and
releases it to users.

- If a tool says "not authenticated", call cws_login once; the user approves
  in the browser, then check cws_login_status.
- The API has no "list my items" call. Every tool needs publisherId (Developer
  Dashboard → Publisher → Settings) and itemId (the 32-letter extension id, or
  the store URL). After a successful call both are remembered and may be
  omitted. CWS_MCP_PUBLISHER_ID sets a default publisher.
- Release flow: cws_upload (zip file or extension folder; the manifest version
  must be higher than the published one) → cws_publish → cws_status until
  PUBLISHED. An upload IN_PROGRESS finishes in the background: poll cws_status.
- cws_publish options: skipReview (only if the item qualifies), staged:true
  (approve now, release later with another cws_publish), deployPercentage
  (initial rollout). cws_set_deploy_percentage raises the rollout of the
  published version (items with over 10,000 seven-day users).
- Confirm with the user before cws_publish, cws_rollback (validateOnly:true
  first) and cws_cancel_submission.
- cws_metrics: installs, uninstalls, users, impressions, page views by day,
  optionally split by one dimension (country, os, app-version, …).`

// Service describes the chromewebstore MCP server.
func Service() mcpkit.Server {
	return mcpkit.Server{
		Auth:         auth,
		Summary:      "MCP server for the Chrome Web Store API v2 (read-write)",
		Instructions: serverInstructions,
		Register: func(s *mcp.Server) {
			registerTools(s)
			registerPrompts(s)
		},
	}
}
