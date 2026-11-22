// Package firefoxaddons is the mcp-homielab service for addons.mozilla.org (AMO) API v5, the
// developer side of Firefox add-ons: list and edit your add-ons, upload
// packages, submit versions (with source code), edit, disable, delete and
// roll back versions, icons, screenshots, EULA / privacy policy, plus public
// search and ratings.
package firefoxaddons

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/mcpkit"
)

const serviceID = "firefoxaddons"

const serverInstructions = `Firefox Add-ons (addons.mozilla.org, AMO) server — your add-ons, uploads, versions, listing metadata, icon, screenshots, EULA/privacy policy, ratings and public search. Tools can WRITE: a version submission goes to Mozilla review and, once signed, to users.

- Auth is an AMO API key (JWT issuer + secret), no browser login. If a tool
  says "not configured", tell the user to run "mcp-homielab firefoxaddons setup" (steps are in
  the error); amo_auth_status checks the key.
- "addon" is the numeric id, slug, guid ({…} or name@domain) or AMO URL.
  After a successful call it is remembered and may be omitted. amo_my_addons
  lists the add-ons you are an author of.
- "version" is a version id or a version number (1.2.0). A number without a
  dot is read as an id: prefix it with v (v2) to mean version "2".
- Release flow: amo_version_create with path (an .xpi/.zip or the extension
  folder) uploads, waits for validation, then creates the version; add
  sourcePath when the code is minified/bundled. channel "listed" goes to
  review and the public listing; "unlisted" is signed for self-distribution.
  A new add-on: amo_addon_create (listed needs name, summary, categories and
  a license).
- Validation errors stop the submission: show them to the user. Warnings
  do not.
- Confirm with the user before amo_addon_delete (permanent; the guid can
  never be reused), amo_version_delete, amo_version_rollback, isDisabled
  changes, and DELETE via amo_request.
- amo_request reaches any other v5 endpoint (see
  https://mozilla.github.io/addons-server/topics/api/).`

// Service describes the firefoxaddons MCP server.
func Service() mcpkit.Server {
	return mcpkit.Server{
		ID:           serviceID,
		Summary:      "MCP server for the addons.mozilla.org (AMO) API v5 (read-write)",
		Instructions: serverInstructions,
		Credentials:  credentialSources,
		Commands: []mcpkit.Command{
			{Name: "setup", Usage: "[--issuer <user:…>] [--api-base <url>]: store an AMO API key; the secret is read from stdin", Run: runSetup},
		},
		Register: func(s *mcp.Server) {
			registerTools(s)
			registerPrompts(s)
		},
	}
}
