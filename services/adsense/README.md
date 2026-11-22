# AdSense

MCP server (Go) for the **Google AdSense Management API v2** — read-only.
It exposes your AdSense data as MCP tools: accounts, sites, ad units, payments,
alerts, and arbitrary reports (by site, page, ad unit, country, platform, ad
format, bid type, traffic source, …) for any date range.

Built with the official Go SDK `github.com/modelcontextprotocol/go-sdk`.

## Tools

| Tool | What it does |
|---|---|
| `adsense_login` | One-time OAuth consent in your browser; **non-blocking** — returns immediately, call `adsense_login_status` after approving. Stores a refresh token locally (read-only scope). |
| `adsense_login_status` | Is the flow running / finished / failed? Is a token stored? |
| `adsense_logout` | Delete the stored token |
| `adsense_preset_report` | **Run one or many ready-made reports in a single call** — see presets below |
| `adsense_generate_report` | Fully custom report: any dimensions/metrics/filters/orderBy; markdown table + totals/averages |
| `adsense_generate_report_csv` | Same, raw CSV (up to 1,000,000 rows) |
| `adsense_list_accounts` | Accessible AdSense accounts (`accounts/pub-XXXX`) |
| `adsense_get_account` | Account details (name, time zone, premium) |
| `adsense_period_comparison` | **Period-over-period deltas**: same report for the current window and the preceding equal-length window, per-key changes sorted by absolute change of the first metric |
| `adsense_list_sites` | Registered sites + review state + auto-ads |
| `adsense_list_ad_units` | Ad units of the content ad client |
| `adsense_list_saved_reports` | Reports you saved in the AdSense UI |
| `adsense_generate_saved_report` | Run a saved report (date range overridable, CSV optional) |
| `adsense_list_payments` | Payment records |
| `adsense_list_alerts` | Active account alerts (policy/payment) |
| `adsense_list_policy_issues` | Policy-center violations per site |
| `adsense_list_channels` | Custom channels / URL channels |
| `adsense_get_ad_code` | HTML ad-code snippet of an ad unit — or `clientLevel: true` for the account-wide code |
| `adsense_get_ad_blocking_recovery_tag` | Ad-blocking recovery tag script |
| `adsense_list_child_accounts` | Sub-accounts, if any |
| `adsense_list_ad_clients` | Ad clients (AFC/AFS/...) — pass as `adClientId` elsewhere |
| `adsense_list_channel_links` | Ad unit ↔ custom channel membership, both directions |

### Report presets (`adsense_preset_report`)

Pass `presets: ["sites", "pages", "countries", "daily", …]` plus the shared
date arguments; every section comes back sorted by earnings (or by date):

`sites`, `pages` (top 100), `ad_units`, `countries`, `site_countries`,
`site_daily`, `platforms`, `ad_formats`, `ad_sizes`, `bid_types`,
`traffic_sources`, `products`, `content_platform`, `os`, `browsers`,
`daily`, `weekly`, `monthly`.

Each preset ships sensible metrics (earnings, views, impressions, clicks, RPM)
so a single call like `["sites", "ad_units", "countries", "platforms", "daily"]`
produces a complete performance picture. A preset whose dimension combination
the API rejects is reported inline without aborting the others.

### Prompts

- `adsense_deep_analysis` — a ready-made analysis template (optional
  `date_range` and `account` arguments): runs the full preset sweep, checks
  alerts/policy issues/site states, then writes a structured report with
  recommended actions. Surface it from your client's prompt menu.

## Setup (one time, ~5 minutes)

Needs the `mcp-homielab` binary ([install](../../README.md#install)).

1. **Google Cloud project + OAuth client** (the API is free):
   - Go to <https://console.cloud.google.com> → create (or pick) a project.
   - *APIs & Services → Library* → search **"AdSense Management API"** → **Enable**.
   - *APIs & Services → OAuth consent screen* → External → fill the minimum
     fields → add yourself as a **test user** (required while the app is in
     testing status).
   - *APIs & Services → Credentials → Create credentials → OAuth client ID* →
     Application type **Desktop app** → Create → **Download JSON**.

2. **Give the credentials to the server** — either:
   - copy the downloaded file as `client_secret.json` (any name matching
     `client_secret*.json`) into `<user config dir>/mcp-homielab/`, shared by all
     six Google services (macOS: `~/Library/Application Support/mcp-homielab/`,
     Linux: `~/.config/mcp-homielab/`), or
   - set env vars on the MCP server registration:
     ```
     ADSENSE_MCP_CLIENT_ID=xxxxx.apps.googleusercontent.com
     ADSENSE_MCP_CLIENT_SECRET=GOCSPX-...
     ```
   Lookup order: `$ADSENSE_MCP_CLIENT_ID` + `$ADSENSE_MCP_CLIENT_SECRET` →
   `$ADSENSE_MCP_CLIENT_SECRET_FILE` → `./client_secret.json` →
   `<user config dir>/mcp-homielab/adsense/client_secret*.json` →
   `<user config dir>/mcp-homielab/client_secret*.json` → the other Google
   services' config dirs. `mcp-homielab adsense --help` prints it.

3. **Authorize** once (token is stored at
   `<user config dir>/mcp-homielab/adsense/token.json`, mode 0600):
   ```
   mcp-homielab adsense login
   ```
   A browser window opens; sign in with the Google account that owns the
   AdSense account and approve the read-only request. Inside an MCP session you
   can also just call the `adsense_login` tool, then `adsense_login_status`.

4. **Register** with your client:
   ```
   mcp-homielab add adsense                          # Claude Code (user scope)
   mcp-homielab add --client claude-desktop adsense  # or cursor; json prints the config
   ```
   Manual config entry, with the optional env vars:
   ```json
   "adsense": {
     "command": "/path/to/mcp-homielab",
     "args": ["adsense"],
     "env": {
       "ADSENSE_MCP_CLIENT_ID": "xxxxx.apps.googleusercontent.com",
       "ADSENSE_MCP_CLIENT_SECRET": "GOCSPX-..."
     }
   }
   ```

## Example asks

Once registered, ask your agent things like:

- *"Full AdSense analysis for the last 30 days: sites, pages, ad units, countries, platforms"* →
  `adsense_preset_report { "presets": ["sites", "pages", "ad_units", "countries", "platforms", "ad_formats", "daily"] }`
- *"Top 50 earning pages this month"* →
  `adsense_preset_report { "presets": ["pages"], "dateRange": "MONTH_TO_DATE" }`
- *"Only example.com, by country"* →
  `adsense_preset_report { "presets": ["site_countries"], "filterDomain": "example.com" }`
- *"Sep 1–15, earnings by week"* →
  `adsense_preset_report { "presets": ["weekly"], "startDate": "2026-09-01", "endDate": "2026-09-15" }`
- *"Export full daily earnings as CSV"* →
  `adsense_generate_report_csv { "dimensions": ["DATE"], "metrics": ["ESTIMATED_EARNINGS", "PAGE_VIEWS", "CLICKS"] }`
- *"Which sites earned more this month than last month?"* →
  `adsense_period_comparison { "dimensions": ["DOMAIN_NAME"], "dateRange": "MONTH_TO_DATE" }`
- *"Which ad unit lost the most earnings this week?"* →
  `adsense_period_comparison { "dimensions": ["AD_UNIT_NAME"], "dateRange": "LAST_7_DAYS" }`

## Robustness

- HTTP client timeout 90s per request; automatic retry (3 attempts, backoff,
  honors `Retry-After`) on 429/5xx and transient network errors — client
  errors (4xx) are never retried.
- `totalMatchedRows` and other int64 fields are decoded the way Google
  serializes them (JSON strings).
- Login flow: 5-minute window, PKCE + state validation, loopback redirect on a
  random port; the tool never blocks the session waiting for a human.
- Date handling: `startDate` + `endDate` without `dateRange` implies CUSTOM.
- `adsense_generate_report_csv` accepts `saveToPath` to write the export straight
  to a file (returns path + preview instead of flooding the conversation).
- Server instructions ship in the MCP handshake so agents pick the right tool
  without extra prompting.

### Coverage note

Implemented (read) — the complete read surface of the API: accounts
(list/get/children/ad-blocking-recovery-tag), ad clients (+ client-level ad
code), ad units (+ ad code + linked custom channels), sites, custom/URL channels
(+ linked ad units), saved reports (list/generate/generateCsv), arbitrary and
preset reports incl. CSV, payments, alerts, and policy issues.

Deliberately out of scope: mutating methods (`adunits.create/patch`,
`customchannels.create/delete/patch`) — the server requests the read-only
`adsense.readonly` scope and cannot change your account; and single-item getters
fully redundant with the list tools (`sites.get`, `adunits.get`,
`adclients.get`, `customchannels.get`, `urlchannels.get`, `policyIssues.get`,
`reports.getSaved`).

## Development

From the repo root:

```
go test ./services/adsense   # query building, rendering, pagination, presets
go build ./cmd/mcp-homielab                   # the mcp-homielab binary
```

## Notes

- The server requests only the `adsense.readonly` scope — it can never modify
  your account.
- Tokens live at `<user config dir>/mcp-homielab/adsense/token.json`
  (macOS: `~/Library/Application Support/mcp-homielab/adsense/token.json`);
  `MCP_HOMIELAB_CONFIG_DIR` replaces the `<user config dir>/mcp-homielab` part.
- If a tool returns "not authenticated", run `mcp-homielab adsense login` or call the
  `adsense_login` tool from your client.
- Revoke access any time at <https://myaccount.google.com/permissions>.
