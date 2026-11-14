// mcp-androidpublisher is an MCP server exposing the Google Play Developer
// API (Android Publisher) as tools: release tracks & staged rollouts, app
// bundle / APK uploads, store listings & images, in-app products, reviews and
// purchase verification.
//
// Usage:
//
//	mcp-androidpublisher           # run the MCP server on stdio (default)
//	mcp-androidpublisher login     # interactive OAuth consent in the default browser
//	mcp-androidpublisher logout    # delete the stored token
package main

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/googleauth"
	"github.com/homielab/homielab-mcp/internal/mcpkit"
)

var version = "0.1.0"

var auth = &googleauth.Provider{
	Name:       "mcp-androidpublisher",
	Title:      "Play Developer",
	Scope:      "https://www.googleapis.com/auth/androidpublisher",
	EnvPrefix:  "PLAY_MCP",
	ToolPrefix: "play",
	APIName:    "Google Play Android Developer API",
	Account:    "a Google account added to the Play Console developer account (Users & permissions)",
}

const serverInstructions = `Google Play Developer (Android Publisher) server — manages Play releases, metadata, IAPs, reviews and purchase verification. Tools can WRITE to your Play Console (publish releases, change store text, delete products).

- If a tool says "not authenticated", call play_login once; the user approves
  in the browser, then check play_login_status. The Google account must be a
  member of the Play Console developer account (Users & permissions).
- Every app tool needs packageName (e.g. com.example.app) — get it from the
  Play Console URL (…/app/<package>) or the Play Store listing URL. After a
  successful call the server remembers it; later calls may omit packageName.
- Reads and writes run inside an API "edit" that the server opens and closes
  for you; only one edit can be open per developer account, so run publishing
  operations one at a time.
- play_track_update controls what users receive: use validateOnly:true to
  dry-run, userFraction for staged rollouts (0.0–1.0), status "halted" to stop
  a rollout. versionCodes must include every build to KEEP in the track.
- Upload tools (play_bundle_upload / play_apk_upload) need a local file path
  and commit immediately, but the artifact only serves users once assigned to
  a track with play_track_update.
- Destructive: play_iap_delete and play_images_delete (without imageId)
  remove data permanently — confirm with the user before calling them.`

func main() {
	mcpkit.Main(mcpkit.Server{
		Auth:         auth,
		Version:      version,
		Summary:      "MCP server for the Google Play Developer API (Android Publisher)",
		Instructions: serverInstructions,
		Register: func(s *mcp.Server) {
			registerTools(s)
			registerPrompts(s)
		},
	})
}
