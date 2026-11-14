// mcp-appstoreconnect is an MCP server exposing the App Store Connect API as
// tools: apps, TestFlight builds / groups / testers, App Store versions,
// localized metadata, review submission, phased release, customer reviews,
// in-app purchases, sales & finance reports, plus raw API access.
//
// Usage:
//
//	mcp-appstoreconnect                                   # run the MCP server on stdio (default)
//	mcp-appstoreconnect setup --key-file AuthKey_X.p8 --issuer-id <id>
package main

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/mcpkit"
)

var version = "0.1.0"

const serverName = "mcp-appstoreconnect"

const serverInstructions = `App Store Connect server — apps, TestFlight, App Store versions and metadata, review submission, releases, customer reviews, IAPs, sales/finance reports. Tools can WRITE to App Store Connect (submit for review, release, change metadata, invite testers).

- Auth is an App Store Connect API key (.p8), no browser login. If a tool says
  "not configured", tell the user to run "mcp-appstoreconnect setup" (steps are
  in the error); asc_auth_status checks the key.
- Most tools take "app": Apple ID, bundle id or app name. After a successful
  call the server remembers it; later calls may omit it.
- Version tools default to the app's editable version (PREPARE_FOR_SUBMISSION,
  rejected, ...) on platform IOS unless versionId / versionString is given.
- Release flow: asc_builds → asc_version_create (or asc_version_update with
  build) → asc_localization_update (whatsNew) → asc_review_submit. After
  approval: asc_version_release for MANUAL releases, asc_phased_release to
  pause/resume/complete a 7-day phased rollout.
- TestFlight: asc_build_distribute adds a build to groups, sets What to Test and
  (external groups) submits it for beta review.
- Reports need the vendor number (setup --vendor-number or vendorNumber arg).
  Sales data for a day is ready the next day (Pacific time).
- asc_get reads any API path; asc_request sends POST/PATCH/DELETE for anything
  without a dedicated tool (see https://developer.apple.com/documentation/appstoreconnectapi).
- Confirm with the user before asc_review_submit, asc_version_release,
  asc_beta_tester_remove with deleteTester, and DELETE via asc_request.`

func main() {
	mcpkit.Main(mcpkit.Server{
		Name:         serverName,
		Version:      version,
		Summary:      "MCP server for the App Store Connect API",
		Instructions: serverInstructions,
		Credentials:  credentialSources,
		Commands: []mcpkit.Command{
			{Name: "setup", Usage: "--key-file AuthKey_<KEYID>.p8 --issuer-id <ID> [--vendor-number <N>]: store and test an API key", Run: runSetup},
		},
		Register: func(s *mcp.Server) {
			registerTools(s)
			registerPrompts(s)
		},
	})
}
