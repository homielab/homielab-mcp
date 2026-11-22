# AdMob

MCP server (Go) for the **Google AdMob API v1** — read-only. It exposes your
AdMob account as MCP tools: accounts, apps, ad units, and network / mediation
reports for any date range.

Built with the official Go SDK `github.com/modelcontextprotocol/go-sdk`.

## Tools

| Tool | What it does |
|---|---|
| `admob_login` | One-time OAuth consent in your browser; **non-blocking** — call `admob_login_status` after approving. Stores a refresh token locally (scope `admob.readonly`). |
| `admob_login_status` | Is the flow running / finished / failed? Is a token stored? |
| `admob_logout` | Delete the stored token |
| `admob_list_accounts` | Publisher accounts with reporting time zone and currency |
| `admob_list_apps` | Apps: name, app id, platform, store id, approval state |
| `admob_list_ad_units` | Ad units (optionally of one `appId`): id, format, ad types |
| `admob_network_report` | AdMob Network report: any dimensions/metrics, `filters`, sorting, up to 100,000 rows, currency conversion, CSV `saveToPath`; markdown table + totals |
| `admob_mediation_report` | Mediation report per ad source / ad source instance / mediation group: observed eCPM, match rate, latency |
| `admob_preset_report` | **Several ready-made cuts in one call** — see presets below |
| `admob_period_comparison` | Earnings / impressions / eCPM per key versus the previous period or a custom baseline (`baselineStartDate`/`baselineEndDate`), sorted by the largest earnings change |

Dimensions and metrics:

- Network: `DATE`, `WEEK`, `MONTH`, `APP`, `AD_UNIT`, `AD_TYPE`, `FORMAT`, `COUNTRY`, `PLATFORM`,
  `MOBILE_OS_VERSION`, `GMA_SDK_VERSION`, `APP_VERSION_NAME`, `SERVING_RESTRICTION` ×
  `ESTIMATED_EARNINGS`, `IMPRESSIONS`, `CLICKS`, `IMPRESSION_CTR`, `IMPRESSION_RPM`, `AD_REQUESTS`,
  `MATCHED_REQUESTS`, `MATCH_RATE`, `SHOW_RATE`.
- Mediation adds `AD_SOURCE`, `AD_SOURCE_INSTANCE`, `MEDIATION_GROUP` and `OBSERVED_ECPM`,
  `AD_LOAD_LATENCY` (no `AD_TYPE`, `IMPRESSION_RPM`, `SHOW_RATE`).
- At most one of `DATE` / `WEEK` / `MONTH`. `AD_TYPE` excludes `AD_REQUESTS`, `MATCH_RATE`,
  `IMPRESSION_RPM` (dropped automatically from the default metrics).
- Date presets (`TODAY`, `YESTERDAY`, `LAST_7_DAYS`, `LAST_30_DAYS`, `MONTH_TO_DATE`,
  `LAST_MONTH`, `YEAR_TO_DATE`) use the account's reporting time zone.

### Presets (`admob_preset_report`)

`daily`, `weekly`, `monthly`, `apps`, `ad_units` (app × ad unit), `formats`, `countries`,
`platforms`, `app_versions`, `sdk_versions`, `os_versions` (network) and `ad_sources`,
`mediation_groups` (mediation). A preset the API rejects is reported inline without aborting
the others.

### Prompts

- `admob_analysis` — analysis template (optional `date_range`, `account_id`): preset
  sweep, mediation, period comparison, then a structured report with recommended actions.

## Setup (one time)

Needs the `mcp-homielab` binary ([install](../../README.md#install)).

1. **Google Cloud project + OAuth client** — the same project and Desktop app client as the
   other Google services work:
   - *APIs & Services → Library* → **AdMob API** → **Enable**.
   - *OAuth consent screen* → External; while in Testing, add your Google account as a **test user**.
   - *Credentials → Create OAuth client ID* → **Desktop app** → download JSON.
2. **Credentials** — put `client_secret*.json` in `<user config dir>/mcp-homielab/` (shared by all
   six Google services; macOS: `~/Library/Application Support/mcp-homielab/`, Linux:
   `~/.config/mcp-homielab/`) or `<user config dir>/mcp-homielab/admob/`, or set
   `ADMOB_MCP_CLIENT_ID` / `ADMOB_MCP_CLIENT_SECRET`. `mcp-homielab admob --help` prints the
   lookup order.
3. **Authorize** once (token at `<user config dir>/mcp-homielab/admob/token.json`, mode 0600):
   ```
   mcp-homielab admob login
   ```
   Sign in with a Google account that has access to the AdMob account. Inside a session you can
   call `admob_login`, then `admob_login_status`.
4. **Register** with your MCP client:
   ```
   mcp-homielab add admob                          # Claude Code (user scope)
   mcp-homielab add --client claude-desktop admob  # or cursor; json prints the config
   ```
   Manual config entry: `"admob": { "command": "/path/to/mcp-homielab", "args": ["admob"] }`.

## Example asks

- *"AdMob earnings last 30 days, overall picture"* → the `admob_analysis` prompt, or
  `admob_preset_report { "presets": ["daily", "apps", "ad_units", "formats", "countries"] }`
- *"Which app earned the most this month?"* →
  `admob_network_report { "dimensions": ["APP"], "dateRange": "MONTH_TO_DATE" }`
- *"eCPM by country for one app"* →
  `admob_network_report { "dimensions": ["COUNTRY"], "metrics": ["ESTIMATED_EARNINGS", "IMPRESSIONS", "IMPRESSION_RPM"], "filters": { "APP": ["ca-app-pub-123~456"] } }`
- *"Which mediation networks perform best?"* →
  `admob_mediation_report { "dimensions": ["AD_SOURCE"], "sortBy": "OBSERVED_ECPM" }`
- *"What dropped compared with last week?"* →
  `admob_period_comparison { "dimensions": ["AD_UNIT"], "dateRange": "LAST_7_DAYS" }`
- *"This October vs last October by app"* →
  `admob_period_comparison { "startDate": "2026-10-01", "endDate": "2026-10-31", "baselineStartDate": "2025-10-01", "baselineEndDate": "2025-10-31" }`
- *"Export daily earnings per ad unit as CSV"* →
  `admob_network_report { "dimensions": ["DATE", "AD_UNIT"], "maxRows": 100000, "saveToPath": "admob-daily.csv" }`

## Robustness

- Dimensions, metrics, sort, filters, dates, row limit and currency are validated before the
  API call (unknown names, duplicates, two time dimensions, `AD_TYPE` conflicts).
- Retry (3 attempts, backoff, `Retry-After`) on 429/5xx and network errors; 4xx never retried.
- Report values are decoded from the API's `microsValue` / `integerValue` / `doubleValue`
  (earnings in currency units, rates as 0–1, shown as percentages).
- When `maxRows` cuts the result, the header shows how many rows matched.

### Coverage note

Implemented: the full AdMob API v1 (accounts list/get, apps list, ad units list, network and
mediation reports). Not implemented: the v1beta write and mediation-management methods (create
apps / ad units / mediation groups, ad unit mappings, A/B experiments) and the campaign report —
they are in closed beta and need Google to allowlist the account.

## Development

From the repo root:

```
go test ./services/admob   # spec building and validation, date presets, report decoding,
                           # rendering/CSV, comparison, pagination
go build ./cmd/mcp-homielab                 # the mcp-homielab binary
```

## Notes

- The server requests only the `admob.readonly` scope — it cannot change your account.
- Revoke access any time at <https://myaccount.google.com/permissions>.
