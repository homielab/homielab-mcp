# HomieLab MCP collection

[![CI](https://github.com/homielab/homielab-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/homielab/homielab-mcp/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[Model Context Protocol](https://modelcontextprotocol.io) servers for the services an app or web
publisher works with every day: AdSense, AdMob, Search Console, Google Play, App Store Connect,
Blogger, Chrome Web Store, Firefox Add-ons and Gemini media generation. Each server is one static
Go binary that talks stdio, so it works with Claude Code, Claude Desktop, Cursor and any other MCP
client.

All servers share one pattern: Google OAuth loopback + PKCE (login runs in the background; App
Store Connect, AMO and Gemini use an API key instead), 0600 credentials in
`<user config dir>/<server>/`, retry on 429/5xx, client-side validation, MCP spec 2026-07-28
(stateless `server/discover` + legacy lifecycle, tool `title`, `ttlMs`/`cacheScope`). Built on the
official SDK, `github.com/modelcontextprotocol/go-sdk`.

Unofficial: not affiliated with or endorsed by Google, Apple or Mozilla. You use your own API
credentials; nothing is sent anywhere except the API each server wraps.

## Quick start

```bash
go install github.com/homielab/homielab-mcp/mcp-searchconsole@latest   # any server from the table
claude mcp add mcp-searchconsole "$(go env GOPATH)/bin/mcp-searchconsole"
```

Then do the [one-time setup](#one-time-setup-per-api) for that API. Step by step:
[INSTALL.md](INSTALL.md).

## Servers

| Server | API | Binary | Config dir |
|---|---|---|---|
| [`mcp-adsense/`](mcp-adsense/README.md) | AdSense Management API v2 (read-only) — reports by site/page/ad unit/country/..., 18 presets, CSV export, policy issues | `~/go/bin/mcp-adsense` | `~/Library/Application Support/mcp-adsense/` |
| [`mcp-searchconsole/`](mcp-searchconsole/README.md) | Google Search Console — Search Analytics, quick report, period comparison, sitemaps, URL inspection (single and batch); add/remove properties and sitemaps | `~/go/bin/mcp-searchconsole` | `~/Library/Application Support/mcp-searchconsole/` |
| [`mcp-admob/`](mcp-admob/README.md) | AdMob API v1 (read-only) — apps, ad units, network & mediation reports, 13 presets, period comparison, CSV export | `~/go/bin/mcp-admob` | `~/Library/Application Support/mcp-admob/` |
| [`mcp-androidpublisher/`](mcp-androidpublisher/README.md) | Google Play Developer (Android Publisher v3, **read-write**) — tracks & staged rollout, .aab/.apk upload (resumable), listings & images, IAP, reviews, purchase verification | `~/go/bin/mcp-androidpublisher` | `~/Library/Application Support/mcp-androidpublisher/` |
| [`mcp-appstoreconnect/`](mcp-appstoreconnect/README.md) | App Store Connect API (**read-write**, API key auth) — TestFlight builds/groups/testers, versions & metadata, review submission, manual & phased release, customer reviews, IAP/subscriptions, sales & finance reports, raw API access | `~/go/bin/mcp-appstoreconnect` | `~/Library/Application Support/mcp-appstoreconnect/` |
| [`mcp-blogger/`](mcp-blogger/README.md) | Blogger API v3 (**read-write**) — posts & pages (create, edit, publish, schedule, revert, delete), comment moderation, page views | `~/go/bin/mcp-blogger` | `~/Library/Application Support/mcp-blogger/` |
| [`mcp-chromewebstore/`](mcp-chromewebstore/README.md) | Chrome Web Store API v2 (**read-write**) — item status, package upload (zip or folder), publish (skip review, staged, % rollout), cancel, rollback, user reviews, rating summary, install/user metrics | `~/go/bin/mcp-chromewebstore` | `~/Library/Application Support/mcp-chromewebstore/` |
| [`mcp-amo/`](mcp-amo/README.md) | addons.mozilla.org API v5 (**read-write**, API key auth) — your Firefox add-ons: listing, icon, screenshots, EULA/privacy, upload & validation, version submit (with source), edit, disable, delete, rollback, ratings, public search | `~/go/bin/mcp-amo` | `~/Library/Application Support/mcp-amo/` |
| [`mcp-gemini/`](mcp-gemini/README.md) | Gemini API media generation (API key, **paid per call**) — images (Nano Banana), speech (Gemini TTS, dialogue), music (Lyria), video (Gemini Omni), spend log | `~/go/bin/mcp-gemini` | `~/Library/Application Support/mcp-gemini/` |

## Layout

One Go module (`github.com/homielab/homielab-mcp`):

| Path | Contents |
|---|---|
| `mcp-<name>/` | One server = one `package main`: `main.go` (Provider + instructions), API client, tools, prompts |
| `internal/googleauth/` | OAuth loopback + PKCE, client secret lookup, token store, background login, login status |
| `internal/googleapi/` | JSON client: retry on 429/5xx/transport errors, `Retry-After`, Google error decoding (or a custom `DecodeError`), extra headers, `ListAll` pagination |
| `internal/mcpkit/` | CLI entry point (`serve`/`--help`, `login`/`logout` with Google auth, extra `Commands`), `*_login`/`*_login_status`/`*_logout` tools, tool result helpers |
| `internal/daterange/` | Date presets (`TODAY` … `YEAR_TO_DATE`, `LAST_N_DAYS` exclude today), explicit-date validation, previous-period window |
| `internal/render/` | Markdown/CSV cell escaping, CSV export to a file with preview |

## Build from source

```bash
git clone https://github.com/homielab/homielab-mcp.git && cd homielab-mcp
./build.sh        # gofmt + vet + test + build each mcp-*/ into $(go env GOPATH)/bin, version from git
```

Other output dir: `BIN_DIR=/some/dir ./build.sh`. Cross-compile: `GOOS=linux GOARCH=arm64 ./build.sh`.
CI (GitHub Actions) runs gofmt + vet + test -race + build on every push/PR.

## Register with an MCP client

Each server is a stdio binary, registered by absolute path. Claude Code:

```bash
claude mcp add mcp-adsense "$(go env GOPATH)/bin/mcp-adsense"
```

JSON-config clients (Claude Desktop, Cursor, ...) — key `mcpServers`:

```json
{
  "mcpServers": {
    "mcp-adsense": { "command": "/path/to/go/bin/mcp-adsense" },
    "mcp-searchconsole": { "command": "/path/to/go/bin/mcp-searchconsole" },
    "mcp-admob": { "command": "/path/to/go/bin/mcp-admob" },
    "mcp-androidpublisher": { "command": "/path/to/go/bin/mcp-androidpublisher" },
    "mcp-appstoreconnect": { "command": "/path/to/go/bin/mcp-appstoreconnect" },
    "mcp-blogger": { "command": "/path/to/go/bin/mcp-blogger" },
    "mcp-chromewebstore": { "command": "/path/to/go/bin/mcp-chromewebstore" },
    "mcp-amo": { "command": "/path/to/go/bin/mcp-amo" },
    "mcp-gemini": { "command": "/path/to/go/bin/mcp-gemini" }
  }
}
```

## One-time setup (per API)

1. Google Cloud Console: enable the API (**AdSense Management API** / **Google Search Console API** /
   **Google Play Android Developer API** / **AdMob API** / **Blogger API v3** / **Chrome Web Store API**) in one project and create **one** OAuth Client ID of
   type **Desktop app**.
2. Put the secret JSON (`client_secret*.json`) in a server's config dir. One file serves all
   servers: each server searches the others' config dirs.
3. Run `<binary> login` (or call the `*_login` tool, then `*_login_status`, in a session).
   - Play only: the Google account must be in **Play Console → Users & permissions** of the
     developer account, and the scope is read-write (the server can publish releases).

App Store Connect (no Google setup): create an API key and run
`mcp-appstoreconnect setup --key-file AuthKey_<KEYID>.p8 --issuer-id <ID>` — see
[mcp-appstoreconnect/README.md](mcp-appstoreconnect/README.md#setup).

Firefox Add-ons (no Google setup): generate a key at
<https://addons.mozilla.org/developers/addon/api/key/> and run `mcp-amo setup --issuer <JWT issuer>` — see
[mcp-amo/README.md](mcp-amo/README.md#setup).

Gemini (no Google Cloud setup): set `GEMINI_API_KEY`, or run `mcp-gemini setup` to store a key —
see [mcp-gemini/README.md](mcp-gemini/README.md#setup).

## Full setup guide

See [INSTALL.md](INSTALL.md): build, Google Cloud setup, login, client registration,
troubleshooting.

## Add a server

1. Create `mcp-<name>/main.go`: declare a `googleauth.Provider` (name, scope, env prefix, tool
   prefix) and call `mcpkit.Main`. Add the name to `googleauth.Collection` to share the client secret.
   A non-Google API sets `Name`, `Credentials` and `Commands` instead of `Auth` (see
   `mcp-appstoreconnect`).
2. API client: wrap `googleapi.New(hc, "<API name>")` with `hc` from `auth.HTTPClient(ctx)`.
3. Tools: `mcpkit.AddLoginTools` for login, then the server's own tools (Title, Annotations),
   prompts, tests. Use `daterange` for date arguments and `render` for tables and CSV export.
4. `./build.sh` builds every `mcp-*/`; register the new binary with your MCP client.

## Security

- Credentials and tokens stay on your machine, in `<user config dir>/<server>/` with mode 0600.
  They are never written to the repo; `.gitignore` blocks the usual file names.
- Several servers can **write** (publish releases, edit listings, delete items) and `mcp-gemini`
  spends money per call. Read each server's README before giving an agent access.
- Report a vulnerability privately through
  [GitHub security advisories](https://github.com/homielab/homielab-mcp/security/advisories/new),
  not a public issue.

## Contributing

Issues and pull requests are welcome. Before a PR, run `./build.sh` (gofmt + vet + test + build).
Tests use local fake servers and never call a real API; keep it that way, and never commit real
account ids, tokens or keys.

## License

[MIT](LICENSE) © HomieLab
