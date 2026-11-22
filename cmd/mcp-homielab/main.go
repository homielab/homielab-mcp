// mcp-homielab is one binary with MCP servers for app, extension and web
// publishers: AdSense, AdMob, Search Console, Google Play, App Store Connect,
// Blogger, Chrome Web Store, Firefox Add-ons and Gemini media generation.
//
//	mcp-homielab                       list services and commands
//	mcp-homielab <service>             run that service's MCP server on stdio
//	mcp-homielab <service> login       Google sign-in (setup for API-key services)
//	mcp-homielab add <service>... | all
package main

import (
	"github.com/homielab/homielab-mcp/internal/mcpkit"
	"github.com/homielab/homielab-mcp/services/admob"
	"github.com/homielab/homielab-mcp/services/adsense"
	"github.com/homielab/homielab-mcp/services/appstoreconnect"
	"github.com/homielab/homielab-mcp/services/blogger"
	"github.com/homielab/homielab-mcp/services/chromewebstore"
	"github.com/homielab/homielab-mcp/services/firefoxaddons"
	"github.com/homielab/homielab-mcp/services/gemini"
	"github.com/homielab/homielab-mcp/services/googleplay"
	"github.com/homielab/homielab-mcp/services/searchconsole"
)

// version is set at release build time (-X main.version=...).
var version = "dev"

var services = []mcpkit.Service{
	{Title: "Google AdSense (read-only)", New: adsense.Service},
	{Title: "Google AdMob (read-only)", New: admob.Service},
	{Title: "Google Search Console", New: searchconsole.Service},
	{Title: "Google Play Console (read-write)", New: googleplay.Service},
	{Title: "App Store Connect (read-write)", New: appstoreconnect.Service},
	{Title: "Blogger (read-write)", New: blogger.Service},
	{Title: "Chrome Web Store (read-write)", New: chromewebstore.Service},
	{Title: "Firefox Add-ons / AMO (read-write)", New: firefoxaddons.Service},
	{Title: "Gemini media generation (paid)", New: gemini.Service},
}

func main() { mcpkit.Main(version, services) }
