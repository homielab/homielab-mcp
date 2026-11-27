# mcp-homielab

[![CI](https://github.com/homielab/homielab-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/homielab/homielab-mcp/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![homielab/homielab-mcp MCP server](https://glama.ai/mcp/servers/homielab/homielab-mcp/badges/score.svg)](https://glama.ai/mcp/servers/homielab/homielab-mcp)

[Model Context Protocol](https://modelcontextprotocol.io) servers for people who build and ship
apps, browser extensions and websites. Let Claude, Cursor or any MCP client read your AdSense,
AdMob and Search Console reports, publish to Google Play, App Store Connect, Chrome Web Store and
Firefox Add-ons, manage a Blogger blog, and generate images, speech, music and video with Gemini.

One static binary, no runtime. Each service runs as its own stdio MCP server
(`mcp-homielab <service>`), so you add only the ones you use.

Unofficial: not affiliated with or endorsed by Google, Apple or Mozilla. You use your own API
credentials; nothing is sent anywhere except the API each service wraps.

## Install

macOS / Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/homielab/homielab-mcp/main/install.sh | sh
```

Installs to `~/.local/bin` (set `MCP_HOMIELAB_INSTALL_DIR` to change). Windows: download the zip
from [Releases](https://github.com/homielab/homielab-mcp/releases). With Go 1.27+:
`go install github.com/homielab/homielab-mcp/cmd/mcp-homielab@latest`.

## Quick start

```bash
mcp-homielab add searchconsole     # register with Claude Code (or: add all)
mcp-homielab searchconsole login   # Google services: sign in once (setup below)
```

Other clients: `mcp-homielab add --client claude-desktop <service>`, `--client cursor`, or
`--client json` to print the config:

```json
{
  "mcpServers": {
    "searchconsole": { "command": "/path/to/mcp-homielab", "args": ["searchconsole"] }
  }
}
```

Run `mcp-homielab` to list services, `mcp-homielab <service> --help` for one service's commands
and credential lookup.

## Services

| Service | API | Auth |
|---|---|---|
| [`adsense`](services/adsense/README.md) | AdSense Management API v2 (read-only): reports by site, page, ad unit, country…, 18 presets, CSV export, policy issues | Google |
| [`admob`](services/admob/README.md) | AdMob API v1 (read-only): apps, ad units, network and mediation reports, 13 presets, period comparison, CSV export | Google |
| [`searchconsole`](services/searchconsole/README.md) | Search Console: Search Analytics, quick report, period comparison, sitemaps, URL inspection; add/remove properties and sitemaps | Google |
| [`googleplay`](services/googleplay/README.md) | Google Play Developer API (**read-write**): tracks and staged rollout, .aab/.apk upload, listings and images, IAP, reviews, purchase verification | Google |
| [`appstoreconnect`](services/appstoreconnect/README.md) | App Store Connect API (**read-write**): TestFlight, versions and metadata, review submission, release, customer reviews, IAP, sales and finance reports | API key |
| [`blogger`](services/blogger/README.md) | Blogger API v3 (**read-write**): posts and pages (create, edit, publish, schedule, delete), comment moderation, page views | Google |
| [`chromewebstore`](services/chromewebstore/README.md) | Chrome Web Store API v2 (**read-write**): status, upload, publish (staged, % rollout), cancel, rollback, reviews, metrics | Google |
| [`firefoxaddons`](services/firefoxaddons/README.md) | addons.mozilla.org API v5 (**read-write**): listing, icon, screenshots, upload and validation, version submit, rollback, ratings, search | API key |
| [`gemini`](services/gemini/README.md) | Gemini API media generation (**paid per call**): images, speech, music, video, spend log | API key |

## Setup per service

**Google services** (`adsense`, `admob`, `searchconsole`, `googleplay`, `blogger`,
`chromewebstore`) share one OAuth client:

1. In [Google Cloud Console](https://console.cloud.google.com), enable the APIs you need and create
   one OAuth Client ID of type **Desktop app**. Download its JSON.
2. Put it in the shared config dir, so it serves every Google service:
   - macOS: `~/Library/Application Support/mcp-homielab/`
   - Linux: `~/.config/mcp-homielab/`
   - Windows: `%AppData%\mcp-homielab\`
3. `mcp-homielab <service> login` (or ask the agent; it calls `<prefix>_login`).

**App Store Connect**: `mcp-homielab appstoreconnect setup --key-file AuthKey_<KEYID>.p8 --issuer-id <ID>`
([details](services/appstoreconnect/README.md#setup)).
**Firefox Add-ons**: `mcp-homielab firefoxaddons setup --issuer <JWT issuer>`
([details](services/firefoxaddons/README.md#setup)).
**Gemini**: set `GEMINI_API_KEY`, or `mcp-homielab gemini setup`
([details](services/gemini/README.md#setup)).

Step-by-step guide and troubleshooting: [INSTALL.md](INSTALL.md).

Credentials, tokens and state live in `<user config dir>/mcp-homielab/<service>/` with mode 0600.
Set `MCP_HOMIELAB_CONFIG_DIR` to use another dir.

## Safety

Several services can **write**: publish releases, edit store listings, delete items. `gemini`
spends money on every call. Read a service's README before giving an agent access, and add only
the services you need.

## Development

```bash
git clone https://github.com/homielab/homielab-mcp.git && cd homielab-mcp
./build.sh     # gofmt + vet + test, then builds mcp-homielab into $(go env GOPATH)/bin
```

| Path | Contents |
|---|---|
| `cmd/mcp-homielab/main.go` | Service list and entry point |
| `services/<id>/` | One service: `main.go` (`Service()`, instructions), API client, tools, prompts, tests |
| `internal/mcpkit/` | CLI (`<service>`, `login`/`logout`, `add`), `*_login` tools, tool result helpers |
| `internal/googleauth/` | OAuth loopback + PKCE, client secret lookup, token store, background login |
| `internal/googleapi/` | JSON client: retry on 429/5xx, `Retry-After`, Google error decoding, pagination |
| `internal/appdir/` | Config dir (`MCP_HOMIELAB_CONFIG_DIR` or `<user config dir>/mcp-homielab`) |
| `internal/daterange/`, `internal/render/` | Date presets; Markdown/CSV tables and CSV export |

Add a service:

1. Create `services/<id>/main.go` with `func Service() mcpkit.Server`. A Google API declares a
   `googleauth.Provider` (Name = id, scope, env prefix, tool prefix) and adds the id to
   `googleauth.Collection`; an API-key service sets `ID`, `Credentials` and a `setup` command
   (see `services/gemini`).
2. API client: `googleapi.New(hc, "<API name>")` with `hc` from `auth.HTTPClient(ctx)`.
3. Tools: `mcpkit.AddLoginTools`, then the service's tools (with `Title` and `Annotations`),
   prompts and tests.
4. Add it to the list in `cmd/mcp-homielab/main.go`.

Release: push a tag `vX.Y.Z`; GitHub Actions runs GoReleaser and attaches binaries for macOS,
Linux and Windows (amd64, arm64). Publishing the release lists every service in the
[MCP Registry](https://registry.modelcontextprotocol.io) as `io.github.homielab/<service>`.

## Contributing

Issues and pull requests are welcome. Run `./build.sh` before a PR. Tests use local fake servers
and never call a real API; keep it that way, and never commit real account ids, tokens or keys.

Report a vulnerability privately through
[GitHub security advisories](https://github.com/homielab/homielab-mcp/security/advisories/new),
not a public issue.

## License

[MIT](LICENSE)
