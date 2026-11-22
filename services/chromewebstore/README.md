# Chrome Web Store

MCP server (Go) for the **Chrome Web Store API v2**. It reads and **writes** your store items:
status, package upload, publish (skip review, staged, percentage rollout), cancel submission,
rollback, user reviews, rating summary and install/user metrics.

## Tools

| Tool | What it does |
|---|---|
| `cws_login` | One-time OAuth consent in your browser; **non-blocking** — call `cws_login_status` after approving. Scope `chromewebstore` (read-write). |
| `cws_login_status` | Is the flow running / finished / failed? Is a token stored? |
| `cws_logout` | Delete the stored token |
| `cws_status` | Published version and rollout %, submitted revision state (PENDING_REVIEW, STAGED, REJECTED, …), last upload state, policy warning / takedown |
| `cws_upload` | **Write**: upload a `.zip` or an extension folder (zipped in memory, dot files left out) as the new draft; checks `manifest.json` is at the root |
| `cws_publish` | **Write, releases to users**: submit for review; `skipReview`, `staged` (release later), `deployPercentage`, `blockOnWarnings` |
| `cws_set_deploy_percentage` | **Write**: raise the rollout of the published version (items with over 10,000 seven-day users) |
| `cws_cancel_submission` | **Write**: cancel the submission under review |
| `cws_rollback` | **Write, releases to users**: republish an earlier version under a new higher version; `validateOnly` dry run |
| `cws_rating_summary` | Average rating, count, count per star |
| `cws_reviews` | User reviews: newest / lowest / highest first, filter by rating, paginated |
| `cws_metrics` | Daily installs, installs-7d, uninstalls, users, users-7d, impressions, page-views; split by one dimension (country, os, app-version, install-source, language, utm-*, …) |

Input conveniences:

- `publisherId`: from **Developer Dashboard → Publisher → Settings**. `itemId`: the 32-letter
  extension id or its store URL. The API cannot list items, so both are needed once; the last pair
  used is remembered (`last_item.json` in the config dir). `CWS_MCP_PUBLISHER_ID` sets a default
  publisher.
- Metrics dates: `dateRange` preset (`LAST_7_DAYS`, `LAST_30_DAYS` default, `LAST_MONTH`, …) or
  `startDate` + `endDate` (`YYYY-MM-DD`).

### Prompts

- `cws_item_review` — read-only review of an extension: release state, policy flags,
  ratings and recurring complaints, installs/uninstalls/users, markets, and an action list.

## Setup

Needs the `mcp-homielab` binary ([install](../../README.md#install)).

1. Google Cloud Console, in the project of your Google OAuth client: *APIs & Services → Library*
   → **Chrome Web Store API** → **Enable**. The Desktop OAuth client of the other Google services works as is: put its
   `client_secret*.json` in `<user config dir>/mcp-homielab/` (shared by all six Google services;
   macOS: `~/Library/Application Support/mcp-homielab/`, Linux: `~/.config/mcp-homielab/`).
   `mcp-homielab chromewebstore --help` prints the lookup order.
2. Log in with a Google account that is a member of the publisher (2-step verification is required
   to publish):
   ```bash
   mcp-homielab chromewebstore login
   ```
   or call `cws_login` in a session, then `cws_login_status`.
   Token: `<user config dir>/mcp-homielab/chromewebstore/token.json` (0600).
3. Register: `mcp-homielab add chromewebstore` (Claude Code, user scope), or
   `mcp-homielab add --client claude-desktop chromewebstore` (also `cursor`; `json` prints the config).
   Manual config entry:
   `"chromewebstore": { "command": "/path/to/mcp-homielab", "args": ["chromewebstore"] }`.

Env vars instead of a secret file: `CWS_MCP_CLIENT_ID`, `CWS_MCP_CLIENT_SECRET`.

## Example asks

- *"Ship version 1.4 of my extension"* → `cws_upload { "path": "./dist", "publisherId": "...", "itemId": "..." }`,
  then `cws_publish`, then `cws_status` until PUBLISHED
- *"Release to 10% first"* → `cws_publish { "deployPercentage": 10 }`, later `cws_set_deploy_percentage { "deployPercentage": 100 }`
- *"1.4 is broken, go back to 1.3"* → `cws_rollback { "targetVersion": "1.3", "newVersion": "1.4.1", "reason": "...", "validateOnly": true }`, then without `validateOnly`
- *"What do 1-star reviews say?"* → `cws_reviews { "rating": 1 }`
- *"Installs by country last month"* → `cws_metrics { "metrics": ["installs"], "dimension": "country", "dateRange": "LAST_MONTH" }`

## Notes

- The API cannot create items, edit the store listing or privacy tab, or reply to reviews: use the
  Developer Dashboard. A new item needs its Store listing and Privacy tabs filled before its first publish.
- Publishing uses the item's current visibility. After changing visibility in the dashboard,
  publish once there before the API can publish again.
- Revoke access at <https://myaccount.google.com/permissions>.

## Development

```bash
go test ./services/chromewebstore   # item id/URL parsing and memory, folder zipping, manifest check, metrics query, HTTP calls
```
