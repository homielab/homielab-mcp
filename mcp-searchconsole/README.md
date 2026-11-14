# mcp-searchconsole

MCP server (Go) for the **Google Search Console API** (Webmaster Tools). It
exposes your Search Console data as MCP tools: properties, Search Analytics
performance reports (clicks / impressions / CTR / position by any dimension),
sitemaps, and URL inspection — plus adding/removing properties and sitemaps.

Built with the official Go SDK `github.com/modelcontextprotocol/go-sdk`.

## Tools

| Tool | What it does |
|---|---|
| `gsc_login` | One-time OAuth consent in your browser; **non-blocking** — call `gsc_login_status` after approving. Stores a refresh token locally (scope `webmasters`). |
| `gsc_login_status` | Is the flow running / finished / failed? Is a token stored? |
| `gsc_logout` | Delete the stored token |
| `gsc_list_sites` | Properties (URL-prefix / `sc-domain:`) with permission level |
| `gsc_search_analytics` | Performance report: clicks, impressions, CTR, position grouped by `date` / `hour` / `query` / `page` / `country` / `device` / `searchAppearance`, with filters (incl. regex), search type (web/image/video/news/discover/googleNews), aggregation, pagination up to 25,000 rows, client-side `sortBy`/`sortOrder` (clicks, impressions, ctr, position) and `minImpressions`/`minPosition`/`maxPosition` row filters, optional CSV `saveToPath` |
| `gsc_quick_report` | **Several analytics cuts in one call**: daily trend, top queries, top pages, device split, country split, query→page mapping (cannibalization check) |
| `gsc_period_comparison` | **Period-over-period deltas**: runs the same query for the current window and a baseline (default: the preceding equal-length window; or `baselineStartDate`/`baselineEndDate`, e.g. last year), reports per-key click/impression/CTR/position changes sorted by absolute click change |
| `gsc_list_sitemaps` | Submitted sitemaps + per-content-type submitted/indexed counts; `sitemapIndex` lists the children of one index |
| `gsc_get_sitemap` | Details of one sitemap (feedpath) |
| `gsc_inspect_url` | URL Inspection: index verdict, coverage state, robots.txt, canonicals, last crawl, AMP / mobile usability / rich results with their issue messages (~2,000 queries/property/day — don't loop it) |
| `gsc_add_site` | **Write**: add a URL-prefix or `sc-domain:` property (unverified until ownership is verified) |
| `gsc_delete_site` | **Write, destructive**: remove a property from the account; `siteUrl` must match `gsc_list_sites` exactly |
| `gsc_submit_sitemap` | **Write**: submit or resubmit a sitemap URL |
| `gsc_delete_sitemap` | **Write, destructive**: remove a sitemap from the Sitemaps report |
| `gsc_inspect_urls` | Up to 10 URLs inspected concurrently: summary of indexed / not indexed / canonical mismatch / robots.txt block / fetch error / invalid rich results, one row per URL |

### Prompts

- `gsc_seo_analysis` — ready-made SEO analysis template (optional
  `date_range`, `site_url` arguments): daily trend, top queries/pages,
  query→page mapping (cannibalization), device/country split, sitemap health,
  index inspection of key pages, and a prioritized action list.

## Setup (one time, ~5 minutes)

1. **Google Cloud project + OAuth client** (the API is free):
   - <https://console.cloud.google.com> → create (or pick) a project.
   - *APIs & Services → Library* → search **"Google Search Console API"**
     (may appear as "Webmaster Tools API") → **Enable**.
   - *OAuth consent screen* → External → minimum fields → add your Google
     account as **test user**.
   - *Credentials → Create OAuth client ID* → **Desktop app** → download JSON.

2. **Give the credentials to the server** — either:
   - copy the JSON as `client_secret*.json` into
     `<user config dir>/mcp-searchconsole/`
     (macOS: `~/Library/Application Support/mcp-searchconsole/`), or
   - reuse the file you made for another server in this collection — their
     config dirs are probed automatically, or
   - set env vars on the MCP registration:
     ```
     GSC_MCP_CLIENT_ID=xxxxx.apps.googleusercontent.com
     GSC_MCP_CLIENT_SECRET=GOCSPX-...
     ```

3. **Authorize** once (token stored at
   `<user config dir>/mcp-searchconsole/token.json`, mode 0600):
   ```
   ./mcp-searchconsole login
   ```
   or call the `gsc_login` tool from your client, then `gsc_login_status`.

4. **Register the MCP server** in your client:

   ```json
   {
     "mcpServers": {
       "searchconsole": {
         "command": "/path/to/go/bin/mcp-searchconsole",
         "env": {
           "GSC_MCP_CLIENT_ID": "xxxxx.apps.googleusercontent.com",
           "GSC_MCP_CLIENT_SECRET": "GOCSPX-..."
         }
       }
     }
   }
   ```

## Example asks

- *"SEO analysis of example.com for the last 30 days"* → the `gsc_seo_analysis` prompt,
  or `gsc_search_analytics { "siteUrl": "example.com", "dimensions": ["date"] }`
  then `["query"]`, `["page"]`, `["device"]`, `["country"]`, `["query","page"]`.
- *"Top 200 queries as CSV"* →
  `gsc_search_analytics { "dimensions": ["query"], "rowLimit": 200, "saveToPath": "gsc-queries.csv" }`
- *"Which queries lead to /abc?"* →
  `gsc_search_analytics { "dimensions": ["query"], "filters": [{ "dimension": "page", "expression": "https://www.example.com/abc", "operator": "equals" }] }`
- *"Is this page indexed?"* →
  `gsc_inspect_url { "inspectionUrl": "https://www.example.com/post/123" }`
- *"Are my top 10 pages indexed?"* → `gsc_search_analytics { "dimensions": ["page"], "rowLimit": 10 }`
  then `gsc_inspect_urls { "urls": [...] }`
- *"Page-two queries worth optimizing"* →
  `gsc_search_analytics { "dimensions": ["query", "page"], "rowLimit": 5000, "minPosition": 11, "maxPosition": 20, "minImpressions": 100, "sortBy": "impressions" }`
- *"Compare the last 30 days with the 30 days before"* →
  `gsc_period_comparison { "dateRange": "LAST_30_DAYS", "dimensions": ["query"] }`
- *"This October vs last October"* →
  `gsc_period_comparison { "startDate": "2026-10-01", "endDate": "2026-10-31", "baselineStartDate": "2025-10-01", "baselineEndDate": "2025-10-31" }`
- *"Quick overall SEO picture"* →
  `gsc_quick_report { "dateRange": "MONTH_TO_DATE" }`
- *"Type the site URL any way you like"* — `example.com` resolves against
  `https://www.example.com/`, `https://example.com/` or `sc-domain:example.com`;
  `gsc_list_sites` returns the exact values.

### Input validation (client-side, before spending quota)

- `type` normalized (`googlenews` → `googleNews`) and validated; unknown values error immediately.
- `dataState` validated (all / final / hourly_all); requesting the `hour`
  dimension auto-selects `dataState: all` (hourly rows only exist there).
- Dimensions validated (no unknown names, no duplicates); filter dimensions,
  operators and empty expressions checked locally.
- `gsc_inspect_url` / `gsc_inspect_urls` reject URLs whose host doesn't belong to the
  property *before* consuming any of the ~2,000 daily inspection queries;
  `gsc_inspect_urls` also drops duplicates and caps a call at 10 URLs.
- `sortBy`, `sortOrder` and the position range are checked before the API call.
- Report tables show totals of the returned rows and warn when exactly
  `rowLimit` rows came back (likely truncated).

## Robustness

- Retry (3 attempts, backoff, honors `Retry-After`) on 429/5xx and network
  errors for both GET and the JSON POSTs (search analytics, URL inspection —
  both are read-only queries); 4xx never retried; 90s HTTP timeout. The POST
  body is re-serialized on every attempt (a consumed reader would send an
  empty body on retry — regression-tested).
- Property URLs are percent-encoded for path segments
  (`https://www.example.com/` → `https:%2F%2Fwww.example.com%2F`).
- `siteUrl` accepts loose input and resolves against your real properties;
  ambiguous input returns the available list instead of guessing.
- Date presets (TODAY / YESTERDAY / LAST_7_DAYS / LAST_30_DAYS /
  MONTH_TO_DATE / YEAR_TO_DATE / LAST_MONTH) expand to the API's explicit
  `startDate`/`endDate`; note data lags ~2 days, so "TODAY" is often partial.
- Markdown tables escape `|` inside query/page values; CSV export quotes cells.

## Development

```
go build ./mcp-searchconsole     # from the repo root
go test ./mcp-searchconsole      # site resolution, URL encoding, query body building,
                                 # date presets, rendering/CSV escaping, decoders
```

## Notes

- The server requests the `webmasters` scope: it can add/remove properties and
  sitemaps, nothing else. Tokens from versions before the write tools are
  read-only: write tools then fail with "insufficient authentication scopes" —
  run `mcp-searchconsole logout` and `login` again.
- Tokens live at `<user config dir>/mcp-searchconsole/token.json`
  (macOS: `~/Library/Application Support/mcp-searchconsole/token.json`).
- Revoke access any time at <https://myaccount.google.com/permissions>.
