# Setup guide

Set up the MCP collection (`mcp-adsense`, `mcp-searchconsole`, `mcp-androidpublisher`,
`mcp-admob`, `mcp-blogger`, `mcp-chromewebstore`, `mcp-appstoreconnect`, `mcp-amo`, `mcp-gemini`) on a new machine, until an agent (Claude Code, Claude
Desktop or any MCP client) can call AdSense, Search Console, Play Developer, AdMob, Blogger, Chrome Web Store, App Store Connect, Firefox Add-ons (AMO) and Gemini media generation.

---

## 1. Requirements

| Need | Version | Check |
|---|---|---|
| Go (only to build from source) | ≥ 1.27 (see `go.mod`) | `go version` |
| Git | any | `git --version` |
| Browser | Chrome/Safari/Firefox | for the OAuth consent page |
| Google account | owns the AdSense account / Search Console properties; for Play: listed in Play Console Users & permissions; for AdMob: has access to the AdMob account; for Blogger: author or admin of the blogs; for Chrome Web Store: member of the publisher, with 2-step verification | |

## 2. Clone and build

```bash
git clone https://github.com/homielab/homielab-mcp.git
cd homielab-mcp
./build.sh
```

Without a clone, install single servers with
`go install github.com/homielab/homielab-mcp/mcp-adsense@latest` (same output dir).

`build.sh` runs gofmt + vet + test, then builds each server into `$(go env GOPATH)/bin`
(default `~/go/bin/`; change with `BIN_DIR=...`):

- `~/go/bin/mcp-adsense`
- `~/go/bin/mcp-searchconsole`
- `~/go/bin/mcp-androidpublisher`
- `~/go/bin/mcp-admob`
- `~/go/bin/mcp-blogger`
- `~/go/bin/mcp-chromewebstore`
- `~/go/bin/mcp-appstoreconnect`
- `~/go/bin/mcp-amo`
- `~/go/bin/mcp-gemini`

Steps 3–5 are for the Google OAuth servers; App Store Connect, AMO and Gemini use API keys (steps 5b, 5c, 5d).

## 3. Google Cloud setup (once for all servers)

1. Open <https://console.cloud.google.com> → create (or pick) a project.
2. **APIs & Services → Library**, enable **all six**:
   - **AdSense Management API**
   - **Google Search Console API** (may show the old name "Webmaster Tools API")
   - **Google Play Android Developer API**
   - **AdMob API**
   - **Blogger API v3**
   - **Chrome Web Store API**
3. **APIs & Services → OAuth consent screen**:
   - User type: **External** → create.
   - Fill in at least app name + email; then **Audience → Add users** and add your Gmail
     address as a **test user** (required while the app is in Testing).
4. **APIs & Services → Credentials → Create credentials → OAuth client ID**:
   - Application type: **Desktop app**
   - Create → **Download JSON** (named like `client_secret_xxxx.apps.googleusercontent.com.json`)

> One Desktop app client serves all six servers.

## 4. Place the client secret

Create the config dir and copy the secret in (the original file name is fine):

```bash
mkdir -p ~/Library/Application\ Support/mcp-adsense
cp ~/Downloads/client_secret_*.json ~/Library/Application\ Support/mcp-adsense/
```

Each server looks for the secret in this order: `$<PREFIX>_CLIENT_SECRET_FILE` →
`./client_secret.json` → its own config dir → the other servers' config dirs. So **one file in
any of these dirs serves all six**. `<binary> --help` prints the order.

Linux: the config dir is `~/.config/mcp-adsense/` (from `os.UserConfigDir`).
Instead of a file you can set env vars in the MCP registration:
`ADSENSE_MCP_CLIENT_ID/SECRET`, `GSC_MCP_CLIENT_ID/SECRET`, `PLAY_MCP_CLIENT_ID/SECRET`,
`ADMOB_MCP_CLIENT_ID/SECRET`, `BLOGGER_MCP_CLIENT_ID/SECRET`, `CWS_MCP_CLIENT_ID/SECRET`.

## 5. Log in (once per server)

```bash
~/go/bin/mcp-adsense login
~/go/bin/mcp-searchconsole login
~/go/bin/mcp-androidpublisher login
~/go/bin/mcp-admob login
~/go/bin/mcp-blogger login
~/go/bin/mcp-chromewebstore login
```

Each command opens Google's consent page in the browser → sign in with the account that owns the
AdSense account / Search Console properties (for Play: an account in **Play Console → Users &
permissions** of the developer account) → **Allow**. The refresh token is saved to:

- `~/Library/Application Support/mcp-adsense/token.json`
- `~/Library/Application Support/mcp-searchconsole/token.json`
- `~/Library/Application Support/mcp-androidpublisher/token.json`
- `~/Library/Application Support/mcp-admob/token.json`
- `~/Library/Application Support/mcp-blogger/token.json`
- `~/Library/Application Support/mcp-chromewebstore/token.json`

(Linux: `~/.config/<server>/token.json`.) Files are 0600. AdSense and AdMob use read-only scopes; Search
Console uses `webmasters` (also adds/removes properties and sitemaps); Play uses the read-write
`androidpublisher` scope; Blogger uses the read-write `blogger` scope; Chrome Web Store the read-write `chromewebstore` scope.

> The warning "Google hasn't verified this app" is **normal** (it is your own OAuth client):
> choose *Advanced → Go to ... (unsafe)* to continue.

## 5b. App Store Connect API key

1. App Store Connect → **Users and Access → Integrations → App Store Connect API → Team Keys** →
   generate a key (Admin or App Manager for writes; Sales or Finance for reports). Download
   `AuthKey_<KEYID>.p8` (only once) and copy the **Issuer ID**.
2. Store and test it (vendor number: **Payments and Financial Reports**, needed for reports):

```bash
~/go/bin/mcp-appstoreconnect setup --key-file ~/Downloads/AuthKey_<KEYID>.p8 --issuer-id <ISSUER_ID> --vendor-number <N>
```

The key is copied to `~/Library/Application Support/mcp-appstoreconnect/` (0600) with `config.json`.

## 5c. Firefox Add-ons (AMO) API key

1. Sign in at <https://addons.mozilla.org/developers/addon/api/key/> → generate credentials: a
   **JWT issuer** (`user:12345:67`) and a **JWT secret** (shown once).
2. Store and test them (paste the secret when asked):

```bash
~/go/bin/mcp-amo setup --issuer user:12345:67
```

Saved to `~/Library/Application Support/mcp-amo/credentials.json` (0600).

## 5d. Gemini API key

Create a key at <https://aistudio.google.com/apikey> (calls are billed to its project). Set
`GEMINI_API_KEY` in the MCP registration, or store the key (paste it when asked):

```bash
~/go/bin/mcp-gemini setup
```

Saved to `~/Library/Application Support/mcp-gemini/api_key` (0600). MP3 speech needs `ffmpeg`.

## 6. Register with your MCP client

Claude Code:

```bash
for s in mcp-adsense mcp-searchconsole mcp-androidpublisher mcp-admob mcp-blogger mcp-chromewebstore mcp-appstoreconnect mcp-amo mcp-gemini; do
  claude mcp add "$s" "$(go env GOPATH)/bin/$s"
done
```

JSON-config clients (Claude Desktop `claude_desktop_config.json`, Cursor, ...) — key
`mcpServers`, `command` is an absolute path (`echo "$(go env GOPATH)/bin"`):

```json
{
  "mcpServers": {
    "mcp-adsense": { "command": "/path/to/go/bin/mcp-adsense" },
    "mcp-searchconsole": { "command": "/path/to/go/bin/mcp-searchconsole" },
    "mcp-androidpublisher": { "command": "/path/to/go/bin/mcp-androidpublisher" },
    "mcp-admob": { "command": "/path/to/go/bin/mcp-admob" },
    "mcp-blogger": { "command": "/path/to/go/bin/mcp-blogger" },
    "mcp-chromewebstore": { "command": "/path/to/go/bin/mcp-chromewebstore" },
    "mcp-appstoreconnect": { "command": "/path/to/go/bin/mcp-appstoreconnect" },
    "mcp-amo": { "command": "/path/to/go/bin/mcp-amo" },
    "mcp-gemini": { "command": "/path/to/go/bin/mcp-gemini" }
  }
}
```

Restart the client → all nine servers show as connected.

## 7. Verify

In a session, in order:

1. `adsense_login_status` / `gsc_login_status` / `play_login_status` / `admob_login_status` / `blogger_login_status` / `cws_login_status` → token present, credentials configured
2. `adsense_list_accounts` → shows `accounts/pub-XXXX`
3. `gsc_list_sites` → shows your properties (`https://.../` or `sc-domain:...`)
4. `play_tracks {"packageName": "com.example.app"}` → list of tracks (Play tools are read-write: only call write tools when you accept publishing)
5. `admob_list_apps` → your AdMob apps; `blogger_list_blogs` → your blogs (Blogger tools are read-write); `cws_status {"publisherId": "...", "itemId": "..."}` → your extension's status
6. `asc_auth_status` → key works; `asc_apps` → your App Store apps; `amo_auth_status` → AMO key works; `amo_my_addons` → your Firefox add-ons
7. Try a report: `adsense_preset_report {"presets": ["daily"]}`,
   `gsc_search_analytics {"dimensions": ["date"]}` and `admob_preset_report {"presets": ["daily"]}`

If a tool returns "not authenticated" → repeat step 5.

## 8. Troubleshooting

| Symptom | Cause & fix |
|---|---|
| `no OAuth client credentials found` | Secret file not in a searched location — see step 4, or set the env vars |
| `invalid_grant` on login | Token/refresh revoked: `~/go/bin/mcp-adsense logout`, then log in again; or delete token.json |
| Tool returns `403 PERMISSION_DENIED` | API not enabled in the GCP project (step 3.2), or the Google account does not own the property |
| Search Analytics returns nothing | Data lags ~2 days — try an older `dateRange` or `dataState: "all"` |
| `gsc_inspect_url` quota error | Inspection API allows ~2,000 calls/property/day — not for bulk URLs |
| Consent page has no "Allow" | Test user not added in step 3.3 |
| Play returns `409 CONFLICT` | Another "edit" is open (one per developer account) — wait a few minutes for it to expire, then retry |
| Play returns `401/403` after login | Google account not in **Play Console → Users & permissions**, or **Google Play Android Developer API** not enabled |
| AdMob returns `403` or no account | **AdMob API** not enabled, or the Google account has no access to the AdMob account (sign in to admob.google.com with it once) |
| Blogger returns `403` | **Blogger API v3** not enabled, or the account is not an author/admin of the blog (drafts, pending comments need admin) |
| Chrome Web Store returns `403` / `404` | **Chrome Web Store API** not enabled, wrong publisherId (Developer Dashboard → Publisher → Settings), or the account is not a member of that publisher |
| App Store Connect `401 NOT_AUTHORIZED` | Wrong key id / issuer id / .p8, or a team key set up without `--issuer-id` (it is then sent as an individual key); also check the Mac's clock |
| App Store Connect `403 FORBIDDEN_ERROR` | The key's role is too low: writes need Admin or App Manager, reports Sales or Finance |
| AMO `401` (`ERROR_SIGNATURE_EXPIRED` / `ERROR_DECODING_SIGNATURE`) | Wrong issuer/secret (`mcp-amo setup` again) or the Mac's clock is off |
| AMO `403` | The account is not an author/owner of the add-on, or has not accepted the developer agreement on AMO |
| Revoke access | <https://myaccount.google.com/permissions> → Remove, then `logout` + log in again |

## 9. Update later

```bash
git pull && ./build.sh   # in the repo dir
```

Login tokens survive rebuilds (they live outside the repo).
