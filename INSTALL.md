# Setup guide

From zero to an agent (Claude Code, Claude Desktop, Cursor or any MCP client) that can call your
services. Do steps 1–2 once, then only the sections for the services you use.

## 1. Install

macOS / Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/homielab/homielab-mcp/main/install.sh | sh
mcp-homielab version
```

The script downloads the latest release for your OS and CPU, checks its SHA-256 and puts
`mcp-homielab` in `~/.local/bin` (`MCP_HOMIELAB_INSTALL_DIR=/usr/local/bin` to change; pin a
version with `MCP_HOMIELAB_VERSION=v0.1.0`). If `~/.local/bin` is not on your `PATH`, the script
prints the line to add.

Other ways:
- Windows: download `mcp-homielab_windows_<arch>.zip` from
  [Releases](https://github.com/homielab/homielab-mcp/releases) and put `mcp-homielab.exe` on your `PATH`.
- Go 1.27+: `go install github.com/homielab/homielab-mcp/cmd/mcp-homielab@latest` (to `$(go env GOPATH)/bin`).
- From source: `git clone https://github.com/homielab/homielab-mcp.git && cd homielab-mcp && ./build.sh`.

## 2. Register with your MCP client

```bash
mcp-homielab add all                              # Claude Code, user scope
mcp-homielab add adsense searchconsole            # or only some services
mcp-homielab add --client claude-desktop all      # Claude Desktop config file
mcp-homielab add --client cursor all              # ~/.cursor/mcp.json
mcp-homielab add --client json all                # print the config for any other client
```

Each service is a separate MCP server: `"<service>": { "command": "/path/to/mcp-homielab", "args": ["<service>"] }`.
Restart the client after `add`. Add only what you use: fewer servers means fewer tools in the
agent's context.

## 3. Google services

`adsense`, `admob`, `searchconsole`, `googleplay`, `blogger`, `chromewebstore` use Google OAuth.
One OAuth client serves all of them.

### 3.1 Google Cloud project (once)

1. Open <https://console.cloud.google.com> → create or pick a project.
2. **APIs & Services → Library**, enable the APIs you use:

   | Service | API to enable |
   |---|---|
   | `adsense` | AdSense Management API |
   | `admob` | AdMob API |
   | `searchconsole` | Google Search Console API |
   | `googleplay` | Google Play Android Developer API |
   | `blogger` | Blogger API v3 |
   | `chromewebstore` | Chrome Web Store API |

3. **APIs & Services → OAuth consent screen**: user type **External**, fill in app name and
   email, then **Audience → Add users** and add your Google account as a **test user**.
4. **APIs & Services → Credentials → Create credentials → OAuth client ID**: type **Desktop app**
   → **Download JSON**.

### 3.2 Put the client secret in the shared config dir

```bash
# macOS
mkdir -p ~/Library/Application\ Support/mcp-homielab
cp ~/Downloads/client_secret_*.json ~/Library/Application\ Support/mcp-homielab/
# Linux
mkdir -p ~/.config/mcp-homielab && cp ~/Downloads/client_secret_*.json ~/.config/mcp-homielab/
```

Windows: `%AppData%\mcp-homielab\`. Instead of a file you can set `<PREFIX>_CLIENT_ID` and
`<PREFIX>_CLIENT_SECRET` in the MCP registration (prefixes: `ADSENSE_MCP`, `ADMOB_MCP`,
`GSC_MCP`, `PLAY_MCP`, `BLOGGER_MCP`, `CWS_MCP`). `mcp-homielab <service> --help` prints the full
lookup order.

### 3.3 Sign in (once per service)

```bash
mcp-homielab adsense login
```

The browser opens Google's consent page → sign in → **Allow**. The token is saved to
`<config dir>/mcp-homielab/<service>/token.json` (0600). You can also ask the agent: it calls
`<prefix>_login` and `<prefix>_login_status`.

"Google hasn't verified this app" is normal: it is your own OAuth client. Choose *Advanced → Go
to … (unsafe)*.

Which account to use:
- `adsense`, `admob`: the account with access to the AdSense / AdMob account.
- `searchconsole`: owner or user of the properties.
- `googleplay`: listed in **Play Console → Users & permissions**. Scope is read-write.
- `blogger`: author or admin of the blogs. Scope is read-write.
- `chromewebstore`: member of the publisher, with 2-step verification. Scope is read-write.

## 4. App Store Connect

1. App Store Connect → **Users and Access → Integrations → App Store Connect API → Team Keys** →
   generate a key (Admin or App Manager for writes; Sales or Finance for reports). Download
   `AuthKey_<KEYID>.p8` (only once) and copy the **Issuer ID**.
2. Store and test it (vendor number: **Payments and Financial Reports**, needed for reports):

```bash
mcp-homielab appstoreconnect setup --key-file ~/Downloads/AuthKey_<KEYID>.p8 --issuer-id <ISSUER_ID> --vendor-number <N>
```

The key is copied to `<config dir>/mcp-homielab/appstoreconnect/` (0600).

## 5. Firefox Add-ons (AMO)

1. Sign in at <https://addons.mozilla.org/developers/addon/api/key/> → generate a **JWT issuer**
   (`user:12345:67`) and **JWT secret** (shown once).
2. Store and test them (paste the secret when asked):

```bash
mcp-homielab firefoxaddons setup --issuer user:12345:67
```

Or set `AMO_JWT_ISSUER` and `AMO_JWT_SECRET` in the MCP registration.

## 6. Gemini

Create a key at <https://aistudio.google.com/apikey> (calls are billed to its project). Set
`GEMINI_API_KEY` in the MCP registration, or store it (paste when asked):

```bash
mcp-homielab gemini setup
```

MP3 speech output needs `ffmpeg`.

## 7. Verify

In an agent session:

1. `<prefix>_login_status` (Google services), `asc_auth_status`, `amo_auth_status` → ready.
2. A read call: `adsense_list_accounts`, `gsc_list_sites`, `admob_list_apps`,
   `play_tracks {"packageName": "com.example.app"}`, `blogger_list_blogs`, `asc_apps`,
   `amo_my_addons`, `gemini_usage`.

## 8. Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `no OAuth client credentials found` | Secret file not found: step 3.2, or set the env vars |
| `invalid_grant` | Token revoked or expired: `mcp-homielab <service> logout`, then `login` |
| Consent page has no "Allow" | Your account is not a test user (step 3.1.3) |
| `403 PERMISSION_DENIED` | API not enabled in the project (step 3.1.2), or the account has no access |
| Search Analytics returns nothing | Data lags ~2 days: use an older date range or `dataState: "all"` |
| `gsc_inspect_url` quota error | ~2,000 inspections per property per day |
| Play `409 CONFLICT` | Another edit is open (one per developer account): wait a few minutes, retry |
| Play `401/403` after login | Account not in **Play Console → Users & permissions**, or API not enabled |
| AdMob `403` or no account | API not enabled, or the account never signed in to admob.google.com |
| Blogger `403` | API not enabled, or the account is not an author/admin of the blog |
| Chrome Web Store `403/404` | API not enabled, wrong publisherId, or the account is not a member of the publisher |
| App Store Connect `401 NOT_AUTHORIZED` | Wrong key id / issuer id / .p8, or the system clock is off |
| App Store Connect `403 FORBIDDEN_ERROR` | The key's role is too low: writes need Admin or App Manager, reports Sales or Finance |
| AMO `401` | Wrong issuer or secret (run `setup` again), or the system clock is off |
| AMO `403` | Not an author of the add-on, or the developer agreement is not accepted |
| `claude mcp add failed` | The name is already registered: `claude mcp remove --scope user <service>`, then `add` again |
| Revoke Google access | <https://myaccount.google.com/permissions> → Remove, then `logout` and `login` |

## 9. Update

Run the install command again (or `go install …@latest`, or `git pull && ./build.sh`). Tokens and
keys live outside the binary and survive updates.
